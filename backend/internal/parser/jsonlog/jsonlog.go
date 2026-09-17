// Package jsonlog parses one JSON object per message (HTTP ingestion,
// JSON over syslog transports) into a normalized entry: nested objects are
// flattened with dots, well-known keys map to core fields (docs/log-data-model.md §6.2)
// and all other keys are preserved as dynamic fields.
package jsonlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/normalization"
	"github.com/freezxp/syslogc/backend/internal/parser"
)

const maxDepth = 16

// aliases lists accepted keys per core field in priority order.
var aliases = []struct {
	core string
	keys []string
}{
	{"timestamp", []string{"timestamp", "@timestamp", "time", "ts", "datetime"}},
	{"message", []string{"message", "msg", "log", "text"}},
	{"hostname", []string{"hostname", "host", "host.name"}},
	{"severity", []string{"severity", "level", "log.level", "loglevel", "severity_text"}},
	{"app_name", []string{"app_name", "service", "service.name", "app", "application"}},
	{"process_id", []string{"process_id", "pid"}},
	{"source_ip", []string{"source_ip", "src_ip", "client_ip"}},
}

var aliasOf = func() map[string]string {
	m := map[string]string{}
	for _, a := range aliases {
		for _, k := range a.keys {
			m[k] = a.core
		}
	}
	return m
}()

// Parser parses JSON objects. It is stateless and safe for concurrent use.
type Parser struct{}

func New() *Parser { return &Parser{} }

func (*Parser) Format() logentry.Format { return logentry.FormatJSON }

type kv struct {
	key   string
	value string
	isStr bool
}

func (*Parser) Parse(in *parser.Input, e *logentry.Entry) error {
	data := strings.TrimSpace(in.Data)
	if len(data) == 0 || data[0] != '{' {
		return fmt.Errorf("json: %w: not a JSON object", parser.ErrNotThisFormat)
	}
	dec := json.NewDecoder(strings.NewReader(data))
	dec.UseNumber()
	var flat []kv
	if err := walkObject(dec, "", 0, &flat); err != nil {
		return fmt.Errorf("json: %w: %w", parser.ErrNotThisFormat, err)
	}
	if dec.More() {
		return fmt.Errorf("json: %w: trailing data after object", parser.ErrNotThisFormat)
	}

	// Pick the highest-priority alias present for each core field.
	chosen := map[string]int{} // core -> index in flat
	for i, f := range flat {
		core, ok := aliasOf[f.key]
		if !ok {
			continue
		}
		if prev, exists := chosen[core]; exists && aliasRank(core, flat[prev].key) <= aliasRank(core, f.key) {
			continue
		}
		chosen[core] = i
	}
	consumed := make([]bool, len(flat))
	for core, i := range chosen {
		f := flat[i]
		consumed[i] = apply(e, core, f)
	}
	for i, f := range flat {
		if !consumed[i] {
			e.AddField(f.key, f.value)
		}
	}
	return nil
}

func aliasRank(core, key string) int {
	for _, a := range aliases {
		if a.core == core {
			for i, k := range a.keys {
				if k == key {
					return i
				}
			}
		}
	}
	return math.MaxInt
}

// apply sets a core field and reports whether the source key is fully
// represented (and need not be kept as a dynamic field).
func apply(e *logentry.Entry, core string, f kv) bool {
	switch core {
	case "timestamp":
		if t, ok := parseTimestamp(f.value, f.isStr); ok {
			e.Time = t
			return true
		}
		e.TimestampRaw = f.value
		return true
	case "message":
		e.Message = f.value
		return true
	case "hostname":
		e.Hostname = f.value
		return true
	case "app_name":
		e.AppName = f.value
		return true
	case "process_id":
		e.ProcessID = f.value
		return true
	case "severity":
		if sev, ok := normalization.ParseSeverity(f.value); ok {
			e.Severity, e.SeveritySource = sev, logentry.SeverityFromLog
			// Keep the original spelling when it differs (e.g. level=WARN).
			return f.value == sev.String()
		}
		return false
	case "source_ip":
		if addr, err := netip.ParseAddr(f.value); err == nil {
			e.SourceIP = addr.Unmap()
			return true
		}
		return false
	}
	return false
}

func walkObject(dec *json.Decoder, prefix string, depth int, out *[]kv) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != json.Delim('{') {
		return errors.New("expected object")
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := kt.(string)
		if prefix != "" {
			key = prefix + "." + key
		}
		if err := walkValue(dec, key, depth+1, out); err != nil {
			return err
		}
	}
	_, err = dec.Token() // '}'
	return err
}

func walkValue(dec *json.Decoder, key string, depth int, out *[]kv) error {
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0:
		return nil
	case raw[0] == '{' && depth < maxDepth:
		return walkObject(jsonDecoder(raw), key, depth, out)
	case raw[0] == '{' || raw[0] == '[':
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			return err
		}
		*out = append(*out, kv{key: key, value: buf.String()})
	case raw[0] == '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		*out = append(*out, kv{key: key, value: s, isStr: true})
	case string(raw) == "null":
	default: // number or boolean
		*out = append(*out, kv{key: key, value: string(raw)})
	}
	return nil
}

func jsonDecoder(raw []byte) *json.Decoder {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	return d
}

var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
}

// parseTimestamp accepts RFC 3339 variants and Unix epochs in seconds,
// milliseconds, microseconds or nanoseconds (by magnitude).
func parseTimestamp(v string, isStr bool) (time.Time, bool) {
	if isStr {
		for _, l := range timeLayouts {
			if t, err := time.Parse(l, v); err == nil {
				return t, true
			}
		}
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 || math.IsInf(f, 0) {
		return time.Time{}, false
	}
	switch {
	case f < 1e11:
		sec, frac := math.Modf(f)
		return time.Unix(int64(sec), int64(frac*1e9)), true
	case f < 1e14:
		return time.UnixMilli(int64(f)), true
	case f < 1e17:
		return time.UnixMicro(int64(f)), true
	default:
		return time.Unix(0, int64(f)), true
	}
}
