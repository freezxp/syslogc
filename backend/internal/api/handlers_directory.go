package api

import (
	"net/http"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/directory"
	"github.com/freezxp/syslogc/backend/internal/query"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

// directoryRequest asks about a window of domain activity.
type directoryRequest struct {
	TimeRange query.TimeRange `json:"time_range"`
	Filter    *filter.Expr    `json:"filter,omitempty"`
	// Buckets is how many points the activity chart should hold.
	Buckets int `json:"buckets,omitempty"`
	// Limit caps the lists.
	Limit int `json:"limit,omitempty"`
}

// handleDirectory answers every directory question in one call.
//
// They are asked together because they are read together — a rise in failed
// sign-ins means one thing beside a lockout and another beside none — and
// one request keeps the page consistent rather than stitched from answers
// taken moments apart.
func (s *Server) handleDirectory(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req directoryRequest
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	sel, step, err := s.opts.API.Query.DirectorySelection(p, query.Selection{
		TimeRange: req.TimeRange, Filter: req.Filter,
	}, req.Buckets)
	if err != nil {
		return err
	}
	svc := directory.New(s.opts.API.Storage.Querier())

	overview, err := svc.Overview(r.Context(), sel)
	if err != nil {
		return err
	}
	activity, err := svc.Activity(r.Context(), sel, step)
	if err != nil {
		return err
	}
	lockouts, err := svc.Lockouts(r.Context(), sel, req.Limit)
	if err != nil {
		return err
	}
	changes, err := svc.Changes(r.Context(), sel, req.Limit)
	if err != nil {
		return err
	}
	topUsers, err := svc.TopBy(r.Context(), sel, directory.FieldUser, 10, directory.EventLogonSuccess)
	if err != nil {
		return err
	}
	topFailures, err := svc.TopBy(r.Context(), sel, directory.FieldUser, 10, directory.EventLogonFailure)
	if err != nil {
		return err
	}
	failureReasons, err := svc.TopBy(r.Context(), sel, directory.FieldStatus, 8, directory.EventLogonFailure)
	if err != nil {
		return err
	}
	logonTypes, err := svc.TopBy(r.Context(), sel, directory.FieldLogonType, 8, directory.EventLogonSuccess)
	if err != nil {
		return err
	}
	sources, err := svc.TopBy(r.Context(), sel, directory.FieldSourceIP, 10,
		directory.EventLogonFailure, directory.EventKerberosPreauthFailed)
	if err != nil {
		return err
	}

	s.auditQuery(r, p, "directory.overview", query.Selection{TimeRange: req.TimeRange, Filter: req.Filter})
	writeJSON(w, http.StatusOK, map[string]any{
		"resolved_range":   query.ResolvedRange{Start: sel.Range.Start, End: sel.Range.End},
		"overview":         overview,
		"activity":         activity,
		"lockouts":         lockouts,
		"changes":          changes,
		"busiest_accounts": topUsers,
		"most_failures":    topFailures,
		"failure_reasons":  failureReasons,
		"logon_types":      logonTypes,
		"failure_sources":  sources,
	})
	return nil
}
