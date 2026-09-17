// Package rfc3164 implements a lenient parser for BSD syslog (RFC 3164) and
// the many vendor variations of it seen in practice.
//
//	[<PRI>][SEQ: ][*|.]TIMESTAMP[ TZ][:] [HOSTNAME ][TAG[PID]: ]MSG
//
// RFC 3164 documents observed practice rather than a strict grammar, so the
// parser never rejects a non-empty message: anything it cannot interpret
// becomes part of MSG.
package rfc3164

import (
	"strings"
	"time"

	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/parser"
)

const (
	maxTagLen = 48
	maxPIDLen = 32
	// defaultPRI is user.notice, the RFC 3164 §4.3.3 default.
	defaultPRI = 13
)

// Parser parses RFC 3164 messages. It is stateless and safe for concurrent use.
type Parser struct{}

func New() *Parser { return &Parser{} }

func (*Parser) Format() logentry.Format { return logentry.FormatRFC3164 }

func (*Parser) Parse(in *parser.Input, e *logentry.Entry) error {
	data := in.Data
	if data == "" {
		return parser.ErrNotThisFormat
	}
	pos := 0
	if pri, n, ok := parser.ParsePRI(data); ok {
		parser.SetPRI(e, pri)
		pos = n
	} else {
		parser.SetPRI(e, defaultPRI)
		e.SeveritySource = logentry.SeverityDefault
	}
	pos = skipSpaces(data, pos)

	// Cisco "service sequence-numbers": "000123: ".
	if seqEnd := digitsEnd(data, pos); seqEnd > pos && hasPrefixAt(data, seqEnd, ": ") {
		e.AddField("sequence", data[pos:seqEnd])
		pos = skipSpaces(data, seqEnd+2)
	}

	loc := in.Location
	if loc == nil {
		loc = time.UTC
	}
	hasTime := false
	if pos < len(data) {
		tsPos := pos
		// Cisco marks unsynchronized clocks with '*' and synchronized-but-lost with '.'.
		if data[tsPos] == '*' || data[tsPos] == '.' {
			tsPos++
		}
		if t, n, ok := parseBSDTimestamp(data[tsPos:], loc, in.ReceivedAt); ok {
			e.Time, pos, hasTime = t, tsPos+n, true
		} else if t, n, ok := parseISOTimestamp(data[tsPos:], loc); ok {
			e.Time, pos, hasTime = t, tsPos+n, true
		}
	}

	if hasTime {
		pos = skipSpaces(data, pos)
		if tok, next := nextToken(data, pos); tok != "" && next < len(data) && isHostname(tok) && !looksLikeTag(tok) {
			e.Hostname = tok
			pos = skipSpaces(data, next)
		}
	}

	pos = parseTag(data, pos, e)
	e.Message = data[pos:]
	return nil
}

var knownZones = map[string]bool{
	"UTC": true, "GMT": true, "CET": true, "CEST": true, "EET": true, "EEST": true, "WET": true, "WEST": true,
	"BST": true, "IST": true, "MSK": true, "EST": true, "EDT": true, "CST": true, "CDT": true, "MST": true,
	"MDT": true, "PST": true, "PDT": true, "AKST": true, "AKDT": true, "HST": true, "JST": true, "KST": true,
	"HKT": true, "SGT": true, "AEST": true, "AEDT": true, "ACST": true, "AWST": true, "NZST": true, "NZDT": true,
}

var months = [...]string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}

// parseBSDTimestamp parses "Mmm dd [yyyy ]hh:mm:ss[.frac][ TZ][:]" and returns
// the time and the number of bytes consumed.
func parseBSDTimestamp(s string, loc *time.Location, received time.Time) (time.Time, int, bool) {
	if len(s) < 14 || s[3] != ' ' {
		return time.Time{}, 0, false
	}
	month := 0
	for i, m := range months {
		if strings.EqualFold(s[:3], m) {
			month = i + 1
			break
		}
	}
	if month == 0 {
		return time.Time{}, 0, false
	}
	pos := 4
	if s[pos] == ' ' { // space-padded day: "Sep  1"
		pos++
	}
	dayEnd := digitsEnd(s, pos)
	if dayEnd-pos < 1 || dayEnd-pos > 2 || dayEnd >= len(s) || s[dayEnd] != ' ' {
		return time.Time{}, 0, false
	}
	day := atoi(s[pos:dayEnd])
	pos = dayEnd + 1

	year := 0
	if yEnd := digitsEnd(s, pos); yEnd-pos == 4 && yEnd < len(s) && s[yEnd] == ' ' {
		year = atoi(s[pos:yEnd])
		pos = yEnd + 1
	}

	hour, min, sec, nsec, n, ok := parseClock(s[pos:])
	if !ok || day < 1 || day > 31 {
		return time.Time{}, 0, false
	}
	pos += n

	// Optional trailing year after the clock: "Sep 14 10:00:00 2026".
	if year == 0 && pos+5 <= len(s) && s[pos] == ' ' {
		if yEnd := digitsEnd(s, pos+1); yEnd-pos-1 == 4 && (yEnd == len(s) || s[yEnd] == ' ') {
			year = atoi(s[pos+1 : yEnd])
			pos = yEnd
		}
	}

	// Optional timezone abbreviation followed by ':' (Cisco "… 18:46:11.123 UTC: …").
	// Only well-known abbreviations are accepted so that an uppercase
	// hostname or tag ("CORE: …") is not mistaken for a zone.
	if pos < len(s) && s[pos] == ' ' {
		zEnd := pos + 1
		for zEnd < len(s) && zEnd-pos-1 < 5 && s[zEnd] >= 'A' && s[zEnd] <= 'Z' {
			zEnd++
		}
		if z := s[pos+1 : zEnd]; knownZones[z] && zEnd < len(s) && s[zEnd] == ':' {
			// Abbreviations are ambiguous (IST, CST); only UTC/GMT change the
			// location, others fall back to the source's configured timezone.
			if z == "UTC" || z == "GMT" {
				loc = time.UTC
			}
			pos = zEnd
		}
	}
	if pos < len(s) && s[pos] == ':' {
		pos++
	}

	if year == 0 {
		return inferYear(month, day, hour, min, sec, nsec, loc, received), pos, true
	}
	return time.Date(year, time.Month(month), day, hour, min, sec, nsec, loc), pos, true
}

// parseClock parses "hh:mm:ss[.frac]".
func parseClock(s string) (hour, min, sec, nsec, n int, ok bool) {
	if len(s) < 8 || s[2] != ':' || s[5] != ':' {
		return 0, 0, 0, 0, 0, false
	}
	if digitsEnd(s, 0) != 2 || digitsEnd(s, 3) != 5 || digitsEnd(s, 6) != 8 {
		return 0, 0, 0, 0, 0, false
	}
	hour, min, sec = atoi(s[0:2]), atoi(s[3:5]), atoi(s[6:8])
	if hour > 23 || min > 59 || sec > 60 {
		return 0, 0, 0, 0, 0, false
	}
	n = 8
	if n < len(s) && s[n] == '.' {
		fEnd := digitsEnd(s, n+1)
		if fEnd > n+1 {
			frac := s[n+1 : fEnd]
			if len(frac) > 9 {
				frac = frac[:9]
			}
			nsec = atoi(frac)
			for i := len(frac); i < 9; i++ {
				nsec *= 10
			}
			n = fEnd
		}
	}
	if sec == 60 { // leap second: clamp
		sec, nsec = 59, 999_999_999
	}
	return hour, min, sec, nsec, n, true
}

// parseISOTimestamp parses an RFC 3339 / ISO 8601 timestamp token as emitted
// by rsyslog's RSYSLOG_ForwardFormat and many appliances.
func parseISOTimestamp(s string, loc *time.Location) (time.Time, int, bool) {
	if len(s) < 19 || s[4] != '-' || s[7] != '-' || (s[10] != 'T' && s[10] != 't') {
		return time.Time{}, 0, false
	}
	end := strings.IndexByte(s, ' ')
	if end < 0 {
		end = len(s)
	}
	tok := strings.TrimSuffix(s[:end], ":")
	if t, err := time.Parse(time.RFC3339Nano, strings.ToUpper(tok)); err == nil {
		return t, end, true
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04:05.999999999", strings.ToUpper(tok), loc); err == nil {
		return t, end, true
	}
	return time.Time{}, 0, false
}

// inferYear picks the year for a year-less timestamp so that it lies close
// to the receive time, handling Dec 31 → Jan 1 rollovers in both directions.
func inferYear(month, day, hour, min, sec, nsec int, loc *time.Location, received time.Time) time.Time {
	if received.IsZero() {
		received = time.Now()
	}
	year := received.In(loc).Year()
	t := time.Date(year, time.Month(month), day, hour, min, sec, nsec, loc)
	switch {
	case t.After(received.Add(7 * 24 * time.Hour)):
		t = time.Date(year-1, time.Month(month), day, hour, min, sec, nsec, loc)
	case t.Before(received.Add(-358 * 24 * time.Hour)):
		t = time.Date(year+1, time.Month(month), day, hour, min, sec, nsec, loc)
	}
	return t
}

// parseTag parses "TAG[PID]: " or "TAG: " at pos and returns the position of MSG.
func parseTag(data string, pos int, e *logentry.Entry) int {
	i := pos
	for i < len(data) && i-pos <= maxTagLen && isTagChar(data[i]) {
		i++
	}
	tagLen := i - pos
	if tagLen == 0 || tagLen > maxTagLen || i >= len(data) {
		return pos
	}
	tag := data[pos:i]
	switch data[i] {
	case ':':
		if i+1 != len(data) && data[i+1] != ' ' {
			return pos // "key:value" in free text, not a tag
		}
		e.AppName = tag
		return skipOneSpace(data, i+1)
	case '[':
		close := strings.IndexByte(data[i+1:], ']')
		if close <= 0 || close > maxPIDLen {
			return pos
		}
		pid := data[i+1 : i+1+close]
		for k := 0; k < len(pid); k++ {
			if pid[k] <= ' ' || pid[k] == '[' || pid[k] >= 0x7f {
				return pos
			}
		}
		j := i + 1 + close + 1
		if j < len(data) && data[j] == ':' {
			j++
		}
		if j < len(data) && data[j] != ' ' {
			return pos
		}
		e.AppName, e.ProcessID = tag, pid
		return skipOneSpace(data, j)
	}
	return pos
}

// isTagChar reports whether c may appear in a TAG. '%' is deliberately
// excluded: Cisco "%FAC-SEV-MNEMONIC:" identifiers vary per message and
// would explode app_name cardinality; they stay in MSG.
func isTagChar(c byte) bool {
	return isAlnum(c) || c == '_' || c == '-' || c == '.' || c == '/'
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func isHostname(tok string) bool {
	if len(tok) > 255 {
		return false
	}
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		if !isAlnum(c) && c != '.' && c != '-' && c != '_' && c != ':' {
			return false
		}
	}
	return true
}

func looksLikeTag(tok string) bool {
	return strings.HasSuffix(tok, ":") || strings.ContainsRune(tok, '[')
}

func nextToken(data string, pos int) (string, int) {
	if pos >= len(data) {
		return "", pos
	}
	i := strings.IndexByte(data[pos:], ' ')
	if i < 0 {
		return data[pos:], len(data)
	}
	return data[pos : pos+i], pos + i
}

func skipSpaces(data string, pos int) int {
	for pos < len(data) && data[pos] == ' ' {
		pos++
	}
	return pos
}

func skipOneSpace(data string, pos int) int {
	if pos < len(data) && data[pos] == ' ' {
		return pos + 1
	}
	return pos
}

func digitsEnd(s string, pos int) int {
	for pos < len(s) && s[pos] >= '0' && s[pos] <= '9' {
		pos++
	}
	return pos
}

func hasPrefixAt(s string, pos int, prefix string) bool {
	return pos <= len(s) && strings.HasPrefix(s[pos:], prefix)
}

// atoi converts a short, pre-validated digit string.
func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}
