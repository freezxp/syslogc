package api

import (
	"net/http"
	"strings"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/query"
)

// auditQuery records native queries (always) and all searches when
// query.audit_all is enabled.
func (s *Server) auditQuery(r *http.Request, p *auth.Principal, action string, sel query.Selection) {
	native := sel.Native != nil && strings.TrimSpace(sel.Native.Text) != ""
	if !native && !s.opts.API.AuditAll {
		return
	}
	details := map[string]any{"from": sel.TimeRange.From, "to": sel.TimeRange.To}
	if native {
		action = "logs.query_native"
		details["native"] = truncate(sel.Native.Text, 2048)
	}
	s.audit(r, p, action, "success", details)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req query.SearchRequest
	if err := decodeJSON(w, r, &req, false); err != nil {
		return err
	}
	resp, err := s.opts.API.Query.Search(r.Context(), p, req)
	if err != nil {
		return err
	}
	s.auditQuery(r, p, "logs.search", req.Selection)
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) handleHistogram(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req query.HistogramRequest
	if err := decodeJSON(w, r, &req, false); err != nil {
		return err
	}
	resp, err := s.opts.API.Query.Histogram(r.Context(), p, req)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) handleFacets(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req query.FacetsRequest
	if err := decodeJSON(w, r, &req, false); err != nil {
		return err
	}
	resp, err := s.opts.API.Query.Facets(r.Context(), p, req)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req query.StatsRequest
	if err := decodeJSON(w, r, &req, false); err != nil {
		return err
	}
	resp, err := s.opts.API.Query.Stats(r.Context(), p, req)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req query.ValidateRequest
	if err := decodeJSON(w, r, &req, false); err != nil {
		return err
	}
	resp, err := s.opts.API.Query.Validate(r.Context(), p, req)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) handleFields(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req query.Selection
	if err := decodeJSON(w, r, &req, false); err != nil {
		return err
	}
	resp, err := s.opts.API.Query.Fields(r.Context(), p, req)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) handleFieldValues(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req query.FieldValuesRequest
	if err := decodeJSON(w, r, &req, false); err != nil {
		return err
	}
	resp, err := s.opts.API.Query.FieldValues(r.Context(), p, r.PathValue("field"), req)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req query.DashboardRequest
	if err := decodeJSON(w, r, &req, false); err != nil {
		return err
	}
	resp, err := s.opts.API.Query.Overview(r.Context(), p, req)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) handleVolume(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req query.DashboardRequest
	if err := decodeJSON(w, r, &req, false); err != nil {
		return err
	}
	resp, err := s.opts.API.Query.Volume(r.Context(), p, req)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) handleTop(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req query.DashboardRequest
	if err := decodeJSON(w, r, &req, false); err != nil {
		return err
	}
	resp, err := s.opts.API.Query.Top(r.Context(), p, req)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) handleIngestionRate(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req query.DashboardRequest
	if err := decodeJSON(w, r, &req, false); err != nil {
		return err
	}
	resp, err := s.opts.API.Query.IngestionRate(r.Context(), p, req)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}
