package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/config"
	"github.com/freezxp/syslogc/backend/internal/forwarding"
	"github.com/freezxp/syslogc/backend/internal/metadata"
)

// forwardTargetBody is one target as the API presents it.
type forwardTargetBody struct {
	ID      *uuid.UUID           `json:"id,omitempty"`
	Config  config.ForwardTarget `json:"config"`
	Enabled bool                 `json:"enabled"`
	Origin  string               `json:"origin"`
	// TokenStored says a credential is held without returning it.
	TokenStored bool               `json:"token_stored,omitempty"`
	Status      *forwarding.Status `json:"status,omitempty"`
	Version     int                `json:"version,omitempty"`
}

type forwardTargetInput struct {
	Config  config.ForwardTarget `json:"config"`
	Enabled *bool                `json:"enabled"`
	// Token is the bearer token for the remote. Write-only: an empty one on
	// an existing target keeps whatever is stored, as a form that never
	// received it cannot send it back.
	Token   *string `json:"token,omitempty"`
	Version int     `json:"version"`
}

func (s *Server) forwardStatuses() map[string]forwarding.Status {
	out := map[string]forwarding.Status{}
	if s.opts.API.Forwarders == nil {
		return out
	}
	for _, st := range s.opts.API.Forwarders() {
		out[strings.ToLower(st.Name)] = st
	}
	return out
}

// handleListForwardTargets returns configuration-file and database targets
// together, the same way sources are listed.
func (s *Server) handleListForwardTargets(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	status := s.forwardStatuses()
	out := []forwardTargetBody{}
	for _, t := range s.opts.API.Config.Forwarding.Targets {
		body := forwardTargetBody{Config: t, Enabled: t.IsEnabled(), Origin: forwarding.OriginFile}
		body.redactToken()
		if st, ok := status[strings.ToLower(t.Name)]; ok {
			body.Status = &st
		}
		out = append(out, body)
	}
	if s.opts.API.Store != nil {
		stored, err := s.opts.API.Store.ListForwardTargets(r.Context(), p.Tenant)
		if err != nil {
			return err
		}
		for _, m := range stored {
			body, err := storedForwardTarget(m)
			if err != nil {
				continue
			}
			if st, ok := status[strings.ToLower(m.Name)]; ok {
				body.Status = &st
			}
			out = append(out, body)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": out})
	return nil
}

func storedForwardTarget(m metadata.ForwardTarget) (forwardTargetBody, error) {
	var t config.ForwardTarget
	if err := json.Unmarshal(m.Config, &t); err != nil {
		return forwardTargetBody{}, err
	}
	t.Name = m.Name
	body := forwardTargetBody{ID: &m.ID, Config: t, Enabled: m.Enabled,
		Origin: forwarding.OriginDatabase, TokenStored: m.Secret != "", Version: m.Version}
	body.redactToken()
	return body, nil
}

// redactToken clears credentials from a response. Tokens are write-only, so
// this is the single place every read passes through.
func (b *forwardTargetBody) redactToken() {
	b.Config.BearerToken = ""
}

func (s *Server) handleCreateForwardTarget(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	in, t, err := s.decodeForwardTarget(w, r, p, nil)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	m := &metadata.ForwardTarget{Tenant: p.Tenant, Name: t.Name, Config: raw,
		Enabled: in.Enabled != nil && *in.Enabled, CreatedBy: &p.UserID}
	if in.Token != nil {
		m.Secret = *in.Token
	}
	if err := s.opts.API.Store.CreateForwardTarget(r.Context(), m); err != nil {
		if errors.Is(err, metadata.ErrConflict) {
			return errStatus(http.StatusConflict, "conflict", "a forward target named %q already exists", t.Name)
		}
		return err
	}
	s.audit(r, p, "forwarding.create", "success", map[string]any{"target": t.Name, "enabled": m.Enabled})
	body, err := storedForwardTarget(*m)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, body)
	return nil
}

func (s *Server) handleUpdateForwardTarget(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	m, err := s.forwardTargetByID(r, p)
	if err != nil {
		return err
	}
	in, t, err := s.decodeForwardTarget(w, r, p, m)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	m.Name, m.Config, m.Version = t.Name, raw, in.Version
	if in.Enabled != nil {
		m.Enabled = *in.Enabled
	}
	// An absent token means unchanged; an empty one means remove it.
	if in.Token != nil {
		m.Secret = *in.Token
	}
	if err := s.opts.API.Store.UpdateForwardTarget(r.Context(), m); err != nil {
		if errors.Is(err, metadata.ErrVersionConflict) {
			return errStatus(http.StatusConflict, "conflict",
				"this target was changed by someone else; reload and apply your change again")
		}
		return err
	}
	s.audit(r, p, "forwarding.update", "success", map[string]any{"target": m.Name, "enabled": m.Enabled})
	body, err := storedForwardTarget(*m)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, body)
	return nil
}

func (s *Server) handleDeleteForwardTarget(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	m, err := s.forwardTargetByID(r, p)
	if err != nil {
		return err
	}
	if err := s.opts.API.Store.DeleteForwardTarget(r.Context(), p.Tenant, m.ID); err != nil {
		return err
	}
	s.audit(r, p, "forwarding.delete", "success", map[string]any{"target": m.Name})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) forwardTargetByID(r *http.Request, p *auth.Principal) (*metadata.ForwardTarget, error) {
	if s.opts.API.Store == nil {
		return nil, errStatus(http.StatusNotFound, "not_configured", "forwarding needs a metadata database")
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return nil, errStatus(http.StatusNotFound, "not_found", "no such forward target")
	}
	m, err := s.opts.API.Store.ForwardTargetByID(r.Context(), p.Tenant, id)
	if errors.Is(err, metadata.ErrNotFound) || m == nil {
		return nil, errStatus(http.StatusNotFound, "not_found", "no such forward target")
	}
	return m, err
}

// decodeForwardTarget validates a target against the file-defined ones and
// the others already stored.
func (s *Server) decodeForwardTarget(w http.ResponseWriter, r *http.Request, p *auth.Principal, self *metadata.ForwardTarget) (*forwardTargetInput, config.ForwardTarget, error) {
	if s.opts.API.Store == nil {
		return nil, config.ForwardTarget{}, errStatus(http.StatusNotFound, "not_configured",
			"forwarding needs a metadata database")
	}
	var in forwardTargetInput
	if err := decodeJSON(w, r, &in, true); err != nil {
		return nil, config.ForwardTarget{}, err
	}
	t := in.Config
	t.Name = strings.TrimSpace(t.Name)
	if err := config.PrepareForwardTarget(&t); err != nil {
		return nil, t, badRequest("validation_failed", "/config", "%s", strings.ReplaceAll(err.Error(), "\n", "; "))
	}
	// A name the configuration file already uses would be ignored at
	// startup, so it is refused here rather than silently doing nothing.
	for _, other := range s.opts.API.Config.Forwarding.Targets {
		if strings.EqualFold(other.Name, t.Name) {
			return nil, t, badRequest("validation_failed", "/config/name",
				"the configuration file already defines a target named %q", t.Name)
		}
	}
	stored, err := s.opts.API.Store.ListForwardTargets(r.Context(), p.Tenant)
	if err != nil {
		return nil, t, err
	}
	for _, m := range stored {
		if self != nil && m.ID == self.ID {
			continue
		}
		if strings.EqualFold(m.Name, t.Name) {
			return nil, t, errStatus(http.StatusConflict, "conflict", "a forward target named %q already exists", t.Name)
		}
	}
	return &in, t, nil
}
