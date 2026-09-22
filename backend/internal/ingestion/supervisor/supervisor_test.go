package supervisor

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/freezxp/syslogc/backend/internal/config"
	"github.com/freezxp/syslogc/backend/internal/ingestion/pipeline"
	"github.com/freezxp/syslogc/backend/internal/metrics"
)

type nopSink struct{}

func (nopSink) TryEnqueue(pipeline.RawMessage) bool                { return true }
func (nopSink) Enqueue(context.Context, pipeline.RawMessage) error { return nil }

func sourceConfig(name, protocol, addr string, enabled bool) config.Source {
	s := config.Source{
		Name: name, Type: "syslog", Protocol: protocol, Address: addr, Enabled: &enabled,
		Format: "auto", Timezone: "UTC", RawMessage: "always", Tenant: "default",
		HostnameFallback: "none", SDFlatten: "full", Framing: "auto", MaxMessageBytes: 1024,
		MaxConnections: 10, IdleTimeout: config.Duration(time.Minute),
	}
	return s
}

func TestSupervisorStartStop(t *testing.T) {
	// Occupy a TCP port so one source fails to bind.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	s := New(nopSink{}, metrics.New("t", "t"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.Start([]config.Source{
		sourceConfig("udp", "udp", "127.0.0.1:0", true),
		sourceConfig("tcp-busy", "tcp", busy.Addr().String(), true),
		sourceConfig("off", "tcp", "127.0.0.1:0", false),
	})

	states := map[string]Status{}
	for _, st := range s.Statuses() {
		states[st.Name] = st
	}
	if states["udp"].State != StateRunning || s.Addr("udp") == nil {
		t.Errorf("udp: %+v", states["udp"])
	}
	if states["tcp-busy"].State != StateError || states["tcp-busy"].Error == "" {
		t.Errorf("bind conflict not reported: %+v", states["tcp-busy"])
	}
	if states["off"].State != StateDisabled {
		t.Errorf("disabled source: %+v", states["off"])
	}
	if !s.Ready() {
		t.Error("Ready() = false with one running source")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.Stop(ctx)
	for _, st := range s.Statuses() {
		if st.State == StateRunning {
			t.Errorf("%s still running after Stop", st.Name)
		}
	}
}

func TestSupervisorNotReadyWhenAllFail(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	s := New(nopSink{}, metrics.New("t", "t"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.Start([]config.Source{sourceConfig("tcp-busy", "tcp", busy.Addr().String(), true)})
	if s.Ready() {
		t.Error("Ready() = true although every source failed")
	}

	empty := New(nopSink{}, metrics.New("t", "t"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	empty.Start(nil)
	if !empty.Ready() {
		t.Error("Ready() = false with no sources configured (API-only style config)")
	}
}

// dial reports whether a UDP datagram to addr is accepted by a listener.
func udpPort(t *testing.T, s *Supervisor, name string) int {
	t.Helper()
	addr := s.Addr(name)
	if addr == nil {
		t.Fatalf("source %s is not running", name)
	}
	return addr.(*net.UDPAddr).Port
}

func TestSupervisorReconcile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := New(nopSink{}, metrics.New("t", "t"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.Start([]config.Source{sourceConfig("file-udp", "udp", "127.0.0.1:0", true)})
	filePort := udpPort(t, s, "file-udp")

	// Adding a managed source leaves the existing listener untouched.
	added := Desired{Source: sourceConfig("managed", "udp", "127.0.0.1:0", true), Origin: OriginDatabase}
	s.Reconcile(ctx, append(FileSources([]config.Source{sourceConfig("file-udp", "udp", "127.0.0.1:0", true)}), added))
	if got := udpPort(t, s, "file-udp"); got != filePort {
		t.Errorf("unchanged source was restarted: port %d -> %d", filePort, got)
	}
	managedPort := udpPort(t, s, "managed")
	states := map[string]Status{}
	for _, st := range s.Statuses() {
		states[st.Name] = st
	}
	if states["managed"].State != StateRunning || states["managed"].Origin != OriginDatabase {
		t.Fatalf("managed source: %+v", states["managed"])
	}
	if states["file-udp"].Origin != OriginFile {
		t.Errorf("file source origin: %+v", states["file-udp"])
	}

	// Reconciling the same set again is a no-op.
	s.Reconcile(ctx, append(FileSources([]config.Source{sourceConfig("file-udp", "udp", "127.0.0.1:0", true)}), added))
	if udpPort(t, s, "managed") != managedPort {
		t.Error("identical definition caused a restart")
	}

	// Changing the address rebinds the source.
	moved := Desired{Source: sourceConfig("managed", "udp", "127.0.0.1:0", true), Origin: OriginDatabase}
	moved.Source.MaxMessageBytes = 2048
	s.Reconcile(ctx, append(FileSources([]config.Source{sourceConfig("file-udp", "udp", "127.0.0.1:0", true)}), moved))
	if udpPort(t, s, "managed") == managedPort {
		t.Error("changed definition did not restart the listener")
	}

	// Disabling stops the listener but keeps the source listed.
	disabled := Desired{Source: sourceConfig("managed", "udp", "127.0.0.1:0", false), Origin: OriginDatabase}
	s.Reconcile(ctx, append(FileSources([]config.Source{sourceConfig("file-udp", "udp", "127.0.0.1:0", true)}), disabled))
	if s.Addr("managed") != nil {
		t.Error("disabled source is still bound")
	}
	states = map[string]Status{}
	for _, st := range s.Statuses() {
		states[st.Name] = st
	}
	if states["managed"].State != StateDisabled {
		t.Errorf("disabled source state: %+v", states["managed"])
	}

	// Removing it drops the listener and the status entry.
	s.Reconcile(ctx, FileSources([]config.Source{sourceConfig("file-udp", "udp", "127.0.0.1:0", true)}))
	for _, st := range s.Statuses() {
		if st.Name == "managed" {
			t.Errorf("removed source still listed: %+v", st)
		}
	}
	if udpPort(t, s, "file-udp") != filePort {
		t.Error("removing a source disturbed another one")
	}

	// An http_json source is tracked without a listener.
	httpSrc := sourceConfig("http", "", "", true)
	httpSrc.Type = "http_json"
	s.Reconcile(ctx, append(FileSources([]config.Source{sourceConfig("file-udp", "udp", "127.0.0.1:0", true)}),
		Desired{Source: httpSrc, Origin: OriginDatabase}))
	if s.HTTPSource() == nil || s.HTTPSource().Name != "http" {
		t.Errorf("http source not registered: %+v", s.HTTPSource())
	}
	s.Reconcile(ctx, FileSources([]config.Source{sourceConfig("file-udp", "udp", "127.0.0.1:0", true)}))
	if s.HTTPSource() != nil {
		t.Error("http source still registered after removal")
	}
}

func TestReconcileKeepsTheOldListenerWhenTheNewOneFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Something else already holds the address the source is moved to.
	busy, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	s := New(nopSink{}, metrics.New("t", "t"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.Start([]config.Source{sourceConfig("moving", "udp", "127.0.0.1:0", true)})
	before := udpPort(t, s, "moving")

	moved := Desired{Source: sourceConfig("moving", "udp", busy.LocalAddr().String(), true), Origin: OriginFile}
	s.Reconcile(ctx, []Desired{moved})

	if s.Addr("moving") == nil {
		t.Fatal("the source was left with no listener after a failed rebind")
	}
	if got := udpPort(t, s, "moving"); got != before {
		t.Errorf("listener moved to %d although the new address was unusable", got)
	}

	// The next reconcile to a usable address succeeds, so the failure is not
	// remembered as the desired state.
	ok := Desired{Source: sourceConfig("moving", "udp", "127.0.0.1:0", true), Origin: OriginFile}
	ok.Source.MaxMessageBytes = 2048
	s.Reconcile(ctx, []Desired{ok})
	if s.Addr("moving") == nil {
		t.Error("source not running after a later successful reconcile")
	}
}

func TestReconcilePinsASourceToThePortItAlreadyHolds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := New(nopSink{}, metrics.New("t", "t"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.Start([]config.Source{sourceConfig("pinned", "udp", "127.0.0.1:0", true)})
	addr := s.Addr("pinned").String()

	// Rewriting ":0" as the port that was handed out is the same socket, so
	// the listener must release it before rebinding rather than fight itself.
	pinned := sourceConfig("pinned", "udp", addr, true)
	pinned.MaxMessageBytes = 4096
	s.Reconcile(ctx, []Desired{{Source: pinned, Origin: OriginFile}})

	if got := s.Addr("pinned"); got == nil || got.String() != addr {
		t.Fatalf("addr = %v, want the source still bound to %s", got, addr)
	}
	for _, st := range s.Statuses() {
		if st.Name == "pinned" && st.State != StateRunning {
			t.Fatalf("state = %s (%s), want running", st.State, st.Error)
		}
	}
}
