package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

const scanColumns = `id, tenant_id, trigger, root_dir, status, files_found, files_added, files_updated, files_missing, error, started_at, finished_at`

func scanScanRun(row pgx.Row) (*domain.MediaScanRun, error) {
	var r domain.MediaScanRun
	err := row.Scan(&r.ID, &r.TenantID, &r.Trigger, &r.RootDir, &r.Status, &r.FilesFound,
		&r.FilesAdded, &r.FilesUpdated, &r.FilesMissing, &r.Error, &r.StartedAt, &r.FinishedAt)
	if err != nil {
		return nil, mapWriteError("media scan", err)
	}
	return &r, nil
}

// Start opens a scan run so the CMS can show progress.
func (l *Local) StartScan(ctx context.Context, run *domain.MediaScanRun) error {
	if run.Trigger == "" {
		run.Trigger = "MANUAL"
	}
	row := l.pool.QueryRow(ctx, `
		INSERT INTO trn_media_scans (tenant_id, trigger, root_dir, status)
		VALUES ($1, $2, $3, 'RUNNING')
		RETURNING `+scanColumns,
		run.TenantID, run.Trigger, run.RootDir)
	created, err := scanScanRun(row)
	if err != nil {
		return err
	}
	*run = *created
	return nil
}

// Finish closes a scan run with its counters.
func (l *Local) FinishScan(ctx context.Context, run *domain.MediaScanRun) error {
	row := l.pool.QueryRow(ctx, `
		UPDATE trn_media_scans
		SET status = $2, files_found = $3, files_added = $4, files_updated = $5,
		    files_missing = $6, error = $7, finished_at = now()
		WHERE id = $1
		RETURNING `+scanColumns,
		run.ID, run.Status, run.FilesFound, run.FilesAdded, run.FilesUpdated, run.FilesMissing, run.Error)
	updated, err := scanScanRun(row)
	if err != nil {
		return err
	}
	*run = *updated
	return nil
}

// Last returns the most recent scan run for a tenant.
func (l *Local) LastScan(ctx context.Context, tenantID string) (*domain.MediaScanRun, error) {
	row := l.pool.QueryRow(ctx, `
		SELECT `+scanColumns+`
		FROM trn_media_scans
		WHERE tenant_id = $1
		ORDER BY started_at DESC
		LIMIT 1`, tenantID)
	return scanScanRun(row)
}

// List returns scan history.
func (l *Local) ListScans(ctx context.Context, tenantID string, p domain.Pagination) ([]domain.MediaScanRun, int64, error) {
	limit, offset := paginate(p.Page, p.Limit)

	var total int64
	if err := l.pool.QueryRow(ctx, `SELECT count(*) FROM trn_media_scans WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
		return nil, 0, mapWriteError("media scan", err)
	}

	rows, err := l.pool.Query(ctx, `
		SELECT `+scanColumns+`
		FROM trn_media_scans
		WHERE tenant_id = $1
		ORDER BY started_at DESC
		LIMIT $2 OFFSET $3`, tenantID, limit, offset)
	if err != nil {
		return nil, 0, mapWriteError("media scan", err)
	}
	defer rows.Close()

	out := make([]domain.MediaScanRun, 0, limit)
	for rows.Next() {
		r, err := scanScanRun(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *r)
	}
	return out, total, nil
}
