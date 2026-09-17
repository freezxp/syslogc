package query

import (
	"context"
	"sort"
	"time"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/storage"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

// maxAnalyticsGroups bounds how many groups a single analytics query returns,
// so a high-cardinality field cannot produce an enormous response.
const maxAnalyticsGroups = 50

// Metric selects what an analytics query measures.
type Metric struct {
	// Type is "count" or "count_distinct".
	Type string `json:"type"`
	// Field is the field whose distinct values are counted.
	Field string `json:"field,omitempty"`
}

func (m Metric) storage() (metric, field string) {
	if m.Type == "count_distinct" {
		return storage.MetricDistinct, m.Field
	}
	return storage.MetricCount, ""
}

func (m Metric) validate() error {
	switch m.Type {
	case "", "count":
		return nil
	case "count_distinct":
		if m.Field == "" {
			return &InputError{Code: "validation_failed", Pointer: "/metric/field", Message: "count_distinct needs a field"}
		}
		return validateFieldName("/metric/field", m.Field)
	default:
		return &InputError{Code: "validation_failed", Pointer: "/metric/type", Message: "metric type must be count or count_distinct"}
	}
}

func validateFieldName(pointer, name string) error {
	if err := filter.ValidateField(name); err != nil {
		return &InputError{Code: "validation_failed", Pointer: pointer, Message: err.Error()}
	}
	return nil
}

type BreakdownRequest struct {
	Selection
	GroupBy string `json:"group_by"`
	Metric  Metric `json:"metric"`
	Limit   int    `json:"limit,omitempty"`
}

// BreakdownRow is one group of a breakdown, highest value first.
type BreakdownRow struct {
	Value  string  `json:"value"`
	Metric float64 `json:"metric"`
	// Share is this group's fraction of Total, 0 when Total is unknown.
	Share float64 `json:"share"`
}

type BreakdownResponse struct {
	ResolvedRange ResolvedRange  `json:"resolved_range"`
	GroupBy       string         `json:"group_by"`
	Metric        Metric         `json:"metric"`
	Rows          []BreakdownRow `json:"rows"`
	// Total is the metric over everything matching, including groups beyond
	// the limit, so shares are honest.
	Total float64 `json:"total"`
	// DistinctGroups is how many groups exist in total.
	DistinctGroups int64       `json:"distinct_groups"`
	Stats          SearchStats `json:"stats"`
}

// Breakdown ranks the values of one field by a metric.
func (s *Service) Breakdown(ctx context.Context, p *auth.Principal, req BreakdownRequest) (*BreakdownResponse, error) {
	start := s.opts.Now()
	r, err := s.resolve(p, req.Selection)
	if err != nil {
		return nil, err
	}
	if req.GroupBy == "" {
		return nil, &InputError{Code: "validation_failed", Pointer: "/group_by", Message: "group_by is required"}
	}
	if err := validateFieldName("/group_by", req.GroupBy); err != nil {
		return nil, err
	}
	if err := req.Metric.validate(); err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > maxAnalyticsGroups {
		return nil, &InputError{Code: "validation_failed", Pointer: "/limit",
			Message: "limit must be at most 50"}
	}

	ctx, release, err := s.acquire(ctx, p, r.limits)
	if err != nil {
		return nil, err
	}
	defer release()

	metric, metricField := req.Metric.storage()
	rows, err := s.opts.Querier.Aggregate(ctx, storage.AggregateQuery{
		Selection: r.sel, GroupBy: req.GroupBy, Metric: metric, MetricField: metricField, Limit: limit,
	})
	if err != nil {
		return nil, mapErr(ctx, err)
	}
	resp := &BreakdownResponse{
		ResolvedRange: ResolvedRange{r.sel.Range.Start, r.sel.Range.End},
		GroupBy:       req.GroupBy, Metric: req.Metric, Rows: []BreakdownRow{},
	}
	for _, row := range rows {
		resp.Rows = append(resp.Rows, BreakdownRow{Value: row.Group, Metric: row.Value})
	}

	// Totals come from a separate query: summing the top groups would
	// understate the whole, and distinct counts do not add up at all.
	total, err := s.opts.Querier.Count(ctx, storage.CountQuery{Selection: r.sel, DistinctField: metricField})
	if err != nil {
		return nil, mapErr(ctx, err)
	}
	resp.Total = float64(total)
	if resp.Total > 0 {
		for i := range resp.Rows {
			resp.Rows[i].Share = resp.Rows[i].Metric / resp.Total
		}
	}
	groups, err := s.opts.Querier.Count(ctx, storage.CountQuery{Selection: r.sel, DistinctField: req.GroupBy})
	if err != nil {
		return nil, mapErr(ctx, err)
	}
	resp.DistinctGroups = groups
	resp.Stats.DurationMS = s.opts.Now().Sub(start).Milliseconds()
	return resp, nil
}

type SeriesRequest struct {
	Selection
	// GroupBy splits the series; empty returns a single series.
	GroupBy string `json:"group_by,omitempty"`
	Metric  Metric `json:"metric"`
	Buckets int    `json:"buckets,omitempty"`
	// Limit caps how many groups are charted, by total metric value.
	Limit int `json:"limit,omitempty"`
}

// SeriesGroup is one line of a time series; Points aligns with Timestamps.
type SeriesGroup struct {
	Value  string    `json:"value"`
	Total  float64   `json:"total"`
	Points []float64 `json:"points"`
}

type SeriesResponse struct {
	ResolvedRange ResolvedRange `json:"resolved_range"`
	Step          string        `json:"step"`
	StepSeconds   int64         `json:"step_seconds"`
	GroupBy       string        `json:"group_by,omitempty"`
	Metric        Metric        `json:"metric"`
	Timestamps    []time.Time   `json:"timestamps"`
	Groups        []SeriesGroup `json:"groups"`
	Stats         SearchStats   `json:"stats"`
}

// Series returns a metric over time, optionally one line per field value.
// With a group, the top values are charted and the rest are omitted (their
// share is visible in Breakdown).
func (s *Service) Series(ctx context.Context, p *auth.Principal, req SeriesRequest) (*SeriesResponse, error) {
	start := s.opts.Now()
	r, err := s.resolve(p, req.Selection)
	if err != nil {
		return nil, err
	}
	if err := req.Metric.validate(); err != nil {
		return nil, err
	}
	if req.GroupBy != "" {
		if err := validateFieldName("/group_by", req.GroupBy); err != nil {
			return nil, err
		}
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > maxAnalyticsGroups {
		return nil, &InputError{Code: "validation_failed", Pointer: "/limit", Message: "limit must be at most 50"}
	}
	buckets := req.Buckets
	if buckets <= 0 {
		buckets = 120
	}
	if buckets > 1000 {
		return nil, &InputError{Code: "validation_failed", Pointer: "/buckets", Message: "buckets must be at most 1000"}
	}
	step := chooseStep(r.sel.Range, buckets)

	ctx, release, err := s.acquire(ctx, p, r.limits)
	if err != nil {
		return nil, err
	}
	defer release()

	metric, metricField := req.Metric.storage()
	sel := r.sel

	// Charting every value of a high-cardinality field is neither useful nor
	// affordable, so the top groups are chosen first and the series query is
	// restricted to them.
	var wanted []string
	totals := map[string]float64{}
	if req.GroupBy != "" {
		top, err := s.opts.Querier.Aggregate(ctx, storage.AggregateQuery{
			Selection: sel, GroupBy: req.GroupBy, Metric: metric, MetricField: metricField, Limit: limit,
		})
		if err != nil {
			return nil, mapErr(ctx, err)
		}
		for _, row := range top {
			wanted = append(wanted, row.Group)
			totals[row.Group] = row.Value
		}
		if len(wanted) == 0 {
			return emptySeries(r, step, req), nil
		}
		sel.Filter = filter.AndAll(sel.Filter, &filter.Expr{Op: filter.In, Field: req.GroupBy, Values: wanted})
	}

	rows, err := s.opts.Querier.Aggregate(ctx, storage.AggregateQuery{
		Selection: sel, Step: step, GroupBy: req.GroupBy, Metric: metric, MetricField: metricField,
	})
	if err != nil {
		return nil, mapErr(ctx, err)
	}

	resp := emptySeries(r, step, req)
	// Buckets are aligned to the step, so the first one usually starts before
	// the range. It is kept: it holds only rows inside the range (the query is
	// bounded), and dropping it would hide up to a step's worth of data and
	// make the points disagree with the totals. The histogram does the same.
	index := map[int64]int{}
	for t := r.sel.Range.Start.Truncate(step); t.Before(r.sel.Range.End); t = t.Add(step) {
		index[t.UnixNano()] = len(resp.Timestamps)
		resp.Timestamps = append(resp.Timestamps, t.UTC())
	}
	series := map[string]*SeriesGroup{}
	for _, row := range rows {
		i, ok := index[row.Time.Truncate(step).UnixNano()]
		if !ok {
			continue
		}
		g := series[row.Group]
		if g == nil {
			g = &SeriesGroup{Value: row.Group, Points: make([]float64, len(resp.Timestamps))}
			series[row.Group] = g
		}
		g.Points[i] += row.Value
	}
	// Totals are measured over the whole range: summing buckets would count
	// the same value twice whenever it appears in more than one of them.
	if req.GroupBy == "" {
		total, err := s.opts.Querier.Count(ctx, storage.CountQuery{Selection: sel, DistinctField: metricField})
		if err != nil {
			return nil, mapErr(ctx, err)
		}
		totals[""] = float64(total)
	}
	for _, g := range series {
		g.Total = totals[g.Value]
		resp.Groups = append(resp.Groups, *g)
	}
	sort.Slice(resp.Groups, func(i, j int) bool { return resp.Groups[i].Total > resp.Groups[j].Total })
	resp.Stats.DurationMS = s.opts.Now().Sub(start).Milliseconds()
	return resp, nil
}

func emptySeries(r *resolved, step time.Duration, req SeriesRequest) *SeriesResponse {
	return &SeriesResponse{
		ResolvedRange: ResolvedRange{r.sel.Range.Start, r.sel.Range.End},
		Step:          formatStep(step), StepSeconds: int64(step / time.Second),
		GroupBy: req.GroupBy, Metric: req.Metric,
		Timestamps: []time.Time{}, Groups: []SeriesGroup{},
	}
}
