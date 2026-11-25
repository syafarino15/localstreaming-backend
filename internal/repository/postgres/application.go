package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

const appConfigColumns = `id, tenant_id, heartbeat_seconds, sync_seconds, minimum_app_version,
	stream_timeout_secs, paused_screen_text, updated_at`

func scanAppConfig(row pgx.Row) (*domain.AppConfig, error) {
	var cfg domain.AppConfig
	err := row.Scan(&cfg.ID, &cfg.TenantID, &cfg.HeartbeatSeconds, &cfg.SyncSeconds,
		&cfg.MinimumAppVersion, &cfg.StreamTimeoutSecs, &cfg.PausedScreenText, &cfg.UpdatedAt)
	if err != nil {
		return nil, mapWriteError("app config", err)
	}
	return &cfg, nil
}

// GetAppConfig returns the configuration of a tenant, or the platform default
// when the tenant has no row yet.
func (s *Store) GetAppConfig(ctx context.Context, tenantID string) (*domain.AppConfig, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+appConfigColumns+` FROM mst_app_configs WHERE tenant_id = $1`, tenantID)
	cfg, err := scanAppConfig(row)
	if err == nil {
		return cfg, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	// A missing row is normal for a freshly created tenant.
	tenant := tenantID
	return &domain.AppConfig{
		TenantID:          &tenant,
		HeartbeatSeconds:  30,
		SyncSeconds:       300,
		MinimumAppVersion: "1.0.0",
		StreamTimeoutSecs: 3600,
		PausedScreenText:  "Siaran dijeda oleh kru kapal.",
	}, nil
}

// UpsertAppConfig stores the configuration and returns the persisted row.
func (s *Store) UpsertAppConfig(ctx context.Context, cfg *domain.AppConfig) error {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO mst_app_configs (tenant_id, heartbeat_seconds, sync_seconds, minimum_app_version,
			stream_timeout_secs, paused_screen_text)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id) DO UPDATE
		SET heartbeat_seconds = EXCLUDED.heartbeat_seconds,
		    sync_seconds = EXCLUDED.sync_seconds,
		    minimum_app_version = EXCLUDED.minimum_app_version,
		    stream_timeout_secs = EXCLUDED.stream_timeout_secs,
		    paused_screen_text = EXCLUDED.paused_screen_text
		RETURNING `+appConfigColumns,
		cfg.TenantID, cfg.HeartbeatSeconds, cfg.SyncSeconds, cfg.MinimumAppVersion,
		cfg.StreamTimeoutSecs, cfg.PausedScreenText)
	saved, err := scanAppConfig(row)
	if err != nil {
		return err
	}
	*cfg = *saved
	return nil
}

const featureFlagColumns = `id, tenant_id, flag_key, enabled, note, updated_at`

func scanFlag(row pgx.Row) (*domain.FeatureFlag, error) {
	var f domain.FeatureFlag
	err := row.Scan(&f.ID, &f.TenantID, &f.Key, &f.Enabled, &f.Note, &f.UpdatedAt)
	if err != nil {
		return nil, mapWriteError("feature flag", err)
	}
	return &f, nil
}

// ListFeatureFlags returns every switch of a tenant ordered by key.
func (s *Store) ListFeatureFlags(ctx context.Context, tenantID string) ([]domain.FeatureFlag, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+featureFlagColumns+`
		FROM mst_feature_flags
		WHERE tenant_id = $1
		ORDER BY flag_key`, tenantID)
	if err != nil {
		return nil, mapWriteError("feature flag", err)
	}
	defer rows.Close()

	out := make([]domain.FeatureFlag, 0, 8)
	for rows.Next() {
		f, err := scanFlag(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	if err := rows.Err(); err != nil {
		return nil, mapWriteError("feature flag", err)
	}
	return out, nil
}

// UpsertFeatureFlag toggles a switch, creating it when the key is new.
func (s *Store) UpsertFeatureFlag(ctx context.Context, tenantID, key string, enabled bool) (*domain.FeatureFlag, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO mst_feature_flags (tenant_id, flag_key, enabled)
		VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, flag_key) DO UPDATE SET enabled = EXCLUDED.enabled
		RETURNING `+featureFlagColumns, tenantID, key, enabled)
	return scanFlag(row)
}

const releaseColumns = `id, tenant_id, version_code, version_name, file_path, file_size_bytes,
	checksum_sha256, changelog, is_active, created_at, updated_at`

func scanRelease(row pgx.Row) (*domain.AppRelease, error) {
	var r domain.AppRelease
	err := row.Scan(&r.ID, &r.TenantID, &r.VersionCode, &r.VersionName, &r.FilePath,
		&r.FileSizeBytes, &r.ChecksumSHA256, &r.Changelog, &r.IsActive, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, mapWriteError("release", err)
	}
	return &r, nil
}

// ListReleases returns packages for a tenant. An empty tenantID lists all.
func (s *Store) ListReleases(ctx context.Context, tenantID string, p domain.Pagination) ([]domain.AppRelease, int64, error) {
	limit, offset := paginate(p.Page, p.Limit)

	var (
		total int64
		rows  pgx.Rows
		err   error
	)
	if tenantID == "" {
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM mst_app_releases`).Scan(&total); err != nil {
			return nil, 0, mapWriteError("release", err)
		}
		rows, err = s.pool.Query(ctx, `SELECT `+releaseColumns+` FROM mst_app_releases ORDER BY version_code DESC LIMIT $1 OFFSET $2`, limit, offset)
	} else {
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM mst_app_releases WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
			return nil, 0, mapWriteError("release", err)
		}
		rows, err = s.pool.Query(ctx, `SELECT `+releaseColumns+` FROM mst_app_releases WHERE tenant_id = $1 ORDER BY version_code DESC LIMIT $2 OFFSET $3`, tenantID, limit, offset)
	}
	if err != nil {
		return nil, 0, mapWriteError("release", err)
	}
	defer rows.Close()

	out := make([]domain.AppRelease, 0, limit)
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *r)
	}
	return out, total, nil
}

// CreateRelease records a new package.
func (s *Store) CreateRelease(ctx context.Context, r *domain.AppRelease) error {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO mst_app_releases (tenant_id, version_code, version_name, file_path,
			file_size_bytes, checksum_sha256, changelog, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+releaseColumns,
		r.TenantID, r.VersionCode, r.VersionName, r.FilePath, r.FileSizeBytes,
		r.ChecksumSHA256, r.Changelog, r.IsActive)
	created, err := scanRelease(row)
	if err != nil {
		return err
	}
	*r = *created
	return nil
}

// SetReleaseActive enables or disables a package.
func (s *Store) SetReleaseActive(ctx context.Context, tenantID, id string, active bool) error {
	var (
		tag pgconn.CommandTag
		err error
	)
	if tenantID == "" {
		tag, err = s.pool.Exec(ctx, `UPDATE mst_app_releases SET is_active = $2 WHERE id = $1`, id, active)
	} else {
		tag, err = s.pool.Exec(ctx, `
			UPDATE mst_app_releases SET is_active = $3
			WHERE id = $2 AND tenant_id = $1`, tenantID, id, active)
	}
	if err != nil {
		return mapWriteError("release", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.WrapNotFound("release")
	}
	return nil
}

// LatestRelease returns the newest active package for a tenant.
func (s *Store) LatestRelease(ctx context.Context, tenantID string) (*domain.AppRelease, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+releaseColumns+`
		FROM mst_app_releases
		WHERE is_active AND (tenant_id = $1 OR tenant_id IS NULL)
		ORDER BY version_code DESC
		LIMIT 1`, tenantID)
	return scanRelease(row)
}
