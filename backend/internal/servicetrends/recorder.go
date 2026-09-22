package servicetrends

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/freezxp/syslogc/backend/internal/metricstore"
	"github.com/freezxp/syslogc/backend/internal/storage"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

// Metric names written to the metrics store.
const (
	MetricUniqueClients = "syslogc_dns_service_unique_clients"
	MetricQueries       = "syslogc_dns_service_queries"
)

// Resolutions recorded. Each is counted over its own window, because
// distinct counts cannot be summed from a finer one.
var (
	// ResolutionHour and ResolutionDay are recorded alongside the configured
	// base resolution (5m by default).
	ResolutionHour = time.Hour
	ResolutionDay  = 24 * time.Hour
)

// Scopes a count can be recorded under.
const (
	// ScopeAll counts every domain a service owns, including the CDNs and
	// APIs its apps query in the background.
	ScopeAll = "all"
	// ScopeMain counts only the domains somebody reaches the service at, so
	// it reads as "who opened it" rather than "whose device talked to it".
	ScopeMain = "main"
)

// maxBucketsPerQuery bounds how many buckets one rollup query covers, so a
// long backfill is done in steps instead of one enormous request.
const maxBucketsPerQuery = 288

// Querier is the part of the log store the recorder needs.
type Querier interface {
	CategoryCounts(ctx context.Context, q storage.CategoryQuery) ([]storage.CategoryRow, error)
}

// Writer stores samples.
type Writer interface {
	Write(ctx context.Context, samples []metricstore.Sample) error
}

// State is how far each resolution has been recorded. It is persisted by the
// caller so a restart does not redo work, and so a gap is filled in rather
// than skipped.
type State map[string]time.Time

// Options configure a Recorder.
type Options struct {
	Querier Querier
	Writer  Writer
	Log     *slog.Logger

	// Catalog returns the current catalog. It is called per run so edits take
	// effect without a restart.
	Catalog func(ctx context.Context) (Catalog, error)
	// LoadState and SaveState persist how far each resolution has been
	// recorded.
	LoadState func(ctx context.Context) (State, error)
	SaveState func(ctx context.Context, s State) error

	// Base is the finest resolution recorded (default 5m).
	Base time.Duration
	// Backfill is how far back to start when a resolution has no state.
	Backfill time.Duration
	// Tenant selects which tenant's logs are counted.
	Tenant string
	// DomainField and ClientField name the fields to read.
	DomainField string
	ClientField string
	// Sources restricts the rollup to these source names; empty reads all.
	Sources []string
	// Now is the clock, for tests.
	Now func() time.Time
}

// Recorder rolls log data up into the metrics store.
type Recorder struct {
	opts Options
}

// NewRecorder validates opts.
func NewRecorder(opts Options) (*Recorder, error) {
	switch {
	case opts.Querier == nil:
		return nil, fmt.Errorf("servicetrends: a querier is required")
	case opts.Writer == nil:
		return nil, fmt.Errorf("servicetrends: a writer is required")
	case opts.Catalog == nil:
		return nil, fmt.Errorf("servicetrends: a catalog source is required")
	case opts.DomainField == "" || opts.ClientField == "":
		return nil, fmt.Errorf("servicetrends: the domain and client fields are required")
	}
	if opts.Base <= 0 {
		opts.Base = 5 * time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Recorder{opts: opts}, nil
}

// Resolutions are the windows this recorder stores, finest first.
func (r *Recorder) Resolutions() []time.Duration {
	return []time.Duration{r.opts.Base, ResolutionHour, ResolutionDay}
}

// Run records every complete window that has not been recorded yet, for
// every resolution, and returns how many samples were written.
//
// It is safe to call at any time and as often as wanted: windows are aligned
// to the resolution, only complete windows are recorded, and writing a sample
// twice replaces it rather than adding to it.
func (r *Recorder) Run(ctx context.Context) (int, error) {
	catalog, err := r.opts.Catalog(ctx)
	if err != nil {
		return 0, fmt.Errorf("reading the service catalog: %w", err)
	}
	categories := catalog.Categories(r.opts.DomainField)
	if len(categories) == 0 {
		return 0, nil
	}

	state := State{}
	if r.opts.LoadState != nil {
		state, err = r.opts.LoadState(ctx)
		if err != nil {
			return 0, fmt.Errorf("reading the rollup state: %w", err)
		}
		if state == nil {
			state = State{}
		}
	}

	// The main-domain counts are a second pass: a service is one category
	// per scope, and both scopes together would not fit in one query.
	mainCategories := catalog.MainCategories(r.opts.DomainField)

	now := r.opts.Now().UTC()
	written := 0
	for _, step := range r.Resolutions() {
		from := state[WindowName(step)]
		n, upto, err := r.record(ctx, categories, ScopeAll, step, from, now)
		written += n
		if err != nil {
			return written, err
		}
		if len(mainCategories) > 0 {
			n, _, err := r.record(ctx, mainCategories, ScopeMain, step, from, now)
			written += n
			if err != nil {
				return written, err
			}
		}
		if !upto.IsZero() {
			state[WindowName(step)] = upto
		}
	}
	if r.opts.SaveState != nil && written > 0 {
		if err := r.opts.SaveState(ctx, state); err != nil {
			return written, fmt.Errorf("saving the rollup state: %w", err)
		}
	}
	return written, nil
}

// record writes every complete window of one resolution between `from` and
// now, and reports the end of the last window it recorded.
func (r *Recorder) record(ctx context.Context, categories []storage.Category, scope string, step time.Duration,
	from, now time.Time) (int, time.Time, error) {
	// Only windows that have fully elapsed are counted; a partial window
	// would be recorded as if it were complete and stay wrong forever.
	end := now.Truncate(step)
	if from.IsZero() {
		if r.opts.Backfill <= 0 {
			// Nothing recorded yet and no backfill: start at the window that
			// is about to complete.
			return 0, end.Add(-step), nil
		}
		from = now.Add(-r.opts.Backfill).Truncate(step)
	}
	from = from.Truncate(step)
	if !from.Before(end) {
		return 0, time.Time{}, nil
	}

	written := 0
	var recorded time.Time
	for start := from; start.Before(end); {
		stop := start.Add(step * maxBucketsPerQuery)
		if stop.After(end) {
			stop = end
		}
		rows, err := r.opts.Querier.CategoryCounts(ctx, storage.CategoryQuery{
			Selection: storage.Selection{
				Tenant: r.opts.Tenant,
				Range:  storage.TimeRange{Start: start, End: stop},
				Filter: r.sourceFilter(),
			},
			Categories:    categories,
			DistinctField: r.opts.ClientField,
			Step:          step,
		})
		if err != nil {
			return written, recorded, fmt.Errorf("counting %s windows from %s: %w",
				WindowName(step), start.Format(time.RFC3339), err)
		}
		samples := make([]metricstore.Sample, 0, len(rows)*2)
		window := WindowName(step)
		for _, row := range rows {
			// A bucket can start before the requested range when the range
			// does not align; those belong to an already recorded window.
			if row.Time.Before(start) || !row.Time.Before(stop) {
				continue
			}
			labels := map[string]string{"service": row.Category, "window": window, "scope": scope}
			if r.opts.Tenant != "" {
				labels["tenant"] = r.opts.Tenant
			}
			samples = append(samples,
				metricstore.Sample{Name: MetricUniqueClients, Labels: labels, Value: row.Distinct, At: row.Time},
				metricstore.Sample{Name: MetricQueries, Labels: labels, Value: row.Messages, At: row.Time},
			)
		}
		// One query can produce more samples than a single write accepts —
		// 288 buckets of a full catalog is far past the limit — so the
		// write is split rather than rejected.
		for len(samples) > 0 {
			n := min(len(samples), metricstore.MaxSamplesPerWrite)
			if err := r.opts.Writer.Write(ctx, samples[:n]); err != nil {
				return written, recorded, fmt.Errorf("writing %s samples: %w", window, err)
			}
			written += n
			samples = samples[n:]
		}
		recorded = stop
		start = stop
	}
	return written, recorded, nil
}

// sourceFilter restricts the rollup to the configured sources.
func (r *Recorder) sourceFilter() *filter.Expr {
	if len(r.opts.Sources) == 0 {
		return nil
	}
	return &filter.Expr{Op: filter.In, Field: "source", Values: r.opts.Sources}
}

// WindowName is the label a resolution is stored under.
func WindowName(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}
