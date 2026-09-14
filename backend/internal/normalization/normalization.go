// Package normalization applies the rules of docs/log-data-model.md to a
// parsed entry: timestamp policy, network identity, field naming rules,
// limits, raw message policy and source metadata.
package normalization

import (
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/freezxp/syslogc/backend/internal/logentry"
)

// RawPolicy controls retention of the original message.
type RawPolicy uint8

const (
	RawAlways RawPolicy = iota
	RawOnError
	RawNever
)

// Options are the global normalization settings.
type Options struct {
	// MaxFutureSkew bounds how far an event time may be ahead of receive time.
	MaxFutureSkew time.Duration
	// MaxPastAge bounds how far an event time may be behind receive time
	// (0 disables the check).
	MaxPastAge         time.Duration
	MaxFields          int
	MaxFieldValueBytes int
	MaxFieldNameBytes  int
}

// Source holds the per-source settings normalization needs.
type Source struct {
	Name               string
	Type               logentry.SourceType
	Protocol           logentry.Protocol
	Tenant             string
	RawPolicy          RawPolicy
	HostnameFallbackIP bool
	// Labels are pre-built "labels.<k>" fields shared by all entries.
	Labels []logentry.Field
}

// Meta describes how the message was received.
type Meta struct {
	Data       string
	ReceivedAt time.Time
	Peer       netip.AddrPort
	// Truncated is set when the listener truncated the message.
	Truncated bool
}

// Normalizer applies normalization rules. It is safe for concurrent use.
type Normalizer struct {
	opts Options
}

func New(opts Options) *Normalizer {
	return &Normalizer{opts: opts}
}

// Apply normalizes e in place. The entry must already hold parser output
// (or a parse-failure placeholder with Format unknown).
func (n *Normalizer) Apply(e *logentry.Entry, src *Source, m *Meta) {
	e.ReceivedAt = m.ReceivedAt
	e.Source = src.Name
	e.SourceType = src.Type
	e.Protocol = src.Protocol
	e.Tenant = src.Tenant
	e.Labels = src.Labels
	e.Truncated = e.Truncated || m.Truncated

	if m.Peer.IsValid() {
		e.SourceIP = m.Peer.Addr().Unmap()
		e.SourcePort = m.Peer.Port()
	}
	if e.Hostname == "" && src.HostnameFallbackIP && e.SourceIP.IsValid() {
		e.Hostname = e.SourceIP.String()
	}

	n.applyTime(e)

	e.Message = validUTF8(e.Message)
	e.Hostname = validUTF8(e.Hostname)
	e.AppName = validUTF8(e.AppName)
	e.ProcessID = validUTF8(e.ProcessID)
	e.MessageID = validUTF8(e.MessageID)
	e.TimestampRaw = validUTF8(e.TimestampRaw)
	n.applyFields(e)

	switch src.RawPolicy {
	case RawAlways:
		e.Raw = validUTF8(m.Data)
	case RawOnError:
		if e.ParseError != "" || e.Format == logentry.FormatUnknown {
			e.Raw = validUTF8(m.Data)
		}
	case RawNever:
		e.Raw = ""
	}
}

// applyTime resolves Entry.Time according to the timestamp policy.
func (n *Normalizer) applyTime(e *logentry.Entry) {
	switch {
	case e.Time.IsZero():
		e.Time = e.ReceivedAt
		e.TimeSource = logentry.TimeReceived
	case n.opts.MaxFutureSkew > 0 && e.Time.After(e.ReceivedAt.Add(n.opts.MaxFutureSkew)),
		n.opts.MaxPastAge > 0 && e.Time.Before(e.ReceivedAt.Add(-n.opts.MaxPastAge)):
		if e.TimestampRaw == "" {
			e.TimestampRaw = e.Time.Format(time.RFC3339Nano)
		}
		e.Time = e.ReceivedAt
		e.TimeSource = logentry.TimeAdjusted
	default:
		e.TimeSource = logentry.TimeEvent
	}
	e.Time = e.Time.UTC()
}

// applyFields enforces naming rules and limits on dynamic fields.
func (n *Normalizer) applyFields(e *logentry.Entry) {
	if n.opts.MaxFields > 0 && len(e.Fields) > n.opts.MaxFields {
		e.FieldsDropped += len(e.Fields) - n.opts.MaxFields
		e.Fields = e.Fields[:n.opts.MaxFields]
	}
	for i := range e.Fields {
		f := &e.Fields[i]
		f.Key = n.fieldName(f.Key)
		f.Value = validUTF8(f.Value)
		if n.opts.MaxFieldValueBytes > 0 && len(f.Value) > n.opts.MaxFieldValueBytes {
			f.Value = truncateUTF8(f.Value, n.opts.MaxFieldValueBytes)
			e.Truncated = true
		}
	}
}

// fieldName applies the dynamic field naming rules:
// reserved and core names move under "fields.", empty names become
// "fields.empty", and long names are truncated with a "~" suffix.
func (n *Normalizer) fieldName(key string) string {
	key = validUTF8(key)
	if key == "" {
		return "fields.empty"
	}
	if max := n.opts.MaxFieldNameBytes; max > 1 && len(key) > max {
		key = truncateUTF8(key, max-1) + "~"
	}
	if IsReserved(key) {
		return "fields." + key
	}
	return key
}

// coreFields are the storage names of core fields (docs/log-data-model.md §2).
var coreFields = map[string]struct{}{
	"timestamp": {}, "message": {}, "received_at": {}, "hostname": {}, "source_ip": {},
	"source_port": {}, "peer_ip": {}, "facility": {}, "facility_code": {}, "severity": {},
	"severity_code": {}, "priority": {}, "protocol": {}, "format": {}, "app_name": {},
	"process_id": {}, "message_id": {}, "source": {}, "source_type": {}, "raw_message": {},
	"parse_error": {}, "time_source": {}, "severity_source": {}, "timestamp_raw": {},
	"truncated": {}, "fields_dropped": {}, "tenant": {},
}

// IsReserved reports whether a dynamic field may not use key as-is.
func IsReserved(key string) bool {
	if strings.HasPrefix(key, "_") || strings.HasPrefix(key, "labels.") {
		return true
	}
	_, core := coreFields[key]
	return core
}

// validUTF8 replaces invalid UTF-8 sequences with U+FFFD, allocating only
// when the input is invalid.
func validUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}

// truncateUTF8 cuts s to at most max bytes without splitting a rune.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// LabelFields converts static labels to sorted "labels.<k>" fields.
func LabelFields(labels map[string]string) []logentry.Field {
	if len(labels) == 0 {
		return nil
	}
	out := make([]logentry.Field, 0, len(labels))
	for k, v := range labels {
		out = append(out, logentry.Field{Key: "labels." + k, Value: v})
	}
	// Deterministic order simplifies testing and diffing stored rows.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Key < out[j-1].Key; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
