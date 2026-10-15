package assistant

import (
	"errors"
	"strings"
	"testing"

	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

func known(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[strings.ToLower(n)] = true
	}
	return m
}

// The field check is the guard that matters. A model asked about something a
// deployment does not collect will answer with a field that ought to exist,
// and a filter on a field nobody sends matches nothing — which reads exactly
// like "there were none" rather than like "nobody is collecting that".
func TestAFilterMayOnlyNameFieldsTheseLogsHave(t *testing.T) {
	have := known("ad.event_id", "ad.user", "source")

	ok := &filter.Expr{Op: filter.And, Args: []*filter.Expr{
		{Op: filter.Eq, Field: "ad.user", Value: "s.tan"},
		{Op: filter.Eq, Field: "ad.event_id", Value: "4625"},
	}}
	used, err := checkFields(ok, have, 0)
	if err != nil {
		t.Fatalf("a filter over known fields was refused: %v", err)
	}
	if len(used) != 2 {
		t.Errorf("used = %v, want both fields", used)
	}

	// The real failure seen from the model: it put the time range inside the
	// filter as a timestamp field that does not exist.
	invented := &filter.Expr{Op: filter.And, Args: []*filter.Expr{
		{Op: filter.Eq, Field: "ad.user", Value: "s.tan"},
		{Op: filter.Gte, Field: "@timestamp", Value: "now-1h"},
	}}
	if _, err := checkFields(invented, have, 0); err == nil {
		t.Error("a filter naming a field these logs do not have was accepted")
	} else if !strings.Contains(err.Error(), "@timestamp") {
		t.Errorf("the error does not name the offending field: %v", err)
	}

	// Nested under a not, which is where a sloppy walk stops looking.
	buried := &filter.Expr{Op: filter.Not, Arg: &filter.Expr{
		Op: filter.Or, Args: []*filter.Expr{
			{Op: filter.Eq, Field: "source", Value: "win-srv"},
			{Op: filter.Eq, Field: "firewall.rule", Value: "deny"},
		},
	}}
	if _, err := checkFields(buried, have, 0); err == nil {
		t.Error("an unknown field nested inside not/or was accepted")
	}
}

func TestFieldNamesAreMatchedWithoutRegardToCase(t *testing.T) {
	if _, err := checkFields(&filter.Expr{Op: filter.Eq, Field: "AD.User", Value: "x"},
		known("ad.user"), 0); err != nil {
		t.Errorf("a field differing only in case was refused: %v", err)
	}
}

// A window the model phrases its own way must not reach the time parser.
func TestOnlyAWindowTheAPIUnderstandsIsKept(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"now-15m", "now-15m"},
		{"now", "now"},
		{"now/d", "now/d"},
		{"2026-10-07T08:00:00Z", "2026-10-07T08:00:00Z"},
		{"", "now-24h"},
		{"last hour", "now-24h"},
		{"yesterday", "now-24h"},
		{"today", "now-24h"},
		{"-1h", "now-24h"},
	} {
		if got := cleanWhen(c.in, "now-24h"); got != c.want {
			t.Errorf("cleanWhen(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Declining is a real answer, and has to be distinguishable from a failure so
// the interface can say "these logs cannot answer that" rather than "the
// assistant broke".
func TestDecliningIsItsOwnKindOfAnswer(t *testing.T) {
	var err error = &Unanswerable{Reason: "no firewall fields exist in these logs"}
	var u *Unanswerable
	if !errors.As(err, &u) {
		t.Fatal("an unanswerable question did not come back as Unanswerable")
	}
	if !strings.Contains(err.Error(), "firewall") {
		t.Errorf("the reason was lost: %q", err.Error())
	}
}

func TestExamplesAreTrimmedBeforeTheyReachThePrompt(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := quoteAll([]string{long})
	if len(got) != 1 || len(got[0]) > 70 {
		t.Errorf("a long example was not shortened: %d characters", len(got[0]))
	}
	if !strings.HasSuffix(strings.TrimSuffix(got[0], `"`), "…") {
		t.Errorf("a shortened example does not say it was shortened: %q", got[0])
	}
}
