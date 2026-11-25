package domain

import (
	"context"
	"time"
)

// AppConfig carries the runtime parameters exposed in the Application menu.
type AppConfig struct {
	ID                string    `json:"id"`
	TenantID          *string   `json:"tenant_id"`
	HeartbeatSeconds  int       `json:"heartbeat_seconds"`
	SyncSeconds       int       `json:"sync_seconds"`
	MinimumAppVersion string    `json:"minimum_app_version"`
	StreamTimeoutSecs int       `json:"stream_timeout_secs"`
	PausedScreenText  string    `json:"paused_screen_text"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// FeatureFlag is a per tenant switch the TV reads on boot.
type FeatureFlag struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Key       string    `json:"flag_key"`
	Enabled   bool      `json:"enabled"`
	Note      string    `json:"note"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AppConfigUpsertRequest is the Application menu form payload.
type AppConfigUpsertRequest struct {
	HeartbeatSeconds  int    `json:"heartbeat_seconds"`
	SyncSeconds       int    `json:"sync_seconds"`
	MinimumAppVersion string `json:"minimum_app_version"`
	StreamTimeoutSecs int    `json:"stream_timeout_secs"`
	PausedScreenText  string `json:"paused_screen_text"`
}

// FeatureFlagUpsertRequest toggles one switch.
type FeatureFlagUpsertRequest struct {
	Key     string `json:"flag_key" binding:"required"`
	Enabled bool   `json:"enabled"`
}

// ReleaseUpsertRequest describes an uploaded package.
type ReleaseUpsertRequest struct {
	VersionCode int    `json:"version_code" binding:"required"`
	VersionName string `json:"version_name" binding:"required"`
	Changelog   string `json:"changelog"`
	FilePath    string `json:"file_path"`
	FileSize    int64  `json:"file_size_bytes"`
	Checksum    string `json:"checksum_sha256"`
	IsActive    *bool  `json:"is_active"`
}

// AppConfigRepository persists runtime parameters.
type AppConfigRepository interface {
	GetAppConfig(ctx context.Context, tenantID string) (*AppConfig, error)
	UpsertAppConfig(ctx context.Context, cfg *AppConfig) error
}

// FeatureFlagRepository persists per tenant switches.
type FeatureFlagRepository interface {
	ListFeatureFlags(ctx context.Context, tenantID string) ([]FeatureFlag, error)
	UpsertFeatureFlag(ctx context.Context, tenantID, key string, enabled bool) (*FeatureFlag, error)
}

// ReleaseRepository persists APK releases.
type ReleaseRepository interface {
	ListReleases(ctx context.Context, tenantID string, p Pagination) ([]AppRelease, int64, error)
	CreateRelease(ctx context.Context, r *AppRelease) error
	SetReleaseActive(ctx context.Context, tenantID, id string, active bool) error
	LatestRelease(ctx context.Context, tenantID string) (*AppRelease, error)
}
