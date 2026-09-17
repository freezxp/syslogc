package api

import (
	"sync"
	"time"

	"github.com/freezxp/syslogc/backend/internal/metrics"
)

const (
	rateSampleInterval = 5 * time.Second
	rateMinWindow      = 5 * time.Second
	rateHistory        = time.Minute
)

type rateSample struct {
	t        time.Time
	received map[string]int64
	stored   map[string]int64
}

// rateTracker keeps recent counter samples so per-source rates can be
// reported by /api/v1/system/ingestion.
type rateTracker struct {
	mu      sync.Mutex
	samples []rateSample
}

func sampleOf(t time.Time, snap metrics.Totals) rateSample {
	s := rateSample{t: t, received: map[string]int64{}, stored: map[string]int64{}}
	for name, st := range snap.Sources {
		s.received[name], s.stored[name] = st.Received, st.Stored
	}
	return s
}

func (rt *rateTracker) add(t time.Time, snap metrics.Totals) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.samples = append(rt.samples, sampleOf(t, snap))
	cut := 0
	for cut < len(rt.samples)-1 && t.Sub(rt.samples[cut].t) > rateHistory {
		cut++
	}
	rt.samples = rt.samples[cut:]
}

// rates returns per-second rates between the newest sample at least
// rateMinWindow old and the current counters. ok is false without history.
func (rt *rateTracker) rates(now time.Time, snap metrics.Totals) (received, stored map[string]float64, ok bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var base *rateSample
	for i := len(rt.samples) - 1; i >= 0; i-- {
		if now.Sub(rt.samples[i].t) >= rateMinWindow {
			base = &rt.samples[i]
			break
		}
	}
	if base == nil {
		return nil, nil, false
	}
	secs := now.Sub(base.t).Seconds()
	cur := sampleOf(now, snap)
	received, stored = map[string]float64{}, map[string]float64{}
	for name, v := range cur.received {
		if d := v - base.received[name]; d > 0 {
			received[name] = float64(d) / secs
		}
		if d := cur.stored[name] - base.stored[name]; d > 0 {
			stored[name] = float64(d) / secs
		}
	}
	return received, stored, true
}

// sampleRates records counter samples until Shutdown.
func (s *Server) sampleRates() {
	if s.opts.Metrics == nil {
		return
	}
	t := time.NewTicker(rateSampleInterval)
	defer t.Stop()
	for {
		if snap, err := s.opts.Metrics.Snapshot(); err == nil {
			s.rates.add(time.Now(), snap)
		}
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
	}
}
