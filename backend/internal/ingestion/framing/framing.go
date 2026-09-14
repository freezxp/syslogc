// Package framing splits a syslog byte stream (TCP/TLS) into messages
// according to RFC 6587: octet counting ("MSG-LEN SP SYSLOG-MSG") or
// non-transparent framing (LF- or NUL-delimited).
package framing

import (
	"bufio"
	"errors"
	"io"
)

// Mode selects the framing method.
type Mode uint8

const (
	// Auto detects per message: a leading non-zero digit sequence followed
	// by a space means octet counting, anything else is LF-delimited.
	Auto Mode = iota
	OctetCounting
	LF
	NUL
)

// ParseMode maps a configuration value to a Mode.
func ParseMode(s string) (Mode, bool) {
	switch s {
	case "auto", "":
		return Auto, true
	case "octet_counting":
		return OctetCounting, true
	case "lf":
		return LF, true
	case "nul":
		return NUL, true
	}
	return Auto, false
}

// ErrInvalidFrame is returned in OctetCounting mode for a malformed length.
var ErrInvalidFrame = errors.New("invalid octet-counting frame")

const (
	readBufferSize = 64 << 10
	// maxLengthDigits bounds MSG-LEN; larger frames are treated as oversize.
	maxLengthDigits = 10
)

// Decoder reads messages from a stream. It is not safe for concurrent use.
type Decoder struct {
	r    *bufio.Reader
	mode Mode
	max  int
	buf  []byte
}

// NewDecoder returns a decoder that truncates messages longer than max bytes.
func NewDecoder(r io.Reader, mode Mode, max int) *Decoder {
	size := readBufferSize
	if max+16 < size {
		size = max + 16
	}
	return &Decoder{r: bufio.NewReaderSize(r, size), mode: mode, max: max}
}

// Next returns the next message without its delimiter. The returned slice
// is only valid until the next call. truncated reports that the message
// exceeded the maximum size and was cut. At the end of the stream Next
// returns io.EOF (a final unterminated message is returned first).
func (d *Decoder) Next() (msg []byte, truncated bool, err error) {
	// Skip delimiters between frames (e.g. a trailing LF after an octet-counted frame).
	for {
		c, err := d.r.ReadByte()
		if err != nil {
			return nil, false, err
		}
		if c != '\n' && c != '\r' && c != 0 && c != ' ' {
			_ = d.r.UnreadByte()
			break
		}
	}

	switch d.mode {
	case LF:
		return d.delimited('\n', nil)
	case NUL:
		return d.delimited(0, nil)
	}

	c, _ := d.r.ReadByte()
	if c < '1' || c > '9' {
		_ = d.r.UnreadByte()
		if d.mode == OctetCounting {
			return nil, false, ErrInvalidFrame
		}
		return d.delimited('\n', nil)
	}

	// Possible MSG-LEN: collect digits.
	var digits [maxLengthDigits + 1]byte
	digits[0] = c
	n := 1
	length := int64(c - '0')
	for {
		c, err := d.r.ReadByte()
		if err != nil {
			// Stream ended inside what looked like a length: treat as text.
			if n > 0 && err == io.EOF {
				return d.finishPrefix(digits[:n])
			}
			return nil, false, err
		}
		switch {
		case c >= '0' && c <= '9' && n < maxLengthDigits:
			digits[n] = c
			n++
			length = length*10 + int64(c-'0')
			continue
		case c == ' ':
			return d.octet(length)
		}
		// Not a length prefix ("123: message" or plain text starting with digits).
		if d.mode == OctetCounting {
			return nil, false, ErrInvalidFrame
		}
		digits[n] = c
		n++
		if c == '\n' {
			d.buf = append(d.buf[:0], digits[:n-1]...)
			return d.limit(d.buf)
		}
		return d.delimited('\n', digits[:n])
	}
}

func (d *Decoder) finishPrefix(prefix []byte) ([]byte, bool, error) {
	d.buf = append(d.buf[:0], prefix...)
	return d.limit(d.buf)
}

// octet reads a frame of exactly length bytes.
func (d *Decoder) octet(length int64) ([]byte, bool, error) {
	take := length
	truncated := false
	if take > int64(d.max) {
		take = int64(d.max)
		truncated = true
	}
	if cap(d.buf) < int(take) {
		d.buf = make([]byte, take)
	}
	d.buf = d.buf[:take]
	if _, err := io.ReadFull(d.r, d.buf); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			err = io.EOF
		}
		return nil, false, err
	}
	if truncated {
		if _, err := d.r.Discard(int(length - take)); err != nil {
			return d.trimmed(d.buf), true, nil
		}
	}
	return d.trimmed(d.buf), truncated, nil
}

// delimited reads until delim, prepending prefix. Oversize messages are
// truncated to max and the rest of the frame is discarded. A final
// unterminated message is returned; the stream error surfaces on the next call.
func (d *Decoder) delimited(delim byte, prefix []byte) ([]byte, bool, error) {
	if len(prefix) == 0 {
		// Fast path: the whole line is buffered → return it without copying.
		line, err := d.r.ReadSlice(delim)
		switch {
		case err == nil:
			return d.limit(line[:len(line)-1])
		case !errors.Is(err, bufio.ErrBufferFull):
			if len(line) == 0 {
				return nil, false, err
			}
			return d.limit(line)
		}
		d.buf = append(d.buf[:0], line...)
	} else {
		d.buf = append(d.buf[:0], prefix...)
	}
	for {
		if len(d.buf) > d.max {
			d.buf = d.buf[:d.max]
			_ = d.skipUntil(delim) // any stream error surfaces on the next call
			return d.trimmed(d.buf), true, nil
		}
		line, err := d.r.ReadSlice(delim)
		d.buf = append(d.buf, line...)
		switch {
		case err == nil:
			return d.limit(d.buf[:len(d.buf)-1])
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case len(d.buf) == 0:
			return nil, false, err
		default:
			return d.limit(d.buf)
		}
	}
}

func (d *Decoder) limit(b []byte) ([]byte, bool, error) {
	if len(b) > d.max {
		return d.trimmed(b[:d.max]), true, nil
	}
	return d.trimmed(b), false, nil
}

func (d *Decoder) skipUntil(delim byte) error {
	for {
		_, err := d.r.ReadSlice(delim)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return err
		}
	}
}

// trimmed removes a trailing CR, LF or NUL.
func (d *Decoder) trimmed(b []byte) []byte {
	for len(b) > 0 {
		switch b[len(b)-1] {
		case '\r', '\n', 0:
			b = b[:len(b)-1]
			continue
		}
		break
	}
	return b
}
