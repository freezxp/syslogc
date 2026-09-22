package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/config"
	"github.com/freezxp/syslogc/backend/internal/extract"
	"github.com/freezxp/syslogc/backend/internal/ingestion/supervisor"
	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/metadata"
)

type sourceJSONBody struct {
	ID      *uuid.UUID    `json:"id,omitempty"`
	Config  config.Source `json:"config"`
	Enabled bool          `json:"enabled"`
	Origin  string        `json:"origin"`
	// Adopted marks a source copied from the configuration file, whose entry
	// there is now ignored.
	Adopted   bool               `json:"adopted,omitempty"`
	Status    *supervisor.Status `json:"status,omitempty"`
	CreatedAt *time.Time         `json:"created_at,omitempty"`
	UpdatedAt *time.Time         `json:"updated_at,omitempty"`
	Version   int                `json:"version,omitempty"`
}

type sourceInput struct {
	Config  config.Source `json:"config"`
	Enabled *bool         `json:"enabled"`
	Version int           `json:"version,omitempty"`
}

// managedSource converts a stored source, dropping defaults the API applies.
func managedSource(m metadata.Source, status map[string]supervisor.Status) (sourceJSONBody, error) {
	var sc config.Source
	if err := json.Unmarshal(m.Config, &sc); err != nil {
		return sourceJSONBody{}, err
	}
	sc.Name = m.Name
	body := sourceJSONBody{ID: &m.ID, Config: sc, Enabled: m.Enabled, Origin: supervisor.OriginDatabase,
		Adopted: m.Adopted, CreatedAt: &m.CreatedAt, UpdatedAt: &m.UpdatedAt, Version: m.Version}
	if st, ok := status[m.Name]; ok {
		body.Status = &st
	}
	return body, nil
}

func (s *Server) sourceStatuses() map[string]supervisor.Status {
	out := map[string]supervisor.Status{}
	if s.opts.API.Sources == nil {
		return out
	}
	for _, st := range s.opts.API.Sources() {
		out[st.Name] = st
	}
	return out
}

// handleListSources returns configuration-file and database sources together.
func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	status := s.sourceStatuses()
	out := []sourceJSONBody{}
	var managed []metadata.Source
	if s.opts.API.Store != nil {
		var err error
		if managed, err = s.opts.API.Store.ListSources(r.Context(), p.Tenant); err != nil {
			return err
		}
	}
	// A configuration-file entry that has been adopted is no longer used, so
	// listing it next to the copy that replaced it would be confusing.
	adopted := map[string]bool{}
	for _, m := range managed {
		if m.Adopted {
			adopted[strings.ToLower(m.Name)] = true
		}
	}
	for _, sc := range s.opts.API.FileSources {
		if adopted[strings.ToLower(sc.Name)] {
			continue
		}
		body := sourceJSONBody{Config: sc, Enabled: sc.IsEnabled(), Origin: supervisor.OriginFile}
		if st, ok := status[sc.Name]; ok {
			body.Status = &st
		}
		out = append(out, body)
	}
	for _, m := range managed {
		body, err := managedSource(m, status)
		if err != nil {
			return err
		}
		out = append(out, body)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": out})
	return nil
}

func (s *Server) handleGetSource(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	m, err := s.loadSource(r, p)
	if err != nil {
		return err
	}
	body, err := managedSource(*m, s.sourceStatuses())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, body)
	return nil
}

func (s *Server) handleCreateSource(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	in, sc, err := s.decodeSource(w, r, p, nil)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	m := &metadata.Source{Tenant: p.Tenant, Name: sc.Name, Config: raw, Enabled: in.Enabled == nil || *in.Enabled,
		CreatedBy: &p.UserID}
	if err := s.opts.API.Store.CreateSource(r.Context(), m); err != nil {
		if errors.Is(err, metadata.ErrConflict) {
			return errStatus(http.StatusConflict, "conflict", "a source named %q already exists", sc.Name)
		}
		return err
	}
	s.audit(r, p, "sources.create", "success", map[string]any{"source": sc.Name, "id": m.ID})
	body, err := managedSource(*m, s.sourceStatuses())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, body)
	return nil
}

func (s *Server) handleUpdateSource(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	m, err := s.loadSource(r, p)
	if err != nil {
		return err
	}
	in, sc, err := s.decodeSource(w, r, p, m)
	if err != nil {
		return err
	}
	if in.Version == 0 {
		return badRequest("validation_failed", "/version", "version is required for updates")
	}
	raw, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	m.Name, m.Config, m.Version = sc.Name, raw, in.Version
	if in.Enabled != nil {
		m.Enabled = *in.Enabled
	}
	switch err := s.opts.API.Store.UpdateSource(r.Context(), m); {
	case errors.Is(err, metadata.ErrVersionConflict):
		return errStatus(http.StatusConflict, "version_conflict", "the source was modified by someone else; reload it")
	case errors.Is(err, metadata.ErrConflict):
		return errStatus(http.StatusConflict, "conflict", "a source named %q already exists", sc.Name)
	case err != nil:
		return err
	}
	s.audit(r, p, "sources.update", "success", map[string]any{"source": sc.Name, "id": m.ID, "enabled": m.Enabled})
	body, err := managedSource(*m, s.sourceStatuses())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, body)
	return nil
}

func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	m, err := s.loadSource(r, p)
	if err != nil {
		return err
	}
	if err := s.opts.API.Store.DeleteSource(r.Context(), p.Tenant, m.ID); err != nil {
		return err
	}
	s.audit(r, p, "sources.delete", "success", map[string]any{"source": m.Name, "id": m.ID})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// loadSource resolves the {id} path parameter.
func (s *Server) loadSource(r *http.Request, p *auth.Principal) (*metadata.Source, error) {
	if s.opts.API.Store == nil {
		return nil, errStatus(http.StatusNotFound, "not_found", "source management requires a metadata database")
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return nil, errStatus(http.StatusNotFound, "not_found", "no such source")
	}
	m, err := s.opts.API.Store.SourceByID(r.Context(), p.Tenant, id)
	if errors.Is(err, metadata.ErrNotFound) {
		return nil, errStatus(http.StatusNotFound, "not_found", "no such source")
	}
	return m, err
}

// decodeSource validates a create or update body against the file sources and
// the other managed sources.
func (s *Server) decodeSource(w http.ResponseWriter, r *http.Request, p *auth.Principal, self *metadata.Source) (*sourceInput, config.Source, error) {
	var in sourceInput
	if s.opts.API.Store == nil {
		return nil, config.Source{}, errStatus(http.StatusNotFound, "not_found", "source management requires a metadata database")
	}
	if err := decodeJSON(w, r, &in, true); err != nil {
		return nil, config.Source{}, err
	}
	sc := in.Config
	sc.Name = strings.TrimSpace(sc.Name)
	if in.Enabled != nil {
		enabled := *in.Enabled
		sc.Enabled = &enabled
	}
	if err := config.PrepareSource(&sc); err != nil {
		return nil, sc, badRequest("validation_failed", "/config", "%s", strings.ReplaceAll(err.Error(), "\n", "; "))
	}
	if sc.Tenant != p.Tenant {
		return nil, sc, badRequest("validation_failed", "/config/tenant", "tenant must be %q", p.Tenant)
	}
	for _, other := range s.opts.API.FileSources {
		// An adopted source replaces its configuration-file entry, so that
		// entry is not something it can collide with.
		if self != nil && self.Adopted && strings.EqualFold(other.Name, self.Name) {
			continue
		}
		if reason := sc.ConflictsWith(other); reason != "" {
			return nil, sc, badRequest("validation_failed", "/config", "conflicts with a source from the configuration file: %s", reason)
		}
	}
	managed, err := s.opts.API.Store.ListSources(r.Context(), p.Tenant)
	if err != nil {
		return nil, sc, err
	}
	for _, m := range managed {
		if self != nil && m.ID == self.ID {
			continue
		}
		other, err := managedSource(m, nil)
		if err != nil {
			continue
		}
		other.Config.Enabled = &m.Enabled
		if reason := sc.ConflictsWith(other.Config); reason != "" {
			return nil, sc, badRequest("validation_failed", "/config", "%s", reason)
		}
	}
	return &in, sc, nil
}

// maxExtractSamples bounds a dry run; it is an authoring aid, not an API for
// bulk processing.
const (
	maxExtractSamples   = 10
	maxExtractSampleLen = 8 << 10
)

type extractTestRequest struct {
	Rules   []config.ExtractRule `json:"rules"`
	Samples []string             `json:"samples"`
}

type extractTestResult struct {
	Sample string `json:"sample"`
	// Rule is the rule that matched, empty when none did.
	Rule   string            `json:"rule"`
	Fields map[string]string `json:"fields,omitempty"`
	// Order lists the field names as the pattern produced them.
	Order []string `json:"order,omitempty"`
}

// handleTestExtract runs extract rules against sample lines without storing
// anything, so a pattern can be checked before it is saved to a source.
func (s *Server) handleTestExtract(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	var req extractTestRequest
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	if len(req.Rules) == 0 {
		return badRequest("validation_failed", "/rules", "at least one rule is required")
	}
	if len(req.Samples) == 0 || len(req.Samples) > maxExtractSamples {
		return badRequest("validation_failed", "/samples", "between 1 and %d samples are required", maxExtractSamples)
	}
	rules := make([]extract.Config, 0, len(req.Rules))
	for _, rule := range req.Rules {
		rules = append(rules, extract.Config{Name: rule.Name, Contains: rule.Contains, Regex: rule.Regex, Prefix: rule.Prefix})
	}
	ex, err := extract.New(rules)
	if err != nil {
		return badRequest("validation_failed", "/rules", "%s", err.Error())
	}

	results := make([]extractTestResult, 0, len(req.Samples))
	for _, sample := range req.Samples {
		if len(sample) > maxExtractSampleLen {
			return badRequest("validation_failed", "/samples", "a sample is longer than %d bytes", maxExtractSampleLen)
		}
		entry := &logentry.Entry{Message: sample}
		out := extractTestResult{Sample: sample, Rule: ex.Apply(entry)}
		if len(entry.Fields) > 0 {
			out.Fields = make(map[string]string, len(entry.Fields))
			for _, f := range entry.Fields {
				out.Fields[f.Key] = f.Value
				out.Order = append(out.Order, f.Key)
			}
		}
		results = append(results, out)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
	return nil
}

// handleAdoptSource copies a configuration-file source into the database so
// it can be edited here. The file entry stays where it is but stops being
// used, and deleting the copy hands control back to it.
func (s *Server) handleAdoptSource(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if s.opts.API.Store == nil {
		return errStatus(http.StatusNotFound, "not_configured", "source management requires a metadata database")
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	var file *config.Source
	for i, sc := range s.opts.API.FileSources {
		if strings.EqualFold(sc.Name, req.Name) {
			file = &s.opts.API.FileSources[i]
			break
		}
	}
	if file == nil {
		return errStatus(http.StatusNotFound, "not_found", "no source named %q in the configuration file", req.Name)
	}
	managed, err := s.opts.API.Store.ListSources(r.Context(), p.Tenant)
	if err != nil {
		return err
	}
	for _, m := range managed {
		if strings.EqualFold(m.Name, file.Name) {
			return errStatus(http.StatusConflict, "conflict", "%q is already managed here", file.Name)
		}
	}

	sc := *file
	if err := config.PrepareSource(&sc); err != nil {
		return badRequest("validation_failed", "/name", "the configuration-file source is invalid: %v", err)
	}
	raw, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	m := &metadata.Source{Tenant: p.Tenant, Name: sc.Name, Config: raw, Enabled: sc.IsEnabled(),
		Adopted: true, CreatedBy: &p.UserID}
	if err := s.opts.API.Store.CreateSource(r.Context(), m); err != nil {
		if errors.Is(err, metadata.ErrConflict) {
			return errStatus(http.StatusConflict, "conflict", "%q is already managed here", sc.Name)
		}
		return err
	}
	s.audit(r, p, "sources.adopt", "success", map[string]any{"source": sc.Name, "id": m.ID})
	body, err := managedSource(*m, s.sourceStatuses())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, body)
	return nil
}

// handleExtractPresets lists the built-in extract rules, so a common log
// format can be turned into fields without writing a regular expression.
func (s *Server) handleExtractPresets(w http.ResponseWriter, _ *http.Request, _ *auth.Principal) error {
	writeJSON(w, http.StatusOK, map[string]any{"presets": extract.Presets()})
	return nil
}
