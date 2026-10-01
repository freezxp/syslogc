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
func NewTLSConfig(c config.TLSConfig, log *slog.Logger) (*tls.Config, error) {
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
	switch c.ClientAuth {
	case "request":
		cfg.ClientAuth = tls.RequestClientCert
	case "require_and_verify":
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	pool, err := certs.ClientCAs(c.ClientCA, c.ClientCAFile)
	if err != nil {
		return nil, err
	}
	if pool != nil {
		cfg.ClientCAs = pool
	}
	return cfg, nil
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
