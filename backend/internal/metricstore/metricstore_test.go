package metricstore

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{URL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestWriteEncodesTheExpositionFormat(t *testing.T) {
	var got string
	var path string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got, path = string(b), r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	at := time.Unix(1700000000, 0)
	err := c.Write(context.Background(), []Sample{
		{Name: "syslogc_dns_service_unique_clients", Labels: map[string]string{"window": "5m", "service": "tiktok"}, Value: 42, At: at},
		{Name: "syslogc_dns_service_queries", Labels: map[string]string{"service": "x"}, Value: 1.5, At: at},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/v1/import/prometheus" {
		t.Errorf("path = %q", path)
	}
	want := `syslogc_dns_service_unique_clients{service="tiktok",window="5m"} 42 1700000000000` + "\n" +
		`syslogc_dns_service_queries{service="x"} 1.5 1700000000000` + "\n"
	if got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}
}

func TestWriteEscapesLabelValues(t *testing.T) {
	var buf bytes.Buffer
	// A service name someone typed is the only untrusted text on the wire.
	err := encodeSample(&buf, &Sample{
		Name:   "syslogc_dns_service_unique_clients",
		Labels: map[string]string{"service": `ev"il\` + "\n" + `{x="y"}`},
		Value:  1,
		At:     time.Unix(0, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(buf.String(), "\n")
	// One line, and every quote inside the value is escaped, so the value
	// cannot close the label set early or forge another label.
	if strings.Contains(line, "\n") {
		t.Errorf("the value broke the line: %q", buf.String())
	}
	body, ok := strings.CutPrefix(line, `syslogc_dns_service_unique_clients{service="`)
	if !ok {
		t.Fatalf("unexpected line: %s", line)
	}
	value, rest, ok := strings.Cut(body, `"} `)
	if !ok || rest != "1 0" {
		t.Fatalf("unexpected line: %s", line)
	}
	if want := `ev\"il\\\n{x=\"y\"}`; value != want {
		t.Errorf("value = %s, want %s", value, want)
	}
}

func TestWriteRejectsUnusableNames(t *testing.T) {
	var buf bytes.Buffer
	for _, s := range []Sample{
		{Name: "", Value: 1},
		{Name: "bad-name", Value: 1},
		{Name: "9leading", Value: 1},
		{Name: "ok", Labels: map[string]string{"bad-label": "v"}, Value: 1},
	} {
		if err := encodeSample(&buf, &s); err == nil {
			t.Errorf("%q with labels %v was accepted", s.Name, s.Labels)
		}
	}
}

func TestWriteIsANoopWithoutSamples(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a request was sent for zero samples")
	}))
	if err := c.Write(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestWriteReportsServerErrors(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "too many lines", http.StatusBadRequest)
	}))
	err := c.Write(context.Background(), []Sample{{Name: "m", Value: 1}})
	if err == nil || !strings.Contains(err.Error(), "too many lines") {
		t.Errorf("error = %v, want it to carry the server's message", err)
	}
}

func TestQueryRangeParsesSeries(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("step") != "300" || r.Form.Get("query") == "" {
			t.Errorf("form = %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"success","data":{"result":[
			{"metric":{"service":"tiktok"},"values":[[1700000000,"12"],[1700000300,"NaN"],[1700000600,"15.5"]]}
		]}}`)
	}))
	got, err := c.QueryRange(context.Background(), RangeQuery{
		Query: `syslogc_dns_service_unique_clients{window="5m"}`,
		Start: time.Unix(1700000000, 0),
		End:   time.Unix(1700000600, 0),
		Step:  5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Labels["service"] != "tiktok" {
		t.Fatalf("series = %+v", got)
	}
	// NaN is a gap, not a value.
	if len(got[0].Points) != 2 || got[0].Points[0].Value != 12 || got[0].Points[1].Value != 15.5 {
		t.Errorf("points = %+v", got[0].Points)
	}
	if !got[0].Points[0].At.Equal(time.Unix(1700000000, 0).UTC()) {
		t.Errorf("timestamp = %v", got[0].Points[0].At)
	}
}

func TestQueryRangeReportsAFailedQuery(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"status":"error","error":"unexpected token"}`)
	}))
	_, err := c.QueryRange(context.Background(), RangeQuery{
		Query: "oops(", Start: time.Unix(1, 0), End: time.Unix(2, 0), Step: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "unexpected token") {
		t.Errorf("error = %v", err)
	}
}

func TestQueryRangeValidatesTheWindow(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	now := time.Now()
	for _, q := range []RangeQuery{
		{Query: "", Start: now, End: now.Add(time.Hour), Step: time.Minute},
		{Query: "m", Start: now, End: now, Step: time.Minute},
		{Query: "m", Start: now, End: now.Add(time.Hour)},
	} {
		if _, err := c.QueryRange(context.Background(), q); err == nil {
			t.Errorf("%+v was accepted", q)
		}
	}
}

func TestNewValidatesTheURL(t *testing.T) {
	for _, u := range []string{"", "ftp://vm:8428", "not a url", "/relative"} {
		if _, err := New(Config{URL: u}); err == nil {
			t.Errorf("%q was accepted", u)
		}
	}
	if _, err := New(Config{URL: "http://vm:8428/"}); err != nil {
		t.Errorf("a valid URL was rejected: %v", err)
	}
}
