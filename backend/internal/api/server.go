// Package api serves Syslogc's HTTP interface: operational endpoints
// (/health, /ready, /metrics), the versioned REST API under /api/v1 and the
// embedded web UI. Every API route declares the permission it requires in a
// single route table (routes.go); handlers never check roles.
package api

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/config"
	"github.com/freezxp/syslogc/backend/internal/ingestion/listener"
	"github.com/freezxp/syslogc/backend/internal/ingestion/source"
	"github.com/freezxp/syslogc/backend/internal/ingestion/supervisor"
	"github.com/freezxp/syslogc/backend/internal/metadata"
	"github.com/freezxp/syslogc/backend/internal/metrics"
	"github.com/freezxp/syslogc/backend/internal/query"
	"github.com/freezxp/syslogc/backend/internal/storage"
)

// ReadinessCheck reports whether a component is ready; a non-nil error
// explains why not.
type ReadinessCheck struct {
	Name  string
	Check func(ctx context.Context) error
}

// APIDeps are the dependencies of the REST API (api role).
type APIDeps struct {
	Auth    *auth.Service
	Store   metadata.Store
	Query   *query.Service
	Storage storage.Backend
	// Retention returns the current retention status (for /system/storage).
	Retention func() any
	// Sources returns source statuses; nil without the ingest role.
	Sources func() []supervisor.Status
	// FileSources are the sources defined in the configuration file; they
	// are read-only and take precedence over database-managed ones.
	FileSources []config.Source
	// Config is the effective configuration, served with secrets redacted.
	Config config.Config
	// Queue returns ingest queue occupancy; nil without the ingest role.
	Queue        func() QueueInfo
	CookieSecure bool
	AuditAll     bool
	// WebUI is the built frontend; nil or empty disables UI serving.
	WebUI fs.FS
}

// IngestDeps are the dependencies of HTTP ingestion (ingest role).
type IngestDeps struct {
	Sink           listener.Sink
	Source         func() *source.Settings
	MaxBodyBytes   int64
	MaxEvents      int
	EnqueueTimeout time.Duration
}

// QueueInfo describes ingest queue occupancy.
type QueueInfo struct {
	Messages         int   `json:"messages"`
	Bytes            int64 `json:"bytes"`
	CapacityMessages int   `json:"capacity_messages"`
	CapacityBytes    int64 `json:"capacity_bytes"`
}

// Options configures the server.
type Options struct {
	Address           string
	ReadHeaderTimeout time.Duration
	IdleTimeout       time.Duration
	Version           string
	NodeID            string
	Roles             []string
	StartedAt         time.Time
	Metrics           *metrics.Metrics
	Log               *slog.Logger
	Checks            []ReadinessCheck
	// Extra returns additional JSON for /ready (e.g. source statuses).
	Extra  func() map[string]any
	API    *APIDeps
	Ingest *IngestDeps
	// IngestOnly restricts API to key authentication for HTTP ingestion.
	IngestOnly bool
	// AllowedOrigins are extra accepted Origin values (lower-case scheme://host).
	AllowedOrigins []string
	// TrustedProxies may set X-Forwarded-For.
	TrustedProxies []netip.Prefix
}

// Server is the HTTP server.
type Server struct {
	opts     Options
	srv      *http.Server
	ln       net.Listener
	draining atomic.Bool
	log      *slog.Logger
	rates    rateTracker
	stop     chan struct{}
	stopOnce sync.Once
}

func New(opts Options) *Server {
	if opts.StartedAt.IsZero() {
		opts.StartedAt = time.Now()
	}
	s := &Server{opts: opts, log: opts.Log, stop: make(chan struct{})}
	mux := http.NewServeMux()
	s.register(mux)
	mux.Handle("GET /metrics", promhttp.HandlerFor(opts.Metrics.Registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
		Timeout:           10 * time.Second,
	}))
	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
		IdleTimeout:       opts.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(opts.Log.Handler(), slog.LevelWarn),
	}
	return s
}

// Handler returns the root handler (for tests).
func (s *Server) Handler() http.Handler { return s.srv.Handler }

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
	s.log.Info("http server started", "address", s.ln.Addr().String())
	go s.sampleRates()
	if err := s.srv.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// SetDraining makes /ready fail so load balancers stop sending traffic.
func (s *Server) SetDraining() { s.draining.Store(true) }

// Shutdown gracefully stops the server. Long-lived streams (live tail) are
// cancelled via their request contexts.
func (s *Server) Shutdown(ctx context.Context) error {
	s.stopOnce.Do(func() { close(s.stop) })
	return s.srv.Shutdown(ctx)
}

type componentStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// readiness runs all checks.
func (s *Server) readiness(ctx context.Context) (status string, components []componentStatus, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ok = !s.draining.Load()
	for _, c := range s.opts.Checks {
		comp := componentStatus{Name: c.Name, Status: "ok"}
		if err := c.Check(ctx); err != nil {
			ok = false
			comp.Status, comp.Error = "fail", err.Error()
		}
		components = append(components, comp)
	}
	switch {
	case s.draining.Load():
		status = "draining"
	case !ok:
		status = "not_ready"
	default:
		status = "ready"
	}
	return status, components, ok
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	status, components, ok := s.readiness(r.Context())
	body := map[string]any{"status": status, "node": s.opts.NodeID, "version": s.opts.Version, "components": components}
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
