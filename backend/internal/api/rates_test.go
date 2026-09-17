package api

import (
	"testing"
	"time"

	"github.com/freezxp/syslogc/backend/internal/metrics"
)

func TestRateTracker(t *testing.T) {
	snap := func(received, stored int64) metrics.Totals {
		return metrics.Totals{Sources: map[string]*metrics.SourceTotals{"udp": {Name: "udp", Received: received, Stored: stored}}}
	}
	var rt rateTracker
	t0 := time.Unix(1_000_000, 0)
	if _, _, ok := rt.rates(t0, snap(0, 0)); ok {
		t.Error("rates without history")
	}
	rt.add(t0, snap(0, 0))
	if _, _, ok := rt.rates(t0.Add(2*time.Second), snap(10, 10)); ok {
		t.Error("rates over a window shorter than the minimum")
	}
	for i := 1; i <= 20; i++ {
		rt.add(t0.Add(time.Duration(i)*5*time.Second), snap(int64(i*500), int64(i*400)))
	}
	if len(rt.samples) > 14 {
		t.Errorf("history not pruned: %d samples", len(rt.samples))
	}
	now := t0.Add(103 * time.Second)
	// Base is the newest sample at least 5s old: t0+95s (9500 received, 7600 stored), 8s ago.
	received, stored, ok := rt.rates(now, snap(10_300, 8_240))
	if !ok || received["udp"] < 99 || received["udp"] > 101 || stored["udp"] != 80 {
		t.Errorf("rates = %v %v %v", received, stored, ok)
	}
	// Counter reset (restart) yields no negative rate.
	if received, _, _ := rt.rates(now, snap(1, 1)); received["udp"] != 0 {
		t.Errorf("rate after reset = %v", received["udp"])
	}
}
