package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/freezxp/syslogc/backend/internal/assistant"
	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/query"
	"github.com/freezxp/syslogc/backend/internal/sourcetemplate"
)

// assistantQueryRequest asks for a question to be turned into a query.
type assistantQueryRequest struct {
	Question string `json:"question"`
	// TimeRange, when given, is the window the field catalogue is read over.
	// It is not the window of the proposed query: the model chooses that.
	TimeRange query.TimeRange `json:"time_range,omitzero"`
}

// handleAssistantQuery proposes a query for a question. It runs nothing.
//
// The proposal comes back for a person to look at and press run. That is the
// whole safety model: the model's output reaches the store only after
// somebody has read it, so a misread question costs a glance rather than a
// large query on a busy deployment.
func (s *Server) handleAssistantQuery(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	a, err := s.assistantOrProblem()
	if err != nil {
		return err
	}
	var req assistantQueryRequest
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	// The catalogue is read over a window rather than all of history: a field
	// nobody has sent for a month is not one to answer questions with.
	sel := query.Selection{TimeRange: req.TimeRange}
	if sel.TimeRange.From == "" {
		sel.TimeRange = query.TimeRange{From: "now-24h", To: "now"}
	}
	fields, err := s.assistantFields(r, p, sel)
	if err != nil {
		return err
	}

	proposal, err := a.Propose(r.Context(), req.Question, fields)
	if err != nil {
		var unanswerable *assistant.Unanswerable
		if errors.As(err, &unanswerable) {
			// Not a failure. The honest answer to a question these logs
			// cannot answer, and far more useful than a query that runs and
			// means nothing.
			writeJSON(w, http.StatusOK, map[string]any{
				"answerable":  false,
				"reason":      unanswerable.Reason,
				"fields_seen": len(fields),
			})
			return nil
		}
		return errStatus(http.StatusBadGateway, "assistant_failed", "%v", err)
	}
	s.audit(r, p, "assistant.query", "success", map[string]any{
		"question": req.Question, "fields": proposal.Fields,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"answerable": true,
		"filter":     proposal.Filter,
		"time_range": map[string]string{"from": proposal.From, "to": proposal.To},
		"reasoning":  proposal.Reasoning,
		"fields":     proposal.Fields,
		"model":      proposal.Model,
		"took_ms":    proposal.Took.Milliseconds(),
	})
	return nil
}

// assistantExplainRequest carries figures that have already been computed.
type assistantExplainRequest struct {
	// Subject says what the figures are about, e.g. "SQL Server, last hour".
	Subject string `json:"subject,omitempty"`
	// Figures is whatever an analysis page already has. It must be the
	// computed numbers, not log lines: a window holds far more lines than
	// fit in a prompt, and the counts are what carry the meaning.
	Figures json.RawMessage `json:"figures"`
}

// handleAssistantExplain describes figures the caller has already computed.
func (s *Server) handleAssistantExplain(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	a, err := s.assistantOrProblem()
	if err != nil {
		return err
	}
	var req assistantExplainRequest
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	if len(req.Figures) == 0 {
		return badRequest("validation_failed", "/figures", "there are no figures to describe")
	}
	summary, err := a.Explain(r.Context(), req.Subject, req.Figures)
	if err != nil {
		return errStatus(http.StatusBadGateway, "assistant_failed", "%v", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"text":    summary.Text,
		"model":   summary.Model,
		"took_ms": summary.Took.Milliseconds(),
	})
	return nil
}

// handleAssistantStatus says whether the assistant is usable, and why not.
//
// It reaches the model endpoint rather than only reporting configuration: a
// URL in a file proves nothing, and "the assistant is on" next to a box that
// never answers is the unhelpful kind of status.
func (s *Server) handleAssistantStatus(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	cfg := s.opts.API.Config.Assistant
	body := map[string]any{"enabled": cfg.Enabled, "model": cfg.Model, "url": cfg.URL}
	if !cfg.Enabled {
		body["ready"] = false
		body["problem"] = "the assistant is off; set assistant.enabled, assistant.url and assistant.model"
		writeJSON(w, http.StatusOK, body)
		return nil
	}
	if s.opts.API.Assistant == nil {
		body["ready"] = false
		body["problem"] = "the assistant is on but was not configured successfully; see the server log"
		writeJSON(w, http.StatusOK, body)
		return nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	models, err := s.opts.API.AssistantModels(ctx)
	if err != nil {
		body["ready"] = false
		body["problem"] = err.Error()
		writeJSON(w, http.StatusOK, body)
		return nil
	}
	has := false
	for _, m := range models {
		if m == cfg.Model {
			has = true
			break
		}
	}
	body["ready"] = has
	body["available_models"] = models
	if !has {
		body["problem"] = "the endpoint answers but does not serve " + cfg.Model +
			"; pull it there, or set assistant.model to one it has"
	}
	writeJSON(w, http.StatusOK, body)
	return nil
}

// assistantOrProblem returns the assistant, or an error saying how to turn it
// on rather than a bare 404.
func (s *Server) assistantOrProblem() (*assistant.Assistant, error) {
	if s.opts.API.Assistant == nil {
		return nil, errStatus(http.StatusNotFound, "not_configured",
			"the assistant is off; set assistant.enabled, assistant.url and assistant.model, then restart")
	}
	return s.opts.API.Assistant, nil
}

// assistantFields reads the field catalogue the model may use, with a few
// example values each so it can write a code rather than a word.
func (s *Server) assistantFields(r *http.Request, p *auth.Principal, sel query.Selection) ([]assistant.Field, error) {
	if s.opts.API.Query == nil {
		return nil, errStatus(http.StatusNotFound, "not_configured", "this node cannot read logs")
	}
	resp, err := s.opts.API.Query.Fields(r.Context(), p, sel)
	if err != nil {
		return nil, err
	}
	// The field list says what exists; the templates say what it means. The
	// second is what lets the model know 4625 is a failed sign-in instead of
	// guessing that a failure is an HTTP 401 — which it did, until these
	// descriptions were passed through.
	described := templateFieldHelp()
	out := make([]assistant.Field, 0, len(resp.Fields))
	for _, f := range resp.Fields {
		fld := assistant.Field{Name: f.Name}
		if help, ok := described[f.Name]; ok {
			fld.Description = help.description
			if help.example != "" {
				fld.Examples = []string{help.example}
			}
		}
		out = append(out, fld)
	}
	return out, nil
}

// fieldHelp is what a template says about one field.
type fieldHelp struct {
	description string
	example     string
}

// templateFieldHelp collects every field every template describes, keyed by
// field name. Templates are the only place in the system that knows what a
// value means rather than merely that it exists.
func templateFieldHelp() map[string]fieldHelp {
	out := map[string]fieldHelp{}
	note := func(fs []sourcetemplate.Field) {
		for _, f := range fs {
			if _, seen := out[f.Name]; seen {
				continue
			}
			out[f.Name] = fieldHelp{description: f.Description, example: f.Example}
		}
	}
	for _, t := range sourcetemplate.All() {
		note(t.Fields)
		for _, p := range t.Parts {
			note(p.Fields)
		}
	}
	return out
}
