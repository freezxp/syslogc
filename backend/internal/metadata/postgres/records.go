package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/freezxp/syslogc/backend/internal/metadata"
)

// ---- API keys ---------------------------------------------------------

//nolint:gosec // column list, not a credential
const apiKeyColumns = `k.id, k.key_id, k.secret_hash, k.name, k.tenant_id, k.owner_user_id, coalesce(u.username, ''),
	k.scopes, k.created_by, k.created_at, k.expires_at, k.last_used_at, k.revoked_at`

func scanAPIKey(row pgx.Row) (*metadata.APIKey, error) {
	var k metadata.APIKey
	err := row.Scan(&k.ID, &k.KeyID, &k.SecretHash, &k.Name, &k.Tenant, &k.OwnerUserID, &k.OwnerName,
		&k.Scopes, &k.CreatedBy, &k.CreatedAt, &k.ExpiresAt, &k.LastUsedAt, &k.RevokedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &k, nil
}

func (s *Store) CreateAPIKey(ctx context.Context, k *metadata.APIKey) error {
	if k.ID == uuid.Nil {
		k.ID = metadata.NewID()
	}
	k.CreatedAt = time.Now().UTC()
	_, err := s.pool.Exec(ctx, `INSERT INTO api_keys (id, key_id, secret_hash, name, tenant_id, owner_user_id, scopes,
		created_by, created_at, expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		k.ID, k.KeyID, k.SecretHash, k.Name, k.Tenant, k.OwnerUserID, k.Scopes, k.CreatedBy, k.CreatedAt, k.ExpiresAt)
	return mapErr(err)
}

func (s *Store) APIKeyByKeyID(ctx context.Context, keyID string) (*metadata.APIKey, error) {
	return scanAPIKey(s.pool.QueryRow(ctx, "SELECT "+apiKeyColumns+
		" FROM api_keys k LEFT JOIN users u ON u.id = k.owner_user_id WHERE k.key_id = $1", keyID))
}

func (s *Store) ListAPIKeys(ctx context.Context, tenant string, owner *uuid.UUID) ([]metadata.APIKey, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+apiKeyColumns+` FROM api_keys k LEFT JOIN users u ON u.id = k.owner_user_id
		WHERE k.tenant_id = $1 AND k.revoked_at IS NULL AND ($2::uuid IS NULL OR k.owner_user_id = $2)
		ORDER BY k.created_at DESC`, tenant, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metadata.APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

func (s *Store) RevokeAPIKey(ctx context.Context, tenant string, id uuid.UUID, owner *uuid.UUID, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE api_keys SET revoked_at = $4 WHERE id = $1 AND tenant_id = $2
		AND revoked_at IS NULL AND ($3::uuid IS NULL OR owner_user_id = $3)`, id, tenant, owner, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return metadata.ErrNotFound
	}
	return nil
}

func (s *Store) TouchAPIKey(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := s.pool.Exec(ctx, "UPDATE api_keys SET last_used_at = $2 WHERE id = $1", id, at)
	return err
}

// ---- Saved searches ---------------------------------------------------

const savedSearchColumns = `s.id, s.tenant_id, s.owner_id, u.username, s.name, s.description, s.query, s.columns,
	s.default_time_range, s.visibility, s.created_at, s.updated_at, s.version`

func scanSavedSearch(row pgx.Row) (*metadata.SavedSearch, error) {
	var ss metadata.SavedSearch
	err := row.Scan(&ss.ID, &ss.Tenant, &ss.OwnerID, &ss.OwnerUsername, &ss.Name, &ss.Description, &ss.Query,
		&ss.Columns, &ss.DefaultTimeRange, &ss.Visibility, &ss.CreatedAt, &ss.UpdatedAt, &ss.Version)
	if err != nil {
		return nil, mapErr(err)
	}
	return &ss, nil
}

func (s *Store) CreateSavedSearch(ctx context.Context, ss *metadata.SavedSearch) error {
	if ss.ID == uuid.Nil {
		ss.ID = metadata.NewID()
	}
	now := time.Now().UTC()
	ss.CreatedAt, ss.UpdatedAt, ss.Version = now, now, 1
	if ss.Columns == nil {
		ss.Columns = []string{}
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO saved_searches (id, tenant_id, owner_id, name, description, query, columns,
		default_time_range, visibility, created_at, updated_at, version) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,1)`,
		ss.ID, ss.Tenant, ss.OwnerID, ss.Name, ss.Description, ss.Query, ss.Columns, nullJSON(ss.DefaultTimeRange),
		ss.Visibility, now, now)
	return mapErr(err)
}

func (s *Store) SavedSearchByID(ctx context.Context, tenant string, id uuid.UUID) (*metadata.SavedSearch, error) {
	return scanSavedSearch(s.pool.QueryRow(ctx, "SELECT "+savedSearchColumns+
		" FROM saved_searches s JOIN users u ON u.id = s.owner_id WHERE s.tenant_id = $1 AND s.id = $2", tenant, id))
}

func (s *Store) ListSavedSearches(ctx context.Context, f metadata.ListSavedSearches) ([]metadata.SavedSearch, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(f.Query) + "%"
	rows, err := s.pool.Query(ctx, "SELECT "+savedSearchColumns+` FROM saved_searches s JOIN users u ON u.id = s.owner_id
		WHERE s.tenant_id = $1 AND (s.visibility = 'shared' OR s.owner_id = $2)
		  AND ($3 = '%%' OR s.name ILIKE $3 OR s.description ILIKE $3)
		  AND ($4 = '' OR (s.name, s.id) > ($4, $5))
		ORDER BY s.name, s.id LIMIT $6`, f.Tenant, f.ViewerID, pattern, f.AfterName, f.AfterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metadata.SavedSearch
	for rows.Next() {
		ss, err := scanSavedSearch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ss)
	}
	return out, rows.Err()
}

func (s *Store) UpdateSavedSearch(ctx context.Context, ss *metadata.SavedSearch) error {
	if ss.Columns == nil {
		ss.Columns = []string{}
	}
	var updated time.Time
	var version int
	err := s.pool.QueryRow(ctx, `UPDATE saved_searches SET name = $3, description = $4, query = $5, columns = $6,
		default_time_range = $7, visibility = $8, updated_at = now(), version = version + 1
		WHERE tenant_id = $1 AND id = $2 AND version = $9 RETURNING updated_at, version`,
		ss.Tenant, ss.ID, ss.Name, ss.Description, ss.Query, ss.Columns, nullJSON(ss.DefaultTimeRange), ss.Visibility, ss.Version).
		Scan(&updated, &version)
	if err != nil {
		err = mapErr(err)
		if errors.Is(err, metadata.ErrNotFound) {
			if _, gerr := s.SavedSearchByID(ctx, ss.Tenant, ss.ID); gerr == nil {
				return metadata.ErrVersionConflict
			}
		}
		return err
	}
	ss.UpdatedAt, ss.Version = updated, version
	return nil
}

func (s *Store) DeleteSavedSearch(ctx context.Context, tenant string, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM saved_searches WHERE tenant_id = $1 AND id = $2", tenant, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return metadata.ErrNotFound
	}
	return nil
}

// ---- Audit ------------------------------------------------------------

func (s *Store) InsertAuditEvent(ctx context.Context, e *metadata.AuditEvent) error {
	if e.ID == uuid.Nil {
		e.ID = metadata.NewID()
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_events (id, ts, tenant_id, actor_type, actor_id, actor_name, ip,
		user_agent, action, outcome, target, details, request_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		e.ID, e.Time, e.Tenant, e.ActorType, e.ActorID, e.ActorName, e.IP, e.UserAgent, e.Action, e.Outcome,
		nullJSON(e.Target), nullJSON(e.Details), e.RequestID)
	return err
}

func (s *Store) DeleteAuditEventsBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM audit_events WHERE ts < $1", t)
	return tag.RowsAffected(), err
}

// ---- Node statistics --------------------------------------------------

func (s *Store) InsertNodeStats(ctx context.Context, n *metadata.NodeStats) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO node_stats (node_id, ts, received, parsed, parse_errors, stored, dropped,
		bytes_received, bytes_stored) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`,
		n.NodeID, n.Time, n.Received, n.Parsed, n.ParseErrors, n.Stored, n.Dropped, n.BytesReceived, n.BytesStored)
	return err
}

func (s *Store) NodeStatsSince(ctx context.Context, since time.Time) ([]metadata.NodeStats, error) {
	rows, err := s.pool.Query(ctx, `SELECT node_id, ts, received, parsed, parse_errors, stored, dropped, bytes_received,
		bytes_stored FROM node_stats WHERE ts >= $1 ORDER BY node_id, ts`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metadata.NodeStats
	for rows.Next() {
		var n metadata.NodeStats
		if err := rows.Scan(&n.NodeID, &n.Time, &n.Received, &n.Parsed, &n.ParseErrors, &n.Stored, &n.Dropped,
			&n.BytesReceived, &n.BytesStored); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) DeleteNodeStatsBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM node_stats WHERE ts < $1", t)
	return tag.RowsAffected(), err
}

// nullJSON stores empty raw JSON as SQL NULL.
func nullJSON(b []byte) any {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return b
}

// ListAuditEvents returns matching events newest first.
func (s *Store) ListAuditEvents(ctx context.Context, f metadata.ListAuditEvents) ([]metadata.AuditEvent, error) {
	limit := min(max(f.Limit, 1), 1000)
	rows, err := s.pool.Query(ctx, `SELECT id, ts, tenant_id, actor_type, actor_id, actor_name, ip, user_agent,
		action, outcome, target, details, request_id FROM audit_events
		WHERE tenant_id = $1
		  AND ($2::timestamptz IS NULL OR ts >= $2)
		  AND ($3::timestamptz IS NULL OR ts < $3)
		  AND ($4 = '' OR actor_name = $4)
		  AND ($5 = '' OR action = $5)
		  AND ($6 = '' OR outcome = $6)
		ORDER BY ts DESC, id DESC LIMIT $7`,
		f.Tenant, nullTime(f.Since), nullTime(f.Before), f.Actor, f.Action, f.Outcome, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []metadata.AuditEvent{}
	for rows.Next() {
		var e metadata.AuditEvent
		if err := rows.Scan(&e.ID, &e.Time, &e.Tenant, &e.ActorType, &e.ActorID, &e.ActorName, &e.IP,
			&e.UserAgent, &e.Action, &e.Outcome, &e.Target, &e.Details, &e.RequestID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// ---- settings ---------------------------------------------------------

func (s *Store) Setting(ctx context.Context, key string) (*metadata.Setting, error) {
	var out metadata.Setting
	err := s.pool.QueryRow(ctx, "SELECT key, value, updated_by, updated_at FROM settings WHERE key = $1", key).
		Scan(&out.Key, &out.Value, &out.UpdatedBy, &out.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &out, nil
}

func (s *Store) SetSetting(ctx context.Context, in *metadata.Setting) error {
	return s.pool.QueryRow(ctx, `INSERT INTO settings (key, value, updated_by, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (key) DO UPDATE SET value = $2, updated_by = $3, updated_at = now()
		RETURNING updated_at`, in.Key, in.Value, in.UpdatedBy).Scan(&in.UpdatedAt)
}
