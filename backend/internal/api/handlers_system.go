package api

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"mime"
	"net/http"
	"net/netip"
	"path"
	"strings"
	"time"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/forwarding"
	"github.com/freezxp/syslogc/backend/internal/ingestion/pipeline"
	"github.com/freezxp/syslogc/backend/internal/metrics"
)

// ---- HTTP ingestion -------------------------------------------------------

type ingestLineError struct {
	Line  int    `json:"line"`
	Error string `json:"error"`
}

type ingestResponse struct {
	Accepted int               `json:"accepted"`
	Rejected int               `json:"rejected"`
	Errors   []ingestLineError `json:"errors,omitempty"`
}

const maxIngestErrors = 20

// handleIngest accepts JSON objects, JSON arrays or NDJSON (FR-HTTP).
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	deps := s.opts.Ingest
	src := deps.Source()
	if src == nil {
		return errStatus(http.StatusNotFound, "not_configured", "HTTP ingestion is not configured (add an http_json source)")
	}
	if p.Tenant != src.Norm.Tenant {
		return errStatus(http.StatusForbidden, "forbidden", "API key tenant does not match the ingestion source")
	}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	ndjson := mt == "application/x-ndjson" || mt == "application/jsonl"
	if !ndjson && mt != "application/json" {
		return errStatus(http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json or application/x-ndjson")
	}

	var body io.Reader = r.Body
	switch strings.ToLower(r.Header.Get("Content-Encoding")) {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			return badRequest("bad_request", "", "invalid gzip body")
		}
		defer func() { _ = zr.Close() }()
		body = zr
	default:
		return errStatus(http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Encoding must be gzip or identity")
	}
	// Limit the decompressed size (zip-bomb protection).
	limited := &limitedReader{r: body, remaining: deps.MaxBodyBytes}
	body = limited

	resp := ingestResponse{}
	peer := netip.AddrPort{}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		peer = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
		if client := s.clientAddr(r); client != peer.Addr() {
			peer = netip.AddrPortFrom(client, 0) // forwarded by a trusted proxy
		}
	}
	m := src.Metrics
	reject := func(line int, msg string) {
		resp.Rejected++
		if len(resp.Errors) < maxIngestErrors {
			resp.Errors = append(resp.Errors, ingestLineError{Line: line, Error: msg})
		}
	}
	events := 0
	var backpressure bool
	enqueue := func(line int, raw []byte) bool {
		events++
		if events > deps.MaxEvents {
			reject(line, fmt.Sprintf("more than %d events in one request", deps.MaxEvents))
			return false
		}
		raw = trimSpace(raw)
		if len(raw) == 0 || raw[0] != '{' || !json.Valid(raw) {
			reject(line, "not a valid JSON object")
			return true
		}
		msg := pipeline.RawMessage{Data: string(raw), ReceivedAt: time.Now(), Peer: peer, Source: src}
		m.Received.Inc()
		m.BytesReceived.Add(float64(len(raw)))
		ctx, cancel := context.WithTimeout(r.Context(), deps.EnqueueTimeout)
		err := deps.Sink.Enqueue(ctx, msg)
		cancel()
		if err != nil {
			backpressure = true
			return false
		}
		resp.Accepted++
		return true
	}

	var readErr error
	if ndjson {
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 0, 64<<10), int(min(deps.MaxBodyBytes, 16<<20)))
		line := 0
		for sc.Scan() {
			line++
			if len(trimSpace(sc.Bytes())) == 0 {
				continue
			}
			if !enqueue(line, sc.Bytes()) {
				break
			}
		}
		readErr = sc.Err()
	} else {
		br := bufio.NewReader(body)
		first, err := peekNonSpace(br)
		dec := json.NewDecoder(br)
		switch {
		case err != nil:
			readErr = err
		case first == '[':
			if _, err := dec.Token(); err != nil {
				readErr = err
				break
			}
			for i := 1; dec.More(); i++ {
				var raw json.RawMessage
				if err := dec.Decode(&raw); err != nil {
					readErr = err
					break
				}
				if !enqueue(i, raw) {
					break
				}
			}
		default:
			for i := 1; ; i++ {
				var raw json.RawMessage
				if err := dec.Decode(&raw); err != nil {
					if !errors.Is(err, io.EOF) {
						readErr = err
					}
					break
				}
				if !enqueue(i, raw) {
					break
				}
			}
		}
	}

	if limited.exceeded {
		return errStatus(http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds %d bytes (after decompression)", deps.MaxBodyBytes)
	}
	if readErr != nil && !backpressure {
		reject(0, "body could not be read completely: "+truncate(readErr.Error(), 200))
	}
	if backpressure {
		w.Header().Set("Retry-After", "2")
		writeJSON(w, http.StatusServiceUnavailable, resp)
		return nil
	}
	writeJSON(w, http.StatusAccepted, resp)
	return nil
}

type limitedReader struct {
	r         io.Reader
	remaining int64
	exceeded  bool
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		l.exceeded = true
		return 0, io.ErrUnexpectedEOF
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.r.Read(p)
	l.remaining -= int64(n)
	return n, err
}

// peekNonSpace skips leading whitespace and returns the next byte unread.
func peekNonSpace(br *bufio.Reader) (byte, error) {
	for {
		b, err := br.Peek(1)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, errors.New("empty body")
			}
			return 0, err
		}
		switch b[0] {
		case ' ', '\t', '\r', '\n':
			_, _ = br.ReadByte()
		default:
			return b[0], nil
		}
	}
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r' || b[0] == '\n') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t' || b[len(b)-1] == '\r' || b[len(b)-1] == '\n') {
		b = b[:len(b)-1]
	}
	return b
}

// ---- system -----------------------------------------------------------------

func (s *Server) handleSystemHealth(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	status, components, _ := s.readiness(r.Context())
	body := map[string]any{
		"status": status, "node": s.opts.NodeID, "version": s.opts.Version, "roles": s.opts.Roles,
		"uptime_seconds": int64(time.Since(s.opts.StartedAt).Seconds()), "components": components,
	}
	if s.opts.API.Sources != nil {
		body["sources"] = s.opts.API.Sources()
	}
	writeJSON(w, http.StatusOK, body)
	return nil
}

func (s *Server) handleSystemIngestion(w http.ResponseWriter, _ *http.Request, _ *auth.Principal) error {
	snap, err := s.opts.Metrics.Snapshot()
	if err != nil {
		return err
	}
	type sourceJSON struct {
		*metrics.SourceTotals
		State             string  `json:"state,omitempty"`
		Error             string  `json:"error,omitempty"`
		ReceivedPerSecond float64 `json:"received_per_second"`
		StoredPerSecond   float64 `json:"stored_per_second"`
	}
	sources := []sourceJSON{}
	seen := map[string]bool{}
	if s.opts.API.Sources != nil {
		for _, st := range s.opts.API.Sources() {
			totals := snap.Sources[st.Name]
			if totals == nil {
				totals = &metrics.SourceTotals{Name: st.Name, Protocol: st.Protocol, Dropped: map[string]int64{}}
			}
			sources = append(sources, sourceJSON{SourceTotals: totals, State: st.State, Error: st.Error})
			seen[st.Name] = true
		}
	}
	for _, t := range snap.SortedSources() {
		if !seen[t.Name] && t.Name != "" {
			sources = append(sources, sourceJSON{SourceTotals: t})
		}
	}
	if received, stored, ok := s.rates.rates(time.Now(), snap); ok {
		for i := range sources {
			sources[i].ReceivedPerSecond = received[sources[i].Name]
			sources[i].StoredPerSecond = stored[sources[i].Name]
		}
	}
	body := map[string]any{"node": s.opts.NodeID, "sources": sources, "storage_healthy": snap.StorageHealthy,
		"forwarding":              s.forwardingStatus(),
		"e2e_latency_p50_seconds": finite(snap.E2EP50), "e2e_latency_p99_seconds": finite(snap.E2EP99)}
	if s.opts.API.Queue != nil {
		body["queue"] = s.opts.API.Queue()
	} else {
		body["queue"] = QueueInfo{}
	}
	writeJSON(w, http.StatusOK, body)
	return nil
}

func finite(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func (s *Server) handleSystemStorage(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	st := s.opts.API.Storage
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	caps := st.Capabilities()
	body := map[string]any{
		"backend":   st.Name(),
		"reachable": st.Ping(ctx) == nil,
		"capabilities": map[string]any{
			"native_dialects": caps.NativeDialects, "native_tail": caps.NativeTail, "per_tenant_retention": caps.PerTenantRetention,
		},
	}
	if u, err := st.Admin().Usage(ctx); err == nil {
		body["usage"] = map[string]int64{"compressed_bytes": u.CompressedBytes, "uncompressed_bytes": u.UncompressedBytes,
			"free_disk_bytes": u.FreeDiskBytes, "total_disk_bytes": u.TotalDiskBytes}
	}
	if s.opts.API.Retention != nil {
		body["retention"] = s.opts.API.Retention()
	}
	writeJSON(w, http.StatusOK, body)
	return nil
}

// handleSystemConfig returns the effective configuration with secrets
// redacted, so operators can confirm what a node is running.
func (s *Server) handleSystemConfig(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	out, err := s.opts.API.Config.YAML()
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"node": s.opts.NodeID, "yaml": string(out)})
	return nil
}

// handleSystemRetention reports the configured and backend retention plus
// how to change them. Syslogc never deletes data itself.
func (s *Server) handleSystemRetention(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	body := map[string]any{
		"configured": s.opts.API.Config.Retention.Period.String(),
		"backend":    s.opts.API.Storage.Name(),
		"instructions": "Retention is enforced by the storage backend. Set the same period in both places: " +
			"VictoriaLogs -retentionPeriod (SYSLOGC_RETENTION in the Compose stack) and retention.period " +
			"in the Syslogc configuration, then restart both.",
	}
	if s.opts.API.Retention != nil {
		body["status"] = s.opts.API.Retention()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if u, err := s.opts.API.Storage.Admin().Usage(ctx); err == nil {
		body["usage"] = map[string]int64{"compressed_bytes": u.CompressedBytes, "uncompressed_bytes": u.UncompressedBytes,
			"free_disk_bytes": u.FreeDiskBytes, "total_disk_bytes": u.TotalDiskBytes}
	}
	writeJSON(w, http.StatusOK, body)
	return nil
}

// forwardingStatus lists the forward targets, or nil when none exist.
func (s *Server) forwardingStatus() []forwarding.Status {
	if s.opts.API.Forwarders == nil {
		return nil
	}
	return s.opts.API.Forwarders()
}

// ---- web UI -------------------------------------------------------------------

// handleFallback answers unknown API paths with a JSON 404 and serves the web
// UI for everything else.
func (s *Server) handleFallback(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if strings.HasPrefix(r.URL.Path, "/api/") || s.opts.API.WebUI == nil {
		securityHeaders(w, false, r.TLS != nil, s.secureOrigin(r))
		return errStatus(http.StatusNotFound, "not_found", "no such endpoint")
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		securityHeaders(w, false, r.TLS != nil, s.secureOrigin(r))
		return errStatus(http.StatusMethodNotAllowed, "bad_request", "method not allowed")
	}
	return s.handleWebUI(w, r, p)
}

// handleWebUI serves the embedded single-page app with SPA fallback.
func (s *Server) handleWebUI(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	ui := s.opts.API.WebUI
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	f, err := ui.Open(name)
	if err == nil {
		st, statErr := f.Stat()
		_ = f.Close()
		if statErr == nil && !st.IsDir() {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			http.ServeFileFS(w, r, ui, name)
			return nil
		}
	}
	if strings.HasPrefix(name, "assets/") || path.Ext(name) != "" && name != "index.html" {
		http.NotFound(w, r)
		return nil
	}
	index, err := fs.ReadFile(ui, "index.html")
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, placeholderPage)
		return nil
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(index)
	return nil
}

const placeholderPage = `<!doctype html><html><head><meta charset="utf-8"><title>Syslogc</title></head>
<body style="font-family:system-ui;background:#0b0d10;color:#e6e6e6;padding:3rem">
<h1>Syslogc</h1><p>The web UI was not built into this binary. Run <code>make web</code> and rebuild,
or use the API under <code>/api/v1</code>.</p></body></html>`
