package victorialogs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/storage"
)

const dialectLogsQL = "logsql"

// maxLineBytes bounds a single result row; larger rows fail the query
// instead of growing memory without limit.
const maxLineBytes = 16 << 20

// Describe compiles a selection for display and reports native pipes.
func (b *Backend) Describe(sel storage.Selection) (string, bool, error) {
	f, pipes, err := buildSelection(sel)
	if err != nil {
		return "", false, err
	}
	if pipes != "" {
		return f + " | " + pipes, true, nil
	}
	return f, false, nil
}

// Search runs a selection newest first with an optional field projection.
func (b *Backend) Search(ctx context.Context, q storage.SearchQuery) (storage.Rows, error) {
	if err := q.Range.Validate(); err != nil {
		return nil, err
	}
	if q.Limit <= 0 {
		return nil, fmt.Errorf("search limit must be positive")
	}
	f, pipes, err := buildSelection(q.Selection)
	if err != nil {
		return nil, err
	}
	if pipes != "" {
		return nil, storage.ErrPipesNotAllowed
	}
	var sb strings.Builder
	sb.WriteString(f)
	sb.WriteString(" | sort by (_time desc) limit ")
	sb.WriteString(strconv.Itoa(q.Limit))
	if len(q.Fields) > 0 {
		sb.WriteString(" | fields ")
		for i, name := range q.Fields {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(QuoteFieldName(storageField(name)))
		}
	}
	return b.query(ctx, q.Tenant, sb.String(), q.Range)
}

// Table runs a native query with pipes, capped at q.Limit rows.
func (b *Backend) Table(ctx context.Context, q storage.SearchQuery) (storage.Rows, error) {
	if err := q.Range.Validate(); err != nil {
		return nil, err
	}
	f, pipes, err := buildSelection(q.Selection)
	if err != nil {
		return nil, err
	}
	logsql := f
	if pipes != "" {
		logsql += " | " + pipes
	}
	logsql += " | limit " + strconv.Itoa(max(q.Limit, 1))
	return b.query(ctx, q.Tenant, logsql, q.Range)
}

// Hits returns bucketed counts via /select/logsql/hits.
func (b *Backend) Hits(ctx context.Context, q storage.HitsQuery) ([]storage.HitsSeries, error) {
	if err := q.Range.Validate(); err != nil {
		return nil, err
	}
	if q.Step <= 0 {
		return nil, fmt.Errorf("hits step must be positive")
	}
	f, pipes, err := buildSelection(q.Selection)
	if err != nil {
		return nil, err
	}
	if pipes != "" {
		return nil, storage.ErrPipesNotAllowed
	}
	form := b.baseForm(f, q.Range)
	form.Set("step", strconv.FormatInt(q.Step.Milliseconds(), 10)+"ms")
	if q.Offset != 0 {
		form.Set("offset", strconv.FormatInt(q.Offset.Milliseconds(), 10)+"ms")
	}
	if q.Field != "" {
		form.Set("field", storageField(q.Field))
		if q.FieldLimit > 0 {
			form.Set("fields_limit", strconv.Itoa(q.FieldLimit))
		}
	}
	var resp struct {
		Hits []struct {
			Fields     map[string]string `json:"fields"`
			Timestamps []time.Time       `json:"timestamps"`
			Values     []int64           `json:"values"`
			Total      int64             `json:"total"`
		} `json:"hits"`
	}
	if err := b.postJSON(ctx, q.Tenant, "/select/logsql/hits", form, &resp); err != nil {
		return nil, err
	}
	out := make([]storage.HitsSeries, 0, len(resp.Hits))
	for _, h := range resp.Hits {
		s := storage.HitsSeries{Timestamps: h.Timestamps, Counts: h.Values, Total: h.Total}
		if q.Field != "" {
			v, ok := h.Fields[storageField(q.Field)]
			s.Value, s.Other = v, !ok
		}
		out = append(out, s)
	}
	return out, nil
}

// Top returns exact counts of the most frequent non-empty values.
func (b *Backend) Top(ctx context.Context, q storage.TopQuery) ([]storage.ValueCount, error) {
	if err := q.Range.Validate(); err != nil {
		return nil, err
	}
	f, pipes, err := buildSelection(q.Selection)
	if err != nil {
		return nil, err
	}
	if pipes != "" {
		return nil, storage.ErrPipesNotAllowed
	}
	field := storageField(q.Field)
	qf := quote(field)
	logsql := f + " AND " + qf + ":*"
	if q.Search != "" {
		logsql += " AND " + qf + ":~" + quote("(?i)"+escapeRegexp(q.Search))
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}
	logsql += " | top " + strconv.Itoa(limit) + " by (" + qf + ")"
	rows, err := b.query(ctx, q.Tenant, logsql, q.Range)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []storage.ValueCount
	for rows.Next() {
		r := rows.Row()
		v, _ := r.Get(field)
		h, _ := r.Get("hits")
		n, _ := strconv.ParseInt(h, 10, 64)
		out = append(out, storage.ValueCount{Value: v, Count: n})
	}
	return out, rows.Err()
}

// Count counts rows or distinct values of a field.
func (b *Backend) Count(ctx context.Context, q storage.CountQuery) (int64, error) {
	if err := q.Range.Validate(); err != nil {
		return 0, err
	}
	f, pipes, err := buildSelection(q.Selection)
	if err != nil {
		return 0, err
	}
	if pipes != "" {
		return 0, storage.ErrPipesNotAllowed
	}
	agg := "count()"
	if q.DistinctField != "" {
		agg = "count_uniq(" + quote(storageField(q.DistinctField)) + ")"
	}
	rows, err := b.query(ctx, q.Tenant, f+" | stats "+agg+" as c", q.Range)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var n int64
	if rows.Next() {
		v, _ := rows.Row().Get("c")
		n, _ = strconv.ParseInt(v, 10, 64)
	}
	return n, rows.Err()
}

// FieldNames lists fields via /select/logsql/field_names (approximate counts).
func (b *Backend) FieldNames(ctx context.Context, sel storage.Selection) ([]storage.FieldInfo, error) {
	if err := sel.Range.Validate(); err != nil {
		return nil, err
	}
	f, pipes, err := buildSelection(sel)
	if err != nil {
		return nil, err
	}
	if pipes != "" {
		return nil, storage.ErrPipesNotAllowed
	}
	var resp struct {
		Values []struct {
			Value string `json:"value"`
			Hits  int64  `json:"hits"`
		} `json:"values"`
	}
	if err := b.postJSON(ctx, sel.Tenant, "/select/logsql/field_names", b.baseForm(f, sel.Range), &resp); err != nil {
		return nil, err
	}
	out := make([]storage.FieldInfo, 0, len(resp.Values))
	for _, v := range resp.Values {
		out = append(out, storage.FieldInfo{Name: v.Value, Count: v.Hits})
	}
	return out, nil
}

// Tail streams newly ingested rows via /select/logsql/tail.
func (b *Backend) Tail(ctx context.Context, q storage.TailQuery) (storage.Rows, error) {
	f, pipes, err := buildSelection(q.Selection)
	if err != nil {
		return nil, err
	}
	if pipes != "" {
		return nil, storage.ErrPipesNotAllowed
	}
	form := url.Values{}
	form.Set("query", f)
	if q.StartOffset > 0 {
		form.Set("start_offset", strconv.FormatInt(q.StartOffset.Milliseconds(), 10)+"ms")
	}
	ctx, cancel := context.WithCancel(ctx)
	resp, err := b.do(ctx, q.Tenant, "/select/logsql/tail", form) //nolint:bodyclose // closed by rows.Close
	if err != nil {
		cancel()
		return nil, err
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	return &rows{body: resp.Body, scanner: sc, cancel: cancel}, nil
}

func (b *Backend) baseForm(logsql string, r storage.TimeRange) url.Values {
	form := url.Values{}
	form.Set("query", logsql)
	form.Set("start", r.Start.UTC().Format(time.RFC3339Nano))
	// VictoriaLogs' start/end args form the half-open interval [start, end)
	// at nanosecond precision (verified against v1.52), matching TimeRange.
	form.Set("end", r.End.UTC().Format(time.RFC3339Nano))
	if b.cfg.QueryTimeout > 0 {
		form.Set("timeout", b.cfg.QueryTimeout.String())
	}
	return form
}

func (b *Backend) query(ctx context.Context, tenant, logsql string, r storage.TimeRange) (storage.Rows, error) {
	var cancel context.CancelFunc
	if b.cfg.QueryTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, b.cfg.QueryTimeout+5*time.Second)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	resp, err := b.do(ctx, tenant, "/select/logsql/query", b.baseForm(logsql, r)) //nolint:bodyclose // closed by rows.Close
	if err != nil {
		cancel()
		return nil, err
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	return &rows{body: resp.Body, scanner: sc, cancel: cancel}, nil
}

func (b *Backend) postJSON(ctx context.Context, tenant, path string, form url.Values, dst any) error {
	if b.cfg.QueryTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.cfg.QueryTimeout+5*time.Second)
		defer cancel()
	}
	resp, err := b.do(ctx, tenant, path, form) //nolint:bodyclose // closed by drain
	if err != nil {
		return err
	}
	defer drain(resp.Body)
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(dst); err != nil {
		return fmt.Errorf("victorialogs: decode %s response: %w", path, err)
	}
	return nil
}

// do POSTs a form and returns a 200 response or a classified error.
func (b *Backend) do(ctx context.Context, tenant, path string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.selectURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := tenantHeaders(req, tenant); err != nil {
		return nil, err
	}
	b.auth(req)
	resp, err := b.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("victorialogs %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		drain(resp.Body)
		return nil, &storage.QueryError{
			StatusCode: resp.StatusCode,
			Message:    strings.TrimSpace(string(msg)),
			UserFacing: resp.StatusCode == http.StatusBadRequest,
		}
	}
	return resp, nil
}

// rows iterates over a JSON-lines response.
type rows struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
	cancel  context.CancelFunc
	row     storage.Row
	err     error
	closed  bool
}

func (r *rows) Next() bool {
	if r.err != nil || r.closed {
		return false
	}
	for r.scanner.Scan() {
		line := r.scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		row, err := decodeRow(r.row[:0], line)
		if err != nil {
			r.err = err
			return false
		}
		r.row = row
		return true
	}
	r.err = r.scanner.Err()
	return false
}

func (r *rows) Row() storage.Row { return r.row }
func (r *rows) Err() error       { return r.err }

func (r *rows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.cancel()
	drain(r.body)
	return nil
}

// decodeRow decodes one flat JSON object, preserving field order.
func decodeRow(dst storage.Row, line []byte) (storage.Row, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("victorialogs: malformed result row")
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("victorialogs: malformed result row: %w", err)
		}
		key, _ := kt.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("victorialogs: malformed result row: %w", err)
		}
		var value string
		if len(raw) > 0 && raw[0] == '"' {
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, fmt.Errorf("victorialogs: malformed result row: %w", err)
			}
		} else {
			value = string(raw)
		}
		dst = append(dst, logentry.Field{Key: key, Value: value})
	}
	return dst, nil
}

// QuoteFieldName quotes a field name for use in LogsQL.
func QuoteFieldName(name string) string { return quote(name) }

// Aggregate groups rows by time, by a field, or both. The LogsQL pipeline is
// built here, never from user text, so grouping cannot widen the selection.
func (b *Backend) Aggregate(ctx context.Context, q storage.AggregateQuery) ([]storage.AggRow, error) {
	if err := q.Range.Validate(); err != nil {
		return nil, err
	}
	f, pipes, err := buildSelection(q.Selection)
	if err != nil {
		return nil, err
	}
	if pipes != "" {
		return nil, storage.ErrPipesNotAllowed
	}

	var by []string
	if q.Step > 0 {
		by = append(by, "_time:"+formatMillis(q.Step))
	}
	group := storageField(q.GroupBy)
	if q.GroupBy != "" {
		by = append(by, quote(group))
	}
	metric := "count() as v"
	if q.Metric == storage.MetricDistinct {
		if q.MetricField == "" {
			return nil, fmt.Errorf("victorialogs: count_distinct needs a field")
		}
		metric = "count_uniq(" + quote(storageField(q.MetricField)) + ") as v"
	}

	logsql := f + " | stats"
	if len(by) > 0 {
		logsql += " by (" + strings.Join(by, ", ") + ")"
	}
	logsql += " " + metric
	if q.Step == 0 {
		logsql += " | sort by (v desc)"
		if q.Limit > 0 {
			logsql += " | limit " + strconv.Itoa(q.Limit)
		}
	}

	rows, err := b.query(ctx, q.Tenant, logsql, q.Range)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []storage.AggRow{}
	for rows.Next() {
		r := rows.Row()
		var row storage.AggRow
		if v, ok := r.Get("v"); ok {
			row.Value, _ = strconv.ParseFloat(v, 64)
		}
		if q.GroupBy != "" {
			row.Group, _ = r.Get(group)
			if row.Group == "" {
				continue
			}
		}
		if q.Step > 0 {
			if ts, ok := r.Get("_time"); ok {
				row.Time, _ = time.Parse(time.RFC3339Nano, ts)
			}
			// Buckets can start before the requested range; the caller trims.
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// formatMillis renders a step for LogsQL's `_time:<step>` bucketing.
func formatMillis(d time.Duration) string {
	return strconv.FormatInt(d.Milliseconds(), 10) + "ms"
}

// CategoryCounts counts distinct values per category in a single pass.
//
// Each category becomes a conditional aggregation over the same scan:
//
//	<selection> | stats by (_time:5m)
//	    count_uniq(<field>) if (<category 1>) as c0, count() if (<category 1>) as n0,
//	    count_uniq(<field>) if (<category 2>) as c1, ...
//
// Column names are generated here (c0, n0, …) rather than taken from the
// category names, so a category called `x") as y, count(` cannot reshape the
// pipeline. The category filters themselves go through CompileFilter, the
// same path as any other user filter.
func (b *Backend) CategoryCounts(ctx context.Context, q storage.CategoryQuery) ([]storage.CategoryRow, error) {
	if err := q.Range.Validate(); err != nil {
		return nil, err
	}
	if len(q.Categories) == 0 {
		return []storage.CategoryRow{}, nil
	}
	if len(q.Categories) > storage.MaxCategories {
		return nil, fmt.Errorf("victorialogs: %d categories exceeds the limit of %d",
			len(q.Categories), storage.MaxCategories)
	}
	if q.DistinctField == "" {
		return nil, errors.New("victorialogs: category counts need a distinct field")
	}
	f, pipes, err := buildSelection(q.Selection)
	if err != nil {
		return nil, err
	}
	if pipes != "" {
		return nil, storage.ErrPipesNotAllowed
	}

	distinct := quote(storageField(q.DistinctField))
	var sb strings.Builder
	sb.WriteString(f)
	sb.WriteString(" | stats")
	if q.Step > 0 {
		sb.WriteString(" by (_time:" + formatMillis(q.Step) + ")")
	}
	for i, c := range q.Categories {
		cond := ""
		switch {
		case len(c.Phrases) > 0:
			if c.Field == "" {
				return nil, fmt.Errorf("victorialogs: category %q: phrases need a field", c.Name)
			}
			field := quote(storageField(c.Field))
			var terms strings.Builder
			for j, p := range c.Phrases {
				if j > 0 {
					terms.WriteString(" OR ")
				}
				// quote escapes the value, so a phrase is data however it
				// was typed.
				terms.WriteString(field + ":" + quote(p))
			}
			cond = " if (" + terms.String() + ")"
		case c.Filter != nil:
			compiled, err := CompileFilter(c.Filter)
			if err != nil {
				return nil, fmt.Errorf("victorialogs: category %q: %w", c.Name, err)
			}
			cond = " if (" + compiled + ")"
		}
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, " count_uniq(%s)%s as c%d, count()%s as n%d", distinct, cond, i, cond, i)
	}

	rows, err := b.query(ctx, q.Tenant, sb.String(), q.Range)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []storage.CategoryRow{}
	for rows.Next() {
		r := rows.Row()
		var at time.Time
		if q.Step > 0 {
			ts, ok := r.Get("_time")
			if !ok {
				continue
			}
			at, err = time.Parse(time.RFC3339Nano, ts)
			if err != nil {
				return nil, fmt.Errorf("victorialogs: bucket timestamp %q: %w", ts, err)
			}
		}
		for i, c := range q.Categories {
			row := storage.CategoryRow{Time: at, Category: c.Name}
			if v, ok := r.Get("c" + strconv.Itoa(i)); ok {
				row.Distinct, _ = strconv.ParseFloat(v, 64)
			}
			if v, ok := r.Get("n" + strconv.Itoa(i)); ok {
				row.Messages, _ = strconv.ParseFloat(v, 64)
			}
			out = append(out, row)
		}
	}
	return out, rows.Err()
}
