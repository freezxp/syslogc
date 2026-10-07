package api

import (
	"net/http"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/mssql"
	"github.com/freezxp/syslogc/backend/internal/query"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

// mssqlRequest asks about a window of SQL Server activity.
type mssqlRequest struct {
	TimeRange query.TimeRange `json:"time_range"`
	Filter    *filter.Expr    `json:"filter,omitempty"`
	// Buckets is how many points the activity chart should hold.
	Buckets int `json:"buckets,omitempty"`
	// Limit caps the lists.
	Limit int `json:"limit,omitempty"`
}

// handleMSSQL answers every SQL Server question in one call.
//
// They are asked together because they are read together — a burst of failed
// sign-ins means one thing beside a transaction log that filled up and
// another beside none — and one request keeps the page consistent rather
// than stitched from answers taken moments apart.
func (s *Server) handleMSSQL(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req mssqlRequest
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	sel, step, err := s.opts.API.Query.AnalysisSelection(p, query.Selection{
		TimeRange: req.TimeRange, Filter: req.Filter,
	}, req.Buckets)
	if err != nil {
		return err
	}
	svc := mssql.New(s.opts.API.Storage.Querier())

	overview, err := svc.Overview(r.Context(), sel)
	if err != nil {
		return err
	}
	activity, err := svc.Activity(r.Context(), sel, step)
	if err != nil {
		return err
	}
	problems, err := svc.Problems(r.Context(), sel, req.Limit)
	if err != nil {
		return err
	}
	failedAccounts, err := svc.TopBy(r.Context(), sel, mssql.FieldLoginUser, 10, mssql.EventLoginFailed)
	if err != nil {
		return err
	}
	failureSources, err := svc.TopBy(r.Context(), sel, mssql.FieldClientIP, 10, mssql.EventLoginFailed)
	if err != nil {
		return err
	}
	// Ranked by error number, which groups; see TopMessages for why ranking
	// by the text does not.
	topErrors, err := svc.TopBy(r.Context(), sel, mssql.FieldEventID, 10, mssql.ProblemEvents()...)
	if err != nil {
		return err
	}
	topMessages, err := svc.TopMessages(r.Context(), sel, 10)
	if err != nil {
		return err
	}
	byInstance, err := svc.TopBy(r.Context(), sel, mssql.FieldProvider, 10, mssql.ProblemEvents()...)
	if err != nil {
		return err
	}
	byHost, err := svc.TopBy(r.Context(), sel, mssql.FieldHost, 10, mssql.ProblemEvents()...)
	if err != nil {
		return err
	}

	s.auditQuery(r, p, "mssql.overview", query.Selection{TimeRange: req.TimeRange, Filter: req.Filter})
	writeJSON(w, http.StatusOK, map[string]any{
		"resolved_range":  query.ResolvedRange{Start: sel.Range.Start, End: sel.Range.End},
		"overview":        overview,
		"activity":        activity,
		"problems":        problems,
		"failed_accounts": failedAccounts,
		"failure_sources": failureSources,
		"top_errors":      topErrors,
		"top_messages":    topMessages,
		"by_instance":     byInstance,
		"by_host":         byHost,
	})
	return nil
}
