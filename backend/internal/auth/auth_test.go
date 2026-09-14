package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/freezxp/syslogc/backend/internal/metadata"
	"github.com/freezxp/syslogc/backend/internal/metadata/postgres/pgtest"
)

func TestPasswordHashing(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Errorf("unexpected hash format %s", h)
	}
	ok, rehash, err := VerifyPassword("correct horse battery staple", h)
	if !ok || rehash || err != nil {
		t.Errorf("verify correct: %v %v %v", ok, rehash, err)
	}
	if ok, _, _ := VerifyPassword("wrong", h); ok {
		t.Error("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse battery staple")
	if h == h2 {
		t.Error("salt not random")
	}
	weak := "$argon2id$v=19$m=1024,t=1,p=1$c2FsdHNhbHRzYWx0$" + strings.Split(h, "$")[5]
	if _, rehash, err := VerifyPassword("x", weak); err != nil || !rehash {
		t.Errorf("outdated parameters not flagged: %v %v", rehash, err)
	}
	for _, bad := range []string{"", "plain", "$bcrypt$x$y$z$w", "$argon2id$v=19$m=x$a$b"} {
		if _, _, err := VerifyPassword("x", bad); err == nil {
			t.Errorf("malformed hash %q accepted", bad)
		}
	}
}

func TestPasswordPolicy(t *testing.T) {
	for pw, ok := range map[string]bool{
		"short":                          false,
		"exactly12chr":                   true,
		"Password1234":                   false, // common
		"alicealice-secret":              false, // contains username
		strings.Repeat("x", 257):         false,
		"ünïcødé-pässwörd":               true,
		"a long passphrase with spaces ": true,
	} {
		if err := ValidatePasswordPolicy(pw, "alice"); (err == nil) != ok {
			t.Errorf("policy(%q) = %v, want ok=%v", pw, err, ok)
		}
	}
}

func TestRolePermissions(t *testing.T) {
	if RolePermissions(RoleViewer).Has(PermLogsExport) || RolePermissions(RoleViewer).Has(PermLogsQueryNative) {
		t.Error("viewer may export or run native queries")
	}
	if !RolePermissions(RoleOperator).Has(PermLogsExport) || RolePermissions(RoleOperator).Has(PermUsersManage) {
		t.Error("operator permissions wrong")
	}
	for p := range RolePermissions(RoleOperator) {
		if !RolePermissions(RoleAdmin).Has(p) {
			t.Errorf("admin lacks operator permission %s", p)
		}
	}
	if len(RolePermissions("root")) != 0 || ValidRole("root") {
		t.Error("unknown role has permissions")
	}
}

func newTestService(t *testing.T) (*Service, metadata.Store) {
	t.Helper()
	store := pgtest.Open(t)
	return NewService(store, Config{SessionTTL: time.Hour, SessionIdleTimeout: 10 * time.Minute},
		slog.New(slog.NewTextHandler(io.Discard, nil))), store
}

func createUser(t *testing.T, store metadata.Store, name, role, password string) *metadata.User {
	t.Helper()
	h, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	u := &metadata.User{Tenant: "default", Username: name, PasswordHash: h, Role: role}
	if err := store.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestLoginSessionLifecycle(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	createUser(t, store, "alice", RoleOperator, "alice-password-1")

	if _, err := svc.Login(ctx, "alice", "nope", "10.0.0.1", "test"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong password: %v", err)
	}
	if _, err := svc.Login(ctx, "nobody", "whatever", "10.0.0.1", "test"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("unknown user: %v", err)
	}
	res, err := svc.Login(ctx, "ALICE", "alice-password-1", "10.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	if res.Token == "" || res.Principal.CSRFToken == "" || !res.Principal.Can(PermLogsExport) {
		t.Fatalf("login result incomplete: %+v", res.Principal)
	}

	p, err := svc.AuthenticateSession(ctx, res.Token)
	if err != nil || p.Username != "alice" || p.CSRFToken != res.Principal.CSRFToken {
		t.Fatalf("AuthenticateSession: %+v, %v", p, err)
	}
	if _, err := svc.AuthenticateSession(ctx, res.Token+"x"); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("tampered token: %v", err)
	}

	// Idle timeout.
	svc.now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	if _, err := svc.AuthenticateSession(ctx, res.Token); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("idle session: %v", err)
	}
	svc.now = time.Now

	res2, _ := svc.Login(ctx, "alice", "alice-password-1", "10.0.0.1", "test")
	p2, _ := svc.AuthenticateSession(ctx, res2.Token)
	if err := svc.Logout(ctx, p2); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateSession(ctx, res2.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("after logout: %v", err)
	}
}

func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	createUser(t, store, "bob", RoleViewer, "bob-password-12")
	a, _ := svc.Login(ctx, "bob", "bob-password-12", "10.0.0.2", "a")
	b, _ := svc.Login(ctx, "bob", "bob-password-12", "10.0.0.2", "b")
	pa, _ := svc.AuthenticateSession(ctx, a.Token)

	var pe *PolicyError
	if err := svc.ChangePassword(ctx, pa, "bob-password-12", "short"); !errors.As(err, &pe) {
		t.Errorf("policy violation: %v", err)
	}
	if err := svc.ChangePassword(ctx, pa, "wrong", "a-much-better-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong current password: %v", err)
	}
	if err := svc.ChangePassword(ctx, pa, "bob-password-12", "a-much-better-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateSession(ctx, a.Token); err != nil {
		t.Errorf("current session revoked: %v", err)
	}
	if _, err := svc.AuthenticateSession(ctx, b.Token); err == nil {
		t.Error("other session survived password change")
	}
	if _, err := svc.Login(ctx, "bob", "a-much-better-password", "10.0.0.3", "c"); err != nil {
		t.Errorf("login with new password: %v", err)
	}
}

func TestLoginRateLimiting(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	createUser(t, store, "carol", RoleViewer, "carol-password-1")
	for i := range 5 {
		_, _ = svc.Login(ctx, "carol", "bad", "10.9.0."+string(rune('1'+i)), "t")
	}
	if _, err := svc.Login(ctx, "carol", "carol-password-1", "10.9.1.1", "t"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("correct password during lockout: %v", err)
	}
	l := newLoginLimiter()
	now := time.Now()
	allowed := 0
	for range 20 {
		if l.allow("1.2.3.4", "u", now) {
			allowed++
		}
	}
	if allowed != 10 {
		t.Errorf("per-IP burst allowed %d, want 10", allowed)
	}
}

func TestAPIKeys(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	createUser(t, store, "ops", RoleOperator, "ops-password-12")
	res, _ := svc.Login(ctx, "ops", "ops-password-12", "10.0.0.4", "t")
	p := res.Principal

	if _, _, err := svc.CreateAPIKey(ctx, p, "bad", []Permission{PermUsersManage}, nil); !errors.Is(err, ErrInvalidScopes) {
		t.Errorf("non-key scope: %v", err)
	}
	key, secret, err := svc.CreateAPIKey(ctx, p, "vector", []Permission{PermLogsIngest, PermLogsSearch}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "slc_"+key.KeyID+"_") {
		t.Fatalf("secret format %q", secret)
	}
	kp, err := svc.AuthenticateAPIKey(ctx, secret)
	if err != nil || kp.Kind != KindAPIKey || !kp.Can(PermLogsIngest) || !kp.Can(PermLogsSearch) || kp.Can(PermLogsExport) {
		t.Fatalf("AuthenticateAPIKey: %+v, %v", kp, err)
	}
	for _, bad := range []string{"", "slc_", "slc_x_y", secret + "x", strings.Replace(secret, "slc_", "abc_", 1)} {
		if _, err := svc.AuthenticateAPIKey(ctx, bad); err == nil {
			t.Errorf("bad key %q accepted", bad)
		}
	}
	keys, _ := svc.ListAPIKeys(ctx, p)
	if len(keys) != 1 {
		t.Errorf("list: %d keys", len(keys))
	}
	if err := svc.RevokeAPIKey(ctx, p, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateAPIKey(ctx, secret); err == nil {
		t.Error("revoked key accepted")
	}

	past := time.Now().Add(-time.Hour)
	_, expired, _ := svc.CreateAPIKey(ctx, p, "old", []Permission{PermLogsIngest}, &past)
	if _, err := svc.AuthenticateAPIKey(ctx, expired); err == nil {
		t.Error("expired key accepted")
	}
}

func TestBootstrap(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	generated, err := svc.Bootstrap(ctx, "", "")
	if err != nil || len(generated) < 16 {
		t.Fatalf("bootstrap: %q, %v", generated, err)
	}
	u, err := store.UserByUsername(ctx, "admin")
	if err != nil || u.Role != RoleAdmin || !u.MustChangePassword {
		t.Fatalf("admin: %+v, %v", u, err)
	}
	res, err := svc.Login(ctx, "admin", generated, "10.0.0.5", "t")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Principal.Permissions) != 0 {
		t.Error("must-change-password principal has permissions")
	}
	if again, err := svc.Bootstrap(ctx, "", ""); err != nil || again != "" {
		t.Errorf("second bootstrap: %q, %v", again, err)
	}

	svc2, store2 := newTestService(t)
	file := filepath.Join(t.TempDir(), "pw")
	_ = os.WriteFile(file, []byte("from-a-secret-file\n"), 0o600)
	if gen, err := svc2.Bootstrap(ctx, "root-admin", file); err != nil || gen != "" {
		t.Fatalf("file bootstrap: %q, %v", gen, err)
	}
	if u, _ := store2.UserByUsername(ctx, "root-admin"); u == nil || u.MustChangePassword {
		t.Errorf("file-bootstrapped admin: %+v", u)
	}
}
