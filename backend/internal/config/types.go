// Package config defines Syslogc's configuration model and loads it from
// defaults, YAML, environment variables and command-line flags
// (precedence: flags > env > YAML > defaults).
package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Config is the root configuration.
type Config struct {
	Node       NodeConfig       `koanf:"node"`
	Server     ServerConfig     `koanf:"server"`
	Log        LogConfig        `koanf:"log"`
	Storage    StorageConfig    `koanf:"storage"`
	Ingestion  IngestionConfig  `koanf:"ingestion"`
	Forwarding ForwardingConfig `koanf:"forwarding"`
	Retention  RetentionConfig  `koanf:"retention"`
	Shutdown   ShutdownConfig   `koanf:"shutdown"`
	Metadata   MetadataConfig   `koanf:"metadata"`
	Auth       AuthConfig       `koanf:"auth"`
	Query      QueryConfig      `koanf:"query"`
}

type MetadataConfig struct {
	Postgres PostgresConfig `koanf:"postgres"`
}

type PostgresConfig struct {
	// DSN is a PostgreSQL connection string. Prefer DSNFile for secrets.
	DSN      string `koanf:"dsn" redact:"true"`
	DSNFile  string `koanf:"dsn_file"`
	MaxConns int    `koanf:"max_conns"`
}

type AuthConfig struct {
	// SecretKeyFile holds at least 32 random bytes used to sign cursors.
	// All API nodes must share it. Generated in memory when empty.
	SecretKeyFile      string          `koanf:"secret_key_file"`
	SessionTTL         Duration        `koanf:"session_ttl"`
	SessionIdleTimeout Duration        `koanf:"session_idle_timeout"`
	CookieSecure       bool            `koanf:"cookie_secure"`
	BootstrapAdmin     BootstrapConfig `koanf:"bootstrap_admin"`
}

type BootstrapConfig struct {
	Username     string `koanf:"username"`
	PasswordFile string `koanf:"password_file"`
}

type QueryConfig struct {
	MaxTieGroup     int  `koanf:"max_tie_group"`
	MaxTailSessions int  `koanf:"max_tail_sessions"`
	AuditAll        bool `koanf:"audit_all"`
}

// Role names a runtime role of the process.
const (
	RoleIngest = "ingest"
	RoleAPI    = "api"
	RoleAll    = "all"
)

type NodeConfig struct {
	// ID identifies the node in logs and metrics. Defaults to the hostname.
	ID string `koanf:"id"`
	// Roles is any combination of "ingest" and "api", or "all".
	Roles []string `koanf:"roles"`
}

// HasRole reports whether the node runs the given role.
func (n NodeConfig) HasRole(role string) bool {
	for _, r := range n.Roles {
		if r == role || r == RoleAll {
			return true
		}
	}
	return false
}

type ServerConfig struct {
	HTTP HTTPConfig `koanf:"http"`
}

type HTTPConfig struct {
	Address           string   `koanf:"address"`
	ReadHeaderTimeout Duration `koanf:"read_header_timeout"`
	IdleTimeout       Duration `koanf:"idle_timeout"`
	// AllowedOrigins lists extra browser origins (scheme://host[:port]) accepted
	// for state-changing requests, e.g. the public URL of a reverse proxy that
	// does not preserve the Host header.
	AllowedOrigins []string `koanf:"allowed_origins"`
	// TrustedProxies lists proxy addresses/CIDRs whose X-Forwarded-For header
	// is trusted to determine the client IP.
	TrustedProxies []string `koanf:"trusted_proxies"`
}

type LogConfig struct {
	Level  string `koanf:"level"`  // debug | info | warn | error
	Format string `koanf:"format"` // json | text
}

type StorageConfig struct {
	Type         string             `koanf:"type"`
	VictoriaLogs VictoriaLogsConfig `koanf:"victorialogs"`
}

type VictoriaLogsConfig struct {
	InsertURL    string   `koanf:"insert_url"`
	SelectURL    string   `koanf:"select_url"`
	StreamFields []string `koanf:"stream_fields"`
	WriteTimeout Duration `koanf:"write_timeout"`
	QueryTimeout Duration `koanf:"query_timeout"`
	// Compression of insert request bodies: none | gzip.
	Compression       string `koanf:"compression"`
	BasicUsername     string `koanf:"basic_username"`
	BasicPasswordFile string `koanf:"basic_password_file"`
	BearerTokenFile   string `koanf:"bearer_token_file"`
}

type IngestionConfig struct {
	Queue        QueueConfig      `koanf:"queue"`
	ParseWorkers int              `koanf:"parse_workers"`
	Batch        BatchConfig      `koanf:"batch"`
	Writers      int              `koanf:"writers"`
	BatchQueue   int              `koanf:"batch_queue"`
	Retry        RetryConfig      `koanf:"retry"`
	Time         TimeConfig       `koanf:"time"`
	Limits       LimitsConfig     `koanf:"limits"`
	HTTP         HTTPIngestConfig `koanf:"http"`
	Sources      []Source         `koanf:"sources"`
}

type HTTPIngestConfig struct {
	MaxBodyBytes   ByteSize `koanf:"max_body_bytes"`
	MaxEvents      int      `koanf:"max_events"`
	EnqueueTimeout Duration `koanf:"enqueue_timeout"`
}

type QueueConfig struct {
	MaxMessages int      `koanf:"max_messages"`
	MaxBytes    ByteSize `koanf:"max_bytes"`
}

type BatchConfig struct {
	MaxRows  int      `koanf:"max_rows"`
	MaxBytes ByteSize `koanf:"max_bytes"`
	MaxWait  Duration `koanf:"max_wait"`
}

type RetryConfig struct {
	InitialBackoff Duration `koanf:"initial_backoff"`
	MaxBackoff     Duration `koanf:"max_backoff"`
}

type TimeConfig struct {
	// MaxFutureSkew is how far ahead of receive time an event timestamp may be.
	MaxFutureSkew Duration `koanf:"max_future_skew"`
	// MaxPastAge is how far behind receive time an event timestamp may be.
	// Zero means "retention period minus one day".
	MaxPastAge Duration `koanf:"max_past_age"`
}

type LimitsConfig struct {
	MaxFields          int      `koanf:"max_fields"`
	MaxFieldValueBytes ByteSize `koanf:"max_field_value_bytes"`
	MaxFieldNameBytes  int      `koanf:"max_field_name_bytes"`
}

// Source types, protocols and policies.
const (
	SourceTypeSyslog   = "syslog"
	SourceTypeHTTPJSON = "http_json"

	ProtocolUDP = "udp"
	ProtocolTCP = "tcp"
	ProtocolTLS = "tls"

	FormatAuto    = "auto"
	FormatRFC5424 = "rfc5424"
	FormatRFC3164 = "rfc3164"

	FramingAuto          = "auto"
	FramingOctetCounting = "octet_counting"
	FramingLF            = "lf"
	FramingNUL           = "nul"

	RawAlways  = "always"
	RawOnError = "on_error"
	RawNever   = "never"

	HostnameFallbackNone = "none"
	HostnameFallbackIP   = "ip"

	SDFlattenFull  = "full"
	SDFlattenShort = "short"

	DefaultTenant = "default"
)

// Source configures one receiver.
type Source struct {
	Name     string `koanf:"name" json:"name"`
	Type     string `koanf:"type" json:"type"`
	Enabled  *bool  `koanf:"enabled" json:"enabled,omitempty"`
	Protocol string `koanf:"protocol" json:"protocol,omitempty"`
	Address  string `koanf:"address" json:"address,omitempty"`

	Format           string            `koanf:"format" json:"format,omitempty"`
	Timezone         string            `koanf:"timezone" json:"timezone,omitempty"`
	AllowedCIDRs     []string          `koanf:"allowed_cidrs" json:"allowed_cidrs,omitempty"`
	MaxMessageBytes  ByteSize          `koanf:"max_message_bytes" json:"max_message_bytes,omitempty"`
	RawMessage       string            `koanf:"raw_message" json:"raw_message,omitempty"`
	HostnameFallback string            `koanf:"hostname_fallback" json:"hostname_fallback,omitempty"`
	SDFlatten        string            `koanf:"sd_flatten" json:"sd_flatten,omitempty"`
	Labels           map[string]string `koanf:"labels" json:"labels,omitempty"`
	Tenant           string            `koanf:"tenant" json:"tenant,omitempty"`

	// Stream-oriented (tcp/tls) settings.
	Framing        string   `koanf:"framing" json:"framing,omitempty"`
	MaxConnections int      `koanf:"max_connections" json:"max_connections,omitempty"`
	IdleTimeout    Duration `koanf:"idle_timeout" json:"idle_timeout,omitempty"`

	UDP UDPConfig `koanf:"udp" json:"udp,omitempty"`
	TLS TLSConfig `koanf:"tls" json:"tls,omitempty"`
}

// IsEnabled reports whether the source is enabled (default true).
func (s Source) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

type UDPConfig struct {
	// Sockets is the number of SO_REUSEPORT sockets; 0 means GOMAXPROCS.
	Sockets         int      `koanf:"sockets" json:"sockets,omitempty"`
	ReadBufferBytes ByteSize `koanf:"read_buffer_bytes" json:"read_buffer_bytes,omitempty"`
}

type TLSConfig struct {
	CertFile     string `koanf:"cert_file" json:"cert_file,omitempty"`
	KeyFile      string `koanf:"key_file" json:"key_file,omitempty"`
	MinVersion   string `koanf:"min_version" json:"min_version,omitempty"` // "1.2" | "1.3"
	ClientAuth   string `koanf:"client_auth" json:"client_auth,omitempty"` // none | request | require_and_verify
	ClientCAFile string `koanf:"client_ca_file" json:"client_ca_file,omitempty"`
}

// ForwardingConfig mirrors stored logs to other instances.
type ForwardingConfig struct {
	Targets []ForwardTarget `koanf:"targets"`
}

// ForwardTarget is one remote instance that receives a copy of stored logs.
type ForwardTarget struct {
	Name    string `koanf:"name"`
	Enabled *bool  `koanf:"enabled"`
	// URL is the base URL of the remote VictoriaLogs instance.
	URL string `koanf:"url"`
	// Tenant selects which tenant's logs are forwarded; empty forwards all.
	Tenant string `koanf:"tenant"`
	// Sources restricts forwarding to these source names; empty forwards all.
	Sources []string `koanf:"sources"`
	// MinSeverity forwards only this severity or more severe (e.g. "warning").
	MinSeverity string `koanf:"min_severity"`

	StreamFields []string `koanf:"stream_fields"`
	Compression  string   `koanf:"compression"`
	WriteTimeout Duration `koanf:"write_timeout"`

	BasicUsername     string `koanf:"basic_username"`
	BasicPasswordFile string `koanf:"basic_password_file"`
	BearerTokenFile   string `koanf:"bearer_token_file"`

	Queue ForwardQueueConfig `koanf:"queue"`
	Batch BatchConfig        `koanf:"batch"`
	Retry RetryConfig        `koanf:"retry"`
}

// IsEnabled reports whether the target is enabled (default true).
func (t ForwardTarget) IsEnabled() bool { return t.Enabled == nil || *t.Enabled }

type ForwardQueueConfig struct {
	MaxMessages int      `koanf:"max_messages"`
	MaxBytes    ByteSize `koanf:"max_bytes"`
}

type RetentionConfig struct {
	Period Duration `koanf:"period"`
}

type ShutdownConfig struct {
	DrainDelay Duration `koanf:"drain_delay"`
	Timeout    Duration `koanf:"timeout"`
}

// Duration is a time.Duration that also accepts a "d" (day) suffix, e.g. "30d".
type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) String() string {
	td := time.Duration(d)
	if td != 0 && td%(24*time.Hour) == 0 {
		return strconv.FormatInt(int64(td/(24*time.Hour)), 10) + "d"
	}
	return td.String()
}

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := ParseDuration(string(b))
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// ParseDuration parses Go durations plus a whole-number "d" suffix.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.ParseInt(n, 10, 64)
		if err != nil || days < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return v, nil
}

// ByteSize is a size in bytes that accepts units: B, KB/KiB, MB/MiB, GB/GiB.
// Decimal units (KB) are treated as binary for simplicity and predictability.
type ByteSize int64

func (b ByteSize) Int() int { return int(b) }

func (b ByteSize) String() string {
	switch {
	case b != 0 && b%(1<<30) == 0:
		return strconv.FormatInt(int64(b)>>30, 10) + "GiB"
	case b != 0 && b%(1<<20) == 0:
		return strconv.FormatInt(int64(b)>>20, 10) + "MiB"
	case b != 0 && b%(1<<10) == 0:
		return strconv.FormatInt(int64(b)>>10, 10) + "KiB"
	}
	return strconv.FormatInt(int64(b), 10)
}

func (b *ByteSize) UnmarshalText(t []byte) error {
	v, err := ParseByteSize(string(t))
	if err != nil {
		return err
	}
	*b = v
	return nil
}

func (b ByteSize) MarshalText() ([]byte, error) { return []byte(b.String()), nil }

// ParseByteSize parses sizes such as "1048576", "64KiB", "8MiB", "1GB".
func ParseByteSize(s string) (ByteSize, error) {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	n, err := strconv.ParseInt(s[:i], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	var mult int64
	switch strings.ToLower(strings.TrimSpace(s[i:])) {
	case "", "b":
		mult = 1
	case "k", "kb", "kib":
		mult = 1 << 10
	case "m", "mb", "mib":
		mult = 1 << 20
	case "g", "gb", "gib":
		mult = 1 << 30
	default:
		return 0, fmt.Errorf("invalid size unit in %q", s)
	}
	if n > (1<<62)/mult {
		return 0, fmt.Errorf("size %q too large", s)
	}
	return ByteSize(n * mult), nil
}
