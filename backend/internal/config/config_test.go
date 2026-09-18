package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "syslogc.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const sampleYAML = `
node:
  id: node-a
log:
  level: debug
storage:
  victorialogs:
    insert_url: http://vl:9428
    select_url: http://vl:9428
    stream_fields: [source, hostname]
ingestion:
  queue:
    max_bytes: 64MiB
  batch:
    max_wait: 250ms
  sources:
    - name: syslog-udp
      protocol: udp
      address: ":5514"
      labels: {site: dc1}
    - name: syslog-tcp
      protocol: tcp
      address: ":5514"
      format: rfc5424
      enabled: false
retention:
  period: 14d
metadata:
  postgres:
    dsn: postgres://u:secret@db/syslogc
`

func TestLoadPrecedence(t *testing.T) {
	file := writeYAML(t, sampleYAML)
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	fs.String("config", "", "not a configuration key")
	RegisterFlags(fs)
	if err := fs.Parse([]string{"--config=x.yaml", "--log.level=warn", "--ingestion.writers=8"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{
		File: file,
		Environ: []string{
			"SYSLOGC_LOG_LEVEL=error", // overridden by flag
			"SYSLOGC_STORAGE_VICTORIALOGS_INSERT_URL=http://env-vl:9428",
			"SYSLOGC_STORAGE_VICTORIALOGS_STREAM_FIELDS=source, app_name",
			"SYSLOGC_INGESTION_QUEUE_MAX_MESSAGES=1234",
			"UNRELATED=1",
		},
		Flags: fs,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	checks := []struct {
		name      string
		got, want any
	}{
		{"flag beats env", cfg.Log.Level, "warn"},
		{"flag only", cfg.Ingestion.Writers, 8},
		{"env beats yaml", cfg.Storage.VictoriaLogs.InsertURL, "http://env-vl:9428"},
		{"yaml beats default", cfg.Storage.VictoriaLogs.SelectURL, "http://vl:9428"},
		{"env list", strings.Join(cfg.Storage.VictoriaLogs.StreamFields, ","), "source,app_name"},
		{"env int", cfg.Ingestion.Queue.MaxMessages, 1234},
		{"yaml byte size", cfg.Ingestion.Queue.MaxBytes, ByteSize(64 << 20)},
		{"yaml duration", cfg.Ingestion.Batch.MaxWait.D(), 250 * time.Millisecond},
		{"day duration", cfg.Retention.Period.D(), 14 * 24 * time.Hour},
		{"default kept", cfg.Ingestion.Batch.MaxRows, 10_000},
		{"node id", cfg.Node.ID, "node-a"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}

	if len(cfg.Ingestion.Sources) != 2 {
		t.Fatalf("sources = %d", len(cfg.Ingestion.Sources))
	}
	udp := cfg.Ingestion.Sources[0]
	if udp.Type != SourceTypeSyslog || udp.Format != FormatAuto || udp.MaxMessageBytes != DefaultUDPMaxMessageBytes ||
		udp.RawMessage != RawOnError || udp.Tenant != DefaultTenant || udp.Labels["site"] != "dc1" || !udp.IsEnabled() {
		t.Errorf("udp source defaults not applied: %+v", udp)
	}
	tcp := cfg.Ingestion.Sources[1]
	if tcp.IsEnabled() || tcp.MaxMessageBytes != DefaultStreamMaxMessageBytes || tcp.Framing != FramingAuto {
		t.Errorf("tcp source: %+v", tcp)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	file := writeYAML(t, `
server:
  http:
    address: "not-an-address"
storage:
  victorialogs:
    insert_url: "ftp://x"
    compression: brotli
ingestion:
  writers: 0
  sources:
    - name: dup
      protocol: udp
      address: ":514"
      timezone: Mars/Olympus
    - name: dup
      protocol: udp
      address: ":514"
      raw_message: sometimes
    - name: secure
      protocol: tls
      address: ":6514"
    - protocol: carrier-pigeon
      address: ":1"
typo_section: {}
`)
	_, err := Load(LoadOptions{File: file, Environ: []string{}})
	if err == nil {
		t.Fatal("invalid configuration accepted")
	}
	// Unknown keys fail decoding before validation runs.
	if !strings.Contains(err.Error(), "typo_section") {
		t.Errorf("unknown key not reported: %v", err)
	}

	cfg := Default()
	cfg.Server.HTTP.Address = "not-an-address"
	cfg.Storage.VictoriaLogs.InsertURL = "ftp://x"
	cfg.Storage.VictoriaLogs.Compression = "brotli"
	cfg.Ingestion.Writers = 0
	cfg.Ingestion.Sources = []Source{
		{Name: "dup", Protocol: "udp", Address: ":514", Timezone: "Mars/Olympus"},
		{Name: "dup", Protocol: "udp", Address: ":514", RawMessage: "sometimes"},
		{Name: "secure", Protocol: "tls", Address: ":6514"},
		{Protocol: "carrier-pigeon", Address: ":1"},
	}
	for i := range cfg.Ingestion.Sources {
		applySourceDefaults(&cfg.Ingestion.Sources[i])
	}
	err = cfg.Validate()
	if err == nil {
		t.Fatal("Validate accepted invalid config")
	}
	for _, want := range []string{
		"server.http.address",
		"insert_url",
		"compression",
		"ingestion.writers",
		"timezone",
		"duplicate source name",
		"already used by source",
		"raw_message",
		"tls.cert_file and tls.key_file are required",
		"name is required",
		"protocol must be udp, tcp or tls",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

func TestLoadUnknownEnv(t *testing.T) {
	_, err := Load(LoadOptions{Environ: []string{"SYSLOGC_STORAGE_VICTORIALOG_INSERT_URL=http://typo"}})
	if err == nil || !strings.Contains(err.Error(), "SYSLOGC_STORAGE_VICTORIALOG_INSERT_URL") {
		t.Errorf("unknown env var not reported: %v", err)
	}
}

func TestUDPAndTCPMayShareAPort(t *testing.T) {
	cfg := Default()
	cfg.Metadata.Postgres.DSN = "postgres://db/syslogc"
	cfg.Ingestion.Sources = []Source{
		{Name: "u", Protocol: "udp", Address: ":514"},
		{Name: "t", Protocol: "tcp", Address: ":514"},
	}
	for i := range cfg.Ingestion.Sources {
		applySourceDefaults(&cfg.Ingestion.Sources[i])
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("udp and tcp on the same port rejected: %v", err)
	}
}

func TestParseByteSize(t *testing.T) {
	tests := map[string]ByteSize{"0": 0, "512": 512, "64KiB": 64 << 10, "8MiB": 8 << 20, "1GB": 1 << 30, "2 mb": 2 << 20}
	for in, want := range tests {
		got, err := ParseByteSize(in)
		if err != nil || got != want {
			t.Errorf("ParseByteSize(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "MiB", "12XB", "-1"} {
		if _, err := ParseByteSize(in); err == nil {
			t.Errorf("ParseByteSize(%q) accepted", in)
		}
	}
	if s := ByteSize(8 << 20).String(); s != "8MiB" {
		t.Errorf("String = %s", s)
	}
}

func TestParseDuration(t *testing.T) {
	tests := map[string]time.Duration{"30d": 30 * 24 * time.Hour, "90s": 90 * time.Second, "1h30m": 90 * time.Minute, "": 0}
	for in, want := range tests {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"1.5d", "-2d", "soon"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) accepted", in)
		}
	}
}

func TestYAMLRoundTrip(t *testing.T) {
	file := writeYAML(t, sampleYAML)
	cfg, err := Load(LoadOptions{File: file, Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := cfg.YAML()
	if err != nil {
		t.Fatal(err)
	}
	printed := strings.Replace(string(out), "[REDACTED]", "postgres://u:x@db/syslogc", 1)
	again, err := Load(LoadOptions{File: writeYAML(t, printed), Environ: []string{}})
	if err != nil {
		t.Fatalf("printed config does not load: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "u:secret@") {
		t.Errorf("DSN not redacted in printed config:\n%s", out)
	}
	if again.Retention.Period != cfg.Retention.Period || again.Ingestion.Queue.MaxBytes != cfg.Ingestion.Queue.MaxBytes ||
		len(again.Ingestion.Sources) != 2 || again.Ingestion.Sources[0].Labels["site"] != "dc1" {
		t.Errorf("round trip changed configuration:\n%s", out)
	}
}

func TestForwardingDefaultsAndValidation(t *testing.T) {
	yaml := `
metadata:
  postgres:
    dsn: postgres://u:p@localhost/db
forwarding:
  targets:
    - name: dr
      url: http://remote:9428
      min_severity: warning
      sources: [syslog-udp]
`
	cfg, err := Load(LoadOptions{File: writeYAML(t, yaml), Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Forwarding.Targets) != 1 {
		t.Fatalf("targets = %+v", cfg.Forwarding.Targets)
	}
	tgt := cfg.Forwarding.Targets[0]
	if !tgt.IsEnabled() {
		t.Error("target should default to enabled")
	}
	if tgt.Compression != "gzip" || tgt.Queue.MaxMessages == 0 || tgt.Batch.MaxRows == 0 ||
		tgt.Retry.MaxBackoff == 0 || len(tgt.StreamFields) == 0 || tgt.WriteTimeout == 0 {
		t.Errorf("defaults not applied: %+v", tgt)
	}

	for _, tc := range []struct{ name, yaml, want string }{
		{"missing name", "forwarding:\n  targets:\n    - url: http://r:9428\n", "name is required"},
		{"bad url", "forwarding:\n  targets:\n    - {name: a, url: ftp://r}\n", "http(s) URL"},
		{"unknown severity", "forwarding:\n  targets:\n    - {name: a, url: 'http://r:9428', min_severity: loud}\n", "unknown severity"},
		{"duplicate name", "forwarding:\n  targets:\n    - {name: a, url: 'http://r:9428'}\n    - {name: a, url: 'http://s:9428'}\n", "duplicate target name"},
		{"bad compression", "forwarding:\n  targets:\n    - {name: a, url: 'http://r:9428', compression: lzma}\n", "compression"},
	} {
		_, err := Load(LoadOptions{File: writeYAML(t, "metadata:\n  postgres:\n    dsn: postgres://u:p@localhost/db\n"+tc.yaml), Environ: []string{}})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
}
