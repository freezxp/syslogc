//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/metricstore"
	"github.com/freezxp/syslogc/backend/internal/servicetrends"
	"github.com/freezxp/syslogc/backend/internal/storage"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

func vmURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_VICTORIAMETRICS_URL")
	if u == "" {
		t.Skip("TEST_VICTORIAMETRICS_URL not set")
	}
	return u
}

// TestServiceTrendsRollup runs the whole loop against real servers: DNS logs
// go into VictoriaLogs, the rollup counts distinct clients per service and
// writes them to VictoriaMetrics, and the series read back match what was
// ingested.
func TestServiceTrendsRollup(t *testing.T) {
	backend := newBackend(t, "none")
	client, err := metricstore.New(metricstore.Config{URL: vmURL(t), Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("VictoriaMetrics not reachable: %v", err)
	}
	ctx := context.Background()
	run := fmt.Sprintf("trend-%d", time.Now().UnixNano())

	// Two windows of DNS traffic, with a known number of distinct clients
	// per service. The second window has more TikTok clients than the first,
	// so the peak is unambiguous.
	//
	// Clients repeat within a window: distinct counting must not count the
	// same address twice, which is the whole point of the metric.
	now := time.Now().UTC().Truncate(5 * time.Minute)
	first, second := now.Add(-10*time.Minute), now.Add(-5*time.Minute)
	type traffic struct {
		at      time.Time
		qname   string
		clients int
		repeats int
	}
	for _, tr := range []traffic{
		{first, "www.tiktok.com", 3, 4},
		{first, "www.youtube.com", 2, 2},
		{second, "api.tiktokv.com", 7, 3},
		{second, "youtubei.googleapis.com", 2, 1},
		// A domain in no category, to prove categories do not have to cover
		// the selection.
		{second, "intranet.example.org", 5, 1},
	} {
		var batch logentry.Batch
		for c := range tr.clients {
			for range tr.repeats {
				at := tr.at.Add(time.Duration(c) * time.Second)
				e := logentry.Entry{
					Time:       at,
					ReceivedAt: at,
					// The run id is in the message so the count helper, which
					// searches free text, can find this run's rows.
					Message:  "dnsdist CLIENT_QUERY " + run,
					Source:   run,
					Hostname: "dnsdist-1",
					AppName:  "dnsdist",
				}
				e.AddField("dns.qname", tr.qname)
				e.AddField("dns.client_ip", fmt.Sprintf("10.9.%d.%d", len(tr.qname)%250, c+1))
				batch.Entries = append(batch.Entries, e)
			}
		}
		if err := backend.WriteBatch(ctx, &batch); err != nil {
			t.Fatal(err)
		}
	}
	wantStored := int64(3*4 + 2*2 + 7*3 + 2*1 + 5*1)
	deadline := time.Now().Add(30 * time.Second)
	for countRows(t, backend, run) < wantStored {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d messages were stored", countRows(t, backend, run), wantStored)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Service names carry the run id: recorded series are keyed by service
	// and window, so two runs of this test would otherwise write different
	// values to the same series at the same timestamps.
	tiktok, youtube := "tiktok-"+run, "youtube-"+run
	catalog := servicetrends.Catalog{Services: []servicetrends.Service{
		{Name: tiktok, Label: "TikTok", Enabled: true, Domains: []string{"tiktok.com", "tiktokv.com"}},
		{Name: youtube, Label: "YouTube", Enabled: true, Domains: []string{"youtube.com", "youtubei.googleapis.com"}},
	}}
	state := servicetrends.State{}
	recorder, err := servicetrends.NewRecorder(servicetrends.Options{
		Querier:     backend,
		Writer:      client,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Catalog:     func(context.Context) (servicetrends.Catalog, error) { return catalog, nil },
		LoadState:   func(context.Context) (servicetrends.State, error) { return state, nil },
		SaveState:   func(_ context.Context, s servicetrends.State) error { state = s; return nil },
		Base:        5 * time.Minute,
		Backfill:    30 * time.Minute,
		DomainField: "dns.qname",
		ClientField: "dns.client_ip",
		Sources:     []string{run},
		Now:         func() time.Time { return now.Add(time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	written, err := recorder.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if written == 0 {
		t.Fatal("the rollup wrote nothing")
	}

	// Read the recorded 5m series back out of VictoriaMetrics.
	want := map[string]map[time.Time]float64{
		tiktok:  {first: 3, second: 7},
		youtube: {first: 2, second: 2},
	}
	// A written sample takes a moment to become queryable, and the grid must
	// start on a window boundary or the points come back shifted.
	deadline = time.Now().Add(90 * time.Second)
	for {
		series, err := client.QueryRange(ctx, metricstore.RangeQuery{
			Query: fmt.Sprintf(`last_over_time(%s{window="5m"}[5m])`, servicetrends.MetricUniqueClients),
			Start: first.Add(-5 * time.Minute), End: now, Step: 5 * time.Minute,
			// These windows have already passed, so a cached answer from
			// before the rollup wrote them would never expire during the test.
			NoCache: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]map[time.Time]float64{}
		for _, s := range series {
			name := s.Labels["service"]
			if _, ok := want[name]; !ok {
				continue
			}
			if got[name] == nil {
				got[name] = map[time.Time]float64{}
			}
			for _, p := range s.Points {
				got[name][p.At.UTC()] = p.Value
			}
		}
		if matchesWanted(want, got) {
			// Reading one window at a time leaves gaps rather than carrying
			// a value forward: nothing is recorded after the second window.
			for service := range want {
				if _, ok := got[service][now]; ok {
					t.Errorf("%s has a value at %s, where nothing was recorded", service, now.Format(time.RFC3339))
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recorded series never matched.\nwant %v\ngot  %v", want, got)
		}
		time.Sleep(time.Second)
	}

	// Distinct counts are not additive: the hour covering both windows has
	// the union of their clients (3 and 7 TikTok clients share addresses
	// only when the address repeats), never their sum.
	rows, err := backend.CategoryCounts(ctx, storage.CategoryQuery{
		Selection: storage.Selection{
			Range: storage.TimeRange{Start: first, End: now},
			// Scoped to this run: the instance holds other runs' rows.
			Filter: &filter.Expr{Op: filter.Eq, Field: "source", Value: run},
		},
		Categories: []storage.Category{
			{Name: tiktok, Filter: catalog.Services[0].Filter("dns.qname")},
		},
		DistinctField: "dns.client_ip",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	// The two windows used different third octets, so the union is 10.
	if rows[0].Distinct != 10 {
		t.Errorf("distinct over both windows = %v, want 10 (the union, not a sum of buckets)", rows[0].Distinct)
	}
	if rows[0].Messages != 3*4+7*3 {
		t.Errorf("messages = %v, want %d", rows[0].Messages, 3*4+7*3)
	}
}

func matchesWanted(want, got map[string]map[time.Time]float64) bool {
	for service, points := range want {
		for at, value := range points {
			if got[service][at] != value {
				return false
			}
		}
	}
	return true
}
