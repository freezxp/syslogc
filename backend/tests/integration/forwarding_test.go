//go:build integration

package integration

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/freezxp/syslogc/backend/internal/config"
	"github.com/freezxp/syslogc/backend/internal/storage"
)

// forwardURL is a second VictoriaLogs instance receiving mirrored logs.
func forwardURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_VICTORIALOGS_FORWARD_URL")
	if u == "" {
		t.Skip("TEST_VICTORIALOGS_FORWARD_URL not set")
	}
	return u
}

// TestForwardingMirrorsToASecondInstance covers the live mirror: logs stored
// locally arrive at the remote too, filters apply, and a remote outage never
// stops local ingestion.
func TestForwardingMirrorsToASecondInstance(t *testing.T) {
	remote := forwardURL(t)
	id := runID()
	a := startApp(t, func(cfg *config.Config) {
		cfg.Forwarding.Targets = []config.ForwardTarget{{
			Name: "dr", URL: remote, Sources: []string{"it-udp"}, MinSeverity: "warning",
			Batch: config.BatchConfig{MaxWait: config.Duration(100 * time.Millisecond)},
		}}
		// applyDefaultsAndValidate fills the rest of the target's defaults.
	})

	udpAddr := a.Supervisor().Addr("it-udp").String()
	conn, err := net.Dial("udp", udpAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Three messages that must be mirrored, one below the severity threshold
	// and one from another source, which must not be.
	for i := range 3 {
		sendUDP(t, a, conn, fmt.Sprintf("<11>1 - fw01 app - - - forwarded error %d %s", i, id))
	}
	sendUDP(t, a, conn, fmt.Sprintf("<14>1 - fw01 app - - - local only info %s", id))

	tcpAddr := a.Supervisor().Addr("it-tcp").String()
	tcp, err := net.Dial("tcp", tcpAddr)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(tcp, "<11>1 - db01 app - - - other source error %s\n", id)
	tcp.Close()

	local := newBackend(t, "none")
	localRows := search(t, local, id, 5)
	if len(localRows) != 5 {
		t.Fatalf("local storage has %d rows, want 5", len(localRows))
	}

	// The remote receives only what the filters allow.
	mirror := backendFor(t, remote)
	rows := search(t, mirror, id, 3)
	for _, r := range rows {
		msg := field(t, r, "_msg")
		if !strings.Contains(msg, "forwarded error") {
			t.Errorf("mirrored a message the filters should have excluded: %q", msg)
		}
		if got := field(t, r, "source"); got != "it-udp" {
			t.Errorf("mirrored row has source %q", got)
		}
		if got := field(t, r, "severity"); got != "error" {
			t.Errorf("mirrored row has severity %q", got)
		}
	}
	time.Sleep(time.Second)
	if extra := countRows(t, mirror, id); extra != 3 {
		t.Errorf("remote holds %d rows, want exactly 3", extra)
	}

	// Ingestion continues while the remote is unreachable: the forwarder
	// buffers, then drops its copies, and local storage is unaffected.
	unreachable := runID()
	a2 := startApp(t, func(cfg *config.Config) {
		cfg.Forwarding.Targets = []config.ForwardTarget{{
			Name: "down", URL: "http://127.0.0.1:1", // nothing listens here
			Queue: config.ForwardQueueConfig{MaxMessages: 10},
			Batch: config.BatchConfig{MaxWait: config.Duration(50 * time.Millisecond)},
			Retry: config.RetryConfig{InitialBackoff: config.Duration(10 * time.Millisecond), MaxBackoff: config.Duration(50 * time.Millisecond)},
		}}
	})
	conn2, err := net.Dial("udp", a2.Supervisor().Addr("it-udp").String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	start := time.Now()
	for i := range 20 {
		sendUDP(t, a2, conn2, fmt.Sprintf("<11>1 - fw01 app - - - still ingesting %d %s", i, unreachable))
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("ingestion took %s while the forward target was down", elapsed)
	}
	if got := len(search(t, local, unreachable, 20)); got != 20 {
		t.Errorf("local storage has %d rows while the remote was down, want 20", got)
	}

	// The API reports the unreachable target.
	c := login(t, a2)
	resp, body := c.do(t, "GET", "/api/v1/system/ingestion", nil, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"name":"down"`) {
		t.Fatalf("system ingestion: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"healthy":false`) {
		t.Errorf("target not reported unhealthy: %s", body)
	}
}

// backendFor opens a querier against another VictoriaLogs instance.
func backendFor(t *testing.T, url string) storage.LogQuerier {
	t.Helper()
	return newBackendURL(t, url, "none")
}

// countRows counts rows matching the run id in a backend.
func countRows(t *testing.T, q storage.LogQuerier, id string) int64 {
	t.Helper()
	now := time.Now()
	n, err := q.Count(context.Background(), storage.CountQuery{Selection: storage.Selection{
		Range:  storage.TimeRange{Start: now.Add(-time.Hour), End: now.Add(time.Hour)},
		Native: &storage.NativeQuery{Dialect: "logsql", Text: id},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
