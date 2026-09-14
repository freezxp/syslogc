package query

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/metadata"
	"github.com/freezxp/syslogc/backend/internal/storage"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

// Errors mapped to HTTP problems by the API layer.
var (
	ErrForbidden          = errors.New("forbidden")
	ErrTooManyQueries     = errors.New("too many concurrent queries")
	ErrQueryTimeout       = errors.New("query timed out")
	ErrTooManyTails       = errors.New("too many live tail sessions")
	ErrStorageUnavailable = errors.New("storage unavailable")
)

// InputError is a client error with an optional JSON pointer.
type InputError struct {
	Code    string // invalid_time_range | validation_failed | query_invalid | invalid_cursor
	Pointer string
	Message string
}

func (e *InputError) Error() string { return e.Message }

// Limits are per-role query guards.
type Limits struct {
	MaxRange        time.Duration
	MaxLimit        int
	MaxExportRows   int
	Timeout         time.Duration
	MaxConcurrent   int
	MaxTailSessions int
}

// DefaultLimits follow docs/api.md §5.4. Admin range is bounded by retention.
func DefaultLimits(retention time.Duration) map[string]Limits {
	return map[string]Limits{
		auth.RoleViewer:   {MaxRange: 7 * 24 * time.Hour, MaxLimit: 1000, MaxExportRows: 100_000, Timeout: 30 * time.Second, MaxConcurrent: 4, MaxTailSessions: 2},
		auth.RoleOperator: {MaxRange: 31 * 24 * time.Hour, MaxLimit: 5000, MaxExportRows: 1_000_000, Timeout: 60 * time.Second, MaxConcurrent: 8, MaxTailSessions: 5},
		auth.RoleAdmin:    {MaxRange: max(retention, 31*24*time.Hour), MaxLimit: 10_000, MaxExportRows: 10_000_000, Timeout: 120 * time.Second, MaxConcurrent: 16, MaxTailSessions: 10},
	}
}

// NodeStatsSource provides ingestion snapshots for rate charts.
type NodeStatsSource interface {
	NodeStatsSince(ctx context.Context, since time.Time) ([]metadata.NodeStats, error)
}

// Options configures the service.
type Options struct {
	Querier      storage.LogQuerier
	Admin        storage.Admin
	NodeStats    NodeStatsSource
	CursorKey    []byte
	Limits       map[string]Limits
	MaxTieGroup  int
	MaxTailTotal int
	Log          *slog.Logger
	Now          func() time.Time
}

// Service implements queries on behalf of principals.
type Service struct {
	opts   Options
	cursor cursorCodec
	cache  *ttlCache

	mu        sync.Mutex
	inflight  map[string]int
	tails     map[string]int
	tailTotal int
}

func NewService(opts Options) *Service {
	if opts.MaxTieGroup <= 0 {
		opts.MaxTieGroup = 5000
	}
	if opts.MaxTailTotal <= 0 {
		opts.MaxTailTotal = 200
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{
		opts:     opts,
		cursor:   cursorCodec{key: opts.CursorKey},
		cache:    newTTLCache(512),
		inflight: map[string]int{},
		tails:    map[string]int{},
	}
}

// ---- request types ------------------------------------------------------

type NativeQuery struct {
	Dialect string `json:"dialect"`
	Text    string `json:"text"`
}

type Selection struct {
	TimeRange TimeRange    `json:"time_range"`
	Filter    *filter.Expr `json:"filter,omitempty"`
	Native    *NativeQuery `json:"native,omitempty"`
}

type resolved struct {
	sel    storage.Selection
	loc    *time.Location
	limits Limits
}

func (s *Service) limitsFor(p *auth.Principal) Limits {
	if l, ok := s.opts.Limits[p.Role]; ok {
		return l
	}
	return s.opts.Limits[auth.RoleViewer]
}

func callerKey(p *auth.Principal) string {
	if p.Kind == auth.KindAPIKey {
		return "key:" + p.APIKeyID.String()
	}
	return "user:" + p.UserID.String()
}

// resolve validates a selection for principal p.
func (s *Service) resolve(p *auth.Principal, in Selection) (*resolved, error) {
	rng, loc, err := Resolve(in.TimeRange, s.opts.Now())
	if err != nil {
		return nil, &InputError{Code: "invalid_time_range", Pointer: "/time_range", Message: err.Error()}
	}
	lim := s.limitsFor(p)
	if lim.MaxRange > 0 && rng.End.Sub(rng.Start) > lim.MaxRange {
		return nil, &InputError{Code: "invalid_time_range", Pointer: "/time_range",
			Message: fmt.Sprintf("time range exceeds the maximum of %s for your role", formatStep(lim.MaxRange))}
	}
	if err := filter.Validate(in.Filter); err != nil {
		var ve *filter.ValidationError
		if errors.As(err, &ve) {
			return nil, &InputError{Code: "validation_failed", Pointer: ve.Pointer, Message: ve.Message}
		}
		return nil, &InputError{Code: "validation_failed", Pointer: "/filter", Message: err.Error()}
	}
	sel := storage.Selection{Tenant: p.Tenant, Range: rng, Filter: in.Filter}
	if in.Native != nil && strings.TrimSpace(in.Native.Text) != "" {
		if !p.Can(auth.PermLogsQueryNative) {
			return nil, fmt.Errorf("%w: native queries require %s", ErrForbidden, auth.PermLogsQueryNative)
		}
		if in.Native.Dialect != "logsql" {
			return nil, &InputError{Code: "validation_failed", Pointer: "/native/dialect", Message: "unsupported dialect"}
		}
		if len(in.Native.Text) > 16384 {
			return nil, &InputError{Code: "validation_failed", Pointer: "/native/text", Message: "native query too long"}
		}
		sel.Native = &storage.NativeQuery{Dialect: in.Native.Dialect, Text: in.Native.Text}
	}
	return &resolved{sel: sel, loc: loc, limits: lim}, nil
}

// acquire takes a per-caller concurrency slot and applies the role timeout.
func (s *Service) acquire(ctx context.Context, p *auth.Principal, lim Limits) (context.Context, func(), error) {
	key := callerKey(p)
	s.mu.Lock()
	if lim.MaxConcurrent > 0 && s.inflight[key] >= lim.MaxConcurrent {
		s.mu.Unlock()
		return nil, nil, ErrTooManyQueries
	}
	s.inflight[key]++
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, lim.Timeout)
	return ctx, func() {
		cancel()
		s.mu.Lock()
		if s.inflight[key]--; s.inflight[key] <= 0 {
			delete(s.inflight, key)
		}
		s.mu.Unlock()
	}, nil
}

// mapErr converts storage/context errors to service errors.
func mapErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded {
		return ErrQueryTimeout
	}
	var qe *storage.QueryError
	if errors.As(err, &qe) {
		if qe.UserFacing {
			return &InputError{Code: "query_invalid", Pointer: "/native/text", Message: qe.Message}
		}
		return fmt.Errorf("%w: %s", ErrStorageUnavailable, qe.Message)
	}
	var ve *filter.ValidationError
	if errors.As(err, &ve) {
		return &InputError{Code: "validation_failed", Pointer: ve.Pointer, Message: ve.Message}
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	var ie *InputError
	if errors.As(err, &ie) {
		return err
	}
	if errors.Is(err, storage.ErrPipesNotAllowed) {
		return &InputError{Code: "query_invalid", Pointer: "/native/text", Message: "pipes (|) are not supported for this request"}
	}
	if strings.Contains(err.Error(), "IPv6") {
		return &InputError{Code: "validation_failed", Pointer: "/filter", Message: err.Error()}
	}
	return fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
}

// ---- search -------------------------------------------------------------

type SearchRequest struct {
	Selection
	Fields []string `json:"fields,omitempty"`
	Limit  int      `json:"limit,omitempty"`
	Cursor *string  `json:"cursor,omitempty"`
}

type Page struct {
	Returned    int     `json:"returned"`
	NextCursor  *string `json:"next_cursor"`
	TieOverflow bool    `json:"tie_overflow"`
}

type SearchStats struct {
	DurationMS int64 `json:"duration_ms"`
}

type SearchResponse struct {
	ResolvedRange  ResolvedRange       `json:"resolved_range"`
	Mode           string              `json:"mode"`
	Rows           []LogRow            `json:"rows,omitzero"`    // non-nil in logs mode
	Columns        []string            `json:"columns,omitzero"` // non-nil in table mode
	TableRows      []map[string]string `json:"table_rows,omitzero"`
	Page           *Page               `json:"page,omitempty"`
	NativeCompiled string              `json:"native_compiled,omitempty"`
	Stats          SearchStats         `json:"stats"`
}

// requiredSearchFields are always fetched for cursors and row references.
var requiredSearchFields = []string{"_time", "_stream_id"}

// Search returns a page of logs newest first. Pages end on timestamp
// boundaries; the whole group of rows sharing the last timestamp is
// included (ADR-0010).
func (s *Service) Search(ctx context.Context, p *auth.Principal, req SearchRequest) (*SearchResponse, error) {
	start := s.opts.Now()
	r, err := s.resolve(p, req.Selection)
	if err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > r.limits.MaxLimit {
		return nil, &InputError{Code: "validation_failed", Pointer: "/limit", Message: fmt.Sprintf("limit exceeds %d for your role", r.limits.MaxLimit)}
	}
	if len(req.Fields) > 100 {
		return nil, &InputError{Code: "validation_failed", Pointer: "/fields", Message: "too many fields"}
	}
	compiled, pipes, err := s.opts.Querier.Describe(r.sel)
	if err != nil {
		return nil, mapErr(ctx, err)
	}
	ctx, release, err := s.acquire(ctx, p, r.limits)
	if err != nil {
		return nil, err
	}
	defer release()

	resp := &SearchResponse{ResolvedRange: ResolvedRange{r.sel.Range.Start, r.sel.Range.End}, NativeCompiled: compiled}
	if pipes {
		if err := s.table(ctx, r, limit, resp); err != nil {
			return nil, mapErr(ctx, err)
		}
		resp.Stats.DurationMS = s.opts.Now().Sub(start).Milliseconds()
		return resp, nil
	}

	fields := req.Fields
	if len(fields) > 0 {
		fields = appendMissing(append([]string(nil), fields...), requiredSearchFields...)
	}
	// Bind cursors to the request as written (relative ranges re-resolve on
	// every page; the cursor's end bound keeps paging stable).
	qhash := hashQuery(p.Tenant, req.Filter, req.Native, req.Fields, req.TimeRange)
	sel := r.sel
	if req.Cursor != nil && *req.Cursor != "" {
		end, err := s.cursor.decode(*req.Cursor, qhash)
		if err != nil || !end.After(sel.Range.Start) || end.After(sel.Range.End) {
			return nil, &InputError{Code: "invalid_cursor", Pointer: "/cursor", Message: "cursor is invalid or does not belong to this query"}
		}
		sel.Range.End = end
	}

	page, err := s.collect(ctx, storage.SearchQuery{Selection: sel, Fields: fields, Limit: limit})
	if err != nil {
		return nil, mapErr(ctx, err)
	}
	info := &Page{Returned: len(page)}
	if len(page) == limit {
		last := page[len(page)-1].t
		tieSel := sel
		tieSel.Range = storage.TimeRange{Start: last, End: last.Add(time.Nanosecond)}
		tie, err := s.collect(ctx, storage.SearchQuery{Selection: tieSel, Fields: fields, Limit: s.opts.MaxTieGroup + 1})
		if err != nil {
			return nil, mapErr(ctx, err)
		}
		if len(tie) > s.opts.MaxTieGroup {
			tie, info.TieOverflow = tie[:s.opts.MaxTieGroup], true
		}
		cut := len(page)
		for cut > 0 && page[cut-1].t.Equal(last) {
			cut--
		}
		page = append(page[:cut], tie...)
		if last.After(sel.Range.Start) {
			c := s.cursor.encode(last, qhash)
			info.NextCursor = &c
		}
	}
	info.Returned = len(page)
	canRaw := p.Can(auth.PermLogsViewRaw)
	resp.Mode = "logs"
	resp.Rows = make([]LogRow, len(page))
	for i, pr := range page {
		resp.Rows[i] = shapeRow(pr.row, canRaw)
	}
	resp.Page = info
	resp.Stats.DurationMS = s.opts.Now().Sub(start).Milliseconds()
	return resp, nil
}

type pageRow struct {
	row storage.Row
	t   time.Time
}

func (s *Service) collect(ctx context.Context, q storage.SearchQuery) ([]pageRow, error) {
	rows, err := s.opts.Querier.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []pageRow
	for rows.Next() {
		row := append(storage.Row(nil), rows.Row()...)
		tv, _ := row.Get("_time")
		t, _ := time.Parse(time.RFC3339Nano, tv)
		out = append(out, pageRow{row: row, t: t})
	}
	return out, rows.Err()
}

func (s *Service) table(ctx context.Context, r *resolved, limit int, resp *SearchResponse) error {
	rows, err := s.opts.Querier.Table(ctx, storage.SearchQuery{Selection: r.sel, Limit: limit})
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	seen := map[string]bool{}
	resp.Mode = "table"
	resp.Columns = []string{}
	resp.TableRows = []map[string]string{}
	for rows.Next() {
		m := make(map[string]string, len(rows.Row()))
		for _, f := range rows.Row() {
			if !seen[f.Key] {
				seen[f.Key] = true
				resp.Columns = append(resp.Columns, f.Key)
			}
			m[f.Key] = f.Value
		}
		resp.TableRows = append(resp.TableRows, m)
	}
	return rows.Err()
}

// ---- histogram ------------------------------------------------------------

type HistogramRequest struct {
	Selection
	SplitBy    *string `json:"split_by,omitempty"`
	Buckets    int     `json:"buckets,omitempty"`
	SplitLimit int     `json:"split_limit,omitempty"`
}

type Bucket struct {
	T     time.Time        `json:"t"`
	Total int64            `json:"total"`
	Split map[string]int64 `json:"split,omitempty"`
}

type HistogramResponse struct {
	ResolvedRange ResolvedRange `json:"resolved_range"`
	Step          string        `json:"step"`
	StepSeconds   int64         `json:"step_seconds"`
	Total         int64         `json:"total"`
	SplitBy       *string       `json:"split_by"`
	SplitValues   []string      `json:"split_values,omitempty"`
	SplitOther    bool          `json:"split_other,omitempty"`
	Buckets       []Bucket      `json:"buckets"`
}

// Histogram returns log volume in human-aligned buckets.
func (s *Service) Histogram(ctx context.Context, p *auth.Principal, req HistogramRequest) (*HistogramResponse, error) {
	r, err := s.resolve(p, req.Selection)
	if err != nil {
		return nil, err
	}
	buckets := req.Buckets
	if buckets == 0 {
		buckets = 120
	}
	if buckets < 10 || buckets > 500 {
		return nil, &InputError{Code: "validation_failed", Pointer: "/buckets", Message: "buckets must be between 10 and 500"}
	}
	splitLimit := req.SplitLimit
	if splitLimit <= 0 {
		splitLimit = 8
	}
	if splitLimit > 20 {
		return nil, &InputError{Code: "validation_failed", Pointer: "/split_limit", Message: "split_limit must be at most 20"}
	}
	ctx, release, err := s.acquire(ctx, p, r.limits)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.histogram(ctx, r, req.SplitBy, buckets, splitLimit)
}

func (s *Service) histogram(ctx context.Context, r *resolved, splitBy *string, buckets, splitLimit int) (*HistogramResponse, error) {
	step := chooseStep(r.sel.Range, buckets)
	q := storage.HitsQuery{Selection: r.sel, Step: step}
	if step >= time.Hour {
		_, off := r.sel.Range.Start.In(r.loc).Zone()
		q.Offset = (time.Duration(off) * time.Second) % step
	}
	if splitBy != nil && *splitBy != "" {
		q.Field, q.FieldLimit = *splitBy, splitLimit
	}
	series, err := s.opts.Querier.Hits(ctx, q)
	if err != nil {
		return nil, mapErr(ctx, err)
	}
	resp := &HistogramResponse{
		ResolvedRange: ResolvedRange{r.sel.Range.Start, r.sel.Range.End},
		Step:          formatStep(step), StepSeconds: int64(step / time.Second),
		SplitBy: splitBy, Buckets: []Bucket{},
	}
	byTime := map[int64]*Bucket{}
	var order []int64
	type valTotal struct {
		v string
		n int64
	}
	var values []valTotal
	for _, sr := range series {
		name := sr.Value
		if sr.Other {
			name = "other"
			if q.Field != "" {
				resp.SplitOther = true
			}
		}
		if q.Field != "" {
			values = append(values, valTotal{name, sr.Total})
		}
		for i, ts := range sr.Timestamps {
			if !ts.Add(step).After(r.sel.Range.Start) || !ts.Before(r.sel.Range.End) || i >= len(sr.Counts) {
				continue
			}
			k := ts.UnixNano()
			b, ok := byTime[k]
			if !ok {
				b = &Bucket{T: ts.UTC()}
				byTime[k] = b
				order = append(order, k)
			}
			b.Total += sr.Counts[i]
			resp.Total += sr.Counts[i]
			if q.Field != "" && sr.Counts[i] > 0 {
				if b.Split == nil {
					b.Split = map[string]int64{}
				}
				b.Split[name] += sr.Counts[i]
			}
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	for _, k := range order {
		resp.Buckets = append(resp.Buckets, *byTime[k])
	}
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].v == "other" || values[j].v == "other" {
			return values[j].v == "other" && values[i].v != "other"
		}
		return values[i].n > values[j].n
	})
	for _, v := range values {
		resp.SplitValues = append(resp.SplitValues, v.v)
	}
	return resp, nil
}

// ---- facets, stats, fields ---------------------------------------------

type ValueCount struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

type FacetsRequest struct {
	Selection
	Fields        []string `json:"fields"`
	LimitPerField int      `json:"limit_per_field,omitempty"`
}

type Facet struct {
	Field  string       `json:"field"`
	Values []ValueCount `json:"values"`
}

type FacetsResponse struct {
	ResolvedRange ResolvedRange `json:"resolved_range"`
	Facets        []Facet       `json:"facets"`
}

// Facets returns exact top values for several fields, queried in parallel.
func (s *Service) Facets(ctx context.Context, p *auth.Principal, req FacetsRequest) (*FacetsResponse, error) {
	r, err := s.resolve(p, req.Selection)
	if err != nil {
		return nil, err
	}
	if len(req.Fields) == 0 || len(req.Fields) > 20 {
		return nil, &InputError{Code: "validation_failed", Pointer: "/fields", Message: "fields must contain 1–20 names"}
	}
	limit := req.LimitPerField
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		return nil, &InputError{Code: "validation_failed", Pointer: "/limit_per_field", Message: "limit_per_field must be at most 100"}
	}
	ctx, release, err := s.acquire(ctx, p, r.limits)
	if err != nil {
		return nil, err
	}
	defer release()

	facets := make([]Facet, len(req.Fields))
	errs := make([]error, len(req.Fields))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, f := range req.Fields {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			vals, err := s.opts.Querier.Top(ctx, storage.TopQuery{Selection: r.sel, Field: f, Limit: limit})
			facets[i] = Facet{Field: f, Values: toValueCounts(vals)}
			errs[i] = err
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, mapErr(ctx, err)
	}
	return &FacetsResponse{ResolvedRange: ResolvedRange{r.sel.Range.Start, r.sel.Range.End}, Facets: facets}, nil
}

type Aggregation struct {
	Type  string `json:"type"`
	Field string `json:"field,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type StatsRequest struct {
	Selection
	Aggregations []Aggregation `json:"aggregations"`
}

type AggregationResult struct {
	Type   string       `json:"type"`
	Field  string       `json:"field,omitempty"`
	Value  *int64       `json:"value,omitempty"`
	Values []ValueCount `json:"values,omitempty"`
}

type StatsResponse struct {
	ResolvedRange ResolvedRange       `json:"resolved_range"`
	Results       []AggregationResult `json:"results"`
}

// Stats computes aggregations.
func (s *Service) Stats(ctx context.Context, p *auth.Principal, req StatsRequest) (*StatsResponse, error) {
	r, err := s.resolve(p, req.Selection)
	if err != nil {
		return nil, err
	}
	if len(req.Aggregations) == 0 || len(req.Aggregations) > 10 {
		return nil, &InputError{Code: "validation_failed", Pointer: "/aggregations", Message: "aggregations must contain 1–10 items"}
	}
	for i, a := range req.Aggregations {
		ptr := fmt.Sprintf("/aggregations/%d", i)
		switch a.Type {
		case "count":
		case "count_distinct", "top":
			if a.Field == "" {
				return nil, &InputError{Code: "validation_failed", Pointer: ptr + "/field", Message: "field is required"}
			}
			if a.Limit > 100 {
				return nil, &InputError{Code: "validation_failed", Pointer: ptr + "/limit", Message: "limit must be at most 100"}
			}
		default:
			return nil, &InputError{Code: "validation_failed", Pointer: ptr + "/type", Message: "type must be count, count_distinct or top"}
		}
	}
	ctx, release, err := s.acquire(ctx, p, r.limits)
	if err != nil {
		return nil, err
	}
	defer release()
	resp := &StatsResponse{ResolvedRange: ResolvedRange{r.sel.Range.Start, r.sel.Range.End}}
	for _, a := range req.Aggregations {
		res := AggregationResult{Type: a.Type, Field: a.Field}
		switch a.Type {
		case "count", "count_distinct":
			distinct := ""
			if a.Type == "count_distinct" {
				distinct = a.Field
			}
			n, err := s.opts.Querier.Count(ctx, storage.CountQuery{Selection: r.sel, DistinctField: distinct})
			if err != nil {
				return nil, mapErr(ctx, err)
			}
			res.Value = &n
		case "top":
			limit := a.Limit
			if limit <= 0 {
				limit = 10
			}
			vals, err := s.opts.Querier.Top(ctx, storage.TopQuery{Selection: r.sel, Field: a.Field, Limit: limit})
			if err != nil {
				return nil, mapErr(ctx, err)
			}
			res.Values = toValueCounts(vals)
		}
		resp.Results = append(resp.Results, res)
	}
	return resp, nil
}

type FieldInfo struct {
	Name             string `json:"name"`
	Count            int64  `json:"count"`
	CountApproximate bool   `json:"count_approximate"`
	Kind             string `json:"kind"`
}

type FieldsResponse struct {
	ResolvedRange ResolvedRange `json:"resolved_range"`
	Fields        []FieldInfo   `json:"fields"`
}

// Fields lists field names present in the selection.
func (s *Service) Fields(ctx context.Context, p *auth.Principal, req Selection) (*FieldsResponse, error) {
	r, err := s.resolve(p, req)
	if err != nil {
		return nil, err
	}
	ctx, release, err := s.acquire(ctx, p, r.limits)
	if err != nil {
		return nil, err
	}
	defer release()
	infos, err := s.opts.Querier.FieldNames(ctx, r.sel)
	if err != nil {
		return nil, mapErr(ctx, err)
	}
	canRaw := p.Can(auth.PermLogsViewRaw)
	resp := &FieldsResponse{ResolvedRange: ResolvedRange{r.sel.Range.Start, r.sel.Range.End}, Fields: []FieldInfo{}}
	for _, fi := range infos {
		name := fi.Name
		switch name {
		case "_stream", "_stream_id":
			continue
		case "_msg":
			name = "message"
		case "_time":
			name = "timestamp"
		case "raw_message":
			if !canRaw {
				continue
			}
		}
		resp.Fields = append(resp.Fields, FieldInfo{Name: name, Count: fi.Count, CountApproximate: true, Kind: fieldKind(name)})
	}
	sort.SliceStable(resp.Fields, func(i, j int) bool {
		ki, kj := kindOrder[resp.Fields[i].Kind], kindOrder[resp.Fields[j].Kind]
		if ki != kj {
			return ki < kj
		}
		return resp.Fields[i].Name < resp.Fields[j].Name
	})
	return resp, nil
}

var kindOrder = map[string]int{"core": 0, "label": 1, "dynamic": 2}

type FieldValuesRequest struct {
	Selection
	Search string `json:"search,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type FieldValuesResponse struct {
	ResolvedRange *ResolvedRange `json:"resolved_range,omitempty"`
	Field         string         `json:"field"`
	Values        []ValueCount   `json:"values"`
}

// FieldValues returns exact top values of one field.
func (s *Service) FieldValues(ctx context.Context, p *auth.Principal, field string, req FieldValuesRequest) (*FieldValuesResponse, error) {
	if field == "" || len(field) > filter.MaxFieldBytes {
		return nil, &InputError{Code: "validation_failed", Pointer: "/field", Message: "invalid field name"}
	}
	if field == "raw_message" && !p.Can(auth.PermLogsViewRaw) {
		return nil, fmt.Errorf("%w: raw messages require %s", ErrForbidden, auth.PermLogsViewRaw)
	}
	r, err := s.resolve(p, req.Selection)
	if err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 1000 || len(req.Search) > 256 {
		return nil, &InputError{Code: "validation_failed", Pointer: "/limit", Message: "limit must be at most 1000 and search at most 256 bytes"}
	}
	ctx, release, err := s.acquire(ctx, p, r.limits)
	if err != nil {
		return nil, err
	}
	defer release()
	vals, err := s.opts.Querier.Top(ctx, storage.TopQuery{Selection: r.sel, Field: field, Limit: limit, Search: req.Search})
	if err != nil {
		return nil, mapErr(ctx, err)
	}
	return &FieldValuesResponse{ResolvedRange: &ResolvedRange{r.sel.Range.Start, r.sel.Range.End}, Field: field, Values: toValueCounts(vals)}, nil
}

// ---- validate ------------------------------------------------------------

type ValidateRequest struct {
	Filter *filter.Expr `json:"filter,omitempty"`
	Native *NativeQuery `json:"native,omitempty"`
}

type ValidationIssue struct {
	Message string `json:"message"`
	Pointer string `json:"pointer,omitempty"`
}

type ValidateResponse struct {
	Valid          bool              `json:"valid"`
	NativeCompiled string            `json:"native_compiled,omitempty"`
	Errors         []ValidationIssue `json:"errors,omitempty"`
}

// Validate checks a filter and native query. Native syntax is verified by
// running the query against storage over an empty time window.
func (s *Service) Validate(ctx context.Context, p *auth.Principal, req ValidateRequest) (*ValidateResponse, error) {
	now := s.opts.Now()
	r, err := s.resolve(p, Selection{
		TimeRange: TimeRange{From: now.Add(-time.Second).UTC().Format(time.RFC3339Nano), To: now.UTC().Format(time.RFC3339Nano)},
		Filter:    req.Filter, Native: req.Native,
	})
	if err != nil {
		var ie *InputError
		if errors.As(err, &ie) {
			return &ValidateResponse{Valid: false, Errors: []ValidationIssue{{Message: ie.Message, Pointer: ie.Pointer}}}, nil
		}
		return nil, err
	}
	compiled, pipes, err := s.opts.Querier.Describe(r.sel)
	if err != nil {
		return &ValidateResponse{Valid: false, Errors: []ValidationIssue{{Message: err.Error()}}}, nil
	}
	resp := &ValidateResponse{Valid: true, NativeCompiled: compiled}
	if r.sel.Native == nil {
		return resp, nil
	}
	ctx, release, err := s.acquire(ctx, p, r.limits)
	if err != nil {
		return nil, err
	}
	defer release()
	var rows storage.Rows
	if pipes {
		rows, err = s.opts.Querier.Table(ctx, storage.SearchQuery{Selection: r.sel, Limit: 1})
	} else {
		rows, err = s.opts.Querier.Search(ctx, storage.SearchQuery{Selection: r.sel, Limit: 1})
	}
	if err == nil {
		_ = rows.Close()
		return resp, nil
	}
	err = mapErr(ctx, err)
	var ie *InputError
	if errors.As(err, &ie) {
		return &ValidateResponse{Valid: false, NativeCompiled: compiled, Errors: []ValidationIssue{{Message: ie.Message, Pointer: ie.Pointer}}}, nil
	}
	return nil, err
}

// ---- export and tail -------------------------------------------------------

type ExportRequest struct {
	Selection
	Fields []string `json:"fields,omitempty"`
	Limit  int      `json:"limit,omitempty"`
}

// Export is an open export stream. Close must be called.
type Export struct {
	Rows          storage.Rows
	Fields        []string
	Limit         int
	ResolvedRange ResolvedRange
	CanViewRaw    bool
	release       func()
}

func (e *Export) Close() {
	if e.Rows != nil {
		_ = e.Rows.Close()
	}
	if e.release != nil {
		e.release()
	}
}

// DefaultExportFields are used when an export request lists no fields.
var DefaultExportFields = []string{"timestamp", "hostname", "source_ip", "facility", "severity", "app_name", "process_id", "message"}

// OpenExport starts a streamed export. The role timeout does not apply to
// exports; the caller's request context bounds them instead.
func (s *Service) OpenExport(ctx context.Context, p *auth.Principal, req ExportRequest) (*Export, error) {
	r, err := s.resolve(p, req.Selection)
	if err != nil {
		return nil, err
	}
	if _, pipes, err := s.opts.Querier.Describe(r.sel); err != nil {
		return nil, mapErr(ctx, err)
	} else if pipes {
		return nil, &InputError{Code: "query_invalid", Pointer: "/native/text", Message: "pipes (|) are not supported in exports"}
	}
	limit := req.Limit
	if limit <= 0 {
		limit = min(100_000, r.limits.MaxExportRows)
	}
	if limit > r.limits.MaxExportRows {
		return nil, &InputError{Code: "validation_failed", Pointer: "/limit", Message: fmt.Sprintf("limit exceeds %d rows for your role", r.limits.MaxExportRows)}
	}
	fields := req.Fields
	if len(fields) == 0 {
		fields = DefaultExportFields
	}
	if len(fields) > 200 {
		return nil, &InputError{Code: "validation_failed", Pointer: "/fields", Message: "too many fields"}
	}
	canRaw := p.Can(auth.PermLogsViewRaw)
	var storageFields []string
	for _, f := range fields {
		if f == "raw_message" && !canRaw {
			continue
		}
		storageFields = append(storageFields, f)
	}
	key := callerKey(p)
	s.mu.Lock()
	if s.inflight[key] >= r.limits.MaxConcurrent {
		s.mu.Unlock()
		return nil, ErrTooManyQueries
	}
	s.inflight[key]++
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		if s.inflight[key]--; s.inflight[key] <= 0 {
			delete(s.inflight, key)
		}
		s.mu.Unlock()
	}
	rows, err := s.opts.Querier.Search(ctx, storage.SearchQuery{Selection: r.sel, Fields: storageFields, Limit: limit + 1}) // +1 detects truncation
	if err != nil {
		release()
		return nil, mapErr(ctx, err)
	}
	return &Export{Rows: rows, Fields: storageFields, Limit: limit, CanViewRaw: canRaw,
		ResolvedRange: ResolvedRange{r.sel.Range.Start, r.sel.Range.End}, release: release}, nil
}

type TailRequest struct {
	Filter *filter.Expr `json:"filter,omitempty"`
	Native *NativeQuery `json:"native,omitempty"`
}

// Tail is an open live tail. Close must be called.
type Tail struct {
	Rows       storage.Rows
	CanViewRaw bool
	cancel     context.CancelFunc
	release    func()
}

// Stop cancels the underlying request so a goroutine blocked in Rows.Next
// returns. It is safe to call concurrently with Next; Close is not.
func (t *Tail) Stop() { t.cancel() }

// Close releases the tail. Callers must not call it while Rows.Next runs.
func (t *Tail) Close() {
	t.cancel()
	_ = t.Rows.Close()
	t.release()
}

// OpenTail starts a live tail subject to per-caller and global limits.
func (s *Service) OpenTail(ctx context.Context, p *auth.Principal, req TailRequest, startOffset time.Duration) (*Tail, error) {
	now := s.opts.Now()
	r, err := s.resolve(p, Selection{
		TimeRange: TimeRange{From: now.Add(-time.Second).UTC().Format(time.RFC3339Nano), To: now.UTC().Format(time.RFC3339Nano)},
		Filter:    req.Filter, Native: req.Native,
	})
	if err != nil {
		return nil, err
	}
	if startOffset < 0 || startOffset > time.Minute {
		return nil, &InputError{Code: "validation_failed", Pointer: "start_offset", Message: "start_offset must be between 0 and 1m"}
	}
	key := callerKey(p)
	s.mu.Lock()
	if s.tails[key] >= r.limits.MaxTailSessions {
		s.mu.Unlock()
		return nil, ErrTooManyTails
	}
	if s.tailTotal >= s.opts.MaxTailTotal {
		s.mu.Unlock()
		return nil, ErrTooManyTails
	}
	s.tails[key]++
	s.tailTotal++
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		s.tailTotal--
		if s.tails[key]--; s.tails[key] <= 0 {
			delete(s.tails, key)
		}
		s.mu.Unlock()
	}
	tailCtx, cancel := context.WithCancel(ctx)
	rows, err := s.opts.Querier.Tail(tailCtx, storage.TailQuery{Selection: r.sel, StartOffset: startOffset})
	if err != nil {
		cancel()
		release()
		return nil, mapErr(ctx, err)
	}
	return &Tail{Rows: rows, CanViewRaw: p.Can(auth.PermLogsViewRaw), cancel: cancel, release: release}, nil
}

// ---- helpers ----------------------------------------------------------------

func toValueCounts(vals []storage.ValueCount) []ValueCount {
	out := make([]ValueCount, 0, len(vals))
	for _, v := range vals {
		out = append(out, ValueCount{Value: v.Value, Count: v.Count})
	}
	return out
}

func appendMissing(dst []string, names ...string) []string {
	for _, n := range names {
		found := false
		for _, d := range dst {
			if d == n {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, n)
		}
	}
	return dst
}
