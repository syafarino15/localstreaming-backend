package http

import (
	"github.com/gin-gonic/gin"

	"github.com/alfateve/stayin-local-backend/internal/domain"
	"github.com/alfateve/stayin-local-backend/pkg/response"
)

// GetAppConfig returns the Application menu values.
func (h *AdminHandler) GetAppConfig(c *gin.Context) {
	cfg, err := h.admin.GetAppConfig(c.Request.Context(), ClaimsFrom(c), c.Query("tenant_id"))
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "konfigurasi aplikasi", cfg)
}

// UpdateAppConfig stores the Application menu values.
func (h *AdminHandler) UpdateAppConfig(c *gin.Context) {
	var req domain.AppConfigUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid", response.FieldError{Field: "body", Message: err.Error()})
		return
	}
	cfg, err := h.admin.UpdateAppConfig(c.Request.Context(), ClaimsFrom(c), c.Query("tenant_id"), req, c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "konfigurasi aplikasi tersimpan", cfg)
}

// ListFeatureFlags returns the tenant switches.
func (h *AdminHandler) ListFeatureFlags(c *gin.Context) {
	flags, err := h.admin.ListFeatureFlags(c.Request.Context(), ClaimsFrom(c), c.Query("tenant_id"))
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "daftar feature flag", gin.H{"items": flags})
}

// ToggleFeatureFlag flips one switch.
func (h *AdminHandler) ToggleFeatureFlag(c *gin.Context) {
	var req domain.FeatureFlagUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid", response.FieldError{Field: "body", Message: err.Error()})
		return
	}
	flag, err := h.admin.ToggleFeatureFlag(c.Request.Context(), ClaimsFrom(c), c.Query("tenant_id"), req, c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "feature flag diperbarui", flag)
}

// ListReleases returns APK packages.
func (h *AdminHandler) ListReleases(c *gin.Context) {
	items, total, err := h.admin.ListReleases(c.Request.Context(), ClaimsFrom(c), c.Query("tenant_id"), pagination(c))
	if err != nil {
		MapError(c, err)
		return
	}
	response.Paged(c, "daftar rilis APK", items, response.BuildMeta(queryInt(c, "page", 1), queryInt(c, "limit", 20), total))
}

// SetReleaseActive enables or disables a package.
func (h *AdminHandler) SetReleaseActive(c *gin.Context) {
	var req struct {
		IsActive bool `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid", response.FieldError{Field: "is_active", Message: "is_active wajib diisi"})
		return
	}
	if err := h.admin.SetReleaseActive(c.Request.Context(), ClaimsFrom(c), c.Param("releaseID"), req.IsActive, c.ClientIP()); err != nil {
		MapError(c, err)
		return
	}
	response.NoContent(c, "status rilis diperbarui")
}
