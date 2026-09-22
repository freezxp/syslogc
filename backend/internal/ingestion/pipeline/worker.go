package pipeline

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/metrics"
	"github.com/freezxp/syslogc/backend/internal/normalization"
	"github.com/freezxp/syslogc/backend/internal/parser"
)

const maxParseErrorLen = 256

// worker parses messages and builds batches. Each worker owns its batches,
// so the hot path takes no locks.
type worker struct {
	p       *Pipeline
	batches map[string]*batch
	pending int
}

func (p *Pipeline) runWorker() {
	defer p.workersWG.Done()
	w := &worker{p: p, batches: make(map[string]*batch, 1)}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	timerActive := false

	for {
		select {
		case msg, ok := <-p.queue:
			if !ok {
				w.flushAll(metrics.FlushShutdown)
				return
			}
			weight := msg.weight()
			w.process(&msg)
			p.queueBytes.Add(-weight)
			p.budget.Release(weight)
			if w.pending > 0 && !timerActive {
				timer.Reset(p.cfg.BatchMaxWait)
				timerActive = true
			} else if w.pending == 0 && timerActive {
				timer.Stop()
				timerActive = false
			}
		case <-timer.C:
			timerActive = false
			w.flushAll(metrics.FlushWait)
		}
	}
}

func (w *worker) process(msg *RawMessage) {
	p := w.p
	src := msg.Source
	tenant := src.Norm.Tenant
	b := w.batches[tenant]
	if b == nil {
		b = p.getBatch(tenant)
		w.batches[tenant] = b
	}
	e := b.entries.Next()

	format := src.Format
	if src.AutoDetect {
		format = parser.Detect(msg.Data)
	}
	prs, ok := p.registry.Get(format)
	if !ok {
		prs, format = p.fallback, p.fallback.Format()
	}
	in := parser.Input{
		Data:       msg.Data,
		ReceivedAt: msg.ReceivedAt,
		Peer:       msg.Peer,
		Location:   src.Location,
		SDShort:    src.SDShort,
	}
	if err := p.safeParse(prs, &in, e); err != nil {
		e.Reset()
		e.Format = logentry.FormatUnknown
		e.Message = msg.Data
		e.ParseError = truncate(err.Error(), maxParseErrorLen)
		src.Metrics.ParseErrors(format).Inc()
	} else {
		e.Format = format
		src.Metrics.Parsed(format).Inc()
		if e.ParseError != "" {
			src.Metrics.ParseErrors(format).Inc()
		}
	}

	// Extraction runs before normalization so the fields it adds go through
	// the same naming rules, limits and sanitisation as parsed ones.
	if src.Extract != nil {
		if rule := src.Extract.Apply(e); rule != "" {
			src.Metrics.Extracted(rule).Inc()
		} else {
			src.Metrics.ExtractMisses.Inc()
		}
	}

	p.norm.Apply(e, &src.Norm, &normalization.Meta{
		Data:       msg.Data,
		ReceivedAt: msg.ReceivedAt,
		Peer:       msg.Peer,
		Truncated:  msg.Truncated,
	})
	if e.FieldsDropped > 0 {
		p.metrics.NormalizationLimits.WithLabelValues("max_fields").Inc()
	}
	if e.Truncated {
		p.metrics.NormalizationLimits.WithLabelValues("truncated").Inc()
	}

	b.sources = append(b.sources, src)
	b.entries.Bytes += e.EstimateSize()
	w.pending++

	switch {
	case len(b.entries.Entries) >= p.cfg.BatchMaxRows:
		w.flush(tenant, metrics.FlushRows)
	case b.entries.Bytes >= p.cfg.BatchMaxBytes:
		w.flush(tenant, metrics.FlushBytes)
	}
}

// safeParse converts parser panics into errors so one bad message cannot
// crash the process. Panics are bugs and are counted.
func (p *Pipeline) safeParse(prs parser.Parser, in *parser.Input, e *logentry.Entry) (err error) {
	defer func() {
		if r := recover(); r != nil {
			p.metrics.ParsePanics.WithLabelValues(prs.Format().String()).Inc()
			p.log.Error("parser panic recovered", "format", prs.Format().String(), "panic", fmt.Sprint(r), "bytes", len(in.Data))
			err = fmt.Errorf("%s: internal parser error", prs.Format())
		}
	}()
	return prs.Parse(in, e)
}

func (w *worker) flush(tenant, reason string) {
	b := w.batches[tenant]
	if b == nil || len(b.entries.Entries) == 0 {
		return
	}
	delete(w.batches, tenant)
	w.pending -= len(b.entries.Entries)
	m := w.p.metrics
	m.BatchFlushReason.WithLabelValues(reason).Inc()
	m.BatchRows.Observe(float64(len(b.entries.Entries)))
	m.BatchBytes.Observe(float64(b.entries.Bytes))
	// Blocks while writers are busy: this is how storage backpressure
	// propagates to the ingest queue.
	w.p.batches <- b
}

func (w *worker) flushAll(reason string) {
	for tenant := range w.batches {
		w.flush(tenant, reason)
	}
	w.pending = 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func newGaugeFunc(name, help string, f func() float64) prometheus.GaugeFunc {
	return prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "syslogc", Name: name, Help: help}, f)
}
