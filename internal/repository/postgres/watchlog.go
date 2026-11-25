package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

const watchColumns = `id, tenant_id, device_id, movie_id, position_seconds, duration_seconds, completed, updated_at`

func scanWatch(row pgx.Row) (*domain.WatchLog, error) {
	var w domain.WatchLog
	err := row.Scan(&w.ID, &w.TenantID, &w.DeviceID, &w.MovieID, &w.PositionSeconds,
		&w.DurationSeconds, &w.Completed, &w.UpdatedAt)
	if err != nil {
		return nil, mapWriteError("watch log", err)
	}
	return &w, nil
}

// Upsert records playback progress. One row per device and movie is kept so
// the TV can resume exactly where it stopped.
func (l *Local) UpsertWatchLog(ctx context.Context, w *domain.WatchLog) error {
	row := l.pool.QueryRow(ctx, `
		INSERT INTO trn_watch_logs (tenant_id, device_id, movie_id, position_seconds, duration_seconds, completed)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (device_id, movie_id) DO UPDATE
		SET position_seconds = EXCLUDED.position_seconds,
		    duration_seconds = EXCLUDED.duration_seconds,
		    completed = EXCLUDED.completed,
		    updated_at = now()
		RETURNING `+watchColumns,
		w.TenantID, w.DeviceID, w.MovieID, w.PositionSeconds, w.DurationSeconds, w.Completed)
	saved, err := scanWatch(row)
	if err != nil {
		return err
	}
	*w = *saved
	return nil
}

// ListByDevice returns resume points for one TV.
func (l *Local) ListByDevice(ctx context.Context, tenantID, deviceID string, limit int) ([]domain.WatchLog, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := l.pool.Query(ctx, `
		SELECT `+watchColumns+`
		FROM trn_watch_logs
		WHERE tenant_id = $1 AND device_id = $2
		ORDER BY updated_at DESC
		LIMIT $3`, tenantID, deviceID, limit)
	if err != nil {
		return nil, mapWriteError("watch log", err)
	}
	defer rows.Close()

	out := make([]domain.WatchLog, 0, limit)
	for rows.Next() {
		w, err := scanWatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, nil
}
