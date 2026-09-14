// Package supervisor starts, tracks and stops the configured sources.
//
// Phase 1 supports sources defined in configuration only; runtime
// reconciliation with database-managed sources arrives with source
// management (Phase 5).
package supervisor

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/freezxp/syslogc/backend/internal/config"
	"github.com/freezxp/syslogc/backend/internal/ingestion/listener"
	"github.com/freezxp/syslogc/backend/internal/ingestion/source"
	"github.com/freezxp/syslogc/backend/internal/metrics"
)

// Source states.
const (
	StateRunning  = "running"
	StateStopped  = "stopped"
	StateError    = "error"
	StateDisabled = "disabled"
)

// Status describes one source.
type Status struct {
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Protocol string    `json:"protocol,omitempty"`
	Address  string    `json:"address,omitempty"`
	State    string    `json:"state"`
	Error    string    `json:"error,omitempty"`
	Since    time.Time `json:"since"`
}

type running struct {
	settings *source.Settings
	listener listener.Listener
}

// Supervisor owns the source listeners.
type Supervisor struct {
	sink    listener.Sink
	metrics *metrics.Metrics
	log     *slog.Logger

	mu      sync.RWMutex
	running map[string]*running
	status  map[string]Status
}

func New(sink listener.Sink, m *metrics.Metrics, log *slog.Logger) *Supervisor {
	return &Supervisor{
		sink:    sink,
		metrics: m,
		log:     log,
		running: make(map[string]*running),
		status:  make(map[string]Status),
	}
}

// Start starts all enabled syslog sources. A source that fails to start is
// reported with state "error" without affecting the others.
func (s *Supervisor) Start(sources []config.Source) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, sc := range sources {
		st := Status{Name: sc.Name, Type: sc.Type, Protocol: sc.Protocol, Address: sc.Address, Since: now}
		switch {
		case !sc.IsEnabled():
			st.State = StateDisabled
		case sc.Type != config.SourceTypeSyslog:
			// http_json sources are served by the API HTTP server (Phase 2).
			st.State = StateStopped
			st.Error = "http_json sources are not available until HTTP ingestion is implemented"
		default:
			r, err := s.start(sc)
			if err != nil {
				st.State, st.Error = StateError, err.Error()
				s.log.Error("source failed to start", "source", sc.Name, "protocol", sc.Protocol, "address", sc.Address, "error", err)
			} else {
				st.State = StateRunning
				st.Address = r.listener.Addr().String()
				s.running[sc.Name] = r
			}
		}
		s.status[sc.Name] = st
	}
}

func (s *Supervisor) start(sc config.Source) (*running, error) {
	settings, err := source.New(sc, s.metrics)
	if err != nil {
		return nil, err
	}
	var l listener.Listener
	switch sc.Protocol {
	case config.ProtocolUDP:
		l = listener.NewUDP(settings, s.sink, s.log)
	case config.ProtocolTCP:
		l = listener.NewTCP(settings, s.sink, s.log, nil)
	case config.ProtocolTLS:
		tlsCfg, err := listener.NewTLSConfig(sc.TLS, s.log.With("source", sc.Name))
		if err != nil {
			return nil, err
		}
		l = listener.NewTCP(settings, s.sink, s.log, tlsCfg)
	default:
		return nil, errors.New("unsupported protocol " + sc.Protocol)
	}
	if err := l.Start(); err != nil {
		return nil, err
	}
	return &running{settings: settings, listener: l}, nil
}

// Stop stops all listeners concurrently.
func (s *Supervisor) Stop(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var wg sync.WaitGroup
	for name, r := range s.running {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.listener.Stop(ctx); err != nil {
				s.log.Warn("source did not stop cleanly", "source", name, "error", err)
			}
		}()
		st := s.status[name]
		st.State, st.Since = StateStopped, time.Now()
		s.status[name] = st
	}
	wg.Wait()
	s.running = make(map[string]*running)
}

// Statuses returns all source statuses sorted by name.
func (s *Supervisor) Statuses() []Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Status, 0, len(s.status))
	for _, st := range s.status {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Ready reports whether ingestion can serve: at least one syslog source is
// running, or none were configured to run.
func (s *Supervisor) Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	wanted := 0
	for _, st := range s.status {
		if st.State == StateRunning || st.State == StateError {
			wanted++
		}
	}
	return wanted == 0 || len(s.running) > 0
}

// Addr returns the bound address of a running source (useful in tests).
func (s *Supervisor) Addr(name string) net.Addr {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if r, ok := s.running[name]; ok {
		return r.listener.Addr()
	}
	return nil
}
