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
	// When restricts the rule to records that match every condition. It is
	// what lets one source carry several kinds of record: a Windows server
	// sends its Security log, its SQL Server log and its IIS log over the
	// same connection, and they must become different fields.
	//
	// Empty means the rule applies to every JSON record, which is what a
	// source carrying only one kind wants.
	When []JSONMatch
}

// JSONMatch is one condition on a record.
type JSONMatch struct {
	// Keys are the keys to test, in order; the first one present decides.
	// Several are allowed because senders disagree about what to call the
	// same thing between versions.
	Keys []string
	// Equals matches any of these values, ignoring case.
	Equals []string
	// Prefix matches a value that starts with it, ignoring case — for names
	// that carry an instance, such as MSSQL$SALES.
	Prefix string
}

// matches reports whether the record satisfies the condition. With neither
// Equals nor Prefix the test is that the key is there at all, which is how a
// record is recognised by the shape it has rather than a value it carries.
func (m JSONMatch) matches(record map[string]json.RawMessage) bool {
	for _, key := range m.Keys {
		raw, ok := record[key]
		if !ok {
			continue
		}
		value := jsonScalar(raw)
		if len(m.Equals) == 0 && m.Prefix == "" {
			return true
		}
		for _, want := range m.Equals {
			if strings.EqualFold(value, want) {
				return true
			}
		}
		if m.Prefix != "" && len(value) >= len(m.Prefix) &&
			strings.EqualFold(value[:len(m.Prefix)], m.Prefix) {
			return true
		}
		// The key was present and did not match; a later key naming the same
		// thing cannot overrule it.
		return false
	}
	return false
}

// JSONExtractor promotes JSON keys to fields.
type JSONExtractor struct {
	prefix string
	keys   map[string]string
	when   []JSONMatch
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
	for _, m := range c.When {
		if len(m.Keys) == 0 {
			return nil, fmt.Errorf("a condition needs at least one key to test")
		}
	}
	return &JSONExtractor{prefix: c.Prefix, keys: c.Keys, when: c.When}, nil
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
	// It was JSON either way — that is what the return value reports, and it
	// decides whether the raw text is worth keeping. A rule that does not
	// apply to this record simply adds no fields.
	for _, m := range e.when {
		if !m.matches(record) {
			return true
		}
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
