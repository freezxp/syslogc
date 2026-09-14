// Package query implements the backend-neutral query service behind the log,
// field and dashboard APIs: time resolution, guards, pagination, response
// shaping and caching.
package query

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/freezxp/syslogc/backend/internal/storage"
)

// TimeRange is an API time range with relative or absolute bounds.
type TimeRange struct {
	From string `json:"from"`
	To   string `json:"to"`
	TZ   string `json:"tz,omitempty"`
}

// ResolvedRange is the absolute range echoed in responses.
type ResolvedRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Resolve converts tr to an absolute half-open range at time now.
func Resolve(tr TimeRange, now time.Time) (storage.TimeRange, *time.Location, error) {
	loc := time.UTC
	if tr.TZ != "" {
		l, err := time.LoadLocation(tr.TZ)
		if err != nil {
			return storage.TimeRange{}, nil, fmt.Errorf("invalid timezone %q", tr.TZ)
		}
		loc = l
	}
	from, err := parseTime(tr.From, now, loc)
	if err != nil {
		return storage.TimeRange{}, nil, fmt.Errorf("invalid from: %w", err)
	}
	to, err := parseTime(tr.To, now, loc)
	if err != nil {
		return storage.TimeRange{}, nil, fmt.Errorf("invalid to: %w", err)
	}
	r := storage.TimeRange{Start: from.UTC(), End: to.UTC()}
	if !r.Start.Before(r.End) {
		return storage.TimeRange{}, nil, fmt.Errorf("from must be before to")
	}
	return r, loc, nil
}

// parseTime parses RFC 3339 or "now[(+|-)<n><unit>][/<unit>]" with units
// s, m, h, d, w. "/unit" rounds down in loc.
func parseTime(s string, now time.Time, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("value is required")
	}
	if !strings.HasPrefix(s, "now") {
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return time.Time{}, fmt.Errorf("want RFC 3339 or a relative expression like now-1h, got %q", s)
		}
		return t, nil
	}
	expr := s[3:]
	round := ""
	if i := strings.IndexByte(expr, '/'); i >= 0 {
		expr, round = expr[:i], expr[i+1:]
	}
	t := now.In(loc)
	if expr != "" {
		sign := expr[0]
		if sign != '+' && sign != '-' {
			return time.Time{}, fmt.Errorf("invalid relative expression %q", s)
		}
		d, err := parseRelDuration(expr[1:])
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid relative expression %q: %w", s, err)
		}
		if sign == '-' {
			d = -d
		}
		t = t.Add(d)
	}
	switch round {
	case "":
	case "m":
		t = t.Truncate(time.Minute)
	case "h":
		t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, loc)
	case "d":
		t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	case "w":
		t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		t = t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7)) // Monday
	default:
		return time.Time{}, fmt.Errorf("invalid rounding unit %q", round)
	}
	return t, nil
}

func parseRelDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("missing amount or unit")
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n < 0 || n > 100_000 {
		return 0, fmt.Errorf("invalid amount")
	}
	unit := map[byte]time.Duration{'s': time.Second, 'm': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}[s[len(s)-1]]
	if unit == 0 {
		return 0, fmt.Errorf("unknown unit %q", s[len(s)-1:])
	}
	return time.Duration(n) * unit, nil
}

// stepLadder are the human-aligned histogram bucket sizes.
var stepLadder = []time.Duration{
	time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second,
	time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour,
}

// chooseStep returns the smallest ladder step producing at most maxBuckets buckets.
func chooseStep(r storage.TimeRange, maxBuckets int) time.Duration {
	span := r.End.Sub(r.Start)
	for _, s := range stepLadder {
		if int64(span/s) < int64(maxBuckets) {
			return s
		}
	}
	return stepLadder[len(stepLadder)-1]
}

// formatStep renders a step like "15m", "1h", "1d".
func formatStep(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d"
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	}
	return strconv.FormatInt(int64(d/time.Second), 10) + "s"
}
