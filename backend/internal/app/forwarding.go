package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/freezxp/syslogc/backend/internal/config"
	"github.com/freezxp/syslogc/backend/internal/forwarding"
	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/normalization"
	"github.com/freezxp/syslogc/backend/internal/storage/victorialogs"
)

// forwardReconcileInterval bounds how long a change takes to apply when the
// database notification is missed.
const forwardReconcileInterval = 5 * time.Second

// buildForwarder opens a client for one target and wraps it in a forwarder.
//
// Each target is its own storage client: a slow or broken remote can only
// fill its own bounded queue, never the local write path.
func (a *App) buildForwarder(t forwarding.Target) (*forwarding.Forwarder, error) {
	cfg, ok := t.Options.Config.(config.ForwardTarget)
	if !ok {
		return nil, fmt.Errorf("forward target %s: no configuration", t.Options.Name)
	}
	remote, err := victorialogs.New(victorialogs.Config{
		InsertURL:         cfg.URL,
		SelectURL:         cfg.URL,
		StreamFields:      cfg.StreamFields,
		WriteTimeout:      cfg.WriteTimeout.D(),
		QueryTimeout:      cfg.WriteTimeout.D(),
		Compression:       cfg.Compression,
		BasicUsername:     cfg.BasicUsername,
		BasicPasswordFile: cfg.BasicPasswordFile,
		BearerTokenFile:   cfg.BearerTokenFile,
		BearerToken:       cfg.BearerToken,
		MaxConnsPerHost:   8,
	})
	if err != nil {
		return nil, fmt.Errorf("forward target %s: %w", cfg.Name, err)
	}
	opts := t.Options
	opts.Writer = remote.Writer()
	opts.Metrics = a.metrics
	opts.Log = a.log.With("component", "forwarding")
	a.closers = append(a.closers, remote)
	return forwarding.New(opts), nil
}

// forwardOptions turns a configured target into the forwarder's settings.
func forwardOptions(t config.ForwardTarget) (forwarding.Options, error) {
	var minSeverity *logentry.Severity
	if t.MinSeverity != "" {
		sev, ok := normalization.ParseSeverity(t.MinSeverity)
		if !ok {
			return forwarding.Options{}, fmt.Errorf("unknown severity %q", t.MinSeverity)
		}
		minSeverity = &sev
	}
	return forwarding.Options{
		Name:             t.Name,
		QueueMaxMessages: t.Queue.MaxMessages,
		QueueMaxBytes:    int64(t.Queue.MaxBytes),
		BatchMaxRows:     t.Batch.MaxRows,
		BatchMaxBytes:    t.Batch.MaxBytes.Int(),
		BatchMaxWait:     t.Batch.MaxWait.D(),
		InitialBackoff:   t.Retry.InitialBackoff.D(),
		MaxBackoff:       t.Retry.MaxBackoff.D(),
		Sources:          t.Sources,
		MinSeverity:      minSeverity,
		Config:           t,
	}, nil
}

// desiredForwardTargets is the union of the configuration file and the
// database, with the file winning a clash of names — the same precedence as
// sources, so there is one rule to remember rather than two.
func (a *App) desiredForwardTargets(ctx context.Context) []forwarding.Target {
	var out []forwarding.Target
	fromFile := map[string]bool{}
	for _, t := range a.cfg.Forwarding.Targets {
		opts, err := forwardOptions(t)
		if err != nil {
			a.log.Error("forward target has an invalid configuration", "target", t.Name, "error", err)
			continue
		}
		fromFile[strings.ToLower(t.Name)] = true
		out = append(out, forwarding.Target{Options: opts, Origin: forwarding.OriginFile, Enabled: t.IsEnabled()})
	}
	if a.store == nil {
		return out
	}
	stored, err := a.store.ListForwardTargets(ctx, config.DefaultTenant)
	if err != nil {
		a.log.Warn("could not read forward targets", "error", err)
		return out
	}
	for _, m := range stored {
		if fromFile[strings.ToLower(m.Name)] {
			a.log.Warn("forward target ignored: the configuration file defines one with this name", "target", m.Name)
			continue
		}
		var t config.ForwardTarget
		if err := json.Unmarshal(m.Config, &t); err != nil {
			a.log.Warn("forward target has an unreadable configuration", "target", m.Name, "error", err)
			continue
		}
		t.Name = m.Name
		if m.Secret != "" {
			// Credentials are stored apart from the configuration, so a read
			// of the configuration cannot return them.
			t.BearerToken = m.Secret
		}
		opts, err := forwardOptions(t)
		if err != nil {
			a.log.Warn("forward target has an invalid configuration", "target", m.Name, "error", err)
			continue
		}
		out = append(out, forwarding.Target{Options: opts, Origin: forwarding.OriginDatabase, Enabled: m.Enabled})
	}
	return out
}

// watchForwardTargets applies database changes to the running forwarders.
func (a *App) watchForwardTargets(ctx context.Context) {
	reconcile := func() {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		a.forwarders.Reconcile(cctx, a.desiredForwardTargets(cctx), a.buildForwarder)
	}
	if w, ok := a.store.(forwardTargetWatcher); ok {
		go func() {
			if err := w.WatchForwardTargets(ctx, reconcile); err != nil && ctx.Err() == nil {
				a.log.Warn("forward target notifications stopped; falling back to polling", "error", err)
			}
		}()
	}
	t := time.NewTicker(forwardReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reconcile()
		}
	}
}

// forwardTargetWatcher is implemented by stores that can push changes.
type forwardTargetWatcher interface {
	WatchForwardTargets(ctx context.Context, notify func()) error
}
