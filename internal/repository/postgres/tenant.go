package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

// tenantColumns keeps every SELECT consistent with scanTenant.
const tenantColumns = `id, slug, name, branding, license_quota, status, catalog_version, created_at, updated_at`

func scanTenant(row pgx.Row) (*domain.Tenant, error) {
	var (
		t        domain.Tenant
		branding []byte
		version  int
	)
	err := row.Scan(&t.ID, &t.Slug, &t.Name, &branding, &t.LicenseQuota, &t.Status, &version, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, mapWriteError("tenant", err)
	}
	t.Branding = rawJSON(branding)
	return &t, nil
}

// GetBySlug returns the tenant identified by its whitelabel slug.
func (s *Store) GetBySlug(ctx context.Context, slug string) (*domain.Tenant, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+tenantColumns+` FROM mst_tenants WHERE lower(slug) = lower($1)`, slug)
	return scanTenant(row)
}

// GetByID returns the tenant identified by primary key.
func (s *Store) GetTenantByID(ctx context.Context, id string) (*domain.Tenant, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+tenantColumns+` FROM mst_tenants WHERE id = $1`, id)
	return scanTenant(row)
}

// List returns tenants ordered by creation date.
func (s *Store) ListTenants(ctx context.Context, p domain.Pagination) ([]domain.Tenant, int64, error) {
	limit, offset := paginate(p.Page, p.Limit)

	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM mst_tenants`).Scan(&total); err != nil {
		return nil, 0, mapWriteError("tenant", err)
	}

	rows, err := s.pool.Query(ctx, `SELECT `+tenantColumns+` FROM mst_tenants ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, mapWriteError("tenant", err)
	}
	defer rows.Close()

	out := make([]domain.Tenant, 0, limit)
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, mapWriteError("tenant", err)
	}
	return out, total, nil
}

// Create inserts a tenant.
func (s *Store) CreateTenant(ctx context.Context, t *domain.Tenant) error {
	branding, err := marshalJSON(t.Branding)
	if err != nil {
		return err
	}
	if t.Status == "" {
		t.Status = "ACTIVE"
	}
	if t.LicenseQuota <= 0 {
		t.LicenseQuota = 50
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO mst_tenants (slug, name, branding, license_quota, status)
		VALUES ($1, $2, $3::jsonb, $4, $5)
		RETURNING `+tenantColumns,
		t.Slug, t.Name, branding, t.LicenseQuota, t.Status)
	created, err := scanTenant(row)
	if err != nil {
		return err
	}
	*t = *created
	return nil
}

// Update rewrites the mutable tenant fields.
func (s *Store) UpdateTenant(ctx context.Context, t *domain.Tenant) error {
	branding, err := marshalJSON(t.Branding)
	if err != nil {
		return err
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE mst_tenants
		SET slug = $2, name = $3, branding = $4::jsonb, license_quota = $5
		WHERE id = $1
		RETURNING `+tenantColumns,
		t.ID, t.Slug, t.Name, branding, t.LicenseQuota)
	updated, err := scanTenant(row)
	if err != nil {
		return err
	}
	*t = *updated
	return nil
}

// SetStatus suspends or reactivates a tenant.
func (s *Store) SetTenantStatus(ctx context.Context, id, status string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE mst_tenants SET status = $2 WHERE id = $1`, id, status)
	if err != nil {
		return mapWriteError("tenant", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.WrapNotFound("tenant")
	}
	return nil
}

// UpdateLicense changes the device quota of a tenant.
func (s *Store) UpdateLicense(ctx context.Context, id string, quota int) error {
	tag, err := s.pool.Exec(ctx, `UPDATE mst_tenants SET license_quota = $2 WHERE id = $1`, id, quota)
	if err != nil {
		return mapWriteError("tenant", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.WrapNotFound("tenant")
	}
	return nil
}

// CountActive counts tenants with ACTIVE status.
func (s *Store) CountActiveTenants(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM mst_tenants WHERE status = 'ACTIVE'`).Scan(&n)
	return n, mapWriteError("tenant", err)
}

// CatalogVersion returns the current catalog version for cache busting.
func (s *Store) CatalogVersion(ctx context.Context, tenantID string) (int, error) {
	var v int
	err := s.pool.QueryRow(ctx, `SELECT catalog_version FROM mst_tenants WHERE id = $1`, tenantID).Scan(&v)
	if err != nil {
		return 0, mapWriteError("tenant", err)
	}
	return v, nil
}

// BumpCatalogVersion increments the catalog version so TVs know to refetch.
func (s *Store) BumpCatalogVersion(ctx context.Context, tenantID string) (int, error) {
	var v int
	err := s.pool.QueryRow(ctx, `
		UPDATE mst_tenants SET catalog_version = catalog_version + 1
		WHERE id = $1
		RETURNING catalog_version`, tenantID).Scan(&v)
	if err != nil {
		return 0, mapWriteError("tenant", err)
	}
	return v, nil
}

// TenantBranding is a convenience accessor used by the TV payload builder.
func TenantBranding(t *domain.Tenant) map[string]any {
	out := map[string]any{}
	if len(t.Branding) == 0 {
		return out
	}
	_ = json.Unmarshal(t.Branding, &out)
	return out
}

// ensureTenantExists guards FK dependent writes with a clear error.
func (s *Store) ensureTenantExists(ctx context.Context, tenantID string) error {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mst_tenants WHERE id = $1)`, tenantID).Scan(&exists)
	if err != nil {
		return mapWriteError("tenant", err)
	}
	if !exists {
		return domain.WrapNotFound("tenant")
	}
	return nil
}

var _ = time.Time{}
