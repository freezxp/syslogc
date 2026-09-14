package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (OWASP-recommended range; tuned for ~100 ms).
const (
	argonMemoryKiB = 64 * 1024
	argonTime      = 3
	argonThreads   = 2
	argonKeyLen    = 32
	argonSaltLen   = 16
)

const (
	MinPasswordLength = 12
	MaxPasswordBytes  = 256
)

var errInvalidHash = errors.New("invalid password hash")

// HashPassword returns an Argon2id hash in PHC string format.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks password against a PHC hash in constant time. It
// reports whether the hash uses outdated parameters and should be replaced.
func VerifyPassword(password, encoded string) (ok, needsRehash bool, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, false, errInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, false, errInvalidHash
	}
	var mem, iters uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &iters, &threads); err != nil {
		return false, false, errInvalidHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, false, errInvalidHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > 64 {
		return false, false, errInvalidHash
	}
	got := argon2.IDKey([]byte(password), salt, iters, mem, threads, uint32(len(want))) //nolint:gosec // len bounded above
	ok = subtle.ConstantTimeCompare(got, want) == 1
	needsRehash = mem != argonMemoryKiB || iters != argonTime || threads != argonThreads || len(want) != argonKeyLen
	return ok, needsRehash, nil
}

// dummyHash is verified against for unknown users so that login timing does
// not reveal whether a username exists.
var dummyHash, _ = HashPassword("syslogc-dummy-password-for-timing")

// ValidatePasswordPolicy enforces NIST SP 800-63B style rules: length only,
// no composition rules, reject a few trivially guessable passwords.
func ValidatePasswordPolicy(password, username string) error {
	if len(password) > MaxPasswordBytes {
		return fmt.Errorf("password must be at most %d bytes", MaxPasswordBytes)
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	lower := strings.ToLower(password)
	if username != "" && strings.Contains(lower, strings.ToLower(username)) {
		return errors.New("password must not contain the username")
	}
	for _, weak := range commonPasswords {
		if lower == weak {
			return errors.New("password is too common")
		}
	}
	return nil
}

var commonPasswords = []string{
	"password1234", "123456789012", "qwertyuiopas", "adminadminadmin", "changemechangeme",
	"syslogcsyslogc", "passwordpassword", "letmeinletmein", "welcome12345", "iloveyou1234",
}

// randomToken returns n random bytes base64url-encoded without padding.
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
