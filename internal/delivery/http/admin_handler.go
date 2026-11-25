package http

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/alfateve/stayin-local-backend/internal/domain"
	"github.com/alfateve/stayin-local-backend/internal/platform/storage"
	"github.com/alfateve/stayin-local-backend/internal/usecase"
	"github.com/alfateve/stayin-local-backend/pkg/response"
)

// AuthHandler serves login, refresh and profile.
type AuthHandler struct {
	auth         *usecase.Auth
	cookieSecure bool
	cookieDomain string
}

// NewAuthHandler builds the auth handler.
func NewAuthHandler(auth *usecase.Auth, cookieSecure bool, cookieDomain string) *AuthHandler {
	return &AuthHandler{auth: auth, cookieSecure: cookieSecure, cookieDomain: cookieDomain}
}

// Login authenticates an admin and sets the session cookies.
func (h *AuthHandler) Login(c *gin.Context) {
	var req domain.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload login tidak valid",
			response.FieldError{Field: "body", Message: err.Error()})
		return
	}

	result, err := h.auth.Login(c.Request.Context(), req, c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}

	h.setCookies(c, result)
	response.OK(c, "login berhasil", result)
}

// Refresh exchanges a refresh token.
func (h *AuthHandler) Refresh(c *gin.Context) {
	token := ""
	if cookie, err := c.Cookie("refresh_token"); err == nil {
		token = cookie
	}
	if token == "" {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := c.ShouldBindJSON(&body); err == nil {
			token = body.RefreshToken
		}
	}
	if token == "" {
		response.Unauthorized(c, "refresh token tidak ditemukan")
		return
	}

	result, err := h.auth.Refresh(c.Request.Context(), token)
	if err != nil {
		MapError(c, err)
		return
	}
	h.setCookies(c, result)
	response.OK(c, "sesi diperbarui", result)
}

// Logout clears the session cookies.
func (h *AuthHandler) Logout(c *gin.Context) {
	h.clearCookies(c)
	response.NoContent(c, "logout berhasil")
}

// Me returns the authenticated account.
func (h *AuthHandler) Me(c *gin.Context) {
	claims := ClaimsFrom(c)
	if claims == nil {
		response.Unauthorized(c, "sesi tidak valid")
		return
	}

	user, err := h.auth.Profile(c.Request.Context(), claims.UserID)
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "profil pengguna", user)
}

// ChangePassword updates the caller's password.
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	claims := ClaimsFrom(c)
	if claims == nil {
		response.Unauthorized(c, "sesi tidak valid")
		return
	}

	var req struct {
		OldPassword string `json:"old_password" binding:"required"`
		NewPassword string `json:"new_password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid",
			response.FieldError{Field: "body", Message: err.Error()})
		return
	}

	if err := h.auth.ChangePassword(c.Request.Context(), claims.UserID, req.OldPassword, req.NewPassword, c.ClientIP()); err != nil {
		MapError(c, err)
		return
	}
	h.clearCookies(c)
	response.NoContent(c, "password berhasil diubah, silakan login kembali")
}

func (h *AuthHandler) setCookies(c *gin.Context, result *domain.LoginResponse) {
	maxAge := int(result.ExpiresIn)
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("access_token", result.AccessToken, maxAge, "/", h.cookieDomain, h.cookieSecure, true)
	c.SetCookie("refresh_token", result.RefreshToken, 7*24*3600, "/api/v1/auth", h.cookieDomain, h.cookieSecure, true)
}

func (h *AuthHandler) clearCookies(c *gin.Context) {
	c.SetCookie("access_token", "", -1, "/", h.cookieDomain, h.cookieSecure, true)
	c.SetCookie("refresh_token", "", -1, "/api/v1/auth", h.cookieDomain, h.cookieSecure, true)
}

// AdminHandler serves the CMS endpoints for both portals.
type AdminHandler struct {
	admin    *usecase.Admin
	store    AssetStore
	uploader *Uploader
}

// AssetStore is the media writer used by upload handlers.
type AssetStore interface {
	PostersDir() string
	BannersDir() string
	APKDir() string
	Relative(absolute string) (string, error)
}

// NewAdminHandler builds the admin handler.
func NewAdminHandler(admin *usecase.Admin, store *storage.Store, uploader *Uploader) *AdminHandler {
	return &AdminHandler{admin: admin, store: store, uploader: uploader}
}

// Overview returns the landing dashboard.
func (h *AdminHandler) Overview(c *gin.Context) {
	data, err := h.admin.Overview(c.Request.Context(), ClaimsFrom(c))
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "ringkasan dashboard", data)
}

// Trend returns the watch time series.
func (h *AdminHandler) Trend(c *gin.Context) {
	days := queryInt(c, "days", 7)
	points, err := h.admin.Trend(c.Request.Context(), ClaimsFrom(c), days)
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "tren tontonan", gin.H{"items": points})
}

// TopMovies returns the most watched titles.
func (h *AdminHandler) TopMovies(c *gin.Context) {
	days := queryInt(c, "days", 30)
	limit := queryInt(c, "limit", 10)
	items, err := h.admin.TopMovies(c.Request.Context(), ClaimsFrom(c), days, limit)
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "film terpopuler", gin.H{"items": items})
}

// ListTenants returns whitelabel partners.
func (h *AdminHandler) ListTenants(c *gin.Context) {
	items, total, err := h.admin.ListTenants(c.Request.Context(), pagination(c))
	if err != nil {
		MapError(c, err)
		return
	}
	response.Paged(c, "daftar tenant", items, response.BuildMeta(queryInt(c, "page", 1), queryInt(c, "limit", 20), total))
}

// CreateTenant registers a partner.
func (h *AdminHandler) CreateTenant(c *gin.Context) {
	var req domain.TenantUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid", response.FieldError{Field: "body", Message: err.Error()})
		return
	}
	tenant, err := h.admin.CreateTenant(c.Request.Context(), req, ClaimsFrom(c), c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}
	response.Created(c, "tenant berhasil dibuat", tenant)
}

// UpdateTenant rewrites a partner.
func (h *AdminHandler) UpdateTenant(c *gin.Context) {
	var req domain.TenantUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid", response.FieldError{Field: "body", Message: err.Error()})
		return
	}
	tenant, err := h.admin.UpdateTenant(c.Request.Context(), c.Param("tenantID"), req, ClaimsFrom(c), c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "tenant diperbarui", tenant)
}

// SetTenantStatus suspends or reactivates a partner.
func (h *AdminHandler) SetTenantStatus(c *gin.Context) {
	var req struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid", response.FieldError{Field: "status", Message: "status wajib diisi"})
		return
	}
	if err := h.admin.SetTenantStatus(c.Request.Context(), c.Param("tenantID"), req.Status, ClaimsFrom(c), c.ClientIP()); err != nil {
		MapError(c, err)
		return
	}
	response.NoContent(c, "status tenant diperbarui")
}

// ListUsers returns admin accounts.
func (h *AdminHandler) ListUsers(c *gin.Context) {
	items, total, err := h.admin.ListUsers(c.Request.Context(), ClaimsFrom(c), pagination(c))
	if err != nil {
		MapError(c, err)
		return
	}
	response.Paged(c, "daftar pengguna", items, response.BuildMeta(queryInt(c, "page", 1), queryInt(c, "limit", 20), total))
}

// CreateUser adds an account.
func (h *AdminHandler) CreateUser(c *gin.Context) {
	var req domain.UserUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid", response.FieldError{Field: "body", Message: err.Error()})
		return
	}
	user, err := h.admin.CreateUser(c.Request.Context(), req, ClaimsFrom(c), c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}
	response.Created(c, "pengguna berhasil dibuat", user)
}

// UpdateUser rewrites an account.
func (h *AdminHandler) UpdateUser(c *gin.Context) {
	var req domain.UserUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid", response.FieldError{Field: "body", Message: err.Error()})
		return
	}
	user, err := h.admin.UpdateUser(c.Request.Context(), c.Param("userID"), req, ClaimsFrom(c), c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "pengguna diperbarui", user)
}

// ListRooms returns cabins.
func (h *AdminHandler) ListRooms(c *gin.Context) {
	items, total, err := h.admin.ListRooms(c.Request.Context(), ClaimsFrom(c), pagination(c))
	if err != nil {
		MapError(c, err)
		return
	}
	response.Paged(c, "daftar kamar", items, response.BuildMeta(queryInt(c, "page", 1), queryInt(c, "limit", 20), total))
}

// CreateRoom adds a cabin.
func (h *AdminHandler) CreateRoom(c *gin.Context) {
	var req domain.RoomUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid", response.FieldError{Field: "body", Message: err.Error()})
		return
	}
	room, err := h.admin.CreateRoom(c.Request.Context(), ClaimsFrom(c), req, c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}
	response.Created(c, "kamar berhasil dibuat", room)
}

// UpdateRoom rewrites a cabin.
func (h *AdminHandler) UpdateRoom(c *gin.Context) {
	var req domain.RoomUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid", response.FieldError{Field: "body", Message: err.Error()})
		return
	}
	room, err := h.admin.UpdateRoom(c.Request.Context(), ClaimsFrom(c), c.Param("roomID"), req, c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "kamar diperbarui", room)
}

// CheckinGuest sets the VIP guest greeting and pushes it to the TV live.
func (h *AdminHandler) CheckinGuest(c *gin.Context) {
	var req domain.GuestCheckinRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid", response.FieldError{Field: "body", Message: err.Error()})
		return
	}
	room, err := h.admin.CheckinGuest(c.Request.Context(), ClaimsFrom(c), c.Param("roomID"), req, c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "tamu VIP tersimpan dan layar TV diperbarui", room)
}

// CheckoutGuest clears the VIP guest greeting.
func (h *AdminHandler) CheckoutGuest(c *gin.Context) {
	if err := h.admin.CheckoutGuest(c.Request.Context(), ClaimsFrom(c), c.Param("roomID"), c.ClientIP()); err != nil {
		MapError(c, err)
		return
	}
	response.NoContent(c, "tamu telah check-out")
}

// ListMovies returns the tenant catalog, or every tenant for a super admin when
// tenant_id is supplied.
func (h *AdminHandler) ListMovies(c *gin.Context) {
	filter := domain.MovieFilter{
		Page:   queryInt(c, "page", 1),
		Limit:  queryInt(c, "limit", 20),
		Genre:  c.Query("genre"),
		Search: c.Query("search"),
	}
	if claims := ClaimsFrom(c); claims != nil && claims.Role == domain.RoleSuperAdmin {
		filter.TenantID = strings.TrimSpace(c.Query("tenant_id"))
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		filter.Statuses = []domain.MovieStatus{domain.MovieStatus(strings.ToUpper(status))}
	}

	items, total, err := h.admin.ListMovies(c.Request.Context(), ClaimsFrom(c), filter)
	if err != nil {
		MapError(c, err)
		return
	}
	response.Paged(c, "daftar film", items, response.BuildMeta(filter.Page, filter.Limit, total))
}

// GetMovie returns one catalog entry with its ship file state.
func (h *AdminHandler) GetMovie(c *gin.Context) {
	item, detail, err := h.admin.GetMovie(c.Request.Context(), ClaimsFrom(c), c.Param("movieID"))
	if err != nil {
		MapError(c, err)
		return
	}
	// Same shape as the list endpoint, plus the metadata detail row.
	response.OK(c, "detail film", gin.H{
		"movie":  item.Movie,
		"local":  item.Local,
		"detail": detail,
	})
}

// UpdateMovie rewrites metadata.
func (h *AdminHandler) UpdateMovie(c *gin.Context) {
	var req domain.MovieUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid", response.FieldError{Field: "body", Message: err.Error()})
		return
	}
	movie, err := h.admin.UpdateMovie(c.Request.Context(), ClaimsFrom(c), c.Param("movieID"), req, c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "metadata film diperbarui", movie)
}

// DeleteMovie removes a catalog entry.
func (h *AdminHandler) DeleteMovie(c *gin.Context) {
	if err := h.admin.DeleteMovie(c.Request.Context(), ClaimsFrom(c), c.Param("movieID"), c.ClientIP()); err != nil {
		MapError(c, err)
		return
	}
	response.NoContent(c, "film dihapus dari katalog")
}

// CreateMovie registers a master catalog entry. The video file is not uploaded
// here: the movie waits as PENDING until it reaches the ship.
func (h *AdminHandler) CreateMovie(c *gin.Context) {
	var req domain.MovieCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid", response.FieldError{Field: "body", Message: err.Error()})
		return
	}
	item, err := h.admin.CreateMovie(c.Request.Context(), ClaimsFrom(c), req, c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}
	response.Created(c, "film ditambahkan ke katalog", item)
}

// ListInventory returns mst_movie_local rows, the real files on the ship.
func (h *AdminHandler) ListInventory(c *gin.Context) {
	filter := domain.MovieLocalFilter{
		Page:   queryInt(c, "page", 1),
		Limit:  queryInt(c, "limit", 50),
		Search: c.Query("search"),
	}
	if claims := ClaimsFrom(c); claims != nil && claims.Role == domain.RoleSuperAdmin {
		filter.TenantID = strings.TrimSpace(c.Query("tenant_id"))
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		filter.Statuses = []domain.LocalMovieStatus{domain.LocalMovieStatus(strings.ToUpper(status))}
	}
	if source := strings.TrimSpace(c.Query("source")); source != "" {
		filter.Source = domain.MovieSource(strings.ToUpper(source))
	}

	items, total, err := h.admin.ListInventory(c.Request.Context(), ClaimsFrom(c), filter)
	if err != nil {
		MapError(c, err)
		return
	}
	response.Paged(c, "inventaris berkas lokal", items, response.BuildMeta(filter.Page, filter.Limit, total))
}

// GetLayout returns the active layout document.
func (h *AdminHandler) GetLayout(c *gin.Context) {
	cfg, err := h.admin.GetLayout(c.Request.Context(), ClaimsFrom(c))
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "konfigurasi layout", cfg)
}

// PublishLayout validates and activates a new layout version.
func (h *AdminHandler) PublishLayout(c *gin.Context) {
	var req domain.LayoutPublishRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid", response.FieldError{Field: "body", Message: err.Error()})
		return
	}
	record, err := h.admin.PublishLayout(c.Request.Context(), ClaimsFrom(c), req.Config, c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "layout dipublikasikan dan layar TV diperbarui", record)
}

// ListAnnouncements returns running text messages.
func (h *AdminHandler) ListAnnouncements(c *gin.Context) {
	items, total, err := h.admin.ListAnnouncements(c.Request.Context(), ClaimsFrom(c), pagination(c))
	if err != nil {
		MapError(c, err)
		return
	}
	response.Paged(c, "daftar pengumuman", items, response.BuildMeta(queryInt(c, "page", 1), queryInt(c, "limit", 20), total))
}

// CreateAnnouncement publishes a message.
func (h *AdminHandler) CreateAnnouncement(c *gin.Context) {
	var req domain.AnnouncementUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid", response.FieldError{Field: "body", Message: err.Error()})
		return
	}
	item, err := h.admin.CreateAnnouncement(c.Request.Context(), ClaimsFrom(c), req, c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}
	response.Created(c, "pengumuman tayang di seluruh TV", item)
}

// UpdateAnnouncement rewrites a message.
func (h *AdminHandler) UpdateAnnouncement(c *gin.Context) {
	var req domain.AnnouncementUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid", response.FieldError{Field: "body", Message: err.Error()})
		return
	}
	item, err := h.admin.UpdateAnnouncement(c.Request.Context(), ClaimsFrom(c), c.Param("announcementID"), req, c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "pengumuman diperbarui", item)
}

// DeleteAnnouncement removes a message.
func (h *AdminHandler) DeleteAnnouncement(c *gin.Context) {
	if err := h.admin.DeleteAnnouncement(c.Request.Context(), ClaimsFrom(c), c.Param("announcementID"), c.ClientIP()); err != nil {
		MapError(c, err)
		return
	}
	response.NoContent(c, "pengumuman dihapus")
}

// ListDevices returns TVs within the caller scope.
func (h *AdminHandler) ListDevices(c *gin.Context) {
	filter := domain.DeviceFilter{
		Page:   queryInt(c, "page", 1),
		Limit:  queryInt(c, "limit", 20),
		Status: strings.ToUpper(c.Query("status")),
		Search: c.Query("search"),
		RoomID: c.Query("room_id"),
	}
	if tenantID := c.Query("tenant_id"); tenantID != "" {
		filter.TenantID = tenantID
	}

	items, total, err := h.admin.ListDevices(c.Request.Context(), ClaimsFrom(c), filter)
	if err != nil {
		MapError(c, err)
		return
	}
	response.Paged(c, "daftar perangkat", items, response.BuildMeta(filter.Page, filter.Limit, total))
}

// SendCommand dispatches a remote command to a TV.
func (h *AdminHandler) SendCommand(c *gin.Context) {
	var req domain.CommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid", response.FieldError{Field: "body", Message: err.Error()})
		return
	}
	command, err := h.admin.SendCommand(c.Request.Context(), ClaimsFrom(c), c.Param("deviceID"), req, c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}
	response.Created(c, "perintah dikirim ke perangkat", command)
}

// ListCommands returns command history.
func (h *AdminHandler) ListCommands(c *gin.Context) {
	items, total, err := h.admin.ListCommands(c.Request.Context(), ClaimsFrom(c), pagination(c))
	if err != nil {
		MapError(c, err)
		return
	}
	response.Paged(c, "riwayat perintah", items, response.BuildMeta(queryInt(c, "page", 1), queryInt(c, "limit", 20), total))
}

// CreateSyncJob enqueues a synchronisation task.
func (h *AdminHandler) CreateSyncJob(c *gin.Context) {
	var req domain.SyncJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload tidak valid", response.FieldError{Field: "body", Message: err.Error()})
		return
	}
	job, err := h.admin.CreateSyncJob(c.Request.Context(), ClaimsFrom(c), req, c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}
	response.Created(c, "job sinkronisasi masuk antrean", job)
}

// ListSyncJobs returns the queue.
func (h *AdminHandler) ListSyncJobs(c *gin.Context) {
	items, total, err := h.admin.ListSyncJobs(c.Request.Context(), ClaimsFrom(c), pagination(c))
	if err != nil {
		MapError(c, err)
		return
	}
	response.Paged(c, "daftar job sinkronisasi", items, response.BuildMeta(queryInt(c, "page", 1), queryInt(c, "limit", 20), total))
}

// RetrySyncJob requeues a failed job.
func (h *AdminHandler) RetrySyncJob(c *gin.Context) {
	if err := h.admin.RetrySyncJob(c.Request.Context(), ClaimsFrom(c), c.Param("jobID"), c.ClientIP()); err != nil {
		MapError(c, err)
		return
	}
	response.NoContent(c, "job dikembalikan ke antrean")
}

// TriggerScan runs the ingest engine immediately.
func (h *AdminHandler) TriggerScan(c *gin.Context) {
	run, err := h.admin.TriggerScan(c.Request.Context(), ClaimsFrom(c), "MANUAL", c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "pemindaian storage selesai", run)
}

// ScanHistory returns previous ingest runs.
func (h *AdminHandler) ScanHistory(c *gin.Context) {
	items, total, err := h.admin.ScanHistory(c.Request.Context(), ClaimsFrom(c), pagination(c))
	if err != nil {
		MapError(c, err)
		return
	}
	response.Paged(c, "riwayat pemindaian", items, response.BuildMeta(queryInt(c, "page", 1), queryInt(c, "limit", 20), total))
}

// ListNodes returns edge nodes.
func (h *AdminHandler) ListNodes(c *gin.Context) {
	items, total, err := h.admin.ListNodes(c.Request.Context(), ClaimsFrom(c), pagination(c))
	if err != nil {
		MapError(c, err)
		return
	}
	response.Paged(c, "daftar node", items, response.BuildMeta(queryInt(c, "page", 1), queryInt(c, "limit", 20), total))
}

// ListAuditLogs returns the audit trail.
func (h *AdminHandler) ListAuditLogs(c *gin.Context) {
	items, total, err := h.admin.ListAuditLogs(c.Request.Context(), ClaimsFrom(c), pagination(c))
	if err != nil {
		MapError(c, err)
		return
	}
	response.Paged(c, "audit log", items, response.BuildMeta(queryInt(c, "page", 1), queryInt(c, "limit", 20), total))
}

func pagination(c *gin.Context) domain.Pagination {
	return domain.Pagination{
		Page:  queryInt(c, "page", 1),
		Limit: queryInt(c, "limit", 20),
	}
}

func queryInt(c *gin.Context, key string, fallback int) int {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}
