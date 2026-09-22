package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/metricstore"
	"github.com/freezxp/syslogc/backend/internal/query"
	"github.com/freezxp/syslogc/backend/internal/servicetrends"
)

// serviceTrendWindows are the resolutions the rollup records. A chart may
// only ask for one of these: a window that was never recorded has no data,
// and distinct counts cannot be re-bucketed after the fact.
var serviceTrendWindows = map[string]time.Duration{
	"5m": 5 * time.Minute,
	"1h": time.Hour,
	"1d": 24 * time.Hour,
}

// maxTrendPoints bounds how many points one response carries.
const maxTrendPoints = 2000

// handleServiceCatalog returns the service catalog used by the trend rollup.
func (s *Server) handleServiceCatalog(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	if s.opts.API.Store == nil {
		return errStatus(http.StatusNotFound, "not_configured", "service trends require a metadata database")
	}
	catalog, err := servicetrends.LoadCatalog(r.Context(), s.opts.API.Store)
	// An unreadable stored catalog is reported with the catalog itself, so
	// the editor can show what is wrong instead of an empty page.
	problem := ""
	if err != nil {
		problem = err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"services":  catalog.Services,
		"windows":   trendWindowNames(),
		"recording": s.opts.API.ServiceTrends != nil,
		"problem":   problem,
	})
	return nil
}

// handleSetServiceCatalog replaces the catalog. The change applies from the
// next rollup; windows already recorded keep the counts they were recorded
// with until they are recorded again.
func (s *Server) handleSetServiceCatalog(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if s.opts.API.Store == nil {
		return errStatus(http.StatusNotFound, "not_configured", "service trends require a metadata database")
	}
	var req struct {
		Services []servicetrends.Service `json:"services"`
	}
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	catalog := servicetrends.Catalog{Services: req.Services}
	if err := catalog.Validate(); err != nil {
		return badRequest("validation_failed", "/services", "%v", err)
	}
	if err := servicetrends.SaveCatalog(r.Context(), s.opts.API.Store, catalog, &p.UserID); err != nil {
		return err
	}
	s.audit(r, p, "analytics.catalog.update", "success", map[string]any{"services": len(catalog.Services)})
	writeJSON(w, http.StatusOK, map[string]any{
		"services": catalog.Services,
		"windows":  trendWindowNames(),
		"applies":  "from the next rollup",
	})
	return nil
}

// serviceTrendRequest asks for recorded series.
type serviceTrendRequest struct {
	TimeRange query.TimeRange `json:"time_range"`
	// Window is the recorded resolution to read: 5m, 1h or 1d.
	Window string `json:"window"`
	// Services restricts the answer to these services; empty returns all.
	Services []string `json:"services,omitempty"`
	// Metric is unique_clients (the default) or queries.
	Metric string `json:"metric,omitempty"`
}

// handleServiceTrends reads recorded trend series out of the metrics store.
func (s *Server) handleServiceTrends(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	reader := s.opts.API.ServiceTrends
	if reader == nil {
		return errStatus(http.StatusNotFound, "not_configured",
			"service trends require a metrics store; set analytics.metrics.url")
	}
	var req serviceTrendRequest
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	step, ok := serviceTrendWindows[req.Window]
	if !ok {
		return badRequest("validation_failed", "/window", "window must be one of %s",
			strings.Join(trendWindowNames(), ", "))
	}
	metric := servicetrends.MetricUniqueClients
	switch req.Metric {
	case "", "unique_clients":
	case "queries":
		metric = servicetrends.MetricQueries
	default:
		return badRequest("validation_failed", "/metric", "metric must be unique_clients or queries")
	}
	rng, _, err := query.Resolve(req.TimeRange, time.Now())
	if err != nil {
		return badRequest("validation_failed", "/time_range", "%v", err)
	}
	if n := rng.End.Sub(rng.Start) / step; n > maxTrendPoints {
		return badRequest("validation_failed", "/time_range",
			"that range holds %d %s windows, more than the %d a chart can show; use a coarser window or a shorter range",
			n, req.Window, maxTrendPoints)
	}

	promQL, err := trendQuery(metric, req.Window, req.Services)
	if err != nil {
		return err
	}
	series, err := reader.QueryRange(r.Context(), metricstore.RangeQuery{
		Query: promQL, Start: rng.Start, End: rng.End, Step: step,
	})
	if err != nil {
		return fmt.Errorf("reading the metrics store: %w", err)
	}

	catalog, _ := servicetrends.LoadCatalog(r.Context(), s.opts.API.Store)
	labels := make(map[string]string, len(catalog.Services))
	for _, svc := range catalog.Services {
		labels[svc.Name] = svc.Label
	}

	type point struct {
		At    time.Time `json:"at"`
		Value float64   `json:"value"`
	}
	type trendSeries struct {
		Service string    `json:"service"`
		Label   string    `json:"label,omitempty"`
		Points  []point   `json:"points"`
		Peak    float64   `json:"peak"`
		PeakAt  time.Time `json:"peak_at,omitzero"`
	}
	out := make([]trendSeries, 0, len(series))
	var overall trendSeries
	for _, s := range series {
		name := s.Labels["service"]
		ts := trendSeries{Service: name, Label: labels[name], Points: make([]point, 0, len(s.Points))}
		for _, pt := range s.Points {
			ts.Points = append(ts.Points, point{At: pt.At, Value: pt.Value})
			if pt.Value > ts.Peak {
				ts.Peak, ts.PeakAt = pt.Value, pt.At
			}
		}
		if ts.Peak > overall.Peak {
			overall = ts
		}
		out = append(out, ts)
	}
	// Busiest service first, so the chart legend reads in the order that
	// matters; ties keep a stable order by name.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Peak != out[j].Peak {
			return out[i].Peak > out[j].Peak
		}
		return out[i].Service < out[j].Service
	})

	resp := map[string]any{
		"resolved_range": query.ResolvedRange{Start: rng.Start, End: rng.End},
		"window":         req.Window,
		"step_seconds":   int(step.Seconds()),
		"metric":         req.Metric,
		"series":         out,
	}
	if overall.Peak > 0 {
		resp["peak"] = map[string]any{
			"service": overall.Service,
			"label":   overall.Label,
			"value":   overall.Peak,
			"at":      overall.PeakAt,
		}
	} else if hint := s.emptyTrendHint(r, p); hint != "" {
		// An empty chart is almost always a setup problem rather than quiet
		// traffic, and the usual cause is that nothing extracts the fields
		// the rollup counts. Say so instead of drawing nothing.
		resp["hint"] = hint
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

// trendQuery builds the PromQL for a recorded metric. Every part of it comes
// from this package or from a validated service name, never from free text.
func trendQuery(metric, window string, services []string) (string, error) {
	selector := fmt.Sprintf("%s{window=%q}", metric, window)
	if len(services) == 0 {
		return selector, nil
	}
	if len(services) > servicetrends.MaxServices {
		return "", badRequest("validation_failed", "/services", "at most %d services can be asked for at once",
			servicetrends.MaxServices)
	}
	names := make([]string, 0, len(services))
	for _, name := range services {
		if err := servicetrends.ValidName(name); err != nil {
			return "", badRequest("validation_failed", "/services", "service %q: %v", name, err)
		}
		names = append(names, name)
	}
	return fmt.Sprintf("%s{window=%q,service=~%q}", metric, window, strings.Join(names, "|")), nil
}

func trendWindowNames() []string {
	names := make([]string, 0, len(serviceTrendWindows))
	for name := range serviceTrendWindows {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return serviceTrendWindows[names[i]] < serviceTrendWindows[names[j]] })
	return names
}

// emptyTrendHint explains an empty result. It runs only when there is
// nothing to chart, so the extra lookup costs nothing in the normal case.
func (s *Server) emptyTrendHint(r *http.Request, p *auth.Principal) string {
	cfg := s.opts.API.Config.Analytics.ServiceTrends
	domain := cfg.DomainField
	if domain == "" {
		domain = "dns.qname"
	}
	if s.opts.API.Query == nil {
		return ""
	}
	// Look at recent logs rather than the charted range: the question is
	// whether anything produces the field at all.
	fields, err := s.opts.API.Query.Fields(r.Context(), p,
		query.Selection{TimeRange: query.TimeRange{From: "now-1h", To: "now"}})
	if err != nil {
		return ""
	}
	has := false
	for _, f := range fields.Fields {
		if f.Name == domain {
			has = true
			break
		}
	}
	if !has {
		return fmt.Sprintf("No %s field was found in recent logs. DNS trends count fields pulled out of the "+
			"message by an extract rule — add the dnsdist preset to the source receiving your DNS logs "+
			"(Sources → the source → Extract rules), then the next rollup will have something to count.", domain)
	}
	return "Nothing has been recorded for this window yet. The rollup records a window once it has fully " +
		"elapsed, so a new deployment fills in from the oldest logs it was asked to backfill."
}
