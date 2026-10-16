package api

import (
	"math"
	"testing"
	"time"

	"github.com/freezxp/syslogc/backend/internal/metricstore"
)

func at(min int) time.Time {
	return time.Date(2026, 10, 16, 4, min, 0, 0, time.UTC)
}

// A missing sample must stay missing. The node may have been stopped or the
// scrape may have failed, and either way it did not use no memory: charting
// the gap as zero invents an idle period and an apparent recovery.
func TestAGapInTheHistoryIsNotAZero(t *testing.T) {
	stamps := []time.Time{at(0), at(1), at(2), at(3), at(4)}
	series := []metricstore.Series{{Points: []metricstore.Point{
		{At: at(0), Value: 0.4},
		// nothing at 04:01 or 04:02 — the node was down
		{At: at(3), Value: 0.6},
		{At: at(4), Value: 0.5},
	}}}

	got := alignPoints(series, stamps, time.Minute)
	if len(got) != len(stamps) {
		t.Fatalf("got %d points for %d timestamps", len(got), len(stamps))
	}
	for i, want := range []*float64{ptr(0.4), nil, nil, ptr(0.6), ptr(0.5)} {
		switch {
		case want == nil && got[i] != nil:
			t.Errorf("point %d is %v, want a gap", i, *got[i])
		case want != nil && got[i] == nil:
			t.Errorf("point %d is a gap, want %v", i, *want)
		case want != nil && got[i] != nil && *got[i] != *want:
			t.Errorf("point %d = %v, want %v", i, *got[i], *want)
		}
	}
}

// VictoriaMetrics answers an absent value with NaN. Carried through it would
// be rendered as a number, so it has to become a gap like any other.
func TestNaNAndInfinityBecomeGaps(t *testing.T) {
	stamps := []time.Time{at(0), at(1), at(2)}
	series := []metricstore.Series{{Points: []metricstore.Point{
		{At: at(0), Value: math.NaN()},
		{At: at(1), Value: math.Inf(1)},
		{At: at(2), Value: 1.5},
	}}}
	got := alignPoints(series, stamps, time.Minute)
	if got[0] != nil || got[1] != nil {
		t.Errorf("NaN or infinity was charted as a number: %v %v", got[0], got[1])
	}
	if got[2] == nil || *got[2] != 1.5 {
		t.Errorf("a real value was lost: %v", got[2])
	}
}

// A history with no samples at all must be a row of gaps, not an empty line
// that a chart would silently draw as nothing having happened.
func TestNoSamplesLeavesEveryPointAGap(t *testing.T) {
	stamps := []time.Time{at(0), at(1)}
	got := alignPoints(nil, stamps, time.Minute)
	if len(got) != 2 || got[0] != nil || got[1] != nil {
		t.Errorf("got %v, want a gap at every timestamp", got)
	}
}

// Samples do not arrive exactly on the step boundary — a scrape runs a second
// or two late — so they are matched to the bucket they fall in.
func TestASampleIsMatchedToItsBucketNotItsExactTime(t *testing.T) {
	stamps := []time.Time{at(0), at(1)}
	series := []metricstore.Series{{Points: []metricstore.Point{
		{At: at(0).Add(14 * time.Second), Value: 7},
		{At: at(1).Add(59 * time.Second), Value: 8},
	}}}
	got := alignPoints(series, stamps, time.Minute)
	if got[0] == nil || *got[0] != 7 || got[1] == nil || *got[1] != 8 {
		t.Errorf("got %v, %v; want both samples placed in their buckets", got[0], got[1])
	}
}

func TestTheGridCoversTheWholeWindow(t *testing.T) {
	start, end := at(0), at(10)
	got := timestampsOver(start, end, 2*time.Minute)
	if len(got) != 6 {
		t.Fatalf("got %d timestamps over 10 minutes at 2m, want 6", len(got))
	}
	if !got[0].Equal(start) || !got[len(got)-1].Equal(end) {
		t.Errorf("grid runs %v..%v, want %v..%v", got[0], got[len(got)-1], start, end)
	}
}

// The step is written into the PromQL, so it has to be a duration PromQL
// accepts. "0.5m" is not one.
func TestTheStepIsWrittenAsPromQLWritesDurations(t *testing.T) {
	for _, c := range []struct {
		in   time.Duration
		want string
	}{
		{time.Minute, "1m"},
		{2 * time.Minute, "2m"},
		{30 * time.Minute, "30m"},
		{time.Hour, "1h"},
		{2 * time.Hour, "2h"},
		{90 * time.Second, "90s"},
	} {
		if got := promDuration(c.in); got != c.want {
			t.Errorf("promDuration(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Every window a chart may ask for has to have a step, and the step has to
// divide the span into a sane number of points: too few and a spike is
// invisible, too many and the response is enormous.
func TestEveryWindowDrawsABetweenFiftyAndFiveHundredPoints(t *testing.T) {
	for _, name := range hostHistoryWindowNames() {
		w, ok := hostHistoryWindows[name]
		if !ok {
			t.Errorf("%s is offered but has no step", name)
			continue
		}
		points := int(w.Span / w.Step)
		if points < 50 || points > 500 {
			t.Errorf("%s draws %d points (span %s, step %s); want 50..500", name, points, w.Span, w.Step)
		}
	}
	// And the list offered matches the windows that exist, so nothing is
	// offered that cannot be asked for.
	if len(hostHistoryWindowNames()) != len(hostHistoryWindows) {
		t.Errorf("%d windows offered, %d defined", len(hostHistoryWindowNames()), len(hostHistoryWindows))
	}
}

func ptr(v float64) *float64 { return &v }
