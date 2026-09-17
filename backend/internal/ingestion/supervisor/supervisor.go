// Package supervisor starts, tracks and stops ingestion sources. The set of
// sources is the union of the configuration file and the metadata store, and
// Reconcile applies changes to the running listeners without a restart.
package supervisor

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"reflect"
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
	// Origin is "file" for sources from the configuration file and
	// "database" for managed ones.
	Origin string `json:"origin,omitempty"`
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

	mu         sync.RWMutex
	running    map[string]*running
	desired    map[string]Desired
	status     map[string]Status
	httpSource *source.Settings
}

// HTTPSource returns the enabled http_json source, or nil.
func (s *Supervisor) HTTPSource() *source.Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.httpSource
}

// SourceSettings returns the runtime settings of all started sources.
func (s *Supervisor) SourceSettings() []*source.Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*source.Settings
	for _, r := range s.running {
		out = append(out, r.settings)
	}
	if s.httpSource != nil {
		out = append(out, s.httpSource)
	}
	return out
}

func New(sink listener.Sink, m *metrics.Metrics, log *slog.Logger) *Supervisor {
	return &Supervisor{
		sink:    sink,
		metrics: m,
		log:     log,
		running: make(map[string]*running),
		desired: make(map[string]Desired),
		status:  make(map[string]Status),
	}
}

// Start starts all enabled sources. A source that fails to start is reported
// with state "error" without affecting the others.
func (s *Supervisor) Start(sources []config.Source) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apply(FileSources(sources))
}

// Reconcile makes the running listeners match sources: it starts added ones,
// stops removed ones and restarts changed ones. Sources whose definition did
// not change are left alone, so their traffic is never interrupted.
func (s *Supervisor) Reconcile(ctx context.Context, sources []Desired) {
	s.mu.Lock()
	defer s.mu.Unlock()

	wanted := make(map[string]bool, len(sources))
	for _, d := range sources {
		wanted[d.Source.Name] = true
	}
	for name, prev := range s.desired {
		if !wanted[name] {
			s.stopSource(ctx, name, prev.Source)
			delete(s.desired, name)
			delete(s.status, name)
		}
	}

	var changed []Desired
	for _, d := range sources {
		if prev, ok := s.desired[d.Source.Name]; !ok || !reflect.DeepEqual(prev, d) {
			changed = append(changed, d)
		}
	}
	if len(changed) == 0 {
		return
	}
	// A source that keeps its address must release it before rebinding; one
	// that moves to a new address binds first, so it is never unreachable.
	var stopAfter []config.Source
	for _, d := range changed {
		prev, known := s.desired[d.Source.Name]
		if !known {
			continue
		}
		if sameBind(prev.Source, d.Source) || !d.Source.IsEnabled() {
			s.stopSource(ctx, d.Source.Name, prev.Source)
		} else {
			stopAfter = append(stopAfter, prev.Source)
		}
	}
	s.apply(changed)
	for _, prev := range stopAfter {
		s.stopSource(ctx, prev.Name, prev)
	}
	s.log.Info("sources reconciled", "changed", len(changed), "sources", len(sources))
}

// sameBind reports whether two definitions listen on the same address.
func sameBind(a, b config.Source) bool {
	return a.Type == b.Type && a.Protocol == b.Protocol && a.Address == b.Address
}

// stopSource stops one running source. The caller holds the lock.
func (s *Supervisor) stopSource(ctx context.Context, name string, prev config.Source) {
	if r, ok := s.running[name]; ok {
		if err := r.listener.Stop(ctx); err != nil {
			s.log.Warn("source did not stop cleanly", "source", name, "error", err)
		}
		delete(s.running, name)
	}
	if prev.Type == config.SourceTypeHTTPJSON && s.httpSource != nil && s.httpSource.Name == name {
		s.httpSource = nil
	}
	if st, ok := s.status[name]; ok && st.State == StateRunning {
		st.State, st.Since = StateStopped, time.Now()
		s.status[name] = st
	}
}

// apply starts the given sources and records their status. The caller holds
// the lock.
func (s *Supervisor) apply(sources []Desired) {
	now := time.Now()
	for _, d := range sources {
		sc := d.Source
		st := Status{Name: sc.Name, Type: sc.Type, Protocol: sc.Protocol, Address: sc.Address, Since: now}
		switch {
		case !sc.IsEnabled():
			st.State = StateDisabled
		case sc.Type == config.SourceTypeHTTPJSON:
			settings, err := source.New(sc, s.metrics)
			switch {
			case err != nil:
				st.State, st.Error = StateError, err.Error()
			case s.httpSource != nil:
				st.State, st.Error = StateError, "only one http_json source is supported; "+s.httpSource.Name+" is active"
			default:
				s.httpSource = settings
				st.State, st.Protocol, st.Address = StateRunning, "http", "POST /api/v1/ingest"
			}
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
		st.Origin = d.Origin
		s.status[sc.Name] = st
		s.desired[sc.Name] = d
	}
}

// Origins of a source definition.
const (
	OriginFile     = "file"
	OriginDatabase = "database"
)

// Desired is one source definition and where it came from.
type Desired struct {
	Source config.Source
	Origin string
}

// FileSources labels configuration-file sources.
func FileSources(sources []config.Source) []Desired {
	out := make([]Desired, 0, len(sources))
	for _, sc := range sources {
		out = append(out, Desired{Source: sc, Origin: OriginFile})
	}
	return out
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
	if s.httpSource != nil {
		st := s.status[s.httpSource.Name]
		st.State, st.Since = StateStopped, time.Now()
		s.status[s.httpSource.Name] = st
		s.httpSource = nil
	}
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
		if st.Type == config.SourceTypeSyslog && (st.State == StateRunning || st.State == StateError) {
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
