package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/metadata"
)

// ---- users ----------------------------------------------------------------

type userInput struct {
	Username    string  `json:"username"`
	DisplayName *string `json:"display_name"`
	Role        string  `json:"role"`
	Password    string  `json:"password"`
	Disabled    *bool   `json:"disabled"`
	NewPassword *string `json:"new_password"`
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	users, err := s.opts.API.Store.ListUsers(r.Context(), p.Tenant)
	if err != nil {
		return err
	}
	out := make([]userJSON, 0, len(users))
	for _, u := range users {
		out = append(out, toUserJSON(&u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
	return nil
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in userInput
	if err := decodeJSON(w, r, &in, true); err != nil {
		return err
	}
	in.Username = strings.TrimSpace(in.Username)
	if in.Username == "" || len(in.Username) > 128 {
		return badRequest("validation_failed", "/username", "username is required (at most 128 characters)")
	}
	if !auth.ValidRole(in.Role) {
		return badRequest("validation_failed", "/role", "role must be admin, operator or viewer")
	}
	u, err := s.opts.API.Auth.CreateUser(r.Context(), in.Username, deref(in.DisplayName), in.Role, in.Password)
	switch {
	case errors.Is(err, metadata.ErrConflict):
		return errStatus(http.StatusConflict, "conflict", "a user named %q already exists", in.Username)
	case err != nil:
		var pe *auth.PolicyError
		if errors.As(err, &pe) {
			return badRequest("validation_failed", "/password", "%s", pe.Error())
		}
		return err
	}
	s.audit(r, p, "users.create", "success", map[string]any{"user": u.Username, "role": u.Role, "id": u.ID})
	writeJSON(w, http.StatusCreated, toUserJSON(u))
	return nil
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return errStatus(http.StatusNotFound, "not_found", "no such user")
	}
	var in userInput
	if err := decodeJSON(w, r, &in, true); err != nil {
		return err
	}
	u, err := s.opts.API.Store.UserByID(r.Context(), id)
	if errors.Is(err, metadata.ErrNotFound) || (u != nil && u.Tenant != p.Tenant) {
		return errStatus(http.StatusNotFound, "not_found", "no such user")
	} else if err != nil {
		return err
	}
	details := map[string]any{"user": u.Username, "id": u.ID}
	if in.Role != "" {
		if !auth.ValidRole(in.Role) {
			return badRequest("validation_failed", "/role", "role must be admin, operator or viewer")
		}
		if u.ID == p.UserID && in.Role != u.Role {
			return badRequest("validation_failed", "/role", "you cannot change your own role")
		}
		u.Role, details["role"] = in.Role, in.Role
	}
	if in.DisplayName != nil {
		u.DisplayName = *in.DisplayName
	}
	if in.Disabled != nil {
		if u.ID == p.UserID && *in.Disabled {
			return badRequest("validation_failed", "/disabled", "you cannot disable your own account")
		}
		u.Disabled, details["disabled"] = *in.Disabled, *in.Disabled
	}
	// The password policy is checked before anything is written, so a
	// rejected request changes nothing.
	if in.NewPassword != nil {
		if err := auth.ValidatePasswordPolicy(*in.NewPassword, u.Username); err != nil {
			return badRequest("validation_failed", "/new_password", "%s", err.Error())
		}
	}
	if err := s.opts.API.Store.UpdateUser(r.Context(), u); err != nil {
		return err
	}
	if in.NewPassword != nil {
		if err := s.opts.API.Auth.ResetPassword(r.Context(), u, *in.NewPassword); err != nil {
			return err
		}
		details["password_reset"] = true
	}
	if u.Disabled {
		if err := s.opts.API.Store.DeleteUserSessions(r.Context(), u.ID, nil); err != nil {
			return err
		}
	}
	s.audit(r, p, "users.update", "success", details)
	writeJSON(w, http.StatusOK, toUserJSON(u))
	return nil
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return errStatus(http.StatusNotFound, "not_found", "no such user")
	}
	if id == p.UserID {
		return badRequest("validation_failed", "/id", "you cannot delete your own account")
	}
	u, err := s.opts.API.Store.UserByID(r.Context(), id)
	if errors.Is(err, metadata.ErrNotFound) || (u != nil && u.Tenant != p.Tenant) {
		return errStatus(http.StatusNotFound, "not_found", "no such user")
	} else if err != nil {
		return err
	}
	if u.Role == auth.RoleAdmin {
		admins, err := s.countAdmins(r, p)
		if err != nil {
			return err
		}
		if admins <= 1 {
			return badRequest("validation_failed", "/id", "the last administrator cannot be deleted")
		}
	}
	if err := s.opts.API.Store.DeleteUser(r.Context(), p.Tenant, id); err != nil {
		return err
	}
	s.audit(r, p, "users.delete", "success", map[string]any{"user": u.Username, "id": u.ID})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// handleRevokeUserSessions signs a user out everywhere.
func (s *Server) handleRevokeUserSessions(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return errStatus(http.StatusNotFound, "not_found", "no such user")
	}
	u, err := s.opts.API.Store.UserByID(r.Context(), id)
	if errors.Is(err, metadata.ErrNotFound) || (u != nil && u.Tenant != p.Tenant) {
		return errStatus(http.StatusNotFound, "not_found", "no such user")
	} else if err != nil {
		return err
	}
	if err := s.opts.API.Store.DeleteUserSessions(r.Context(), id, nil); err != nil {
		return err
	}
	s.audit(r, p, "users.revoke_sessions", "success", map[string]any{"user": u.Username, "id": id})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) countAdmins(r *http.Request, p *auth.Principal) (int, error) {
	users, err := s.opts.API.Store.ListUsers(r.Context(), p.Tenant)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, u := range users {
		if u.Role == auth.RoleAdmin && !u.Disabled {
			n++
		}
	}
	return n, nil
}

// ---- audit log ------------------------------------------------------------

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	q := r.URL.Query()
	f := metadata.ListAuditEvents{
		Tenant: p.Tenant, Actor: q.Get("actor"), Action: q.Get("action"), Outcome: q.Get("outcome"), Limit: 200,
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			return badRequest("validation_failed", "/limit", "limit must be between 1 and 1000")
		}
		f.Limit = n
	}
	for _, sel := range []struct {
		param string
		dst   *time.Time
	}{{"since", &f.Since}, {"before", &f.Before}} {
		if v := q.Get(sel.param); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return badRequest("validation_failed", "/"+sel.param, "%s must be an RFC 3339 timestamp", sel.param)
			}
			*sel.dst = t
		}
	}
	if f.Outcome != "" && f.Outcome != "success" && f.Outcome != "failure" {
		return badRequest("validation_failed", "/outcome", "outcome must be success or failure")
	}
	events, err := s.opts.API.Store.ListAuditEvents(r.Context(), f)
	if err != nil {
		return err
	}
	type eventJSON struct {
		ID        uuid.UUID  `json:"id"`
		Time      time.Time  `json:"time"`
		ActorType string     `json:"actor_type"`
		ActorID   *uuid.UUID `json:"actor_id,omitempty"`
		ActorName string     `json:"actor_name,omitempty"`
		IP        string     `json:"ip,omitempty"`
		UserAgent string     `json:"user_agent,omitempty"`
		Action    string     `json:"action"`
		Outcome   string     `json:"outcome"`
		Details   any        `json:"details,omitempty"`
		RequestID string     `json:"request_id,omitempty"`
	}
	out := make([]eventJSON, 0, len(events))
	for _, e := range events {
		item := eventJSON{ID: e.ID, Time: e.Time, ActorType: e.ActorType, ActorID: e.ActorID, ActorName: e.ActorName,
			IP: e.IP, UserAgent: e.UserAgent, Action: e.Action, Outcome: e.Outcome, RequestID: e.RequestID}
		if len(e.Details) > 0 {
			item.Details = e.Details
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
	return nil
}

// deref returns the value of an optional string field.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
