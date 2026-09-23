package servicetrends

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/freezxp/syslogc/backend/internal/metricstore"
	"github.com/freezxp/syslogc/backend/internal/storage"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

func TestDefaultCatalogIsValid(t *testing.T) {
	c := DefaultCatalog()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(c.Enabled()) != len(c.Services) {
		t.Errorf("%d of %d services are enabled", len(c.Enabled()), len(c.Services))
	}
	// Enabled() is sorted, so the stored series keep a stable order.
	prev := ""
	for _, s := range c.Enabled() {
		if s.Name <= prev {
			t.Errorf("services are not in a stable order: %q after %q", s.Name, prev)
		}
		prev = s.Name
	}
}

func TestServiceFilterMatchesDomainsAndSubdomains(t *testing.T) {
	s := Service{Name: "tiktok", Domains: []string{"tiktok.com", " TikTokcdn.com. "}}
	e := s.Filter("dns.qname")
	if err := filter.Validate(e); err != nil {
		t.Fatal(err)
	}
	if e.Op != filter.Or || len(e.Args) != 4 {
		t.Fatalf("filter = %+v", e)
	}
	// Exact match, plus a dotted suffix so "nottiktok.com" cannot match.
	want := []struct {
		op    filter.Op
		value string
	}{
		{filter.Eq, "tiktok.com"},
		{filter.Contains, ".tiktok.com"},
		{filter.Eq, "tiktokcdn.com"},
		{filter.Contains, ".tiktokcdn.com"},
	}
	for i, w := range want {
		got := e.Args[i]
		if got.Op != w.op || got.Value != w.value || got.Field != "dns.qname" {
			t.Errorf("arg %d = %+v, want %s %q", i, got, w.op, w.value)
		}
	}
	empty := Service{Name: "empty"}
	if empty.Filter("f") != nil {
		t.Error("a service without domains produced a filter")
	}
}

func TestCatalogValidationReportsEveryProblem(t *testing.T) {
	c := Catalog{Services: []Service{
		{Name: "", Domains: []string{"a.com"}},
		{Name: "Bad Name", Domains: []string{"a.com"}},
		{Name: "dup", Domains: []string{"a.com"}},
		{Name: "dup", Domains: []string{"b.com"}},
		{Name: "nodomains"},
		{Name: "badomain", Domains: []string{"no-dot", "has space.com", "*.wild.com"}},
		{Name: "multiline", Label: "a\nb", Domains: []string{"a.com"}},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("the catalog was accepted")
	}
	for _, want := range []string{
		"name: is required",
		"unexpected character",
		"already used",
		"needs at least one domain",
		"must be a domain name",
		"without spaces or wildcards",
		"single line",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%v", want, err)
		}
	}
}

func TestCatalogRejectsTooMuch(t *testing.T) {
	many := Catalog{}
	for i := 0; i <= MaxServices; i++ {
		many.Services = append(many.Services, Service{Name: "s" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Domains: []string{"a.com"}})
	}
	if err := many.Validate(); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("err = %v", err)
	}
	wide := Catalog{Services: []Service{{Name: "wide"}}}
	for i := 0; i <= MaxDomainsPerService; i++ {
		wide.Services[0].Domains = append(wide.Services[0].Domains, "d.com")
	}
	if err := wide.Validate(); err == nil || !strings.Contains(err.Error(), "domains are allowed") {
		t.Errorf("err = %v", err)
	}
}

func TestWindowName(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Minute: "1m", 5 * time.Minute: "5m", 30 * time.Minute: "30m",
		time.Hour: "1h", 6 * time.Hour: "6h", 24 * time.Hour: "1d", 168 * time.Hour: "7d",
	} {
		if got := WindowName(d); got != want {
			t.Errorf("WindowName(%s) = %q, want %q", d, got, want)
		}
	}
}

// fakeQuerier records the queries it is asked for and answers with one
// distinct client per bucket per category.
type fakeQuerier struct {
	queries []storage.CategoryQuery
	err     error
}

func (f *fakeQuerier) CategoryCounts(_ context.Context, q storage.CategoryQuery) ([]storage.CategoryRow, error) {
	f.queries = append(f.queries, q)
	if f.err != nil {
		return nil, f.err
	}
	out := []storage.CategoryRow{}
	for t := q.Range.Start; t.Before(q.Range.End); t = t.Add(q.Step) {
		for i, c := range q.Categories {
			out = append(out, storage.CategoryRow{Time: t, Category: c.Name, Distinct: float64(i + 1), Messages: float64(10 * (i + 1))})
		}
	}
	return out, nil
}

type fakeWriter struct {
	samples []metricstore.Sample
	err     error
}

func (f *fakeWriter) Write(_ context.Context, s []metricstore.Sample) error {
	if f.err != nil {
		return f.err
	}
	f.samples = append(f.samples, s...)
	return nil
}

func testRecorder(t *testing.T, q Querier, w Writer, now time.Time, backfill time.Duration) (*Recorder, *State) {
	t.Helper()
	state := State{}
	r, err := NewRecorder(Options{
		Querier: q,
		Writer:  w,
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Catalog: func(context.Context) (Catalog, error) {
			return Catalog{Services: []Service{{Name: "tiktok", Domains: []string{"tiktok.com"}, Enabled: true}}}, nil
		},
		LoadState:   func(context.Context) (State, error) { return state, nil },
		SaveState:   func(_ context.Context, s State) error { state = s; return nil },
		Base:        5 * time.Minute,
		Backfill:    backfill,
		DomainField: "dns.qname",
		ClientField: "dns.client_ip",
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return r, &state
}

func TestRunRecordsEveryResolutionOverItsOwnWindow(t *testing.T) {
	// 13:07 — the last complete 5m window ends at 13:05, the last complete
	// hour at 13:00, the last complete day at midnight. The backfill reaches
	// back far enough that all three have at least one complete window.
	now := time.Date(2026, 9, 22, 13, 7, 0, 0, time.UTC)
	q, w := &fakeQuerier{}, &fakeWriter{}
	r, state := testRecorder(t, q, w, now, 48*time.Hour)

	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	steps := map[time.Duration]storage.CategoryQuery{}
	for _, got := range q.queries {
		steps[got.Step] = got
	}
	if len(steps) != 3 {
		t.Fatalf("queried %d resolutions, want 3: %v", len(steps), steps)
	}
	// Each resolution counts over its own window — an hour is counted as one
	// hour, never summed from the five-minute counts.
	if got := steps[5*time.Minute]; !got.Range.End.Equal(now.Truncate(5 * time.Minute)) {
		t.Errorf("5m window ends at %s, want 13:05", got.Range.End)
	}
	if got := steps[time.Hour]; !got.Range.End.Equal(now.Truncate(time.Hour)) {
		t.Errorf("1h window ends at %s, want 13:00", got.Range.End)
	}

	windows := map[string]int{}
	for _, s := range w.samples {
		windows[s.Labels["window"]]++
		if s.Labels["service"] != "tiktok" {
			t.Errorf("sample labels = %v", s.Labels)
		}
		if s.Name != MetricUniqueClients && s.Name != MetricQueries {
			t.Errorf("unexpected metric %q", s.Name)
		}
	}
	for _, w := range []string{"5m", "1h", "1d"} {
		if windows[w] == 0 {
			t.Errorf("nothing was recorded for the %s window", w)
		}
	}
	if (*state)["5m"].IsZero() || (*state)["1h"].IsZero() {
		t.Errorf("state was not advanced: %v", *state)
	}
}

func TestRunRecordsOnlyCompleteWindows(t *testing.T) {
	now := time.Date(2026, 9, 22, 13, 7, 0, 0, time.UTC)
	q, w := &fakeQuerier{}, &fakeWriter{}
	r, _ := testRecorder(t, q, w, now, time.Hour)
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The window in progress (13:05–13:10) must not be recorded: it would be
	// stored as if it were a whole window and never corrected.
	for _, s := range w.samples {
		if s.Labels["window"] == "5m" && !s.At.Before(now.Truncate(5*time.Minute)) {
			t.Errorf("recorded the window in progress at %s", s.At)
		}
	}
}

func TestRunResumesFromTheRecordedState(t *testing.T) {
	now := time.Date(2026, 9, 22, 13, 7, 0, 0, time.UTC)
	q, w := &fakeQuerier{}, &fakeWriter{}
	r, state := testRecorder(t, q, w, now, time.Hour)
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := len(w.samples)
	if first == 0 {
		t.Fatal("nothing was recorded")
	}

	// A second run a minute later has no new complete window, so it records
	// nothing rather than rewriting the same windows.
	r2, _ := testRecorder(t, q, w, now.Add(time.Minute), time.Hour)
	r2.opts.LoadState = func(context.Context) (State, error) { return *state, nil }
	written, err := r2.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if written != 0 {
		t.Errorf("a run with no new window wrote %d samples", written)
	}

	// Six minutes later exactly one new 5m window exists.
	r3, _ := testRecorder(t, q, w, now.Add(6*time.Minute), time.Hour)
	r3.opts.LoadState = func(context.Context) (State, error) { return *state, nil }
	before := len(w.samples)
	if _, err := r3.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	newSamples := w.samples[before:]
	for _, s := range newSamples {
		if s.Labels["window"] != "5m" {
			t.Errorf("unexpected window %q recorded", s.Labels["window"])
		}
	}
	if len(newSamples) != 2 { // one service × unique clients + queries
		t.Errorf("recorded %d samples for one new window, want 2", len(newSamples))
	}
}

func TestBackfillWalksHistoryInSteps(t *testing.T) {
	now := time.Date(2026, 9, 22, 13, 0, 0, 0, time.UTC)
	q, w := &fakeQuerier{}, &fakeWriter{}
	// Two days of 5m windows is 576 buckets, more than one query covers.
	r, _ := testRecorder(t, q, w, now, 48*time.Hour)
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	var fine []storage.CategoryQuery
	for _, got := range q.queries {
		if got.Step == 5*time.Minute {
			fine = append(fine, got)
		}
	}
	if len(fine) < 2 {
		t.Fatalf("backfill used %d queries, want it split into steps", len(fine))
	}
	// The steps are contiguous and cover the whole backfill window.
	if want := now.Add(-48 * time.Hour); !fine[0].Range.Start.Equal(want) {
		t.Errorf("backfill starts at %s, want %s", fine[0].Range.Start, want)
	}
	for i := 1; i < len(fine); i++ {
		if !fine[i].Range.Start.Equal(fine[i-1].Range.End) {
			t.Errorf("gap between %s and %s", fine[i-1].Range.End, fine[i].Range.Start)
		}
	}
	if !fine[len(fine)-1].Range.End.Equal(now) {
		t.Errorf("backfill ends at %s, want %s", fine[len(fine)-1].Range.End, now)
	}
}

func TestRunWithoutBackfillStartsAtTheNextWindow(t *testing.T) {
	now := time.Date(2026, 9, 22, 13, 7, 0, 0, time.UTC)
	q, w := &fakeQuerier{}, &fakeWriter{}
	r, state := testRecorder(t, q, w, now, 0)
	written, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if written != 0 || len(q.queries) != 0 {
		t.Errorf("wrote %d samples from %d queries, want none on the first run", written, len(q.queries))
	}
	if (*state)["5m"].IsZero() {
		t.Log("state is not persisted until something is written, which is fine")
	}
}

func TestRunReportsFailures(t *testing.T) {
	now := time.Date(2026, 9, 22, 13, 7, 0, 0, time.UTC)
	r, _ := testRecorder(t, &fakeQuerier{err: errors.New("victorialogs is down")}, &fakeWriter{}, now, time.Hour)
	if _, err := r.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "victorialogs is down") {
		t.Errorf("err = %v", err)
	}

	r2, _ := testRecorder(t, &fakeQuerier{}, &fakeWriter{err: errors.New("metrics store is down")}, now, time.Hour)
	if _, err := r2.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "metrics store is down") {
		t.Errorf("err = %v", err)
	}
}

func TestRunDoesNothingWithoutAnEnabledService(t *testing.T) {
	now := time.Date(2026, 9, 22, 13, 7, 0, 0, time.UTC)
	q, w := &fakeQuerier{}, &fakeWriter{}
	r, _ := testRecorder(t, q, w, now, time.Hour)
	r.opts.Catalog = func(context.Context) (Catalog, error) {
		return Catalog{Services: []Service{{Name: "off", Domains: []string{"a.com"}}}}, nil
	}
	written, err := r.Run(context.Background())
	if err != nil || written != 0 || len(q.queries) != 0 {
		t.Errorf("written = %d, queries = %d, err = %v", written, len(q.queries), err)
	}
}

func TestNewRecorderValidatesOptions(t *testing.T) {
	ok := Options{
		Querier: &fakeQuerier{}, Writer: &fakeWriter{},
		Catalog:     func(context.Context) (Catalog, error) { return DefaultCatalog(), nil },
		DomainField: "dns.qname", ClientField: "dns.client_ip",
	}
	if _, err := NewRecorder(ok); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(o *Options){
		func(o *Options) { o.Querier = nil },
		func(o *Options) { o.Writer = nil },
		func(o *Options) { o.Catalog = nil },
		func(o *Options) { o.DomainField = "" },
		func(o *Options) { o.ClientField = "" },
	} {
		o := ok
		mutate(&o)
		if _, err := NewRecorder(o); err == nil {
			t.Error("invalid options were accepted")
		}
	}
}

func TestSourceFilterRestrictsTheRollup(t *testing.T) {
	now := time.Date(2026, 9, 22, 13, 7, 0, 0, time.UTC)
	q, w := &fakeQuerier{}, &fakeWriter{}
	r, _ := testRecorder(t, q, w, now, time.Hour)
	r.opts.Sources = []string{"syslog-udp"}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := q.queries[0].Filter
	if got == nil || got.Op != filter.In || got.Field != "source" || got.Values[0] != "syslog-udp" {
		t.Errorf("filter = %+v", got)
	}
}

func TestBackfillSplitsOversizedWrites(t *testing.T) {
	// A full catalog over a day of five-minute windows produces far more
	// samples than one write accepts; they must be split, not rejected.
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	q := &fakeQuerier{}
	w := &limitedWriter{limit: metricstore.MaxSamplesPerWrite}
	r, _ := testRecorder(t, q, w, now, 24*time.Hour)
	full := Catalog{}
	for i := range 30 {
		full.Services = append(full.Services, Service{
			Name: fmt.Sprintf("svc-%02d", i), Domains: []string{"a.com"}, Enabled: true})
	}
	r.opts.Catalog = func(context.Context) (Catalog, error) { return full, nil }

	written, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 288 five-minute windows × 30 services × 2 metrics is 17 280 samples,
	// which cannot be one write.
	if written < 17280 {
		t.Errorf("wrote %d samples, want at least the 5m windows of a full catalog", written)
	}
	if w.biggest > metricstore.MaxSamplesPerWrite {
		t.Errorf("one write carried %d samples, over the %d limit", w.biggest, metricstore.MaxSamplesPerWrite)
	}
	if w.calls < 2 {
		t.Errorf("writes = %d, want them split", w.calls)
	}
}

// limitedWriter rejects writes larger than a real metrics store would take.
type limitedWriter struct {
	limit   int
	calls   int
	biggest int
	total   int
}

func (w *limitedWriter) Write(_ context.Context, s []metricstore.Sample) error {
	w.calls++
	if len(s) > w.biggest {
		w.biggest = len(s)
	}
	if len(s) > w.limit {
		return fmt.Errorf("%d samples exceeds the limit of %d", len(s), w.limit)
	}
	w.total += len(s)
	return nil
}

func TestDefaultCatalogCountsEachServiceSeparately(t *testing.T) {
	c := DefaultCatalog()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}

	// No domain may belong to two services: a shared one would count the
	// same client under both, which is exactly what separating Facebook from
	// Instagram is meant to avoid.
	owner := map[string]string{}
	for _, s := range c.Services {
		for _, d := range s.Domains {
			if other, taken := owner[d]; taken {
				t.Errorf("%q is claimed by both %s and %s", d, other, s.Name)
			}
			owner[d] = s.Name
		}
	}

	// Meta's shared CDN belongs to no app in particular, so it belongs to
	// none of them: including it would fold Instagram and Threads back into
	// whichever service claimed it.
	for _, shared := range []string{"fbcdn.net", "facebook.net"} {
		if svc, ok := owner[shared]; ok {
			t.Errorf("shared CDN %q is claimed by %s, which would merge Meta's apps", shared, svc)
		}
	}

	// The services people ask about by name are all present and separate.
	want := []string{
		"facebook", "instagram", "threads", "tiktok", "youtube", "x", "linkedin", "reddit",
		"xiaohongshu", "lemon8", "tumblr", "pinterest", "snapchat", "quora", "weibo", "bigolive",
	}
	have := map[string]bool{}
	for _, s := range c.Services {
		have[s.Name] = true
	}
	for _, name := range want {
		if !have[name] {
			t.Errorf("the default catalog is missing %q", name)
		}
	}

	// One pass counts every service, so the catalog has to fit in one query.
	if len(c.Services) > MaxServices {
		t.Errorf("%d services, at most %d can be counted in one pass", len(c.Services), MaxServices)
	}
	t.Logf("%d services, %d domains", len(c.Services), len(owner))
}

func TestDefaultCatalogSeparatesMetaApps(t *testing.T) {
	c := DefaultCatalog()
	filters := map[string]func(string) bool{}
	for _, s := range c.Services {
		f := s.Filter("dns.qname")
		filters[s.Name] = func(qname string) bool { return matchesExpr(f, qname) }
	}
	for _, tc := range []struct{ qname, service string }{
		{"www.facebook.com", "facebook"},
		{"graph.facebook.com", "facebook"},
		{"www.instagram.com", "instagram"},
		{"scontent.cdninstagram.com", "instagram"},
		{"www.threads.net", "threads"},
		{"web.whatsapp.com", "whatsapp"},
		{"api.tiktokv.com", "tiktok"},
		{"www.lemon8-app.com", "lemon8"},
		{"www.xiaohongshu.com", "xiaohongshu"},
		{"api.bigo.tv", "bigolive"},
	} {
		matched := []string{}
		for name, f := range filters {
			if f(tc.qname) {
				matched = append(matched, name)
			}
		}
		if len(matched) != 1 || matched[0] != tc.service {
			t.Errorf("%s matched %v, want only %s", tc.qname, matched, tc.service)
		}
	}
}

// matchesExpr applies a catalog filter the way the storage backend would.
func matchesExpr(e *filter.Expr, qname string) bool {
	if e == nil {
		return false
	}
	switch e.Op {
	case filter.Or:
		for _, a := range e.Args {
			if matchesExpr(a, qname) {
				return true
			}
		}
		return false
	case filter.Eq:
		return qname == e.Value
	case filter.Contains:
		return strings.Contains(qname, e.Value)
	default:
		return false
	}
}

func TestRunRecordsBothScopes(t *testing.T) {
	now := time.Date(2026, 9, 22, 13, 7, 0, 0, time.UTC)
	q, w := &fakeQuerier{}, &fakeWriter{}
	r, _ := testRecorder(t, q, w, now, time.Hour)
	r.opts.Catalog = func(context.Context) (Catalog, error) {
		return Catalog{Services: []Service{
			{Name: "tiktok", Enabled: true, Domains: []string{"tiktok.com", "tiktokcdn.com"}, MainDomains: []string{"tiktok.com"}},
			// A service with no main domains is counted once, not twice.
			{Name: "corporate", Enabled: true, Domains: []string{"example.com"}},
		}}, nil
	}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	scopes := map[string]map[string]bool{}
	for _, s := range w.samples {
		scope, service := s.Labels["scope"], s.Labels["service"]
		if scopes[scope] == nil {
			scopes[scope] = map[string]bool{}
		}
		scopes[scope][service] = true
	}
	if !scopes[ScopeAll]["tiktok"] || !scopes[ScopeAll]["corporate"] {
		t.Errorf("the full scope is missing services: %v", scopes[ScopeAll])
	}
	if !scopes[ScopeMain]["tiktok"] {
		t.Errorf("the main scope is missing tiktok: %v", scopes[ScopeMain])
	}
	if scopes[ScopeMain]["corporate"] {
		t.Error("a service without main domains was counted in the main scope")
	}

	// The main pass asks only about the main domains.
	var mainQuery *storage.CategoryQuery
	for i, got := range q.queries {
		if len(got.Categories) == 1 && got.Categories[0].Name == "tiktok" {
			mainQuery = &q.queries[i]
		}
	}
	if mainQuery == nil {
		t.Fatal("no main-domain query was made")
	}
	if n := len(mainQuery.Categories[0].Filter.Args); n != 2 {
		t.Errorf("main filter has %d arms, want the one domain's two", n)
	}
}

func TestMainDomainsMustBeDomainsOfTheService(t *testing.T) {
	c := Catalog{Services: []Service{
		{Name: "tiktok", Enabled: true, Domains: []string{"tiktok.com"}, MainDomains: []string{"tiktok.com"}},
		{Name: "typo", Enabled: true, Domains: []string{"example.com"}, MainDomains: []string{"exmaple.com"}},
	}}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "is not one of its domains") {
		t.Errorf("err = %v, want the typo to be caught", err)
	}
}

func TestDefaultCatalogMainDomainsAreASubsetAndMeaningful(t *testing.T) {
	c := DefaultCatalog()
	for _, s := range c.Services {
		if len(s.MainDomains) == 0 {
			t.Errorf("%s has no main domains, so it has no separate main count", s.Name)
		}
		if len(s.MainDomains) > len(s.Domains) {
			t.Errorf("%s: more main domains than domains", s.Name)
		}
	}
	// The CDNs are what the main scope exists to leave out.
	byName := map[string]Service{}
	for _, s := range c.Services {
		byName[s.Name] = s
	}
	for _, tc := range []struct{ service, cdn string }{
		{"instagram", "cdninstagram.com"},
		{"youtube", "googlevideo.com"},
		{"tiktok", "tiktokcdn.com"},
		{"x", "twimg.com"},
		{"snapchat", "sc-cdn.net"},
	} {
		for _, d := range byName[tc.service].MainDomains {
			if d == tc.cdn {
				t.Errorf("%s counts %s as a main domain, which is a CDN", tc.service, tc.cdn)
			}
		}
	}
}

// failingQuerier answers normally except for the resolutions named, which is
// how an expensive daily window behaves on a busy deployment.
type failingQuerier struct {
	fakeQuerier
	failStep time.Duration
	attempts int
}

func (f *failingQuerier) CategoryCounts(ctx context.Context, q storage.CategoryQuery) ([]storage.CategoryRow, error) {
	if q.Step == f.failStep {
		f.attempts++
		return nil, context.DeadlineExceeded
	}
	return f.fakeQuerier.CategoryCounts(ctx, q)
}

func TestAWindowThatCannotBeCountedDoesNotStopTheOthers(t *testing.T) {
	// The daily window is the expensive one: counting distinct clients over
	// a whole day is far more work than over five minutes.
	now := time.Date(2026, 9, 23, 0, 5, 0, 0, time.UTC)
	q := &failingQuerier{failStep: ResolutionDay}
	w := &fakeWriter{}
	r, state := testRecorder(t, q, w, now, 48*time.Hour)

	written, err := r.Run(context.Background())
	if err == nil {
		t.Fatal("the failing window was not reported")
	}
	if !strings.Contains(err.Error(), "1d windows") {
		t.Errorf("err = %v, want it to name the window that failed", err)
	}
	if written == 0 {
		t.Fatal("nothing was recorded, although only the daily window failed")
	}
	windows := map[string]bool{}
	for _, s := range w.samples {
		windows[s.Labels["window"]] = true
	}
	if !windows["5m"] || !windows["1h"] {
		t.Errorf("recorded windows %v, want the healthy ones to carry on", windows)
	}

	// Progress is saved despite the failure. Without this the next run
	// starts from the same place, so the range grows every five minutes
	// until nothing finishes — which is how the rollup stops for good.
	if (*state)["5m"].IsZero() {
		t.Error("no progress was saved, so the next run would redo this one")
	}

	// A second run a few minutes later records only what is new, and does
	// not spend itself retrying the window that just failed.
	before := len(w.samples)
	attempts := q.attempts
	r2, _ := testRecorder(t, q, w, now.Add(6*time.Minute), 48*time.Hour)
	r2.opts.LoadState = func(context.Context) (State, error) { return *state, nil }
	r2.retryAfter = r.retryAfter
	if _, err := r2.Run(context.Background()); err != nil {
		t.Errorf("the second run failed: %v", err)
	}
	if q.attempts != attempts {
		t.Errorf("the failing window was retried after %v, want it held back", 6*time.Minute)
	}
	newSamples := len(w.samples) - before
	if newSamples == 0 || newSamples > 8 {
		t.Errorf("the second run wrote %d samples, want only the new five-minute window", newSamples)
	}
}

func TestAFailingWindowIsRetriedOnItsOwnSchedule(t *testing.T) {
	now := time.Date(2026, 9, 23, 0, 5, 0, 0, time.UTC)
	q := &failingQuerier{failStep: ResolutionDay}
	r, state := testRecorder(t, q, &fakeWriter{}, now, 48*time.Hour)
	if _, err := r.Run(context.Background()); err == nil {
		t.Fatal("expected the daily window to fail")
	}
	first := q.attempts

	// Six hours later — a quarter of a day — it is tried again.
	r2, _ := testRecorder(t, q, &fakeWriter{}, now.Add(6*time.Hour+time.Minute), 48*time.Hour)
	r2.opts.LoadState = func(context.Context) (State, error) { return *state, nil }
	r2.retryAfter = r.retryAfter
	_, _ = r2.Run(context.Background())
	if q.attempts <= first {
		t.Error("the failing window was never retried")
	}
}

func TestQueryTimeoutBoundsOneWindow(t *testing.T) {
	now := time.Date(2026, 9, 23, 0, 5, 0, 0, time.UTC)
	slow := &slowQuerier{delay: 300 * time.Millisecond}
	r, _ := testRecorder(t, slow, &fakeWriter{}, now, time.Hour)
	r.opts.QueryTimeout = 50 * time.Millisecond
	if _, err := r.Run(context.Background()); err == nil {
		t.Fatal("a query that outran its budget was not reported")
	}
	if !slow.deadlineSeen {
		t.Error("the query was not given a deadline of its own")
	}
}

type slowQuerier struct {
	delay        time.Duration
	deadlineSeen bool
}

func (s *slowQuerier) CategoryCounts(ctx context.Context, _ storage.CategoryQuery) ([]storage.CategoryRow, error) {
	if _, ok := ctx.Deadline(); ok {
		s.deadlineSeen = true
	}
	select {
	case <-time.After(s.delay):
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
