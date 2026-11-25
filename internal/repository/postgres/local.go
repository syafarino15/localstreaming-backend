package postgres

import (
	"context"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

// ---------------------------------------------------------------------------
// Tenant mirror reads for the local application.
//
// The local app needs the tenant identity to render branding and to keep
// catalog versions, but it never mutates tenant master data. UpsertTenantMirror
// is called by the distribution path when the main server pushes a tenant.
// ---------------------------------------------------------------------------

// GetTenantByID returns the mirrored tenant.
func (l *Local) GetTenantByID(ctx context.Context, id string) (*domain.Tenant, error) {
	row := l.pool.QueryRow(ctx, `SELECT `+tenantColumns+` FROM mst_tenants WHERE id = $1`, id)
	return scanTenant(row)
}

// GetBySlug returns the mirrored tenant by slug.
func (l *Local) GetBySlug(ctx context.Context, slug string) (*domain.Tenant, error) {
	row := l.pool.QueryRow(ctx, `SELECT `+tenantColumns+` FROM mst_tenants WHERE lower(slug) = lower($1)`, slug)
	return scanTenant(row)
}

// CatalogVersion reports the local catalog generation shown to TVs.
func (l *Local) CatalogVersion(ctx context.Context, tenantID string) (int, error) {
	var v int
	err := l.pool.QueryRow(ctx, `SELECT catalog_version FROM mst_tenants WHERE id = $1`, tenantID).Scan(&v)
	return v, mapWriteError("tenant", err)
}

// BumpCatalogVersion increments the local catalog generation.
func (l *Local) BumpCatalogVersion(ctx context.Context, tenantID string) (int, error) {
	var v int
	err := l.pool.QueryRow(ctx, `
		UPDATE mst_tenants SET catalog_version = catalog_version + 1
		WHERE id = $1
		RETURNING catalog_version`, tenantID).Scan(&v)
	return v, mapWriteError("tenant", err)
}

// UpsertTenantMirror keeps the local branding in step with the main server.
func (l *Local) UpsertTenantMirror(ctx context.Context, t *domain.Tenant) error {
	branding := "{}"
	if len(t.Branding) > 0 {
		branding = string(t.Branding)
	}
	_, err := l.pool.Exec(ctx, `
		INSERT INTO mst_tenants (id, slug, name, branding, license_quota, status)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6)
		ON CONFLICT (id) DO UPDATE SET
			slug = EXCLUDED.slug,
			name = EXCLUDED.name,
			branding = EXCLUDED.branding,
			license_quota = EXCLUDED.license_quota,
			status = EXCLUDED.status`,
		t.ID, t.Slug, t.Name, branding, t.LicenseQuota, t.Status)
	return mapWriteError("tenant", err)
}

// ---------------------------------------------------------------------------
// Local dashboard aggregates.
//
// These live on the local pool because rooms, TVs, announcements and files are
// operational data owned by the ship database.
// ---------------------------------------------------------------------------

// LocalOverviewMetrics returns the aggregate counters from the local database.
func (l *Local) LocalOverviewMetrics(ctx context.Context, tenantID string) (*domain.LocalOverview, error) {
	var o domain.LocalOverview
	if err := l.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM mst_rooms WHERE ($1 = '' OR tenant_id::text = $1)),
			(SELECT count(*) FROM mst_tv_devices WHERE ($1 = '' OR tenant_id::text = $1)),
			(SELECT count(*) FROM mst_tv_devices
			  WHERE ($1 = '' OR tenant_id::text = $1) AND status = 'ONLINE'
			    AND last_seen_at > now() - interval '5 minutes'),
			(SELECT count(*) FROM mst_movie_local WHERE ($1 = '' OR tenant_id::text = $1) AND status = 'READY'),
			(SELECT count(*) FROM mst_movie_local WHERE ($1 = '' OR tenant_id::text = $1)),
			(SELECT count(*) FROM mst_announcements
			  WHERE ($1 = '' OR tenant_id::text = $1) AND is_active
			    AND (active_from IS NULL OR active_from <= now())
			    AND (active_to IS NULL OR active_to >= now())),
			(SELECT COALESCE(sum(file_size_bytes), 0) FROM mst_movie_local
			  WHERE ($1 = '' OR tenant_id::text = $1) AND status = 'READY')
	`, tenantID).Scan(
		&o.TotalRooms, &o.TotalDevices, &o.OnlineDevices, &o.ReadyMovies,
		&o.TotalLocalMovies, &o.ActiveAnnouncements, &o.StorageUsedBytes,
	); err != nil {
		return nil, mapWriteError("overview", err)
	}
	return &o, nil
}

// WatchLogCount returns the number of playback records.
func (l *Local) WatchLogCount(ctx context.Context, tenantID string) (int64, error) {
	var n int64
	err := l.pool.QueryRow(ctx, `
		SELECT count(*) FROM trn_watch_logs WHERE ($1 = '' OR tenant_id::text = $1)`, tenantID).Scan(&n)
	return n, mapWriteError("watch log", err)
}

// WatchTrend is the daily watch series for the tenant dashboard.
func (l *Local) WatchTrend(ctx context.Context, tenantID string, days int) ([]domain.TrendPoint, error) {
	if days <= 0 {
		days = 7
	}
	rows, err := l.pool.Query(ctx, `
		SELECT to_char(d.day, 'YYYY-MM-DD') AS day,
		       COALESCE(count(w.id), 0)
		FROM generate_series(
		         (current_date - ($2::int - 1)),
		         current_date,
		         interval '1 day'
		     ) AS d(day)
		LEFT JOIN trn_watch_logs w
		       ON w.tenant_id = $1
		      AND date_trunc('day', w.updated_at) = d.day
		GROUP BY d.day
		ORDER BY d.day`, tenantID, days)
	if err != nil {
		return nil, mapWriteError("overview", err)
	}
	defer rows.Close()

	out := make([]domain.TrendPoint, 0, days)
	for rows.Next() {
		var p domain.TrendPoint
		if err := rows.Scan(&p.Date, &p.Count); err != nil {
			return nil, mapWriteError("overview", err)
		}
		out = append(out, p)
	}
	return out, nil
}

// MarkStaleDevicesOffline flips devices that stopped sending heartbeats. It is
// called periodically so the CMS never shows a dead TV as online.
func (l *Local) MarkStaleDevicesOffline(ctx context.Context, timeoutSeconds int) (int64, error) {
	// make_interval keeps the parameter numeric. Building the interval with
	// string concatenation makes PostgreSQL infer a text placeholder, and pgx
	// cannot encode an integer into text.
	tag, err := l.pool.Exec(ctx, `
		UPDATE mst_tv_devices
		SET status = 'OFFLINE'
		WHERE status = 'ONLINE'
		  AND (last_seen_at IS NULL
		       OR last_seen_at < now() - make_interval(secs => $1::double precision))`,
		timeoutSeconds)
	if err != nil {
		return 0, mapWriteError("device", err)
	}
	return tag.RowsAffected(), nil
}

// InventorySnapshot returns the inventory payload for one node. The node pushes
// it to the main server so the CMS can display ship state while offline.
func (l *Local) InventorySnapshot(ctx context.Context, tenantID, nodeCode string) ([]domain.InventoryEntry, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT movie_id, status, source, file_path, file_size_bytes, checksum_sha256, ready_at
		FROM mst_movie_local
		WHERE tenant_id = $1
		ORDER BY movie_id`, tenantID)
	if err != nil {
		return nil, mapWriteError("movie local", err)
	}
	defer rows.Close()

	out := make([]domain.InventoryEntry, 0, 128)
	for rows.Next() {
		var e domain.InventoryEntry
		if err := rows.Scan(&e.MovieID, &e.Status, &e.Source, &e.FilePath,
			&e.FileSizeBytes, &e.ChecksumSHA256, &e.ReadyAt); err != nil {
			return nil, mapWriteError("movie local", err)
		}
		out = append(out, e)
	}
	_ = nodeCode
	return out, nil
}

// ---------------------------------------------------------------------------
// Main-side dashboard counters.
// ---------------------------------------------------------------------------

// MainOverviewMetrics returns authoring counters from the main database.
func (s *Store) MainOverviewMetrics(ctx context.Context) (*domain.MainOverview, error) {
	var o domain.MainOverview
	if err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM mst_tenants),
			(SELECT count(*) FROM mst_tenants WHERE status = 'ACTIVE'),
			(SELECT count(*) FROM mst_edge_nodes),
			(SELECT count(*) FROM mst_edge_nodes WHERE status = 'ONLINE'),
			(SELECT count(*) FROM mst_movies),
			(SELECT count(*) FROM mst_movies WHERE status = 'PENDING')
	`).Scan(&o.TotalTenants, &o.ActiveTenants, &o.TotalNodes, &o.OnlineNodes,
		&o.TotalMovies, &o.PendingMovies); err != nil {
		return nil, mapWriteError("overview", err)
	}
	return &o, nil
}
