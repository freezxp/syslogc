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
