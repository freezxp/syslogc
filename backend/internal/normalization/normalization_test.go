package normalization

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/freezxp/syslogc/backend/internal/logentry"
)

var recv = time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)

func newEntry() *logentry.Entry {
	var e logentry.Entry
	e.Reset()
	return &e
}

func defaultNormalizer() *Normalizer {
	return New(Options{
		MaxFutureSkew:      10 * time.Minute,
		MaxPastAge:         29 * 24 * time.Hour,
		MaxFields:          4,
		MaxFieldValueBytes: 16,
		MaxFieldNameBytes:  12,
	})
}

func TestApplyTimePolicy(t *testing.T) {
	tests := []struct {
		name       string
		event      time.Time
		tsRaw      string
		wantTime   time.Time
		wantSource logentry.TimeSource
		wantRaw    string
	}{
		{"valid event time", recv.Add(-time.Minute), "", recv.Add(-time.Minute), logentry.TimeEvent, ""},
		{"missing timestamp", time.Time{}, "", recv, logentry.TimeReceived, ""},
		{"unparseable timestamp", time.Time{}, "yesterday", recv, logentry.TimeReceived, "yesterday"},
		{"too far in future", recv.Add(time.Hour), "", recv, logentry.TimeAdjusted, "2026-09-14T11:00:00Z"},
		{"within future skew", recv.Add(5 * time.Minute), "", recv.Add(5 * time.Minute), logentry.TimeEvent, ""},
		{"too old", recv.Add(-60 * 24 * time.Hour), "", recv, logentry.TimeAdjusted, "2026-07-16T10:00:00Z"},
	}
	n := defaultNormalizer()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEntry()
			e.Time, e.TimestampRaw = tt.event, tt.tsRaw
			n.Apply(e, &Source{Name: "s"}, &Meta{ReceivedAt: recv})
			if !e.Time.Equal(tt.wantTime) || e.TimeSource != tt.wantSource || e.TimestampRaw != tt.wantRaw {
				t.Errorf("got time=%s source=%s raw=%q; want time=%s source=%s raw=%q",
					e.Time, e.TimeSource, e.TimestampRaw, tt.wantTime, tt.wantSource, tt.wantRaw)
			}
			if e.Time.Location() != time.UTC {
				t.Errorf("time not in UTC: %s", e.Time.Location())
			}
		})
	}
}

func TestApplyNetworkIdentity(t *testing.T) {
	n := defaultNormalizer()
	peer := netip.MustParseAddrPort("[::ffff:10.1.2.3]:51514")

	e := newEntry()
	n.Apply(e, &Source{Name: "s", HostnameFallbackIP: true}, &Meta{ReceivedAt: recv, Peer: peer})
	if got := e.SourceIP.String(); got != "10.1.2.3" {
		t.Errorf("SourceIP = %s, want unmapped 10.1.2.3", got)
	}
	if e.SourcePort != 51514 {
		t.Errorf("SourcePort = %d", e.SourcePort)
	}
	if e.Hostname != "10.1.2.3" {
		t.Errorf("Hostname fallback = %q", e.Hostname)
	}

	e = newEntry()
	e.Hostname = "fw01"
	n.Apply(e, &Source{Name: "s", HostnameFallbackIP: true}, &Meta{ReceivedAt: recv, Peer: peer})
	if e.Hostname != "fw01" {
		t.Errorf("Hostname overwritten by fallback: %q", e.Hostname)
	}

	e = newEntry()
	n.Apply(e, &Source{Name: "s"}, &Meta{ReceivedAt: recv, Peer: peer})
	if e.Hostname != "" {
		t.Errorf("Hostname fallback applied without being enabled: %q", e.Hostname)
	}
}

func TestApplyFieldRules(t *testing.T) {
	n := defaultNormalizer()
	e := newEntry()
	e.Fields = []logentry.Field{
		{Key: "hostname", Value: "spoofed"},
		{Key: "_time", Value: "x"},
		{Key: "", Value: "anon"},
		{Key: "a_very_long_field_name", Value: "héllo wörld and more text"},
		{Key: "dropped", Value: "1"},
	}
	n.Apply(e, &Source{Name: "s"}, &Meta{ReceivedAt: recv})

	want := []logentry.Field{
		{Key: "fields.hostname", Value: "spoofed"},
		{Key: "fields._time", Value: "x"},
		{Key: "fields.empty", Value: "anon"},
		{Key: "a_very_long~", Value: "héllo wörld an"},
	}
	if len(e.Fields) != len(want) {
		t.Fatalf("fields = %v, want %v", e.Fields, want)
	}
	for i := range want {
		if e.Fields[i] != want[i] {
			t.Errorf("field[%d] = %+v, want %+v", i, e.Fields[i], want[i])
		}
	}
	if e.FieldsDropped != 1 {
		t.Errorf("FieldsDropped = %d, want 1", e.FieldsDropped)
	}
	if !e.Truncated {
		t.Error("Truncated not set after value truncation")
	}
}

func TestApplyInvalidUTF8(t *testing.T) {
	n := defaultNormalizer()
	e := newEntry()
	e.Message = "bad \xff byte"
	e.Fields = []logentry.Field{{Key: "k\xfe", Value: "v\xc3"}}
	n.Apply(e, &Source{Name: "s", RawPolicy: RawAlways}, &Meta{ReceivedAt: recv, Data: "raw \xff"})
	for _, s := range []string{e.Message, e.Fields[0].Key, e.Fields[0].Value, e.Raw} {
		if !strings.Contains(s, "�") {
			t.Errorf("invalid UTF-8 not replaced in %q", s)
		}
	}
}

func TestRawPolicy(t *testing.T) {
	n := defaultNormalizer()
	tests := []struct {
		policy     RawPolicy
		parseError string
		format     logentry.Format
		wantRaw    bool
	}{
		{RawAlways, "", logentry.FormatRFC5424, true},
		{RawNever, "", logentry.FormatRFC5424, false},
		{RawOnError, "", logentry.FormatRFC5424, false},
		{RawOnError, "bad SD", logentry.FormatRFC5424, true},
		{RawOnError, "", logentry.FormatUnknown, true},
	}
	for _, tt := range tests {
		e := newEntry()
		e.ParseError, e.Format = tt.parseError, tt.format
		n.Apply(e, &Source{Name: "s", RawPolicy: tt.policy}, &Meta{ReceivedAt: recv, Data: "<14>raw"})
		if got := e.Raw != ""; got != tt.wantRaw {
			t.Errorf("policy=%d parseError=%q format=%s: raw stored = %v, want %v", tt.policy, tt.parseError, tt.format, got, tt.wantRaw)
		}
	}
}

func TestApplySourceMetadata(t *testing.T) {
	n := defaultNormalizer()
	labels := LabelFields(map[string]string{"site": "dc1", "env": "prod"})
	e := newEntry()
	n.Apply(e, &Source{
		Name: "syslog-udp", Type: logentry.SourceTypeSyslog, Protocol: logentry.ProtocolUDP,
		Tenant: "default", Labels: labels,
	}, &Meta{ReceivedAt: recv, Truncated: true})
	if e.Source != "syslog-udp" || e.Protocol != logentry.ProtocolUDP || e.Tenant != "default" || !e.Truncated {
		t.Errorf("metadata not applied: %+v", e)
	}
	if len(e.Labels) != 2 || e.Labels[0].Key != "labels.env" || e.Labels[1].Key != "labels.site" {
		t.Errorf("labels = %v", e.Labels)
	}
}

func TestParseSeverity(t *testing.T) {
	tests := map[string]logentry.Severity{
		"emerg": 0, "PANIC": 0, "alert": 1, "crit": 2, "Fatal": 2, "ERR": 3, "error": 3,
		"warn": 4, "WARNING": 4, "notice": 5, "info": 6, "Informational": 6, "debug": 7,
		"trace": 7, "0": 0, "7": 7, " error ": 3,
	}
	for in, want := range tests {
		got, ok := ParseSeverity(in)
		if !ok || got != want {
			t.Errorf("ParseSeverity(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "8", "loud", "10"} {
		if _, ok := ParseSeverity(in); ok {
			t.Errorf("ParseSeverity(%q) accepted", in)
		}
	}
}

func TestTruncateUTF8(t *testing.T) {
	if got := truncateUTF8("aé", 2); got != "a" {
		t.Errorf("truncateUTF8 split a rune: %q", got)
	}
	if got := truncateUTF8("abc", 5); got != "abc" {
		t.Errorf("truncateUTF8 changed short string: %q", got)
	}
}
