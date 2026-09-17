package victorialogs

import (
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/freezxp/syslogc/backend/internal/logentry"
)

// Storage field names written for core fields. _time and _msg are
// VictoriaLogs' reserved time and message fields.
const (
	fieldTime = "_time"
	fieldMsg  = "_msg"
)

// appendEntry appends e as one JSON line (including the trailing newline).
// It is a hand-rolled encoder: no reflection and no intermediate maps.
func appendEntry(b []byte, e *logentry.Entry) []byte {
	b = append(b, `{"_time":"`...)
	b = e.Time.UTC().AppendFormat(b, time.RFC3339Nano)
	b = append(b, `","_msg":`...)
	b = appendString(b, e.Message)

	b = append(b, `,"received_at":"`...)
	b = e.ReceivedAt.UTC().AppendFormat(b, time.RFC3339Nano)
	b = append(b, '"')

	b = appendOpt(b, "hostname", e.Hostname)
	if e.SourceIP.IsValid() {
		b = append(b, `,"source_ip":"`...)
		b = e.SourceIP.AppendTo(b)
		b = append(b, '"')
		if e.SourcePort != 0 {
			b = append(b, `,"source_port":"`...)
			b = strconv.AppendUint(b, uint64(e.SourcePort), 10)
			b = append(b, '"')
		}
	}
	if e.PeerIP.IsValid() && e.PeerIP != e.SourceIP {
		b = append(b, `,"peer_ip":"`...)
		b = e.PeerIP.AppendTo(b)
		b = append(b, '"')
	}
	if pri, ok := e.Priority(); ok {
		b = appendOpt(b, "facility", e.Facility.String())
		b = appendInt(b, "facility_code", int(e.Facility))
		b = appendInt(b, "priority", pri)
	}
	b = appendOpt(b, "severity", e.Severity.String())
	b = appendInt(b, "severity_code", int(e.Severity))
	b = appendOpt(b, "protocol", e.Protocol.String())
	b = appendOpt(b, "format", e.Format.String())
	b = appendOpt(b, "app_name", e.AppName)
	b = appendOpt(b, "process_id", e.ProcessID)
	b = appendOpt(b, "message_id", e.MessageID)
	b = appendOpt(b, "source", e.Source)
	b = appendOpt(b, "source_type", e.SourceType.String())
	b = appendOpt(b, "raw_message", e.Raw)
	b = appendOpt(b, "parse_error", e.ParseError)
	if e.TimeSource != logentry.TimeEvent {
		b = appendOpt(b, "time_source", e.TimeSource.String())
	}
	if e.SeveritySource == logentry.SeverityDefault {
		b = appendOpt(b, "severity_source", "default")
	}
	b = appendOpt(b, "timestamp_raw", e.TimestampRaw)
	if e.Truncated {
		b = appendOpt(b, "truncated", "true")
	}
	if e.FieldsDropped > 0 {
		b = appendInt(b, "fields_dropped", e.FieldsDropped)
	}
	for _, f := range e.Labels {
		b = appendKV(b, f.Key, f.Value)
	}
	for _, f := range e.Fields {
		b = appendKV(b, f.Key, f.Value)
	}
	return append(b, '}', '\n')
}

func appendOpt(b []byte, key, value string) []byte {
	if value == "" {
		return b
	}
	return appendKV(b, key, value)
}

func appendKV(b []byte, key, value string) []byte {
	b = append(b, ',')
	b = appendString(b, key)
	b = append(b, ':')
	return appendString(b, value)
}

func appendInt(b []byte, key string, v int) []byte {
	b = append(b, ',')
	b = appendString(b, key)
	b = append(b, ':', '"')
	b = strconv.AppendInt(b, int64(v), 10)
	return append(b, '"')
}

const hexDigits = "0123456789abcdef"

// appendString appends s as a JSON string. Control characters are escaped
// and invalid UTF-8 is replaced with U+FFFD, matching encoding/json.
func appendString(b []byte, s string) []byte {
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' {
				i++
				continue
			}
			b = append(b, s[start:i]...)
			switch c {
			case '"', '\\':
				b = append(b, '\\', c)
			case '\n':
				b = append(b, '\\', 'n')
			case '\r':
				b = append(b, '\\', 'r')
			case '\t':
				b = append(b, '\\', 't')
			default:
				b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, s[start:i]...)
			b = append(b, `�`...)
			i += size
			start = i
			continue
		}
		i += size
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}
