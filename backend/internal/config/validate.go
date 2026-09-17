package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Validate checks the configuration and returns all problems found.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if len(c.Node.Roles) == 0 {
		add("node.roles: at least one role is required")
	}
	for _, r := range c.Node.Roles {
		if r != RoleIngest && r != RoleAPI && r != RoleAll {
			add("node.roles: unknown role %q (want ingest, api or all)", r)
		}
	}

	if err := validateAddress(c.Server.HTTP.Address); err != nil {
		add("server.http.address: %v", err)
	}
	for _, o := range c.Server.HTTP.AllowedOrigins {
		if u, err := url.Parse(o); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") {
			add("server.http.allowed_origins: %q must be scheme://host[:port]", o)
		}
	}
	for _, p := range c.Server.HTTP.TrustedProxies {
		if _, err := ParsePrefixOrAddr(p); err != nil {
			add("server.http.trusted_proxies: %q: %v", p, err)
		}
	}

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add("log.level: must be debug, info, warn or error")
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		add("log.format: must be json or text")
	}

	if c.Storage.Type != "victorialogs" {
		add("storage.type: unsupported storage %q (supported: victorialogs)", c.Storage.Type)
	}
	vl := c.Storage.VictoriaLogs
	for name, u := range map[string]string{"insert_url": vl.InsertURL, "select_url": vl.SelectURL} {
		if pu, err := url.Parse(u); err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
			add("storage.victorialogs.%s: must be an http(s) URL, got %q", name, u)
		}
	}
	if len(vl.StreamFields) == 0 {
		add("storage.victorialogs.stream_fields: at least one field is required")
	}
	if vl.Compression != "none" && vl.Compression != "gzip" {
		add("storage.victorialogs.compression: must be none or gzip")
	}
	if vl.WriteTimeout <= 0 {
		add("storage.victorialogs.write_timeout: must be positive")
	}

	in := c.Ingestion
	if in.Queue.MaxMessages <= 0 {
		add("ingestion.queue.max_messages: must be positive")
	}
	if in.Queue.MaxBytes < 1<<20 {
		add("ingestion.queue.max_bytes: must be at least 1MiB")
	}
	if in.ParseWorkers < 0 {
		add("ingestion.parse_workers: must be >= 0")
	}
	if in.Batch.MaxRows <= 0 {
		add("ingestion.batch.max_rows: must be positive")
	}
	if in.Batch.MaxBytes < 64<<10 {
		add("ingestion.batch.max_bytes: must be at least 64KiB")
	}
	if in.Batch.MaxWait <= 0 {
		add("ingestion.batch.max_wait: must be positive")
	}
	if in.Writers <= 0 {
		add("ingestion.writers: must be positive")
	}
	if in.BatchQueue < 0 {
		add("ingestion.batch_queue: must be >= 0")
	}
	if in.Retry.InitialBackoff <= 0 || in.Retry.MaxBackoff < in.Retry.InitialBackoff {
		add("ingestion.retry: initial_backoff must be positive and <= max_backoff")
	}
	if in.Time.MaxFutureSkew < 0 || in.Time.MaxPastAge < 0 {
		add("ingestion.time: skew values must be >= 0")
	}
	if in.Limits.MaxFields <= 0 || in.Limits.MaxFieldValueBytes <= 0 || in.Limits.MaxFieldNameBytes <= 0 {
		add("ingestion.limits: all limits must be positive")
	}

	pg := c.Metadata.Postgres
	if c.Node.HasRole(RoleAPI) && pg.DSN == "" && pg.DSNFile == "" {
		add("metadata.postgres: dsn or dsn_file is required for the api role")
	}
	if pg.MaxConns < 1 {
		add("metadata.postgres.max_conns: must be positive")
	}
	if c.Auth.SessionTTL <= 0 || c.Auth.SessionIdleTimeout <= 0 {
		add("auth: session_ttl and session_idle_timeout must be positive")
	}
	if c.Query.MaxTieGroup < 100 || c.Query.MaxTailSessions < 1 {
		add("query: max_tie_group must be >= 100 and max_tail_sessions >= 1")
	}
	if in.HTTP.MaxBodyBytes < 1<<20 || in.HTTP.MaxEvents < 1 || in.HTTP.EnqueueTimeout <= 0 {
		add("ingestion.http: max_body_bytes >= 1MiB, max_events >= 1 and enqueue_timeout > 0 are required")
	}

	if c.Retention.Period < Duration(24*time.Hour) {
		add("retention.period: must be at least 1d")
	}
	if c.Shutdown.Timeout <= 0 {
		add("shutdown.timeout: must be positive")
	}

	names := map[string]bool{}
	binds := map[string]string{}
	for i, s := range in.Sources {
		p := fmt.Sprintf("ingestion.sources[%d]", i)
		if s.Name != "" {
			p = fmt.Sprintf("ingestion.sources[%s]", s.Name)
		}
		errs = append(errs, validateSource(p, s)...)
		if s.Name != "" {
			if names[s.Name] {
				add("%s: duplicate source name", p)
			}
			names[s.Name] = true
		}
		if s.Type == SourceTypeSyslog && s.IsEnabled() {
			transport := "tcp"
			if s.Protocol == ProtocolUDP {
				transport = "udp"
			}
			key := transport + "/" + s.Address
			if other, dup := binds[key]; dup {
				add("%s: address %s/%s already used by source %q", p, transport, s.Address, other)
			}
			binds[key] = s.Name
		}
		if int64(s.MaxMessageBytes)+512 > int64(in.Queue.MaxBytes) {
			add("%s.max_message_bytes: must be smaller than ingestion.queue.max_bytes", p)
		}
	}

	return errors.Join(errs...)
}

func validateSource(p string, s Source) []error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(p+": "+format, args...)) }

	if s.Name == "" {
		add("name is required")
	} else if len(s.Name) > 128 {
		add("name longer than 128 characters")
	}
	switch s.Type {
	case SourceTypeSyslog:
		switch s.Protocol {
		case ProtocolUDP, ProtocolTCP, ProtocolTLS:
		default:
			add("protocol must be udp, tcp or tls")
		}
		if err := validateAddress(s.Address); err != nil {
			add("address: %v", err)
		}
	case SourceTypeHTTPJSON:
		// Served by the HTTP server; no own address.
	default:
		add("type must be syslog or http_json")
	}
	switch s.Format {
	case FormatAuto, FormatRFC5424, FormatRFC3164:
	default:
		add("format must be auto, rfc5424 or rfc3164")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		add("timezone: %v", err)
	}
	for _, c := range s.AllowedCIDRs {
		if _, err := netip.ParsePrefix(c); err != nil {
			add("allowed_cidrs: invalid CIDR %q", c)
		}
	}
	if s.MaxMessageBytes < 256 || s.MaxMessageBytes > 16<<20 {
		add("max_message_bytes must be between 256B and 16MiB")
	}
	if s.Protocol == ProtocolUDP && s.MaxMessageBytes > 65535 {
		add("max_message_bytes cannot exceed 65535 for udp")
	}
	switch s.RawMessage {
	case RawAlways, RawOnError, RawNever:
	default:
		add("raw_message must be always, on_error or never")
	}
	switch s.HostnameFallback {
	case HostnameFallbackNone, HostnameFallbackIP:
	default:
		add("hostname_fallback must be none or ip")
	}
	switch s.SDFlatten {
	case SDFlattenFull, SDFlattenShort:
	default:
		add("sd_flatten must be full or short")
	}
	if s.Tenant != DefaultTenant {
		add("tenant: only %q is supported until multi-tenancy is implemented", DefaultTenant)
	}
	switch s.Framing {
	case FramingAuto, FramingOctetCounting, FramingLF, FramingNUL:
	default:
		add("framing must be auto, octet_counting, lf or nul")
	}
	if s.MaxConnections < 0 {
		add("max_connections must be >= 0")
	}
	if s.UDP.Sockets < 0 || s.UDP.Sockets > 256 {
		add("udp.sockets must be between 0 and 256")
	}
	for k := range s.Labels {
		if k == "" || len(k) > 128 {
			add("labels: invalid label name %q", k)
		}
	}
	if s.Protocol == ProtocolTLS && s.IsEnabled() {
		if s.TLS.CertFile == "" || s.TLS.KeyFile == "" {
			add("tls.cert_file and tls.key_file are required for tls sources")
		}
		if s.TLS.MinVersion != "1.2" && s.TLS.MinVersion != "1.3" {
			add("tls.min_version must be 1.2 or 1.3")
		}
		switch s.TLS.ClientAuth {
		case "none", "request":
		case "require_and_verify":
			if s.TLS.ClientCAFile == "" {
				add("tls.client_ca_file is required with client_auth require_and_verify")
			}
		default:
			add("tls.client_auth must be none, request or require_and_verify")
		}
	}
	return errs
}

func validateAddress(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid address %q: %w", addr, err)
	}
	if host != "" {
		if _, err := netip.ParseAddr(host); err != nil && host != "localhost" {
			return fmt.Errorf("invalid host in %q (use an IP address or empty for all interfaces)", addr)
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("invalid port in %q", addr)
	}
	return nil
}

// ParsePrefixOrAddr parses a CIDR or a single IP address (as a /32 or /128).
func ParsePrefixOrAddr(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		return p.Masked(), err
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// PrepareSource applies per-source defaults and validates one source on its
// own (the API uses it for database-managed sources). Cross-source checks
// such as unique names and bind addresses are the caller's responsibility.
func PrepareSource(s *Source) error {
	applySourceDefaults(s)
	return errors.Join(validateSource("source", *s)...)
}

// ConflictsWith reports why s cannot run alongside other, or "" if it can.
func (s Source) ConflictsWith(other Source) string {
	if strings.EqualFold(s.Name, other.Name) {
		return fmt.Sprintf("name is already used by source %q", other.Name)
	}
	if !s.IsEnabled() || !other.IsEnabled() {
		return ""
	}
	if s.Type == SourceTypeHTTPJSON && other.Type == SourceTypeHTTPJSON {
		return fmt.Sprintf("only one http_json source is supported; %q is already defined", other.Name)
	}
	if s.Type != SourceTypeSyslog || other.Type != SourceTypeSyslog {
		return ""
	}
	if transport(s) == transport(other) && s.Address == other.Address {
		return fmt.Sprintf("address %s/%s is already used by source %q", transport(s), s.Address, other.Name)
	}
	return ""
}

func transport(s Source) string {
	if s.Protocol == ProtocolUDP {
		return "udp"
	}
	return "tcp"
}
