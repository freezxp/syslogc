package config

import "time"

// Default returns the built-in configuration defaults.
func Default() Config {
	return Config{
		Node: NodeConfig{Roles: []string{RoleAll}},
		Server: ServerConfig{HTTP: HTTPConfig{
			Address:           ":8080",
			ReadHeaderTimeout: Duration(10 * time.Second),
			IdleTimeout:       Duration(120 * time.Second),
		}},
		Log: LogConfig{Level: "info", Format: "json"},
		Storage: StorageConfig{
			Type: "victorialogs",
			VictoriaLogs: VictoriaLogsConfig{
				InsertURL:    "http://127.0.0.1:9428",
				SelectURL:    "http://127.0.0.1:9428",
				StreamFields: []string{"source", "hostname", "app_name"},
				WriteTimeout: Duration(30 * time.Second),
				QueryTimeout: Duration(60 * time.Second),
				Compression:  "none",
			},
		},
		Ingestion: IngestionConfig{
			Queue:        QueueConfig{MaxMessages: 500_000, MaxBytes: 256 << 20},
			ParseWorkers: 0,
			Batch:        BatchConfig{MaxRows: 10_000, MaxBytes: 8 << 20, MaxWait: Duration(500 * time.Millisecond)},
			Writers:      4,
			BatchQueue:   0,
			Retry:        RetryConfig{InitialBackoff: Duration(250 * time.Millisecond), MaxBackoff: Duration(30 * time.Second)},
			Time:         TimeConfig{MaxFutureSkew: Duration(10 * time.Minute)},
			Limits:       LimitsConfig{MaxFields: 256, MaxFieldValueBytes: 32 << 10, MaxFieldNameBytes: 256},
		},
		Retention: RetentionConfig{Period: Duration(30 * 24 * time.Hour)},
		Shutdown:  ShutdownConfig{DrainDelay: Duration(5 * time.Second), Timeout: Duration(30 * time.Second)},
	}
}

// Per-source defaults applied to zero values after loading.
const (
	DefaultUDPMaxMessageBytes    = 65535
	DefaultStreamMaxMessageBytes = 64 << 10
	DefaultMaxConnections        = 2000
	DefaultTCPIdleTimeout        = 10 * time.Minute
	DefaultUDPReadBuffer         = 8 << 20
)

func applySourceDefaults(s *Source) {
	if s.Type == "" {
		s.Type = SourceTypeSyslog
	}
	if s.Format == "" {
		s.Format = FormatAuto
	}
	if s.Timezone == "" {
		s.Timezone = "UTC"
	}
	if s.RawMessage == "" {
		if s.Type == SourceTypeHTTPJSON {
			s.RawMessage = RawNever
		} else {
			s.RawMessage = RawAlways
		}
	}
	if s.HostnameFallback == "" {
		s.HostnameFallback = HostnameFallbackNone
	}
	if s.SDFlatten == "" {
		s.SDFlatten = SDFlattenFull
	}
	if s.Tenant == "" {
		s.Tenant = DefaultTenant
	}
	if s.MaxMessageBytes == 0 {
		if s.Protocol == ProtocolUDP {
			s.MaxMessageBytes = DefaultUDPMaxMessageBytes
		} else {
			s.MaxMessageBytes = DefaultStreamMaxMessageBytes
		}
	}
	if s.Framing == "" {
		s.Framing = FramingAuto
	}
	if s.MaxConnections == 0 {
		s.MaxConnections = DefaultMaxConnections
	}
	if s.IdleTimeout == 0 {
		s.IdleTimeout = Duration(DefaultTCPIdleTimeout)
	}
	if s.UDP.ReadBufferBytes == 0 {
		s.UDP.ReadBufferBytes = DefaultUDPReadBuffer
	}
	if s.TLS.MinVersion == "" {
		s.TLS.MinVersion = "1.2"
	}
	if s.TLS.ClientAuth == "" {
		s.TLS.ClientAuth = "none"
	}
}
