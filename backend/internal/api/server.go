// Package api serves Syslogc's HTTP endpoints.
//
// Phase 1 provides operational endpoints (/health, /ready, /metrics) and a
// development-only search endpoint. The versioned, authenticated API with
// its route/permission table arrives in Phase 2.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/freezxp/syslogc/backend/internal/metrics"
	"github.com/freezxp/syslogc/backend/internal/storage"
)

// ReadinessCheck reports whether a component is ready; a non-nil error
// explains why not.
type ReadinessCheck struct {
	Name  string
	Check func(ctx context.Context) error
}

// Options configures the server.
type Options struct {
	Address           string
	ReadHeaderTimeout time.Duration
	IdleTimeout       time.Duration
	Version           string
	NodeID            string
	Metrics           *metrics.Metrics
	Log               *slog.Logger
	Checks            []ReadinessCheck
	// Querier enables the development search endpoint when non-nil.
	DevQuerier storage.LogQuerier
	// Extra returns additional JSON for /ready (e.g. source statuses).
	Extra func() map[string]any
}

// Server is the HTTP server.
type Server struct {
	opts     Options
	srv      *http.Server
	ln       net.Listener
	draining atomic.Bool
}

func New(opts Options) *Server {
	s := &Server{opts: opts}
	mux := http.NewServeMux()
	s.handle(mux, "GET /health", s.health)
	s.handle(mux, "GET /ready", s.ready)
	mux.Handle("GET /metrics", promhttp.HandlerFor(opts.Metrics.Registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
		Timeout:           10 * time.Second,
	}))
	if opts.DevQuerier != nil {
		s.handle(mux, "GET /api/v1/dev/search", s.devSearch)
	}
	s.srv = &http.Server{
		Handler:           secureHeaders(mux),
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
		IdleTimeout:       opts.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(opts.Log.Handler(), slog.LevelWarn),
	}
	return s
}

// handle registers h with request metrics and panic recovery.
func (s *Server) handle(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	m := s.opts.Metrics
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if v := recover(); v != nil {
				s.opts.Log.Error("panic in HTTP handler", "route", pattern, "panic", v)
				if !rec.wrote {
					writeJSON(rec, http.StatusInternalServerError, map[string]string{"error": "internal error"})
				}
			}
			m.HTTPRequests.WithLabelValues(pattern, r.Method, strconv.Itoa(rec.status)).Inc()
			m.HTTPDuration.WithLabelValues(pattern, r.Method).Observe(time.Since(start).Seconds())
		}()
		h(rec, r)
	})
}

// Listen binds the configured address.
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.opts.Address)
	if err != nil {
		return err
	}
	s.ln = ln
	return nil
}

// Addr returns the bound address (after Listen).
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Serve serves until Shutdown. It returns nil on graceful shutdown.
func (s *Server) Serve() error {
	s.opts.Log.Info("http server started", "address", s.ln.Addr().String())
	if err := s.srv.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// SetDraining makes /ready fail so load balancers stop sending traffic.
func (s *Server) SetDraining() { s.draining.Store(true) }

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error { return s.srv.Shutdown(ctx) }

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	type component struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	ok := !s.draining.Load()
	var components []component
	for _, c := range s.opts.Checks {
		comp := component{Name: c.Name, Status: "ok"}
		if err := c.Check(ctx); err != nil {
			ok = false
			comp.Status, comp.Error = "fail", err.Error()
		}
		components = append(components, comp)
	}
	body := map[string]any{
		"status":     "ready",
		"node":       s.opts.NodeID,
		"version":    s.opts.Version,
		"components": components,
	}
	if s.draining.Load() {
		body["status"] = "draining"
	} else if !ok {
		body["status"] = "not_ready"
	}
	if s.opts.Extra != nil {
		for k, v := range s.opts.Extra() {
			body[k] = v
		}
	}
	code := http.StatusOK
	if !ok {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
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
