package forwarding

import (
	"context"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/freezxp/syslogc/backend/internal/logentry"
)

// Registry is the set of forwarders in use, which can change while logs are
// arriving.
//
// The ingestion path reads it on every batch, so the set is swapped as a
// whole rather than locked per message: a reconfiguration must never put a
// mutex between a log and storage. A target whose settings did not change is
// left running, so adding one does not interrupt the others.
type Registry struct {
	log *slog.Logger

	// current is read by the ingestion path without a lock.
	current atomic.Pointer[[]*Forwarder]

	// mu guards reconciliation against itself.
	mu      sync.Mutex
	desired map[string]Target
	started bool
}

// Target is one place to mirror to, and where its definition came from.
type Target struct {
	// Options are what the forwarder is built from. Two targets whose
	// Options match are the same target, and the running one is kept.
	Options Options
	// Origin is "file" or "database", for display.
	Origin string
	// Enabled is false for a target that is configured but not running.
	Enabled bool
}

// Origins of a target definition.
const (
	OriginFile     = "file"
	OriginDatabase = "database"
)

// NewRegistry returns an empty registry. Nothing is forwarded until
// Reconcile is called.
func NewRegistry(log *slog.Logger) *Registry {
	r := &Registry{log: log, desired: map[string]Target{}}
	empty := []*Forwarder{}
	r.current.Store(&empty)
	return r
}

// Forward mirrors a batch to every running target.
func (r *Registry) Forward(tenant string, entries []logentry.Entry) {
	for _, f := range *r.current.Load() {
		f.Forward(tenant, entries)
	}
}

// Start marks the registry live, so targets reconciled from now on are
// started as they are added.
func (r *Registry) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = true
	for _, f := range *r.current.Load() {
		f.Start()
	}
}

// Reconcile makes the running forwarders match targets: it starts added
// ones, stops removed ones, and replaces those whose settings changed. A
// target that did not change keeps its queue, so a change to one target does
// not drop another's backlog.
//
// build turns a target's options into a forwarder, which is where the
// remote client is opened; an error there leaves that one target out rather
// than failing the rest.
func (r *Registry) Reconcile(ctx context.Context, targets []Target, build func(Target) (*Forwarder, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()

	running := map[string]*Forwarder{}
	for _, f := range *r.current.Load() {
		running[f.Name()] = f
	}

	next := make([]*Forwarder, 0, len(targets))
	keep := map[string]bool{}
	desired := make(map[string]Target, len(targets))
	for _, t := range targets {
		name := t.Options.Name
		desired[name] = t
		if !t.Enabled {
			continue
		}
		if prev, ok := r.desired[name]; ok && running[name] != nil && reflect.DeepEqual(prev, t) {
			// Unchanged: keep the forwarder and whatever it has queued.
			next = append(next, running[name])
			keep[name] = true
			continue
		}
		f, err := build(t)
		if err != nil {
			r.log.Error("forward target could not be started", "target", name, "error", err)
			continue
		}
		if r.started {
			f.Start()
		}
		next = append(next, f)
		keep[name] = true
	}

	r.current.Store(&next)
	r.desired = desired

	// Stop what is no longer wanted, after the swap: a forwarder that is
	// still being handed batches must not be shut underneath them.
	for name, f := range running {
		if keep[name] {
			continue
		}
		r.log.Info("forward target stopped", "target", name)
		f.Stop(ctx)
	}
}

// Stop shuts every forwarder down.
func (r *Registry) Stop(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var wg sync.WaitGroup
	for _, f := range *r.current.Load() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.Stop(ctx)
		}()
	}
	wg.Wait()
	empty := []*Forwarder{}
	r.current.Store(&empty)
}

// Statuses reports every target, including ones configured but not running,
// so the interface can show a target that is off rather than omit it.
func (r *Registry) Statuses() []Status {
	r.mu.Lock()
	desired := make(map[string]Target, len(r.desired))
	for k, v := range r.desired {
		desired[k] = v
	}
	r.mu.Unlock()

	out := []Status{}
	seen := map[string]bool{}
	for _, f := range *r.current.Load() {
		st := f.Status()
		if t, ok := desired[st.Name]; ok {
			st.Origin, st.Enabled = t.Origin, true
		}
		seen[st.Name] = true
		out = append(out, st)
	}
	for name, t := range desired {
		if seen[name] {
			continue
		}
		st := Status{Name: name, Origin: t.Origin, Enabled: t.Enabled, Sources: t.Options.Sources}
		if t.Options.MinSeverity != nil {
			st.MinSeverity = t.Options.MinSeverity.String()
		}
		out = append(out, st)
	}
	return out
}
