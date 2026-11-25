package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

const announcementColumns = `id, tenant_id, message, severity, active_from, active_to, is_active, created_at, updated_at`

func scanAnnouncement(row pgx.Row) (*domain.Announcement, error) {
	var a domain.Announcement
	err := row.Scan(&a.ID, &a.TenantID, &a.Message, &a.Severity, &a.ActiveFrom, &a.ActiveTo, &a.IsActive, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, mapWriteError("announcement", err)
	}
	return &a, nil
}

// List returns announcements for the CMS.
func (l *Local) ListAnnouncements(ctx context.Context, tenantID string, p domain.Pagination) ([]domain.Announcement, int64, error) {
	limit, offset := paginate(p.Page, p.Limit)

	var total int64
	if err := l.pool.QueryRow(ctx, `SELECT count(*) FROM mst_announcements WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
		return nil, 0, mapWriteError("announcement", err)
	}

	rows, err := l.pool.Query(ctx, `
		SELECT `+announcementColumns+`
		FROM mst_announcements
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3`, tenantID, limit, offset)
	if err != nil {
		return nil, 0, mapWriteError("announcement", err)
	}
	defer rows.Close()

	out := make([]domain.Announcement, 0, limit)
	for rows.Next() {
		a, err := scanAnnouncement(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, mapWriteError("announcement", err)
	}
	return out, total, nil
}

// ListActive returns the announcements currently on screen.
func (l *Local) ListActive(ctx context.Context, tenantID string) ([]domain.Announcement, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT `+announcementColumns+`
		FROM mst_announcements
		WHERE tenant_id = $1
		  AND is_active
		  AND (active_from IS NULL OR active_from <= now())
		  AND (active_to IS NULL OR active_to >= now())
		ORDER BY
		  CASE severity WHEN 'EMERGENCY' THEN 0 WHEN 'WARNING' THEN 1 ELSE 2 END,
		  created_at DESC`, tenantID)
	if err != nil {
		return nil, mapWriteError("announcement", err)
	}
	defer rows.Close()

	out := make([]domain.Announcement, 0, 8)
	for rows.Next() {
		a, err := scanAnnouncement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, nil
}

// Create inserts an announcement.
func (l *Local) CreateAnnouncement(ctx context.Context, a *domain.Announcement) error {
	if a.Severity == "" {
		a.Severity = "INFO"
	}
	row := l.pool.QueryRow(ctx, `
		INSERT INTO mst_announcements (tenant_id, message, severity, active_from, active_to, is_active)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+announcementColumns,
		a.TenantID, a.Message, a.Severity, a.ActiveFrom, a.ActiveTo, a.IsActive)
	created, err := scanAnnouncement(row)
	if err != nil {
		return err
	}
	*a = *created
	return nil
}

// Update rewrites an announcement.
func (l *Local) UpdateAnnouncement(ctx context.Context, a *domain.Announcement) error {
	row := l.pool.QueryRow(ctx, `
		UPDATE mst_announcements
		SET message = $3, severity = $4, active_from = $5, active_to = $6, is_active = $7
		WHERE tenant_id = $1 AND id = $2
		RETURNING `+announcementColumns,
		a.TenantID, a.ID, a.Message, a.Severity, a.ActiveFrom, a.ActiveTo, a.IsActive)
	updated, err := scanAnnouncement(row)
	if err != nil {
		return err
	}
	*a = *updated
	return nil
}

// Delete removes an announcement.
func (l *Local) DeleteAnnouncement(ctx context.Context, tenantID, id string) error {
	tag, err := l.pool.Exec(ctx, `DELETE FROM mst_announcements WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return mapWriteError("announcement", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.WrapNotFound("announcement")
	}
	return nil
}

// CountActive counts announcements currently on screen.
func (l *Local) CountActiveAnnouncements(ctx context.Context, tenantID string) (int64, error) {
	var n int64
	err := l.pool.QueryRow(ctx, `
		SELECT count(*) FROM mst_announcements
		WHERE tenant_id = $1 AND is_active
		  AND (active_from IS NULL OR active_from <= now())
		  AND (active_to IS NULL OR active_to >= now())`, tenantID).Scan(&n)
	return n, mapWriteError("announcement", err)
}
