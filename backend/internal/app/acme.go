package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/freezxp/syslogc/backend/internal/acme"
	"github.com/freezxp/syslogc/backend/internal/config"
	"github.com/freezxp/syslogc/backend/internal/ingestion/supervisor"
)

// certificateAuthority builds and remembers one manager per set of domains.
//
// One manager per distinct set rather than per source: two sources certified
// for the same name should share an account and a certificate, because the
// authority counts certificates per name and would otherwise see two
// deployments asking for the same thing.
type certificateAuthority struct {
	app *App

	mu       sync.Mutex
	managers map[string]*acme.Manager
	handlers []http.Handler
	server   *http.Server
}

// serveChallenge offers the request to each manager until one recognises the
// token. A manager that does not own it answers 403 or 404, so trying the
// next is what lets several sets of domains share one port.
func (ca *certificateAuthority) serveChallenge(w http.ResponseWriter, r *http.Request) {
	ca.mu.Lock()
	handlers := append([]http.Handler(nil), ca.handlers...)
	ca.mu.Unlock()
	for _, h := range handlers {
		rec := &challengeRecorder{header: http.Header{}}
		h.ServeHTTP(rec, r)
		if rec.status == 0 || rec.status < 300 {
			for k, v := range rec.header {
				w.Header()[k] = v
			}
			w.WriteHeader(cmp.Or(rec.status, http.StatusOK))
			_, _ = w.Write(rec.body)
			return
		}
	}
	http.Error(w, "this port serves certificate challenges only", http.StatusNotFound)
}

// challengeRecorder captures one manager's answer so an unsuccessful one can
// be discarded in favour of the next manager's.
type challengeRecorder struct {
	header http.Header
	status int
	body   []byte
}

func (c *challengeRecorder) Header() http.Header { return c.header }
func (c *challengeRecorder) WriteHeader(s int)   { c.status = s }
func (c *challengeRecorder) Write(b []byte) (int, error) {
	c.body = append(c.body, b...)
	return len(b), nil
}

// wireACME lets sources ask an authority for their certificates. It starts
// nothing: the challenge listener opens when the first such source does.
func (a *App) wireACME() {
	if a.store == nil || a.supervisor == nil {
		return
	}
	ca := &certificateAuthority{app: a, managers: map[string]*acme.Manager{}}
	a.acme = ca
	a.supervisor.UseCertificateManager(ca.manager)
}

// manager returns the certificate manager for a source, starting the
// challenge listener the first time one is needed.
func (ca *certificateAuthority) manager(sc config.Source) (supervisor.CertificateManager, error) {
	cfg := sc.TLS.ACME
	key := strings.ToLower(strings.Join(cfg.Domains, ",")) + "|" + fmt.Sprint(cfg.Staging)

	ca.mu.Lock()
	defer ca.mu.Unlock()
	if m, ok := ca.managers[key]; ok {
		return m, nil
	}

	directory := acme.DirectoryProduction
	if cfg.Staging {
		directory = acme.DirectoryStaging
	}
	m, err := acme.New(acme.Config{
		Domains:       cfg.Domains,
		Email:         cfg.Email,
		Directory:     directory,
		AgreedToTerms: cfg.AcceptTerms,
		SkipPreflight: cfg.SkipPreflight,
	}, acme.SettingsStore{Store: ca.app.store}, ca.app.log.With("component", "acme"))
	if err != nil {
		return nil, err
	}
	// Every manager must be told it may use HTTP-01, and every manager's
	// challenges must be answerable — the authority picks which to ask for.
	// Registering only the first would leave a second set of domains able to
	// offer nothing but TLS-ALPN, which this deployment does not serve.
	ca.handlers = append(ca.handlers, m.HTTPHandler())
	if err := ca.startChallengeServer(); err != nil {
		return nil, err
	}
	ca.managers[key] = m

	// Fetch up front, in the background: a misconfiguration should be
	// reported in the log at startup rather than discovered by the first
	// sender, but it must not hold up the listener — a source whose
	// certificate is not ready yet still binds, and serves once it is.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := m.Prime(ctx); err != nil {
			ca.app.log.Warn("no certificate yet for this source", "source", sc.Name, "error", err)
			return
		}
		ca.app.log.Info("certificate obtained", "source", sc.Name, "domains", cfg.Domains,
			"staging", m.Staging())
	}()
	return m, nil
}

// startChallengeServer opens port 80 for the authority's challenges, once.
// The handler consults every manager, because a token belongs to whichever
// one asked for it.
func (ca *certificateAuthority) startChallengeServer() error {
	if ca.server != nil {
		return nil
	}
	addr := fmt.Sprintf(":%d", acme.ChallengePort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("a certificate authority reaches this host on port %d, which could not be opened: %w. "+
			"The container must publish it, and nothing else may hold it", acme.ChallengePort, err)
	}
	ca.server = &http.Server{
		Handler:           http.HandlerFunc(ca.serveChallenge),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() {
		if err := ca.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			ca.app.log.Error("the certificate challenge listener stopped", "error", err)
		}
	}()
	ca.app.log.Info("serving certificate challenges", "address", addr)
	return nil
}

// stop closes the challenge listener.
func (ca *certificateAuthority) stop(ctx context.Context) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if ca.server != nil {
		_ = ca.server.Shutdown(ctx)
		ca.server = nil
	}
}

// statuses reports every certificate held, for the system page.
func (ca *certificateAuthority) statuses() []acme.Status {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	out := make([]acme.Status, 0, len(ca.managers))
	for _, m := range ca.managers {
		out = append(out, m.Status())
	}
	return out
}

// certificateStatuses reports what the authority has given us, or nothing
// when no source asks for a certificate.
func (a *App) certificateStatuses() []acme.Status {
	if a.acme == nil {
		return nil
	}
	return a.acme.statuses()
}
