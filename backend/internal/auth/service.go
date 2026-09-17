package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"github.com/freezxp/syslogc/backend/internal/metadata"
)

// Errors returned to callers; handlers map them to HTTP problems.
var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrSessionExpired     = errors.New("session expired")
	ErrUnauthenticated    = errors.New("authentication required")
	ErrRateLimited        = errors.New("too many attempts")
	ErrInvalidScopes      = errors.New("invalid API key scopes")
)

// PrincipalKind distinguishes users from API keys.
type PrincipalKind string

const (
	KindUser   PrincipalKind = "user"
	KindAPIKey PrincipalKind = "api_key"
)

// Principal is the authenticated caller.
type Principal struct {
	Kind        PrincipalKind
	UserID      uuid.UUID
	Username    string
	Tenant      string
	Role        string
	Permissions PermissionSet
	User        *metadata.User
	// Session fields (KindUser via cookie).
	SessionHash []byte
	CSRFToken   string
	// API key fields.
	APIKeyID uuid.UUID
}

// Can reports whether the principal holds permission p.
func (p *Principal) Can(perm Permission) bool { return p != nil && p.Permissions.Has(perm) }

type principalKey struct{}

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal stored in ctx, or nil.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}

// Config configures the service.
type Config struct {
	SessionTTL         time.Duration
	SessionIdleTimeout time.Duration
}

// Service implements authentication operations.
type Service struct {
	store metadata.Store
	cfg   Config
	log   *slog.Logger
	now   func() time.Time

	limiter *loginLimiter
}

func NewService(store metadata.Store, cfg Config, log *slog.Logger) *Service {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 12 * time.Hour
	}
	if cfg.SessionIdleTimeout <= 0 {
		cfg.SessionIdleTimeout = time.Hour
	}
	return &Service{store: store, cfg: cfg, log: log, now: time.Now, limiter: newLoginLimiter()}
}

// LoginResult is returned on successful login.
type LoginResult struct {
	Principal *Principal
	// Token is the raw session token for the cookie.
	Token     string
	ExpiresAt time.Time
}

// Login verifies credentials and creates a session.
func (s *Service) Login(ctx context.Context, username, password, ip, userAgent string) (*LoginResult, error) {
	if !s.limiter.allow(ip, username, s.now()) {
		return nil, ErrRateLimited
	}
	u, err := s.store.UserByUsername(ctx, username)
	if err != nil && !errors.Is(err, metadata.ErrNotFound) {
		return nil, err
	}
	hash := dummyHash
	if u != nil {
		hash = u.PasswordHash
	}
	ok, needsRehash, verr := VerifyPassword(password, hash)
	if u == nil || u.Disabled || !ok || verr != nil {
		s.limiter.failure(ip, username, s.now())
		return nil, ErrInvalidCredentials
	}
	s.limiter.success(username)

	if needsRehash {
		if h, err := HashPassword(password); err == nil {
			_ = s.store.UpdatePassword(ctx, u.ID, h, u.MustChangePassword)
		}
	}
	now := s.now().UTC()
	token, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	csrf, err := randomToken(24)
	if err != nil {
		return nil, err
	}
	sess := &metadata.Session{
		TokenHash: hashToken(token), UserID: u.ID, CSRFToken: csrf,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(s.cfg.SessionTTL),
		IP: truncate(ip, 64), UserAgent: truncate(userAgent, 256),
	}
	if err := s.store.CreateSession(ctx, sess); err != nil {
		return nil, err
	}
	_ = s.store.TouchLogin(ctx, u.ID, now)
	u.LastLoginAt = &now
	return &LoginResult{Principal: userPrincipal(u, sess), Token: token, ExpiresAt: sess.ExpiresAt}, nil
}

// AuthenticateSession resolves a session token to a principal, enforcing
// absolute and idle expiry.
func (s *Service) AuthenticateSession(ctx context.Context, token string) (*Principal, error) {
	if token == "" {
		return nil, ErrUnauthenticated
	}
	h := hashToken(token)
	sess, err := s.store.SessionByHash(ctx, h)
	if errors.Is(err, metadata.ErrNotFound) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	if now.After(sess.ExpiresAt) || now.Sub(sess.LastSeenAt) > s.cfg.SessionIdleTimeout {
		_ = s.store.DeleteSession(ctx, h)
		return nil, ErrSessionExpired
	}
	u, err := s.store.UserByID(ctx, sess.UserID)
	if err != nil || u.Disabled {
		_ = s.store.DeleteSession(ctx, h)
		return nil, ErrUnauthenticated
	}
	// Sliding idle window; write at most once a minute.
	if now.Sub(sess.LastSeenAt) > time.Minute {
		_ = s.store.TouchSession(ctx, h, now)
	}
	return userPrincipal(u, sess), nil
}

// Logout revokes the session of p.
func (s *Service) Logout(ctx context.Context, p *Principal) error {
	if p == nil || p.SessionHash == nil {
		return nil
	}
	return s.store.DeleteSession(ctx, p.SessionHash)
}

// ChangePassword verifies the current password, applies the policy, stores
// the new hash and revokes all other sessions of the user.
func (s *Service) ChangePassword(ctx context.Context, p *Principal, current, next string) error {
	if p == nil || p.Kind != KindUser {
		return ErrUnauthenticated
	}
	u, err := s.store.UserByID(ctx, p.UserID)
	if err != nil {
		return err
	}
	if ok, _, _ := VerifyPassword(current, u.PasswordHash); !ok {
		return ErrInvalidCredentials
	}
	if err := ValidatePasswordPolicy(next, u.Username); err != nil {
		return &PolicyError{Err: err}
	}
	if current == next {
		return &PolicyError{Err: errors.New("new password must differ from the current password")}
	}
	h, err := HashPassword(next)
	if err != nil {
		return err
	}
	if err := s.store.UpdatePassword(ctx, u.ID, h, false); err != nil {
		return err
	}
	return s.store.DeleteUserSessions(ctx, u.ID, p.SessionHash)
}

// PolicyError wraps a password policy violation.
type PolicyError struct{ Err error }

func (e *PolicyError) Error() string { return e.Err.Error() }
func (e *PolicyError) Unwrap() error { return e.Err }

// ---- API keys ---------------------------------------------------------

const apiKeyPrefix = "slc_"

// CreateAPIKey creates a key for p. Scopes must be API-key scopes the owner
// holds. It returns the stored key and the full secret, shown only once.
func (s *Service) CreateAPIKey(ctx context.Context, p *Principal, name string, scopes []Permission, expiresAt *time.Time) (*metadata.APIKey, string, error) {
	if len(scopes) == 0 {
		return nil, "", ErrInvalidScopes
	}
	seen := map[Permission]bool{}
	var scopeNames []string
	for _, sc := range scopes {
		if !apiKeyScopes.Has(sc) || (!p.Can(sc) && sc != PermLogsIngest) || seen[sc] {
			return nil, "", fmt.Errorf("%w: %q", ErrInvalidScopes, sc)
		}
		seen[sc] = true
		scopeNames = append(scopeNames, string(sc))
	}
	keyID, err := randomToken(12)
	if err != nil {
		return nil, "", err
	}
	keyID = strings.NewReplacer("-", "x", "_", "y").Replace(keyID)
	secret, err := randomToken(32)
	if err != nil {
		return nil, "", err
	}
	k := &metadata.APIKey{
		KeyID: keyID, SecretHash: hashToken(secret), Name: name, Tenant: p.Tenant,
		OwnerUserID: &p.UserID, OwnerName: p.Username, Scopes: scopeNames, CreatedBy: &p.UserID, ExpiresAt: expiresAt,
	}
	if err := s.store.CreateAPIKey(ctx, k); err != nil {
		return nil, "", err
	}
	return k, apiKeyPrefix + keyID + "_" + secret, nil
}

// AuthenticateAPIKey resolves a bearer token. A key's permissions are its
// scopes limited to what its owner currently holds (logs:ingest excepted).
func (s *Service) AuthenticateAPIKey(ctx context.Context, token string) (*Principal, error) {
	rest, ok := strings.CutPrefix(token, apiKeyPrefix)
	if !ok {
		return nil, ErrUnauthenticated
	}
	keyID, secret, ok := strings.Cut(rest, "_")
	if !ok || keyID == "" || secret == "" {
		return nil, ErrUnauthenticated
	}
	k, err := s.store.APIKeyByKeyID(ctx, keyID)
	if errors.Is(err, metadata.ErrNotFound) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	if subtle.ConstantTimeCompare(hashToken(secret), k.SecretHash) != 1 || k.RevokedAt != nil ||
		(k.ExpiresAt != nil && now.After(*k.ExpiresAt)) {
		return nil, ErrUnauthenticated
	}
	p := &Principal{Kind: KindAPIKey, Tenant: k.Tenant, APIKeyID: k.ID, Username: "apikey:" + k.Name, Permissions: PermissionSet{}}
	var ownerPerms PermissionSet
	if k.OwnerUserID != nil {
		u, err := s.store.UserByID(ctx, *k.OwnerUserID)
		if err != nil || u.Disabled {
			return nil, ErrUnauthenticated
		}
		p.UserID, p.Role, ownerPerms = u.ID, u.Role, RolePermissions(u.Role)
	}
	for _, sc := range k.Scopes {
		perm := Permission(sc)
		if perm == PermLogsIngest || ownerPerms.Has(perm) {
			p.Permissions[perm] = struct{}{}
		}
	}
	if k.LastUsedAt == nil || now.Sub(*k.LastUsedAt) > time.Minute {
		_ = s.store.TouchAPIKey(ctx, k.ID, now)
	}
	return p, nil
}

// ListAPIKeys lists keys: all tenant keys for key managers, own keys otherwise.
func (s *Service) ListAPIKeys(ctx context.Context, p *Principal) ([]metadata.APIKey, error) {
	var owner *uuid.UUID
	if !p.Can(PermAPIKeysManage) {
		owner = &p.UserID
	}
	return s.store.ListAPIKeys(ctx, p.Tenant, owner)
}

// RevokeAPIKey revokes a key the principal may manage.
func (s *Service) RevokeAPIKey(ctx context.Context, p *Principal, id uuid.UUID) error {
	var owner *uuid.UUID
	if !p.Can(PermAPIKeysManage) {
		owner = &p.UserID
	}
	return s.store.RevokeAPIKey(ctx, p.Tenant, id, owner, s.now().UTC())
}

// ---- Bootstrap --------------------------------------------------------

// Bootstrap creates the initial admin account when no users exist. The
// password comes from passwordFile, or is generated and returned so it can
// be printed once (the user must change it at first login).
func (s *Service) Bootstrap(ctx context.Context, username, passwordFile string) (generated string, err error) {
	n, err := s.store.CountUsers(ctx)
	if err != nil || n > 0 {
		return "", err
	}
	if username == "" {
		username = "admin"
	}
	password, mustChange := "", false
	if passwordFile != "" {
		data, err := os.ReadFile(passwordFile) //nolint:gosec // operator-provided path
		if err != nil {
			return "", fmt.Errorf("bootstrap admin password file: %w", err)
		}
		password = strings.TrimSpace(string(data))
		if err := ValidatePasswordPolicy(password, username); err != nil {
			return "", fmt.Errorf("bootstrap admin password: %w", err)
		}
	} else {
		password, err = randomToken(15)
		if err != nil {
			return "", err
		}
		generated, mustChange = password, true
	}
	h, err := HashPassword(password)
	if err != nil {
		return "", err
	}
	err = s.store.CreateUser(ctx, &metadata.User{
		Tenant: "default", Username: username, DisplayName: "Administrator",
		PasswordHash: h, Role: RoleAdmin, MustChangePassword: mustChange,
	})
	if errors.Is(err, metadata.ErrConflict) {
		return "", nil // another node bootstrapped concurrently
	}
	return generated, err
}

// ---- helpers ----------------------------------------------------------

func userPrincipal(u *metadata.User, sess *metadata.Session) *Principal {
	p := &Principal{
		Kind: KindUser, UserID: u.ID, Username: u.Username, Tenant: u.Tenant, Role: u.Role,
		Permissions: RolePermissions(u.Role), User: u,
	}
	if sess != nil {
		p.SessionHash, p.CSRFToken = sess.TokenHash, sess.CSRFToken
	}
	if u.MustChangePassword {
		// Until the password is changed only identity endpoints are usable.
		p.Permissions = PermissionSet{}
	}
	return p
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// loginLimiter rate-limits login attempts per client IP and applies an
// exponential delay per username after consecutive failures.
type loginLimiter struct {
	mu       sync.Mutex
	perIP    map[string]*rate.Limiter
	failures map[string]*failureState
}

type failureState struct {
	count int
	until time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{perIP: map[string]*rate.Limiter{}, failures: map[string]*failureState{}}
}

func (l *loginLimiter) allow(ip, username string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.perIP) > 100_000 {
		l.perIP = map[string]*rate.Limiter{}
	}
	lim, ok := l.perIP[ip]
	if !ok {
		lim = rate.NewLimiter(rate.Every(6*time.Second), 10)
		l.perIP[ip] = lim
	}
	if !lim.AllowN(now, 1) {
		return false
	}
	if f, ok := l.failures[strings.ToLower(username)]; ok && now.Before(f.until) {
		return false
	}
	return true
}

func (l *loginLimiter) failure(_, username string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := strings.ToLower(username)
	f, ok := l.failures[key]
	if !ok {
		if len(l.failures) > 100_000 {
			l.failures = map[string]*failureState{}
		}
		f = &failureState{}
		l.failures[key] = f
	}
	f.count++
	if f.count >= 5 {
		delay := time.Second << min(f.count-5, 10) // 1s … ~17m
		f.until = now.Add(min(delay, 15*time.Minute))
	}
}

func (l *loginLimiter) success(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, strings.ToLower(username))
}
