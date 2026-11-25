package usecase

import (
	"context"
	"errors"
	"strings"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

// AppConfigDeps are the repositories needed by the Application menu.
type AppConfigDeps struct {
	Configs  domain.AppConfigRepository
	Flags    domain.FeatureFlagRepository
	Releases domain.ReleaseRepository
}

// resolveTenant returns the tenant the caller may operate on. A tenant admin is
// always pinned to its own tenant; a super admin may target any tenant by id.
func (a *Admin) resolveTenant(claims *domain.AuthClaims, requested string) (string, error) {
	if claims == nil {
		return "", domain.ErrUnauthorized
	}
	if claims.Role == domain.RoleTenantAdmin {
		if claims.TenantID == "" {
			return "", domain.ErrForbidden
		}
		return claims.TenantID, nil
	}
	target := strings.TrimSpace(requested)
	if target == "" {
		return "", domain.NewValidation("tenant_id", "tenant_id wajib untuk super admin")
	}
	return target, nil
}

// GetAppConfig returns the runtime parameters for a tenant.
func (a *Admin) GetAppConfig(ctx context.Context, claims *domain.AuthClaims, requestedTenant string) (*domain.AppConfig, error) {
	tenantID, err := a.resolveTenant(claims, requestedTenant)
	if err != nil {
		return nil, err
	}
	if a.d.AppConfigs == nil {
		return nil, domain.ErrUnavailable
	}
	return a.d.AppConfigs.GetAppConfig(ctx, tenantID)
}

// UpdateAppConfig validates and stores runtime parameters.
func (a *Admin) UpdateAppConfig(ctx context.Context, claims *domain.AuthClaims, requestedTenant string, req domain.AppConfigUpsertRequest, ip string) (*domain.AppConfig, error) {
	tenantID, err := a.resolveTenant(claims, requestedTenant)
	if err != nil {
		return nil, err
	}
	if a.d.AppConfigs == nil {
		return nil, domain.ErrUnavailable
	}

	validation := &domain.ValidationError{}
	if req.HeartbeatSeconds != 0 && (req.HeartbeatSeconds < 5 || req.HeartbeatSeconds > 600) {
		validation.AddField("heartbeat_seconds", "harus antara 5 dan 600 detik")
	}
	if req.SyncSeconds != 0 && (req.SyncSeconds < 10 || req.SyncSeconds > 86400) {
		validation.AddField("sync_seconds", "harus antara 10 dan 86400 detik")
	}
	if len(validation.Fields) > 0 {
		return nil, validation
	}

	current, err := a.d.AppConfigs.GetAppConfig(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if req.HeartbeatSeconds > 0 {
		current.HeartbeatSeconds = req.HeartbeatSeconds
	}
	if req.SyncSeconds > 0 {
		current.SyncSeconds = req.SyncSeconds
	}
	if req.MinimumAppVersion != "" {
		current.MinimumAppVersion = strings.TrimSpace(req.MinimumAppVersion)
	}
	if req.StreamTimeoutSecs > 0 {
		current.StreamTimeoutSecs = req.StreamTimeoutSecs
	}
	if req.PausedScreenText != "" {
		current.PausedScreenText = strings.TrimSpace(req.PausedScreenText)
	}

	if err := a.d.AppConfigs.UpsertAppConfig(ctx, current); err != nil {
		return nil, err
	}
	a.audit(ctx, claims, "APP_CONFIG_UPDATE", "app_config", current.ID, ip)
	return current, nil
}

// ListFeatureFlags returns the switches of a tenant.
func (a *Admin) ListFeatureFlags(ctx context.Context, claims *domain.AuthClaims, requestedTenant string) ([]domain.FeatureFlag, error) {
	tenantID, err := a.resolveTenant(claims, requestedTenant)
	if err != nil {
		return nil, err
	}
	if a.d.Flags == nil {
		return nil, domain.ErrUnavailable
	}
	return a.d.Flags.ListFeatureFlags(ctx, tenantID)
}

// ToggleFeatureFlag flips one switch and pushes a refresh to the TVs.
func (a *Admin) ToggleFeatureFlag(ctx context.Context, claims *domain.AuthClaims, requestedTenant string, req domain.FeatureFlagUpsertRequest, ip string) (*domain.FeatureFlag, error) {
	key := strings.TrimSpace(req.Key)
	if key == "" {
		return nil, domain.NewValidation("flag_key", "flag_key wajib diisi")
	}
	tenantID, err := a.resolveTenant(claims, requestedTenant)
	if err != nil {
		return nil, err
	}
	if a.d.Flags == nil {
		return nil, domain.ErrUnavailable
	}

	flag, err := a.d.Flags.UpsertFeatureFlag(ctx, tenantID, key, req.Enabled)
	if err != nil {
		return nil, err
	}
	a.d.Notifier.BroadcastTenant(tenantID, "REFRESH_CONFIG", map[string]any{
		"reason": "FEATURE_FLAG",
		"flag":   flag.Key,
	})
	a.audit(ctx, claims, "FEATURE_FLAG_UPDATE", "feature_flag", flag.ID, ip, map[string]any{
		"flag":    flag.Key,
		"enabled": flag.Enabled,
	})
	return flag, nil
}

// ListReleases returns APK packages.
func (a *Admin) ListReleases(ctx context.Context, claims *domain.AuthClaims, requestedTenant string, p domain.Pagination) ([]domain.AppRelease, int64, error) {
	scope := ""
	if claims.Role == domain.RoleTenantAdmin {
		scope = claims.TenantID
	} else if strings.TrimSpace(requestedTenant) != "" {
		scope = strings.TrimSpace(requestedTenant)
	}
	if a.d.Releases == nil {
		return nil, 0, domain.ErrUnavailable
	}
	return a.d.Releases.ListReleases(ctx, scope, p)
}

// CreateRelease records an uploaded APK.
func (a *Admin) CreateRelease(ctx context.Context, claims *domain.AuthClaims, requestedTenant string, req domain.ReleaseUpsertRequest, ip string) (*domain.AppRelease, error) {
	if req.VersionCode <= 0 {
		return nil, domain.NewValidation("version_code", "version_code wajib lebih besar dari 0")
	}
	if strings.TrimSpace(req.VersionName) == "" {
		return nil, domain.NewValidation("version_name", "version_name wajib diisi")
	}
	if strings.TrimSpace(req.FilePath) == "" {
		return nil, domain.NewValidation("file_path", "berkas APK belum diunggah")
	}
	tenantID, err := a.resolveTenant(claims, requestedTenant)
	if err != nil {
		return nil, err
	}
	if a.d.Releases == nil {
		return nil, domain.ErrUnavailable
	}

	active := true
	if req.IsActive != nil {
		active = *req.IsActive
	}

	release := &domain.AppRelease{
		TenantID:       &tenantID,
		VersionCode:    req.VersionCode,
		VersionName:    strings.TrimSpace(req.VersionName),
		FilePath:       strings.TrimSpace(req.FilePath),
		FileSizeBytes:  req.FileSize,
		ChecksumSHA256: req.Checksum,
		Changelog:      strings.TrimSpace(req.Changelog),
		IsActive:       active,
	}
	if err := a.d.Releases.CreateRelease(ctx, release); err != nil {
		return nil, err
	}

	a.d.Notifier.BroadcastTenant(tenantID, "APK_AVAILABLE", map[string]any{
		"path":    "/api/v1/assets/" + release.FilePath,
		"version": release.VersionName,
		"size":    release.FileSizeBytes,
	})
	a.audit(ctx, claims, "RELEASE_CREATE", "release", release.ID, ip, map[string]any{
		"version": release.VersionName,
	})
	return release, nil
}

// SetReleaseActive enables or disables a package.
func (a *Admin) SetReleaseActive(ctx context.Context, claims *domain.AuthClaims, id string, active bool, ip string) error {
	scope := ""
	if claims.Role == domain.RoleTenantAdmin {
		scope = claims.TenantID
	}
	if a.d.Releases == nil {
		return domain.ErrUnavailable
	}
	if err := a.d.Releases.SetReleaseActive(ctx, scope, id, active); err != nil {
		return err
	}
	a.audit(ctx, claims, "RELEASE_STATUS", "release", id, ip, map[string]any{"active": active})
	return nil
}

// LatestRelease exposes the current package to the TV client.
func (a *Admin) LatestRelease(ctx context.Context, claims *domain.AuthClaims) (*domain.AppRelease, error) {
	if a.d.Releases == nil {
		return nil, domain.ErrUnavailable
	}
	release, err := a.d.Releases.LatestRelease(ctx, claims.TenantID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, domain.WrapNotFound("release")
		}
		return nil, err
	}
	return release, nil
}
