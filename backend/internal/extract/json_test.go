package extract

import (
	"testing"

	"github.com/freezxp/syslogc/backend/internal/logentry"
)

// nxlogRecord is what NXLog sends for a successful domain sign-in, trimmed to
// the keys the Active Directory template reads.
const nxlogRecord = `{"EventTime":"2026-10-04 09:12:44","Hostname":"DC01","EventID":4624,` +
	`"Channel":"Security","TargetUserName":"a.hassan","TargetDomainName":"CORP","LogonType":3,` +
	`"IpAddress":"10.20.4.19","IpPort":"52114","WorkstationName":"LAPTOP-07",` +
	`"AuthenticationPackageName":"Kerberos","SubjectUserName":"-","Status":null,` +
	`"Nested":{"ignored":true},"List":[1,2]}`

func adExtractor(t *testing.T) *JSONExtractor {
	t.Helper()
	e, err := NewJSON(JSONConfig{Prefix: "ad.", Keys: map[string]string{
		"EventID": "event_id", "TargetUserName": "user", "TargetDomainName": "domain",
		"LogonType": "logon_type", "IpAddress": "source_ip", "WorkstationName": "workstation",
		"Hostname": "dc", "Status": "status", "Nested": "nested", "List": "list",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestPromotesTheSendersOwnFields(t *testing.T) {
	entry := &logentry.Entry{Message: nxlogRecord}
	if !adExtractor(t).Apply(entry) {
		t.Fatal("a JSON record was not recognised")
	}
	got := fields(entry)
	want := map[string]string{
		"ad.event_id":    "4624",
		"ad.user":        "a.hassan",
		"ad.domain":      "CORP",
		"ad.logon_type":  "3",
		"ad.source_ip":   "10.20.4.19",
		"ad.workstation": "LAPTOP-07",
		"ad.dc":          "DC01",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	// An event id must read as a number anyone can filter on, not as the
	// float JSON decoding would otherwise give.
	if got["ad.event_id"] != "4624" {
		t.Errorf("event id = %q, want 4624", got["ad.event_id"])
	}
	// A null field is absent rather than the word "null".
	if _, ok := got["ad.status"]; ok {
		t.Errorf("a null value became a field: %q", got["ad.status"])
	}
	// Nested structures are not fields: a field is something to filter by.
	for _, k := range []string{"ad.nested", "ad.list"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s was promoted, but it is not a scalar", k)
		}
	}
}

func TestANonJSONMessageIsLeftAlone(t *testing.T) {
	// A source carrying structured records usually carries a few plain ones
	// too; they should pass through rather than fail.
	for _, msg := range []string{
		"Oct  4 09:12:44 DC01 something plain",
		"", "   ", "[1,2,3]", `{"unterminated": `, "not json at all",
	} {
		entry := &logentry.Entry{Message: msg}
		if adExtractor(t).Apply(entry) {
			t.Errorf("%q was treated as a record", msg)
		}
		if len(entry.Fields) != 0 {
			t.Errorf("%q produced fields: %v", msg, entry.Fields)
		}
	}
}

func TestJSONRulesAreChecked(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  JSONConfig
		want string
	}{
		{"no keys", JSONConfig{Prefix: "ad."}, "at least one key"},
		{"bad prefix", JSONConfig{Prefix: "ad-", Keys: map[string]string{"A": "a"}}, "unexpected character"},
		{"bad field name", JSONConfig{Keys: map[string]string{"A": "a b"}}, "unexpected character"},
		{"reserved field", JSONConfig{Keys: map[string]string{"A": "_time"}}, "reserved"},
		{"empty key", JSONConfig{Keys: map[string]string{" ": "a"}}, "key to read is required"},
	} {
		if _, err := NewJSON(tc.cfg); err == nil {
			t.Errorf("%s: accepted", tc.name)
		} else if got := err.Error(); !contains(got, tc.want) {
			t.Errorf("%s: error = %q, want it to mention %q", tc.name, got, tc.want)
		}
	}
	many := JSONConfig{Keys: map[string]string{}}
	for i := range MaxJSONKeys + 1 {
		many.Keys[string(rune('a'+i%26))+string(rune('a'+i/26))] = "f" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	if _, err := NewJSON(many); err == nil {
		t.Error("too many keys accepted")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestANilExtractorIsANoop(t *testing.T) {
	var e *JSONExtractor
	entry := &logentry.Entry{Message: nxlogRecord}
	if e.Apply(entry) || len(entry.Fields) != 0 {
		t.Error("a nil extractor did something")
	}
}
