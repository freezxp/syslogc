package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/freezxp/syslogc/backend/internal/metadata"
	. "github.com/freezxp/syslogc/backend/internal/metadata/postgres"
	"github.com/freezxp/syslogc/backend/internal/metadata/postgres/pgtest"
)

func openTestStore(t *testing.T) *Store { return pgtest.Open(t) }

func TestMigrationsAreIdempotent(t *testing.T) {
	_, dsn := pgtest.OpenDSN(t)
	again, err := Open(context.Background(), dsn, 2, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("reopening migrated database: %v", err)
	}
	defer again.Close()
}

func TestUsersAndSessions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	u := &metadata.User{Tenant: "default", Username: "Alice", PasswordHash: "h", Role: "operator"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, &metadata.User{Tenant: "default", Username: "alice", PasswordHash: "h", Role: "viewer"}); !errors.Is(err, metadata.ErrConflict) {
		t.Errorf("case-insensitive duplicate username: %v", err)
	}
	got, err := s.UserByUsername(ctx, "ALICE")
	if err != nil || got.ID != u.ID || got.Role != "operator" {
		t.Fatalf("UserByUsername = %+v, %v", got, err)
	}
	if n, _ := s.CountUsers(ctx); n != 1 {
		t.Errorf("CountUsers = %d", n)
	}
	if err := s.UpdatePassword(ctx, u.ID, "h2", true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.UserByID(ctx, u.ID); got.PasswordHash != "h2" || !got.MustChangePassword {
		t.Errorf("password not updated: %+v", got)
	}
	if _, err := s.UserByID(ctx, uuid.New()); !errors.Is(err, metadata.ErrNotFound) {
		t.Errorf("missing user: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	for i, h := range [][]byte{{1}, {2}, {3}} {
		exp := now.Add(time.Hour)
		if i == 2 {
			exp = now.Add(-time.Minute)
		}
		if err := s.CreateSession(ctx, &metadata.Session{TokenHash: h, UserID: u.ID, CSRFToken: "c", CreatedAt: now, LastSeenAt: now, ExpiresAt: exp}); err != nil {
			t.Fatal(err)
		}
	}
	ss, err := s.SessionByHash(ctx, []byte{1})
	if err != nil || ss.UserID != u.ID {
		t.Fatalf("SessionByHash: %+v %v", ss, err)
	}
	if n, _ := s.DeleteExpiredSessions(ctx, now); n != 1 {
		t.Errorf("expired sessions deleted = %d", n)
	}
	if err := s.DeleteUserSessions(ctx, u.ID, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionByHash(ctx, []byte{2}); !errors.Is(err, metadata.ErrNotFound) {
		t.Errorf("other session survived: %v", err)
	}
	if _, err := s.SessionByHash(ctx, []byte{1}); err != nil {
		t.Errorf("excepted session deleted: %v", err)
	}
}

func TestAPIKeys(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := &metadata.User{Tenant: "default", Username: "bob", PasswordHash: "h", Role: "operator"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	k := &metadata.APIKey{KeyID: "abc", SecretHash: []byte{9}, Name: "shipper", Tenant: "default", OwnerUserID: &u.ID, Scopes: []string{"logs:ingest"}, CreatedBy: &u.ID}
	if err := s.CreateAPIKey(ctx, k); err != nil {
		t.Fatal(err)
	}
	got, err := s.APIKeyByKeyID(ctx, "abc")
	if err != nil || got.OwnerName != "bob" || got.Scopes[0] != "logs:ingest" {
		t.Fatalf("APIKeyByKeyID = %+v, %v", got, err)
	}
	other := uuid.New()
	if err := s.RevokeAPIKey(ctx, "default", k.ID, &other, time.Now()); !errors.Is(err, metadata.ErrNotFound) {
		t.Errorf("revoke by non-owner: %v", err)
	}
	if err := s.RevokeAPIKey(ctx, "default", k.ID, &u.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if keys, _ := s.ListAPIKeys(ctx, "default", nil); len(keys) != 0 {
		t.Errorf("revoked key listed: %v", keys)
	}
}

func TestSavedSearches(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	alice := &metadata.User{Tenant: "default", Username: "alice", PasswordHash: "h", Role: "operator"}
	bob := &metadata.User{Tenant: "default", Username: "bob", PasswordHash: "h", Role: "viewer"}
	for _, u := range []*metadata.User{alice, bob} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	q := json.RawMessage(`{"filter":{"op":"eq","field":"severity","value":"error"}}`)
	private := &metadata.SavedSearch{Tenant: "default", OwnerID: alice.ID, Name: "Errors", Query: q, Visibility: "private"}
	shared := &metadata.SavedSearch{Tenant: "default", OwnerID: alice.ID, Name: "VPN failures", Query: q, Visibility: "shared", Columns: []string{"message"}}
	for _, ss := range []*metadata.SavedSearch{private, shared} {
		if err := s.CreateSavedSearch(ctx, ss); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateSavedSearch(ctx, &metadata.SavedSearch{Tenant: "default", OwnerID: alice.ID, Name: "errors", Query: q, Visibility: "private"}); !errors.Is(err, metadata.ErrConflict) {
		t.Errorf("duplicate name for owner: %v", err)
	}

	list, err := s.ListSavedSearches(ctx, metadata.ListSavedSearches{Tenant: "default", ViewerID: bob.ID})
	if err != nil || len(list) != 1 || list[0].Name != "VPN failures" || list[0].OwnerUsername != "alice" {
		t.Fatalf("bob sees %+v, %v", list, err)
	}
	list, _ = s.ListSavedSearches(ctx, metadata.ListSavedSearches{Tenant: "default", ViewerID: alice.ID, Query: "vpn"})
	if len(list) != 1 {
		t.Errorf("search filter: %d results", len(list))
	}
	list, _ = s.ListSavedSearches(ctx, metadata.ListSavedSearches{Tenant: "default", ViewerID: alice.ID, Limit: 1})
	next, _ := s.ListSavedSearches(ctx, metadata.ListSavedSearches{Tenant: "default", ViewerID: alice.ID, Limit: 1, AfterName: list[0].Name, AfterID: list[0].ID})
	if len(list) != 1 || len(next) != 1 || list[0].ID == next[0].ID {
		t.Errorf("keyset pagination broken: %v / %v", list, next)
	}

	shared.Name = "VPN tunnel failures"
	if err := s.UpdateSavedSearch(ctx, shared); err != nil || shared.Version != 2 {
		t.Fatalf("update: %v version=%d", err, shared.Version)
	}
	stale := *shared
	stale.Version = 1
	if err := s.UpdateSavedSearch(ctx, &stale); !errors.Is(err, metadata.ErrVersionConflict) {
		t.Errorf("stale update: %v", err)
	}
	if err := s.DeleteSavedSearch(ctx, "default", shared.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSavedSearch(ctx, "default", shared.ID); !errors.Is(err, metadata.ErrNotFound) {
		t.Errorf("double delete: %v", err)
	}
}

func TestAuditAndNodeStats(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.InsertAuditEvent(ctx, &metadata.AuditEvent{Tenant: "default", ActorType: "user", Action: "auth.login", Outcome: "success", Details: json.RawMessage(`{"x":1}`)}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for i := range 3 {
		if err := s.InsertNodeStats(ctx, &metadata.NodeStats{NodeID: "n1", Time: now.Add(time.Duration(i) * 10 * time.Second), Received: int64(i * 100)}); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := s.NodeStatsSince(ctx, now.Add(5*time.Second))
	if err != nil || len(stats) != 2 || stats[1].Received != 200 {
		t.Fatalf("NodeStatsSince = %+v, %v", stats, err)
	}
	if n, _ := s.DeleteNodeStatsBefore(ctx, now.Add(15*time.Second)); n != 2 {
		t.Errorf("deleted %d", n)
	}
}

func TestSources(t *testing.T) {
	s := pgtest.Open(t)
	ctx := context.Background()
	src := &metadata.Source{Tenant: "default", Name: "branch", Config: json.RawMessage(`{"type":"syslog"}`), Enabled: true}
	if err := s.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSource(ctx, &metadata.Source{Tenant: "default", Name: "BRANCH", Config: json.RawMessage(`{}`)}); !errors.Is(err, metadata.ErrConflict) {
		t.Errorf("duplicate name (case-insensitive): %v", err)
	}
	got, err := s.SourceByID(ctx, "default", src.ID)
	if err != nil || got.Name != "branch" || !got.Enabled || got.Version != 1 {
		t.Fatalf("read back: %+v %v", got, err)
	}
	list, err := s.ListSources(ctx, "default")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}

	stale := *got
	got.Enabled = false
	got.Config = json.RawMessage(`{"type":"syslog","protocol":"tcp"}`)
	if err := s.UpdateSource(ctx, got); err != nil || got.Version != 2 {
		t.Fatalf("update: %v version=%d", err, got.Version)
	}
	if err := s.UpdateSource(ctx, &stale); !errors.Is(err, metadata.ErrVersionConflict) {
		t.Errorf("stale update: %v", err)
	}
	if err := s.DeleteSource(ctx, "default", src.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSource(ctx, "default", src.ID); !errors.Is(err, metadata.ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
}

func TestUserAdministrationAndAudit(t *testing.T) {
	s := pgtest.Open(t)
	ctx := context.Background()
	u := &metadata.User{Tenant: "default", Username: "dana", PasswordHash: "x", Role: "viewer"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	u.Role, u.DisplayName, u.Disabled = "operator", "Dana", true
	if err := s.UpdateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	users, err := s.ListUsers(ctx, "default")
	if err != nil || len(users) != 1 || users[0].Role != "operator" || !users[0].Disabled || users[0].DisplayName != "Dana" {
		t.Fatalf("list users: %+v %v", users, err)
	}

	for i, action := range []string{"auth.login", "users.create", "auth.login"} {
		e := &metadata.AuditEvent{Tenant: "default", ActorType: "user", ActorName: "admin", Action: action,
			Outcome: "success", Time: time.Now().Add(-time.Duration(i) * time.Hour)}
		if err := s.InsertAuditEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.ListAuditEvents(ctx, metadata.ListAuditEvents{Tenant: "default", Limit: 10})
	if err != nil || len(all) != 3 || !all[0].Time.After(all[1].Time) {
		t.Fatalf("list audit: %d events %v", len(all), err)
	}
	byAction, err := s.ListAuditEvents(ctx, metadata.ListAuditEvents{Tenant: "default", Action: "users.create", Limit: 10})
	if err != nil || len(byAction) != 1 {
		t.Errorf("filter by action: %d events %v", len(byAction), err)
	}
	recent, err := s.ListAuditEvents(ctx, metadata.ListAuditEvents{Tenant: "default", Since: time.Now().Add(-90 * time.Minute), Limit: 10})
	if err != nil || len(recent) != 2 {
		t.Errorf("filter by time: %d events %v", len(recent), err)
	}

	if err := s.DeleteUser(ctx, "default", u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserByID(ctx, u.ID); !errors.Is(err, metadata.ErrNotFound) {
		t.Errorf("deleted user: %v", err)
	}
}
