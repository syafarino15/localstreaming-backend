package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

const syncColumns = `id, tenant_id, node_id, type, payload, status, bytes_total, bytes_done, error, started_at, finished_at, created_at`

func scanSyncJob(row pgx.Row) (*domain.SyncJob, error) {
	var (
		j       domain.SyncJob
		payload []byte
	)
	err := row.Scan(&j.ID, &j.TenantID, &j.NodeID, &j.Type, &payload, &j.Status,
		&j.BytesTotal, &j.BytesDone, &j.Error, &j.StartedAt, &j.FinishedAt, &j.CreatedAt)
	if err != nil {
		return nil, mapWriteError("sync job", err)
	}
	j.Payload = rawJSON(payload)
	return &j, nil
}

// Create enqueues a synchronisation job.
func (s *Store) CreateSyncJob(ctx context.Context, j *domain.SyncJob) error {
	payload, err := marshalJSON(j.Payload)
	if err != nil {
		return err
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO trn_sync_jobs (tenant_id, node_id, type, payload, status)
		VALUES ($1, $2, $3, $4::jsonb, 'QUEUED')
		RETURNING `+syncColumns,
		j.TenantID, j.NodeID, j.Type, payload)
	created, err := scanSyncJob(row)
	if err != nil {
		return err
	}
	*j = *created
	return nil
}

// List returns sync jobs for a tenant. An empty tenantID lists every job.
func (s *Store) ListSyncJobs(ctx context.Context, tenantID string, p domain.Pagination) ([]domain.SyncJob, int64, error) {
	limit, offset := paginate(p.Page, p.Limit)

	var (
		total int64
		rows  pgx.Rows
		err   error
	)
	if tenantID == "" {
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM trn_sync_jobs`).Scan(&total); err != nil {
			return nil, 0, mapWriteError("sync job", err)
		}
		rows, err = s.pool.Query(ctx, `SELECT `+syncColumns+` FROM trn_sync_jobs ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	} else {
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM trn_sync_jobs WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
			return nil, 0, mapWriteError("sync job", err)
		}
		rows, err = s.pool.Query(ctx, `SELECT `+syncColumns+` FROM trn_sync_jobs WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`, tenantID, limit, offset)
	}
	if err != nil {
		return nil, 0, mapWriteError("sync job", err)
	}
	defer rows.Close()

	out := make([]domain.SyncJob, 0, limit)
	for rows.Next() {
		j, err := scanSyncJob(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *j)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, mapWriteError("sync job", err)
	}
	return out, total, nil
}

// GetByID returns one job.
func (s *Store) GetSyncJobByID(ctx context.Context, tenantID, id string) (*domain.SyncJob, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+syncColumns+` FROM trn_sync_jobs WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return scanSyncJob(row)
}

// ClaimNext atomically reserves the oldest queued job. SKIP LOCKED keeps
// multiple workers from picking the same row.
func (s *Store) ClaimNext(ctx context.Context) (*domain.SyncJob, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE trn_sync_jobs
		SET status = 'RUNNING', started_at = now()
		WHERE id = (
			SELECT id FROM trn_sync_jobs
			WHERE status = 'QUEUED'
			ORDER BY created_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+syncColumns)
	return scanSyncJob(row)
}

// Complete marks a job finished successfully.
func (s *Store) CompleteSyncJob(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE trn_sync_jobs
		SET status = 'COMPLETED', finished_at = now(), bytes_done = bytes_total
		WHERE id = $1`, id)
	return mapWriteError("sync job", err)
}

// Fail marks a job failed with a reason.
func (s *Store) FailSyncJob(ctx context.Context, id, message string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE trn_sync_jobs
		SET status = 'FAILED', finished_at = now(), error = $2
		WHERE id = $1`, id, message)
	return mapWriteError("sync job", err)
}

// Requeue resets a failed job so the worker retries it.
func (s *Store) RequeueSyncJob(ctx context.Context, tenantID, id string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE trn_sync_jobs
		SET status = 'QUEUED', error = '', started_at = NULL, finished_at = NULL, bytes_done = 0
		WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return mapWriteError("sync job", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.WrapNotFound("sync job")
	}
	return nil
}

// UpdateProgress records download progress for the CMS progress bar.
func (s *Store) UpdateJobProgress(ctx context.Context, id string, done, total int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE trn_sync_jobs SET bytes_done = $2, bytes_total = $3 WHERE id = $1`, id, done, total)
	return mapWriteError("sync job", err)
}
