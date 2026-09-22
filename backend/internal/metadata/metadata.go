// Package metadata defines the relational metadata model (users, sessions,
// API keys, saved searches, audit events, node statistics) and the store
// interface. The PostgreSQL implementation lives in metadata/postgres.
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Errors returned by stores.
var (
	ErrNotFound = errors.New("not found")
	// ErrConflict indicates a uniqueness violation (e.g. duplicate name).
	ErrConflict = errors.New("conflict")
	// ErrVersionConflict indicates an optimistic-concurrency mismatch.
	ErrVersionConflict = errors.New("version conflict")
)

type User struct {
	ID                 uuid.UUID
	Tenant             string
	Username           string
	DisplayName        string
	PasswordHash       string
	Role               string
	MustChangePassword bool
	Disabled           bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
	LastLoginAt        *time.Time
	// GeneratedPassword is set in memory only, when the server generated a
	// password that must be shown to an administrator once.
	GeneratedPassword string `json:"-"`
}

type Session struct {
	TokenHash  []byte
	UserID     uuid.UUID
	CSRFToken  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	IP         string
	UserAgent  string
}

type APIKey struct {
	ID          uuid.UUID
	KeyID       string
	SecretHash  []byte
	Name        string
	Tenant      string
	OwnerUserID *uuid.UUID
	OwnerName   string
	Scopes      []string
	CreatedBy   *uuid.UUID
	CreatedAt   time.Time
	ExpiresAt   *time.Time
	LastUsedAt  *time.Time
	RevokedAt   *time.Time
}

type SavedSearch struct {
	ID               uuid.UUID
	Tenant           string
	OwnerID          uuid.UUID
	OwnerUsername    string
	Name             string
	Description      string
	Query            json.RawMessage
	Columns          []string
	DefaultTimeRange json.RawMessage
	Visibility       string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Version          int
}

type AuditEvent struct {
	ID        uuid.UUID
	Time      time.Time
	Tenant    string
	ActorType string // user | api_key | system
	ActorID   *uuid.UUID
	ActorName string
	IP        string
	UserAgent string
	Action    string
	Outcome   string // success | failure
	Target    json.RawMessage
	Details   json.RawMessage
	RequestID string
}

// Source is a database-managed ingestion source. Config is the YAML source
// object as JSON, so the ingestion layer stays unaware of the metadata store.
type Source struct {
	ID        uuid.UUID
	Tenant    string
	Name      string
	Config    json.RawMessage
	Enabled   bool
	CreatedBy *uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
	Version   int
}

// Setting is one stored deployment setting. Values are JSON so a setting can
// grow fields without a migration.
type Setting struct {
	Key       string
	Value     json.RawMessage
	UpdatedBy *uuid.UUID
	UpdatedAt time.Time
}

// SettingRetention is the key holding the desired retention period.
const SettingRetention = "retention"

// ListAuditEvents filters the audit log. Times are exclusive of Before and
// inclusive of Since; an empty filter field matches everything.
type ListAuditEvents struct {
	Tenant  string
	Since   time.Time
	Before  time.Time
	Actor   string
	Action  string
	Outcome string
	Limit   int
}

// NodeStats is a cumulative counter snapshot written periodically by a node.
type NodeStats struct {
	NodeID        string
	Time          time.Time
	Received      int64
	Parsed        int64
	ParseErrors   int64
	Stored        int64
	Dropped       int64
	BytesReceived int64
	BytesStored   int64
}

// ListSavedSearches filters saved searches visible to a user.
type ListSavedSearches struct {
	Tenant   string
	ViewerID uuid.UUID
	Query    string
	Limit    int
	// Cursor is the name/ID keyset position from the previous page.
	AfterName string
	AfterID   uuid.UUID
}

// Store is the metadata persistence interface.
type Store interface {
	Ping(ctx context.Context) error
	Close()

	CountUsers(ctx context.Context) (int, error)
	CreateUser(ctx context.Context, u *User) error
	ListUsers(ctx context.Context, tenant string) ([]User, error)
	UpdateUser(ctx context.Context, u *User) error
	DeleteUser(ctx context.Context, tenant string, id uuid.UUID) error
	UserByID(ctx context.Context, id uuid.UUID) (*User, error)
	UserByUsername(ctx context.Context, username string) (*User, error)
	UpdatePassword(ctx context.Context, id uuid.UUID, hash string, mustChange bool) error
	TouchLogin(ctx context.Context, id uuid.UUID, at time.Time) error

	CreateSession(ctx context.Context, s *Session) error
	SessionByHash(ctx context.Context, hash []byte) (*Session, error)
	TouchSession(ctx context.Context, hash []byte, lastSeen time.Time) error
	DeleteSession(ctx context.Context, hash []byte) error
	DeleteUserSessions(ctx context.Context, userID uuid.UUID, except []byte) error
	DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error)

	CreateAPIKey(ctx context.Context, k *APIKey) error
	APIKeyByKeyID(ctx context.Context, keyID string) (*APIKey, error)
	ListAPIKeys(ctx context.Context, tenant string, owner *uuid.UUID) ([]APIKey, error)
	RevokeAPIKey(ctx context.Context, tenant string, id uuid.UUID, owner *uuid.UUID, at time.Time) error
	TouchAPIKey(ctx context.Context, id uuid.UUID, at time.Time) error

	CreateSavedSearch(ctx context.Context, s *SavedSearch) error
	SavedSearchByID(ctx context.Context, tenant string, id uuid.UUID) (*SavedSearch, error)
	ListSavedSearches(ctx context.Context, f ListSavedSearches) ([]SavedSearch, error)
	UpdateSavedSearch(ctx context.Context, s *SavedSearch) error
	DeleteSavedSearch(ctx context.Context, tenant string, id uuid.UUID) error

	CreateSource(ctx context.Context, s *Source) error
	SourceByID(ctx context.Context, tenant string, id uuid.UUID) (*Source, error)
	ListSources(ctx context.Context, tenant string) ([]Source, error)
	UpdateSource(ctx context.Context, s *Source) error
	DeleteSource(ctx context.Context, tenant string, id uuid.UUID) error

	Setting(ctx context.Context, key string) (*Setting, error)
	SetSetting(ctx context.Context, s *Setting) error

	InsertAuditEvent(ctx context.Context, e *AuditEvent) error
	ListAuditEvents(ctx context.Context, f ListAuditEvents) ([]AuditEvent, error)
	DeleteAuditEventsBefore(ctx context.Context, t time.Time) (int64, error)

	InsertNodeStats(ctx context.Context, s *NodeStats) error
	NodeStatsSince(ctx context.Context, since time.Time) ([]NodeStats, error)
	DeleteNodeStatsBefore(ctx context.Context, t time.Time) (int64, error)
}

// NewID returns a time-ordered UUIDv7.
func NewID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return id
}
