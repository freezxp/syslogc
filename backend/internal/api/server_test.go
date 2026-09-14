package api

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/config"
	"github.com/freezxp/syslogc/backend/internal/ingestion/pipeline"
	"github.com/freezxp/syslogc/backend/internal/ingestion/source"
	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/metadata"
	"github.com/freezxp/syslogc/backend/internal/metadata/postgres/pgtest"
	"github.com/freezxp/syslogc/backend/internal/metrics"
	"github.com/freezxp/syslogc/backend/internal/query"
	"github.com/freezxp/syslogc/backend/internal/storage"
)

// ---- fakes ------------------------------------------------------------------

type fakeRow struct {
	t      time.Time
	fields storage.Row
}

// fakeQuerier is an in-memory LogQuerier. Filters are ignored except that a
// native text of "BAD" yields a user-facing syntax error.
type fakeQuerier struct {
	mu   sync.Mutex
	rows []fakeRow
	tail chan storage.Row
}

func (f *fakeQuerier) add(t time.Time, kv ...string) {
	row := storage.Row{{Key: "_time", Value: t.UTC().Format(time.RFC3339Nano)}, {Key: "_stream_id", Value: "s1"}}
	for i := 0; i+1 < len(kv); i += 2 {
		row = append(row, logentry.Field{Key: kv[i], Value: kv[i+1]})
	}
	f.mu.Lock()
	f.rows = append(f.rows, fakeRow{t: t, fields: row})
	f.mu.Unlock()
}

func (f *fakeQuerier) Describe(sel storage.Selection) (string, bool, error) {
	if sel.Native != nil {
		return sel.Native.Text, strings.Contains(sel.Native.Text, "|"), nil
	}
	return "*", false, nil
}

func (f *fakeQuerier) check(sel storage.Selection) error {
	if sel.Native != nil && strings.Contains(sel.Native.Text, "BAD") {
		return &storage.QueryError{StatusCode: 400, Message: "cannot parse query", UserFacing: true}
	}
	return nil
}

func (f *fakeQuerier) Search(_ context.Context, q storage.SearchQuery) (storage.Rows, error) {
	if err := f.check(q.Selection); err != nil {
		return nil, err
	}
	f.mu.Lock()
	var out []fakeRow
	for _, r := range f.rows {
		if !r.t.Before(q.Range.Start) && r.t.Before(q.Range.End) {
			out = append(out, r)
		}
	}
	f.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool { return out[i].t.After(out[j].t) })
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	rows := make([]storage.Row, len(out))
	for i, r := range out {
		rows[i] = r.fields
	}
	return &sliceRows{rows: rows, i: -1}, nil
}

func (f *fakeQuerier) Table(_ context.Context, q storage.SearchQuery) (storage.Rows, error) {
	return &sliceRows{rows: []storage.Row{{{Key: "severity", Value: "error"}, {Key: "hits", Value: "3"}}}, i: -1}, f.check(q.Selection)
}

func (f *fakeQuerier) Hits(_ context.Context, q storage.HitsQuery) ([]storage.HitsSeries, error) {
	return []storage.HitsSeries{{Value: "error", Timestamps: []time.Time{q.Range.Start}, Counts: []int64{int64(len(f.rows))}, Total: int64(len(f.rows))}}, nil
}

func (f *fakeQuerier) Top(_ context.Context, q storage.TopQuery) ([]storage.ValueCount, error) {
	return []storage.ValueCount{{Value: q.Field + "-a", Count: 2}, {Value: q.Field + "-b", Count: 1}}, nil
}

func (f *fakeQuerier) Count(context.Context, storage.CountQuery) (int64, error) {
	return int64(len(f.rows)), nil
}

func (f *fakeQuerier) FieldNames(context.Context, storage.Selection) ([]storage.FieldInfo, error) {
	return []storage.FieldInfo{{Name: "_msg", Count: 5}, {Name: "_stream", Count: 5}, {Name: "labels.site", Count: 5}, {Name: "vpn_name", Count: 2}, {Name: "hostname", Count: 5}}, nil
}

func (f *fakeQuerier) Tail(ctx context.Context, _ storage.TailQuery) (storage.Rows, error) {
	return &chanRows{ctx: ctx, ch: f.tail}, nil
}

type sliceRows struct {
	rows []storage.Row
	i    int
}

func (s *sliceRows) Next() bool       { s.i++; return s.i < len(s.rows) }
func (s *sliceRows) Row() storage.Row { return s.rows[s.i] }
func (s *sliceRows) Err() error       { return nil }
func (s *sliceRows) Close() error     { return nil }

type chanRows struct {
	ctx context.Context
	ch  chan storage.Row
	row storage.Row
}

func (c *chanRows) Next() bool {
	select {
	case r, ok := <-c.ch:
		c.row = r
		return ok
	case <-c.ctx.Done():
		return false
	}
}
func (c *chanRows) Row() storage.Row { return c.row }
func (c *chanRows) Err() error       { return nil }
func (c *chanRows) Close() error     { return nil }

type fakeSink struct {
	mu   sync.Mutex
	msgs []pipeline.RawMessage
	full bool
}

func (s *fakeSink) TryEnqueue(m pipeline.RawMessage) bool {
	return s.Enqueue(context.Background(), m) == nil
}
func (s *fakeSink) Enqueue(ctx context.Context, m pipeline.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.full {
		<-ctx.Done()
		return ctx.Err()
	}
	s.msgs = append(s.msgs, m)
	return nil
}

type fakeBackend struct{ storage.Backend }

func (fakeBackend) Name() string               { return "fake" }
func (fakeBackend) Ping(context.Context) error { return nil }
func (fakeBackend) Capabilities() storage.Capabilities {
	return storage.Capabilities{NativeDialects: []string{"logsql"}}
}
func (fakeBackend) Admin() storage.Admin { return fakeAdmin{} }

type fakeAdmin struct{}

func (fakeAdmin) Retention(context.Context) (storage.RetentionInfo, error) {
	return storage.RetentionInfo{Period: 30 * 24 * time.Hour}, nil
}
func (fakeAdmin) Usage(context.Context) (storage.UsageInfo, error) {
	return storage.UsageInfo{CompressedBytes: 1234}, nil
}

// ---- harness ------------------------------------------------------------------

type env struct {
	t       *testing.T
	srv     *httptest.Server
	store   metadata.Store
	querier *fakeQuerier
	sink    *fakeSink
	metrics *metrics.Metrics
}

var passwords = map[string]string{"admin": "admin-password-1", "ops": "operator-pass-1", "viewer": "viewer-password1"}

func newEnv(t *testing.T) *env {
	t.Helper()
	store := pgtest.Open(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	authSvc := auth.NewService(store, auth.Config{SessionTTL: time.Hour, SessionIdleTimeout: time.Hour}, log)
	for name, role := range map[string]string{"admin": auth.RoleAdmin, "ops": auth.RoleOperator, "viewer": auth.RoleViewer} {
		h, _ := auth.HashPassword(passwords[name])
		if err := store.CreateUser(context.Background(), &metadata.User{Tenant: "default", Username: name, PasswordHash: h, Role: role}); err != nil {
			t.Fatal(err)
		}
	}
	m := metrics.New("test", "abc")
	fq := &fakeQuerier{tail: make(chan storage.Row, 10)}
	qs := query.NewService(query.Options{Querier: fq, Admin: fakeAdmin{}, NodeStats: store, CursorKey: []byte("0123456789abcdef0123456789abcdef"),
		Limits: query.DefaultLimits(30 * 24 * time.Hour), MaxTieGroup: 100})
	sc := config.Source{Name: "http-json", Type: "http_json", Timezone: "UTC", RawMessage: "never", Tenant: "default",
		HostnameFallback: "none", SDFlatten: "full", Format: "auto"}
	httpSrc, err := source.New(sc, m)
	if err != nil {
		t.Fatal(err)
	}
	sink := &fakeSink{}
	ui := fstest.MapFS{
		"index.html":      {Data: []byte("<!doctype html><title>Syslogc UI</title>")},
		"assets/app-1.js": {Data: []byte("console.log(1)")},
	}
	server := New(Options{
		Metrics: m, Log: log, NodeID: "n1", Version: "test", Roles: []string{"all"},
		Checks: []ReadinessCheck{{Name: "storage", Check: func(context.Context) error { return nil }}},
		API: &APIDeps{Auth: authSvc, Store: store, Query: qs, Storage: fakeBackend{}, WebUI: ui,
			Retention: func() any { return map[string]string{"status": "in_sync"} }},
		Ingest: &IngestDeps{Sink: sink, Source: func() *source.Settings { return httpSrc },
			MaxBodyBytes: 1 << 20, MaxEvents: 100, EnqueueTimeout: 50 * time.Millisecond},
	})
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return &env{t: t, srv: ts, store: store, querier: fq, sink: sink, metrics: m}
}

// client is a browser-like client with a cookie jar and CSRF token.
type client struct {
	e    *env
	http *http.Client
	csrf string
}

func (e *env) anon() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, http: &http.Client{Jar: jar}}
}

func (e *env) login(user string) *client {
	e.t.Helper()
	c := e.anon()
	resp, body := c.do("POST", "/api/v1/auth/login", map[string]string{"username": user, "password": passwords[user]}, nil)
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("login %s: %d %s", user, resp.StatusCode, body)
	}
	var s struct {
		CSRFToken string `json:"csrf_token"`
	}
	_ = json.Unmarshal(body, &s)
	c.csrf = s.CSRFToken
	return c
}

func (c *client) do(method, path string, body any, headers map[string]string) (*http.Response, []byte) {
	c.e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	default:
		data, _ := json.Marshal(b)
		rd = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

var lastHour = map[string]any{"time_range": map[string]string{"from": "now-1h", "to": "now"}}

// ---- tests --------------------------------------------------------------------

func TestOperationalEndpoints(t *testing.T) {
	e := newEnv(t)
	c := e.anon()
	if resp, body := c.do("GET", "/health", nil, nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "ok") {
		t.Errorf("health: %d %s", resp.StatusCode, body)
	}
	resp, body := c.do("GET", "/ready", nil, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ready"`) {
		t.Errorf("ready: %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Request-ID") == "" || resp.Header.Get("Content-Security-Policy") == "" {
		t.Error("missing request ID or CSP header")
	}
	if resp, body := c.do("GET", "/metrics", nil, nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `syslogc_http_requests_total{code="200",method="GET",route="GET /ready"}`) {
		t.Errorf("metrics: %d", resp.StatusCode)
	}
}

func TestEveryProtectedRouteRequiresAuthentication(t *testing.T) {
	e := newEnv(t)
	s := &Server{opts: Options{API: &APIDeps{WebUI: fstest.MapFS{}}, Ingest: &IngestDeps{}}}
	c := e.anon()
	for _, rt := range s.routes() {
		if rt.access == public {
			continue
		}
		method, path, _ := strings.Cut(rt.pattern, " ")
		path = strings.ReplaceAll(strings.ReplaceAll(path, "{id}", "0192f0c4-0000-7000-8000-000000000000"), "{field}", "hostname")
		resp, body := c.do(method, path, nil, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d without credentials (%s)", rt.pattern, resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("%s: content type %q", rt.pattern, ct)
		}
	}
}

func TestLoginSessionAndCSRF(t *testing.T) {
	e := newEnv(t)
	c := e.anon()
	resp, body := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "ops", "password": "wrong"}, nil)
	var prob Problem
	_ = json.Unmarshal(body, &prob)
	if resp.StatusCode != http.StatusUnauthorized || prob.Code != "unauthenticated" || prob.RequestID == "" {
		t.Errorf("bad login: %d %+v", resp.StatusCode, prob)
	}

	ops := e.login("ops")
	for _, ck := range ops.http.Jar.Cookies(mustURL(e.srv.URL)) {
		if ck.Name == sessionCookie && ck.Value == "" {
			t.Error("empty session cookie")
		}
	}
	resp, body = ops.do("GET", "/api/v1/auth/me", nil, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"username":"ops"`) || !strings.Contains(string(body), "logs:export") {
		t.Errorf("me: %d %s", resp.StatusCode, body)
	}

	// Unsafe request without CSRF token is rejected.
	noCSRF := *ops
	noCSRF.csrf = ""
	if resp, _ := noCSRF.do("POST", "/api/v1/logs/search", lastHour, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("missing CSRF token: %d", resp.StatusCode)
	}
	// Cross-site request is rejected even with the token.
	if resp, _ := ops.do("POST", "/api/v1/logs/search", lastHour, map[string]string{"Origin": "https://evil.example"}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin: %d", resp.StatusCode)
	}
	if resp, _ := ops.do("POST", "/api/v1/logs/search", lastHour, map[string]string{"Origin": e.srv.URL}); resp.StatusCode != http.StatusOK {
		t.Errorf("same-origin: %d", resp.StatusCode)
	}

	if resp, _ := ops.do("POST", "/api/v1/auth/logout", nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("logout: %d", resp.StatusCode)
	}
	if resp, _ := ops.do("GET", "/api/v1/auth/me", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("after logout: %d", resp.StatusCode)
	}
}

func TestPermissionMatrix(t *testing.T) {
	e := newEnv(t)
	viewer, ops := e.login("viewer"), e.login("ops")
	native := map[string]any{"time_range": map[string]string{"from": "now-1h", "to": "now"}, "native": map[string]string{"dialect": "logsql", "text": "error"}}

	cases := []struct {
		c      *client
		method string
		path   string
		body   any
		want   int
	}{
		{viewer, "POST", "/api/v1/logs/search", lastHour, 200},
		{viewer, "POST", "/api/v1/logs/search", native, 403},
		{ops, "POST", "/api/v1/logs/search", native, 200},
		{viewer, "POST", "/api/v1/logs/export?format=csv", lastHour, 403},
		{ops, "POST", "/api/v1/logs/export?format=csv", lastHour, 200},
		{viewer, "GET", "/api/v1/api-keys", nil, 403},
		{ops, "GET", "/api/v1/api-keys", nil, 200},
		{viewer, "GET", "/api/v1/system/health", nil, 200},
		{viewer, "POST", "/api/v1/dashboard/overview", lastHour, 200},
	}
	for _, tc := range cases {
		resp, body := tc.c.do(tc.method, tc.path, tc.body, nil)
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s as viewer=%v: %d, want %d (%s)", tc.method, tc.path, tc.c == viewer, resp.StatusCode, tc.want, body)
		}
	}
}

func TestForcedPasswordChange(t *testing.T) {
	e := newEnv(t)
	u, _ := e.store.UserByUsername(context.Background(), "viewer")
	_ = e.store.UpdatePassword(context.Background(), u.ID, u.PasswordHash, true)
	c := e.login("viewer")
	resp, body := c.do("POST", "/api/v1/logs/search", lastHour, nil)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "change your password") {
		t.Errorf("before change: %d %s", resp.StatusCode, body)
	}
	if resp, body := c.do("PUT", "/api/v1/auth/me/password", map[string]string{"current_password": passwords["viewer"], "new_password": "short"}, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("weak password: %d %s", resp.StatusCode, body)
	}
	if resp, body := c.do("PUT", "/api/v1/auth/me/password", map[string]string{"current_password": passwords["viewer"], "new_password": "a-new-strong-passphrase"}, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("change: %d %s", resp.StatusCode, body)
	}
	if resp, _ := c.do("POST", "/api/v1/logs/search", lastHour, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("after change: %d", resp.StatusCode)
	}
}

func TestSearchPaginationWithTies(t *testing.T) {
	e := newEnv(t)
	base := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	// 5 rows at base+3s, 10 rows sharing base+2s, 5 rows at base+1s.
	for i := range 5 {
		e.querier.add(base.Add(3*time.Second), "_msg", fmt.Sprintf("newest %d", i), "hostname", "h1", "severity", "info", "severity_code", "6")
	}
	for i := range 10 {
		e.querier.add(base.Add(2*time.Second), "_msg", fmt.Sprintf("tie %d", i), "hostname", "h2")
	}
	for i := range 5 {
		e.querier.add(base.Add(time.Second), "_msg", fmt.Sprintf("oldest %d", i), "vpn_name", "HQ", "labels.site", "dc1", "raw_message", "<14>raw")
	}
	c := e.login("viewer")
	seen := map[string]bool{}
	var cursor *string
	pages := 0
	for {
		req := map[string]any{"time_range": map[string]string{"from": "now-1h", "to": "now"}, "limit": 7, "cursor": cursor}
		resp, body := c.do("POST", "/api/v1/logs/search", req, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("search: %d %s", resp.StatusCode, body)
		}
		var sr query.SearchResponse
		if err := json.Unmarshal(body, &sr); err != nil {
			t.Fatal(err)
		}
		pages++
		for _, row := range sr.Rows {
			msg := row["message"].(string)
			if seen[msg] {
				t.Fatalf("duplicate row %q on page %d", msg, pages)
			}
			seen[msg] = true
		}
		if sr.Page.NextCursor == nil || pages > 10 {
			break
		}
		cursor = sr.Page.NextCursor
	}
	if len(seen) != 20 {
		t.Errorf("paged %d unique rows, want 20", len(seen))
	}

	// Shaping: dynamic fields, labels, raw message, numbers, _ref.
	_, body := c.do("POST", "/api/v1/logs/search", map[string]any{"time_range": map[string]string{"from": "now-1h", "to": "now"}, "limit": 1000}, nil)
	var sr struct {
		Rows []map[string]any `json:"rows"`
	}
	_ = json.Unmarshal(body, &sr)
	var oldest, newest map[string]any
	for _, r := range sr.Rows {
		if strings.HasPrefix(r["message"].(string), "oldest") {
			oldest = r
		}
		if strings.HasPrefix(r["message"].(string), "newest") {
			newest = r
		}
	}
	if oldest["fields"].(map[string]any)["vpn_name"] != "HQ" || oldest["labels"].(map[string]any)["site"] != "dc1" || oldest["raw_message"] != "<14>raw" {
		t.Errorf("row shaping: %v", oldest)
	}
	if newest["severity_code"] != float64(6) || newest["_ref"].(map[string]any)["stream_id"] != "s1" {
		t.Errorf("typed fields/_ref: %v", newest)
	}

	// A cursor cannot be replayed against a different query.
	req := map[string]any{"time_range": map[string]string{"from": "now-1h", "to": "now"}, "limit": 7}
	_, body = c.do("POST", "/api/v1/logs/search", req, nil)
	var first query.SearchResponse
	_ = json.Unmarshal(body, &first)
	req["filter"] = map[string]any{"op": "eq", "field": "hostname", "value": "h1"}
	req["cursor"] = *first.Page.NextCursor
	if resp, body := c.do("POST", "/api/v1/logs/search", req, nil); resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "invalid_cursor") {
		t.Errorf("replayed cursor: %d %s", resp.StatusCode, body)
	}
}

func TestSearchErrors(t *testing.T) {
	e := newEnv(t)
	ops := e.login("ops")
	tests := []struct {
		body any
		code string
	}{
		{map[string]any{"time_range": map[string]string{"from": "now", "to": "now-1h"}}, "invalid_time_range"},
		{map[string]any{"time_range": map[string]string{"from": "now-400d", "to": "now"}}, "invalid_time_range"},
		{map[string]any{"time_range": map[string]string{"from": "now-1h", "to": "now"}, "filter": map[string]any{"op": "gt", "field": "x", "value": "abc"}}, "validation_failed"},
		{map[string]any{"time_range": map[string]string{"from": "now-1h", "to": "now"}, "native": map[string]string{"dialect": "logsql", "text": "BAD("}}, "query_invalid"},
		{map[string]any{"time_range": map[string]string{"from": "now-1h", "to": "now"}, "limit": 999999}, "validation_failed"},
		{"{not json", "bad_request"},
	}
	for _, tt := range tests {
		resp, body := ops.do("POST", "/api/v1/logs/search", tt.body, nil)
		var p Problem
		_ = json.Unmarshal(body, &p)
		if p.Code != tt.code || resp.StatusCode < 400 {
			t.Errorf("body %v: %d %s, want code %s", tt.body, resp.StatusCode, body, tt.code)
		}
	}
	resp, body := ops.do("POST", "/api/v1/logs/search", map[string]any{"time_range": map[string]string{"from": "now-1h", "to": "now"},
		"native": map[string]string{"dialect": "logsql", "text": "* | stats by (severity) count() hits"}}, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"mode":"table"`) || !strings.Contains(string(body), `"columns":["severity","hits"]`) {
		t.Errorf("table mode: %d %s", resp.StatusCode, body)
	}
}

func TestAggregationEndpoints(t *testing.T) {
	e := newEnv(t)
	e.querier.add(time.Now().Add(-time.Minute), "_msg", "x")
	v := e.login("viewer")
	for path, want := range map[string]string{
		"/api/v1/logs/histogram":           `"step":"1m"`,
		"/api/v1/logs/facets":              `"field":"hostname"`,
		"/api/v1/fields":                   `"name":"message","count":5,"count_approximate":true,"kind":"core"`,
		"/api/v1/fields/app_name/values":   `"value":"app_name-a"`,
		"/api/v1/dashboard/overview":       `"logs_in_range":1`,
		"/api/v1/dashboard/volume":         `"split_by":"severity"`,
		"/api/v1/dashboard/ingestion-rate": `"series":[]`,
	} {
		body := map[string]any{"time_range": map[string]string{"from": "now-1h", "to": "now"}, "fields": []string{"hostname"}}
		resp, data := v.do("POST", path, body, nil)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(data), want) {
			t.Errorf("%s: %d %s (want %s)", path, resp.StatusCode, data, want)
		}
		if strings.Contains(string(data), `"_stream"`) {
			t.Errorf("%s leaks internal fields: %s", path, data)
		}
	}
	resp, data := v.do("POST", "/api/v1/dashboard/top", map[string]any{"time_range": map[string]string{"from": "now-1h", "to": "now"}, "field": "hostname"}, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(data), "hostname-a") {
		t.Errorf("top: %d %s", resp.StatusCode, data)
	}
	resp, data = v.do("POST", "/api/v1/logs/stats", map[string]any{"time_range": map[string]string{"from": "now-1h", "to": "now"},
		"aggregations": []map[string]any{{"type": "count"}, {"type": "top", "field": "severity", "limit": 2}}}, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(data), `"value":1`) {
		t.Errorf("stats: %d %s", resp.StatusCode, data)
	}
	resp, data = v.do("POST", "/api/v1/query/validate", map[string]any{"filter": map[string]any{"op": "eq", "field": "a", "value": "b"}}, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(data), `"valid":true`) {
		t.Errorf("validate: %d %s", resp.StatusCode, data)
	}
}

func TestExport(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	e.querier.add(now.Add(-time.Minute), "_msg", "=HYPERLINK(\"http://evil\")", "hostname", "h1")
	e.querier.add(now.Add(-2*time.Minute), "_msg", "plain, with comma", "hostname", "h2")
	ops := e.login("ops")

	req := map[string]any{"time_range": map[string]string{"from": "now-1h", "to": "now"}, "fields": []string{"timestamp", "hostname", "message"}}
	resp, body := ops.do("POST", "/api/v1/logs/export?format=csv", req, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("csv: %d %v", resp.StatusCode, resp.Header)
	}
	records, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil || len(records) != 3 || records[0][1] != "hostname" {
		t.Fatalf("csv records: %v %v", records, err)
	}
	if !strings.HasPrefix(records[1][2], "'=") {
		t.Errorf("formula not neutralized: %q", records[1][2])
	}

	resp, body = ops.do("POST", "/api/v1/logs/export?format=ndjson", req, nil)
	sc := bufio.NewScanner(bytes.NewReader(body))
	lines := 0
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			t.Fatalf("invalid ndjson line %s", sc.Text())
		}
		lines++
	}
	if resp.StatusCode != http.StatusOK || lines != 2 {
		t.Errorf("ndjson: %d, %d lines", resp.StatusCode, lines)
	}
	// A limit equal to the row count is not a truncation; a smaller one is.
	for limit, wantMarker := range map[int]bool{2: false, 1: true} {
		limited := map[string]any{"time_range": req["time_range"], "fields": req["fields"], "limit": limit}
		_, body := ops.do("POST", "/api/v1/logs/export?format=ndjson", limited, nil)
		if got := strings.Contains(string(body), `"truncated":true`); got != wantMarker || bytes.Count(body, []byte("\n")) != limit+btoi(wantMarker) {
			t.Errorf("limit %d: truncation marker = %v, body %s", limit, got, body)
		}
	}
	resp, body = ops.do("POST", "/api/v1/logs/export?format=json", req, nil)
	var arr []map[string]any
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &arr) != nil || len(arr) != 2 {
		t.Errorf("json: %d %s", resp.StatusCode, body)
	}

	// Form fallback with CSRF token field.
	reqJSON, _ := json.Marshal(req)
	form := url.Values{"request": {string(reqJSON)}, "csrf_token": {ops.csrf}}
	noHeader := *ops
	noHeader.csrf = ""
	resp, _ = noHeader.do("POST", "/api/v1/logs/export?format=csv", form.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("form export: %d", resp.StatusCode)
	}
}

func TestSavedSearches(t *testing.T) {
	e := newEnv(t)
	ops, viewer := e.login("ops"), e.login("viewer")
	in := map[string]any{"name": "VPN failures", "description": "tunnels", "visibility": "shared",
		"query":   map[string]any{"filter": map[string]any{"op": "contains", "field": "message", "value": "VPN"}},
		"columns": []string{"timestamp", "message"}, "default_time_range": map[string]string{"from": "now-24h", "to": "now"}}
	resp, body := ops.do("POST", "/api/v1/saved-searches", in, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var ss map[string]any
	_ = json.Unmarshal(body, &ss)
	id := ss["id"].(string)

	if resp, _ := ops.do("POST", "/api/v1/saved-searches", in, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("duplicate name: %d", resp.StatusCode)
	}
	private := map[string]any{"name": "mine", "query": map[string]any{}, "visibility": "private"}
	ops.do("POST", "/api/v1/saved-searches", private, nil)
	_, body = viewer.do("GET", "/api/v1/saved-searches", nil, nil)
	if !strings.Contains(string(body), "VPN failures") || strings.Contains(string(body), `"mine"`) {
		t.Errorf("viewer list: %s", body)
	}
	if resp, _ := viewer.do("PUT", "/api/v1/saved-searches/"+id, map[string]any{"name": "x", "query": map[string]any{}, "version": 1}, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-owner update: %d", resp.StatusCode)
	}
	in["version"] = 1
	in["name"] = "VPN tunnel failures"
	if resp, body := ops.do("PUT", "/api/v1/saved-searches/"+id, in, nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"version":2`) {
		t.Errorf("update: %d %s", resp.StatusCode, body)
	}
	if resp, _ := ops.do("PUT", "/api/v1/saved-searches/"+id, in, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("stale version: %d", resp.StatusCode)
	}
	nativeSearch := map[string]any{"name": "native", "query": map[string]any{"native": map[string]string{"dialect": "logsql", "text": "error"}}}
	if resp, _ := viewer.do("POST", "/api/v1/saved-searches", nativeSearch, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer saving native query: %d", resp.StatusCode)
	}
	if resp, body := ops.do("POST", "/api/v1/saved-searches", nativeSearch, nil); resp.StatusCode != http.StatusCreated || !strings.Contains(string(body), `"dialect":"logsql"`) {
		t.Errorf("native saved search: %d %s", resp.StatusCode, body)
	}
	if resp, _ := ops.do("DELETE", "/api/v1/saved-searches/"+id, nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete: %d", resp.StatusCode)
	}
	if resp, _ := ops.do("GET", "/api/v1/saved-searches/"+id, nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("get deleted: %d", resp.StatusCode)
	}
}

func TestAPIKeysAndHTTPIngest(t *testing.T) {
	e := newEnv(t)
	ops := e.login("ops")
	resp, body := ops.do("POST", "/api/v1/api-keys", map[string]any{"name": "vector", "scopes": []string{"logs:ingest"}}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create key: %d %s", resp.StatusCode, body)
	}
	var created struct {
		Secret string `json:"secret"`
		APIKey struct {
			ID string `json:"id"`
		} `json:"api_key"`
	}
	_ = json.Unmarshal(body, &created)
	anon := e.anon()
	bearer := map[string]string{"Authorization": "Bearer " + created.Secret}

	// Sessions cannot ingest (no logs:ingest permission).
	if resp, _ := ops.do("POST", "/api/v1/ingest", `{"message":"x"}`, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("session ingest: %d", resp.StatusCode)
	}
	// Single object.
	resp, body = anon.do("POST", "/api/v1/ingest", `{"message":"one","level":"error"}`, bearer)
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(string(body), `"accepted":1`) {
		t.Errorf("object: %d %s", resp.StatusCode, body)
	}
	// Array with an invalid element.
	resp, body = anon.do("POST", "/api/v1/ingest", `[{"message":"a"}, 42, {"message":"b"}]`, bearer)
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(string(body), `"accepted":2,"rejected":1`) {
		t.Errorf("array: %d %s", resp.StatusCode, body)
	}
	// NDJSON, gzip, with a broken line.
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = io.WriteString(zw, "{\"message\":\"n1\"}\n\n{broken\n{\"message\":\"n2\"}\n")
	_ = zw.Close()
	headers := map[string]string{"Authorization": bearer["Authorization"], "Content-Type": "application/x-ndjson", "Content-Encoding": "gzip"}
	resp, body = anon.do("POST", "/api/v1/ingest", gz.Bytes(), headers)
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(string(body), `"accepted":2,"rejected":1`) || !strings.Contains(string(body), `"line":3`) {
		t.Errorf("ndjson: %d %s", resp.StatusCode, body)
	}
	e.sink.mu.Lock()
	if len(e.sink.msgs) != 5 || e.sink.msgs[0].Source.Name != "http-json" {
		t.Errorf("sink got %d messages", len(e.sink.msgs))
	}
	e.sink.mu.Unlock()

	// Too large (after decompression).
	big := `{"message":"` + strings.Repeat("x", 2<<20) + `"}`
	if resp, _ := anon.do("POST", "/api/v1/ingest", big, bearer); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize: %d", resp.StatusCode)
	}
	// Backpressure.
	e.sink.mu.Lock()
	e.sink.full = true
	e.sink.mu.Unlock()
	resp, _ = anon.do("POST", "/api/v1/ingest", `{"message":"x"}`, bearer)
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Errorf("backpressure: %d", resp.StatusCode)
	}
	e.sink.mu.Lock()
	e.sink.full = false
	e.sink.mu.Unlock()

	// Revoked keys stop working.
	if resp, _ := ops.do("DELETE", "/api/v1/api-keys/"+created.APIKey.ID, nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("revoke: %d", resp.StatusCode)
	}
	if resp, _ := anon.do("POST", "/api/v1/ingest", `{"message":"x"}`, bearer); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("revoked key: %d", resp.StatusCode)
	}
}

func TestTailSSE(t *testing.T) {
	e := newEnv(t)
	v := e.login("viewer")
	q := base64.RawURLEncoding.EncodeToString([]byte(`{"filter":{"op":"exists","field":"hostname"}}`))
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/v1/logs/tail?q="+q, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := v.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
	e.querier.tail <- storage.Row{{Key: "_time", Value: time.Now().UTC().Format(time.RFC3339Nano)}, {Key: "_msg", Value: "live one"}}
	sc := bufio.NewScanner(resp.Body)
	var event string
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") && event == "logs" {
			if !strings.Contains(line, "live one") {
				t.Errorf("unexpected data %s", line)
			}
			return
		}
	}
	t.Fatalf("no logs event received: %v", sc.Err())
}

func TestWebUIAndSystemEndpoints(t *testing.T) {
	e := newEnv(t)
	c := e.anon()
	resp, body := c.do("GET", "/logs/live?x=1", nil, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Syslogc UI") || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Errorf("SPA fallback: %d %s", resp.StatusCode, body)
	}
	if resp, _ := c.do("GET", "/assets/app-1.js", nil, nil); resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, _ := c.do("GET", "/assets/missing.js", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing asset: %d", resp.StatusCode)
	}
	if resp, body := c.do("GET", "/api/v1/nope", nil, nil); resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "not_found") {
		t.Errorf("unknown API path: %d %s", resp.StatusCode, body)
	}

	v := e.login("viewer")
	for path, want := range map[string]string{
		"/api/v1/system/health":    `"uptime_seconds"`,
		"/api/v1/system/ingestion": `"queue"`,
		"/api/v1/system/storage":   `"compressed_bytes":1234`,
	} {
		if resp, body := v.do("GET", path, nil, nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), want) {
			t.Errorf("%s: %d %s", path, resp.StatusCode, body)
		}
	}
}

func mustURL(s string) *url.URL {
	u, _ := url.Parse(s)
	return u
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestOriginAndClientIP(t *testing.T) {
	s := &Server{opts: Options{
		AllowedOrigins: []string{"https://syslogc.example.com"},
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	}}
	req := func(remote, origin, host string, xff ...string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://"+host+"/api/v1/auth/login", nil)
		r.RemoteAddr, r.Host = remote, host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		for _, v := range xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		return r
	}
	origins := []struct {
		origin, host string
		want         bool
	}{
		{"http://192.168.0.53:8080", "192.168.0.53:8080", true},
		{"https://syslogc.example.com", "192.168.0.53:8080", true}, // proxy rewrote Host
		{"https://SYSLOGC.example.com", "127.0.0.1:8080", true},
		{"http://syslogc.example.com", "192.168.0.53:8080", false}, // scheme differs
		{"https://evil.example.com", "192.168.0.53:8080", false},
		{"", "192.168.0.53:8080", true},
	}
	for _, o := range origins {
		if got := s.sameOrigin(req("1.2.3.4:5", o.origin, o.host)); got != o.want {
			t.Errorf("sameOrigin(%q, host %q) = %v", o.origin, o.host, got)
		}
	}
	ips := []struct {
		remote string
		xff    []string
		want   string
	}{
		{"203.0.113.9:4000", []string{"1.1.1.1"}, "203.0.113.9"},                       // untrusted peer: header ignored
		{"10.0.0.2:4000", []string{"198.51.100.7"}, "198.51.100.7"},                    // trusted proxy
		{"10.0.0.2:4000", []string{"6.6.6.6, 198.51.100.7, 10.0.0.9"}, "198.51.100.7"}, // spoofed left-most entry ignored
		{"10.0.0.2:4000", []string{"6.6.6.6", "198.51.100.7"}, "198.51.100.7"},         // multiple headers
		{"10.0.0.2:4000", nil, "10.0.0.2"},
		{"[::ffff:10.0.0.2]:4000", []string{"garbage"}, "10.0.0.2"},
	}
	for _, c := range ips {
		if got := s.clientIP(req(c.remote, "", "h", c.xff...)); got != c.want {
			t.Errorf("clientIP(%s, %v) = %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}
