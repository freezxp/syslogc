package api

import (
	"net/http"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/iis"
	"github.com/freezxp/syslogc/backend/internal/query"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

// iisRequest asks about a window of web traffic.
type iisRequest struct {
	TimeRange query.TimeRange `json:"time_range"`
	Filter    *filter.Expr    `json:"filter,omitempty"`
	// Buckets is how many points the activity chart should hold.
	Buckets int `json:"buckets,omitempty"`
	// Limit caps the lists.
	Limit int `json:"limit,omitempty"`
	// SlowMillis is the threshold above which a request counts as slow.
	// Zero takes the default. It is a request parameter because what counts
	// as slow is a property of the site, not of the log.
	SlowMillis int `json:"slow_millis,omitempty"`
}

// handleIIS answers every IIS question in one call.
//
// They are asked together because they are read together — a rise in 5xx
// means one thing when the successes fell by the same amount and another
// when they did not — and one request keeps the page consistent rather than
// stitched from answers taken moments apart.
func (s *Server) handleIIS(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req iisRequest
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	sel, step, err := s.opts.API.Query.AnalysisSelection(p, query.Selection{
		TimeRange: req.TimeRange, Filter: req.Filter,
	}, req.Buckets)
	if err != nil {
		return err
	}
	svc := iis.New(s.opts.API.Storage.Querier())

	overview, err := svc.Overview(r.Context(), sel, req.SlowMillis)
	if err != nil {
		return err
	}
	activity, err := svc.Activity(r.Context(), sel, step, req.SlowMillis)
	if err != nil {
		return err
	}
	serverErrors, err := svc.TopByClass(r.Context(), sel, iis.ClassServerError, iis.FieldURI, 10)
	if err != nil {
		return err
	}
	clientErrors, err := svc.TopByClass(r.Context(), sel, iis.ClassClientError, iis.FieldURI, 10)
	if err != nil {
		return err
	}
	recentServerErrors, err := svc.Failures(r.Context(), sel, iis.ClassServerError, req.Limit)
	if err != nil {
		return err
	}
	slowURLs, err := svc.SlowURLs(r.Context(), sel, req.SlowMillis, 10)
	if err != nil {
		return err
	}
	topURLs, err := svc.TopBy(r.Context(), sel, iis.FieldURI, 10)
	if err != nil {
		return err
	}
	topClients, err := svc.TopBy(r.Context(), sel, iis.FieldClientIP, 10)
	if err != nil {
		return err
	}
	statusCodes, err := svc.TopBy(r.Context(), sel, iis.FieldStatus, 12)
	if err != nil {
		return err
	}
	authFailureAccounts, err := svc.TopByStatus(r.Context(), sel, iis.StatusAuthRequired, iis.FieldUsername, 10)
	if err != nil {
		return err
	}
	authFailureSources, err := svc.TopByStatus(r.Context(), sel, iis.StatusAuthRequired, iis.FieldClientIP, 10)
	if err != nil {
		return err
	}
	authFailureReasons, err := svc.TopByStatus(r.Context(), sel, iis.StatusAuthRequired, iis.FieldSubStatus, 8)
	if err != nil {
		return err
	}
	byServer, err := svc.TopBy(r.Context(), sel, iis.FieldServerIP, 10)
	if err != nil {
		return err
	}

	s.auditQuery(r, p, "iis.overview", query.Selection{TimeRange: req.TimeRange, Filter: req.Filter})
	writeJSON(w, http.StatusOK, map[string]any{
		"resolved_range":        query.ResolvedRange{Start: sel.Range.Start, End: sel.Range.End},
		"overview":              overview,
		"activity":              activity,
		"server_errors":         serverErrors,
		"client_errors":         clientErrors,
		"recent_server_errors":  recentServerErrors,
		"slow_urls":             slowURLs,
		"top_urls":              topURLs,
		"top_clients":           topClients,
		"status_codes":          statusCodes,
		"auth_failure_accounts": authFailureAccounts,
		"auth_failure_sources":  authFailureSources,
		"auth_failure_reasons":  authFailureReasons,
		"by_server":             byServer,
	})
	return nil
}
