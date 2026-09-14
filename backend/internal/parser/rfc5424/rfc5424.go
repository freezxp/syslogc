// Package rfc5424 implements a single-pass parser for RFC 5424 syslog messages.
//
//	SYSLOG-MSG = HEADER SP STRUCTURED-DATA [SP MSG]
//	HEADER     = PRI VERSION SP TIMESTAMP SP HOSTNAME SP APP-NAME SP PROCID SP MSGID
package rfc5424

import (
	"fmt"
	"strings"
	"time"

	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/parser"
)

const nilValue = "-"

// Parser parses RFC 5424 messages. It is stateless and safe for concurrent use.
type Parser struct{}

func New() *Parser { return &Parser{} }

func (*Parser) Format() logentry.Format { return logentry.FormatRFC5424 }

func (*Parser) Parse(in *parser.Input, e *logentry.Entry) error {
	data := in.Data
	pri, n, ok := parser.ParsePRI(data)
	if !ok {
		return fmt.Errorf("rfc5424: %w: missing or invalid PRI", parser.ErrNotThisFormat)
	}
	pos := n
	if len(data) < pos+2 || data[pos] < '1' || data[pos] > '9' {
		return fmt.Errorf("rfc5424: %w: missing VERSION", parser.ErrNotThisFormat)
	}
	// VERSION = NONZERO-DIGIT 0*2DIGIT; only version 1 is defined.
	vEnd := pos + 1
	for vEnd < len(data) && vEnd < pos+3 && data[vEnd] >= '0' && data[vEnd] <= '9' {
		vEnd++
	}
	if vEnd >= len(data) || data[vEnd] != ' ' {
		return fmt.Errorf("rfc5424: %w: missing SP after VERSION", parser.ErrNotThisFormat)
	}
	if data[pos:vEnd] != "1" {
		return fmt.Errorf("rfc5424: %w: unsupported VERSION %s", parser.ErrNotThisFormat, data[pos:vEnd])
	}
	parser.SetPRI(e, pri)
	pos = vEnd + 1

	// Header tokens. A message may legitimately end after any header field
	// (truncated senders); we keep what we have and note the problem.
	var tokens [5]string // TIMESTAMP HOSTNAME APP-NAME PROCID MSGID
	for i := range tokens {
		tok, next, ok := token(data, pos)
		if !ok {
			e.ParseError = fmt.Sprintf("rfc5424: header truncated at offset %d", pos)
			applyHeader(in, e, tokens[:i])
			return nil
		}
		tokens[i] = tok
		pos = next
	}
	applyHeader(in, e, tokens[:])

	if pos >= len(data) {
		e.ParseError = fmt.Sprintf("rfc5424: missing STRUCTURED-DATA at offset %d", pos)
		return nil
	}

	switch data[pos] {
	case '-':
		pos++
	case '[':
		end, perr := parseSD(data, pos, in.SDShort, e)
		if perr != "" {
			// Keep everything from the malformed SD onwards as the message.
			e.ParseError = perr
			e.Message = trimBOM(data[pos:])
			return nil
		}
		pos = end
	default:
		// Lenient: some senders omit STRUCTURED-DATA entirely.
		e.Message = trimBOM(data[pos:])
		return nil
	}

	if pos < len(data) {
		if data[pos] == ' ' {
			pos++
		} else {
			e.ParseError = fmt.Sprintf("rfc5424: expected SP after STRUCTURED-DATA at offset %d", pos)
		}
		e.Message = trimBOM(data[pos:])
	}
	return nil
}

func applyHeader(in *parser.Input, e *logentry.Entry, tokens []string) {
	for i, tok := range tokens {
		if tok == nilValue {
			continue
		}
		switch i {
		case 0:
			if t, ok := parseTimestamp(tok); ok {
				e.Time = t
			} else {
				e.TimestampRaw = tok
			}
		case 1:
			e.Hostname = tok
		case 2:
			e.AppName = tok
		case 3:
			e.ProcessID = tok
		case 4:
			e.MessageID = tok
		}
	}
}

// token returns the next SP-terminated token starting at pos.
func token(data string, pos int) (tok string, next int, ok bool) {
	if pos >= len(data) {
		return "", pos, false
	}
	i := strings.IndexByte(data[pos:], ' ')
	if i <= 0 { // no SP, or empty token
		return "", pos, false
	}
	return data[pos : pos+i], pos + i + 1, true
}

// parseTimestamp parses an RFC 3339 timestamp, accepting lowercase 't'/'z'
// and fractional seconds beyond the RFC's 6-digit limit.
func parseTimestamp(s string) (time.Time, bool) {
	if len(s) < 20 {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		if strings.ContainsAny(s, "tz") {
			t, err = time.Parse(time.RFC3339Nano, strings.ToUpper(s))
		}
		if err != nil {
			return time.Time{}, false
		}
	}
	return t, true
}

// trimBOM removes a UTF-8 byte order mark from the start of MSG.
func trimBOM(s string) string {
	return strings.TrimPrefix(s, "\ufeff")
}

// parseSD parses one or more SD-ELEMENTs starting at data[pos] == '['.
// It returns the position after the last element or an error description.
func parseSD(data string, pos int, short bool, e *logentry.Entry) (int, string) {
	start := len(e.Fields)
	for pos < len(data) && data[pos] == '[' {
		elemStart := len(e.Fields)
		pos++
		idEnd := pos
		for idEnd < len(data) && isSDNameChar(data[idEnd]) {
			idEnd++
		}
		if idEnd == pos || idEnd >= len(data) {
			e.Fields = e.Fields[:start]
			return pos, fmt.Sprintf("rfc5424: invalid SD-ID at offset %d", pos)
		}
		sdID := data[pos:idEnd]
		pos = idEnd
		for {
			if pos >= len(data) {
				e.Fields = e.Fields[:start]
				return pos, "rfc5424: unterminated STRUCTURED-DATA element"
			}
			if data[pos] == ']' {
				pos++
				break
			}
			if data[pos] != ' ' {
				e.Fields = e.Fields[:start]
				return pos, fmt.Sprintf("rfc5424: invalid STRUCTURED-DATA at offset %d", pos)
			}
			pos++
			nameEnd := pos
			for nameEnd < len(data) && isSDNameChar(data[nameEnd]) {
				nameEnd++
			}
			if nameEnd == pos || nameEnd+1 >= len(data) || data[nameEnd] != '=' || data[nameEnd+1] != '"' {
				e.Fields = e.Fields[:start]
				return pos, fmt.Sprintf("rfc5424: invalid SD-PARAM at offset %d", pos)
			}
			name := data[pos:nameEnd]
			value, next, ok := sdValue(data, nameEnd+2)
			if !ok {
				e.Fields = e.Fields[:start]
				return pos, fmt.Sprintf("rfc5424: unterminated SD-PARAM value at offset %d", nameEnd+2)
			}
			pos = next
			addSDParam(e, elemStart, sdID, name, value, short)
		}
	}
	return pos, ""
}

// sdValue reads a PARAM-VALUE starting after the opening quote, handling
// the escapes \" \\ and \]. It allocates only if escapes are present.
func sdValue(data string, pos int) (string, int, bool) {
	var b *strings.Builder
	runStart := pos
	for i := pos; i < len(data); i++ {
		switch data[i] {
		case '\\':
			if i+1 < len(data) && (data[i+1] == '"' || data[i+1] == '\\' || data[i+1] == ']') {
				if b == nil {
					b = &strings.Builder{}
				}
				b.WriteString(data[runStart:i])
				b.WriteByte(data[i+1])
				i++
				runStart = i + 1
			}
		case '"':
			if b == nil {
				return data[pos:i], i + 1, true
			}
			b.WriteString(data[runStart:i])
			return b.String(), i + 1, true
		}
	}
	return "", len(data), false
}

func addSDParam(e *logentry.Entry, elemStart int, sdID, name, value string, short bool) {
	full := "sd." + sdID + "." + name
	// Repeated PARAM-NAMEs within one SD-ELEMENT become a JSON array.
	for i := elemStart; i < len(e.Fields); i++ {
		if k := e.Fields[i].Key; k == full || (short && k == name) {
			e.Fields[i].Value = appendJSONArray(e.Fields[i].Value, value)
			return
		}
	}
	key := full
	if short {
		key = name
		// Fall back to the full key on collision with an earlier element's field.
		for _, f := range e.Fields {
			if f.Key == name {
				key = full
				break
			}
		}
	}
	e.AddField(key, value)
}

// appendJSONArray turns "a" + "b" into `["a","b"]` and `["a","b"]` + "c" into `["a","b","c"]`.
func appendJSONArray(existing, value string) string {
	var b strings.Builder
	if strings.HasPrefix(existing, `["`) && strings.HasSuffix(existing, `"]`) {
		b.WriteString(existing[:len(existing)-1])
		b.WriteByte(',')
	} else {
		b.WriteByte('[')
		writeJSONString(&b, existing)
		b.WriteByte(',')
	}
	writeJSONString(&b, value)
	b.WriteByte(']')
	return b.String()
}

func writeJSONString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 0x20:
			fmt.Fprintf(b, `\u%04x`, c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
}

// isSDNameChar reports whether c is allowed in SD-NAME:
// PRINTUSASCII except '=', SP, ']', '"'.
func isSDNameChar(c byte) bool {
	return c > 32 && c < 127 && c != '=' && c != ']' && c != '"'
}
