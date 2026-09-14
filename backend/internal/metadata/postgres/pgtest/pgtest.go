// Package pgtest provides throwaway PostgreSQL databases for tests.
package pgtest

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/freezxp/syslogc/backend/internal/metadata/postgres"
)

// DSN returns TEST_POSTGRES_DSN or skips the test.
func DSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	return dsn
}

// Open creates a fresh database (dropped on cleanup), applies migrations and
// returns a store connected to it.
func Open(t testing.TB) *postgres.Store {
	t.Helper()
	s, _ := OpenDSN(t)
	return s
}

// OpenDSN is like Open and also returns the database DSN.
func OpenDSN(t testing.TB) (*postgres.Store, string) {
	t.Helper()
	dsn := DSN(t)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	dbName := "syslogc_test_" + uuid.NewString()[:8]
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + dbName
	s, err := postgres.Open(ctx, u.String(), 4, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	return s, u.String()
}
