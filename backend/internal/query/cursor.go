package query

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ErrInvalidCursor is returned for tampered, stale or mismatched cursors.
var ErrInvalidCursor = errors.New("invalid cursor")

const cursorVersion = 1

type cursorPayload struct {
	V int    `json:"v"`
	T int64  `json:"t"` // next page ends (exclusive) at this time, ns
	Q string `json:"q"` // query hash
}

type cursorCodec struct{ key []byte }

func (c cursorCodec) encode(end time.Time, queryHash string) string {
	payload, _ := json.Marshal(cursorPayload{V: cursorVersion, T: end.UnixNano(), Q: queryHash})
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + c.sign(body)
}

func (c cursorCodec) decode(s, queryHash string) (time.Time, error) {
	body, sig, ok := strings.Cut(s, ".")
	if !ok || len(s) > 512 || !hmac.Equal([]byte(sig), []byte(c.sign(body))) {
		return time.Time{}, ErrInvalidCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return time.Time{}, ErrInvalidCursor
	}
	var p cursorPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.V != cursorVersion || p.Q != queryHash {
		return time.Time{}, ErrInvalidCursor
	}
	return time.Unix(0, p.T).UTC(), nil
}

func (c cursorCodec) sign(body string) string {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:16])
}

// hashQuery binds a cursor to the tenant, filter, native query, projection
// and range start so it cannot be replayed against a different search.
func hashQuery(parts ...any) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	for _, p := range parts {
		_ = enc.Encode(p)
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}
