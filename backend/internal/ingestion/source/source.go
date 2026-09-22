// Package source turns source configuration into immutable runtime
// settings shared by listeners and the pipeline.
package source

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/freezxp/syslogc/backend/internal/config"
	"github.com/freezxp/syslogc/backend/internal/extract"
	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/metrics"
	"github.com/freezxp/syslogc/backend/internal/normalization"
)

// Settings are the resolved, read-only runtime settings of one source.
type Settings struct {
	Name   string
	Config config.Source

	Protocol logentry.Protocol
	// Format is the configured format; when AutoDetect is set it is ignored
	// and the format is detected per message.
	Format     logentry.Format
	AutoDetect bool
	Location   *time.Location
	SDShort    bool

	AllowedCIDRs    []netip.Prefix
	MaxMessageBytes int

	Norm normalization.Source
	// Extract pulls fields out of the message; nil when none are configured.
	Extract *extract.Extractor
	Metrics *metrics.SourceMetrics
}

// New resolves cfg (which must already be validated).
func New(cfg config.Source, m *metrics.Metrics) (*Settings, error) {
	s := &Settings{
		Name:            cfg.Name,
		Config:          cfg,
		SDShort:         cfg.SDFlatten == config.SDFlattenShort,
		MaxMessageBytes: cfg.MaxMessageBytes.Int(),
	}
	switch cfg.Protocol {
	case config.ProtocolUDP:
		s.Protocol = logentry.ProtocolUDP
	case config.ProtocolTCP:
		s.Protocol = logentry.ProtocolTCP
	case config.ProtocolTLS:
		s.Protocol = logentry.ProtocolTLS
	default:
		if cfg.Type == config.SourceTypeHTTPJSON {
			s.Protocol = logentry.ProtocolHTTP
		} else {
			return nil, fmt.Errorf("source %s: unsupported protocol %q", cfg.Name, cfg.Protocol)
		}
	}
	switch {
	case cfg.Type == config.SourceTypeHTTPJSON:
		s.Format = logentry.FormatJSON
	case cfg.Format == config.FormatAuto:
		s.AutoDetect = true
	case cfg.Format == config.FormatRFC5424:
		s.Format = logentry.FormatRFC5424
	case cfg.Format == config.FormatRFC3164:
		s.Format = logentry.FormatRFC3164
	default:
		return nil, fmt.Errorf("source %s: unsupported format %q", cfg.Name, cfg.Format)
	}
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return nil, fmt.Errorf("source %s: timezone: %w", cfg.Name, err)
	}
	s.Location = loc
	for _, c := range cfg.AllowedCIDRs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return nil, fmt.Errorf("source %s: allowed_cidrs: %w", cfg.Name, err)
		}
		s.AllowedCIDRs = append(s.AllowedCIDRs, p.Masked())
	}

	sourceType := logentry.SourceTypeSyslog
	if cfg.Type == config.SourceTypeHTTPJSON {
		sourceType = logentry.SourceTypeHTTPJSON
	}
	raw := normalization.RawAlways
	switch cfg.RawMessage {
	case config.RawOnError:
		raw = normalization.RawOnError
	case config.RawNever:
		raw = normalization.RawNever
	}
	s.Norm = normalization.Source{
		Name:               cfg.Name,
		Type:               sourceType,
		Protocol:           s.Protocol,
		Tenant:             cfg.Tenant,
		RawPolicy:          raw,
		HostnameFallbackIP: cfg.HostnameFallback == config.HostnameFallbackIP,
		Labels:             normalization.LabelFields(cfg.Labels),
	}
	if len(cfg.Extract) > 0 {
		rules := make([]extract.Config, 0, len(cfg.Extract))
		for _, r := range cfg.Extract {
			rules = append(rules, extract.Config{Name: r.Name, Contains: r.Contains, Regex: r.Regex, Prefix: r.Prefix})
		}
		ex, err := extract.New(rules)
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", cfg.Name, err)
		}
		s.Extract = ex
	}
	s.Metrics = m.Source(cfg.Name, s.Protocol)
	return s, nil
}

// Allowed reports whether a peer address may send to this source.
func (s *Settings) Allowed(addr netip.Addr) bool {
	if len(s.AllowedCIDRs) == 0 {
		return true
	}
	addr = addr.Unmap()
	for _, p := range s.AllowedCIDRs {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
