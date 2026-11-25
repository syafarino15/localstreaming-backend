package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/alfateve/stayin-local-backend/internal/domain"
	"github.com/alfateve/stayin-local-backend/internal/platform/auth"
	"github.com/alfateve/stayin-local-backend/internal/platform/storage"
)

// ScanTrigger runs the ingest engine on demand.
type ScanTrigger interface {
	Run(ctx context.Context, trigger string) (*domain.MediaScanRun, error)
}

// LocalDeps groups every repository backed by stayin_local. It is nil when this
// process runs in main-only mode, where operational data lives on the ship.
type LocalDeps struct {
	Rooms         domain.RoomRepository
	Devices       domain.DeviceRepository
	Layouts       domain.LayoutRepository
	Announcements domain.AnnouncementRepository
	Commands      domain.CommandRepository
	Scans         domain.MediaScanRepository
	WatchLogs     domain.WatchLogRepository
	Movies        domain.MovieLocalRepository
	Tenants       domain.LocalTenantRepository
	Stats         domain.LocalStatsRepository
	NodeCode      string
}

// AdminDeps groups the admin usecase dependencies.
//
// Everything directly on AdminDeps is backed by stayin_main. Everything under
// Local is backed by stayin_local. The split is deliberate: the CMS authoring
// data and the ship operational data are two applications with two databases.
type AdminDeps struct {
	Tenants    domain.TenantRepository
	Users      domain.UserRepository
	Nodes      domain.NodeRepository
	Movies     domain.MovieRepository
	SyncJobs   domain.SyncJobRepository
	Audits     domain.AuditRepository
	AppConfigs domain.AppConfigRepository
	Flags      domain.FeatureFlagRepository
	Releases   domain.ReleaseRepository
	MainStats  domain.MainStatsRepository

	Local    *LocalDeps
	Notifier domain.Notifier
	Scanner  ScanTrigger
	Store    *storage.Store
	Log      zerolog.Logger
}

// Admin implements the CMS operations for both portals.
type Admin struct {
	d AdminDeps
}

// NewAdmin builds the admin usecase.
func NewAdmin(d AdminDeps) *Admin {
	return &Admin{d: d}
}

// errLocalUnavailable is returned when an operational endpoint is called from a
// main-only deployment. It is a clear 503 rather than a nil dereference.
func errLocalUnavailable(feature string) error {
	return fmt.Errorf("%s dikelola pada server lokal; jalankan APP_MODE=combined atau hubungi node kapal: %w",
		feature, domain.ErrUnavailable)
}

func (a *Admin) localReady() error {
	if a.d.Local == nil {
		return errLocalUnavailable("data operasional")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Dashboards
// ---------------------------------------------------------------------------

// Overview returns the landing dashboard for the caller's role. Counters are
// merged in Go from the two databases; there is no cross-database SQL query.
func (a *Admin) Overview(ctx context.Context, claims *domain.AuthClaims) (any, error) {
	if claims.Role == domain.RoleSuperAdmin {
		return a.adminOverview(ctx)
	}
	return a.tenantOverview(ctx, claims.TenantID)
}

func (a *Admin) adminOverview(ctx context.Context) (*domain.AdminOverview, error) {
	mainCounts, err := a.d.MainStats.MainOverviewMetrics(ctx)
	if err != nil {
		return nil, err
	}
	out := &domain.AdminOverview{
		TotalTenants:  mainCounts.TotalTenants,
		ActiveTenants: mainCounts.ActiveTenants,
		TotalNodes:    mainCounts.TotalNodes,
		OnlineNodes:   mainCounts.OnlineNodes,
		TotalMovies:   mainCounts.TotalMovies,
		PendingMovies: mainCounts.PendingMovies,
	}
	if a.d.Local == nil {
		return out, nil
	}

	localCounts, err := a.d.Local.Stats.LocalOverviewMetrics(ctx, "")
	if err != nil {
		a.d.Log.Warn().Err(err).Msg("local overview unavailable")
		return out, nil
	}
	out.TotalDevices = localCounts.TotalDevices
	out.OnlineDevices = localCounts.OnlineDevices
	out.ReadyMovies = localCounts.ReadyMovies
	out.StorageUsedBytes = localCounts.StorageUsedBytes
	out.LocalDBConnected = true

	if n, err := a.d.Local.Stats.WatchLogCount(ctx, ""); err == nil {
		out.TotalWatchLogs = n
	}
	return out, nil
}

func (a *Admin) tenantOverview(ctx context.Context, tenantID string) (*domain.TenantOverview, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("overview: tenant tidak teridentifikasi: %w", domain.ErrForbidden)
	}

	// Master catalog total comes from stayin_main.
	_, masterTotal, err := a.d.Movies.ListMovies(ctx, domain.MovieFilter{
		TenantID: tenantID,
		Page:     1,
		Limit:    1,
	})
	if err != nil {
		return nil, err
	}

	out := &domain.TenantOverview{TotalMovies: masterTotal}
	if a.d.Local == nil {
		return out, nil
	}

	localCounts, err := a.d.Local.Stats.LocalOverviewMetrics(ctx, tenantID)
	if err != nil {
		a.d.Log.Warn().Err(err).Msg("local overview unavailable")
		return out, nil
	}
	out.TotalRooms = localCounts.TotalRooms
	out.TotalDevices = localCounts.TotalDevices
	out.OnlineDevices = localCounts.OnlineDevices
	out.ReadyMovies = localCounts.ReadyMovies
	out.ActiveAnnouncements = localCounts.ActiveAnnouncements
	out.StorageUsedBytes = localCounts.StorageUsedBytes
	out.LocalDBConnected = true

	if pending, err := a.d.Local.Movies.CountMovieLocalByStatus(ctx, tenantID, domain.LocalPending); err == nil {
		out.PendingLocalMovies = pending
	}
	return out, nil
}

// Trend returns the daily watch series for the tenant.
func (a *Admin) Trend(ctx context.Context, claims *domain.AuthClaims, days int) ([]domain.TrendPoint, error) {
	if a.d.Local == nil {
		return nil, errLocalUnavailable("tren tontonan")
	}
	return a.d.Local.Stats.WatchTrend(ctx, claims.TenantID, days)
}

// TopMovies returns the most watched titles.
func (a *Admin) TopMovies(ctx context.Context, claims *domain.AuthClaims, days, limit int) ([]domain.TopMovie, error) {
	if a.d.Local == nil {
		return nil, errLocalUnavailable("statistik tontonan")
	}
	return a.d.Local.Movies.TopMovies(ctx, claims.TenantID, days, limit)
}

// ---------------------------------------------------------------------------
// Tenants (super admin only)
// ---------------------------------------------------------------------------

// ListTenants returns whitelabel partners.
func (a *Admin) ListTenants(ctx context.Context, p domain.Pagination) ([]domain.Tenant, int64, error) {
	return a.d.Tenants.ListTenants(ctx, p)
}

// CreateTenant registers a new partner.
func (a *Admin) CreateTenant(ctx context.Context, req domain.TenantUpsertRequest, actor *domain.AuthClaims, ip string) (*domain.Tenant, error) {
	if strings.TrimSpace(req.Slug) == "" {
		return nil, domain.NewValidation("slug", "slug wajib diisi")
	}
	if strings.TrimSpace(req.Name) == "" {
		return nil, domain.NewValidation("name", "nama tenant wajib diisi")
	}

	branding, err := json.Marshal(defaultMap(req.Branding))
	if err != nil {
		return nil, domain.NewValidation("branding", "format branding tidak valid")
	}

	tenant := &domain.Tenant{
		Slug:         strings.ToLower(strings.TrimSpace(req.Slug)),
		Name:         strings.TrimSpace(req.Name),
		Branding:     branding,
		LicenseQuota: req.LicenseQuota,
		Status:       "ACTIVE",
	}
	if err := a.d.Tenants.CreateTenant(ctx, tenant); err != nil {
		return nil, err
	}
	a.audit(ctx, actor, "TENANT_CREATE", "tenant", tenant.ID, ip)
	return tenant, nil
}

// UpdateTenant rewrites a partner.
func (a *Admin) UpdateTenant(ctx context.Context, id string, req domain.TenantUpsertRequest, actor *domain.AuthClaims, ip string) (*domain.Tenant, error) {
	tenant, err := a.d.Tenants.GetTenantByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Name) != "" {
		tenant.Name = strings.TrimSpace(req.Name)
	}
	if strings.TrimSpace(req.Slug) != "" {
		tenant.Slug = strings.ToLower(strings.TrimSpace(req.Slug))
	}
	if req.LicenseQuota > 0 {
		tenant.LicenseQuota = req.LicenseQuota
	}
	if req.Branding != nil {
		branding, err := json.Marshal(req.Branding)
		if err != nil {
			return nil, domain.NewValidation("branding", "format branding tidak valid")
		}
		tenant.Branding = branding
	}
	if err := a.d.Tenants.UpdateTenant(ctx, tenant); err != nil {
		return nil, err
	}

	// Keep the ship mirror in step when both databases are in this process.
	if a.d.Local != nil {
		if err := a.d.Local.Tenants.UpsertTenantMirror(ctx, tenant); err != nil {
			a.d.Log.Warn().Err(err).Msg("mirror tenant to local failed")
		}
	}

	a.audit(ctx, actor, "TENANT_UPDATE", "tenant", tenant.ID, ip)
	return tenant, nil
}

// SetTenantStatus suspends or reactivates a partner.
func (a *Admin) SetTenantStatus(ctx context.Context, id, status string, actor *domain.AuthClaims, ip string) error {
	status = strings.ToUpper(strings.TrimSpace(status))
	if status != "ACTIVE" && status != "SUSPENDED" {
		return domain.NewValidation("status", "status harus ACTIVE atau SUSPENDED")
	}
	if err := a.d.Tenants.SetTenantStatus(ctx, id, status); err != nil {
		return err
	}
	a.audit(ctx, actor, "TENANT_STATUS", "tenant", id, ip)
	return nil
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

// ListUsers returns admin accounts. Tenant admins only see their own team.
func (a *Admin) ListUsers(ctx context.Context, claims *domain.AuthClaims, p domain.Pagination) ([]domain.User, int64, error) {
	tenantID := ""
	if claims.Role == domain.RoleTenantAdmin {
		tenantID = claims.TenantID
	}
	return a.d.Users.ListUsers(ctx, tenantID, p)
}

// CreateUser adds an account.
func (a *Admin) CreateUser(ctx context.Context, req domain.UserUpsertRequest, actor *domain.AuthClaims, ip string) (*domain.User, error) {
	if req.Role != domain.RoleSuperAdmin && req.Role != domain.RoleTenantAdmin {
		return nil, domain.NewValidation("role", "role harus SUPER_ADMIN atau TENANT_ADMIN")
	}
	if req.Role == domain.RoleSuperAdmin && actor.Role != domain.RoleSuperAdmin {
		return nil, fmt.Errorf("user: hanya super admin dapat membuat super admin: %w", domain.ErrForbidden)
	}
	if req.Password == "" {
		return nil, domain.NewValidation("password", "password wajib diisi")
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}

	var tenantID *string
	if req.Role == domain.RoleTenantAdmin {
		resolved := strings.TrimSpace(req.TenantID)
		if resolved == "" {
			resolved = actor.TenantID
		}
		if resolved == "" {
			return nil, domain.NewValidation("tenant_id", "tenant_id wajib untuk TENANT_ADMIN")
		}
		tenantID = &resolved
	}

	user := &domain.User{
		TenantID:     tenantID,
		Email:        strings.ToLower(strings.TrimSpace(req.Email)),
		PasswordHash: hash,
		FullName:     strings.TrimSpace(req.FullName),
		Role:         req.Role,
		Status:       "ACTIVE",
	}
	if req.Status != "" {
		user.Status = req.Status
	}
	if err := a.d.Users.CreateUser(ctx, user); err != nil {
		return nil, err
	}
	a.audit(ctx, actor, "USER_CREATE", "user", user.ID, ip)
	return user, nil
}

// UpdateUser rewrites an account.
func (a *Admin) UpdateUser(ctx context.Context, id string, req domain.UserUpsertRequest, actor *domain.AuthClaims, ip string) (*domain.User, error) {
	user, err := a.d.Users.GetUserByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if actor.Role == domain.RoleTenantAdmin && (user.TenantID == nil || *user.TenantID != actor.TenantID) {
		return nil, fmt.Errorf("user: di luar cakupan tenant: %w", domain.ErrForbidden)
	}
	if req.FullName != "" {
		user.FullName = strings.TrimSpace(req.FullName)
	}
	if req.Email != "" {
		user.Email = strings.ToLower(strings.TrimSpace(req.Email))
	}
	if req.Status != "" {
		user.Status = req.Status
	}
	if err := a.d.Users.UpdateUser(ctx, user); err != nil {
		return nil, err
	}
	if req.Password != "" {
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			return nil, err
		}
		if err := a.d.Users.UpdatePassword(ctx, id, hash); err != nil {
			return nil, err
		}
	}
	a.audit(ctx, actor, "USER_UPDATE", "user", id, ip)
	return user, nil
}

// ---------------------------------------------------------------------------
// Rooms and VIP guests (local database)
// ---------------------------------------------------------------------------

// ListRooms returns cabins for the caller's tenant.
func (a *Admin) ListRooms(ctx context.Context, claims *domain.AuthClaims, p domain.Pagination) ([]domain.Room, int64, error) {
	if err := a.localReady(); err != nil {
		return nil, 0, err
	}
	return a.d.Local.Rooms.ListRooms(ctx, claims.TenantID, p)
}

// CreateRoom adds a cabin.
func (a *Admin) CreateRoom(ctx context.Context, claims *domain.AuthClaims, req domain.RoomUpsertRequest, ip string) (*domain.Room, error) {
	if err := a.localReady(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.RoomNumber) == "" {
		return nil, domain.NewValidation("room_number", "nomor kamar wajib diisi")
	}
	room := &domain.Room{
		TenantID:    claims.TenantID,
		RoomNumber:  strings.TrimSpace(req.RoomNumber),
		Deck:        strings.TrimSpace(req.Deck),
		RoomType:    normaliseRoomType(req.RoomType),
		IsExclusive: req.IsExclusive,
		Status:      "ACTIVE",
	}
	if err := a.d.Local.Rooms.CreateRoom(ctx, room); err != nil {
		return nil, err
	}
	a.audit(ctx, claims, "ROOM_CREATE", "room", room.ID, ip)
	return room, nil
}

// UpdateRoom rewrites a cabin.
func (a *Admin) UpdateRoom(ctx context.Context, claims *domain.AuthClaims, id string, req domain.RoomUpsertRequest, ip string) (*domain.Room, error) {
	if err := a.localReady(); err != nil {
		return nil, err
	}
	room, err := a.d.Local.Rooms.GetRoomByID(ctx, claims.TenantID, id)
	if err != nil {
		return nil, err
	}
	if req.RoomNumber != "" {
		room.RoomNumber = strings.TrimSpace(req.RoomNumber)
	}
	room.Deck = strings.TrimSpace(req.Deck)
	room.RoomType = normaliseRoomType(req.RoomType)
	room.IsExclusive = req.IsExclusive
	if err := a.d.Local.Rooms.UpdateRoom(ctx, room); err != nil {
		return nil, err
	}
	a.audit(ctx, claims, "ROOM_UPDATE", "room", id, ip)
	return room, nil
}

// CheckinGuest sets the personalised greeting for a VIP cabin and pushes it to
// every TV in that cabin immediately.
func (a *Admin) CheckinGuest(ctx context.Context, claims *domain.AuthClaims, roomID string, req domain.GuestCheckinRequest, ip string) (*domain.Room, error) {
	if err := a.localReady(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GuestName) == "" {
		return nil, domain.NewValidation("guest_name", "nama tamu wajib diisi")
	}

	room, err := a.d.Local.Rooms.GetRoomByID(ctx, claims.TenantID, roomID)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	checkin := now
	if req.CheckinAt != nil {
		checkin = *req.CheckinAt
	}

	room.GuestName = strings.TrimSpace(req.GuestName)
	room.GuestSalutation = strings.TrimSpace(req.GuestSalutation)
	room.GuestCheckinAt = &checkin
	room.GuestCheckoutAt = req.CheckoutAt

	if err := a.d.Local.Rooms.SetGuest(ctx, room); err != nil {
		return nil, err
	}

	layout, layoutErr := a.d.Local.Layouts.GetActive(ctx, claims.TenantID)
	greeting := ""
	if layoutErr == nil {
		greeting = substituteGreeting(layout.Config.WelcomeScreen.DefaultGreeting, room)
	}

	delivered := a.d.Notifier.SendToRoom(room.ID, "ROOM_GUEST_UPDATED", map[string]any{
		"room_id":          room.ID,
		"room_number":      room.RoomNumber,
		"guest_name":       room.GuestName,
		"guest_salutation": room.GuestSalutation,
		"is_exclusive":     room.IsExclusive,
		"greeting":         greeting,
	})

	a.audit(ctx, claims, "GUEST_CHECKIN", "room", room.ID, ip, map[string]any{
		"guest_name": room.GuestName,
		"delivered":  delivered,
	})

	return room, nil
}

// CheckoutGuest clears the personalised greeting.
func (a *Admin) CheckoutGuest(ctx context.Context, claims *domain.AuthClaims, roomID, ip string) error {
	if err := a.localReady(); err != nil {
		return err
	}
	room, err := a.d.Local.Rooms.GetRoomByID(ctx, claims.TenantID, roomID)
	if err != nil {
		return err
	}
	if err := a.d.Local.Rooms.ClearGuest(ctx, claims.TenantID, roomID); err != nil {
		return err
	}
	a.d.Notifier.SendToRoom(room.ID, "ROOM_GUEST_UPDATED", map[string]any{
		"room_id":     room.ID,
		"room_number": room.RoomNumber,
		"guest_name":  "",
		"greeting":    substituteGreeting("Selamat berlayar. Nikmati hiburan di kabin {room_number}.", room),
	})
	a.audit(ctx, claims, "GUEST_CHECKOUT", "room", roomID, ip)
	return nil
}

// ---------------------------------------------------------------------------
// Movies — master catalog (main) merged with local file state (local)
// ---------------------------------------------------------------------------

// ListMovies returns the tenant catalog together with the ship's file state.
func (a *Admin) ListMovies(ctx context.Context, claims *domain.AuthClaims, f domain.MovieFilter) ([]domain.MovieWithLocal, int64, error) {
	// A tenant admin is always pinned to its own tenant. A super admin may
	// filter by any tenant, or leave it empty to read the whole library.
	if claims.TenantID != "" {
		f.TenantID = claims.TenantID
	}
	movies, total, err := a.d.Movies.ListMovies(ctx, f)
	if err != nil {
		return nil, 0, err
	}

	out := make([]domain.MovieWithLocal, 0, len(movies))
	for i := range movies {
		item := domain.MovieWithLocal{Movie: movies[i]}
		if a.d.Local != nil && movies[i].TenantID != "" {
			if local, localErr := a.d.Local.Movies.GetMovieLocalByMovieID(ctx, movies[i].TenantID, movies[i].ID); localErr == nil {
				item.Local = local
			} else if !errors.Is(localErr, domain.ErrNotFound) {
				a.d.Log.Warn().Err(localErr).Str("movie", movies[i].ID).Msg("read local state failed")
			}
		}
		out = append(out, item)
	}
	return out, total, nil
}

// CreateMovie registers a master catalog entry. The movie starts as PENDING and
// waits for either a network transfer or a flashdisk delivery. No video file is
// uploaded here.
func (a *Admin) CreateMovie(ctx context.Context, claims *domain.AuthClaims, req domain.MovieCreateRequest, ip string) (*domain.MovieWithLocal, error) {
	if strings.TrimSpace(req.Title) == "" {
		return nil, domain.NewValidation("title", "judul film wajib diisi")
	}

	// A tenant admin writes to its own tenant no matter what the body says; a
	// super admin must name the target tenant explicitly.
	tenantID := claims.TenantID
	if claims.Role == domain.RoleSuperAdmin {
		tenantID = strings.TrimSpace(req.TenantID)
		if tenantID == "" {
			return nil, domain.NewValidation("tenant_id", "tenant_id wajib untuk super admin")
		}
		if _, err := a.d.Tenants.GetTenantByID(ctx, tenantID); err != nil {
			return nil, domain.NewValidation("tenant_id", "tenant tidak ditemukan")
		}
	}
	if tenantID == "" {
		return nil, fmt.Errorf("movie: tenant tidak teridentifikasi: %w", domain.ErrForbidden)
	}

	slug := normaliseSlug(req.Slug)
	if slug == "" {
		slug = normaliseSlug(req.Title)
	}

	status := domain.MovieDraft
	if req.Publish {
		status = domain.MoviePending
	}

	movie := &domain.Movie{
		TenantID:        tenantID,
		Slug:            slug,
		Title:           strings.TrimSpace(req.Title),
		Year:            req.Year,
		DurationSeconds: req.DurationSeconds,
		Genres:          nonEmptyStrings(req.Genres),
		PosterPath:      "",
		BannerPath:      "",
		IsFeatured:      req.IsFeatured,
		SortOrder:       req.SortOrder,
		Status:          status,
	}
	detail := &domain.MovieDetail{
		Synopsis:         req.Synopsis,
		Tagline:          req.Tagline,
		Director:         req.Director,
		Cast:             nonEmptyStrings(req.Cast),
		Country:          req.Country,
		OriginalLanguage: req.OriginalLanguage,
		AgeRating:        req.AgeRating,
		Quality:          req.Quality,
		AudioTracks:      req.AudioTracks,
		Subtitles:        req.Subtitles,
		MetadataSource:   "MANUAL",
		Raw:              json.RawMessage(`{}`),
	}

	if err := a.d.Movies.CreateMovie(ctx, movie, detail); err != nil {
		return nil, err
	}

	item := &domain.MovieWithLocal{Movie: *movie}

	// Combined mode: create the placeholder on the ship database so the
	// operator immediately sees the pending item, and mirror the metadata so
	// the ship can render it the moment the file arrives.
	if a.d.Local != nil && req.Publish {
		if _, err := a.d.Local.Movies.UpsertCatalogMovie(ctx, movie, detail, domain.SourceCloud); err != nil {
			a.d.Log.Warn().Err(err).Str("movie", movie.ID).Msg("mirror catalog to local failed")
		}
		local := &domain.MovieLocal{
			TenantID:         movie.TenantID,
			MovieID:          movie.ID,
			NodeCode:         a.localNodeCode(),
			Status:           domain.LocalPending,
			Source:           domain.SourceCloud,
			ExpectedFilename: strings.TrimSpace(req.ExpectedFilename),
		}
		if _, err := a.d.Local.Movies.UpsertMovieLocal(ctx, local); err != nil {
			a.d.Log.Warn().Err(err).Str("movie", movie.ID).Msg("create local placeholder failed")
		} else {
			item.Local = local
		}
	}

	// Always enqueue distribution so a separated main server has work to do.
	if req.Publish {
		payload, _ := json.Marshal(map[string]any{
			"movie_id":          movie.ID,
			"expected_filename": strings.TrimSpace(req.ExpectedFilename),
			"title":             movie.Title,
		})
		job := &domain.SyncJob{TenantID: movie.TenantID, Type: "DISTRIBUTE_MOVIE", Payload: payload}
		if err := a.d.SyncJobs.CreateSyncJob(ctx, job); err != nil {
			a.d.Log.Warn().Err(err).Str("movie", movie.ID).Msg("enqueue distribution failed")
		}
	}

	a.audit(ctx, claims, "MOVIE_CREATE", "movie", movie.ID, ip, map[string]any{
		"status":            movie.Status,
		"expected_filename": req.ExpectedFilename,
	})
	return item, nil
}

// GetMovie returns a master catalog entry merged with its local state.
func (a *Admin) GetMovie(ctx context.Context, claims *domain.AuthClaims, id string) (*domain.MovieWithLocal, *domain.MovieDetail, error) {
	movie, detail, err := a.movieForClaims(ctx, claims, id)
	if err != nil {
		return nil, nil, err
	}
	item := &domain.MovieWithLocal{Movie: *movie}
	if a.d.Local != nil {
		if local, localErr := a.d.Local.Movies.GetMovieLocalByMovieID(ctx, movie.TenantID, movie.ID); localErr == nil {
			item.Local = local
		}
	}
	return item, detail, nil
}

// UpdateMovie writes metadata edited in the CMS and marks it as MANUAL so a
// later sync does not overwrite the operator's work.
func (a *Admin) UpdateMovie(ctx context.Context, claims *domain.AuthClaims, id string, req domain.MovieUpsertRequest, ip string) (*domain.Movie, error) {
	movie, detail, err := a.movieForClaims(ctx, claims, id)
	if err != nil {
		return nil, err
	}

	if req.Title != "" {
		movie.Title = strings.TrimSpace(req.Title)
	}
	if req.Slug != "" {
		movie.Slug = normaliseSlug(req.Slug)
	}
	if req.Year > 0 {
		movie.Year = req.Year
	}
	if req.DurationSeconds > 0 {
		movie.DurationSeconds = req.DurationSeconds
	}
	if len(req.Genres) > 0 {
		movie.Genres = req.Genres
	}
	if req.IsFeatured {
		movie.IsFeatured = true
	} else if req.Status == "" {
		movie.IsFeatured = false
	}
	movie.SortOrder = req.SortOrder
	if req.Status != "" {
		switch req.Status {
		case domain.MovieDraft, domain.MoviePending, domain.MoviePublished, domain.MovieArchived:
			movie.Status = req.Status
		default:
			return nil, domain.NewValidation("status", "status harus DRAFT, PENDING, PUBLISHED, atau ARCHIVED")
		}
	}

	if req.Synopsis != "" {
		detail.Synopsis = req.Synopsis
	}
	if req.Tagline != "" {
		detail.Tagline = req.Tagline
	}
	if req.Director != "" {
		detail.Director = req.Director
	}
	if len(req.Cast) > 0 {
		detail.Cast = req.Cast
	}
	if req.Country != "" {
		detail.Country = req.Country
	}
	if req.OriginalLanguage != "" {
		detail.OriginalLanguage = req.OriginalLanguage
	}
	if req.AgeRating != "" {
		detail.AgeRating = req.AgeRating
	}
	if req.Quality != "" {
		detail.Quality = req.Quality
	}
	if len(req.AudioTracks) > 0 {
		detail.AudioTracks = req.AudioTracks
	}
	if len(req.Subtitles) > 0 {
		detail.Subtitles = req.Subtitles
	}
	if detail.MovieID == "" {
		detail.MovieID = movie.ID
	}
	detail.MetadataSource = "MANUAL"

	if err := a.d.Movies.UpdateMovie(ctx, movie, detail); err != nil {
		return nil, err
	}

	if a.d.Local != nil {
		if _, err := a.d.Local.Movies.UpsertCatalogMovie(ctx, movie, detail, domain.SourceCloud); err != nil {
			a.d.Log.Warn().Err(err).Str("movie", movie.ID).Msg("mirror catalog update to local failed")
		}
	}

	a.audit(ctx, claims, "MOVIE_UPDATE", "movie", movie.ID, ip)
	return movie, nil
}

// SetMovieArtwork stores the relative path of an uploaded poster or banner.
func (a *Admin) SetMovieArtwork(ctx context.Context, claims *domain.AuthClaims, id, kind, relPath, ip string) (*domain.Movie, error) {
	movie, detail, err := a.movieForClaims(ctx, claims, id)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "poster":
		movie.PosterPath = relPath
	case "banner":
		movie.BannerPath = relPath
	default:
		return nil, domain.NewValidation("kind", "jenis aset harus poster atau banner")
	}
	if err := a.d.Movies.UpdateMovie(ctx, movie, detail); err != nil {
		return nil, err
	}
	if a.d.Local != nil {
		if _, err := a.d.Local.Movies.UpsertCatalogMovie(ctx, movie, detail, domain.SourceCloud); err != nil {
			a.d.Log.Warn().Err(err).Str("movie", movie.ID).Msg("mirror artwork to local failed")
		}
	}
	a.audit(ctx, claims, "MOVIE_ARTWORK", "movie", id, ip, map[string]any{"kind": kind, "path": relPath})
	return movie, nil
}

// DeleteMovie removes a master catalog row. The physical file is never deleted
// from storage, so an accidental delete is recoverable by rescanning.
func (a *Admin) DeleteMovie(ctx context.Context, claims *domain.AuthClaims, id, ip string) error {
	movie, _, err := a.movieForClaims(ctx, claims, id)
	if err != nil {
		return err
	}
	if err := a.d.Movies.DeleteMovie(ctx, movie.TenantID, id); err != nil {
		return err
	}
	if a.d.Local != nil {
		if err := a.d.Local.Movies.DeleteMovieLocal(ctx, movie.TenantID, id); err != nil && !errors.Is(err, domain.ErrNotFound) {
			a.d.Log.Warn().Err(err).Str("movie", id).Msg("delete local file row failed")
		}
	}
	a.audit(ctx, claims, "MOVIE_DELETE", "movie", id, ip)
	return nil
}

// movieForClaims resolves a movie inside the caller's scope. A tenant admin is
// pinned to its own tenant (anti-IDOR); a super admin may read any tenant.
func (a *Admin) movieForClaims(ctx context.Context, claims *domain.AuthClaims, id string) (*domain.Movie, *domain.MovieDetail, error) {
	if claims.TenantID == "" {
		return a.d.Movies.GetMovieByIDGlobal(ctx, id)
	}
	return a.d.Movies.GetMovieByID(ctx, claims.TenantID, id)
}

// ListInventory returns the raw mst_movie_local rows of the ship.
func (a *Admin) ListInventory(ctx context.Context, claims *domain.AuthClaims, f domain.MovieLocalFilter) ([]domain.MovieLocal, int64, error) {
	if err := a.localReady(); err != nil {
		return nil, 0, err
	}
	if claims.TenantID != "" {
		f.TenantID = claims.TenantID
	}
	return a.d.Local.Movies.ListMovieLocals(ctx, f)
}

// ---------------------------------------------------------------------------
// Layout (local database, welcome screen only)
// ---------------------------------------------------------------------------

// GetLayout returns the active welcome layout document.
func (a *Admin) GetLayout(ctx context.Context, claims *domain.AuthClaims) (*domain.LayoutConfig, error) {
	if err := a.localReady(); err != nil {
		return nil, err
	}
	record, err := a.d.Local.Layouts.GetActive(ctx, claims.TenantID)
	if err != nil {
		return nil, err
	}
	return &record.Config, nil
}

// PublishLayout validates and activates a new welcome layout version, then
// pushes a refresh to every TV of the tenant.
func (a *Admin) PublishLayout(ctx context.Context, claims *domain.AuthClaims, cfg domain.LayoutConfig, ip string) (*domain.LayoutConfigRecord, error) {
	if err := a.localReady(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.AppTitle) == "" {
		return nil, domain.NewValidation("app_title", "judul aplikasi wajib diisi")
	}
	switch strings.ToUpper(cfg.WelcomeScreen.LayoutVariant) {
	case "SPLIT_LEFT_CARD", "FULLSCREEN_CENTER", "BOTTOM_OVERLAY", "":
	default:
		return nil, domain.NewValidation("layout_variant", "varian harus SPLIT_LEFT_CARD, FULLSCREEN_CENTER, atau BOTTOM_OVERLAY")
	}
	if cfg.WelcomeScreen.PrimaryColor == "" {
		cfg.WelcomeScreen.PrimaryColor = "#0F4C81"
	}
	if cfg.WelcomeScreen.AccentColor == "" {
		cfg.WelcomeScreen.AccentColor = "#F59E0B"
	}
	if cfg.WelcomeScreen.LayoutVariant == "" {
		cfg.WelcomeScreen.LayoutVariant = "SPLIT_LEFT_CARD"
	}
	if cfg.WelcomeScreen.OverscanPercent <= 0 {
		cfg.WelcomeScreen.OverscanPercent = 4
	}

	record, err := a.d.Local.Layouts.Publish(ctx, claims.TenantID, cfg, claims.UserID)
	if err != nil {
		return nil, err
	}

	delivered := a.d.Notifier.BroadcastTenant(claims.TenantID, "REFRESH_CONFIG", map[string]any{
		"reason":  "LAYOUT_PUBLISHED",
		"version": record.Version,
	})

	a.audit(ctx, claims, "LAYOUT_PUBLISH", "layout", record.ID, ip, map[string]any{
		"version":   record.Version,
		"delivered": delivered,
	})
	return record, nil
}

// ---------------------------------------------------------------------------
// Announcements (local database)
// ---------------------------------------------------------------------------

// ListAnnouncements returns every announcement of the tenant.
func (a *Admin) ListAnnouncements(ctx context.Context, claims *domain.AuthClaims, p domain.Pagination) ([]domain.Announcement, int64, error) {
	if err := a.localReady(); err != nil {
		return nil, 0, err
	}
	return a.d.Local.Announcements.ListAnnouncements(ctx, claims.TenantID, p)
}

// CreateAnnouncement publishes a running text message and pushes it live.
func (a *Admin) CreateAnnouncement(ctx context.Context, claims *domain.AuthClaims, req domain.AnnouncementUpsertRequest, ip string) (*domain.Announcement, error) {
	if err := a.localReady(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Message) == "" {
		return nil, domain.NewValidation("message", "pesan wajib diisi")
	}
	severity := strings.ToUpper(strings.TrimSpace(req.Severity))
	switch severity {
	case "", "INFO", "WARNING", "EMERGENCY":
	default:
		return nil, domain.NewValidation("severity", "severity harus INFO, WARNING, atau EMERGENCY")
	}
	if severity == "" {
		severity = "INFO"
	}
	active := true
	if req.IsActive != nil {
		active = *req.IsActive
	}

	announcement := &domain.Announcement{
		TenantID:   claims.TenantID,
		Message:    strings.TrimSpace(req.Message),
		Severity:   severity,
		ActiveFrom: req.ActiveFrom,
		ActiveTo:   req.ActiveTo,
		IsActive:   active,
	}
	if err := a.d.Local.Announcements.CreateAnnouncement(ctx, announcement); err != nil {
		return nil, err
	}

	a.d.Notifier.BroadcastTenant(claims.TenantID, "ANNOUNCEMENT", map[string]any{
		"id":       announcement.ID,
		"message":  announcement.Message,
		"severity": announcement.Severity,
	})
	a.audit(ctx, claims, "ANNOUNCEMENT_CREATE", "announcement", announcement.ID, ip)
	return announcement, nil
}

// UpdateAnnouncement rewrites a message.
func (a *Admin) UpdateAnnouncement(ctx context.Context, claims *domain.AuthClaims, id string, req domain.AnnouncementUpsertRequest, ip string) (*domain.Announcement, error) {
	if err := a.localReady(); err != nil {
		return nil, err
	}
	list, _, err := a.d.Local.Announcements.ListAnnouncements(ctx, claims.TenantID, domain.Pagination{Page: 1, Limit: 200})
	if err != nil {
		return nil, err
	}
	var target *domain.Announcement
	for i := range list {
		if list[i].ID == id {
			target = &list[i]
			break
		}
	}
	if target == nil {
		return nil, domain.WrapNotFound("announcement")
	}
	if req.Message != "" {
		target.Message = strings.TrimSpace(req.Message)
	}
	if req.Severity != "" {
		target.Severity = strings.ToUpper(strings.TrimSpace(req.Severity))
	}
	target.ActiveFrom = req.ActiveFrom
	target.ActiveTo = req.ActiveTo
	if req.IsActive != nil {
		target.IsActive = *req.IsActive
	}
	if err := a.d.Local.Announcements.UpdateAnnouncement(ctx, target); err != nil {
		return nil, err
	}
	a.d.Notifier.BroadcastTenant(claims.TenantID, "ANNOUNCEMENT", map[string]any{
		"id":       target.ID,
		"message":  target.Message,
		"severity": target.Severity,
	})
	a.audit(ctx, claims, "ANNOUNCEMENT_UPDATE", "announcement", id, ip)
	return target, nil
}

// DeleteAnnouncement removes a message and refreshes the ticker.
func (a *Admin) DeleteAnnouncement(ctx context.Context, claims *domain.AuthClaims, id, ip string) error {
	if err := a.localReady(); err != nil {
		return err
	}
	if err := a.d.Local.Announcements.DeleteAnnouncement(ctx, claims.TenantID, id); err != nil {
		return err
	}
	a.d.Notifier.BroadcastTenant(claims.TenantID, "ANNOUNCEMENT", map[string]any{"id": id, "deleted": true})
	a.audit(ctx, claims, "ANNOUNCEMENT_DELETE", "announcement", id, ip)
	return nil
}

// ---------------------------------------------------------------------------
// Devices and remote commands (local database)
// ---------------------------------------------------------------------------

// ListDevices returns devices within the caller's scope.
func (a *Admin) ListDevices(ctx context.Context, claims *domain.AuthClaims, f domain.DeviceFilter) ([]domain.Device, int64, error) {
	if err := a.localReady(); err != nil {
		return nil, 0, err
	}
	if claims.Role == domain.RoleTenantAdmin {
		f.TenantID = claims.TenantID
	}
	return a.d.Local.Devices.ListDevices(ctx, f)
}

// SendCommand enqueues a command and delivers it immediately when the TV is
// connected. Commands stay queued while the TV is offline.
func (a *Admin) SendCommand(ctx context.Context, claims *domain.AuthClaims, deviceID string, req domain.CommandRequest, ip string) (*domain.RemoteCommand, error) {
	command := strings.ToUpper(strings.TrimSpace(req.Command))
	switch command {
	case "REFRESH_CONFIG", "ANNOUNCEMENT", "RESTART_APP", "SYNC_NOW", "RELOAD_CATALOG":
	default:
		return nil, domain.NewValidation("command", "perintah harus REFRESH_CONFIG, ANNOUNCEMENT, RESTART_APP, SYNC_NOW, atau RELOAD_CATALOG")
	}

	if a.d.Local == nil {
		// Main-only deployment: queue a distribution job for the node instead
		// of touching a database that is not here.
		payload, err := json.Marshal(map[string]any{
			"device_id": deviceID,
			"command":   command,
			"data":      defaultMap(req.Payload),
		})
		if err != nil {
			return nil, domain.NewValidation("payload", "payload tidak valid")
		}
		job := &domain.SyncJob{TenantID: claims.TenantID, Type: "DEVICE_COMMAND", Payload: payload}
		if err := a.d.SyncJobs.CreateSyncJob(ctx, job); err != nil {
			return nil, err
		}
		a.audit(ctx, claims, "COMMAND_SEND", "device", deviceID, ip, map[string]any{"command": command, "queued": true})
		return &domain.RemoteCommand{
			TenantID: claims.TenantID,
			DeviceID: deviceID,
			Command:  command,
			Status:   domain.CommandQueued,
		}, nil
	}

	device, err := a.d.Local.Devices.GetDeviceByID(ctx, claims.TenantID, deviceID)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(defaultMap(req.Payload))
	if err != nil {
		return nil, domain.NewValidation("payload", "payload tidak valid")
	}

	record := &domain.RemoteCommand{
		TenantID: claims.TenantID,
		DeviceID: device.ID,
		Command:  command,
		Payload:  payload,
		IssuedBy: &claims.UserID,
	}
	if err := a.d.Local.Commands.CreateCommand(ctx, record); err != nil {
		return nil, err
	}

	delivered := a.d.Notifier.SendToDevice(device.ID, "COMMAND", map[string]any{
		"id":      record.ID,
		"command": record.Command,
		"data":    defaultMap(req.Payload),
	})
	if delivered {
		_ = a.d.Local.Commands.MarkSent(ctx, record.ID)
		record.Status = domain.CommandSent
	}

	a.audit(ctx, claims, "COMMAND_SEND", "device", device.ID, ip, map[string]any{
		"command":   command,
		"delivered": delivered,
	})
	return record, nil
}

// ListCommands returns command history.
func (a *Admin) ListCommands(ctx context.Context, claims *domain.AuthClaims, p domain.Pagination) ([]domain.RemoteCommand, int64, error) {
	if err := a.localReady(); err != nil {
		return nil, 0, err
	}
	tenantID := claims.TenantID
	if claims.Role == domain.RoleSuperAdmin {
		tenantID = ""
	}
	return a.d.Local.Commands.ListCommands(ctx, tenantID, p)
}

// ---------------------------------------------------------------------------
// Sync jobs and ingest
// ---------------------------------------------------------------------------

// CreateSyncJob enqueues a task for the sync worker.
func (a *Admin) CreateSyncJob(ctx context.Context, claims *domain.AuthClaims, req domain.SyncJobRequest, ip string) (*domain.SyncJob, error) {
	jobType := strings.ToUpper(strings.TrimSpace(req.Type))
	switch jobType {
	case "MEDIA_RESCAN", "LAYOUT_PUSH", "CLOUD_PULL", "APK_DISTRIBUTE", "DEVICE_COMMAND":
	default:
		return nil, domain.NewValidation("type", "tipe job harus MEDIA_RESCAN, LAYOUT_PUSH, CLOUD_PULL, APK_DISTRIBUTE, atau DEVICE_COMMAND")
	}

	payload, err := json.Marshal(defaultMap(req.Payload))
	if err != nil {
		return nil, domain.NewValidation("payload", "payload tidak valid")
	}

	job := &domain.SyncJob{TenantID: claims.TenantID, Type: jobType, Payload: payload}
	if req.NodeID != "" {
		job.NodeID = &req.NodeID
	}
	if err := a.d.SyncJobs.CreateSyncJob(ctx, job); err != nil {
		return nil, err
	}
	a.audit(ctx, claims, "SYNC_ENQUEUE", "sync_job", job.ID, ip, map[string]any{"type": jobType})
	return job, nil
}

// ListSyncJobs returns the queue.
func (a *Admin) ListSyncJobs(ctx context.Context, claims *domain.AuthClaims, p domain.Pagination) ([]domain.SyncJob, int64, error) {
	tenantID := claims.TenantID
	if claims.Role == domain.RoleSuperAdmin {
		tenantID = ""
	}
	return a.d.SyncJobs.ListSyncJobs(ctx, tenantID, p)
}

// RetrySyncJob requeues a failed job.
func (a *Admin) RetrySyncJob(ctx context.Context, claims *domain.AuthClaims, id, ip string) error {
	if err := a.d.SyncJobs.RequeueSyncJob(ctx, claims.TenantID, id); err != nil {
		return err
	}
	a.audit(ctx, claims, "SYNC_RETRY", "sync_job", id, ip)
	return nil
}

// TriggerScan runs the ingest engine immediately.
func (a *Admin) TriggerScan(ctx context.Context, claims *domain.AuthClaims, trigger, ip string) (*domain.MediaScanRun, error) {
	if a.d.Scanner == nil {
		return nil, fmt.Errorf("scan: engine tidak aktif: %w", domain.ErrUnavailable)
	}
	a.audit(ctx, claims, "MEDIA_SCAN", "scan", "", ip, map[string]any{"trigger": trigger})
	return a.d.Scanner.Run(ctx, strings.ToUpper(trigger))
}

// ScanHistory returns previous ingest runs.
func (a *Admin) ScanHistory(ctx context.Context, claims *domain.AuthClaims, p domain.Pagination) ([]domain.MediaScanRun, int64, error) {
	if err := a.localReady(); err != nil {
		return nil, 0, err
	}
	return a.d.Local.Scans.ListScans(ctx, claims.TenantID, p)
}

// ---------------------------------------------------------------------------
// Nodes, inventory and audit
// ---------------------------------------------------------------------------

// ListNodes returns edge nodes.
func (a *Admin) ListNodes(ctx context.Context, claims *domain.AuthClaims, p domain.Pagination) ([]domain.EdgeNode, int64, error) {
	tenantID := claims.TenantID
	return a.d.Nodes.ListNodes(ctx, tenantID, p)
}

// RecordInventory stores a node inventory report so the CMS can show ship
// content state while the node is offline. Every movie the node reports as
// READY is also promoted to PUBLISHED in the master catalog: that is how an
// upload delivered by flashdisk becomes visible as available in the CMS.
func (a *Admin) RecordInventory(ctx context.Context, tenantID, nodeCode string, entries []domain.InventoryEntry) error {
	if tenantID == "" || nodeCode == "" {
		return domain.NewValidation("node", "tenant_id dan node_code wajib diisi")
	}
	payload, err := json.Marshal(map[string]any{
		"node_code":   nodeCode,
		"reported_at": time.Now().UTC(),
		"items":       entries,
	})
	if err != nil {
		return domain.NewValidation("inventory", "payload inventory tidak valid")
	}
	if err := a.d.Nodes.UpsertInventory(ctx, tenantID, nodeCode, payload); err != nil {
		return err
	}

	readyIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Status == string(domain.LocalReady) && entry.MovieID != "" {
			readyIDs = append(readyIDs, entry.MovieID)
		}
	}
	if len(readyIDs) > 0 {
		if _, err := a.d.Movies.MarkPublished(ctx, tenantID, readyIDs); err != nil {
			a.d.Log.Warn().Err(err).Msg("promote published movies failed")
		}
	}
	return nil
}

// ListAuditLogs returns the audit trail.
func (a *Admin) ListAuditLogs(ctx context.Context, claims *domain.AuthClaims, p domain.Pagination) ([]domain.AuditLog, int64, error) {
	tenantID := claims.TenantID
	if claims.Role == domain.RoleSuperAdmin {
		tenantID = ""
	}
	return a.d.Audits.ListAuditLogs(ctx, tenantID, p)
}

func (a *Admin) localNodeCode() string {
	if a.d.Local == nil {
		return ""
	}
	return a.d.Local.NodeCode
}

func (a *Admin) audit(ctx context.Context, claims *domain.AuthClaims, action, entity, entityID, ip string, extra ...map[string]any) {
	if claims == nil {
		return
	}
	meta := map[string]any{}
	if len(extra) > 0 && extra[0] != nil {
		meta = extra[0]
	}
	payload, err := json.Marshal(meta)
	if err != nil {
		payload = []byte("{}")
	}

	entry := &domain.AuditLog{
		ActorID:   &claims.UserID,
		ActorRole: string(claims.Role),
		Action:    action,
		Entity:    entity,
		EntityID:  entityID,
		IP:        ip,
		Metadata:  payload,
	}
	if claims.TenantID != "" {
		tenantID := claims.TenantID
		entry.TenantID = &tenantID
	}
	if err := a.d.Audits.CreateAuditLog(ctx, entry); err != nil {
		a.d.Log.Warn().Err(err).Msg("write audit log failed")
	}
}

func normaliseRoomType(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "EXCLUSIVE":
		return "EXCLUSIVE"
	case "VIP":
		return "VIP"
	default:
		return "STANDARD"
	}
}

// normaliseSlug produces a url safe identifier from a title.
func normaliseSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == ' ' || r == '-' || r == '_' || r == '/' || r == '.':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func nonEmptyStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func defaultMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	return in
}

var _ = errors.Is
