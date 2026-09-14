// Package app is the composition root: it wires components for the
// configured roles and runs the ordered startup and graceful shutdown.
package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/freezxp/syslogc/backend/internal/api"
	"github.com/freezxp/syslogc/backend/internal/api/webui"
	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/config"
	"github.com/freezxp/syslogc/backend/internal/ingestion/pipeline"
	"github.com/freezxp/syslogc/backend/internal/ingestion/source"
	"github.com/freezxp/syslogc/backend/internal/ingestion/supervisor"
	"github.com/freezxp/syslogc/backend/internal/metadata"
	"github.com/freezxp/syslogc/backend/internal/metadata/postgres"
	"github.com/freezxp/syslogc/backend/internal/metrics"
	"github.com/freezxp/syslogc/backend/internal/normalization"
	"github.com/freezxp/syslogc/backend/internal/query"
	"github.com/freezxp/syslogc/backend/internal/storage"
	"github.com/freezxp/syslogc/backend/internal/storage/victorialogs"
)

// BuildInfo identifies the running binary.
type BuildInfo struct {
	Version string
	Commit  string
}

// saturationGrace is how long the ingest queue may stay saturated before
// readiness fails.
const saturationGrace = 30 * time.Second

// App is a wired, runnable Syslogc process.
type App struct {
	cfg     *config.Config
	log     *slog.Logger
	metrics *metrics.Metrics
	backend storage.Backend
	store   metadata.Store

	pipeline   *pipeline.Pipeline
	supervisor *supervisor.Supervisor
	server     *api.Server

	saturatedSince atomic.Int64
	retention      atomic.Pointer[retentionStatus]
}

type retentionStatus struct {
	Configured string `json:"configured"`
	Backend    string `json:"backend,omitempty"`
	Status     string `json:"status"` // in_sync | drift | unknown
	Error      string `json:"error,omitempty"`
}

// New wires the application. It connects to PostgreSQL (applying
// migrations) when configured; listeners start in Run.
func New(ctx context.Context, cfg *config.Config, info BuildInfo, log *slog.Logger) (*App, error) {
	a := &App{cfg: cfg, log: log, metrics: metrics.New(info.Version, info.Commit)}

	vl := cfg.Storage.VictoriaLogs
	backend, err := victorialogs.New(victorialogs.Config{
		InsertURL:         vl.InsertURL,
		SelectURL:         vl.SelectURL,
		StreamFields:      vl.StreamFields,
		WriteTimeout:      vl.WriteTimeout.D(),
		QueryTimeout:      vl.QueryTimeout.D(),
		Compression:       vl.Compression,
		BasicUsername:     vl.BasicUsername,
		BasicPasswordFile: vl.BasicPasswordFile,
		BearerTokenFile:   vl.BearerTokenFile,
		MaxConnsPerHost:   max(cfg.Ingestion.Writers*2, 32),
	})
	if err != nil {
		return nil, err
	}
	a.backend = backend
	a.retention.Store(&retentionStatus{Configured: cfg.Retention.Period.String(), Status: "unknown"})

	dsn, err := postgresDSN(cfg.Metadata.Postgres)
	if err != nil {
		return nil, err
	}
	if dsn != "" {
		store, err := postgres.Open(ctx, dsn, cfg.Metadata.Postgres.MaxConns, log.With("component", "metadata"))
		if err != nil {
			return nil, err
		}
		a.store = store
	}

	checks := []api.ReadinessCheck{{Name: "storage", Check: backend.Ping}}
	if a.store != nil {
		checks = append(checks, api.ReadinessCheck{Name: "database", Check: a.store.Ping})
	}
	opts := api.Options{
		Address:           cfg.Server.HTTP.Address,
		ReadHeaderTimeout: cfg.Server.HTTP.ReadHeaderTimeout.D(),
		IdleTimeout:       cfg.Server.HTTP.IdleTimeout.D(),
		Version:           info.Version,
		NodeID:            cfg.Node.ID,
		Roles:             cfg.Node.Roles,
		StartedAt:         time.Now(),
		Metrics:           a.metrics,
		Log:               log.With("component", "http"),
		Extra:             a.readyExtra,
	}

	if cfg.Node.HasRole(config.RoleIngest) {
		a.wireIngest()
		checks = append(checks,
			api.ReadinessCheck{Name: "sources", Check: func(context.Context) error {
				if !a.supervisor.Ready() {
					return errors.New("no syslog source could be started")
				}
				return nil
			}},
			api.ReadinessCheck{Name: "ingest_queue", Check: func(context.Context) error {
				if since := a.saturatedSince.Load(); since != 0 && time.Since(time.Unix(0, since)) > saturationGrace {
					return errors.New("ingest queue saturated")
				}
				return nil
			}},
		)
		if a.store != nil {
			opts.Ingest = &api.IngestDeps{
				Sink:           a.pipeline,
				Source:         func() *source.Settings { return a.supervisor.HTTPSource() },
				MaxBodyBytes:   int64(cfg.Ingestion.HTTP.MaxBodyBytes),
				MaxEvents:      cfg.Ingestion.HTTP.MaxEvents,
				EnqueueTimeout: cfg.Ingestion.HTTP.EnqueueTimeout.D(),
			}
		}
	}

	if a.store != nil {
		deps, err := a.wireAPI(ctx)
		if err != nil {
			a.store.Close()
			return nil, err
		}
		if cfg.Node.HasRole(config.RoleAPI) {
			opts.API = deps
		} else if opts.Ingest != nil {
			// Ingest-only nodes authenticate API keys for HTTP ingestion only.
			opts.API = &api.APIDeps{Auth: deps.Auth, Store: a.store}
			opts.IngestOnly = true
		}
	}
	opts.Checks = checks
	a.server = api.New(opts)
	return a, nil
}

func (a *App) wireIngest() {
	cfg := a.cfg
	in := cfg.Ingestion
	workers := in.ParseWorkers
	if workers == 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	maxPast := in.Time.MaxPastAge.D()
	if maxPast == 0 {
		maxPast = cfg.Retention.Period.D() - 24*time.Hour
	}
	a.pipeline = pipeline.New(pipeline.Config{
		QueueMaxMessages: in.Queue.MaxMessages,
		QueueMaxBytes:    int64(in.Queue.MaxBytes),
		Workers:          workers,
		BatchMaxRows:     in.Batch.MaxRows,
		BatchMaxBytes:    in.Batch.MaxBytes.Int(),
		BatchMaxWait:     in.Batch.MaxWait.D(),
		Writers:          in.Writers,
		BatchQueue:       in.BatchQueue,
		InitialBackoff:   in.Retry.InitialBackoff.D(),
		MaxBackoff:       in.Retry.MaxBackoff.D(),
		Normalization: normalization.Options{
			MaxFutureSkew:      in.Time.MaxFutureSkew.D(),
			MaxPastAge:         maxPast,
			MaxFields:          in.Limits.MaxFields,
			MaxFieldValueBytes: in.Limits.MaxFieldValueBytes.Int(),
			MaxFieldNameBytes:  in.Limits.MaxFieldNameBytes,
		},
	}, a.backend.Writer(), a.backend.Name(), a.metrics, a.log.With("component", "pipeline"))
	a.supervisor = supervisor.New(a.pipeline, a.metrics, a.log.With("component", "ingestion"))
}

func (a *App) wireAPI(ctx context.Context) (*api.APIDeps, error) {
	cfg := a.cfg
	authSvc := auth.NewService(a.store, auth.Config{
		SessionTTL:         cfg.Auth.SessionTTL.D(),
		SessionIdleTimeout: cfg.Auth.SessionIdleTimeout.D(),
	}, a.log.With("component", "auth"))

	if cfg.Node.HasRole(config.RoleAPI) {
		generated, err := authSvc.Bootstrap(ctx, cfg.Auth.BootstrapAdmin.Username, cfg.Auth.BootstrapAdmin.PasswordFile)
		if err != nil {
			return nil, err
		}
		if generated != "" {
			fmt.Fprintf(os.Stderr, "\n"+
				"==================================================================\n"+
				"  Syslogc created the initial administrator account.\n"+
				"  Username: %s\n"+
				"  Password: %s\n"+
				"  You must change this password at first login.\n"+
				"==================================================================\n\n",
				cfg.Auth.BootstrapAdmin.Username, generated)
		}
	}

	key, err := loadSecretKey(cfg.Auth.SecretKeyFile)
	if err != nil {
		return nil, err
	}
	if cfg.Auth.SecretKeyFile == "" {
		a.log.Warn("auth.secret_key_file not set: using a random key; pagination cursors will not survive restarts or work across API nodes")
	}
	querySvc := query.NewService(query.Options{
		Querier:      a.backend.Querier(),
		Admin:        a.backend.Admin(),
		NodeStats:    a.store,
		CursorKey:    key,
		Limits:       query.DefaultLimits(cfg.Retention.Period.D()),
		MaxTieGroup:  cfg.Query.MaxTieGroup,
		MaxTailTotal: cfg.Query.MaxTailSessions,
		Log:          a.log.With("component", "query"),
	})
	deps := &api.APIDeps{
		Auth: authSvc, Store: a.store, Query: querySvc, Storage: a.backend,
		Retention:    func() any { return a.retention.Load() },
		CookieSecure: cfg.Auth.CookieSecure,
		AuditAll:     cfg.Query.AuditAll,
		WebUI:        webui.FS(),
	}
	if a.supervisor != nil {
		deps.Sources = a.supervisor.Statuses
		deps.Queue = func() api.QueueInfo {
			q := a.pipeline.QueueStats()
			return api.QueueInfo{Messages: q.Messages, Bytes: q.Bytes, CapacityMessages: q.CapacityMessages, CapacityBytes: q.CapacityBytes}
		}
	}
	return deps, nil
}

// postgresDSN returns the DSN; an inline dsn takes precedence over dsn_file.
func postgresDSN(c config.PostgresConfig) (string, error) {
	if c.DSN != "" || c.DSNFile == "" {
		return c.DSN, nil
	}
	data, err := os.ReadFile(c.DSNFile)
	if err != nil {
		return "", fmt.Errorf("metadata.postgres.dsn_file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func loadSecretKey(path string) ([]byte, error) {
	if path == "" {
		key := make([]byte, 32)
		_, err := rand.Read(key)
		return key, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // operator-provided path
	if err != nil {
		return nil, fmt.Errorf("auth.secret_key_file: %w", err)
	}
	if len(data) < 32 {
		return nil, errors.New("auth.secret_key_file must contain at least 32 bytes")
	}
	return data, nil
}

func (a *App) readyExtra() map[string]any {
	extra := map[string]any{"roles": a.cfg.Node.Roles, "retention": a.retention.Load()}
	if a.supervisor != nil {
		extra["sources"] = a.supervisor.Statuses()
	}
	return extra
}

// Server exposes the HTTP server (for tests needing its address).
func (a *App) Server() *api.Server { return a.server }

// Supervisor exposes the source supervisor; nil without the ingest role.
func (a *App) Supervisor() *supervisor.Supervisor { return a.supervisor }

// Store exposes the metadata store; nil when not configured.
func (a *App) Store() metadata.Store { return a.store }

// Run starts all components and blocks until ctx is cancelled, then shuts
// down gracefully. It returns an error if startup fails.
func (a *App) Run(ctx context.Context, ready func()) error {
	if err := a.server.Listen(); err != nil {
		a.closeResources()
		return fmt.Errorf("http server: %w", err)
	}
	if a.pipeline != nil {
		a.pipeline.Start()
		a.supervisor.Start(a.cfg.Ingestion.Sources)
	}

	bgCtx, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()
	go a.watchRetention(bgCtx)
	go a.watchStorage(bgCtx)
	if a.pipeline != nil {
		go a.watchSaturation(bgCtx)
		if a.store != nil {
			go a.writeNodeStats(bgCtx)
		}
	}
	if a.store != nil {
		go a.maintenance(bgCtx)
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- a.server.Serve() }()
	a.log.Info("syslogc started", "roles", a.cfg.Node.Roles)
	if ready != nil {
		ready()
	}

	var runErr error
	select {
	case <-ctx.Done():
		a.log.Info("shutdown requested")
	case err := <-serveErr:
		runErr = fmt.Errorf("http server: %w", err)
	}
	stopBackground()
	a.shutdown()
	return runErr
}

// shutdown follows docs/ingestion.md §8: fail readiness, let balancers
// notice, stop listeners, drain the pipeline, then stop HTTP and storage.
func (a *App) shutdown() {
	a.server.SetDraining()
	if d := a.cfg.Shutdown.DrainDelay.D(); d > 0 {
		a.log.Info("draining before shutdown", "delay", d.String())
		time.Sleep(d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.cfg.Shutdown.Timeout.D())
	defer cancel()

	if a.supervisor != nil {
		a.supervisor.Stop(ctx)
	}
	if a.pipeline != nil {
		if err := a.pipeline.Close(ctx); err != nil {
			a.log.Warn("pipeline did not drain before shutdown timeout; remaining logs counted as dropped", "error", err)
		}
	}
	httpCtx, httpCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer httpCancel()
	if err := a.server.Shutdown(httpCtx); err != nil {
		a.log.Warn("http server shutdown", "error", err)
	}
	if a.pipeline != nil && a.store != nil {
		a.snapshotNodeStats(context.Background())
	}
	a.closeResources()
	a.log.Info("shutdown complete")
}

func (a *App) closeResources() {
	_ = a.backend.Close()
	if a.store != nil {
		a.store.Close()
	}
}

func (a *App) watchSaturation(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if a.pipeline.Saturated() {
				a.saturatedSince.CompareAndSwap(0, time.Now().UnixNano())
			} else {
				a.saturatedSince.Store(0)
			}
		}
	}
}

// watchStorage pings the backend every 5 seconds. Write-based health alone
// misses outages where requests hang instead of failing.
func (a *App) watchStorage(ctx context.Context) {
	gauge := a.metrics.StorageReachable.WithLabelValues(a.backend.Name())
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if err := a.backend.Ping(pctx); err != nil {
			gauge.Set(0)
		} else {
			gauge.Set(1)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// nodeStatsInterval is how often ingestion counters are snapshotted.
const nodeStatsInterval = 10 * time.Second

// writeNodeStats persists counter snapshots for dashboard rate charts.
func (a *App) writeNodeStats(ctx context.Context) {
	t := time.NewTicker(nodeStatsInterval)
	defer t.Stop()
	a.snapshotNodeStats(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.snapshotNodeStats(ctx)
		}
	}
}

func (a *App) snapshotNodeStats(ctx context.Context) {
	snap, err := a.metrics.Snapshot()
	if err != nil {
		return
	}
	sum := snap.Sum()
	var dropped int64
	for _, v := range sum.Dropped {
		dropped += v
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = a.store.InsertNodeStats(ctx, &metadata.NodeStats{
		NodeID: a.cfg.Node.ID, Time: time.Now().UTC().Truncate(time.Second),
		Received: sum.Received, Parsed: sum.Parsed, ParseErrors: sum.ParseErrors, Stored: sum.Stored,
		Dropped: dropped, BytesReceived: sum.BytesReceived, BytesStored: snap.BytesStored,
	})
	if err != nil && ctx.Err() == nil {
		a.log.Warn("node statistics not stored", "error", err)
	}
}

// maintenance prunes expired sessions, old node statistics and audit events.
func (a *App) maintenance(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	run := func() {
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		now := time.Now().UTC()
		if _, err := a.store.DeleteExpiredSessions(cctx, now); err != nil && cctx.Err() == nil {
			a.log.Warn("session cleanup failed", "error", err)
		}
		if _, err := a.store.DeleteNodeStatsBefore(cctx, now.Add(-25*time.Hour)); err != nil && cctx.Err() == nil {
			a.log.Warn("node statistics cleanup failed", "error", err)
		}
		if _, err := a.store.DeleteAuditEventsBefore(cctx, now.AddDate(0, 0, -400)); err != nil && cctx.Err() == nil {
			a.log.Warn("audit cleanup failed", "error", err)
		}
	}
	run()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// watchRetention compares configured retention with the backend's effective
// retention at startup and every 10 minutes.
func (a *App) watchRetention(ctx context.Context) {
	check := func() {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		st := &retentionStatus{Configured: a.cfg.Retention.Period.String()}
		info, err := a.backend.Admin().Retention(cctx)
		switch {
		case err != nil:
			st.Status, st.Error = "unknown", err.Error()
		case info.Period == a.cfg.Retention.Period.D():
			st.Status, st.Backend = "in_sync", config.Duration(info.Period).String()
		default:
			st.Status, st.Backend = "drift", config.Duration(info.Period).String()
			if prev := a.retention.Load(); prev == nil || prev.Status != "drift" {
				a.log.Warn("retention drift: configured retention differs from storage backend",
					"configured", st.Configured, "backend", st.Backend)
			}
		}
		a.retention.Store(st)
	}
	check()
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	retry := time.NewTimer(15 * time.Second)
	defer retry.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-retry.C:
			if a.retention.Load().Status == "unknown" {
				check()
				retry.Reset(15 * time.Second)
			}
		case <-t.C:
			check()
		}
	}
}
