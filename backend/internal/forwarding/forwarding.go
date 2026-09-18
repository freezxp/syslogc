// Package forwarding mirrors stored logs to other storage instances.
//
// A forwarder receives the entries the pipeline has just stored locally and
// writes a copy to a remote backend. It never blocks local ingestion: each
// target owns a bounded queue, and when a remote stays unreachable long
// enough to fill it, forwarded copies are dropped and counted. Local storage
// and search are unaffected either way.
package forwarding

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/metrics"
	"github.com/freezxp/syslogc/backend/internal/storage"
)

// Drop reasons reported in syslogc_forward_messages_dropped_total.
const (
	DropQueueFull = "queue_full"
	DropRejected  = "rejected"
	DropShutdown  = "shutdown"
)

// Options configures one target.
type Options struct {
	Name string
	// Writer is the remote backend.
	Writer storage.LogWriter
	// QueueMaxMessages and QueueMaxBytes bound the in-memory buffer.
	QueueMaxMessages int
	QueueMaxBytes    int64
	BatchMaxRows     int
	BatchMaxBytes    int
	BatchMaxWait     time.Duration
	InitialBackoff   time.Duration
	MaxBackoff       time.Duration
	// Sources restricts forwarding to these source names; empty forwards all.
	Sources []string
	// MinSeverity forwards only entries at this severity or more severe
	// (numerically lower). Nil forwards every severity.
	MinSeverity *logentry.Severity
	Metrics     *metrics.Metrics
	Log         *slog.Logger
}

// Forwarder mirrors entries to one remote backend.
type Forwarder struct {
	opts    Options
	sources map[string]bool
	m       *metrics.ForwardMetrics

	queue    chan []logentry.Entry
	queued   atomic.Int64 // messages waiting, including the batch being written
	bytes    atomic.Int64
	sent     atomic.Int64
	dropped  atomic.Int64
	healthy  atomic.Bool
	lastSend atomic.Int64 // unix nanos of the last successful write
	lastErr  atomic.Pointer[string]

	stopCtx context.Context
	stop    context.CancelFunc
	mu      sync.RWMutex
	closed  bool
	wg      sync.WaitGroup
}

// New creates a forwarder. Start must be called to begin sending.
func New(opts Options) *Forwarder {
	if opts.BatchMaxRows <= 0 {
		opts.BatchMaxRows = 10_000
	}
	if opts.BatchMaxWait <= 0 {
		opts.BatchMaxWait = time.Second
	}
	if opts.QueueMaxMessages <= 0 {
		opts.QueueMaxMessages = 200_000
	}
	if opts.InitialBackoff <= 0 {
		opts.InitialBackoff = 250 * time.Millisecond
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 30 * time.Second
	}
	f := &Forwarder{
		opts: opts,
		// One channel slot per batch-sized group keeps the queue bounded by
		// messages rather than by an arbitrary number of sends.
		queue: make(chan []logentry.Entry, max(opts.QueueMaxMessages/opts.BatchMaxRows, 8)),
		m:     opts.Metrics.Forward(opts.Name),
	}
	if len(opts.Sources) > 0 {
		f.sources = make(map[string]bool, len(opts.Sources))
		for _, s := range opts.Sources {
			f.sources[s] = true
		}
	}
	f.healthy.Store(true)
	f.stopCtx, f.stop = context.WithCancel(context.Background())
	f.m.QueueMessages(func() float64 { return float64(f.queued.Load()) })
	f.m.Healthy(func() float64 {
		if f.healthy.Load() {
			return 1
		}
		return 0
	})
	f.m.LastSuccess(func() float64 { return float64(f.lastSend.Load()) / 1e9 })
	return f
}

func (f *Forwarder) Name() string { return f.opts.Name }

// Start runs the sender goroutine.
func (f *Forwarder) Start() {
	f.wg.Add(1)
	go f.run()
}

// Forward queues a copy of entries. It never blocks: when the queue is full
// the entries are dropped and counted, so local ingestion keeps its pace.
func (f *Forwarder) Forward(tenant string, entries []logentry.Entry) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return
	}
	// Entries belong to a pooled batch that is reset as soon as this returns,
	// so anything queued must be copied, including dynamic fields.
	var out []logentry.Entry
	var size int64
	for i := range entries {
		e := &entries[i]
		if f.sources != nil && !f.sources[e.Source] {
			continue
		}
		if f.opts.MinSeverity != nil && e.Severity > *f.opts.MinSeverity {
			continue
		}
		if out == nil {
			out = make([]logentry.Entry, 0, len(entries)-i)
		}
		cp := *e
		cp.Tenant = tenant
		cp.Fields = append([]logentry.Field(nil), e.Fields...)
		out = append(out, cp)
		size += int64(e.EstimateSize())
	}
	if len(out) == 0 {
		return
	}
	if f.queued.Load()+int64(len(out)) > int64(f.opts.QueueMaxMessages) ||
		(f.opts.QueueMaxBytes > 0 && f.bytes.Load()+size > f.opts.QueueMaxBytes) {
		f.drop(DropQueueFull, len(out))
		return
	}
	select {
	case f.queue <- out:
		f.queued.Add(int64(len(out)))
		f.bytes.Add(size)
	default:
		f.drop(DropQueueFull, len(out))
	}
}

// Stop drains the queue within ctx and stops the sender.
func (f *Forwarder) Stop(ctx context.Context) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	close(f.queue)
	f.mu.Unlock()

	done := make(chan struct{})
	go func() {
		f.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		f.stop() // abandon in-flight writes
		<-done
	}
}

func (f *Forwarder) run() {
	defer f.wg.Done()
	timer := time.NewTimer(f.opts.BatchMaxWait)
	defer timer.Stop()
	var pending []logentry.Entry
	var tenant string
	var size int

	flush := func() {
		if len(pending) > 0 {
			f.write(tenant, pending)
			pending, size = pending[:0], 0
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(f.opts.BatchMaxWait)
	}
	for {
		select {
		case group, ok := <-f.queue:
			if !ok {
				flush()
				return
			}
			// A batch carries one tenant; a change flushes what is pending.
			if len(pending) > 0 && group[0].Tenant != tenant {
				flush()
			}
			tenant = group[0].Tenant
			for i := range group {
				pending = append(pending, group[i])
				size += group[i].EstimateSize()
				if len(pending) >= f.opts.BatchMaxRows || (f.opts.BatchMaxBytes > 0 && size >= f.opts.BatchMaxBytes) {
					flush()
				}
			}
		case <-timer.C:
			flush()
		}
	}
}

// write sends one batch, retrying transient failures until it succeeds or
// the forwarder is stopped. Rejected batches are dropped: retrying data the
// remote refuses would stall every log behind it.
func (f *Forwarder) write(tenant string, entries []logentry.Entry) {
	batch := &logentry.Batch{Tenant: tenant, Entries: entries}
	backoff := f.opts.InitialBackoff
	var size int64
	for i := range entries {
		size += int64(entries[i].EstimateSize())
	}
	defer func() {
		f.queued.Add(-int64(len(entries)))
		f.bytes.Add(-size)
	}()
	for {
		err := f.opts.Writer.WriteBatch(f.stopCtx, batch)
		if err == nil {
			f.m.Sent.Add(float64(len(entries)))
			f.sent.Add(int64(len(entries)))
			f.lastSend.Store(time.Now().UnixNano())
			f.setHealthy(true)
			f.lastErr.Store(nil)
			return
		}
		if f.stopCtx.Err() != nil {
			f.drop(DropShutdown, len(entries))
			return
		}
		class := storage.ClassOf(err)
		f.m.Errors(class.String()).Inc()
		f.setHealthy(false)
		msg := err.Error()
		f.lastErr.Store(&msg)
		if class == storage.Rejected {
			f.opts.Log.Warn("forward target rejected a batch; dropping it",
				"target", f.opts.Name, "rows", len(entries), "error", err)
			f.drop(DropRejected, len(entries))
			return
		}
		f.opts.Log.Warn("forward write failed; retrying", "target", f.opts.Name, "error", err, "backoff", backoff)
		select {
		case <-f.stopCtx.Done():
			f.drop(DropShutdown, len(entries))
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > f.opts.MaxBackoff {
			backoff = f.opts.MaxBackoff
		}
	}
}

// drop records messages that will never reach the remote.
func (f *Forwarder) drop(reason string, n int) {
	f.m.Dropped(reason).Add(float64(n))
	f.dropped.Add(int64(n))
}

func (f *Forwarder) setHealthy(ok bool) {
	if f.healthy.Swap(ok) != ok {
		if ok {
			f.opts.Log.Info("forward target healthy again", "target", f.opts.Name)
		}
	}
}

// Status describes a target for the system API.
type Status struct {
	Name           string `json:"name"`
	Healthy        bool   `json:"healthy"`
	QueuedMessages int64  `json:"queued_messages"`
	// SentMessages and DroppedMessages count since this node started.
	SentMessages    int64     `json:"sent_messages"`
	DroppedMessages int64     `json:"dropped_messages"`
	LastSuccessAt   time.Time `json:"last_success_at,omitzero"`
	// LastError explains an unhealthy target; empty while writes succeed.
	LastError   string   `json:"last_error,omitempty"`
	MinSeverity string   `json:"min_severity,omitempty"`
	Sources     []string `json:"sources,omitempty"`
}

func (f *Forwarder) Status() Status {
	st := Status{
		Name: f.opts.Name, Healthy: f.healthy.Load(), QueuedMessages: f.queued.Load(),
		SentMessages: f.sent.Load(), DroppedMessages: f.dropped.Load(), Sources: f.opts.Sources,
	}
	if msg := f.lastErr.Load(); msg != nil {
		st.LastError = *msg
	}
	if ns := f.lastSend.Load(); ns > 0 {
		st.LastSuccessAt = time.Unix(0, ns).UTC()
	}
	if f.opts.MinSeverity != nil {
		st.MinSeverity = f.opts.MinSeverity.String()
	}
	return st
}

// Fanout forwards to every target. It implements the pipeline's tee.
type Fanout []*Forwarder

func (fs Fanout) Forward(tenant string, entries []logentry.Entry) {
	for _, f := range fs {
		f.Forward(tenant, entries)
	}
}

func (fs Fanout) Start() {
	for _, f := range fs {
		f.Start()
	}
}

func (fs Fanout) Stop(ctx context.Context) {
	var wg sync.WaitGroup
	for _, f := range fs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.Stop(ctx)
		}()
	}
	wg.Wait()
}

func (fs Fanout) Statuses() []Status {
	out := make([]Status, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Status())
	}
	return out
}
