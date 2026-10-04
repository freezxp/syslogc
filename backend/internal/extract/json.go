package extract

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/freezxp/syslogc/backend/internal/logentry"
)

// MaxJSONKeys bounds how many keys one rule may promote.
const MaxJSONKeys = 64

// JSONConfig promotes the keys of a JSON message body to fields.
//
// Senders that can emit structured records should: matching an event out of
// its English description works until the server is installed in another
// language, and a key name does not change when it is.
type JSONConfig struct {
	// Prefix is prepended to every field produced.
	Prefix string
	// Keys maps a key in the message to the field name it becomes. A key
	// that is absent from a record produces no field.
	Keys map[string]string
}

// JSONExtractor promotes JSON keys to fields.
type JSONExtractor struct {
	prefix string
	keys   map[string]string
}

// NewJSON compiles a JSON extraction rule.
func NewJSON(c JSONConfig) (*JSONExtractor, error) {
	if len(c.Keys) == 0 {
		return nil, fmt.Errorf("a JSON rule needs at least one key")
	}
	if len(c.Keys) > MaxJSONKeys {
		return nil, fmt.Errorf("at most %d keys are allowed, the rule has %d", MaxJSONKeys, len(c.Keys))
	}
	if c.Prefix != "" {
		if err := validFieldName(strings.TrimSuffix(c.Prefix, ".")); err != nil {
			return nil, fmt.Errorf("prefix %q: %w", c.Prefix, err)
		}
	}
	for key, field := range c.Keys {
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("a key to read is required")
		}
		if err := validFieldName(field); err != nil {
			return nil, fmt.Errorf("key %q becomes field %q: %w", key, field, err)
		}
	}
	return &JSONExtractor{prefix: c.Prefix, keys: c.Keys}, nil
}

// Apply reads the message as a JSON object and adds the mapped fields,
// reporting whether it was JSON at all.
//
// A message that is not JSON is left alone rather than treated as an error:
// a source carrying structured records usually carries a few plain ones too,
// and dropping them would be worse than not enriching them.
func (e *JSONExtractor) Apply(entry *logentry.Entry) bool {
	if e == nil || entry.Message == "" {
		return false
	}
	msg := strings.TrimSpace(entry.Message)
	// Only an object can have the keys this reads, so anything else is
	// rejected before the cost of parsing it.
	if !strings.HasPrefix(msg, "{") {
		return false
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal([]byte(msg), &record); err != nil {
		return false
	}
	for key, field := range e.keys {
		raw, ok := record[key]
		if !ok {
			continue
		}
		value := jsonScalar(raw)
		if value == "" {
			continue
		}
		entry.AddField(e.prefix+field, value)
	}
	return true
}

// jsonScalar renders a value as the text a field holds. Objects and arrays
// are skipped: a field is a value to filter and group by, and nesting one
// inside it helps nobody.
func jsonScalar(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	switch {
	case trimmed == "" || trimmed == "null":
		return ""
	case strings.HasPrefix(trimmed, "{"), strings.HasPrefix(trimmed, "["):
		return ""
	case strings.HasPrefix(trimmed, `"`):
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return ""
		}
		return strings.TrimSpace(s)
	case trimmed == "true", trimmed == "false":
		return trimmed
	default:
		// A number. Rendered without exponent or trailing zeros so that an
		// event id reads as 4624 rather than 4.624e+03.
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil {
			return trimmed
		}
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
}
