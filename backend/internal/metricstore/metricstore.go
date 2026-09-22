// Package metricstore reads and writes time series in VictoriaMetrics.
//
// Syslogc stores derived measurements here — counts that are expensive to
// recompute from logs and that should outlive log retention, such as how many
// distinct clients queried a service in a window. Logs stay in VictoriaLogs;
// this holds only the small series rolled up from them.
//
// Writes use the Prometheus text exposition format with explicit timestamps
// (/api/v1/import/prometheus), so a sample can be written for a window that
// has already passed — that is what makes backfilling history possible.
package metricstore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Limits on what one call may carry.
const (
	MaxSamplesPerWrite = 10000
	MaxLabelsPerSample = 16
	maxResponseBytes   = 8 << 20
)

// Config configures the client.
type Config struct {
	// URL is the base URL of a VictoriaMetrics single-node instance.
	URL string
	// Timeout bounds one request (default 30s).
	Timeout time.Duration
	// BasicUsername and BasicPassword authenticate if set.
	BasicUsername string
	BasicPassword string
}

// Sample is one measurement.
type Sample struct {
	// Name is the metric name, e.g. syslogc_dns_service_unique_clients.
	Name string
	// Labels are the series labels; values are escaped when encoded.
	Labels map[string]string
	Value  float64
	// At is the sample timestamp. The zero value means "now".
	At time.Time
}

// Client talks to VictoriaMetrics.
type Client struct {
	base    *url.URL
	client  *http.Client
	user    string
	pass    string
	timeout time.Duration
}

// New validates cfg and returns a client.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("metricstore: URL is required")
	}
	u, err := url.Parse(strings.TrimRight(cfg.URL, "/"))
	if err != nil {
		return nil, fmt.Errorf("metricstore: URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("metricstore: URL scheme %q is not http or https", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("metricstore: URL has no host")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &Client{
		base: u,
		client: &http.Client{
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 5 * time.Second,
			},
		},
		user:    cfg.BasicUsername,
		pass:    cfg.BasicPassword,
		timeout: cfg.Timeout,
	}, nil
}

// Ping reports whether the instance is reachable and ready.
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base.JoinPath("/health").String(), nil)
	if err != nil {
		return err
	}
	c.authorize(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("metricstore: %w", err)
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("metricstore: health returned %s", resp.Status)
	}
	return nil
}

// Write stores samples. Writing is idempotent for a given series and
// timestamp: the same sample written twice replaces itself rather than
// accumulating, so a re-run of a rollup cannot double-count.
func (c *Client) Write(ctx context.Context, samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	if len(samples) > MaxSamplesPerWrite {
		return fmt.Errorf("metricstore: %d samples exceeds the limit of %d", len(samples), MaxSamplesPerWrite)
	}
	var body bytes.Buffer
	for i := range samples {
		if err := encodeSample(&body, &samples[i]); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base.JoinPath("/api/v1/import/prometheus").String(), bytes.NewReader(body.Bytes()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain")
	c.authorize(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("metricstore: write: %w", err)
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("metricstore: write returned %s: %s", resp.Status, snippet(resp.Body))
	}
	return nil
}

// Series is one time series returned by a range query.
type Series struct {
	Labels map[string]string
	Points []Point
}

// Point is one value at one instant.
type Point struct {
	At    time.Time
	Value float64
}

// RangeQuery asks for one metric over a window.
type RangeQuery struct {
	// Query is PromQL. Callers must build it from trusted input only: this
	// package does not parse or restrict it.
	Query string
	Start time.Time
	End   time.Time
	Step  time.Duration
	// NoCache bypasses the server's result cache. VictoriaMetrics caches
	// answers for windows that have already passed, so a reader that just
	// wrote samples for such a window — a rollup filling in history, or a
	// test — can otherwise be served the answer from before the write.
	NoCache bool
}

// QueryRange evaluates a PromQL query over a time range.
func (c *Client) QueryRange(ctx context.Context, q RangeQuery) ([]Series, error) {
	if strings.TrimSpace(q.Query) == "" {
		return nil, errors.New("metricstore: query is required")
	}
	if !q.End.After(q.Start) {
		return nil, errors.New("metricstore: end must be after start")
	}
	if q.Step <= 0 {
		return nil, errors.New("metricstore: step is required")
	}
	form := url.Values{
		"query": {q.Query},
		"start": {strconv.FormatInt(q.Start.Unix(), 10)},
		"end":   {strconv.FormatInt(q.End.Unix(), 10)},
		"step":  {strconv.FormatInt(int64(q.Step.Seconds()), 10)},
	}
	if q.NoCache {
		form.Set("nocache", "1")
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base.JoinPath("/api/v1/query_range").String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.authorize(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metricstore: query: %w", err)
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metricstore: query returned %s: %s", resp.Status, snippet(resp.Body))
	}
	var out struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Values [][2]json.RawMessage
			} `json:"result"`
		} `json:"data"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return nil, fmt.Errorf("metricstore: decoding the response: %w", err)
	}
	if out.Status != "success" {
		return nil, fmt.Errorf("metricstore: query failed: %s", out.Error)
	}
	series := make([]Series, 0, len(out.Data.Result))
	for _, r := range out.Data.Result {
		s := Series{Labels: r.Metric, Points: make([]Point, 0, len(r.Values))}
		for _, v := range r.Values {
			var at float64
			if err := json.Unmarshal(v[0], &at); err != nil {
				return nil, fmt.Errorf("metricstore: sample timestamp: %w", err)
			}
			// Values arrive as JSON strings ("1234"), including NaN and Inf,
			// which are not valid JSON numbers.
			var raw string
			if err := json.Unmarshal(v[1], &raw); err != nil {
				return nil, fmt.Errorf("metricstore: sample value: %w", err)
			}
			// ParseFloat accepts "NaN" and "Inf", which VictoriaMetrics uses
			// for gaps and overflow; neither is a measurement.
			value, err := strconv.ParseFloat(raw, 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			s.Points = append(s.Points, Point{At: time.UnixMilli(int64(at * 1000)).UTC(), Value: value})
		}
		series = append(series, s)
	}
	return series, nil
}

func (c *Client) authorize(req *http.Request) {
	if c.user != "" || c.pass != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
}

// encodeSample writes one line of the Prometheus exposition format.
func encodeSample(w *bytes.Buffer, s *Sample) error {
	if err := validMetricName(s.Name); err != nil {
		return err
	}
	if len(s.Labels) > MaxLabelsPerSample {
		return fmt.Errorf("metricstore: %s: %d labels exceeds the limit of %d", s.Name, len(s.Labels), MaxLabelsPerSample)
	}
	w.WriteString(s.Name)
	if len(s.Labels) > 0 {
		// Sorted so a line is reproducible, which makes tests and diffs sane.
		keys := make([]string, 0, len(s.Labels))
		for k := range s.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		w.WriteByte('{')
		for i, k := range keys {
			if err := validLabelName(k); err != nil {
				return fmt.Errorf("metricstore: %s: %w", s.Name, err)
			}
			if i > 0 {
				w.WriteByte(',')
			}
			w.WriteString(k)
			w.WriteString(`="`)
			writeEscaped(w, s.Labels[k])
			w.WriteByte('"')
		}
		w.WriteByte('}')
	}
	at := s.At
	if at.IsZero() {
		at = time.Now()
	}
	fmt.Fprintf(w, " %s %d\n", strconv.FormatFloat(s.Value, 'f', -1, 64), at.UnixMilli())
	return nil
}

// writeEscaped escapes a label value, which is the only place untrusted text
// (a service name someone typed) reaches the wire format.
func writeEscaped(w *bytes.Buffer, v string) {
	for _, r := range v {
		switch r {
		case '\\':
			w.WriteString(`\\`)
		case '"':
			w.WriteString(`\"`)
		case '\n':
			w.WriteString(`\n`)
		default:
			w.WriteRune(r)
		}
	}
}

func validMetricName(name string) error {
	if name == "" {
		return errors.New("metricstore: metric name is required")
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_', r == ':':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return fmt.Errorf("metricstore: metric name %q has an unexpected character %q", name, r)
		}
	}
	return nil
}

func validLabelName(name string) error {
	if name == "" {
		return errors.New("label name is required")
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return fmt.Errorf("label name %q has an unexpected character %q", name, r)
		}
	}
	return nil
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

// snippet reads the start of an error body for the error message.
func snippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(bufio.NewReader(r), 512))
	return strings.TrimSpace(string(b))
}
