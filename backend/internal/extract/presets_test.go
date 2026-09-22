package extract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/freezxp/syslogc/backend/internal/logentry"
)

// productionLine is a real DNScollector message, copied from a running
// deployment. The preset must keep matching it exactly.
const productionLine = "2026-09-22T12:51:29.98450571Z dnsdist CLIENT_QUERY - 2001:f40:973::595 3039 INET6 UDP 78b " +
	"report.appmetrica.yandex.net A -"

func TestPresetsCompileAndMatchTheirSamples(t *testing.T) {
	for _, p := range Presets() {
		e, err := New([]Config{p.Rule})
		if err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
		entry := &logentry.Entry{Message: p.Sample}
		if rule := e.Apply(entry); rule == "" {
			t.Errorf("%s: its own sample does not match", p.ID)
		}
	}
}

func TestDnsdistPresetOnAProductionLine(t *testing.T) {
	var preset Preset
	for _, p := range Presets() {
		if p.ID == "dnsdist" {
			preset = p
		}
	}
	e, err := New([]Config{preset.Rule})
	if err != nil {
		t.Fatal(err)
	}
	entry := &logentry.Entry{Message: productionLine}
	if rule := e.Apply(entry); rule != "dnsdist-query" {
		t.Fatalf("rule = %q, want the line to match", rule)
	}
	got := fields(entry)
	want := map[string]string{
		"dns.query_time":     "2026-09-22T12:51:29.98450571Z",
		"dns.event":          "CLIENT_QUERY",
		"dns.client_ip":      "2001:f40:973::595", // IPv6, unbracketed
		"dns.client_port":    "3039",
		"dns.address_family": "INET6",
		"dns.transport":      "UDP",
		"dns.query_bytes":    "78b",
		"dns.qname":          "report.appmetrica.yandex.net",
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

	// The fields the trend rollup reads by default must be the ones this
	// produces, or a deployment would record nothing.
	for _, needed := range []string{"dns.qname", "dns.client_ip"} {
		if got[needed] == "" {
			t.Errorf("%s is required by the service trend rollup but was not extracted", needed)
		}
	}
}

func TestDnsdistPresetHandlesAnIPv4Client(t *testing.T) {
	e, _ := New([]Config{Presets()[0].Rule})
	entry := &logentry.Entry{Message: "2026-09-22T12:51:29Z dnsdist CLIENT_QUERY - 10.21.111.203 51515 INET UDP 44b www.tiktok.com HTTPS -"}
	if rule := e.Apply(entry); rule == "" {
		t.Fatal("an IPv4 client query did not match")
	}
	got := fields(entry)
	if got["dns.client_ip"] != "10.21.111.203" || got["dns.qname"] != "www.tiktok.com" || got["dns.qtype"] != "HTTPS" {
		t.Errorf("fields = %v", got)
	}
}

func TestPresetsSerialiseAsRulesAreWritten(t *testing.T) {
	// The editor pastes a preset straight into a source, so the rule has to
	// arrive under the names a source's extract rule uses.
	//
	// Asserted against the bytes, not by decoding them: Go matches field
	// names case-insensitively, so a round trip through this package would
	// accept "Regex" — while the browser, which is what reads this, would
	// see undefined and fall over.
	out, err := json.Marshal(Presets()[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"id":`, `"title":`, `"sample":`, `"rule":`, `"name":`, `"contains":`, `"regex":`, `"prefix":`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("%s is missing from the preset JSON:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{`"Regex"`, `"Name"`, `"Contains"`, `"Prefix"`} {
		if strings.Contains(string(out), unwanted) {
			t.Errorf("%s is serialised with a Go field name, which a browser cannot read:\n%s", unwanted, out)
		}
	}
}
