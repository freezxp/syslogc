package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/freezxp/syslogc/backend/internal/metadata"
)

const userColumns = `id, tenant_id, username, display_name, password_hash, role, must_change_password,
	disabled, created_at, updated_at, last_login_at`

func scanUser(row pgx.Row) (*metadata.User, error) {
	var u metadata.User
	err := row.Scan(&u.ID, &u.Tenant, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Role,
		&u.MustChangePassword, &u.Disabled, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &u, nil
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&n)
	return n, err
}

func (s *Store) CreateUser(ctx context.Context, u *metadata.User) error {
	if u.ID == uuid.Nil {
		u.ID = metadata.NewID()
	}
	now := time.Now().UTC()
	u.CreatedAt, u.UpdatedAt = now, now
	_, err := s.pool.Exec(ctx, `INSERT INTO users (id, tenant_id, username, display_name, password_hash, role,
		must_change_password, disabled, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		u.ID, u.Tenant, u.Username, u.DisplayName, u.PasswordHash, u.Role, u.MustChangePassword, u.Disabled, now, now)
	return mapErr(err)
}

func (s *Store) UserByID(ctx context.Context, id uuid.UUID) (*metadata.User, error) {
	return scanUser(s.pool.QueryRow(ctx, "SELECT "+userColumns+" FROM users WHERE id = $1", id))
}

func (s *Store) UserByUsername(ctx context.Context, username string) (*metadata.User, error) {
	return scanUser(s.pool.QueryRow(ctx, "SELECT "+userColumns+" FROM users WHERE lower(username) = lower($1)", username))
}

func (s *Store) UpdatePassword(ctx context.Context, id uuid.UUID, hash string, mustChange bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET password_hash = $2, must_change_password = $3, updated_at = now()
		WHERE id = $1`, id, hash, mustChange)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return metadata.ErrNotFound
	}
	return nil
}

func (s *Store) TouchLogin(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := s.pool.Exec(ctx, "UPDATE users SET last_login_at = $2 WHERE id = $1", id, at)
	return err
}

func (s *Store) CreateSession(ctx context.Context, ss *metadata.Session) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, csrf_token, created_at, last_seen_at,
		expires_at, ip, user_agent) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		ss.TokenHash, ss.UserID, ss.CSRFToken, ss.CreatedAt, ss.LastSeenAt, ss.ExpiresAt, ss.IP, ss.UserAgent)
	return mapErr(err)
}

func (s *Store) SessionByHash(ctx context.Context, hash []byte) (*metadata.Session, error) {
	var ss metadata.Session
	err := s.pool.QueryRow(ctx, `SELECT token_hash, user_id, csrf_token, created_at, last_seen_at, expires_at, ip, user_agent
		FROM sessions WHERE token_hash = $1`, hash).
		Scan(&ss.TokenHash, &ss.UserID, &ss.CSRFToken, &ss.CreatedAt, &ss.LastSeenAt, &ss.ExpiresAt, &ss.IP, &ss.UserAgent)
	if err != nil {
		return nil, mapErr(err)
	}
	return &ss, nil
}

func (s *Store) TouchSession(ctx context.Context, hash []byte, lastSeen time.Time) error {
	_, err := s.pool.Exec(ctx, "UPDATE sessions SET last_seen_at = $2 WHERE token_hash = $1", hash, lastSeen)
	return err
}

func (s *Store) DeleteSession(ctx context.Context, hash []byte) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE token_hash = $1", hash)
	return err
}

func (s *Store) DeleteUserSessions(ctx context.Context, userID uuid.UUID, except []byte) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE user_id = $1 AND ($2::bytea IS NULL OR token_hash <> $2)", userID, except)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE expires_at < $1", now)
	return tag.RowsAffected(), err
}

func (s *Store) ListUsers(ctx context.Context, tenant string) ([]metadata.User, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+userColumns+" FROM users WHERE tenant_id = $1 ORDER BY lower(username)", tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []metadata.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// UpdateUser changes the profile fields an administrator owns; the password
// is changed with UpdatePassword.
func (s *Store) UpdateUser(ctx context.Context, u *metadata.User) error {
	var updated time.Time
	err := s.pool.QueryRow(ctx, `UPDATE users SET display_name = $3, role = $4, disabled = $5, updated_at = now()
		WHERE tenant_id = $1 AND id = $2 RETURNING updated_at`,
		u.Tenant, u.ID, u.DisplayName, u.Role, u.Disabled).Scan(&updated)
	if err != nil {
		return mapErr(err)
	}
	u.UpdatedAt = updated
	return nil
}

func (s *Store) DeleteUser(ctx context.Context, tenant string, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM users WHERE tenant_id = $1 AND id = $2", tenant, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return metadata.ErrNotFound
	}
	return nil
}
