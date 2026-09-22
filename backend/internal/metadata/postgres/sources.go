package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/freezxp/syslogc/backend/internal/metadata"
)

const sourceColumns = `id, tenant_id, name, config, enabled, adopted, created_by, created_at, updated_at, version`

func scanSource(row pgx.Row) (*metadata.Source, error) {
	var s metadata.Source
	if err := row.Scan(&s.ID, &s.Tenant, &s.Name, &s.Config, &s.Enabled, &s.Adopted, &s.CreatedBy,
		&s.CreatedAt, &s.UpdatedAt, &s.Version); err != nil {
		return nil, mapErr(err)
	}
	return &s, nil
}

func (s *Store) CreateSource(ctx context.Context, src *metadata.Source) error {
	if src.ID == uuid.Nil {
		src.ID = metadata.NewID()
	}
	now := time.Now().UTC()
	src.CreatedAt, src.UpdatedAt, src.Version = now, now, 1
	_, err := s.pool.Exec(ctx, `INSERT INTO sources (id, tenant_id, name, config, enabled, adopted, created_by,
		created_at, updated_at, version) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,1)`,
		src.ID, src.Tenant, src.Name, src.Config, src.Enabled, src.Adopted, src.CreatedBy, now, now)
	return mapErr(err)
}

func (s *Store) SourceByID(ctx context.Context, tenant string, id uuid.UUID) (*metadata.Source, error) {
	return scanSource(s.pool.QueryRow(ctx, "SELECT "+sourceColumns+
		" FROM sources WHERE tenant_id = $1 AND id = $2", tenant, id))
}

func (s *Store) ListSources(ctx context.Context, tenant string) ([]metadata.Source, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+sourceColumns+" FROM sources WHERE tenant_id = $1 ORDER BY name", tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []metadata.Source{}
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *src)
	}
	return out, rows.Err()
}

// UpdateSource applies an optimistic-concurrency update; it returns
// ErrVersionConflict when the stored version moved on.
func (s *Store) UpdateSource(ctx context.Context, src *metadata.Source) error {
	var updated time.Time
	var version int
	err := s.pool.QueryRow(ctx, `UPDATE sources SET name = $3, config = $4, enabled = $5,
		updated_at = now(), version = version + 1
		WHERE tenant_id = $1 AND id = $2 AND version = $6 RETURNING updated_at, version`,
		src.Tenant, src.ID, src.Name, src.Config, src.Enabled, src.Version).Scan(&updated, &version)
	if err != nil {
		err = mapErr(err)
		if errors.Is(err, metadata.ErrNotFound) {
			if _, gerr := s.SourceByID(ctx, src.Tenant, src.ID); gerr == nil {
				return metadata.ErrVersionConflict
			}
		}
		return err
	}
	src.UpdatedAt, src.Version = updated, version
	return nil
}

func (s *Store) DeleteSource(ctx context.Context, tenant string, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM sources WHERE tenant_id = $1 AND id = $2", tenant, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return metadata.ErrNotFound
	}
	return nil
}

// WatchSources calls notify when a source changes, using PostgreSQL
// LISTEN/NOTIFY, and returns when ctx ends. Nodes also poll, so a dropped
// connection only delays reconciliation.
func (s *Store) WatchSources(ctx context.Context, notify func()) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN syslogc_sources"); err != nil {
		return err
	}
	for {
		if _, err := conn.Conn().WaitForNotification(ctx); err != nil {
			return err
		}
		notify()
	}
}
