package query

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/metadata"
	"github.com/freezxp/syslogc/backend/internal/storage"
)

var now = time.Date(2026, 9, 14, 15, 37, 42, 0, time.UTC)

func TestResolve(t *testing.T) {
	tests := []struct {
		tr         TimeRange
		start, end string
	}{
		{TimeRange{From: "now-1h", To: "now"}, "2026-09-14T14:37:42Z", "2026-09-14T15:37:42Z"},
		{TimeRange{From: "now-7d", To: "now-1d"}, "2026-09-07T15:37:42Z", "2026-09-13T15:37:42Z"},
		{TimeRange{From: "now/d", To: "now"}, "2026-09-14T00:00:00Z", "2026-09-14T15:37:42Z"},
		{TimeRange{From: "now-1d/d", To: "now/d"}, "2026-09-13T00:00:00Z", "2026-09-14T00:00:00Z"},
		{TimeRange{From: "now/d", To: "now", TZ: "America/New_York"}, "2026-09-14T04:00:00Z", "2026-09-14T15:37:42Z"},
		{TimeRange{From: "now/w", To: "now"}, "2026-09-14T00:00:00Z", "2026-09-14T15:37:42Z"}, // Monday
		{TimeRange{From: "now-90s", To: "now/m"}, "2026-09-14T15:36:12Z", "2026-09-14T15:37:00Z"},
		{TimeRange{From: "2026-09-14T10:00:00+02:00", To: "2026-09-14T09:00:00Z"}, "2026-09-14T08:00:00Z", "2026-09-14T09:00:00Z"},
	}
	for _, tt := range tests {
		r, _, err := Resolve(tt.tr, now)
		if err != nil {
			t.Errorf("Resolve(%+v): %v", tt.tr, err)
			continue
		}
		if got := r.Start.Format(time.RFC3339); got != tt.start {
			t.Errorf("Resolve(%+v) start = %s, want %s", tt.tr, got, tt.start)
		}
		if got := r.End.Format(time.RFC3339); got != tt.end {
			t.Errorf("Resolve(%+v) end = %s, want %s", tt.tr, got, tt.end)
		}
	}
	for _, bad := range []TimeRange{
		{From: "", To: "now"}, {From: "yesterday", To: "now"}, {From: "now-1x", To: "now"},
		{From: "now", To: "now-1h"}, {From: "now-1h", To: "now", TZ: "Mars/Base"}, {From: "now*2", To: "now"},
		{From: "now/q", To: "now"},
	} {
		if _, _, err := Resolve(bad, now); err == nil {
			t.Errorf("Resolve(%+v) accepted", bad)
		}
	}
}

func TestChooseStep(t *testing.T) {
	tests := map[time.Duration]string{
		5 * time.Minute:     "5s",
		time.Hour:           "1m",
		24 * time.Hour:      "15m",
		7 * 24 * time.Hour:  "3h",
		30 * 24 * time.Hour: "12h",
	}
	for span, want := range tests {
		r := storage.TimeRange{Start: now.Add(-span), End: now}
		if got := formatStep(chooseStep(r, 120)); got != want {
			t.Errorf("chooseStep(%s) = %s, want %s", span, got, want)
		}
	}
}

func TestCursor(t *testing.T) {
	c := cursorCodec{key: []byte("k1")}
	end := time.Unix(0, 1789396262123456789)
	tok := c.encode(end, "hash1")
	got, err := c.decode(tok, "hash1")
	if err != nil || !got.Equal(end) {
		t.Fatalf("round trip: %v %v", got, err)
	}
	if _, err := c.decode(tok, "hash2"); err == nil {
		t.Error("cursor accepted for another query")
	}
	if _, err := (cursorCodec{key: []byte("k2")}).decode(tok, "hash1"); err == nil {
		t.Error("cursor accepted with another key")
	}
	body, sig, _ := strings.Cut(tok, ".")
	tampered := body[:len(body)-2] + "AA" + "." + sig
	if _, err := c.decode(tampered, "hash1"); err == nil {
		t.Error("tampered cursor accepted")
	}
	for _, junk := range []string{"", ".", "abc", strings.Repeat("a", 600)} {
		if _, err := c.decode(junk, "hash1"); err == nil {
			t.Errorf("junk cursor %q accepted", junk)
		}
	}
}

// histQuerier returns canned hits.
type histQuerier struct {
	storage.LogQuerier
	got    storage.HitsQuery
	series []storage.HitsSeries
}

func (h *histQuerier) Hits(_ context.Context, q storage.HitsQuery) ([]storage.HitsSeries, error) {
	h.got = q
	return h.series, nil
}

func principal(role string) *auth.Principal {
	return &auth.Principal{Kind: auth.KindUser, UserID: uuid.New(), Tenant: "default", Role: role, Permissions: auth.RolePermissions(role)}
}

func TestHistogramMergesSeriesAndTrims(t *testing.T) {
	start := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	ts := []time.Time{start.Add(-time.Hour), start, start.Add(time.Hour), start.Add(2 * time.Hour)}
	hq := &histQuerier{series: []storage.HitsSeries{
		{Value: "info", Timestamps: ts, Counts: []int64{99, 5, 7, 0}, Total: 111},
		{Value: "error", Timestamps: ts, Counts: []int64{0, 1, 0, 2}, Total: 3},
		{Other: true, Timestamps: ts, Counts: []int64{0, 0, 4, 0}, Total: 4},
	}}
	svc := NewService(Options{Querier: hq, Limits: DefaultLimits(30 * 24 * time.Hour), Now: func() time.Time { return start.Add(3 * time.Hour) }})
	split := "severity"
	resp, err := svc.Histogram(context.Background(), principal(auth.RoleViewer), HistogramRequest{
		Selection: Selection{TimeRange: TimeRange{From: "2026-09-14T00:00:00Z", To: "2026-09-14T03:00:00Z", TZ: "Europe/London"}},
		SplitBy:   &split, Buckets: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Step != "30m" && resp.Step != "1h" {
		t.Errorf("step = %s", resp.Step)
	}
	if len(resp.Buckets) != 3 || resp.Total != 19 {
		t.Fatalf("buckets = %+v total = %d (bucket before range must be trimmed)", resp.Buckets, resp.Total)
	}
	if resp.Buckets[1].Split["other"] != 4 || resp.Buckets[0].Split["error"] != 1 || !resp.SplitOther {
		t.Errorf("split: %+v", resp.Buckets)
	}
	if strings.Join(resp.SplitValues, ",") != "info,error,other" {
		t.Errorf("split values order = %v", resp.SplitValues)
	}
	if hq.got.Step >= time.Hour && hq.got.Offset != time.Hour {
		t.Errorf("London (UTC+1) offset = %s", hq.got.Offset)
	}
}

type statsSource []metadata.NodeStats

func (s statsSource) NodeStatsSince(context.Context, time.Time) ([]metadata.NodeStats, error) {
	return s, nil
}

func TestIngestionRate(t *testing.T) {
	base := now.Add(-5 * time.Minute).Truncate(10 * time.Second)
	var snaps statsSource
	for i := range 30 {
		ts := base.Add(time.Duration(i) * 10 * time.Second)
		snaps = append(snaps,
			metadata.NodeStats{NodeID: "a", Time: ts, Received: int64(i * 1000), Stored: int64(i * 1000)},
			metadata.NodeStats{NodeID: "b", Time: ts, Received: int64(i * 500), Stored: int64(i * 500), Dropped: int64(i * 10)},
		)
	}
	// Node b restarts: counters reset.
	snaps = append(snaps, metadata.NodeStats{NodeID: "b", Time: base.Add(300 * time.Second), Received: 3})
	svc := NewService(Options{NodeStats: snaps, Limits: DefaultLimits(time.Hour), Now: func() time.Time { return now }})
	resp, err := svc.IngestionRate(context.Background(), principal(auth.RoleViewer), DashboardRequest{TimeRange: TimeRange{From: "now-15m", To: "now"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Series) == 0 {
		t.Fatal("no series")
	}
	for _, p := range resp.Series[:len(resp.Series)-1] {
		if p.ReceivedPerSecond != 150 || p.DroppedPerSecond != 1 {
			t.Errorf("point %+v, want 150 logs/s received, 1/s dropped", p)
		}
	}
	rate, err := svc.currentRate(context.Background(), now)
	if err != nil || rate.LogsPerSecond < 99 {
		t.Errorf("current rate: %+v %v", rate, err)
	}
}

func TestGuards(t *testing.T) {
	svc := NewService(Options{Querier: &histQuerier{}, Limits: DefaultLimits(30 * 24 * time.Hour), Now: func() time.Time { return now }})
	viewer := principal(auth.RoleViewer)
	_, err := svc.resolve(viewer, Selection{TimeRange: TimeRange{From: "now-8d", To: "now"}})
	var ie *InputError
	if !errors.As(err, &ie) || ie.Code != "invalid_time_range" {
		t.Errorf("viewer 8d range: %v", err)
	}
	_, err = svc.resolve(viewer, Selection{TimeRange: TimeRange{From: "now-1h", To: "now"}, Native: &NativeQuery{Dialect: "logsql", Text: "x"}})
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("viewer native: %v", err)
	}
	lim := Limits{MaxConcurrent: 1, Timeout: time.Second}
	_, release, err := svc.acquire(context.Background(), viewer, lim)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.acquire(context.Background(), viewer, lim); !errors.Is(err, ErrTooManyQueries) {
		t.Errorf("second concurrent query: %v", err)
	}
	release()
	if _, r2, err := svc.acquire(context.Background(), viewer, lim); err != nil {
		t.Errorf("after release: %v", err)
	} else {
		r2()
	}
}

func TestIngestionRatePartialBucket(t *testing.T) {
	// Only two minutes of history in a 24h range (15m steps): the rate must
	// reflect the covered time, not be averaged over the whole bucket.
	var snaps statsSource
	start := now.Add(-2 * time.Minute)
	for i := range 13 {
		snaps = append(snaps, metadata.NodeStats{NodeID: "a", Time: start.Add(time.Duration(i) * 10 * time.Second), Received: int64(i * 1000)})
	}
	svc := NewService(Options{NodeStats: snaps, Limits: DefaultLimits(30 * 24 * time.Hour), Now: func() time.Time { return now }})
	resp, err := svc.IngestionRate(context.Background(), principal(auth.RoleViewer), DashboardRequest{TimeRange: TimeRange{From: "now-24h", To: "now"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range resp.Series {
		if p.ReceivedPerSecond != 100 {
			t.Errorf("point %s: %.1f/s, want 100/s (step %ds)", p.T, p.ReceivedPerSecond, resp.StepSeconds)
		}
	}
}

func TestIngestionRateNodeRestart(t *testing.T) {
	// Node "old" runs for the first minute, "new" (after a restart with a new
	// ID) for the second; both at 300/s. The bucket must show 300/s, not 600.
	var snaps statsSource
	start := now.Add(-2 * time.Minute)
	for i := range 7 {
		ts := time.Duration(i) * 10 * time.Second
		snaps = append(snaps,
			metadata.NodeStats{NodeID: "old", Time: start.Add(ts), Received: int64(i * 3000)},
			metadata.NodeStats{NodeID: "new", Time: start.Add(time.Minute + ts), Received: int64(i * 3000)})
	}
	svc := NewService(Options{NodeStats: snaps, Limits: DefaultLimits(30 * 24 * time.Hour), Now: func() time.Time { return now }})
	resp, err := svc.IngestionRate(context.Background(), principal(auth.RoleViewer), DashboardRequest{TimeRange: TimeRange{From: "now-24h", To: "now"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range resp.Series {
		if p.ReceivedPerSecond != 300 {
			t.Errorf("point %s: %.1f/s, want 300/s", p.T, p.ReceivedPerSecond)
		}
	}
}
