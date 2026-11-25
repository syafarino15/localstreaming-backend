package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

const layoutColumns = `id, tenant_id, name, config, version, is_active, updated_by, created_at, updated_at`

func scanLayout(row pgx.Row) (*domain.LayoutConfigRecord, error) {
	var (
		rec    domain.LayoutConfigRecord
		config []byte
	)
	err := row.Scan(&rec.ID, &rec.TenantID, &rec.Name, &config, &rec.Version, &rec.IsActive, &rec.UpdatedBy, &rec.CreatedAt, &rec.UpdatedAt)
	if err != nil {
		return nil, mapWriteError("layout", err)
	}
	if err := json.Unmarshal(config, &rec.Config); err != nil {
		return nil, fmt.Errorf("layout: decode config: %w", err)
	}
	return &rec, nil
}

// GetActive returns the currently published layout document.
func (l *Local) GetActive(ctx context.Context, tenantID string) (*domain.LayoutConfigRecord, error) {
	row := l.pool.QueryRow(ctx, `
		SELECT `+layoutColumns+`
		FROM mst_tv_layout_configs
		WHERE tenant_id = $1 AND is_active
		LIMIT 1`, tenantID)
	return scanLayout(row)
}

// Publish stores a new layout version and makes it the only active one.
func (l *Local) Publish(ctx context.Context, tenantID string, cfg domain.LayoutConfig, updatedBy string) (*domain.LayoutConfigRecord, error) {
	payload, err := marshalJSON(cfg)
	if err != nil {
		return nil, err
	}

	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("layout: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		UPDATE mst_tv_layout_configs SET is_active = false
		WHERE tenant_id = $1 AND is_active`, tenantID); err != nil {
		return nil, mapWriteError("layout", err)
	}

	var nextVersion int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(version), 0) + 1 FROM mst_tv_layout_configs WHERE tenant_id = $1`,
		tenantID).Scan(&nextVersion); err != nil {
		return nil, mapWriteError("layout", err)
	}

	var updatedByArg any
	if updatedBy != "" {
		updatedByArg = updatedBy
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO mst_tv_layout_configs (tenant_id, name, config, version, is_active, updated_by)
		VALUES ($1, 'default', $2::jsonb, $3, true, $4)
		RETURNING `+layoutColumns, tenantID, payload, nextVersion, updatedByArg)

	rec, err := scanLayout(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("layout: commit: %w", err)
	}
	return rec, nil
}

// History returns previous layout versions, newest first.
func (l *Local) History(ctx context.Context, tenantID string, p domain.Pagination) ([]domain.LayoutConfigRecord, int64, error) {
	limit, offset := paginate(p.Page, p.Limit)

	var total int64
	if err := l.pool.QueryRow(ctx, `SELECT count(*) FROM mst_tv_layout_configs WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
		return nil, 0, mapWriteError("layout", err)
	}

	rows, err := l.pool.Query(ctx, `
		SELECT `+layoutColumns+`
		FROM mst_tv_layout_configs
		WHERE tenant_id = $1
		ORDER BY version DESC
		LIMIT $2 OFFSET $3`, tenantID, limit, offset)
	if err != nil {
		return nil, 0, mapWriteError("layout", err)
	}
	defer rows.Close()

	out := make([]domain.LayoutConfigRecord, 0, limit)
	for rows.Next() {
		rec, err := scanLayout(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *rec)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, mapWriteError("layout", err)
	}
	return out, total, nil
}
