// Package acme obtains and renews certificates from Let's Encrypt, so a TLS
// source can be set up with a hostname instead of a certificate.
//
// Proof of control is the HTTP-01 challenge: the certificate authority asks
// for a file at http://<domain>/.well-known/acme-challenge/<token>, so the
// domain must resolve to this host from the public internet and port 80 must
// reach it. Nothing else is served on that port.
//
// The limits are the dangerous part. Let's Encrypt allows five failed
// validations per hostname per hour, so a deployment whose DNS or firewall is
// not ready does not merely fail — it locks itself out of retrying for the
// rest of the hour, and a renewal that silently stops is a listener that
// stops months later. This package therefore checks what it can before asking
// for anything, refuses to ask when the check fails, and reports the state of
// every certificate it holds.
package acme

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// Directories a certificate can be asked of.
const (
	// DirectoryProduction issues certificates browsers and senders trust,
	// under limits that punish a misconfigured deployment.
	DirectoryProduction = acme.LetsEncryptURL
	// DirectoryStaging issues certificates nothing trusts, under limits that
	// forgive. It is the right place to find out whether port 80 is open.
	DirectoryStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// ChallengePort is where the authority looks. It is not configurable because
// the authority will not look anywhere else.
const ChallengePort = 80

// Config describes what to ask for.
type Config struct {
	// Domains are the hostnames to certify. Senders must connect by one of
	// these names, and each must resolve to this host.
	Domains []string
	// Email receives expiry warnings from the authority. Optional, and worth
	// setting: it is the only warning that arrives if renewal stops.
	Email string
	// Directory is the authority to ask. Empty means staging, because the
	// safe default is the one that cannot lock you out.
	Directory string
	// AgreedToTerms must be true. The subscriber agreement is a legal
	// undertaking, so it is recorded explicitly rather than implied by
	// turning the feature on.
	AgreedToTerms bool
	// SkipPreflight asks anyway when the reachability check fails. For a
	// deployment whose port 80 is reachable only from the authority's
	// network, where the check cannot see what the authority sees.
	SkipPreflight bool
}

// Store holds account keys and certificates between restarts. Without one,
// every restart asks for a new certificate and the weekly limit is reached in
// a day.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, data []byte) error
	Delete(ctx context.Context, key string) error
}

// ErrCacheMiss is returned by a Store that holds no such key.
var ErrCacheMiss = autocert.ErrCacheMiss

// Manager obtains certificates and serves the challenges that earn them.
type Manager struct {
	cfg      Config
	mgr      *autocert.Manager
	log      *slog.Logger
	mu       sync.Mutex
	lastErr  error
	lastTry  time.Time
	obtained map[string]time.Time
}

// New validates cfg and prepares a manager. It asks for nothing yet: the
// first certificate is fetched when a sender first connects, or when Prime is
// called.
func New(cfg Config, store Store, log *slog.Logger) (*Manager, error) {
	if len(cfg.Domains) == 0 {
		return nil, errors.New("acme: at least one domain is required")
	}
	for _, d := range cfg.Domains {
		if err := validDomain(d); err != nil {
			return nil, fmt.Errorf("acme: %q: %w", d, err)
		}
	}
	if !cfg.AgreedToTerms {
		return nil, errors.New("acme: the certificate authority's subscriber agreement has not been accepted; " +
			"set accept_terms once you have read https://letsencrypt.org/repository/")
	}
	if cfg.Directory == "" {
		cfg.Directory = DirectoryStaging
	}
	if store == nil {
		return nil, errors.New("acme: a store is required, or every restart would ask for a new certificate")
	}
	m := &Manager{cfg: cfg, log: log, obtained: map[string]time.Time{}}
	m.mgr = &autocert.Manager{
		Cache:  cache{store},
		Prompt: autocert.AcceptTOS,
		Email:  cfg.Email,
		// Exactly the configured names. Without this, anything that connects
		// with an unknown name would make us ask for a certificate for it,
		// which is both an abuse channel and the fastest way to exhaust the
		// weekly limit.
		HostPolicy: autocert.HostWhitelist(cfg.Domains...),
		Client:     &acme.Client{DirectoryURL: cfg.Directory},
	}
	return m, nil
}

// Staging reports whether certificates come from the test authority, which
// nothing trusts.
func (m *Manager) Staging() bool { return m.cfg.Directory != DirectoryProduction }

// GetCertificate serves the certificate for a connection, fetching one if
// there is none yet.
func (m *Manager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	cert, err := m.mgr.GetCertificate(hello)
	m.note(hello.ServerName, err)
	return cert, err
}

// HTTPHandler answers the authority's challenges and nothing else. Any other
// request is refused rather than redirected: this listener exists for one
// purpose and is open to the internet.
func (m *Manager) HTTPHandler() http.Handler {
	challenges := m.mgr.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "this port serves certificate challenges only", http.StatusNotFound)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") {
			http.Error(w, "this port serves certificate challenges only", http.StatusNotFound)
			return
		}
		challenges.ServeHTTP(w, r)
	})
}

// Prime fetches the certificates up front, so a misconfiguration is found
// when the server starts rather than when the first sender connects.
//
// It checks reachability first. Asking when the check has already failed
// spends one of the five failures the hour allows.
func (m *Manager) Prime(ctx context.Context) error {
	if !m.cfg.SkipPreflight {
		if err := m.preflight(ctx); err != nil {
			m.mu.Lock()
			m.lastErr, m.lastTry = err, time.Now()
			m.mu.Unlock()
			return err
		}
	}
	var errs []error
	for _, d := range m.cfg.Domains {
		// autocert bounds this itself (five minutes, from a background
		// context), so there is nothing of ours to cancel.
		_, err := m.mgr.GetCertificate(&tls.ClientHelloInfo{ServerName: d})
		m.note(d, err)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", d, err))
		}
	}
	return errors.Join(errs...)
}

// preflight checks that the challenge could be answered, from outside.
//
// It cannot see what the authority sees — a firewall may admit their network
// and not ours — so a failure here is reported as a refusal to try rather
// than as proof the setup is broken, and SkipPreflight overrides it.
func (m *Manager) preflight(ctx context.Context) error {
	var errs []error
	for _, d := range m.cfg.Domains {
		addrs, err := net.DefaultResolver.LookupHost(ctx, d)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s does not resolve: %w. The authority looks it up in public DNS, "+
				"so an entry only your network can see is not enough", d, err))
			continue
		}
		dialer := net.Dialer{Timeout: 5 * time.Second}
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(addrs[0], fmt.Sprint(ChallengePort)))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s resolves to %s but port %d is not reachable: %w. "+
				"The authority fetches a file over plain HTTP on that port",
				d, addrs[0], ChallengePort, err))
			continue
		}
		_ = conn.Close()
	}
	if len(errs) > 0 {
		return fmt.Errorf("not asking for a certificate yet, because the check would fail and "+
			"only five failures an hour are allowed: %w", errors.Join(errs...))
	}
	return nil
}

// Status describes what the manager holds, for the system page.
type Status struct {
	Domains []string `json:"domains"`
	// Staging certificates are trusted by nothing; saying so is the
	// difference between "it works" and "it will work for real senders".
	Staging bool `json:"staging"`
	// Obtained is when each domain last got a certificate.
	Obtained map[string]time.Time `json:"obtained,omitempty"`
	// Error is why the last attempt failed, empty when the last one worked.
	Error     string    `json:"error,omitempty"`
	LastTried time.Time `json:"last_tried,omitzero"`
}

// Status reports the state of the certificates.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{Domains: m.cfg.Domains, Staging: m.Staging(), LastTried: m.lastTry}
	if m.lastErr != nil {
		st.Error = m.lastErr.Error()
	}
	if len(m.obtained) > 0 {
		st.Obtained = make(map[string]time.Time, len(m.obtained))
		for k, v := range m.obtained {
			st.Obtained[k] = v
		}
	}
	return st
}

func (m *Manager) note(domain string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastTry = time.Now()
	m.lastErr = err
	if err == nil && domain != "" {
		m.obtained[domain] = time.Now()
	}
	if err != nil {
		m.log.Warn("could not obtain a certificate", "domain", domain, "error", err,
			"staging", m.Staging())
	}
}

func validDomain(d string) error {
	switch {
	case strings.TrimSpace(d) == "":
		return errors.New("is empty")
	case strings.ContainsAny(d, " \t/:"):
		return errors.New("must be a bare hostname, without a scheme or port")
	case strings.HasPrefix(d, "*"):
		return errors.New("wildcards need the DNS-01 challenge, which this does not do")
	case strings.EqualFold(d, "localhost"), strings.EqualFold(d, "localhost.localdomain"):
		return errors.New("is not a public name, so no authority can certify it")
	case !strings.Contains(d, "."):
		return errors.New("must be a fully qualified name the authority can look up")
	}
	return nil
}

// cache adapts a Store to autocert, encoding the material so it survives a
// store that holds text.
type cache struct{ store Store }

type cacheEntry struct {
	Data string `json:"data"`
}

func (c cache) Get(ctx context.Context, key string) ([]byte, error) {
	raw, err := c.store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	var e cacheEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(e.Data)
}

func (c cache) Put(ctx context.Context, key string, data []byte) error {
	raw, err := json.Marshal(cacheEntry{Data: base64.StdEncoding.EncodeToString(data)})
	if err != nil {
		return err
	}
	return c.store.Put(ctx, key, raw)
}

func (c cache) Delete(ctx context.Context, key string) error { return c.store.Delete(ctx, key) }
