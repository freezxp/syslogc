package pipeline

import (
	"time"

	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/storage"
)

func (p *Pipeline) runWriter() {
	defer p.writersWG.Done()
	for b := range p.batches {
		p.writeRange(b, 0, len(b.entries.Entries))
		p.putBatch(b)
	}
}

// writeRange writes entries[lo:hi] of b, retrying transient failures with
// exponential backoff and bisecting rejected batches to isolate bad rows.
// It returns only when the rows are stored or accounted as dropped.
func (p *Pipeline) writeRange(b *batch, lo, hi int) {
	sub := &logentry.Batch{Tenant: b.entries.Tenant, Entries: b.entries.Entries[lo:hi]}
	backoff := p.cfg.InitialBackoff
	for {
		if p.writeCtx.Err() != nil {
			p.countDropped(b, lo, hi, dropShutdown)
			return
		}
		start := time.Now()
		err := p.writer.WriteBatch(p.writeCtx, sub)
		p.metrics.StorageWriteDuration.WithLabelValues(p.backend).Observe(time.Since(start).Seconds())
		if err == nil {
			p.recordStored(b, lo, hi)
			return
		}
		if p.writeCtx.Err() != nil {
			p.countDropped(b, lo, hi, dropShutdown)
			return
		}

		class := storage.ClassOf(err)
		p.metrics.StorageWriteErrors.WithLabelValues(p.backend, class.String()).Inc()
		switch class {
		case storage.Rejected:
			if hi-lo > 1 {
				mid := lo + (hi-lo)/2
				p.writeRange(b, lo, mid)
				p.writeRange(b, mid, hi)
				return
			}
			e := &b.entries.Entries[lo]
			p.log.Warn("storage rejected log entry; dropping",
				"source", e.Source, "format", e.Format.String(), "bytes", len(e.Message), "error", err)
			p.countDropped(b, lo, hi, dropRejected)
			return
		case storage.Fatal:
			p.setHealthy(false)
			p.logThrottled("storage write failed (configuration problem?); retrying", "error", err, "rows", hi-lo)
		default:
			p.setHealthy(false)
			p.logThrottled("storage write failed; retrying", "error", err, "rows", hi-lo, "backoff", backoff)
		}

		p.metrics.StorageWriteRetries.WithLabelValues(p.backend).Inc()
		timer := time.NewTimer(jitter(backoff))
		select {
		case <-timer.C:
		case <-p.writeCtx.Done():
			timer.Stop()
		}
		backoff = min(backoff*2, p.cfg.MaxBackoff)
	}
}

type dropReason uint8

const (
	dropShutdown dropReason = iota
	dropRejected
)

func (p *Pipeline) countDropped(b *batch, lo, hi int, reason dropReason) {
	for _, src := range b.sources[lo:hi] {
		switch reason {
		case dropShutdown:
			src.Metrics.DroppedShutdown.Inc()
		case dropRejected:
			src.Metrics.DroppedRejected.Inc()
		}
	}
}

func (p *Pipeline) recordStored(b *batch, lo, hi int) {
	p.setHealthy(true)
	oldest := b.entries.Entries[lo].ReceivedAt
	bytes := 0
	for i := lo; i < hi; i++ {
		b.sources[i].Metrics.Stored.Inc()
		if r := b.entries.Entries[i].ReceivedAt; r.Before(oldest) {
			oldest = r
		}
	}
	if lo == 0 && hi == len(b.entries.Entries) {
		bytes = b.entries.Bytes
	} else {
		for i := lo; i < hi; i++ {
			bytes += b.entries.Entries[i].EstimateSize()
		}
	}
	p.metrics.BytesStored.WithLabelValues("estimated").Add(float64(bytes))
	p.metrics.E2ELatency.Observe(time.Since(oldest).Seconds())
}

func (p *Pipeline) setHealthy(ok bool) {
	if p.healthy.Swap(ok) != ok {
		v := 0.0
		if ok {
			v = 1
			p.log.Info("storage writes recovered", "backend", p.backend)
		}
		p.metrics.StorageHealthy.WithLabelValues(p.backend).Set(v)
	}
}
