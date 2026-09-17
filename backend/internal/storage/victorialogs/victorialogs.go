// Package victorialogs implements the storage contract on VictoriaLogs'
// HTTP API (/insert/jsonline for writes, /select/logsql/* for reads).
package victorialogs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/freezxp/syslogc/backend/internal/storage"
)

// Config configures the adapter.
type Config struct {
	// InsertURL and SelectURL are base URLs; in cluster mode they point at
	// vlinsert and vlselect respectively.
	InsertURL    string
	SelectURL    string
	StreamFields []string
	WriteTimeout time.Duration
	QueryTimeout time.Duration
	// Compression is "none" or "gzip".
	Compression       string
	BasicUsername     string
	BasicPasswordFile string
	BearerTokenFile   string
	// MaxConnsPerHost bounds concurrent connections (defaults to 64).
	MaxConnsPerHost int
}

// Backend is the VictoriaLogs storage adapter.
type Backend struct {
	cfg       Config
	client    *http.Client
	insertURL string
	selectURL string
	auth      func(*http.Request)
	gzip      bool
}

var _ storage.Backend = (*Backend)(nil)

// New validates cfg and returns a Backend.
func New(cfg Config) (*Backend, error) {
	insert, err := baseURL(cfg.InsertURL)
	if err != nil {
		return nil, fmt.Errorf("victorialogs insert URL: %w", err)
	}
	sel, err := baseURL(cfg.SelectURL)
	if err != nil {
		return nil, fmt.Errorf("victorialogs select URL: %w", err)
	}
	if len(cfg.StreamFields) == 0 {
		return nil, errors.New("victorialogs: at least one stream field is required")
	}
	if cfg.MaxConnsPerHost == 0 {
		cfg.MaxConnsPerHost = 64
	}
	auth, err := authFunc(cfg)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          cfg.MaxConnsPerHost * 2,
		MaxIdleConnsPerHost:   cfg.MaxConnsPerHost,
		MaxConnsPerHost:       cfg.MaxConnsPerHost,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 0, // bounded per request via context
		// Responses are small (writes) or streamed (queries); request bodies
		// are compressed explicitly when configured.
		DisableCompression: true,
	}
	return &Backend{
		cfg:       cfg,
		client:    &http.Client{Transport: transport},
		insertURL: insert,
		selectURL: sel,
		auth:      auth,
		gzip:      cfg.Compression == "gzip",
	}, nil
}

func (b *Backend) Name() string { return "victorialogs" }

func (b *Backend) Capabilities() storage.Capabilities {
	return storage.Capabilities{
		NativeDialects: []string{"logsql"},
		NativeTail:     true,
		NativeFacets:   true,
	}
}

func (b *Backend) Writer() storage.LogWriter   { return b }
func (b *Backend) Querier() storage.LogQuerier { return b }
func (b *Backend) Admin() storage.Admin        { return b }

// Ping checks both the insert and select endpoints' health.
func (b *Backend) Ping(ctx context.Context) error {
	for _, base := range uniq(b.insertURL, b.selectURL) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
		if err != nil {
			return err
		}
		b.auth(req)
		resp, err := b.client.Do(req) //nolint:bodyclose // closed by drain
		if err != nil {
			return fmt.Errorf("victorialogs health: %w", err)
		}
		drain(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("victorialogs health: HTTP %d", resp.StatusCode)
		}
	}
	return nil
}

func (b *Backend) Close() error {
	b.client.CloseIdleConnections()
	return nil
}

// tenantHeaders maps a Syslogc tenant to VictoriaLogs AccountID/ProjectID.
// Only the default tenant exists until multi-tenancy is implemented.
func tenantHeaders(req *http.Request, tenant string) error {
	switch tenant {
	case "", "default":
		req.Header.Set("AccountID", "0")
		req.Header.Set("ProjectID", "0")
		return nil
	}
	return fmt.Errorf("%w: %q", storage.ErrUnknownTenant, tenant)
}

func authFunc(cfg Config) (func(*http.Request), error) {
	switch {
	case cfg.BearerTokenFile != "":
		token, err := readSecretFile(cfg.BearerTokenFile)
		if err != nil {
			return nil, err
		}
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }, nil
	case cfg.BasicUsername != "":
		password := ""
		if cfg.BasicPasswordFile != "" {
			p, err := readSecretFile(cfg.BasicPasswordFile)
			if err != nil {
				return nil, err
			}
			password = p
		}
		return func(r *http.Request) { r.SetBasicAuth(cfg.BasicUsername, password) }, nil
	}
	return func(*http.Request) {}, nil
}

func readSecretFile(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-configured secret file path
	if err != nil {
		return "", fmt.Errorf("victorialogs: read secret file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func baseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid URL %q", raw)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func uniq(a, b string) []string {
	if a == b {
		return []string{a}
	}
	return []string{a, b}
}

// drain reads and closes a response body so the connection can be reused.
func drain(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 1<<20))
	_ = body.Close()
}
