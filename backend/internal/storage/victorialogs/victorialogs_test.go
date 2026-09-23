package victorialogs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/gzip"

	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/storage"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

func sampleEntry() logentry.Entry {
	var e logentry.Entry
	e.Reset()
	e.Time = time.Date(2026, 9, 14, 14, 31, 2, 123456789, time.UTC)
	e.ReceivedAt = time.Date(2026, 9, 14, 14, 31, 2, 130001234, time.UTC)
	e.Message = "VPN tunnel \"HQ\" disconnected\n\tretrying \x01 \xff"
	e.Hostname = "fw01"
	e.SourceIP = netip.MustParseAddr("10.10.1.1")
	e.SourcePort = 51514
	e.Facility = 20
	e.Severity = logentry.SeverityWarning
	e.SeveritySource = logentry.SeverityFromLog
	e.Protocol = logentry.ProtocolUDP
	e.Format = logentry.FormatRFC5424
	e.AppName = "vpnd"
	e.ProcessID = "812"
	e.MessageID = "TUNNEL"
	e.Source = "syslog-udp"
	e.Tenant = "default"
	e.Raw = "<164>1 raw"
	e.Labels = []logentry.Field{{Key: "labels.site", Value: "dc1"}}
	e.Fields = []logentry.Field{{Key: "vpn_name", Value: "HQ-VPN"}, {Key: "sd.x@1.k", Value: `a\b`}}
	return e
}

func TestAppendEntry(t *testing.T) {
	e := sampleEntry()
	line := appendEntry(nil, &e)
	if line[len(line)-1] != '\n' {
		t.Fatal("line not newline-terminated")
	}
	want := `{"_time":"2026-09-14T14:31:02.123456789Z","_msg":"VPN tunnel \"HQ\" disconnected\n\tretrying \u0001 �",` +
		`"received_at":"2026-09-14T14:31:02.130001234Z","hostname":"fw01","source_ip":"10.10.1.1","source_port":"51514",` +
		`"facility":"local4","facility_code":"20","priority":"164","severity":"warning","severity_code":"4",` +
		`"protocol":"udp","format":"rfc5424","app_name":"vpnd","process_id":"812","message_id":"TUNNEL",` +
		`"source":"syslog-udp","source_type":"syslog","raw_message":"<164>1 raw","labels.site":"dc1",` +
		`"vpn_name":"HQ-VPN","sd.x@1.k":"a\\b"}` + "\n"
	if got := string(line); got != want {
		t.Errorf("appendEntry mismatch\n got: %s\nwant: %s", got, want)
	}
	var decoded map[string]string
	if err := json.Unmarshal(line, &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if decoded["_msg"] != "VPN tunnel \"HQ\" disconnected\n\tretrying \x01 �" {
		t.Errorf("_msg round trip = %q", decoded["_msg"])
	}
}

func TestAppendEntryOptionalMarkers(t *testing.T) {
	var e logentry.Entry
	e.Reset() // no facility, default severity
	e.Time = time.Unix(0, 0)
	e.ReceivedAt = time.Unix(0, 0)
	e.TimeSource = logentry.TimeAdjusted
	e.TimestampRaw = "2099-01-01T00:00:00Z"
	e.Truncated = true
	e.FieldsDropped = 3
	e.ParseError = "bad"
	e.Format = logentry.FormatUnknown
	var got map[string]string
	if err := json.Unmarshal(appendEntry(nil, &e), &got); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"time_source": "adjusted", "severity_source": "default", "timestamp_raw": "2099-01-01T00:00:00Z",
		"truncated": "true", "fields_dropped": "3", "parse_error": "bad", "format": "unknown", "severity": "info",
	} {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	for _, k := range []string{"facility", "priority", "hostname", "source_ip", "peer_ip"} {
		if _, ok := got[k]; ok {
			t.Errorf("unexpected field %s", k)
		}
	}
}

func FuzzAppendString(f *testing.F) {
	f.Add("plain")
	f.Add("quote\" backslash\\ ctrl\x00\x1f bad\xff\xfe emoji 🚀")
	f.Fuzz(func(t *testing.T, s string) {
		b := appendString(nil, s)
		var out string
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("invalid JSON for %q: %s: %v", s, b, err)
		}
		var want strings.Builder
		for _, r := range s { // ranging over a string yields U+FFFD per invalid byte
			want.WriteRune(r)
		}
		if want.String() != out {
			t.Fatalf("round trip mismatch: %q -> %q", s, out)
		}
	})
}

func newTestBackend(t *testing.T, h http.HandlerFunc, compression string) *Backend {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	b, err := New(Config{
		InsertURL: srv.URL, SelectURL: srv.URL, StreamFields: []string{"source", "hostname"},
		WriteTimeout: 5 * time.Second, QueryTimeout: 5 * time.Second, Compression: compression,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWriteBatchRequest(t *testing.T) {
	for _, compression := range []string{"none", "gzip"} {
		t.Run(compression, func(t *testing.T) {
			var gotBody string
			var gotReq *http.Request
			b := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
				gotReq = r
				var body io.Reader = r.Body
				if r.Header.Get("Content-Encoding") == "gzip" {
					zr, err := gzip.NewReader(r.Body)
					if err != nil {
						t.Errorf("gzip: %v", err)
						return
					}
					body = zr
				}
				data, _ := io.ReadAll(body)
				gotBody = string(data)
			}, compression)

			e1, e2 := sampleEntry(), sampleEntry()
			e2.Message = "second"
			batch := &logentry.Batch{Tenant: "default", Entries: []logentry.Entry{e1, e2}}
			if err := b.WriteBatch(context.Background(), batch); err != nil {
				t.Fatalf("WriteBatch: %v", err)
			}
			if gotReq.URL.Path != "/insert/jsonline" {
				t.Errorf("path = %s", gotReq.URL.Path)
			}
			q := gotReq.URL.Query()
			if q.Get("_stream_fields") != "source,hostname" || q.Get("_time_field") != "_time" || q.Get("_msg_field") != "_msg" {
				t.Errorf("query = %v", q)
			}
			if ct := gotReq.Header.Get("Content-Type"); ct != "application/stream+json" {
				t.Errorf("Content-Type = %q (VictoriaLogs drops bodies without it)", ct)
			}
			if gotReq.Header.Get("AccountID") != "0" || gotReq.Header.Get("ProjectID") != "0" {
				t.Errorf("tenant headers missing: %v", gotReq.Header)
			}
			if n := strings.Count(gotBody, "\n"); n != 2 {
				t.Errorf("body has %d lines, want 2", n)
			}
		})
	}
}

func TestWriteBatchErrorClassification(t *testing.T) {
	tests := []struct {
		status int
		want   storage.ErrorClass
	}{
		{http.StatusBadRequest, storage.Rejected},
		{http.StatusRequestEntityTooLarge, storage.Rejected},
		{http.StatusUnauthorized, storage.Fatal},
		{http.StatusNotFound, storage.Fatal},
		{http.StatusTooManyRequests, storage.Retryable},
		{http.StatusServiceUnavailable, storage.Retryable},
	}
	for _, tt := range tests {
		b := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", tt.status)
		}, "none")
		e := sampleEntry()
		err := b.WriteBatch(context.Background(), &logentry.Batch{Entries: []logentry.Entry{e}})
		var we *storage.WriteError
		if !errors.As(err, &we) || we.Class != tt.want || we.StatusCode != tt.status {
			t.Errorf("status %d: err = %v, want class %s", tt.status, err, tt.want)
		}
	}

	// Connection failures are retryable.
	b, _ := New(Config{InsertURL: "http://127.0.0.1:1", SelectURL: "http://127.0.0.1:1", StreamFields: []string{"s"}, WriteTimeout: time.Second})
	e := sampleEntry()
	if err := b.WriteBatch(context.Background(), &logentry.Batch{Entries: []logentry.Entry{e}}); storage.ClassOf(err) != storage.Retryable {
		t.Errorf("connection refused: class = %s, err = %v", storage.ClassOf(err), err)
	}

	// Unknown tenants are rejected without a request.
	err := b.WriteBatch(context.Background(), &logentry.Batch{Tenant: "other", Entries: []logentry.Entry{e}})
	if !errors.Is(err, storage.ErrUnknownTenant) || storage.ClassOf(err) != storage.Rejected {
		t.Errorf("unknown tenant: err = %v", err)
	}
}

func TestSearch(t *testing.T) {
	var form map[string][]string
	b := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		if strings.Contains(r.PostForm.Get("query"), "syntax(") {
			http.Error(w, "cannot parse query", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"_time":"2026-09-14T10:00:00Z","_msg":"b","hostname":"h\"1"}`+"\n\n"+`{"_time":"2026-09-14T09:00:00Z","_msg":"a","n":5}`+"\n")
	}, "none")

	start := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	q := storage.SearchQuery{
		Selection: storage.Selection{
			Range:  storage.TimeRange{Start: start, End: start.Add(24 * time.Hour)},
			Native: &storage.NativeQuery{Dialect: "logsql", Text: `hostname:="fw01"`},
		},
		Fields: []string{"_time", "weird key"},
		Limit:  50,
	}
	rows, err := b.Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []storage.Row
	for rows.Next() {
		got = append(got, append(storage.Row(nil), rows.Row()...))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows", len(got))
	}
	if v, _ := got[0].Get("hostname"); v != `h"1` {
		t.Errorf("hostname = %q", v)
	}
	if v, _ := got[1].Get("n"); v != "5" {
		t.Errorf("non-string value = %q", v)
	}
	if got[0][0].Key != "_time" || got[0][1].Key != "_msg" {
		t.Errorf("field order not preserved: %v", got[0])
	}
	wantQuery := `(hostname:="fw01") | sort by (_time desc) limit 50 | fields "_time", "weird key"`
	if form["query"][0] != wantQuery {
		t.Errorf("query = %q, want %q", form["query"][0], wantQuery)
	}
	if form["start"][0] != "2026-09-14T00:00:00Z" || form["end"][0] != "2026-09-15T00:00:00Z" {
		t.Errorf("range = %s..%s", form["start"][0], form["end"][0])
	}

	q.Native.Text = "syntax("
	_, err = b.Search(context.Background(), q)
	var qe *storage.QueryError
	if !errors.As(err, &qe) || !qe.UserFacing || qe.StatusCode != http.StatusBadRequest {
		t.Errorf("syntax error: %v", err)
	}

	q.Native.Dialect = "sql"
	if _, err := b.Search(context.Background(), q); !errors.Is(err, storage.ErrUnsupportedDialect) {
		t.Errorf("dialect: %v", err)
	}
}

func TestParseRetention(t *testing.T) {
	info, err := parseRetention(strings.NewReader("-httpListenAddr=\":9428\"\n-retentionPeriod=\"30d\"\n"))
	if err != nil || info.Period != 30*24*time.Hour || !info.Explicit {
		t.Errorf("explicit: %+v, %v", info, err)
	}
	info, err = parseRetention(strings.NewReader("-httpListenAddr=\":9428\"\n"))
	if err != nil || info.Period != 7*24*time.Hour || info.Explicit {
		t.Errorf("default: %+v, %v", info, err)
	}
	for in, want := range map[string]time.Duration{"12h": 12 * time.Hour, "2w": 14 * 24 * time.Hour, "1y": 365 * 24 * time.Hour, "3": 93 * 24 * time.Hour} {
		if got, err := parseVMDuration(in); err != nil || got != want {
			t.Errorf("parseVMDuration(%q) = %v, %v", in, got, err)
		}
	}
}

func TestParseUsage(t *testing.T) {
	metrics := `# HELP x
vl_compressed_data_size_bytes{type="storage/small"} 100
vl_compressed_data_size_bytes{type="storage/big"} 1.5e3
vl_uncompressed_data_size_bytes{type="storage/small"} 4000
vl_free_disk_space_bytes{path="d"} 10
vl_total_disk_space_bytes{path="d"} 20
vl_partitions 2
vl_rows_ingested_total{type="jsonline"} 99
`
	u, err := parseUsage(strings.NewReader(metrics))
	if err != nil {
		t.Fatal(err)
	}
	want := storage.UsageInfo{CompressedBytes: 1600, UncompressedBytes: 4000, FreeDiskBytes: 10, TotalDiskBytes: 20, Partitions: 2}
	if u != want {
		t.Errorf("usage = %+v, want %+v", u, want)
	}
}

func BenchmarkEncodeBatch(b *testing.B) {
	batch := &logentry.Batch{}
	for range 1000 {
		batch.Entries = append(batch.Entries, sampleEntry())
	}
	buf := make([]byte, 0, 1<<20)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		buf = encodeBatch(buf[:0], batch)
	}
	b.SetBytes(int64(len(buf)))
}

func TestCategoryCounts(t *testing.T) {
	var query string
	b := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		query = r.PostForm.Get("query")
		_, _ = io.WriteString(w, `{"_time":"2026-09-14T10:00:00Z","c0":"120","n0":"400","c1":"7","n1":"9"}`+"\n"+
			`{"_time":"2026-09-14T10:05:00Z","c0":"98","n0":"310","c1":"0","n1":"0"}`+"\n")
	}, "none")

	start := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	rows, err := b.CategoryCounts(context.Background(), storage.CategoryQuery{
		Selection:     storage.Selection{Range: storage.TimeRange{Start: start, End: start.Add(10 * time.Minute)}},
		DistinctField: "dns.client_ip",
		Step:          5 * time.Minute,
		Categories: []storage.Category{
			{Name: "tiktok", Filter: &filter.Expr{Op: filter.Contains, Field: "dns.qname", Value: "tiktok"}},
			{Name: "everything", Filter: nil},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// One conditional pair per category, over a single pass.
	for _, want := range []string{
		"| stats by (_time:300000ms)",
		`count_uniq("dns.client_ip") if (`,
		" as c0, count() if (",
		" as n0,",
		"as c1, count() as n1", // a nil filter counts everything, with no condition
	} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q is missing %q", query, want)
		}
	}

	if len(rows) != 4 {
		t.Fatalf("rows = %d, want one per category per bucket: %+v", len(rows), rows)
	}
	first := rows[0]
	if first.Category != "tiktok" || first.Distinct != 120 || first.Messages != 400 ||
		!first.Time.Equal(start) {
		t.Errorf("row = %+v", first)
	}
	// A category with no matches is reported as zero, not dropped: the chart
	// needs the gap to be a zero.
	if last := rows[3]; last.Category != "everything" || last.Distinct != 0 {
		t.Errorf("row = %+v", last)
	}
}

func TestCategoryCountsCannotBeReshapedByACategoryName(t *testing.T) {
	var query string
	b := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		query = r.PostForm.Get("query")
		_, _ = io.WriteString(w, `{"c0":"1","n0":"1"}`+"\n")
	}, "none")

	start := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	hostile := `x") as y, count(*`
	rows, err := b.CategoryCounts(context.Background(), storage.CategoryQuery{
		Selection:     storage.Selection{Range: storage.TimeRange{Start: start, End: start.Add(time.Hour)}},
		DistinctField: "dns.client_ip",
		Categories:    []storage.Category{{Name: hostile, Filter: &filter.Expr{Op: filter.Eq, Field: "dns.qname", Value: `evil" | drop`}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The name is never part of the pipeline, and the filter value stays
	// inside a quoted string with its quote escaped, so its pipe is data.
	if strings.Contains(query, "as y") {
		t.Errorf("the category name reached the pipeline: %s", query)
	}
	if strings.Contains(query, `evil" | drop`) {
		t.Errorf("the filter value escaped its quotes: %s", query)
	}
	if !strings.Contains(query, `evil\" | drop`) {
		t.Errorf("the filter value was not passed through escaped: %s", query)
	}
	// Outside the quoted literals there is exactly one pipeline stage.
	if n := strings.Count(unquoted(query), "|"); n != 1 {
		t.Errorf("query has %d pipeline stages, want only the stats stage: %s", n, query)
	}
	if !strings.Contains(query, "as c0") {
		t.Errorf("query = %s", query)
	}
	// The name still labels the result.
	if len(rows) != 1 || rows[0].Category != hostile {
		t.Errorf("rows = %+v", rows)
	}
}

func TestCategoryCountsValidatesTheQuery(t *testing.T) {
	b := newTestBackend(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a rejected query was sent")
	}, "none")
	start := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	sel := storage.Selection{Range: storage.TimeRange{Start: start, End: start.Add(time.Hour)}}
	cat := []storage.Category{{Name: "a"}}

	if _, err := b.CategoryCounts(context.Background(), storage.CategoryQuery{Selection: sel, Categories: cat}); err == nil {
		t.Error("a query without a distinct field was accepted")
	}
	many := make([]storage.Category, storage.MaxCategories+1)
	for i := range many {
		many[i] = storage.Category{Name: "c"}
	}
	if _, err := b.CategoryCounts(context.Background(), storage.CategoryQuery{
		Selection: sel, Categories: many, DistinctField: "f"}); err == nil {
		t.Error("too many categories were accepted")
	}
	// No categories is not an error, it just has nothing to count.
	rows, err := b.CategoryCounts(context.Background(), storage.CategoryQuery{Selection: sel, DistinctField: "f"})
	if err != nil || len(rows) != 0 {
		t.Errorf("rows = %+v, err = %v", rows, err)
	}
	// Pipes in the selection would let the caller reshape the results.
	_, err = b.CategoryCounts(context.Background(), storage.CategoryQuery{
		Selection: storage.Selection{Range: sel.Range,
			Native: &storage.NativeQuery{Dialect: "logsql", Text: `* | drop _msg`}},
		Categories: cat, DistinctField: "f"})
	if !errors.Is(err, storage.ErrPipesNotAllowed) {
		t.Errorf("err = %v, want ErrPipesNotAllowed", err)
	}
}

// unquoted removes double-quoted literals, so a test can look at the shape of
// a query without seeing the values inside it.
func unquoted(q string) string {
	var b strings.Builder
	in, escaped := false, false
	for _, r := range q {
		switch {
		case escaped:
			escaped = false
		case r == '\\':
			escaped = true
		case r == '"':
			in = !in
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestCategoryCountsMatchesDomainsAsPhrases(t *testing.T) {
	var query string
	b := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		query = r.PostForm.Get("query")
		_, _ = io.WriteString(w, `{"c0":"7","n0":"9"}`+"\n")
	}, "none")

	start := time.Date(2026, 9, 23, 2, 0, 0, 0, time.UTC)
	rows, err := b.CategoryCounts(context.Background(), storage.CategoryQuery{
		Selection:     storage.Selection{Range: storage.TimeRange{Start: start, End: start.Add(time.Hour)}},
		DistinctField: "dns.client_ip",
		Categories: []storage.Category{{
			Name: "tiktok", Field: "dns.qname",
			// A hostile value is data, not syntax.
			Phrases: []string{"tiktok.com", `evil" or *`},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A phrase, not a regular expression: the backend answers it from its
	// index instead of testing every row.
	if !strings.Contains(query, `if ("dns.qname":"tiktok.com" OR `) {
		t.Errorf("query = %s", query)
	}
	if strings.Contains(query, ":~") {
		t.Errorf("a phrase category compiled to a regular expression: %s", query)
	}
	if strings.Contains(query, `evil" or *`) {
		t.Errorf("the phrase escaped its quotes: %s", query)
	}
	if len(rows) != 1 || rows[0].Distinct != 7 {
		t.Errorf("rows = %+v", rows)
	}

	// Phrases need a field to match against.
	_, err = b.CategoryCounts(context.Background(), storage.CategoryQuery{
		Selection:     storage.Selection{Range: storage.TimeRange{Start: start, End: start.Add(time.Hour)}},
		DistinctField: "dns.client_ip",
		Categories:    []storage.Category{{Name: "x", Phrases: []string{"a.com"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "need a field") {
		t.Errorf("err = %v", err)
	}
}
