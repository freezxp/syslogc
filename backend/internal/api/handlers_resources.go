package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/metadata"
	"github.com/freezxp/syslogc/backend/internal/query"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

// ---- saved searches -----------------------------------------------------

type savedSearchQuery struct {
	Filter *filter.Expr       `json:"filter,omitempty"`
	Native *query.NativeQuery `json:"native,omitempty"`
}

type savedSearchInput struct {
	Name             string           `json:"name"`
	Description      string           `json:"description"`
	Query            savedSearchQuery `json:"query"`
	Columns          []string         `json:"columns"`
	DefaultTimeRange *query.TimeRange `json:"default_time_range,omitempty"`
	Visibility       string           `json:"visibility"`
	Version          int              `json:"version,omitempty"`
}

type savedSearchJSON struct {
	ID               uuid.UUID       `json:"id"`
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	Query            json.RawMessage `json:"query"`
	Dialect          *string         `json:"dialect"`
	Columns          []string        `json:"columns"`
	DefaultTimeRange json.RawMessage `json:"default_time_range,omitempty"`
	Visibility       string          `json:"visibility"`
	CreatedBy        map[string]any  `json:"created_by"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	Version          int             `json:"version"`
}

func toSavedSearchJSON(ss *metadata.SavedSearch) savedSearchJSON {
	out := savedSearchJSON{
		ID: ss.ID, Name: ss.Name, Description: ss.Description, Query: ss.Query, Columns: ss.Columns,
		DefaultTimeRange: ss.DefaultTimeRange, Visibility: ss.Visibility,
		CreatedBy: map[string]any{"id": ss.OwnerID, "username": ss.OwnerUsername},
		CreatedAt: ss.CreatedAt, UpdatedAt: ss.UpdatedAt, Version: ss.Version,
	}
	if out.Columns == nil {
		out.Columns = []string{}
	}
	var q savedSearchQuery
	if json.Unmarshal(ss.Query, &q) == nil && q.Native != nil && q.Native.Text != "" {
		d := q.Native.Dialect
		out.Dialect = &d
	}
	return out
}

func (in *savedSearchInput) validate(p *auth.Principal) error {
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "" || len(in.Name) > 128 || strings.ContainsAny(in.Name, "\x00\r\n"):
		return badRequest("validation_failed", "/name", "name must be 1–128 characters without line breaks")
	case len(in.Description) > 1024:
		return badRequest("validation_failed", "/description", "description must be at most 1024 characters")
	case len(in.Columns) > 50:
		return badRequest("validation_failed", "/columns", "at most 50 columns")
	}
	if in.Visibility == "" {
		in.Visibility = "private"
	}
	if in.Visibility != "private" && in.Visibility != "shared" {
		return badRequest("validation_failed", "/visibility", "visibility must be private or shared")
	}
	if err := filter.Validate(in.Query.Filter); err != nil {
		var ve *filter.ValidationError
		if errors.As(err, &ve) {
			return badRequest("validation_failed", "/query"+ve.Pointer, "%s", ve.Message)
		}
		return badRequest("validation_failed", "/query/filter", "%s", err.Error())
	}
	if n := in.Query.Native; n != nil && n.Text != "" {
		if n.Dialect != "logsql" {
			return badRequest("validation_failed", "/query/native/dialect", "unsupported dialect")
		}
		if !p.Can(auth.PermLogsQueryNative) {
			return errStatus(http.StatusForbidden, "forbidden", "saving native queries requires %s", auth.PermLogsQueryNative)
		}
	}
	if in.DefaultTimeRange != nil {
		if _, _, err := query.Resolve(*in.DefaultTimeRange, time.Now()); err != nil {
			return badRequest("validation_failed", "/default_time_range", "%s", err.Error())
		}
	}
	return nil
}

func (s *Server) handleListSearches(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	q := r.URL.Query()
	f := metadata.ListSavedSearches{Tenant: p.Tenant, ViewerID: p.UserID, Query: truncate(q.Get("q"), 128), Limit: 50}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			return badRequest("validation_failed", "limit", "limit must be between 1 and 200")
		}
		f.Limit = n
	}
	if c := q.Get("cursor"); c != "" {
		raw, err := base64.RawURLEncoding.DecodeString(c)
		name, id, ok := strings.Cut(string(raw), "\x00")
		uid, uerr := uuid.Parse(id)
		if err != nil || !ok || uerr != nil {
			return badRequest("invalid_cursor", "cursor", "invalid cursor")
		}
		f.AfterName, f.AfterID = name, uid
	}
	items, err := s.opts.API.Store.ListSavedSearches(r.Context(), f)
	if err != nil {
		return err
	}
	resp := struct {
		Items      []savedSearchJSON `json:"items"`
		NextCursor *string           `json:"next_cursor"`
	}{Items: []savedSearchJSON{}}
	for i := range items {
		resp.Items = append(resp.Items, toSavedSearchJSON(&items[i]))
	}
	if len(items) == f.Limit {
		last := items[len(items)-1]
		c := base64.RawURLEncoding.EncodeToString([]byte(last.Name + "\x00" + last.ID.String()))
		resp.NextCursor = &c
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) loadSearch(r *http.Request, p *auth.Principal) (*metadata.SavedSearch, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return nil, metadata.ErrNotFound
	}
	ss, err := s.opts.API.Store.SavedSearchByID(r.Context(), p.Tenant, id)
	if err != nil {
		return nil, err
	}
	if ss.Visibility != "shared" && ss.OwnerID != p.UserID {
		return nil, metadata.ErrNotFound
	}
	return ss, nil
}

func (s *Server) handleGetSearch(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	ss, err := s.loadSearch(r, p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toSavedSearchJSON(ss))
	return nil
}

func (s *Server) handleCreateSearch(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if p.Kind != auth.KindUser {
		return errStatus(http.StatusForbidden, "forbidden", "saved searches belong to users")
	}
	var in savedSearchInput
	if err := decodeJSON(w, r, &in, true); err != nil {
		return err
	}
	if err := in.validate(p); err != nil {
		return err
	}
	qjson, _ := json.Marshal(in.Query)
	ss := &metadata.SavedSearch{Tenant: p.Tenant, OwnerID: p.UserID, OwnerUsername: p.Username, Name: in.Name,
		Description: in.Description, Query: qjson, Columns: in.Columns, Visibility: in.Visibility}
	if in.DefaultTimeRange != nil {
		ss.DefaultTimeRange, _ = json.Marshal(in.DefaultTimeRange)
	}
	if err := s.opts.API.Store.CreateSavedSearch(r.Context(), ss); err != nil {
		return err
	}
	s.audit(r, p, "saved_search.create", "success", map[string]any{"id": ss.ID, "name": ss.Name})
	writeJSON(w, http.StatusCreated, toSavedSearchJSON(ss))
	return nil
}

// canModify allows owners and administrators to change a saved search.
func canModify(p *auth.Principal, ss *metadata.SavedSearch) bool {
	return ss.OwnerID == p.UserID || p.Can(auth.PermUsersManage)
}

func (s *Server) handleUpdateSearch(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	ss, err := s.loadSearch(r, p)
	if err != nil {
		return err
	}
	if !canModify(p, ss) {
		return errStatus(http.StatusForbidden, "forbidden", "only the owner can modify this saved search")
	}
	var in savedSearchInput
	if err := decodeJSON(w, r, &in, true); err != nil {
		return err
	}
	if in.Version <= 0 {
		return badRequest("validation_failed", "/version", "version is required")
	}
	if err := in.validate(p); err != nil {
		return err
	}
	ss.Name, ss.Description, ss.Columns, ss.Visibility, ss.Version = in.Name, in.Description, in.Columns, in.Visibility, in.Version
	ss.Query, _ = json.Marshal(in.Query)
	ss.DefaultTimeRange = nil
	if in.DefaultTimeRange != nil {
		ss.DefaultTimeRange, _ = json.Marshal(in.DefaultTimeRange)
	}
	if err := s.opts.API.Store.UpdateSavedSearch(r.Context(), ss); err != nil {
		return err
	}
	s.audit(r, p, "saved_search.update", "success", map[string]any{"id": ss.ID})
	writeJSON(w, http.StatusOK, toSavedSearchJSON(ss))
	return nil
}

func (s *Server) handleDeleteSearch(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	ss, err := s.loadSearch(r, p)
	if err != nil {
		return err
	}
	if !canModify(p, ss) {
		return errStatus(http.StatusForbidden, "forbidden", "only the owner can delete this saved search")
	}
	if err := s.opts.API.Store.DeleteSavedSearch(r.Context(), p.Tenant, ss.ID); err != nil {
		return err
	}
	s.audit(r, p, "saved_search.delete", "success", map[string]any{"id": ss.ID, "name": ss.Name})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- API keys -------------------------------------------------------------

type apiKeyJSON struct {
	ID         uuid.UUID  `json:"id"`
	KeyID      string     `json:"key_id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	Owner      *string    `json:"owner"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

func toAPIKeyJSON(k *metadata.APIKey) apiKeyJSON {
	out := apiKeyJSON{ID: k.ID, KeyID: k.KeyID, Name: k.Name, Scopes: k.Scopes, CreatedAt: k.CreatedAt,
		ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt}
	if k.OwnerName != "" {
		out.Owner = &k.OwnerName
	}
	return out
}

func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	keys, err := s.opts.API.Auth.ListAPIKeys(r.Context(), p)
	if err != nil {
		return err
	}
	items := make([]apiKeyJSON, 0, len(keys))
	for i := range keys {
		items = append(items, toAPIKeyJSON(&keys[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if p.Kind != auth.KindUser {
		return errStatus(http.StatusForbidden, "forbidden", "API keys cannot create API keys")
	}
	var req struct {
		Name      string            `json:"name"`
		Scopes    []auth.Permission `json:"scopes"`
		ExpiresAt *time.Time        `json:"expires_at"`
	}
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 128 {
		return badRequest("validation_failed", "/name", "name must be 1–128 characters")
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		return badRequest("validation_failed", "/expires_at", "expires_at must be in the future")
	}
	key, secret, err := s.opts.API.Auth.CreateAPIKey(r.Context(), p, req.Name, req.Scopes, req.ExpiresAt)
	if err != nil {
		return err
	}
	s.audit(r, p, "apikey.create", "success", map[string]any{"id": key.ID, "name": key.Name, "scopes": key.Scopes})
	writeJSON(w, http.StatusCreated, map[string]any{"api_key": toAPIKeyJSON(key), "secret": secret})
	return nil
}

func (s *Server) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return metadata.ErrNotFound
	}
	if err := s.opts.API.Auth.RevokeAPIKey(r.Context(), p, id); err != nil {
		return err
	}
	s.audit(r, p, "apikey.revoke", "success", map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
	return nil
}
