//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/freezxp/syslogc/backend/internal/app"
)

type apiClient struct {
	base string
	http *http.Client
	csrf string
}

func login(t *testing.T, a *app.App) *apiClient {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &apiClient{base: "http://" + a.Server().Addr().String(), http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	resp, body := c.post(t, "/api/v1/auth/login", map[string]string{"username": "admin", "password": adminPassword})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	var s struct {
		CSRFToken string `json:"csrf_token"`
	}
	_ = json.Unmarshal(body, &s)
	c.csrf = s.CSRFToken
	return c
}

func (c *apiClient) do(t *testing.T, method, path string, body io.Reader, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, c.base+path, body)
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func (c *apiClient) post(t *testing.T, path string, v any) (*http.Response, []byte) {
	t.Helper()
	data, _ := json.Marshal(v)
	return c.do(t, "POST", path, bytes.NewReader(data), map[string]string{"Content-Type": "application/json"})
}

// waitSearch polls the search API until n rows match the text filter.
func waitSearch(t *testing.T, c *apiClient, text string, n int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, body := c.post(t, "/api/v1/logs/stats", map[string]any{
			"time_range":   map[string]string{"from": "now-48h", "to": "now+1h"},
			"filter":       map[string]any{"op": "text", "value": text},
			"aggregations": []map[string]string{{"type": "count"}},
		})
		var sr struct {
			Results []struct {
				Value int `json:"value"`
			} `json:"results"`
		}
		_ = json.Unmarshal(body, &sr)
		if len(sr.Results) == 1 && sr.Results[0].Value >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %d rows matching %q: %s", n, text, body)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// TestQueryAPIAgainstVictoriaLogs covers API → Storage → Query end to end:
// HTTP ingestion with an API key, search with tie-group pagination over
// second-precision timestamps, histogram, facets, fields, export and tail.
func TestQueryAPIAgainstVictoriaLogs(t *testing.T) {
	a := startApp(t, nil)
	c := login(t, a)
	id := runID()

	// API key for ingestion.
	resp, body := c.post(t, "/api/v1/api-keys", map[string]any{"name": "it", "scopes": []string{"logs:ingest"}})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create key: %d %s", resp.StatusCode, body)
	}
	var key struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(body, &key)

	// 250 JSON logs sharing one second (tie group) + 50 in earlier seconds.
	ts := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	var nd bytes.Buffer
	for i := range 300 {
		t0 := ts
		if i >= 250 {
			t0 = ts.Add(-time.Duration(i-249) * time.Second)
		}
		sev := []string{"info", "warning", "error"}[i%3]
		fmt.Fprintf(&nd, `{"timestamp":%q,"message":"api %s n%d","host":"web-%d","level":%q,"service":"checkout","http":{"status":%d},"source_ip":"10.0.0.%d"}`+"\n",
			t0.Format(time.RFC3339), id, i, i%4, sev, 200+i%5, i%250)
	}
	ingestClient := &apiClient{base: c.base, http: &http.Client{Timeout: 30 * time.Second}}
	resp, body = ingestClient.do(t, "POST", "/api/v1/ingest", &nd, map[string]string{
		"Authorization": "Bearer " + key.Secret, "Content-Type": "application/x-ndjson"})
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(string(body), `"accepted":300`) {
		t.Fatalf("ingest: %d %s", resp.StatusCode, body)
	}
	waitSearch(t, c, id, 300)

	selection := map[string]any{
		"time_range": map[string]string{"from": "now-1h", "to": "now"},
		"filter":     map[string]any{"op": "text", "value": id},
	}

	// Pagination: limit 100 forces a page boundary inside the 250-row tie group.
	seen := map[string]bool{}
	var cursor any
	pages := 0
	for {
		req := map[string]any{"time_range": selection["time_range"], "filter": selection["filter"], "limit": 100,
			"fields": []string{"timestamp", "message"}, "cursor": cursor}
		resp, body := c.post(t, "/api/v1/logs/search", req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("search page %d: %d %s", pages, resp.StatusCode, body)
		}
		var sr struct {
			Rows []map[string]any `json:"rows"`
			Page struct {
				NextCursor  *string `json:"next_cursor"`
				TieOverflow bool    `json:"tie_overflow"`
			} `json:"page"`
		}
		_ = json.Unmarshal(body, &sr)
		pages++
		for _, r := range sr.Rows {
			m := r["message"].(string)
			if seen[m] {
				t.Fatalf("duplicate %q on page %d", m, pages)
			}
			seen[m] = true
		}
		if sr.Page.NextCursor == nil || pages > 20 {
			break
		}
		cursor = *sr.Page.NextCursor
	}
	if len(seen) != 300 {
		t.Errorf("pagination returned %d unique rows in %d pages, want 300", len(seen), pages)
	}

	// Structured fields from JSON are queryable with typed operators.
	resp, body = c.post(t, "/api/v1/logs/stats", map[string]any{"time_range": selection["time_range"],
		"filter": map[string]any{"op": "and", "args": []any{selection["filter"],
			map[string]any{"op": "gte", "field": "http.status", "value": 203},
			map[string]any{"op": "cidr", "field": "source_ip", "value": "10.0.0.0/24"}}},
		"aggregations": []map[string]any{{"type": "count"}, {"type": "count_distinct", "field": "hostname"}, {"type": "top", "field": "severity", "limit": 3}}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"value":120`) || !strings.Contains(string(body), `"value":4`) {
		t.Errorf("stats with typed filters: %d %s", resp.StatusCode, body)
	}

	resp, body = c.post(t, "/api/v1/logs/histogram", map[string]any{"time_range": selection["time_range"], "filter": selection["filter"], "split_by": "severity"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"total":300`) || !strings.Contains(string(body), `"error"`) {
		t.Errorf("histogram: %d %s", resp.StatusCode, body)
	}
	resp, body = c.post(t, "/api/v1/logs/facets", map[string]any{"time_range": selection["time_range"], "filter": selection["filter"], "fields": []string{"severity", "hostname"}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `{"value":"info","count":100}`) {
		t.Errorf("facets: %d %s", resp.StatusCode, body)
	}
	resp, body = c.post(t, "/api/v1/fields", selection)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"name":"http.status"`) || !strings.Contains(string(body), `"kind":"dynamic"`) {
		t.Errorf("fields: %d %s", resp.StatusCode, body)
	}
	resp, body = c.post(t, "/api/v1/fields/hostname/values", map[string]any{"time_range": selection["time_range"], "filter": selection["filter"], "search": "WEB-3"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"web-3"`) || strings.Contains(string(body), `"web-1"`) {
		t.Errorf("field values: %d %s", resp.StatusCode, body)
	}
	resp, body = c.post(t, "/api/v1/query/validate", map[string]any{"native": map[string]string{"dialect": "logsql", "text": "severity:=error |"}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"valid":false`) {
		t.Errorf("validate invalid native: %d %s", resp.StatusCode, body)
	}
	resp, body = c.post(t, "/api/v1/logs/search", map[string]any{"time_range": selection["time_range"],
		"native": map[string]string{"dialect": "logsql", "text": id + " | stats by (severity) count() hits"}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"mode":"table"`) {
		t.Errorf("native stats table: %d %s", resp.StatusCode, body)
	}

	// Export streams all rows.
	resp, body = c.post(t, "/api/v1/logs/export?format=ndjson", map[string]any{"time_range": selection["time_range"], "filter": selection["filter"]})
	if lines := bytes.Count(body, []byte("\n")); resp.StatusCode != http.StatusOK || lines != 300 {
		t.Errorf("export: %d, %d lines", resp.StatusCode, lines)
	}

	// Dashboard overview counts real data.
	resp, body = c.post(t, "/api/v1/dashboard/overview", map[string]any{"time_range": map[string]string{"from": "now-1h", "to": "now"}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"logs_in_range"`) {
		t.Errorf("overview: %d %s", resp.StatusCode, body)
	}

	// Live tail receives a message sent after subscribing.
	tailID := runID()
	q := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"filter":{"op":"text","value":%q}}`, tailID)))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1/logs/tail?q="+q+"&start_offset=1s", nil)
	tailResp, err := (&http.Client{Jar: c.http.Jar}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer tailResp.Body.Close()
	go func() {
		time.Sleep(time.Second)
		conn, err := net.Dial("udp", a.Supervisor().Addr("it-udp").String())
		if err == nil {
			for range 3 {
				fmt.Fprintf(conn, "<14>1 - tailhost app - - - tail %s", tailID)
				time.Sleep(500 * time.Millisecond)
			}
			conn.Close()
		}
	}()
	sc := bufio.NewScanner(tailResp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	got := false
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: ") && strings.Contains(sc.Text(), tailID) {
			got = true
			break
		}
	}
	if !got {
		t.Error("live tail did not deliver the message")
	}

	// Saved search round trip.
	resp, body = c.post(t, "/api/v1/saved-searches", map[string]any{"name": "it " + id, "query": map[string]any{"filter": selection["filter"]}})
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("saved search: %d %s", resp.StatusCode, body)
	}
}

// TestManagedSourceLifecycle covers the Phase 5 exit criterion: a source
// created in the API starts receiving within seconds and without a restart,
// and disabling it stops the listener.
func TestManagedSourceLifecycle(t *testing.T) {
	a := startApp(t, nil)
	c := login(t, a)
	id := runID()

	port := freePort(t)
	body := map[string]any{"config": map[string]any{
		"name": "managed-" + id, "type": "syslog", "protocol": "udp", "address": fmt.Sprintf("127.0.0.1:%d", port),
		"labels": map[string]string{"site": "branch"},
	}}
	resp, raw := c.post(t, "/api/v1/sources", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create source: %d %s", resp.StatusCode, raw)
	}
	var created struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	_ = json.Unmarshal(raw, &created)

	// The listener appears without restarting the process.
	deadline := time.Now().Add(20 * time.Second)
	var conn net.Conn
	for {
		var err error
		if conn, err = net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			fmt.Fprintf(conn, "<14>1 - managedhost app - - - managed %s", id)
			conn.Close()
		}
		_, sr := c.post(t, "/api/v1/logs/stats", map[string]any{
			"time_range":   map[string]string{"from": "now-15m", "to": "now+1m"},
			"filter":       map[string]any{"op": "text", "value": id},
			"aggregations": []map[string]string{{"type": "count"}},
		})
		if strings.Contains(string(sr), `"value":1`) || strings.Contains(string(sr), `"value":2`) {
			break
		}
		if time.Now().After(deadline) {
			_, st := c.do(t, "GET", "/api/v1/sources", nil, nil)
			t.Fatalf("managed source did not receive logs: %s\nsources: %s", sr, st)
		}
		time.Sleep(time.Second)
	}

	// The source reports as running, with its origin.
	_, raw = c.do(t, "GET", "/api/v1/sources", nil, nil)
	if !strings.Contains(string(raw), `"origin":"database"`) || !strings.Contains(string(raw), `"state":"running"`) {
		t.Errorf("source status: %s", raw)
	}

	// Disabling frees the port.
	body["enabled"] = false
	body["version"] = created.Version
	if resp, raw := c.do(t, "PUT", "/api/v1/sources/"+created.ID, jsonBody(body), map[string]string{"Content-Type": "application/json"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("disable source: %d %s", resp.StatusCode, raw)
	}
	stopped := false
	for until := time.Now().Add(20 * time.Second); time.Now().Before(until); {
		if ln, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			ln.Close()
			stopped = true
			break
		}
		time.Sleep(time.Second)
	}
	if !stopped {
		t.Error("disabled source kept its port bound")
	}
}

// freePort returns a port that is free right now.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.LocalAddr().(*net.UDPAddr).Port
}

func jsonBody(v any) io.Reader {
	data, _ := json.Marshal(v)
	return bytes.NewReader(data)
}

// TestAnalyticsAgainstVictoriaLogs checks the aggregation primitives against
// real data: group counts, distinct counts and bucketed series.
func TestAnalyticsAgainstVictoriaLogs(t *testing.T) {
	a := startApp(t, nil)
	c := login(t, a)
	id := runID()

	resp, body := c.post(t, "/api/v1/api-keys", map[string]any{"name": "an", "scopes": []string{"logs:ingest"}})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create key: %d %s", resp.StatusCode, body)
	}
	var key struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(body, &key)

	// Two minutes of traffic: three hosts with known shares, two clients each.
	base := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Minute)
	hosts := map[string]int{"web-1": 30, "web-2": 20, "db-1": 10}
	var nd bytes.Buffer
	for host, n := range hosts {
		for i := range n {
			ts := base.Add(time.Duration(i%2) * time.Minute)
			fmt.Fprintf(&nd, `{"timestamp":%q,"message":"analytics %s","host":%q,"level":"info","source_ip":"10.1.0.%d"}`+"\n",
				ts.Format(time.RFC3339), id, host, i%2)
		}
	}
	ingest := &apiClient{base: c.base, http: &http.Client{Timeout: 30 * time.Second}}
	resp, body = ingest.do(t, "POST", "/api/v1/ingest", &nd, map[string]string{
		"Authorization": "Bearer " + key.Secret, "Content-Type": "application/x-ndjson"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("ingest: %d %s", resp.StatusCode, body)
	}
	waitSearch(t, c, id, 60)

	sel := map[string]any{"time_range": map[string]string{"from": "now-15m", "to": "now+1m"},
		"filter": map[string]any{"op": "text", "value": id}}

	// Breakdown: exact counts, honest shares, and the number of groups.
	resp, body = c.post(t, "/api/v1/analytics/breakdown", map[string]any{"time_range": sel["time_range"],
		"filter": sel["filter"], "group_by": "hostname", "limit": 2})
	var bd struct {
		Rows []struct {
			Value  string  `json:"value"`
			Metric float64 `json:"metric"`
			Share  float64 `json:"share"`
		} `json:"rows"`
		Total          float64 `json:"total"`
		DistinctGroups int64   `json:"distinct_groups"`
	}
	if err := json.Unmarshal(body, &bd); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("breakdown: %d %s", resp.StatusCode, body)
	}
	if len(bd.Rows) != 2 || bd.Rows[0].Value != "web-1" || bd.Rows[0].Metric != 30 || bd.Rows[1].Metric != 20 {
		t.Errorf("breakdown rows = %+v", bd.Rows)
	}
	if bd.Total != 60 || bd.DistinctGroups != 3 {
		t.Errorf("breakdown total = %v over %d groups, want 60 over 3", bd.Total, bd.DistinctGroups)
	}
	if share := bd.Rows[0].Share; share < 0.49 || share > 0.51 {
		t.Errorf("top share = %v, want 0.5", share)
	}

	// Distinct metric: two client addresses per host.
	resp, body = c.post(t, "/api/v1/analytics/breakdown", map[string]any{"time_range": sel["time_range"],
		"filter": sel["filter"], "group_by": "hostname", "metric": map[string]string{"type": "count_distinct", "field": "source_ip"}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"metric":2`) {
		t.Errorf("distinct breakdown: %d %s", resp.StatusCode, body)
	}

	// Series: one bucket per minute, counts split by host.
	resp, body = c.post(t, "/api/v1/analytics/series", map[string]any{"time_range": sel["time_range"],
		"filter": sel["filter"], "group_by": "hostname", "buckets": 15, "limit": 3})
	var sr struct {
		StepSeconds int64 `json:"step_seconds"`
		Timestamps  []any `json:"timestamps"`
		Groups      []struct {
			Value  string    `json:"value"`
			Total  float64   `json:"total"`
			Points []float64 `json:"points"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(body, &sr); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("series: %d %s", resp.StatusCode, body)
	}
	if len(sr.Groups) != 3 {
		t.Fatalf("series groups = %d, want 3: %s", len(sr.Groups), body)
	}
	total := 0.0
	for _, g := range sr.Groups {
		if len(g.Points) != len(sr.Timestamps) {
			t.Fatalf("%s: %d points for %d timestamps", g.Value, len(g.Points), len(sr.Timestamps))
		}
		sum := 0.0
		for _, p := range g.Points {
			sum += p
		}
		if sum != g.Total {
			t.Errorf("%s: points sum to %v but total is %v", g.Value, sum, g.Total)
		}
		total += g.Total
	}
	if total != 60 {
		t.Errorf("series total = %v, want 60", total)
	}
	// Pipes belong to the explorer, not to analytics.
	resp, body = c.post(t, "/api/v1/analytics/breakdown", map[string]any{"time_range": sel["time_range"],
		"group_by": "hostname", "native": map[string]string{"dialect": "logsql", "text": "* | stats count()"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "pipes") {
		t.Errorf("native pipes in analytics: %d %s", resp.StatusCode, body)
	}
}
