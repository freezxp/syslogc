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
	"github.com/freezxp/syslogc/backend/internal/ingestion/supervisor"
	"github.com/freezxp/syslogc/backend/internal/metadata"
)

type sourceJSONBody struct {
	ID        *uuid.UUID         `json:"id,omitempty"`
	Config    config.Source      `json:"config"`
	Enabled   bool               `json:"enabled"`
	Origin    string             `json:"origin"`
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
		CreatedAt: &m.CreatedAt, UpdatedAt: &m.UpdatedAt, Version: m.Version}
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
	for _, sc := range s.opts.API.FileSources {
		body := sourceJSONBody{Config: sc, Enabled: sc.IsEnabled(), Origin: supervisor.OriginFile}
		if st, ok := status[sc.Name]; ok {
			body.Status = &st
		}
		out = append(out, body)
	}
	if s.opts.API.Store != nil {
		managed, err := s.opts.API.Store.ListSources(r.Context(), p.Tenant)
		if err != nil {
			return err
		}
		for _, m := range managed {
			body, err := managedSource(m, status)
			if err != nil {
				return err
			}
			out = append(out, body)
		}
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
	in, sc, err := s.decodeSource(w, r, p, uuid.Nil)
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
	in, sc, err := s.decodeSource(w, r, p, m.ID)
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
func (s *Server) decodeSource(w http.ResponseWriter, r *http.Request, p *auth.Principal, self uuid.UUID) (*sourceInput, config.Source, error) {
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
		if reason := sc.ConflictsWith(other); reason != "" {
			return nil, sc, badRequest("validation_failed", "/config", "conflicts with a source from the configuration file: %s", reason)
		}
	}
	managed, err := s.opts.API.Store.ListSources(r.Context(), p.Tenant)
	if err != nil {
		return nil, sc, err
	}
	for _, m := range managed {
		if m.ID == self {
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
