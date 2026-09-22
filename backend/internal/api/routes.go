package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/freezxp/syslogc/backend/internal/auth"
)

// access describes who may call a route.
type access int

const (
	// public routes need no authentication.
	public access = iota
	// authenticated routes need a valid principal but no permission (used
	// for identity endpoints that must work before a forced password change).
	authenticated
	// permitted routes need a principal holding route.perm.
	permitted
)

type handlerFunc func(w http.ResponseWriter, r *http.Request, p *auth.Principal) error

type route struct {
	pattern string
	access  access
	perm    auth.Permission
	handler handlerFunc
	// raw disables the JSON-API security headers (web UI assets).
	raw bool
}

// routes is the single authorization table of the API.
func (s *Server) routes() []route {
	var rs []route
	add := func(pattern string, a access, perm auth.Permission, h handlerFunc) {
		rs = append(rs, route{pattern: pattern, access: a, perm: perm, handler: h})
	}
	add("GET /health", public, "", func(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
		s.handleHealth(w, r)
		return nil
	})
	add("GET /ready", public, "", func(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
		s.handleReady(w, r)
		return nil
	})

	if s.opts.Ingest != nil {
		add("POST /api/v1/ingest", permitted, auth.PermLogsIngest, s.handleIngest)
	}
	if s.opts.API == nil || s.opts.IngestOnly {
		return rs
	}

	add("POST /api/v1/auth/login", public, "", s.handleLogin)
	add("POST /api/v1/auth/logout", authenticated, "", s.handleLogout)
	add("GET /api/v1/auth/me", authenticated, "", s.handleMe)
	add("PUT /api/v1/auth/me/password", authenticated, "", s.handleChangePassword)

	add("POST /api/v1/logs/search", permitted, auth.PermLogsSearch, s.handleSearch)
	add("POST /api/v1/logs/histogram", permitted, auth.PermLogsSearch, s.handleHistogram)
	add("POST /api/v1/logs/facets", permitted, auth.PermLogsSearch, s.handleFacets)
	add("POST /api/v1/logs/stats", permitted, auth.PermLogsSearch, s.handleStats)
	add("GET /api/v1/logs/tail", permitted, auth.PermLogsTail, s.handleTail)
	add("POST /api/v1/logs/export", permitted, auth.PermLogsExport, s.handleExport)
	add("POST /api/v1/query/validate", permitted, auth.PermLogsSearch, s.handleValidate)
	add("POST /api/v1/fields", permitted, auth.PermLogsSearch, s.handleFields)
	add("POST /api/v1/fields/{field}/values", permitted, auth.PermLogsSearch, s.handleFieldValues)

	add("POST /api/v1/analytics/breakdown", permitted, auth.PermLogsSearch, s.handleBreakdown)
	add("POST /api/v1/analytics/series", permitted, auth.PermLogsSearch, s.handleSeries)

	add("POST /api/v1/dashboard/overview", permitted, auth.PermDashboardView, s.handleOverview)
	add("POST /api/v1/dashboard/volume", permitted, auth.PermDashboardView, s.handleVolume)
	add("POST /api/v1/dashboard/top", permitted, auth.PermDashboardView, s.handleTop)
	add("POST /api/v1/dashboard/ingestion-rate", permitted, auth.PermDashboardView, s.handleIngestionRate)

	add("GET /api/v1/saved-searches", permitted, auth.PermSearchesRead, s.handleListSearches)
	add("POST /api/v1/saved-searches", permitted, auth.PermSearchesWrite, s.handleCreateSearch)
	add("GET /api/v1/saved-searches/{id}", permitted, auth.PermSearchesRead, s.handleGetSearch)
	add("PUT /api/v1/saved-searches/{id}", permitted, auth.PermSearchesWrite, s.handleUpdateSearch)
	add("DELETE /api/v1/saved-searches/{id}", permitted, auth.PermSearchesWrite, s.handleDeleteSearch)

	add("GET /api/v1/api-keys", permitted, auth.PermAPIKeysOwn, s.handleListAPIKeys)
	add("POST /api/v1/api-keys", permitted, auth.PermAPIKeysOwn, s.handleCreateAPIKey)
	add("DELETE /api/v1/api-keys/{id}", permitted, auth.PermAPIKeysOwn, s.handleRevokeAPIKey)

	add("GET /api/v1/sources", permitted, auth.PermSourcesRead, s.handleListSources)
	add("POST /api/v1/sources", permitted, auth.PermSourcesManage, s.handleCreateSource)
	add("POST /api/v1/sources/test-extract", permitted, auth.PermSourcesManage, s.handleTestExtract)
	add("GET /api/v1/sources/{id}", permitted, auth.PermSourcesRead, s.handleGetSource)
	add("PUT /api/v1/sources/{id}", permitted, auth.PermSourcesManage, s.handleUpdateSource)
	add("DELETE /api/v1/sources/{id}", permitted, auth.PermSourcesManage, s.handleDeleteSource)

	add("GET /api/v1/users", permitted, auth.PermUsersManage, s.handleListUsers)
	add("POST /api/v1/users", permitted, auth.PermUsersManage, s.handleCreateUser)
	add("PUT /api/v1/users/{id}", permitted, auth.PermUsersManage, s.handleUpdateUser)
	add("DELETE /api/v1/users/{id}", permitted, auth.PermUsersManage, s.handleDeleteUser)
	add("POST /api/v1/users/{id}/revoke-sessions", permitted, auth.PermUsersManage, s.handleRevokeUserSessions)

	add("GET /api/v1/audit", permitted, auth.PermAuditView, s.handleListAudit)

	add("GET /api/v1/system/health", permitted, auth.PermSystemView, s.handleSystemHealth)
	add("GET /api/v1/system/ingestion", permitted, auth.PermSystemView, s.handleSystemIngestion)
	add("GET /api/v1/system/storage", permitted, auth.PermSystemView, s.handleSystemStorage)
	add("GET /api/v1/system/config", permitted, auth.PermConfigView, s.handleSystemConfig)
	add("GET /api/v1/system/retention", permitted, auth.PermSystemView, s.handleSystemRetention)
	add("PUT /api/v1/system/retention", permitted, auth.PermRetentionManage, s.handleSetRetention)

	// Fallback: JSON 404 for unknown API paths, otherwise the web UI.
	rs = append(rs, route{pattern: "/", access: public, handler: s.handleFallback, raw: true})
	return rs
}

// register installs every route with the middleware chain.
func (s *Server) register(mux *http.ServeMux) {
	for _, rt := range s.routes() {
		if rt.access == permitted && rt.perm == "" {
			panic(fmt.Sprintf("route %s requires a permission", rt.pattern))
		}
		mux.Handle(rt.pattern, s.wrap(rt))
	}
}

type ctxKey int

const requestIDKey ctxKey = iota

func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// wrap applies: request ID → recovery → metrics/access log → security
// headers → authentication → CSRF → authorization → handler.
func (s *Server) wrap(rt route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" || len(reqID) > 64 || strings.ContainsAny(reqID, "\r\n") {
			reqID = newRequestID()
		}
		w.Header().Set("X-Request-ID", reqID)
		ctx := context.WithValue(r.Context(), requestIDKey, reqID)
		r = r.WithContext(ctx)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic in HTTP handler", "route", rt.pattern, "request_id", reqID, "panic", fmt.Sprint(v))
				if !rec.wrote {
					s.writeError(rec, r, fmt.Errorf("panic: %v", v))
				}
			}
			m := s.opts.Metrics
			m.HTTPRequests.WithLabelValues(rt.pattern, r.Method, strconv.Itoa(rec.status)).Inc()
			m.HTTPDuration.WithLabelValues(rt.pattern, r.Method).Observe(time.Since(start).Seconds())
			if rt.pattern != "GET /health" && rt.pattern != "GET /ready" && !rt.raw {
				s.log.Debug("http request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
					"duration_ms", time.Since(start).Milliseconds(), "request_id", reqID)
			}
		}()

		securityHeaders(rec, rt.raw, r.TLS != nil, s.secureOrigin(r))

		var p *auth.Principal
		if rt.access != public {
			var err error
			if p, err = s.authenticate(r); err != nil {
				s.writeError(rec, r, err)
				return
			}
			if p.Kind == auth.KindUser && !safeMethod(r.Method) {
				if err := s.checkCSRF(r, p); err != nil {
					s.writeError(rec, r, err)
					return
				}
			}
			if rt.access == permitted && !p.Can(rt.perm) {
				detail := fmt.Sprintf("this action requires the %s permission", rt.perm)
				if p.User != nil && p.User.MustChangePassword {
					detail = "you must change your password before continuing"
				}
				s.writeError(rec, r, errStatus(http.StatusForbidden, "forbidden", "%s", detail))
				return
			}
			r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		} else if !safeMethod(r.Method) && !s.sameOrigin(r) && rt.pattern != "/" {
			s.writeError(rec, r, errStatus(http.StatusForbidden, "csrf_failed", "cross-origin request rejected"))
			return
		}
		if err := rt.handler(rec, r, p); err != nil {
			if rec.wrote {
				s.log.Warn("error after response started", "route", rt.pattern, "request_id", reqID, "error", err)
				return
			}
			s.writeError(rec, r, err)
		}
	})
}

const sessionCookie = "slc_session"

// authenticate resolves the principal from a bearer API key or session cookie.
func (s *Server) authenticate(r *http.Request) (*auth.Principal, error) {
	if h := r.Header.Get("Authorization"); h != "" {
		token, ok := strings.CutPrefix(h, "Bearer ")
		if !ok || s.opts.API == nil && s.opts.Ingest == nil {
			return nil, auth.ErrUnauthenticated
		}
		svc := s.authService()
		if svc == nil {
			return nil, auth.ErrUnauthenticated
		}
		return svc.AuthenticateAPIKey(r.Context(), strings.TrimSpace(token))
	}
	svc := s.authService()
	c, err := r.Cookie(sessionCookie)
	if err != nil || svc == nil {
		return nil, auth.ErrUnauthenticated
	}
	return svc.AuthenticateSession(r.Context(), c.Value)
}

func (s *Server) authService() *auth.Service {
	if s.opts.API != nil {
		return s.opts.API.Auth
	}
	return nil
}

// checkCSRF enforces the synchronizer token and same-origin checks for
// cookie-authenticated unsafe requests.
func (s *Server) checkCSRF(r *http.Request, p *auth.Principal) error {
	if !s.sameOrigin(r) {
		return errStatus(http.StatusForbidden, "csrf_failed", "cross-origin request rejected")
	}
	token := r.Header.Get("X-CSRF-Token")
	if token == "" {
		// HTML form fallback (streamed export downloads).
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "application/x-www-form-urlencoded" {
			r.Body = http.MaxBytesReader(nil, r.Body, maxJSONBody)
			if err := r.ParseForm(); err == nil {
				token = r.PostForm.Get("csrf_token")
			}
		}
	}
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(p.CSRFToken)) != 1 {
		return errStatus(http.StatusForbidden, "csrf_failed", "missing or invalid CSRF token")
	}
	return nil
}

// sameOrigin rejects browser requests whose Sec-Fetch-Site or Origin show a
// different site. Requests without these headers (non-browser clients) pass.
func (s *Server) sameOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	for _, allowed := range s.opts.AllowedOrigins {
		if strings.EqualFold(allowed, u.Scheme+"://"+u.Host) {
			return true
		}
	}
	return false
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// securityHeaders sets response hardening headers. tls means this server
// terminated TLS; secure means the browser sees an HTTPS origin (directly or
// through a trusted proxy). Browsers ignore and warn about COOP otherwise.
func securityHeaders(w http.ResponseWriter, ui, tls, secure bool) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	if secure {
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
	}
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if ui {
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	} else {
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	}
	if tls {
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// clientIP returns the request's remote IP (proxies are not trusted yet).
// clientIP returns the peer address, or with a trusted proxy peer the
// right-most untrusted address in X-Forwarded-For.
func (s *Server) clientIP(r *http.Request) string {
	if a := s.clientAddr(r); a.IsValid() {
		return a.String()
	}
	return r.RemoteAddr
}

func (s *Server) clientAddr(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	addr := ap.Addr().Unmap()
	if !s.trusted(addr) {
		return addr
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		addr = hop.Unmap()
		if !s.trusted(addr) {
			break
		}
	}
	return addr
}

// secureOrigin reports whether the client reached us over HTTPS.
func (s *Server) secureOrigin(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	return err == nil && s.trusted(ap.Addr().Unmap()) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) trusted(a netip.Addr) bool {
	for _, p := range s.opts.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// audit records a security-relevant action; failures are logged, not returned.
func (s *Server) audit(r *http.Request, p *auth.Principal, action, outcome string, details map[string]any) {
	attrs := []any{"component", "audit", "action", action, "outcome", outcome, "request_id", requestID(r.Context()), "ip", s.clientIP(r)}
	if p != nil {
		attrs = append(attrs, "actor", p.Username)
	}
	s.log.Log(r.Context(), slog.LevelInfo, "audit", append(attrs, "details", details)...)
	if s.opts.API == nil {
		return
	}
	ev := newAuditEvent(r, s.clientIP(r), p, action, outcome, details)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
	defer cancel()
	if err := s.opts.API.Store.InsertAuditEvent(ctx, ev); err != nil {
		s.log.Warn("audit event not stored", "action", action, "error", err)
	}
}
