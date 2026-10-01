package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/freezxp/syslogc/backend/internal/metadata"
)

const forwardTargetColumns = `id, tenant_id, name, config, secret, enabled, created_by, created_at, updated_at, version`

func scanForwardTarget(row pgx.Row) (*metadata.ForwardTarget, error) {
	var t metadata.ForwardTarget
	if err := row.Scan(&t.ID, &t.Tenant, &t.Name, &t.Config, &t.Secret, &t.Enabled, &t.CreatedBy,
		&t.CreatedAt, &t.UpdatedAt, &t.Version); err != nil {
		return nil, mapErr(err)
	}
	return &t, nil
}

func (s *Store) CreateForwardTarget(ctx context.Context, t *metadata.ForwardTarget) error {
	if t.ID == uuid.Nil {
		t.ID = metadata.NewID()
	}
	now := time.Now().UTC()
	t.CreatedAt, t.UpdatedAt, t.Version = now, now, 1
	_, err := s.pool.Exec(ctx, `INSERT INTO forward_targets (id, tenant_id, name, config, secret, enabled,
		created_by, created_at, updated_at, version) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,1)`,
		t.ID, t.Tenant, t.Name, t.Config, t.Secret, t.Enabled, t.CreatedBy, now, now)
	return mapErr(err)
}

func (s *Store) ForwardTargetByID(ctx context.Context, tenant string, id uuid.UUID) (*metadata.ForwardTarget, error) {
	return scanForwardTarget(s.pool.QueryRow(ctx, "SELECT "+forwardTargetColumns+
		" FROM forward_targets WHERE tenant_id = $1 AND id = $2", tenant, id))
}

func (s *Store) ListForwardTargets(ctx context.Context, tenant string) ([]metadata.ForwardTarget, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+forwardTargetColumns+
		" FROM forward_targets WHERE tenant_id = $1 ORDER BY name", tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []metadata.ForwardTarget{}
	for rows.Next() {
		t, err := scanForwardTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// UpdateForwardTarget applies an optimistic-concurrency update; it returns
// ErrVersionConflict when the stored version moved on.
func (s *Store) UpdateForwardTarget(ctx context.Context, t *metadata.ForwardTarget) error {
	var updated time.Time
	var version int
	err := s.pool.QueryRow(ctx, `UPDATE forward_targets SET name = $3, config = $4, secret = $5, enabled = $6,
		updated_at = now(), version = version + 1
		WHERE tenant_id = $1 AND id = $2 AND version = $7
		RETURNING updated_at, version`,
		t.Tenant, t.ID, t.Name, t.Config, t.Secret, t.Enabled, t.Version).Scan(&updated, &version)
	if err != nil {
		err = mapErr(err)
		// No row matched: either it is gone, or somebody else changed it
		// since this edit was loaded. The two call for different answers.
		if errors.Is(err, metadata.ErrNotFound) {
			if _, gerr := s.ForwardTargetByID(ctx, t.Tenant, t.ID); gerr == nil {
				return metadata.ErrVersionConflict
			}
		}
		return err
	}
	t.UpdatedAt, t.Version = updated, version
	return nil
}

func (s *Store) DeleteForwardTarget(ctx context.Context, tenant string, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM forward_targets WHERE tenant_id = $1 AND id = $2", tenant, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return metadata.ErrNotFound
	}
	return nil
}

// WatchForwardTargets blocks, calling notify when a target changes anywhere
// in the deployment. Callers also poll, so a dropped connection delays a
// change rather than losing it.
func (s *Store) WatchForwardTargets(ctx context.Context, notify func()) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN syslogc_forward_targets"); err != nil {
		return err
	}
	for {
		if _, err := conn.Conn().WaitForNotification(ctx); err != nil {
			return err
		}
		notify()
	}
}
