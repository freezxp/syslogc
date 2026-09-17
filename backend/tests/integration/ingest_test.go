//go:build integration

// Package integration runs Syslogc against a real VictoriaLogs instance.
//
//	TEST_VICTORIALOGS_URL=http://127.0.0.1:9428 go test -tags integration ./tests/integration/
package integration

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/freezxp/syslogc/backend/internal/app"
	"github.com/freezxp/syslogc/backend/internal/config"
	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/metadata/postgres/pgtest"
	"github.com/freezxp/syslogc/backend/internal/storage"
	"github.com/freezxp/syslogc/backend/internal/storage/victorialogs"
)

func vlURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_VICTORIALOGS_URL")
	if u == "" {
		t.Skip("TEST_VICTORIALOGS_URL not set")
	}
	return u
}

func newBackend(t *testing.T, compression string) *victorialogs.Backend {
	t.Helper()
	b, err := victorialogs.New(victorialogs.Config{
		InsertURL: vlURL(t), SelectURL: vlURL(t),
		StreamFields: []string{"source", "hostname", "app_name"},
		WriteTimeout: 10 * time.Second, QueryTimeout: 30 * time.Second, Compression: compression,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Ping(context.Background()); err != nil {
		t.Fatalf("VictoriaLogs not reachable: %v", err)
	}
	return b
}

func runID() string { return fmt.Sprintf("it%x", rand.Uint64()) }

// search polls storage until want rows matching query exist (VictoriaLogs
// makes data searchable shortly after ingestion).
func search(t *testing.T, q storage.LogQuerier, query string, want int) []storage.Row {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		now := time.Now()
		rows, err := q.Search(context.Background(), storage.SearchQuery{
			Selection: storage.Selection{
				Range:  storage.TimeRange{Start: now.Add(-48 * time.Hour), End: now.Add(time.Hour)},
				Native: &storage.NativeQuery{Dialect: "logsql", Text: query},
			},
			Limit: 10_000,
		})
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		var got []storage.Row
		for rows.Next() {
			got = append(got, append(storage.Row(nil), rows.Row()...))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if len(got) >= want || time.Now().After(deadline) {
			if len(got) != want {
				t.Fatalf("search %q: got %d rows, want %d", query, len(got), want)
			}
			return got
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func field(t *testing.T, r storage.Row, key string) string {
	t.Helper()
	v, ok := r.Get(key)
	if !ok {
		t.Fatalf("row has no field %q: %v", key, r)
	}
	return v
}

// TestWriteSearchRoundTrip covers the adapter directly, including hostile
// field values and both compression modes.
func TestWriteSearchRoundTrip(t *testing.T) {
	for _, compression := range []string{"none", "gzip"} {
		t.Run(compression, func(t *testing.T) {
			b := newBackend(t, compression)
			id := runID()
			hostile := []string{
				`quote " and backslash \ and pipe | and colon : and star *`,
				`") OR hostname:* OR ("`,
				"newline\nand\ttab and \x01 control",
				"unicode ünïcødé 🚀 日本語",
			}
			batch := &logentry.Batch{Tenant: "default"}
			now := time.Now().UTC()
			for i, v := range hostile {
				e := batch.Next()
				e.Time = now.Add(time.Duration(i) * time.Millisecond)
				e.ReceivedAt = now
				e.Message = "roundtrip " + id
				e.Hostname = "it-host"
				e.AppName = "it"
				e.Source = "integration"
				e.Facility, e.Severity = 20, logentry.SeverityError
				e.Format, e.Protocol = logentry.FormatRFC5424, logentry.ProtocolTCP
				e.AddField("hostile", v)
				e.AddField("idx", fmt.Sprint(i))
			}
			if err := b.WriteBatch(context.Background(), batch); err != nil {
				t.Fatal(err)
			}
			rows := search(t, b, id, len(hostile))
			byIdx := map[string]storage.Row{}
			for _, r := range rows {
				byIdx[field(t, r, "idx")] = r
			}
			for i, v := range hostile {
				r := byIdx[fmt.Sprint(i)]
				if got := field(t, r, "hostile"); got != v {
					t.Errorf("hostile[%d] round trip: got %q, want %q", i, got, v)
				}
				if field(t, r, "severity") != "error" || field(t, r, "facility") != "local4" || field(t, r, "priority") != "163" {
					t.Errorf("core fields wrong: %v", r)
				}
			}
			// Exact-match filter on a hostile value built with strconv-style
			// quoting must match exactly one row (no filter injection).
			q := fmt.Sprintf(`%s hostile:=%s`, id, victorialogs.QuoteFieldName(hostile[1]))
			search(t, b, q, 1)
		})
	}
}

func startApp(t *testing.T, mutate func(*config.Config)) *app.App {
	t.Helper()
	_, dsn := pgtest.OpenDSN(t)
	secretDir := t.TempDir()
	adminPw := filepath.Join(secretDir, "admin")
	_ = os.WriteFile(adminPw, []byte(adminPassword), 0o600)

	cfg := config.Default()
	cfg.Node.ID = "integration"
	cfg.Server.HTTP.Address = "127.0.0.1:0"
	cfg.Storage.VictoriaLogs.InsertURL = vlURL(t)
	cfg.Storage.VictoriaLogs.SelectURL = vlURL(t)
	cfg.Ingestion.Batch.MaxWait = config.Duration(100 * time.Millisecond)
	cfg.Shutdown.DrainDelay = 0
	cfg.Shutdown.Timeout = config.Duration(10 * time.Second)
	cfg.Metadata.Postgres.DSN = dsn
	cfg.Auth.BootstrapAdmin.PasswordFile = adminPw
	cfg.Auth.CookieSecure = false
	cfg.Ingestion.Sources = []config.Source{
		{Name: "it-udp", Protocol: "udp", Address: "127.0.0.1:0", UDP: config.UDPConfig{Sockets: 2}},
		{Name: "it-tcp", Protocol: "tcp", Address: "127.0.0.1:0", Labels: map[string]string{"site": "lab"}},
		{Name: "it-http", Type: "http_json"},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	if err := applyDefaultsAndValidate(&cfg); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	ctx, cancel := context.WithCancel(context.Background())
	a, err := app.New(ctx, &cfg, app.BuildInfo{Version: "test"}, log)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, func() { close(ready) }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("app exited during startup: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("app did not start")
	}
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("app.Run: %v", err)
		}
	})
	return a
}

const adminPassword = "it-bootstrap-secret-9471"

// applyDefaultsAndValidate mimics config.Load for programmatic configs.
func applyDefaultsAndValidate(cfg *config.Config) error {
	dsn := cfg.Metadata.Postgres.DSN
	out, err := cfg.YAML()
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("", "syslogc-it-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, _ = f.Write([]byte(strings.Replace(string(out), "[REDACTED]", dsn, 1)))
	_ = f.Close()
	loaded, err := config.Load(config.LoadOptions{File: f.Name(), Environ: []string{}})
	if err != nil {
		return err
	}
	*cfg = *loaded
	return nil
}

// sendUDP writes a datagram and resends it until the source counts it.
// UDP has no delivery guarantee: loopback datagrams are dropped when the
// receive buffer is full, which happens on small CI machines.
func sendUDP(t *testing.T, a *app.App, conn net.Conn, msg string) {
	t.Helper()
	want := udpReceived(t, a) + 1
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := io.WriteString(conn, msg); err != nil {
			t.Fatal(err)
		}
		for until := time.Now().Add(2 * time.Second); time.Now().Before(until); {
			if udpReceived(t, a) >= want {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		if time.Now().After(deadline) {
			t.Fatalf("UDP datagram not received after retries: %s", msg)
		}
	}
}

// udpReceived returns messages received by the it-udp source.
func udpReceived(t *testing.T, a *app.App) int {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://%s/metrics", a.Server().Addr()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for line := range strings.Lines(string(body)) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), `syslogc_ingest_messages_received_total{protocol="udp",source="it-udp"} `)
		if !ok {
			continue
		}
		n, err := strconv.Atoi(rest)
		if err != nil {
			t.Fatalf("parsing %q: %v", line, err)
		}
		return n
	}
	return 0
}

// TestSyslogToStorage covers Syslog → Parser → Normalizer → Storage over
// UDP and TCP (both framings), then verifies through the dev search API.
func TestSyslogToStorage(t *testing.T) {
	a := startApp(t, nil)
	id := runID()
	udpAddr := a.Supervisor().Addr("it-udp").String()
	tcpAddr := a.Supervisor().Addr("it-tcp").String()

	udp, err := net.Dial("udp", udpAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	// A recent timestamp: the search window below is relative to now.
	ts5424 := time.Now().UTC().Add(-time.Minute).Format("2006-01-02T15:04:05.9Z")
	sendUDP(t, a, udp, fmt.Sprintf("<165>1 %s fw01 vpnd 812 TUNNEL [meta@1 vpn=\"HQ-VPN\"] udp5424 %s", ts5424, id))
	sendUDP(t, a, udp, fmt.Sprintf("<38>%s web-1 sshd[4021]: udp3164 %s", time.Now().UTC().Format("Jan _2 15:04:05"), id))

	tcp, err := net.Dial("tcp", tcpAddr)
	if err != nil {
		t.Fatal(err)
	}
	msg := fmt.Sprintf("<14>1 - - app - - - tcpoctet %s", id)
	fmt.Fprintf(tcp, "%d %s", len(msg), msg)
	fmt.Fprintf(tcp, "<11>%s db01 postgres[77]: tcplf %s\n", time.Now().UTC().Format("Jan _2 15:04:05"), id)
	fmt.Fprintf(tcp, "garbage without any structure %s\n", id)
	fmt.Fprintf(tcp, "<14>1 - h app - - [x@1 k=\"unterminated] badsd %s\n", id)
	tcp.Close()

	be := newBackend(t, "none")
	rows := search(t, be, id, 6)
	byKind := map[string]storage.Row{}
	for _, r := range rows {
		msg := field(t, r, "_msg")
		for _, kind := range []string{"udp5424", "udp3164", "tcpoctet", "tcplf", "garbage", "badsd"} {
			if strings.Contains(msg, kind) {
				byKind[kind] = r
			}
		}
	}
	if len(byKind) != 6 {
		t.Fatalf("missing messages: have %v", byKind)
	}

	expect := map[string]map[string]string{
		"udp5424": {"format": "rfc5424", "protocol": "udp", "source": "it-udp", "hostname": "fw01", "app_name": "vpnd",
			"process_id": "812", "message_id": "TUNNEL", "facility": "local4", "severity": "notice", "sd.meta@1.vpn": "HQ-VPN",
			"_time": ts5424, "source_ip": "127.0.0.1"},
		"udp3164":  {"format": "rfc3164", "hostname": "web-1", "app_name": "sshd", "process_id": "4021", "facility": "auth", "severity": "info"},
		"tcpoctet": {"format": "rfc5424", "protocol": "tcp", "source": "it-tcp", "app_name": "app", "time_source": "received", "labels.site": "lab"},
		"tcplf":    {"format": "rfc3164", "hostname": "db01", "app_name": "postgres", "severity": "error"},
		"garbage":  {"format": "rfc3164", "severity_source": "default", "severity": "notice"},
		"badsd":    {"format": "rfc5424", "hostname": "h", "app_name": "app"},
	}
	for kind, fields := range expect {
		for k, v := range fields {
			if got := field(t, byKind[kind], k); got != v {
				t.Errorf("%s: %s = %q, want %q", kind, k, got, v)
			}
		}
		// Default raw_message policy is on_error: only the partial parse keeps raw input.
		_, hasRaw := byKind[kind].Get("raw_message")
		_, hasErr := byKind[kind].Get("parse_error")
		if kind == "badsd" {
			if !hasRaw || !hasErr {
				t.Errorf("badsd: raw_message stored = %v, parse_error = %v; want both", hasRaw, hasErr)
			}
		} else if hasRaw || hasErr {
			t.Errorf("%s: raw_message stored = %v, parse_error = %v; want neither", kind, hasRaw, hasErr)
		}
	}

	// The same data through the authenticated search API.
	c := login(t, a)
	resp, body := c.post(t, "/api/v1/logs/search", map[string]any{
		"time_range": map[string]string{"from": "now-48h", "to": "now+1h"},
		"filter":     map[string]any{"op": "text", "value": id},
		"fields":     []string{"timestamp", "message", "hostname"},
	})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"returned":6`) || strings.Contains(string(body), "raw_message") {
		t.Errorf("search API: HTTP %d %s", resp.StatusCode, body)
	}

	// Readiness and metrics reflect the ingestion.
	resp, err = http.Get(fmt.Sprintf("http://%s/ready", a.Server().Addr()))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ready"`) {
		t.Errorf("ready: HTTP %d %s", resp.StatusCode, body)
	}
	resp, _ = http.Get(fmt.Sprintf("http://%s/metrics", a.Server().Addr()))
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{
		`syslogc_ingest_messages_stored_total{source="it-tcp"} 4`,
		`syslogc_storage_reachable{backend="victorialogs"} 1`,
		`syslogc_ingest_messages_stored_total{source="it-udp"} 2`,
		`syslogc_storage_healthy{backend="victorialogs"} 1`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

// TestLoggerCompatibility sends messages with util-linux logger, the tool
// used in the project's Definition of Done.
func TestLoggerCompatibility(t *testing.T) {
	if _, err := exec.LookPath("logger"); err != nil {
		t.Skip("logger not installed")
	}
	a := startApp(t, nil)
	_, udpPort, _ := net.SplitHostPort(a.Supervisor().Addr("it-udp").String())
	_, tcpPort, _ := net.SplitHostPort(a.Supervisor().Addr("it-tcp").String())
	id := runID()
	variants := [][]string{
		{"--udp", "--port", udpPort},
		{"--udp", "--port", udpPort, "--rfc3164"},
		{"--tcp", "--port", tcpPort},
		{"--tcp", "--port", tcpPort, "--octet-count"},
		{"--tcp", "--port", tcpPort, "--rfc3164", "-p", "local4.err", "-t", "vpnd"},
	}
	for i, v := range variants {
		args := append([]string{"--server", "127.0.0.1"}, v...)
		args = append(args, fmt.Sprintf("Test syslog message %s variant%d", id, i))
		if out, err := exec.Command("logger", args...).CombinedOutput(); err != nil {
			t.Fatalf("logger %v: %v: %s", args, err, out)
		}
	}
	rows := search(t, newBackend(t, "none"), id, len(variants))
	for _, r := range rows {
		msg := field(t, r, "_msg")
		if !strings.HasPrefix(msg, "Test syslog message "+id) {
			t.Errorf("message not parsed cleanly: %q", msg)
		}
		if _, ok := r.Get("parse_error"); ok {
			t.Errorf("parse error for %q: %v", msg, r)
		}
		if strings.HasSuffix(msg, "variant4") && (field(t, r, "severity") != "error" || field(t, r, "facility") != "local4" || field(t, r, "app_name") != "vpnd") {
			t.Errorf("priority/tag not honoured: %v", r)
		}
	}
}
