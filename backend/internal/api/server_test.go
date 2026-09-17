package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/freezxp/syslogc/backend/internal/metrics"
	"github.com/freezxp/syslogc/backend/internal/storage"
)

type fakeQuerier struct {
	got  storage.SearchQuery
	rows []storage.Row
	err  error
}

func (f *fakeQuerier) Search(_ context.Context, q storage.SearchQuery) (storage.Rows, error) {
	f.got = q
	if f.err != nil {
		return nil, f.err
	}
	return &sliceRows{rows: f.rows, i: -1}, nil
}

type sliceRows struct {
	rows []storage.Row
	i    int
}

func (s *sliceRows) Next() bool       { s.i++; return s.i < len(s.rows) }
func (s *sliceRows) Row() storage.Row { return s.rows[s.i] }
func (s *sliceRows) Err() error       { return nil }
func (s *sliceRows) Close() error     { return nil }

func newServer(t *testing.T, checks []ReadinessCheck, q storage.LogQuerier) *Server {
	t.Helper()
	return New(Options{
		Metrics:    metrics.New("test", "abc"),
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Checks:     checks,
		DevQuerier: q,
		NodeID:     "n1",
		Version:    "test",
		Extra:      func() map[string]any { return map[string]any{"sources": []string{"s1"}} },
	})
}

func do(t *testing.T, s *Server, target string) (*http.Response, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestHealthAndSecureHeaders(t *testing.T) {
	s := newServer(t, nil, nil)
	resp, body := do(t, s, "/health")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"ok"`) {
		t.Errorf("health: %d %s", resp.StatusCode, body)
	}
	for _, h := range []string{"X-Content-Type-Options", "Content-Security-Policy", "Referrer-Policy"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("missing security header %s", h)
		}
	}
}

func TestReady(t *testing.T) {
	healthy := true
	s := newServer(t, []ReadinessCheck{{Name: "storage", Check: func(context.Context) error {
		if healthy {
			return nil
		}
		return errors.New("down")
	}}}, nil)

	resp, body := do(t, s, "/ready")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"status":"ready"`) || !strings.Contains(body, `"sources"`) {
		t.Errorf("ready: %d %s", resp.StatusCode, body)
	}

	healthy = false
	resp, body = do(t, s, "/ready")
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, `"error":"down"`) {
		t.Errorf("not ready: %d %s", resp.StatusCode, body)
	}

	healthy = true
	s.SetDraining()
	resp, body = do(t, s, "/ready")
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, `"status":"draining"`) {
		t.Errorf("draining: %d %s", resp.StatusCode, body)
	}
}

func TestMetricsEndpointAndRequestMetrics(t *testing.T) {
	s := newServer(t, nil, nil)
	do(t, s, "/health")
	resp, body := do(t, s, "/metrics")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics: %d", resp.StatusCode)
	}
	for _, want := range []string{
		`syslogc_build_info{commit="abc",version="test"} 1`,
		`syslogc_http_requests_total{code="200",method="GET",route="GET /health"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

func TestDevSearchDisabledByDefault(t *testing.T) {
	s := newServer(t, nil, nil)
	if resp, _ := do(t, s, "/api/v1/dev/search?query=x"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("dev search reachable without being enabled: %d", resp.StatusCode)
	}
}

func TestDevSearch(t *testing.T) {
	q := &fakeQuerier{rows: []storage.Row{
		{{Key: "_time", Value: "2026-09-14T10:00:00Z"}, {Key: "_msg", Value: `hello "world" <script>`}},
	}}
	s := newServer(t, nil, q)

	resp, body := do(t, s, "/api/v1/dev/search?query=hostname:fw01&from=2h&limit=5&fields=_time,_msg")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dev search: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Rows []map[string]string `json:"rows"`
		Meta struct {
			Returned int `json:"returned"`
		} `json:"meta"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("invalid JSON %s: %v", body, err)
	}
	if out.Meta.Returned != 1 || out.Rows[0]["_msg"] != `hello "world" <script>` {
		t.Errorf("unexpected body: %s", body)
	}
	if strings.Contains(body, "<script>") {
		t.Error("HTML characters not escaped in JSON output")
	}
	if q.got.Limit != 5 || q.got.Native.Text != "hostname:fw01" || len(q.got.Fields) != 2 ||
		q.got.Range.End.Sub(q.got.Range.Start).Hours() != 2 {
		t.Errorf("query not mapped correctly: %+v", q.got)
	}

	for _, target := range []string{
		"/api/v1/dev/search?limit=0",
		"/api/v1/dev/search?limit=5000",
		"/api/v1/dev/search?from=yesterday",
		"/api/v1/dev/search?from=2000h",
		"/api/v1/dev/search?to=notatime",
	} {
		if resp, body := do(t, s, target); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s", target, resp.StatusCode, body)
		}
	}

	q.err = &storage.QueryError{StatusCode: 400, Message: "cannot parse query", UserFacing: true}
	if resp, body := do(t, s, "/api/v1/dev/search?query=bad("); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "cannot parse") {
		t.Errorf("syntax error: %d %s", resp.StatusCode, body)
	}
	q.err = errors.New("connection refused to 10.0.0.5")
	if resp, body := do(t, s, "/api/v1/dev/search?query=x"); resp.StatusCode != http.StatusBadGateway || strings.Contains(body, "10.0.0.5") {
		t.Errorf("internal error leaked or wrong status: %d %s", resp.StatusCode, body)
	}
}
