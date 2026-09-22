package extract

import (
	"strings"
	"testing"

	"github.com/freezxp/syslogc/backend/internal/logentry"
)

// dnsdistQuery matches a dnsdist CLIENT_QUERY line, the shape this package
// was written for.
const dnsdistQuery = `^(?P<query_time>\S+) dnsdist (?P<event>\S+) \S+ (?P<client_ip>\S+) (?P<client_port>\d+) ` +
	`(?P<address_family>\S+) (?P<transport>\S+) (?P<query_bytes>\S+) (?P<qname>\S+) (?P<qtype>\S+) (?P<policy>\S+)$`

func fields(e *logentry.Entry) map[string]string {
	out := map[string]string{}
	for _, f := range e.Fields {
		out[f.Key] = f.Value
	}
	return out
}

func TestExtractsDnsdistQuery(t *testing.T) {
	e, err := New([]Config{{Name: "dnsdist-query", Contains: "dnsdist", Regex: dnsdistQuery, Prefix: "dns."}})
	if err != nil {
		t.Fatal(err)
	}
	entry := &logentry.Entry{Message: "2026-09-22T05:30:00.978892101Z dnsdist CLIENT_QUERY - 2001:db8:1:2::5 7248 " +
		"INET6 UDP 81b siplb-1.ane2-prd.connectrcs.com A -"}
	if rule := e.Apply(entry); rule != "dnsdist-query" {
		t.Fatalf("rule = %q, want dnsdist-query", rule)
	}
	got := fields(entry)
	want := map[string]string{
		"dns.query_time":     "2026-09-22T05:30:00.978892101Z",
		"dns.event":          "CLIENT_QUERY",
		"dns.client_ip":      "2001:db8:1:2::5",
		"dns.client_port":    "7248",
		"dns.address_family": "INET6",
		"dns.transport":      "UDP",
		"dns.query_bytes":    "81b",
		"dns.qname":          "siplb-1.ane2-prd.connectrcs.com",
		"dns.qtype":          "A",
		"dns.policy":         "-",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("extracted %d fields, want %d: %v", len(got), len(want), got)
	}
}

func TestFirstMatchingRuleWins(t *testing.T) {
	e, err := New([]Config{
		{Name: "query", Contains: "CLIENT_QUERY", Regex: `client (?P<client_ip>\S+)`},
		{Name: "response", Regex: `client (?P<responder>\S+)`},
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := &logentry.Entry{Message: "CLIENT_QUERY from client 10.0.0.1"}
	if rule := e.Apply(entry); rule != "query" {
		t.Errorf("rule = %q, want the first matching rule", rule)
	}
	if got := fields(entry); got["client_ip"] != "10.0.0.1" || len(got) != 1 {
		t.Errorf("fields = %v", got)
	}

	// Without the literal, the first rule is skipped and the second applies.
	other := &logentry.Entry{Message: "CLIENT_RESPONSE from client 10.0.0.2"}
	if rule := e.Apply(other); rule != "response" {
		t.Errorf("rule = %q, want response", rule)
	}
	if got := fields(other); got["responder"] != "10.0.0.2" {
		t.Errorf("fields = %v", got)
	}
}

func TestNoMatchLeavesTheEntryAlone(t *testing.T) {
	e, _ := New([]Config{{Regex: `^dnsdist (?P<event>\S+)`}})
	entry := &logentry.Entry{Message: "something else entirely"}
	if rule := e.Apply(entry); rule != "" {
		t.Errorf("rule = %q, want no match", rule)
	}
	if len(entry.Fields) != 0 {
		t.Errorf("fields added on a non-match: %v", entry.Fields)
	}
	// An empty message never matches, and a nil extractor is a no-op.
	if rule := e.Apply(&logentry.Entry{}); rule != "" {
		t.Errorf("empty message matched rule %q", rule)
	}
	var nilExtractor *Extractor
	nilExtractor.Apply(entry)
}

func TestEmptyCapturesProduceNoFields(t *testing.T) {
	e, _ := New([]Config{{Regex: `^(?P<first>\S*) ?(?P<second>\S*)$`}})
	entry := &logentry.Entry{Message: "only "}
	e.Apply(entry)
	got := fields(entry)
	if _, ok := got["second"]; ok {
		t.Errorf("empty capture became a field: %v", got)
	}
	if got["first"] != "only" {
		t.Errorf("first = %q", got["first"])
	}
}

func TestRejectsUnusablePatterns(t *testing.T) {
	for _, tc := range []struct{ name, regex, prefix, want string }{
		{"no named groups", `dnsdist (\S+)`, "", "no named capture groups"},
		{"invalid regex", `dnsdist (?P<a>`, "", "error parsing regexp"},
		{"reserved name", `(?P<_time>\S+)`, "", "reserved"},
		{"bad prefix", `(?P<a>\S+)`, "dns-", "unexpected character"},
		{"long pattern", "(?P<a>" + strings.Repeat("x", MaxPatternLen) + ")", "", "longer than"},
	} {
		if _, err := New([]Config{{Name: tc.name, Regex: tc.regex, Prefix: tc.prefix}}); err == nil ||
			!strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
	many := make([]Config, MaxRules+1)
	for i := range many {
		many[i] = Config{Regex: `(?P<a>x)`}
	}
	if _, err := New(many); err == nil {
		t.Error("too many rules accepted")
	}
}

func BenchmarkApply(b *testing.B) {
	e, _ := New([]Config{{Contains: "dnsdist", Regex: dnsdistQuery, Prefix: "dns."}})
	msg := "2026-09-22T05:30:00.978892101Z dnsdist CLIENT_QUERY - 2001:db8:1:2::5 7248 INET6 UDP 81b siplb-1.ane2-prd.connectrcs.com A -"
	entry := &logentry.Entry{}
	b.ReportAllocs()
	for b.Loop() {
		entry.Reset()
		entry.Message = msg
		e.Apply(entry)
	}
}
