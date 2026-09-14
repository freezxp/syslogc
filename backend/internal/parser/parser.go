// Package parser defines the parser contract and format detection.
// Concrete parsers live in sub-packages and are registered in a Registry.
package parser

import (
	"errors"
	"net/netip"
	"time"

	"github.com/freezxp/syslogc/backend/internal/logentry"
)

// Input is one framed message plus the context a parser may need.
type Input struct {
	// Data is the complete message. Parsers set entry fields to substrings
	// of Data, so a message costs a single string allocation.
	Data       string
	ReceivedAt time.Time
	Peer       netip.AddrPort
	// Location is used for timestamps without an explicit offset (RFC 3164).
	Location *time.Location
	// SDShort drops the "sd.<sd-id>." prefix from RFC 5424 structured data
	// parameter names when they do not collide.
	SDShort bool
}

// Parser turns an Input into envelope fields of an Entry.
//
// Parse must not panic. It returns an error only when the message is not in
// the parser's format at all; partial parses set Entry.ParseError and
// return nil. Parsers set Entry.Time only when the message carried a valid
// timestamp; normalization resolves the final time.
type Parser interface {
	Format() logentry.Format
	Parse(in *Input, e *logentry.Entry) error
}

// ErrNotThisFormat indicates the input is not in the parser's format.
var ErrNotThisFormat = errors.New("input is not in this format")

// Registry maps formats to parsers.
type Registry struct {
	parsers map[logentry.Format]Parser
}

// NewRegistry returns a registry containing the given parsers.
func NewRegistry(ps ...Parser) *Registry {
	r := &Registry{parsers: make(map[logentry.Format]Parser, len(ps))}
	for _, p := range ps {
		r.parsers[p.Format()] = p
	}
	return r
}

// Get returns the parser for f.
func (r *Registry) Get(f logentry.Format) (Parser, bool) {
	p, ok := r.parsers[f]
	return p, ok
}

// Detect guesses the format of data from its first bytes without allocating.
//
//	"<PRI>1 "      → RFC 5424
//	"<PRI>…"       → RFC 3164
//	"{" or "["     → JSON
//	anything else  → RFC 3164 (lenient, PRI-less BSD syslog)
func Detect(data string) logentry.Format {
	if data == "" {
		return logentry.FormatUnknown
	}
	switch data[0] {
	case '{', '[':
		return logentry.FormatJSON
	case '<':
		_, n, ok := ParsePRI(data)
		if !ok {
			return logentry.FormatRFC3164
		}
		rest := data[n:]
		if len(rest) >= 2 && rest[0] == '1' && rest[1] == ' ' {
			return logentry.FormatRFC5424
		}
		return logentry.FormatRFC3164
	}
	return logentry.FormatRFC3164
}

// ParsePRI parses a leading "<N>" priority (0–191). It returns the value,
// the number of bytes consumed, and whether a valid PRI was present.
func ParsePRI(data string) (pri int, n int, ok bool) {
	if len(data) < 3 || data[0] != '<' {
		return 0, 0, false
	}
	i := 1
	for i < len(data) && i <= 4 && data[i] >= '0' && data[i] <= '9' {
		pri = pri*10 + int(data[i]-'0')
		i++
	}
	if i == 1 || i > 4 || i >= len(data) || data[i] != '>' || pri > 191 {
		return 0, 0, false
	}
	return pri, i + 1, true
}

// SetPRI assigns facility and severity from a PRI value.
func SetPRI(e *logentry.Entry, pri int) {
	e.Facility = logentry.Facility(pri >> 3) //nolint:gosec // pri is validated to 0–191, so pri>>3 <= 23
	e.Severity = logentry.Severity(pri & 7)
	e.SeveritySource = logentry.SeverityFromLog
}
