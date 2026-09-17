package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/freezxp/syslogc/backend/internal/config"
	"github.com/freezxp/syslogc/backend/internal/ingestion/source"
	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/metrics"
	"github.com/freezxp/syslogc/backend/internal/normalization"
	"github.com/freezxp/syslogc/backend/internal/storage"
)

// fakeWriter records written entries and can fail or block on demand.
type fakeWriter struct {
	mu      sync.Mutex
	entries []logentry.Entry
	calls   int
	// fail, if set, decides the error for each call.
	fail func(call int, batch *logentry.Batch) error
	// gate, if set, blocks each write until it is closed.
	gate chan struct{}
}

func (f *fakeWriter) WriteBatch(ctx context.Context, b *logentry.Batch) error {
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		if err := f.fail(f.calls, b); err != nil {
			return err
		}
	}
	for _, e := range b.Entries {
		e.Fields = append([]logentry.Field(nil), e.Fields...)
		f.entries = append(f.entries, e)
	}
	return nil
}

func (f *fakeWriter) snapshot() []logentry.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]logentry.Entry(nil), f.entries...)
}

type harness struct {
	p   *Pipeline
	m   *metrics.Metrics
	src *source.Settings
	w   *fakeWriter
}

func newHarness(t *testing.T, w *fakeWriter, mutate func(*Config), format string) *harness {
	t.Helper()
	m := metrics.New("test", "test")
	cfg := Config{
		QueueMaxMessages: 1000,
		QueueMaxBytes:    1 << 20,
		Workers:          2,
		BatchMaxRows:     100,
		BatchMaxBytes:    1 << 20,
		BatchMaxWait:     20 * time.Millisecond,
		Writers:          2,
		InitialBackoff:   time.Millisecond,
		MaxBackoff:       5 * time.Millisecond,
		Normalization:    normalization.Options{MaxFutureSkew: time.Hour, MaxFields: 100, MaxFieldValueBytes: 1024, MaxFieldNameBytes: 64},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	p := New(cfg, w, "fake", m, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.Start()
	sc := config.Source{Name: "test-src", Type: "syslog", Protocol: "tcp", Address: ":0", Format: format,
		Timezone: "UTC", RawMessage: "always", Tenant: "default", HostnameFallback: "none", SDFlatten: "full"}
	src, err := source.New(sc, m)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{p: p, m: m, src: src, w: w}
}

func (h *harness) msg(data string) RawMessage {
	return RawMessage{Data: data, ReceivedAt: time.Now(), Source: h.src}
}

func (h *harness) close(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.p.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPipelineStoresEverything(t *testing.T) {
	w := &fakeWriter{}
	h := newHarness(t, w, nil, "auto")
	const n = 1234
	for i := range n {
		var data string
		if i%2 == 0 {
			data = fmt.Sprintf("<165>1 2026-09-14T10:00:00Z fw01 vpnd - - [m@1 seq=\"%d\"] message %d", i, i)
		} else {
			data = fmt.Sprintf("<38>Sep 14 10:00:00 web-1 sshd[1]: message %d", i)
		}
		if err := h.p.Enqueue(context.Background(), h.msg(data)); err != nil {
			t.Fatal(err)
		}
	}
	h.close(t)

	got := w.snapshot()
	if len(got) != n {
		t.Fatalf("stored %d entries, want %d", len(got), n)
	}
	seen := map[string]bool{}
	for _, e := range got {
		seen[e.Message] = true
		if e.Source != "test-src" || e.Tenant != "default" || e.Raw == "" || e.Time.IsZero() {
			t.Fatalf("entry not normalized: %+v", e)
		}
		switch e.Format {
		case logentry.FormatRFC5424:
			if e.Hostname != "fw01" || len(e.Fields) != 1 {
				t.Fatalf("rfc5424 entry wrong: %+v", e)
			}
		case logentry.FormatRFC3164:
			if e.AppName != "sshd" {
				t.Fatalf("rfc3164 entry wrong: %+v", e)
			}
		default:
			t.Fatalf("unexpected format %s", e.Format)
		}
	}
	if len(seen) != n {
		t.Errorf("duplicate or missing messages: %d unique", len(seen))
	}
	if v := testutil.ToFloat64(h.src.Metrics.Stored); v != n {
		t.Errorf("stored counter = %v, want %d", v, n)
	}
	if v := testutil.ToFloat64(h.src.Metrics.Parsed(logentry.FormatRFC5424)) + testutil.ToFloat64(h.src.Metrics.Parsed(logentry.FormatRFC3164)); v != n {
		t.Errorf("parsed counters = %v, want %d", v, n)
	}
}

func TestBatchFlushOnMaxWait(t *testing.T) {
	w := &fakeWriter{}
	h := newHarness(t, w, func(c *Config) { c.BatchMaxWait = 30 * time.Millisecond }, "auto")
	defer h.close(t)
	if err := h.p.Enqueue(context.Background(), h.msg("<14>Sep 14 10:00:00 h a: lonely")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(w.snapshot()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("single message not flushed by max_wait")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if v := testutil.ToFloat64(h.m.BatchFlushReason.WithLabelValues(metrics.FlushWait)); v < 1 {
		t.Errorf("flush reason wait = %v", v)
	}
}

func TestBatchFlushOnMaxRows(t *testing.T) {
	w := &fakeWriter{}
	h := newHarness(t, w, func(c *Config) {
		c.Workers = 1
		c.BatchMaxRows = 10
		c.BatchMaxWait = time.Hour
	}, "auto")
	for range 25 {
		if err := h.p.Enqueue(context.Background(), h.msg("<14>Sep 14 10:00:00 h a: x")); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(w.snapshot()) < 20 {
		if time.Now().After(deadline) {
			t.Fatalf("got %d rows, want 20 flushed by max_rows", len(w.snapshot()))
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.close(t)
	if n := len(w.snapshot()); n != 25 {
		t.Errorf("after close %d rows, want 25", n)
	}
}

func TestBackpressureBlocksWithoutLoss(t *testing.T) {
	w := &fakeWriter{gate: make(chan struct{})}
	h := newHarness(t, w, func(c *Config) {
		c.QueueMaxMessages = 5
		c.Workers = 1
		c.Writers = 1
		c.BatchQueue = 1
		c.BatchMaxRows = 1
	}, "auto")

	accepted := 0
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		err := h.p.Enqueue(ctx, h.msg("<14>Sep 14 10:00:00 h a: blocked"))
		cancel()
		if errors.Is(err, context.DeadlineExceeded) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		accepted++
		if accepted > 100 {
			t.Fatal("queue never filled: backpressure not applied")
		}
	}
	if !h.p.Saturated() {
		t.Error("Saturated() = false with a full queue")
	}
	close(w.gate) // storage recovers
	h.close(t)
	if got := len(w.snapshot()); got != accepted {
		t.Errorf("stored %d, want all %d accepted messages", got, accepted)
	}
	if v := testutil.ToFloat64(h.src.Metrics.DroppedQueueFull); v != 0 {
		t.Errorf("blocking enqueue dropped %v messages", v)
	}
}

func TestTryEnqueueDropsWhenFull(t *testing.T) {
	w := &fakeWriter{gate: make(chan struct{})}
	h := newHarness(t, w, func(c *Config) {
		c.QueueMaxMessages = 3
		c.Workers = 1
		c.Writers = 1
		c.BatchQueue = 1
		c.BatchMaxRows = 1
	}, "auto")

	accepted, dropped := 0, 0
	for range 50 {
		if h.p.TryEnqueue(h.msg("<14>Sep 14 10:00:00 h a: udp")) {
			accepted++
		} else {
			dropped++
		}
	}
	if dropped == 0 {
		t.Fatal("no drops with a blocked writer and tiny queue")
	}
	if v := testutil.ToFloat64(h.src.Metrics.DroppedQueueFull); int(v) != dropped {
		t.Errorf("dropped counter = %v, want %d", v, dropped)
	}
	close(w.gate)
	h.close(t)
	if got := len(w.snapshot()); got != accepted {
		t.Errorf("stored %d, want %d accepted", got, accepted)
	}
}

func TestByteBudgetBoundsQueue(t *testing.T) {
	w := &fakeWriter{gate: make(chan struct{})}
	h := newHarness(t, w, func(c *Config) {
		c.QueueMaxMessages = 10_000
		c.QueueMaxBytes = 64 << 10
		c.Workers = 1
		c.Writers = 1
		c.BatchQueue = 1
		c.BatchMaxRows = 1
	}, "auto")
	big := "<14>Sep 14 10:00:00 h a: " + strings.Repeat("x", 8<<10)
	accepted := 0
	for range 100 {
		if h.p.TryEnqueue(h.msg(big)) {
			accepted++
		}
		if b := h.p.queueBytes.Load(); b > 64<<10 {
			t.Fatalf("queue bytes %d exceed budget", b)
		}
	}
	if accepted >= 20 {
		t.Errorf("accepted %d 8KiB messages into a 64KiB budget", accepted)
	}
	close(w.gate)
	h.close(t)
}

func TestRetryableErrorsRecover(t *testing.T) {
	w := &fakeWriter{fail: func(call int, _ *logentry.Batch) error {
		if call <= 3 {
			return &storage.WriteError{Class: storage.Retryable, Err: errors.New("503")}
		}
		return nil
	}}
	h := newHarness(t, w, func(c *Config) { c.Workers = 1; c.Writers = 1 }, "auto")
	for range 10 {
		_ = h.p.Enqueue(context.Background(), h.msg("<14>Sep 14 10:00:00 h a: retry me"))
	}
	h.close(t)
	if got := len(w.snapshot()); got != 10 {
		t.Errorf("stored %d, want 10", got)
	}
	if v := testutil.ToFloat64(h.m.StorageWriteRetries.WithLabelValues("fake")); v < 3 {
		t.Errorf("retries = %v, want >= 3", v)
	}
	if !h.p.Healthy() {
		t.Error("pipeline unhealthy after recovery")
	}
}

func TestRejectedRowsAreIsolated(t *testing.T) {
	w := &fakeWriter{fail: func(_ int, b *logentry.Batch) error {
		for _, e := range b.Entries {
			if strings.Contains(e.Message, "poison") {
				return &storage.WriteError{Class: storage.Rejected, StatusCode: 400, Err: errors.New("bad row")}
			}
		}
		return nil
	}}
	h := newHarness(t, w, func(c *Config) {
		c.Workers = 1
		c.BatchMaxRows = 64
		c.BatchMaxWait = time.Hour
	}, "auto")
	for i := range 64 {
		msg := fmt.Sprintf("<14>Sep 14 10:00:00 h a: good %d", i)
		if i == 17 || i == 40 {
			msg = "<14>Sep 14 10:00:00 h a: poison"
		}
		_ = h.p.Enqueue(context.Background(), h.msg(msg))
	}
	h.close(t)
	if got := len(w.snapshot()); got != 62 {
		t.Errorf("stored %d rows, want 62", got)
	}
	if v := testutil.ToFloat64(h.src.Metrics.DroppedRejected); v != 2 {
		t.Errorf("rejected drops = %v, want 2", v)
	}
}

func TestShutdownDeadlineCountsDrops(t *testing.T) {
	w := &fakeWriter{fail: func(int, *logentry.Batch) error {
		return &storage.WriteError{Class: storage.Retryable, Err: errors.New("down")}
	}}
	h := newHarness(t, w, nil, "auto")
	for range 20 {
		_ = h.p.Enqueue(context.Background(), h.msg("<14>Sep 14 10:00:00 h a: never stored"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := h.p.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v, want deadline exceeded", err)
	}
	if v := testutil.ToFloat64(h.src.Metrics.DroppedShutdown); v != 20 {
		t.Errorf("shutdown drops = %v, want 20", v)
	}
	if err := h.p.Enqueue(context.Background(), h.msg("late")); !errors.Is(err, ErrClosed) {
		t.Errorf("Enqueue after close = %v", err)
	}
	if h.p.TryEnqueue(h.msg("late")) {
		t.Error("TryEnqueue accepted after close")
	}
}

func TestCloseUnblocksWaitingProducers(t *testing.T) {
	w := &fakeWriter{gate: make(chan struct{})}
	h := newHarness(t, w, func(c *Config) {
		c.QueueMaxMessages = 1
		c.Workers = 1
		c.Writers = 1
		c.BatchQueue = 1
		c.BatchMaxRows = 1
	}, "auto")
	errc := make(chan error, 1)
	go func() {
		for {
			if err := h.p.Enqueue(context.Background(), h.msg("<14>x")); err != nil {
				errc <- err
				return
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = h.p.Close(ctx) // writer is gated: expect deadline, but producers must be released
	select {
	case err := <-errc:
		if !errors.Is(err, ErrClosed) {
			t.Errorf("blocked producer got %v, want ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("producer still blocked after Close")
	}
}

func TestParseFailureIsStoredAsUnknown(t *testing.T) {
	w := &fakeWriter{}
	h := newHarness(t, w, nil, "rfc5424") // forced format, BSD input
	_ = h.p.Enqueue(context.Background(), h.msg("<14>Sep 14 10:00:00 host app: not 5424"))
	h.close(t)
	got := w.snapshot()
	if len(got) != 1 {
		t.Fatalf("stored %d", len(got))
	}
	e := got[0]
	if e.Format != logentry.FormatUnknown || e.ParseError == "" || e.Message != "<14>Sep 14 10:00:00 host app: not 5424" {
		t.Errorf("parse failure not preserved: format=%s parseError=%q message=%q", e.Format, e.ParseError, e.Message)
	}
	if v := testutil.ToFloat64(h.src.Metrics.ParseErrors(logentry.FormatRFC5424)); v != 1 {
		t.Errorf("parse error counter = %v", v)
	}
}
