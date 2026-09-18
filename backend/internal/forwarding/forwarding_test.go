package forwarding

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/metrics"
	"github.com/freezxp/syslogc/backend/internal/storage"
)

// fakeWriter records batches and can fail on demand.
type fakeWriter struct {
	mu      sync.Mutex
	batches [][]logentry.Entry
	err     error
	calls   int
	block   chan struct{}
}

func (w *fakeWriter) WriteBatch(ctx context.Context, b *logentry.Batch) error {
	w.mu.Lock()
	block, err := w.block, w.err
	w.calls++
	w.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.batches = append(w.batches, append([]logentry.Entry(nil), b.Entries...))
	return nil
}

func (w *fakeWriter) rows() []logentry.Entry {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []logentry.Entry
	for _, b := range w.batches {
		out = append(out, b...)
	}
	return out
}

func (w *fakeWriter) setErr(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.err = err
}

func (w *fakeWriter) callCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}

func newForwarder(t *testing.T, w storage.LogWriter, opts Options) *Forwarder {
	t.Helper()
	opts.Writer = w
	opts.Metrics = metrics.New("test", "test")
	opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	if opts.Name == "" {
		opts.Name = "target"
	}
	if opts.BatchMaxWait == 0 {
		opts.BatchMaxWait = 20 * time.Millisecond
	}
	f := New(opts)
	f.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		f.Stop(ctx)
	})
	return f
}

func entries(n int, source string, sev logentry.Severity) []logentry.Entry {
	out := make([]logentry.Entry, n)
	for i := range out {
		out[i] = logentry.Entry{Message: "m", Source: source, Severity: sev, ReceivedAt: time.Now()}
		out[i].AddField("k", "v")
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestForwardsCopies(t *testing.T) {
	w := &fakeWriter{}
	f := newForwarder(t, w, Options{})

	// The pipeline reuses its entries, so the forwarder must copy them: the
	// slice handed over is mutated as soon as Forward returns.
	batch := entries(3, "syslog-udp", logentry.SeverityInfo)
	f.Forward("default", batch)
	for i := range batch {
		batch[i].Message = "overwritten"
		batch[i].Fields[0].Value = "overwritten"
	}

	waitFor(t, "3 rows", func() bool { return len(w.rows()) == 3 })
	for _, e := range w.rows() {
		if e.Message != "m" || e.Fields[0].Value != "v" {
			t.Fatalf("entry was not copied before the batch was reused: %+v", e)
		}
		if e.Tenant != "default" {
			t.Errorf("tenant = %q", e.Tenant)
		}
	}
}

func TestFiltersBySourceAndSeverity(t *testing.T) {
	w := &fakeWriter{}
	warning := logentry.SeverityWarning
	f := newForwarder(t, w, Options{Sources: []string{"wanted"}, MinSeverity: &warning})

	f.Forward("default", entries(2, "other", logentry.SeverityError))    // wrong source
	f.Forward("default", entries(2, "wanted", logentry.SeverityInfo))    // too mild
	f.Forward("default", entries(3, "wanted", logentry.SeverityError))   // kept
	f.Forward("default", entries(1, "wanted", logentry.SeverityWarning)) // kept: the threshold itself

	waitFor(t, "4 forwarded rows", func() bool { return len(w.rows()) == 4 })
	time.Sleep(50 * time.Millisecond)
	if got := len(w.rows()); got != 4 {
		t.Errorf("forwarded %d rows, want 4", got)
	}
}

func TestDropsWhenQueueFullWithoutBlocking(t *testing.T) {
	w := &fakeWriter{block: make(chan struct{})}
	f := newForwarder(t, w, Options{QueueMaxMessages: 20, BatchMaxRows: 5, BatchMaxWait: time.Millisecond})

	// The writer is stuck, so the queue fills. Forward must still return
	// promptly: local ingestion never waits for a remote.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 50 {
			f.Forward("default", entries(5, "s", logentry.SeverityInfo))
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Forward blocked while the remote was stuck")
	}
	if f.queued.Load() > 25 {
		t.Errorf("queued %d messages, want the queue bounded near 20", f.queued.Load())
	}
	close(w.block)
}

func TestRetriesUntilTheRemoteRecovers(t *testing.T) {
	w := &fakeWriter{}
	w.setErr(errors.New("connection refused"))
	f := newForwarder(t, w, Options{InitialBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond})

	f.Forward("default", entries(2, "s", logentry.SeverityInfo))
	waitFor(t, "a retry", func() bool { return w.callCount() > 1 })
	if f.Status().Healthy {
		t.Error("target reported healthy while writes were failing")
	}

	w.setErr(nil)
	waitFor(t, "delivery after recovery", func() bool { return len(w.rows()) == 2 })
	waitFor(t, "healthy status", func() bool { return f.Status().Healthy })
	if f.Status().LastSuccessAt.IsZero() {
		t.Error("last success time not recorded")
	}
}

func TestDropsRejectedBatches(t *testing.T) {
	w := &fakeWriter{}
	w.setErr(&storage.WriteError{Class: storage.Rejected, StatusCode: 400, Err: errors.New("bad row")})
	f := newForwarder(t, w, Options{InitialBackoff: time.Millisecond})

	f.Forward("default", entries(2, "s", logentry.SeverityInfo))
	waitFor(t, "the rejected batch to be abandoned", func() bool { return w.callCount() == 1 })
	time.Sleep(50 * time.Millisecond)
	if got := w.callCount(); got != 1 {
		t.Errorf("writer called %d times; a rejected batch must not be retried forever", got)
	}
	if q := f.queued.Load(); q != 0 {
		t.Errorf("queue still holds %d messages after a drop", q)
	}
}

func TestStopDrainsPending(t *testing.T) {
	w := &fakeWriter{}
	f := New(Options{Name: "t", Writer: w, BatchMaxWait: time.Hour, Metrics: metrics.New("t", "t"),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	f.Start()
	f.Forward("default", entries(4, "s", logentry.SeverityInfo))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	f.Stop(ctx)
	if got := len(w.rows()); got != 4 {
		t.Errorf("Stop delivered %d of 4 pending rows", got)
	}
	// Forwarding after Stop is a no-op rather than a panic on a closed channel.
	f.Forward("default", entries(1, "s", logentry.SeverityInfo))
}

func TestStatusExplainsAnUnhealthyTarget(t *testing.T) {
	w := &fakeWriter{}
	w.setErr(errors.New("dial tcp 10.0.0.9:9428: connection refused"))
	f := newForwarder(t, w, Options{InitialBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond})

	f.Forward("default", entries(2, "s", logentry.SeverityInfo))
	waitFor(t, "the failure to be reported", func() bool { return !f.Status().Healthy })
	if st := f.Status(); !strings.Contains(st.LastError, "connection refused") {
		t.Errorf("last error = %q, want the write failure", st.LastError)
	}

	w.setErr(nil)
	waitFor(t, "recovery", func() bool { return f.Status().Healthy })
	st := f.Status()
	if st.LastError != "" {
		t.Errorf("last error still set after recovery: %q", st.LastError)
	}
	if st.SentMessages != 2 {
		t.Errorf("sent = %d, want 2", st.SentMessages)
	}
}

func TestStatusCountsDrops(t *testing.T) {
	w := &fakeWriter{}
	w.setErr(&storage.WriteError{Class: storage.Rejected, StatusCode: 400, Err: errors.New("bad row")})
	f := newForwarder(t, w, Options{InitialBackoff: time.Millisecond})
	f.Forward("default", entries(3, "s", logentry.SeverityInfo))
	waitFor(t, "the drop to be counted", func() bool { return f.Status().DroppedMessages == 3 })
}
