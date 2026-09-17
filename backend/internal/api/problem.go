package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/metadata"
	"github.com/freezxp/syslogc/backend/internal/query"
)

// Problem is an RFC 9457 problem details body.
type Problem struct {
	Type      string         `json:"type"`
	Title     string         `json:"title"`
	Status    int            `json:"status"`
	Detail    string         `json:"detail,omitempty"`
	Instance  string         `json:"instance,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
	Code      string         `json:"code"`
	Errors    []ProblemError `json:"errors,omitempty"`
}

type ProblemError struct {
	Pointer  string `json:"pointer,omitempty"`
	Position int    `json:"position,omitempty"`
	Message  string `json:"message"`
}

// apiError is an error with an explicit HTTP mapping.
type apiError struct {
	status  int
	code    string
	detail  string
	pointer string
}

func (e *apiError) Error() string { return e.detail }

func errStatus(status int, code, format string, args ...any) error {
	return &apiError{status: status, code: code, detail: fmt.Sprintf(format, args...)}
}

func badRequest(code, pointer, format string, args ...any) error {
	status := http.StatusUnprocessableEntity
	if code == "bad_request" {
		status = http.StatusBadRequest
	}
	return &apiError{status: status, code: code, detail: fmt.Sprintf(format, args...), pointer: pointer}
}

var titles = map[string]string{
	"bad_request": "Bad request", "invalid_time_range": "Invalid time range", "invalid_cursor": "Invalid cursor",
	"unauthenticated": "Authentication required", "session_expired": "Session expired", "forbidden": "Forbidden",
	"csrf_failed": "CSRF check failed", "not_found": "Not found", "conflict": "Conflict",
	"payload_too_large": "Payload too large", "unsupported_media_type": "Unsupported media type",
	"validation_failed": "Validation failed", "query_invalid": "Invalid query", "rate_limited": "Too many requests",
	"too_many_concurrent_queries": "Too many concurrent queries", "too_many_tail_sessions": "Too many live tail sessions",
	"ingest_backpressure": "Ingestion is saturated", "storage_unavailable": "Storage unavailable",
	"query_timeout": "Query timed out", "internal": "Internal error", "not_configured": "Not configured",
}

// toProblem maps an error to a problem.
func toProblem(err error) Problem {
	p := Problem{Status: http.StatusInternalServerError, Code: "internal", Detail: "an unexpected error occurred"}
	var ae *apiError
	var ie *query.InputError
	var pe *auth.PolicyError
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &ae):
		p.Status, p.Code, p.Detail = ae.status, ae.code, ae.detail
		if ae.pointer != "" {
			p.Errors = []ProblemError{{Pointer: ae.pointer, Message: ae.detail}}
		}
	case errors.As(err, &ie):
		p.Code, p.Detail = ie.Code, ie.Message
		p.Status = http.StatusUnprocessableEntity
		if ie.Code == "invalid_time_range" || ie.Code == "invalid_cursor" {
			p.Status = http.StatusBadRequest
		}
		p.Errors = []ProblemError{{Pointer: ie.Pointer, Message: ie.Message}}
	case errors.As(err, &pe):
		p.Status, p.Code, p.Detail = http.StatusUnprocessableEntity, "validation_failed", pe.Error()
		p.Errors = []ProblemError{{Pointer: "/new_password", Message: pe.Error()}}
	case errors.As(err, &mbe):
		p.Status, p.Code, p.Detail = http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large"
	case errors.Is(err, auth.ErrInvalidCredentials):
		p.Status, p.Code, p.Detail = http.StatusUnauthorized, "unauthenticated", "invalid username or password"
	case errors.Is(err, auth.ErrSessionExpired):
		p.Status, p.Code, p.Detail = http.StatusUnauthorized, "session_expired", "your session has expired"
	case errors.Is(err, auth.ErrUnauthenticated):
		p.Status, p.Code, p.Detail = http.StatusUnauthorized, "unauthenticated", "authentication required"
	case errors.Is(err, auth.ErrRateLimited):
		p.Status, p.Code, p.Detail = http.StatusTooManyRequests, "rate_limited", "too many login attempts; try again later"
	case errors.Is(err, auth.ErrInvalidScopes):
		p.Status, p.Code, p.Detail = http.StatusUnprocessableEntity, "validation_failed", err.Error()
	case errors.Is(err, query.ErrForbidden):
		p.Status, p.Code, p.Detail = http.StatusForbidden, "forbidden", err.Error()
	case errors.Is(err, query.ErrTooManyQueries):
		p.Status, p.Code, p.Detail = http.StatusTooManyRequests, "too_many_concurrent_queries", "too many concurrent queries; wait for running queries to finish"
	case errors.Is(err, query.ErrTooManyTails):
		p.Status, p.Code, p.Detail = http.StatusTooManyRequests, "too_many_tail_sessions", "too many live tail sessions"
	case errors.Is(err, query.ErrQueryTimeout):
		p.Status, p.Code, p.Detail = http.StatusGatewayTimeout, "query_timeout", "the query took too long; narrow the time range or filter"
	case errors.Is(err, query.ErrStorageUnavailable):
		p.Status, p.Code, p.Detail = http.StatusServiceUnavailable, "storage_unavailable", "log storage is unavailable"
	case errors.Is(err, metadata.ErrNotFound):
		p.Status, p.Code, p.Detail = http.StatusNotFound, "not_found", "not found"
	case errors.Is(err, metadata.ErrVersionConflict):
		p.Status, p.Code, p.Detail = http.StatusConflict, "conflict", "the resource was modified by someone else; reload and retry"
	case errors.Is(err, metadata.ErrConflict):
		p.Status, p.Code, p.Detail = http.StatusConflict, "conflict", "a resource with this name already exists"
	}
	p.Title = titles[p.Code]
	p.Type = "https://syslogc.dev/problems/" + strings.ReplaceAll(p.Code, "_", "-")
	return p
}

// writeError renders err as problem details and logs server-side failures.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		return // client went away
	}
	p := toProblem(err)
	p.Instance = r.URL.Path
	p.RequestID = requestID(r.Context())
	if p.Status >= 500 {
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "request_id", p.RequestID, "error", err)
	}
	if p.Status == http.StatusTooManyRequests || p.Status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "2")
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

const maxJSONBody = 1 << 20

// decodeJSON reads a JSON request body into dst.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, strict bool) error {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil || mt != "application/json" {
			return errStatus(http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		}
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return err
		}
		return badRequest("bad_request", "", "invalid JSON body: %s", jsonErrorMessage(err))
	}
	if dec.More() {
		return badRequest("bad_request", "", "unexpected data after JSON body")
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1024))
	return nil
}

func jsonErrorMessage(err error) string {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		return fmt.Sprintf("field %q has the wrong type", te.Field)
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}
