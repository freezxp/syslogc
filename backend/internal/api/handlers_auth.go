package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/metadata"
)

type userJSON struct {
	ID                 uuid.UUID  `json:"id"`
	Username           string     `json:"username"`
	DisplayName        string     `json:"display_name,omitempty"`
	Role               string     `json:"role"`
	MustChangePassword bool       `json:"must_change_password"`
	CreatedAt          time.Time  `json:"created_at"`
	LastLoginAt        *time.Time `json:"last_login_at"`
}

type sessionJSON struct {
	User        userJSON          `json:"user"`
	Permissions []auth.Permission `json:"permissions"`
	CSRFToken   string            `json:"csrf_token"`
}

func toSessionJSON(p *auth.Principal) sessionJSON {
	u := p.User
	return sessionJSON{
		User: userJSON{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Role: u.Role,
			MustChangePassword: u.MustChangePassword, CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt},
		Permissions: p.Permissions.Sorted(),
		CSRFToken:   p.CSRFToken,
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	if req.Username == "" || req.Password == "" || len(req.Username) > 128 || len(req.Password) > auth.MaxPasswordBytes {
		return badRequest("validation_failed", "/username", "username and password are required")
	}
	res, err := s.opts.API.Auth.Login(r.Context(), strings.TrimSpace(req.Username), req.Password, s.clientIP(r), r.UserAgent())
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) || errors.Is(err, auth.ErrRateLimited) {
			s.audit(r, nil, "auth.login", "failure", map[string]any{"username": truncate(req.Username, 128), "reason": err.Error()})
		}
		return err
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is configurable for plain-HTTP deployments (auth.cookie_secure)
		Name: sessionCookie, Value: res.Token, Path: "/", Expires: res.ExpiresAt,
		HttpOnly: true, Secure: s.opts.API.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
	s.audit(r, res.Principal, "auth.login", "success", nil)
	writeJSON(w, http.StatusOK, toSessionJSON(res.Principal))
	return nil
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.opts.API.Auth.Logout(r.Context(), p); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is configurable for plain-HTTP deployments (auth.cookie_secure)Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: s.opts.API.CookieSecure, SameSite: http.SameSiteLaxMode})
	s.audit(r, p, "auth.logout", "success", nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleMe(w http.ResponseWriter, _ *http.Request, p *auth.Principal) error {
	if p.Kind != auth.KindUser {
		return errStatus(http.StatusForbidden, "forbidden", "API keys have no user session")
	}
	writeJSON(w, http.StatusOK, toSessionJSON(p))
	return nil
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	if err := s.opts.API.Auth.ChangePassword(r.Context(), p, req.CurrentPassword, req.NewPassword); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			s.audit(r, p, "auth.password_change", "failure", nil)
			return badRequest("validation_failed", "/current_password", "current password is incorrect")
		}
		return err
	}
	s.audit(r, p, "auth.password_change", "success", nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func newAuditEvent(r *http.Request, ip string, p *auth.Principal, action, outcome string, details map[string]any) *metadata.AuditEvent {
	ev := &metadata.AuditEvent{
		Tenant: "default", ActorType: "anonymous", IP: ip, UserAgent: truncate(r.UserAgent(), 256),
		Action: action, Outcome: outcome, RequestID: requestID(r.Context()),
	}
	if p != nil {
		ev.Tenant, ev.ActorName = p.Tenant, p.Username
		switch p.Kind {
		case auth.KindUser:
			ev.ActorType, ev.ActorID = "user", &p.UserID
		case auth.KindAPIKey:
			ev.ActorType, ev.ActorID = "api_key", &p.APIKeyID
		}
	}
	if len(details) > 0 {
		ev.Details, _ = json.Marshal(details)
	}
	return ev
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
