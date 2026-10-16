package api

import (
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/metricstore"
)

// Host utilisation over time comes from the metrics store rather than from
// the sampler, which only ever knows "now". VictoriaMetrics scrapes this
// node's /metrics (see deploy/compose/scrape.yml), so the same gauges the
// System page shows live are also a history — which is what answers the
// question people actually have about a disk: not how full it is, but how
// fast it is filling.

// hostHistoryWindows are the ranges a chart may ask for, with the step each
// is drawn at. The step is chosen so every window is a few hundred points:
// enough to see a spike, few enough to send and draw.
var hostHistoryWindows = map[string]struct {
	Span time.Duration
	Step time.Duration
}{
	"1h":  {time.Hour, time.Minute},
	"6h":  {6 * time.Hour, 2 * time.Minute},
	"24h": {24 * time.Hour, 5 * time.Minute},
	"7d":  {7 * 24 * time.Hour, 30 * time.Minute},
	"30d": {30 * 24 * time.Hour, 2 * time.Hour},
}

func hostHistoryWindowNames() []string {
	return []string{"1h", "6h", "24h", "7d", "30d"}
}

// hostLine is one measurement over time.
type hostLine struct {
	Name string `json:"name"`
	// Label is what a legend shows.
	Label string `json:"label"`
	// Unit is "ratio" for a fraction of a limit, or "bytes".
	Unit string `json:"unit"`
	// Points line up with the shared timestamps; a gap is null, because a
	// node that was switched off did not use zero resources, and drawing it
	// as zero invents a recovery that never happened.
	Points []*float64 `json:"points"`
	// Limit is the ceiling the line is measured against, where it has one
	// and it did not change over the window.
	Limit *float64 `json:"limit,omitempty"`
}

// handleHostHistory returns host utilisation over a window.
func (s *Server) handleHostHistory(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	if s.opts.API.MetricsStore == nil {
		return errStatus(http.StatusNotFound, "not_configured",
			"host history needs a metrics store; set analytics.metrics.url")
	}
	name := strings.TrimSpace(r.URL.Query().Get("range"))
	if name == "" {
		name = "24h"
	}
	window, ok := hostHistoryWindows[name]
	if !ok {
		return badRequest("validation_failed", "/range", "range must be one of %s",
			strings.Join(hostHistoryWindowNames(), ", "))
	}

	// The start is aligned to the step so points land on round times and two
	// loads of the same window draw the same chart. The end is now, not the
	// last boundary: truncating it down would drop everything since that
	// boundary, which at the 30-day window's two-hour step means a chart
	// that is silently two hours stale. The final bucket is therefore a
	// partial one, which max_over_time answers correctly — the peak so far.
	end := time.Now().UTC()
	start := end.Add(-window.Span).Truncate(window.Step)

	node := s.opts.NodeID
	// Selected by job, not by a node label: Syslogc's metrics carry no
	// identity of their own, because in Prometheus the scraper owns that —
	// it adds job and instance. This Compose stack scrapes exactly one
	// target, and alignPoints refuses rather than guessing if that ever
	// stops being true.
	//
	// max_over_time, not the raw gauge: at a 30-minute step a 7-day chart
	// would otherwise show whichever sample happened to land on the boundary
	// and hide every spike between. The question is whether it ever got
	// close to the limit, so the peak in each bucket is the honest summary.
	queries := []struct {
		line  hostLine
		query string
	}{
		{hostLine{Name: "cpu", Label: "CPU", Unit: "ratio"},
			fmt.Sprintf(`max_over_time(syslogc_host_cpu_usage_ratio{job="syslogc"}[%s])`, promDuration(window.Step))},
		{hostLine{Name: "memory", Label: "Memory", Unit: "bytes"},
			fmt.Sprintf(`max_over_time(syslogc_host_memory_used_bytes{job="syslogc"}[%s])`, promDuration(window.Step))},
		{hostLine{Name: "memory_limit", Label: "Memory limit", Unit: "bytes"},
			fmt.Sprintf(`max_over_time(syslogc_host_memory_limit_bytes{job="syslogc"}[%s])`, promDuration(window.Step))},
	}
	// One pair of lines per filesystem, so a node watching more than one
	// does not silently chart only the first.
	for _, path := range s.hostDiskPaths() {
		queries = append(queries,
			struct {
				line  hostLine
				query string
			}{
				hostLine{Name: "disk:" + path, Label: "Disk " + path, Unit: "bytes"},
				fmt.Sprintf(`max_over_time(syslogc_host_disk_used_bytes{job="syslogc",path=%q}[%s])`,
					path, promDuration(window.Step)),
			},
			struct {
				line  hostLine
				query string
			}{
				hostLine{Name: "disk_total:" + path, Label: "Disk " + path + " total", Unit: "bytes"},
				fmt.Sprintf(`max_over_time(syslogc_host_disk_total_bytes{job="syslogc",path=%q}[%s])`,
					path, promDuration(window.Step)),
			})
	}

	stamps := timestampsOver(start, end, window.Step)
	lines := make([]hostLine, 0, len(queries))
	for _, q := range queries {
		series, err := s.opts.API.MetricsStore.QueryRange(r.Context(), metricstore.RangeQuery{
			Query: q.query, Start: start, End: end, Step: window.Step,
		})
		if err != nil {
			return fmt.Errorf("reading host history: %w", err)
		}
		if len(series) > 1 {
			// More than one target is answering. Charting the first would be
			// a different node's history under this node's name, which is
			// worse than saying so.
			return errStatus(http.StatusConflict, "ambiguous_metrics",
				"%d targets report %s; host history assumes one. Give each node its own "+
					"scrape job, or query the metrics store directly", len(series), q.line.Name)
		}
		line := q.line
		line.Points = alignPoints(series, stamps, window.Step)
		lines = append(lines, line)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"range":        name,
		"windows":      hostHistoryWindowNames(),
		"step_seconds": int(window.Step.Seconds()),
		"timestamps":   stamps,
		"lines":        lines,
		// What the chart is of, so a second node added later is not silently
		// mixed into this one's history.
		"node": node,
	})
	return nil
}

// hostDiskPaths names the filesystems this node reports, read from the last
// sample rather than guessed, so the chart covers exactly what the tiles do.
func (s *Server) hostDiskPaths() []string {
	if s.opts.API.Host == nil {
		return []string{"/"}
	}
	snap, ok := s.opts.API.Host()
	if !ok || len(snap.Disks) == 0 {
		return []string{"/"}
	}
	out := make([]string, 0, len(snap.Disks))
	for _, d := range snap.Disks {
		out = append(out, d.Path)
	}
	return out
}

// timestampsOver is the grid the lines are drawn on.
func timestampsOver(start, end time.Time, step time.Duration) []time.Time {
	var out []time.Time
	for t := start; !t.After(end); t = t.Add(step) {
		out = append(out, t)
	}
	return out
}

// alignPoints puts a series onto the shared grid, leaving a gap where there
// is no sample.
//
// A missing point is null rather than zero. The node may have been stopped,
// or the scrape may have failed, and either way it did not use no memory —
// charting that as a drop to zero invents an idle period and, worse, an
// apparent recovery.
func alignPoints(series []metricstore.Series, stamps []time.Time, step time.Duration) []*float64 {
	out := make([]*float64, len(stamps))
	if len(series) == 0 {
		return out
	}
	// One query, one series: the selectors above name a single node and path.
	byTime := make(map[int64]float64, len(series[0].Points))
	for _, p := range series[0].Points {
		if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			continue
		}
		byTime[p.At.UTC().Truncate(step).Unix()] = p.Value
	}
	for i, t := range stamps {
		if v, ok := byTime[t.UTC().Truncate(step).Unix()]; ok {
			value := v
			out[i] = &value
		}
	}
	return out
}

// promDuration renders a step the way PromQL writes one.
func promDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}
