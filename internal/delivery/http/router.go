package http

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"github.com/alfateve/stayin-local-backend/internal/delivery/ws"
	"github.com/alfateve/stayin-local-backend/internal/domain"
)

// Router assembles the HTTP surface.
//
// ServesMain and ServesLocal gate the two route families so a separated
// deployment exposes only the application it actually runs.
type Router struct {
	Mode        string
	AppMode     string
	ServesMain  bool
	ServesLocal bool

	Log         zerolog.Logger
	Pingers     []Pinger
	Verifier    Verifier
	Devices     domain.DeviceRepository
	Auth        *AuthHandler
	TV          *TVHandler
	Admin       *AdminHandler
	Node        *NodeHandler
	WS          *ws.Handler
	StartedAt   time.Time
	CORSOrigins []string
}

// Build returns the configured Gin engine.
func (r *Router) Build() *gin.Engine {
	if r.Mode == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	engine := gin.New()
	engine.Use(RequestID(), Logger(r.Log), Recovery(r.Log), CORS(r.CORSOrigins))
	engine.MaxMultipartMemory = 16 << 20

	engine.GET("/healthz", Health(r.Pingers, r.StartedAt, r.AppMode))
	engine.GET("/swagger/index.html", SwaggerIndex)
	engine.GET("/swagger/openapi.yaml", SwaggerSpec)
	engine.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "endpoint tidak ditemukan"})
	})

	v1 := engine.Group("/api/v1")
	v1.GET("/health", Health(r.Pingers, r.StartedAt, r.AppMode))

	// --- Auth -------------------------------------------------------------
	if r.ServesMain {
		auth := v1.Group("/auth")
		{
			auth.POST("/login", r.Auth.Login)
			auth.POST("/refresh", r.Auth.Refresh)
			auth.POST("/logout", r.Auth.Logout)
		}
	}
	if r.ServesLocal {
		v1.POST("/auth/device/pair", r.TV.Pair)
	}

	// --- Device (TV) and media: local application only --------------------
	if r.ServesLocal {
		tv := v1.Group("/tv", DeviceAuth(r.Devices))
		{
			tv.GET("/init", r.TV.Init)
			tv.GET("/movies/:movieID", r.TV.Movie)
			tv.POST("/heartbeat", r.TV.Heartbeat)
			tv.POST("/watch-logs", r.TV.SaveWatchLog)
			tv.GET("/resume", r.TV.Resume)
			tv.GET("/commands", r.TV.PendingCommands)
			tv.POST("/commands/:commandID/ack", r.TV.AckCommand)
		}

		v1.GET("/stream/:movieID", r.TV.Stream)
		v1.HEAD("/stream/:movieID", r.TV.Stream)
		v1.GET("/assets/*path", r.TV.Asset)
		v1.HEAD("/assets/*path", r.TV.Asset)

		engine.GET("/ws/tv", r.WS.TV)
		engine.GET("/ws/remote", r.WS.Remote)
	}

	// --- Admin CMS: main application only ---------------------------------
	if r.ServesMain {
		admin := v1.Group("/admin", AdminAuth(r.Verifier))
		{
			admin.GET("/me", r.Auth.Me)
			admin.PUT("/me/password", r.Auth.ChangePassword)
			admin.GET("/overview", r.Admin.Overview)
			admin.GET("/overview/trend", r.Admin.Trend)
			admin.GET("/overview/top-movies", r.Admin.TopMovies)
			admin.GET("/devices", r.Admin.ListDevices)
			admin.POST("/devices/:deviceID/commands", r.Admin.SendCommand)
			admin.GET("/commands", r.Admin.ListCommands)
			admin.GET("/sync-jobs", r.Admin.ListSyncJobs)
			admin.POST("/sync-jobs", r.Admin.CreateSyncJob)
			admin.POST("/sync-jobs/:jobID/retry", r.Admin.RetrySyncJob)
			admin.GET("/scans", r.Admin.ScanHistory)
			admin.POST("/scans", r.Admin.TriggerScan)
			admin.GET("/inventory", r.Admin.ListInventory)
			admin.GET("/app-config", r.Admin.GetAppConfig)
			admin.PUT("/app-config", r.Admin.UpdateAppConfig)
			admin.GET("/feature-flags", r.Admin.ListFeatureFlags)
			admin.PUT("/feature-flags", r.Admin.ToggleFeatureFlag)
			admin.GET("/releases", r.Admin.ListReleases)
			admin.POST("/releases", r.Admin.UploadRelease)
			admin.PUT("/releases/:releaseID", r.Admin.SetReleaseActive)
		}

		super := admin.Group("", RequireRole(domain.RoleSuperAdmin))
		{
			super.GET("/tenants", r.Admin.ListTenants)
			super.POST("/tenants", r.Admin.CreateTenant)
			super.PUT("/tenants/:tenantID", r.Admin.UpdateTenant)
			super.POST("/tenants/:tenantID/status", r.Admin.SetTenantStatus)
			super.GET("/users", r.Admin.ListUsers)
			super.POST("/users", r.Admin.CreateUser)
			super.PUT("/users/:userID", r.Admin.UpdateUser)
			super.GET("/nodes", r.Admin.ListNodes)
			super.GET("/audit-logs", r.Admin.ListAuditLogs)

			// Cross-tenant content library. Reads take tenant_id from the query,
			// writes from the body; a super admin is not pinned to one tenant.
			super.GET("/movies", r.Admin.ListMovies)
			super.POST("/movies", r.Admin.CreateMovie)
			super.GET("/movies/:movieID", r.Admin.GetMovie)
			super.PUT("/movies/:movieID", r.Admin.UpdateMovie)
			super.DELETE("/movies/:movieID", r.Admin.DeleteMovie)
			super.POST("/movies/:movieID/poster", r.Admin.UploadMovieArtwork("poster"))
			super.POST("/movies/:movieID/banner", r.Admin.UploadMovieArtwork("banner"))
		}

		tenant := v1.Group("/tenant", AdminAuth(r.Verifier), RequireTenant())
		{
			tenant.GET("/overview", r.Admin.Overview)
			tenant.GET("/overview/trend", r.Admin.Trend)
			tenant.GET("/overview/top-movies", r.Admin.TopMovies)

			tenant.GET("/movies", r.Admin.ListMovies)
			tenant.POST("/movies", r.Admin.CreateMovie)
			tenant.GET("/movies/:movieID", r.Admin.GetMovie)
			tenant.PUT("/movies/:movieID", r.Admin.UpdateMovie)
			tenant.DELETE("/movies/:movieID", r.Admin.DeleteMovie)
			tenant.POST("/movies/:movieID/poster", r.Admin.UploadMovieArtwork("poster"))
			tenant.POST("/movies/:movieID/banner", r.Admin.UploadMovieArtwork("banner"))
			tenant.GET("/inventory", r.Admin.ListInventory)

			tenant.GET("/rooms", r.Admin.ListRooms)
			tenant.POST("/rooms", r.Admin.CreateRoom)
			tenant.PUT("/rooms/:roomID", r.Admin.UpdateRoom)
			tenant.POST("/rooms/:roomID/guest", r.Admin.CheckinGuest)
			tenant.DELETE("/rooms/:roomID/guest", r.Admin.CheckoutGuest)

			tenant.GET("/layout", r.Admin.GetLayout)
			tenant.PUT("/layout", r.Admin.PublishLayout)

			tenant.GET("/announcements", r.Admin.ListAnnouncements)
			tenant.POST("/announcements", r.Admin.CreateAnnouncement)
			tenant.PUT("/announcements/:announcementID", r.Admin.UpdateAnnouncement)
			tenant.DELETE("/announcements/:announcementID", r.Admin.DeleteAnnouncement)

			tenant.GET("/devices", r.Admin.ListDevices)
			tenant.POST("/devices/:deviceID/commands", r.Admin.SendCommand)
			tenant.GET("/commands", r.Admin.ListCommands)

			tenant.GET("/sync-jobs", r.Admin.ListSyncJobs)
			tenant.POST("/sync-jobs", r.Admin.CreateSyncJob)
			tenant.POST("/sync-jobs/:jobID/retry", r.Admin.RetrySyncJob)
			tenant.GET("/scans", r.Admin.ScanHistory)
			tenant.POST("/scans", r.Admin.TriggerScan)
			tenant.GET("/app-config", r.Admin.GetAppConfig)
			tenant.GET("/feature-flags", r.Admin.ListFeatureFlags)
			tenant.GET("/releases", r.Admin.ListReleases)
		}

		if r.Node != nil {
			v1.POST("/nodes/inventory", r.Node.ReportInventory)
		}
	}

	return engine
}
