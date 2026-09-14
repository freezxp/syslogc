package listener

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/freezxp/syslogc/backend/internal/config"
)

// certCheckInterval is how often certificate files are checked for changes.
const certCheckInterval = 10 * time.Second

// NewTLSConfig builds a server TLS configuration for a syslog TLS source.
// Certificates are reloaded when the files change, without a restart.
func NewTLSConfig(c config.TLSConfig, log *slog.Logger) (*tls.Config, error) {
	reloader, err := newCertReloader(c.CertFile, c.KeyFile, log)
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
	if c.ClientCAFile != "" {
		pem, err := os.ReadFile(c.ClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("read client CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("client CA file contains no certificates")
		}
		cfg.ClientCAs = pool
	}
	return cfg, nil
}

type certReloader struct {
	certFile, keyFile string
	log               *slog.Logger

	mu        sync.Mutex
	cert      *tls.Certificate
	modTime   time.Time
	lastCheck time.Time
}

func newCertReloader(certFile, keyFile string, log *slog.Logger) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile, log: log}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *certReloader) load() error {
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("load TLS certificate: %w", err)
	}
	r.cert = &cert
	r.modTime = r.latestModTime()
	r.lastCheck = time.Now()
	return nil
}

func (r *certReloader) latestModTime() time.Time {
	var latest time.Time
	for _, f := range []string{r.certFile, r.keyFile} {
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
				r.log.Info("TLS certificate reloaded", "cert_file", r.certFile)
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
