package servicetrends

import (
	"context"
	"errors"
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

// Limits on how much one rollup query covers.
const (
	// maxBucketsPerQuery bounds how many buckets one query returns, so a
	// long backfill is done in steps instead of one enormous request.
	maxBucketsPerQuery = 288
	// maxShrinkAttempts bounds how often one run halves its query size
	// before accepting that the window cannot be recorded, so a query that
	// fails for some other reason is not retried all the way down.
	maxShrinkAttempts = 6
	// maxQuerySpan bounds how much *time* one query scans, which is what
	// actually costs: 288 five-minute buckets is a day of logs, and on a
	// deployment counting hundreds of thousands of clients a minute that
	// cannot finish. A window longer than this is still queried whole,
	// because a bucket cannot be split.
	maxQuerySpan = 2 * time.Hour
)

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
	// QueryTimeout bounds one rollup query (default 2m). A window that
	// cannot be counted within it is skipped and retried later.
	QueryTimeout time.Duration
	// Now is the clock, for tests.
	Now func() time.Time
}

// Recorder rolls log data up into the metrics store.
type Recorder struct {
	opts Options
	// retryAfter holds back resolutions that failed, so one that cannot be
	// counted does not consume every run. It is in memory on purpose: a
	// restart is a reason to try again.
	retryAfter map[string]time.Time
	// buckets is how much of each window a single query covers, learned from
	// what this deployment's storage can actually answer in time.
	buckets map[string]int
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
	if opts.QueryTimeout <= 0 {
		opts.QueryTimeout = 2 * time.Minute
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
	var errs []error
	for _, step := range r.Resolutions() {
		window := WindowName(step)
		// A resolution that keeps failing is retried on its own schedule
		// rather than on every run: a daily window that cannot be counted
		// must not spend the rollup's time every five minutes.
		if until, waiting := r.retryAfter[window]; waiting && now.Before(until) {
			continue
		}
		from := state[window]

		n, upto, err := r.record(ctx, categories, ScopeAll, step, from, now)
		written += n
		errAll := err
		if len(mainCategories) > 0 {
			n, uptoMain, err := r.record(ctx, mainCategories, ScopeMain, step, from, now)
			written += n
			errs = appendNonNil(errs, wrapWindow(window, err))
			// Both scopes must have covered a window before it counts as
			// recorded, or the one that fell behind would never catch up.
			upto = earliest(upto, uptoMain)
			if err != nil {
				r.backOff(window, step, now, err)
			}
		}
		errs = appendNonNil(errs, wrapWindow(window, errAll))
		if errAll != nil {
			r.backOff(window, step, now, errAll)
		} else if len(mainCategories) == 0 {
			delete(r.retryAfter, window)
		}
		// Progress is kept even when a later window failed: the alternative
		// is starting from the same place next time, so the range to cover
		// grows with every run until nothing finishes at all.
		if !upto.IsZero() {
			state[window] = upto
		}
	}
	if r.opts.SaveState != nil && written > 0 {
		if err := r.opts.SaveState(ctx, state); err != nil {
			errs = append(errs, fmt.Errorf("saving the rollup state: %w", err))
		}
	}
	return written, errors.Join(errs...)
}

// backOff holds a failing resolution back until a quarter of its window has
// passed — a daily window is retried every six hours, a five-minute one on
// the next run.
func (r *Recorder) backOff(window string, step time.Duration, now time.Time, cause error) {
	if r.retryAfter == nil {
		r.retryAfter = map[string]time.Time{}
	}
	r.retryAfter[window] = now.Add(step / 4)
	r.opts.Log.Warn("a service trend window could not be recorded; holding it back",
		"window", window, "retrying_after", r.retryAfter[window].Format(time.RFC3339),
		"buckets_per_query", r.bucketsPerQuery(step), "error", cause)
}

func wrapWindow(window string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s windows: %w", window, err)
}

func appendNonNil(errs []error, err error) []error {
	if err == nil {
		return errs
	}
	return append(errs, err)
}

// earliest is the later-of-two progress markers both passes have reached; a
// pass that made none holds the pair back.
func earliest(a, b time.Time) time.Time {
	if a.IsZero() || b.IsZero() {
		return time.Time{}
	}
	if b.Before(a) {
		return b
	}
	return a
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
	buckets := r.bucketsPerQuery(step)
	shrinks := 0
	for start := from; start.Before(end); {
		stop := start.Add(step * time.Duration(buckets))
		if stop.After(end) {
			stop = end
		}
		// One query is bounded on its own, so a window that cannot be counted
		// inside the budget is given up on instead of taking the run with it.
		qctx, cancel := context.WithTimeout(ctx, r.opts.QueryTimeout)
		rows, err := r.opts.Querier.CategoryCounts(qctx, storage.CategoryQuery{
			Selection: storage.Selection{
				Tenant: r.opts.Tenant,
				Range:  storage.TimeRange{Start: start, End: stop},
				Filter: r.sourceFilter(),
			},
			Categories:    categories,
			DistinctField: r.opts.ClientField,
			Step:          step,
		})
		cancel()
		if err != nil {
			// A failed query is asking for less at a time, not for giving
			// up. The cause is not always a deadline of ours: the storage
			// enforces its own query limits and refuses with an error of its
			// own, which looks nothing like a timeout. Any failure is worth
			// one smaller attempt while there is still room to halve.
			if buckets > 1 && shrinks < maxShrinkAttempts {
				buckets /= 2
				shrinks++
				r.learnBuckets(step, buckets)
				r.opts.Log.Info("a service trend query failed; asking for less at a time",
					"window", WindowName(step), "buckets_per_query", buckets,
					"span", (step * time.Duration(buckets)).String(), "error", err.Error())
				continue
			}
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

// bucketsPerQuery is how many buckets to ask for at once: the size learned
// from earlier queries, or as much as the span limit allows.
func (r *Recorder) bucketsPerQuery(step time.Duration) int {
	if n, ok := r.buckets[WindowName(step)]; ok && n > 0 {
		return n
	}
	n := maxBucketsPerQuery
	if step > 0 {
		if bySpan := int(maxQuerySpan / step); bySpan < n {
			n = bySpan
		}
	}
	return max(n, 1)
}

// learnBuckets remembers a size that worked, so the next run starts there
// instead of timing its way down again.
func (r *Recorder) learnBuckets(step time.Duration, n int) {
	if r.buckets == nil {
		r.buckets = map[string]int{}
	}
	r.buckets[WindowName(step)] = max(n, 1)
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
