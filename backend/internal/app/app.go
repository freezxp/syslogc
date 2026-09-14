// Package app is the composition root: it wires components for the
// configured roles and runs the ordered startup and graceful shutdown.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/freezxp/syslogc/backend/internal/api"
	"github.com/freezxp/syslogc/backend/internal/config"
	"github.com/freezxp/syslogc/backend/internal/ingestion/pipeline"
	"github.com/freezxp/syslogc/backend/internal/ingestion/supervisor"
	"github.com/freezxp/syslogc/backend/internal/metrics"
	"github.com/freezxp/syslogc/backend/internal/normalization"
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

// New wires the application. Nothing is started until Run.
func New(cfg *config.Config, info BuildInfo, log *slog.Logger) (*App, error) {
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
		MaxConnsPerHost:   max(cfg.Ingestion.Writers*2, 16),
	})
	if err != nil {
		return nil, err
	}
	a.backend = backend
	a.retention.Store(&retentionStatus{Configured: cfg.Retention.Period.String(), Status: "unknown"})

	checks := []api.ReadinessCheck{{Name: "storage", Check: backend.Ping}}

	if cfg.Node.HasRole(config.RoleIngest) {
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
		}, backend.Writer(), backend.Name(), a.metrics, log.With("component", "pipeline"))
		a.supervisor = supervisor.New(a.pipeline, a.metrics, log.With("component", "ingestion"))
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
	}

	var devQuerier storage.LogQuerier
	if cfg.Node.HasRole(config.RoleAPI) && cfg.Dev.SearchEndpoint {
		devQuerier = backend.Querier()
		log.Warn("development search endpoint enabled: GET /api/v1/dev/search is UNAUTHENTICATED; do not expose this node")
	}

	a.server = api.New(api.Options{
		Address:           cfg.Server.HTTP.Address,
		ReadHeaderTimeout: cfg.Server.HTTP.ReadHeaderTimeout.D(),
		IdleTimeout:       cfg.Server.HTTP.IdleTimeout.D(),
		Version:           info.Version,
		NodeID:            cfg.Node.ID,
		Metrics:           a.metrics,
		Log:               log.With("component", "http"),
		Checks:            checks,
		DevQuerier:        devQuerier,
		Extra:             a.readyExtra,
	})
	return a, nil
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

// Run starts all components and blocks until ctx is cancelled, then shuts
// down gracefully. It returns an error if startup fails.
func (a *App) Run(ctx context.Context, ready func()) error {
	if err := a.server.Listen(); err != nil {
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
	_ = a.backend.Close()
	a.log.Info("shutdown complete")
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
