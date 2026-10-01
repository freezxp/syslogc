package listener

import (
	"crypto/tls"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/freezxp/syslogc/backend/internal/certs"
	"github.com/freezxp/syslogc/backend/internal/config"
)

// certCheckInterval is how often certificate files are checked for changes.
const certCheckInterval = 10 * time.Second

// NewTLSConfig builds a server TLS configuration for a syslog TLS source.
// Certificates are reloaded when the files change, without a restart.
// GetCertificateFunc serves a certificate for a connection. A source using
// ACME is given one of these; everything else loads its own material.
type GetCertificateFunc func(*tls.ClientHelloInfo) (*tls.Certificate, error)

func NewTLSConfig(c config.TLSConfig, log *slog.Logger) (*tls.Config, error) {
	return NewTLSConfigWith(c, nil, log)
}

// NewTLSConfigWith builds the configuration, taking its certificate from
// getCert when one is given — which is how a source backed by a certificate
// authority gets one without any material of its own.
func NewTLSConfigWith(c config.TLSConfig, getCert GetCertificateFunc, log *slog.Logger) (*tls.Config, error) {
	if getCert != nil {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: getCert}
		if c.MinVersion == "1.3" {
			cfg.MinVersion = tls.VersionTLS13
		}
		if err := applyClientAuth(cfg, c); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	material := certs.Material{CertFile: c.CertFile, KeyFile: c.KeyFile, Cert: c.Cert, Key: c.Key}
	reloader, err := newCertReloader(material, log)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: reloader.GetCertificate,
	}
	if c.MinVersion == "1.3" {
		cfg.MinVersion = tls.VersionTLS13
	}
	if err := applyClientAuth(cfg, c); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyClientAuth sets whether senders must present a certificate of their
// own, and whose certificates are accepted.
func applyClientAuth(cfg *tls.Config, c config.TLSConfig) error {
	switch c.ClientAuth {
	case "request":
		cfg.ClientAuth = tls.RequestClientCert
	case "require_and_verify":
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	pool, err := certs.ClientCAs(c.ClientCA, c.ClientCAFile)
	if err != nil {
		return err
	}
	if pool != nil {
		cfg.ClientCAs = pool
	}
	return nil
}

type certReloader struct {
	material certs.Material
	log      *slog.Logger

	mu        sync.Mutex
	cert      *tls.Certificate
	modTime   time.Time
	lastCheck time.Time
}

func newCertReloader(material certs.Material, log *slog.Logger) (*certReloader, error) {
	r := &certReloader{material: material, log: log}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *certReloader) load() error {
	cert, _, err := certs.Load(r.material)
	if err != nil {
		return err
	}
	r.cert = cert
	r.modTime = r.latestModTime()
	r.lastCheck = time.Now()
	return nil
}

// latestModTime is the newest change to the files, or the zero time when the
// certificate is PEM text: pasted material changes when the source is saved,
// which restarts the listener anyway.
func (r *certReloader) latestModTime() time.Time {
	if r.material.Inline() {
		return time.Time{}
	}
	var latest time.Time
	for _, f := range []string{r.material.CertFile, r.material.KeyFile} {
		if st, err := os.Stat(f); err == nil && st.ModTime().After(latest) {
			latest = st.ModTime()
		}
	}
	return latest
}

func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.lastCheck) >= certCheckInterval {
		r.lastCheck = time.Now()
		if mt := r.latestModTime(); mt.After(r.modTime) {
			old := r.cert
			if err := r.load(); err != nil {
				r.cert = old
				r.log.Error("TLS certificate reload failed; keeping previous certificate", "error", err)
			} else {
				r.log.Info("TLS certificate reloaded", "cert_file", r.material.CertFile)
			}
		}
	}
	return r.cert, nil
}

// addrPort converts a net.Addr to an unmapped netip.AddrPort.
func addrPort(a net.Addr) netip.AddrPort {
	switch v := a.(type) {
	case *net.TCPAddr:
		ap := v.AddrPort()
		return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	case *net.UDPAddr:
		ap := v.AddrPort()
		return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	}
	return netip.AddrPort{}
}
