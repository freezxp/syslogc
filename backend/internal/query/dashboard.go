package query

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/metadata"
	"github.com/freezxp/syslogc/backend/internal/storage"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

type DashboardRequest struct {
	TimeRange TimeRange `json:"time_range"`
	Field     string    `json:"field,omitempty"`
	Limit     int       `json:"limit,omitempty"`
}

type StorageUsage struct {
	CompressedBytes   int64 `json:"compressed_bytes"`
	UncompressedBytes int64 `json:"uncompressed_bytes"`
	FreeDiskBytes     int64 `json:"free_disk_bytes"`
	TotalDiskBytes    int64 `json:"total_disk_bytes"`
}

type IngestRate struct {
	LogsPerSecond  float64 `json:"logs_per_second"`
	BytesPerSecond float64 `json:"bytes_per_second"`
	WindowSeconds  int64   `json:"window_seconds"`
}

type DashboardOverview struct {
	ResolvedRange ResolvedRange `json:"resolved_range"`
	LogsInRange   int64         `json:"logs_in_range"`
	LogsToday     int64         `json:"logs_today"`
	ErrorsInRange int64         `json:"errors_in_range"`
	ActiveSources int64         `json:"active_sources"`
	Storage       *StorageUsage `json:"storage,omitempty"`
	IngestRate    *IngestRate   `json:"ingest_rate,omitempty"`
	CachedAt      time.Time     `json:"cached_at"`
}

func (s *Service) cacheTTL(r storage.TimeRange) time.Duration {
	if r.End.Sub(r.Start) <= time.Hour {
		return 15 * time.Second
	}
	return 60 * time.Second
}

func (s *Service) dashboardSelection(p *auth.Principal, tr TimeRange) (*resolved, error) {
	if !p.Can(auth.PermDashboardView) {
		return nil, fmt.Errorf("%w: requires %s", ErrForbidden, auth.PermDashboardView)
	}
	return s.resolve(p, Selection{TimeRange: tr})
}

// Overview computes the dashboard tiles in parallel.
func (s *Service) Overview(ctx context.Context, p *auth.Principal, req DashboardRequest) (*DashboardOverview, error) {
	r, err := s.dashboardSelection(p, req.TimeRange)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("overview|%s|%s|%s|%s", p.Tenant, req.TimeRange.From, req.TimeRange.To, req.TimeRange.TZ)
	now := s.opts.Now()
	if v, ok := s.cache.get(key, now); ok {
		return v.(*DashboardOverview), nil
	}
	ctx, release, err := s.acquire(ctx, p, r.limits)
	if err != nil {
		return nil, err
	}
	defer release()

	nowLocal := now.In(r.loc)
	today := r.sel
	today.Range = storage.TimeRange{Start: time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, r.loc).UTC(), End: now.UTC()}
	errorsSel := r.sel
	errorsSel.Filter = &filter.Expr{Op: filter.Lte, Field: "severity_code", Value: "3"}

	out := &DashboardOverview{ResolvedRange: ResolvedRange{r.sel.Range.Start, r.sel.Range.End}, CachedAt: now.UTC()}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	run := func(fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	run(func() (err error) {
		out.LogsInRange, err = s.opts.Querier.Count(ctx, storage.CountQuery{Selection: r.sel})
		return err
	})
	run(func() (err error) {
		out.LogsToday, err = s.opts.Querier.Count(ctx, storage.CountQuery{Selection: today})
		return err
	})
	run(func() (err error) {
		out.ErrorsInRange, err = s.opts.Querier.Count(ctx, storage.CountQuery{Selection: errorsSel})
		return err
	})
	run(func() (err error) {
		out.ActiveSources, err = s.opts.Querier.Count(ctx, storage.CountQuery{Selection: r.sel, DistinctField: "hostname"})
		return err
	})
	if s.opts.Admin != nil {
		run(func() error {
			u, err := s.opts.Admin.Usage(ctx)
			if err == nil {
				out.Storage = &StorageUsage{u.CompressedBytes, u.UncompressedBytes, u.FreeDiskBytes, u.TotalDiskBytes}
			}
			return nil // usage is optional
		})
	}
	if s.opts.NodeStats != nil {
		run(func() error {
			rate, err := s.currentRate(ctx, now)
			if err == nil {
				out.IngestRate = rate
			}
			return nil
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, mapErr(ctx, err)
	}
	s.cache.set(key, out, s.cacheTTL(r.sel.Range), now)
	return out, nil
}

// Volume is the severity-split histogram for the dashboard.
func (s *Service) Volume(ctx context.Context, p *auth.Principal, req DashboardRequest) (*HistogramResponse, error) {
	r, err := s.dashboardSelection(p, req.TimeRange)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("volume|%s|%s|%s|%s", p.Tenant, req.TimeRange.From, req.TimeRange.To, req.TimeRange.TZ)
	now := s.opts.Now()
	if v, ok := s.cache.get(key, now); ok {
		return v.(*HistogramResponse), nil
	}
	ctx, release, err := s.acquire(ctx, p, r.limits)
	if err != nil {
		return nil, err
	}
	defer release()
	split := "severity"
	resp, err := s.histogram(ctx, r, &split, 120, 8)
	if err != nil {
		return nil, err
	}
	s.cache.set(key, resp, s.cacheTTL(r.sel.Range), now)
	return resp, nil
}

// dashboardTopFields are the fields /dashboard/top accepts.
var dashboardTopFields = map[string]bool{"hostname": true, "app_name": true, "source_ip": true, "facility": true, "severity": true, "format": true, "source": true}

// Top returns the top values of a core field for the dashboard.
func (s *Service) Top(ctx context.Context, p *auth.Principal, req DashboardRequest) (*FieldValuesResponse, error) {
	if !dashboardTopFields[req.Field] {
		return nil, &InputError{Code: "validation_failed", Pointer: "/field", Message: "unsupported field"}
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		return nil, &InputError{Code: "validation_failed", Pointer: "/limit", Message: "limit must be at most 50"}
	}
	r, err := s.dashboardSelection(p, req.TimeRange)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("top|%s|%s|%s|%s|%s|%d", p.Tenant, req.TimeRange.From, req.TimeRange.To, req.TimeRange.TZ, req.Field, limit)
	now := s.opts.Now()
	if v, ok := s.cache.get(key, now); ok {
		return v.(*FieldValuesResponse), nil
	}
	ctx, release, err := s.acquire(ctx, p, r.limits)
	if err != nil {
		return nil, err
	}
	defer release()
	vals, err := s.opts.Querier.Top(ctx, storage.TopQuery{Selection: r.sel, Field: req.Field, Limit: limit})
	if err != nil {
		return nil, mapErr(ctx, err)
	}
	resp := &FieldValuesResponse{ResolvedRange: &ResolvedRange{r.sel.Range.Start, r.sel.Range.End}, Field: req.Field, Values: toValueCounts(vals)}
	s.cache.set(key, resp, s.cacheTTL(r.sel.Range), now)
	return resp, nil
}

type RatePoint struct {
	T                    time.Time `json:"t"`
	ReceivedPerSecond    float64   `json:"received_per_second"`
	StoredPerSecond      float64   `json:"stored_per_second"`
	DroppedPerSecond     float64   `json:"dropped_per_second"`
	ParseErrorsPerSecond float64   `json:"parse_errors_per_second"`
}

type IngestionRateResponse struct {
	ResolvedRange ResolvedRange `json:"resolved_range"`
	StepSeconds   int64         `json:"step_seconds"`
	Series        []RatePoint   `json:"series"`
}

// maxRateWindow is how much node statistics history is kept.
const maxRateWindow = 24 * time.Hour

// IngestionRate derives per-second rates from node snapshots, summed across nodes.
func (s *Service) IngestionRate(ctx context.Context, p *auth.Principal, req DashboardRequest) (*IngestionRateResponse, error) {
	r, err := s.dashboardSelection(p, req.TimeRange)
	if err != nil {
		return nil, err
	}
	rng := r.sel.Range
	if rng.End.Sub(rng.Start) > maxRateWindow {
		rng.Start = rng.End.Add(-maxRateWindow)
	}
	resp := &IngestionRateResponse{ResolvedRange: ResolvedRange{rng.Start, rng.End}, Series: []RatePoint{}}
	if s.opts.NodeStats == nil {
		return resp, nil
	}
	step := chooseStep(rng, 120)
	if step < 10*time.Second {
		step = 10 * time.Second
	}
	resp.StepSeconds = int64(step / time.Second)
	snaps, err := s.opts.NodeStats.NodeStatsSince(ctx, rng.Start.Add(-step))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
	}
	type acc struct{ received, stored, dropped, parseErrors float64 }
	buckets := map[int64]*acc{}
	byNode := map[string][]metadata.NodeStats{}
	for _, sn := range snaps {
		byNode[sn.NodeID] = append(byNode[sn.NodeID], sn)
	}
	for _, list := range byNode {
		for i := 1; i < len(list); i++ {
			prev, cur := list[i-1], list[i]
			if cur.Time.Before(rng.Start) || !cur.Time.Before(rng.End) {
				continue
			}
			// Counter resets (node restart) produce negative deltas; skip them.
			if cur.Received < prev.Received {
				continue
			}
			k := cur.Time.Truncate(step).UnixNano()
			a := buckets[k]
			if a == nil {
				a = &acc{}
				buckets[k] = a
			}
			a.received += float64(cur.Received - prev.Received)
			a.stored += float64(cur.Stored - prev.Stored)
			a.dropped += float64(cur.Dropped - prev.Dropped)
			a.parseErrors += float64(cur.ParseErrors - prev.ParseErrors)
		}
	}
	keys := make([]int64, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	secs := step.Seconds()
	for _, k := range keys {
		a := buckets[k]
		resp.Series = append(resp.Series, RatePoint{
			T: time.Unix(0, k).UTC(), ReceivedPerSecond: a.received / secs, StoredPerSecond: a.stored / secs,
			DroppedPerSecond: a.dropped / secs, ParseErrorsPerSecond: a.parseErrors / secs,
		})
	}
	return resp, nil
}

// currentRate returns the ingest rate over the last minute across all nodes.
func (s *Service) currentRate(ctx context.Context, now time.Time) (*IngestRate, error) {
	snaps, err := s.opts.NodeStats.NodeStatsSince(ctx, now.Add(-90*time.Second))
	if err != nil {
		return nil, err
	}
	first := map[string]metadata.NodeStats{}
	last := map[string]metadata.NodeStats{}
	for _, sn := range snaps {
		if _, ok := first[sn.NodeID]; !ok {
			first[sn.NodeID] = sn
		}
		last[sn.NodeID] = sn
	}
	rate := &IngestRate{}
	for node, f := range first {
		l := last[node]
		secs := l.Time.Sub(f.Time).Seconds()
		if secs <= 0 || l.Received < f.Received || now.Sub(l.Time) > 60*time.Second {
			continue
		}
		rate.LogsPerSecond += float64(l.Received-f.Received) / secs
		rate.BytesPerSecond += float64(l.BytesReceived-f.BytesReceived) / secs
		rate.WindowSeconds = max(rate.WindowSeconds, int64(secs))
	}
	return rate, nil
}
