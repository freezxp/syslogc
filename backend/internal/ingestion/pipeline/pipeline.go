// Package pipeline implements the bounded ingestion pipeline:
//
//	listeners → ingest queue (count + byte budget) → parse workers →
//	per-worker batchers → batch queue → storage writers (retry/bisect)
//
// Memory is bounded by configuration regardless of storage speed. Overflow
// is protocol-specific: TryEnqueue drops (UDP), Enqueue blocks (TCP/TLS).
// Every loss increments syslogc_ingest_messages_dropped_total.
package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/freezxp/syslogc/backend/internal/ingestion/source"
	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/metrics"
	"github.com/freezxp/syslogc/backend/internal/normalization"
	"github.com/freezxp/syslogc/backend/internal/parser"
	"github.com/freezxp/syslogc/backend/internal/parser/rfc3164"
	"github.com/freezxp/syslogc/backend/internal/parser/rfc5424"
	"github.com/freezxp/syslogc/backend/internal/storage"
)

// ErrClosed is returned when enqueueing into a closed pipeline.
var ErrClosed = errors.New("pipeline closed")

// messageOverhead approximates per-message memory beyond the payload bytes.
const messageOverhead = 256

// RawMessage is one framed message handed over by a listener.
type RawMessage struct {
	Data       string
	ReceivedAt time.Time
	Peer       netip.AddrPort
	// Truncated is set when the listener cut the message at max size.
	Truncated bool
	Source    *source.Settings
}

func (m *RawMessage) weight() int64 { return int64(len(m.Data) + messageOverhead) }

// Config holds pipeline sizing.
type Config struct {
	QueueMaxMessages int
	QueueMaxBytes    int64
	Workers          int
	BatchMaxRows     int
	BatchMaxBytes    int
	BatchMaxWait     time.Duration
	Writers          int
	// BatchQueue is the number of flushed batches waiting for writers.
	BatchQueue     int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Normalization  normalization.Options
}

// Pipeline is the ingestion pipeline. Create with New, then Start.
type Pipeline struct {
	cfg      Config
	writer   storage.LogWriter
	backend  string
	metrics  *metrics.Metrics
	log      *slog.Logger
	registry *parser.Registry
	fallback parser.Parser
	norm     *normalization.Normalizer

	queue      chan RawMessage
	budget     *semaphore.Weighted
	queueBytes atomic.Int64

	batches   chan *batch
	batchPool sync.Pool

	mu         sync.RWMutex // guards closed and close(queue)
	closed     bool
	stopCtx    context.Context
	stop       context.CancelFunc
	writeCtx   context.Context
	cancelIO   context.CancelFunc
	closeOnce  sync.Once
	workersWG  sync.WaitGroup
	writersWG  sync.WaitGroup
	healthy    atomic.Bool
	lastErrLog atomic.Int64
}

// New creates a pipeline writing to w.
func New(cfg Config, w storage.LogWriter, backendName string, m *metrics.Metrics, log *slog.Logger) *Pipeline {
	if cfg.BatchQueue <= 0 {
		cfg.BatchQueue = 2 * cfg.Writers
	}
	p := &Pipeline{
		cfg:      cfg,
		writer:   w,
		backend:  backendName,
		metrics:  m,
		log:      log,
		registry: parser.NewRegistry(rfc5424.New(), rfc3164.New()),
		fallback: rfc3164.New(),
		norm:     normalization.New(cfg.Normalization),
		queue:    make(chan RawMessage, cfg.QueueMaxMessages),
		budget:   semaphore.NewWeighted(cfg.QueueMaxBytes),
		batches:  make(chan *batch, cfg.BatchQueue),
	}
	p.batchPool.New = func() any { return &batch{} }
	p.stopCtx, p.stop = context.WithCancel(context.Background())
	p.writeCtx, p.cancelIO = context.WithCancel(context.Background())
	p.healthy.Store(true)
	m.StorageHealthy.WithLabelValues(backendName).Set(1)

	m.Registry.MustRegister(
		newGaugeFunc("ingest_queue_messages", "Messages waiting in the ingest queue.", func() float64 { return float64(len(p.queue)) }),
		newGaugeFunc("ingest_queue_bytes", "Byte budget used by the ingest queue.", func() float64 { return float64(p.queueBytes.Load()) }),
		newGaugeFunc("ingest_queue_capacity_bytes", "Byte budget of the ingest queue.", func() float64 { return float64(cfg.QueueMaxBytes) }),
		newGaugeFunc("ingest_queue_capacity_messages", "Message capacity of the ingest queue.", func() float64 { return float64(cfg.QueueMaxMessages) }),
		newGaugeFunc("ingest_batch_queue_batches", "Flushed batches waiting for storage writers.", func() float64 { return float64(len(p.batches)) }),
	)
	return p
}

// Start launches workers and writers.
func (p *Pipeline) Start() {
	for range p.cfg.Writers {
		p.writersWG.Add(1)
		go p.runWriter()
	}
	for range p.cfg.Workers {
		p.workersWG.Add(1)
		go p.runWorker()
	}
}

// TryEnqueue adds msg without blocking. It returns false, counting the drop,
// when the queue is full or closed. Used by UDP listeners.
func (p *Pipeline) TryEnqueue(msg RawMessage) bool {
	w := msg.weight()
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		msg.Source.Metrics.DroppedShutdown.Inc()
		return false
	}
	if !p.budget.TryAcquire(w) {
		msg.Source.Metrics.DroppedQueueFull.Inc()
		return false
	}
	select {
	case p.queue <- msg:
		p.queueBytes.Add(w)
		return true
	default:
		p.budget.Release(w)
		msg.Source.Metrics.DroppedQueueFull.Inc()
		return false
	}
}

// Enqueue adds msg, blocking while the queue is full. It returns ErrClosed
// if the pipeline is closing and ctx.Err() if ctx is done; in both cases
// the message is not queued and the caller owns accounting for it.
func (p *Pipeline) Enqueue(ctx context.Context, msg RawMessage) error {
	w := msg.weight()
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return ErrClosed
	}
	// Fast path: no contention.
	if p.budget.TryAcquire(w) {
		select {
		case p.queue <- msg:
			p.queueBytes.Add(w)
			return nil
		default:
			p.budget.Release(w)
		}
	}
	// Slow path: wait for budget and queue space, aborting on close.
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopAfter := context.AfterFunc(p.stopCtx, cancel)
	defer stopAfter()
	if err := p.budget.Acquire(waitCtx, w); err != nil {
		return p.waitErr(ctx)
	}
	select {
	case p.queue <- msg:
		p.queueBytes.Add(w)
		return nil
	case <-waitCtx.Done():
		p.budget.Release(w)
		return p.waitErr(ctx)
	}
}

func (p *Pipeline) waitErr(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrClosed
}

// Saturated reports whether the ingest queue is more than 95% full.
func (p *Pipeline) Saturated() bool {
	return len(p.queue) > cap(p.queue)*95/100 || p.queueBytes.Load() > p.cfg.QueueMaxBytes*95/100
}

// Healthy reports whether the most recent storage write succeeded.
func (p *Pipeline) Healthy() bool { return p.healthy.Load() }

// Close stops accepting messages, drains the queue and flushes batches.
// If ctx expires first, in-flight writes are cancelled and remaining rows
// are counted as dropped with reason "shutdown".
func (p *Pipeline) Close(ctx context.Context) error {
	p.closeOnce.Do(func() {
		p.stop() // wake producers blocked in Enqueue
		p.mu.Lock()
		p.closed = true
		close(p.queue)
		p.mu.Unlock()
		go func() {
			p.workersWG.Wait()
			close(p.batches)
		}()
	})
	done := make(chan struct{})
	go func() {
		p.workersWG.Wait()
		p.writersWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		p.cancelIO()
		<-done
		return ctx.Err()
	}
}

// batch is a logentry.Batch plus index-aligned per-entry source settings.
type batch struct {
	entries logentry.Batch
	sources []*source.Settings
}

func (p *Pipeline) getBatch(tenant string) *batch {
	b := p.batchPool.Get().(*batch)
	b.entries.Reset()
	b.entries.Tenant = tenant
	b.sources = b.sources[:0]
	return b
}

func (p *Pipeline) putBatch(b *batch) {
	if cap(b.entries.Entries) > p.cfg.BatchMaxRows*2 {
		return // let unusually large batches be collected
	}
	for i := range b.sources {
		b.sources[i] = nil
	}
	p.batchPool.Put(b)
}

func (p *Pipeline) logThrottled(msg string, args ...any) {
	now := time.Now().UnixNano()
	last := p.lastErrLog.Load()
	if now-last < int64(10*time.Second) || !p.lastErrLog.CompareAndSwap(last, now) {
		return
	}
	p.log.Warn(msg, args...)
}

// jitter returns a random duration in [d/2, d].
func jitter(d time.Duration) time.Duration {
	if d <= 1 {
		return d
	}
	return d/2 + rand.N(d/2)
}
