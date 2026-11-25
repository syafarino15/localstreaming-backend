// Command api runs the StayIn platform for DLU X ALFATEVE.
//
// The product is TWO applications with TWO separate PostgreSQL databases:
//
//	main  -> server utama: CMS authoring, master catalog, distribution
//	local -> server lokal kapal: TV API, streaming, physical files, layout
//
// APP_MODE decides which application this process is. "combined" runs both in
// one process for local development, still against two separate databases.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	dbpkg "github.com/alfateve/stayin-local-backend/db"
	deliveryhttp "github.com/alfateve/stayin-local-backend/internal/delivery/http"
	"github.com/alfateve/stayin-local-backend/internal/delivery/ws"
	"github.com/alfateve/stayin-local-backend/internal/domain"
	"github.com/alfateve/stayin-local-backend/internal/platform/auth"
	"github.com/alfateve/stayin-local-backend/internal/platform/config"
	"github.com/alfateve/stayin-local-backend/internal/platform/httpx"
	"github.com/alfateve/stayin-local-backend/internal/platform/logger"
	"github.com/alfateve/stayin-local-backend/internal/platform/migrator"
	"github.com/alfateve/stayin-local-backend/internal/platform/storage"
	"github.com/alfateve/stayin-local-backend/internal/repository/postgres"
	"github.com/alfateve/stayin-local-backend/internal/usecase"
	"github.com/alfateve/stayin-local-backend/internal/worker/media"
	syncworker "github.com/alfateve/stayin-local-backend/internal/worker/sync"
)

func main() {
	if err := run(); err != nil {
		// The logger may not exist yet, so a plain stderr line is the last resort.
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logger.New(cfg.LogLevel, os.Stdout)
	startedAt := time.Now()

	log.Info().
		Str("mode", string(cfg.AppMode)).
		Str("addr", cfg.HTTPAddr).
		Str("tenant", cfg.TenantSlug).
		Str("media_root", cfg.MediaRoot).
		Bool("serves_main", cfg.ServesMain()).
		Bool("serves_local", cfg.ServesLocal()).
		Msg("starting stayin server")

	rootCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	// --- Storage ----------------------------------------------------------
	store, err := storage.New(storage.Layout{
		Root:    cfg.MediaRoot,
		Movies:  cfg.MediaMoviesDir,
		Banners: cfg.MediaBannersDir,
		Posters: cfg.MediaPostersDir,
		APK:     cfg.MediaAPKDir,
	}, cfg.StreamMaxOpenFiles)
	if err != nil {
		return err
	}
	if err := store.EnsureDirs(); err != nil {
		return err
	}
	log.Info().
		Str("movies", store.MoviesDir()).
		Str("banners", store.BannersDir()).
		Str("posters", store.PostersDir()).
		Str("apk", store.APKDir()).
		Msg("media directories ready")

	// --- Two separate databases ------------------------------------------
	initCtx, cancelInit := context.WithTimeout(rootCtx, 60*time.Second)

	var mainDB *postgres.Store
	if cfg.ServesMain() {
		mainDB, err = postgres.New(initCtx, cfg.MainDatabaseURL, cfg.DBMaxConns, cfg.DBMinConns)
		if err != nil {
			cancelInit()
			return fmt.Errorf("open main database: %w", err)
		}
		defer mainDB.Close()
		if err := migrator.Run(rootCtx, mainDB.Pool(), dbpkg.MainMigrations, dbpkg.MainDir, log); err != nil {
			cancelInit()
			return fmt.Errorf("migrate main database: %w", err)
		}
		log.Info().Msg("stayin_main schema is up to date")
	}

	var localDB *postgres.Local
	if cfg.ServesLocal() {
		localDB, err = postgres.NewLocal(initCtx, cfg.LocalDatabaseURL, cfg.DBMaxConns, cfg.DBMinConns)
		if err != nil {
			cancelInit()
			return fmt.Errorf("open local database: %w", err)
		}
		defer localDB.Close()
		if err := migrator.Run(rootCtx, localDB.Pool(), dbpkg.LocalMigrations, dbpkg.LocalDir, log); err != nil {
			cancelInit()
			return fmt.Errorf("migrate local database: %w", err)
		}
		log.Info().Msg("stayin_local schema is up to date")
	}
	cancelInit()

	// --- Real-time hub ----------------------------------------------------
	hub := ws.NewHub(log, cfg.WSPingInterval, cfg.WSWriteTimeout)
	notifier := domain.Notifier(hub)

	// --- Auth (main database) --------------------------------------------
	tokens := auth.NewManager(cfg.JWTSecret, cfg.JWTAccessTTL, cfg.JWTRefreshTTL)

	var authUC *usecase.Auth
	if mainDB != nil {
		authUC = usecase.NewAuth(mainDB, tokens, mainDB, log)

		bootstrapCtx, bootstrapCancel := context.WithTimeout(rootCtx, 30*time.Second)
		if err := authUC.EnsureBootstrap(bootstrapCtx, mainDB,
			domain.UserUpsertRequest{
				Email:    cfg.BootstrapSuperAdminEmail,
				Password: cfg.BootstrapSuperAdminPassword,
				FullName: cfg.BootstrapSuperAdminName,
				Role:     domain.RoleSuperAdmin,
			},
			domain.UserUpsertRequest{
				Email:    cfg.BootstrapTenantAdminEmail,
				Password: cfg.BootstrapTenantAdminPassword,
				FullName: cfg.BootstrapTenantAdminName,
				Role:     domain.RoleTenantAdmin,
			},
			cfg.TenantSlug,
		); err != nil {
			bootstrapCancel()
			return err
		}
		bootstrapCancel()
	}

	// The tenant master row lives in stayin_main; the ship gets a mirror in
	// stayin_local so it can render branding with no internet.
	var tenant *domain.Tenant
	if mainDB != nil {
		tenant, err = mainDB.GetBySlug(rootCtx, cfg.TenantSlug)
		if err != nil {
			return fmt.Errorf("resolve tenant %q: %w", cfg.TenantSlug, err)
		}
	}
	if localDB != nil {
		if tenant != nil {
			if err := localDB.UpsertTenantMirror(rootCtx, tenant); err != nil {
				return fmt.Errorf("mirror tenant to local: %w", err)
			}
		} else {
			tenant, err = localDB.GetBySlug(rootCtx, cfg.TenantSlug)
			if err != nil {
				return fmt.Errorf("resolve local tenant %q: %w", cfg.TenantSlug, err)
			}
		}
	}
	if tenant == nil {
		return fmt.Errorf("tenant %q tidak ditemukan pada database yang aktif", cfg.TenantSlug)
	}

	// --- Local application: TV, streaming, ingest -------------------------
	var tvUC *usecase.TV
	var mediaEngine *media.Engine
	if localDB != nil {
		tvUC = usecase.NewTV(usecase.TVDeps{
			Devices:       localDB,
			Rooms:         localDB,
			Tenants:       localDB,
			Layouts:       localDB,
			Movies:        localDB,
			Announcements: localDB,
			Commands:      localDB,
			WatchLogs:     localDB,
			Store:         store,
			Notifier:      notifier,
			Log:           log,
			PairingTTL:    cfg.PairingTTL,
			NodeCode:      cfg.NodeCode,
			AppMode:       string(cfg.AppMode),
			HeartbeatSecs: int(cfg.HeartbeatTimeout.Seconds()),
		})

		var mainCatalog domain.MovieRepository
		if mainDB != nil {
			mainCatalog = mainDB
		}

		mediaEngine = media.NewEngine(media.Options{
			TenantID:    tenant.ID,
			NodeCode:    cfg.NodeCode,
			MoviesDir:   store.MoviesDir(),
			ComputeHash: cfg.ScanComputeHash,
			HashWorkers: cfg.ScanHashWorkers,
			MaxDepth:    cfg.ScanMaxDepth,
			Interval:    cfg.ScanInterval,
		}, store, localDB, localDB, localDB, mainCatalog, hub, log)
	}

	// --- Main application: CMS -------------------------------------------
	var scanTrigger usecase.ScanTrigger
	if mediaEngine != nil {
		scanTrigger = mediaEngine
	}

	var adminUC *usecase.Admin
	if mainDB != nil {
		deps := usecase.AdminDeps{
			Tenants:    mainDB,
			Users:      mainDB,
			Nodes:      mainDB,
			Movies:     mainDB,
			SyncJobs:   mainDB,
			Audits:     mainDB,
			AppConfigs: mainDB,
			Flags:      mainDB,
			Releases:   mainDB,
			MainStats:  mainDB,
			Notifier:   notifier,
			Scanner:    scanTrigger,
			Store:      store,
			Log:        log,
		}
		if localDB != nil {
			deps.Local = &usecase.LocalDeps{
				Rooms:         localDB,
				Devices:       localDB,
				Layouts:       localDB,
				Announcements: localDB,
				Commands:      localDB,
				Scans:         localDB,
				WatchLogs:     localDB,
				Movies:        localDB,
				Tenants:       localDB,
				Stats:         localDB,
				NodeCode:      cfg.NodeCode,
			}
		}
		adminUC = usecase.NewAdmin(deps)
	}

	// --- Sync worker (main queue) ----------------------------------------
	var syncWorker *syncworker.Worker
	if mainDB != nil {
		syncWorker = syncworker.New(mainDB, scanTrigger, notifier, store, syncworker.Options{
			Interval:     cfg.SyncInterval,
			CloudBaseURL: cfg.CloudBaseURL,
			CloudAPIKey:  cfg.CloudAPIKey,
		}, log)
	}

	// --- HTTP surface -----------------------------------------------------
	pool := httpx.NewBufferPool(cfg.StreamBufferBytes)

	var wsHandler *ws.Handler
	if localDB != nil {
		wsHandler = ws.NewHandler(hub, localDB, localDB, log, cfg.HeartbeatTimeout, cfg.CORSAllowedOrigins)
	}

	ginMode := "release"
	if cfg.LogLevel == "debug" {
		ginMode = "debug"
	}

	uploader := deliveryhttp.NewUploader(store, cfg.MaxUploadBytes)

	pingers := make([]deliveryhttp.Pinger, 0, 2)
	if mainDB != nil {
		pingers = append(pingers, mainDB)
	}
	if localDB != nil {
		pingers = append(pingers, localDB)
	}

	var tvHandler *deliveryhttp.TVHandler
	if tvUC != nil {
		tvHandler = deliveryhttp.NewTVHandler(tvUC, store, pool, cfg.StreamCacheSeconds, log)
	}

	var authHandler *deliveryhttp.AuthHandler
	if authUC != nil {
		authHandler = deliveryhttp.NewAuthHandler(authUC, cfg.CookieSecure, cfg.CookieDomain)
	}

	router := &deliveryhttp.Router{
		Mode:        ginMode,
		AppMode:     string(cfg.AppMode),
		ServesMain:  cfg.ServesMain(),
		ServesLocal: cfg.ServesLocal(),
		Log:         log,
		Pingers:     pingers,
		Verifier:    authUC,
		Devices:     localDB,
		Auth:        authHandler,
		TV:          tvHandler,
		Admin:       deliveryhttp.NewAdminHandler(adminUC, store, uploader),
		Node:        deliveryhttp.NewNodeHandler(adminUC, cfg.NodeAPIKey),
		WS:          wsHandler,
		StartedAt:   startedAt,
		CORSOrigins: cfg.CORSAllowedOrigins,
	}

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router.Build(),
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       0, // video uploads and long reads are legitimate
		WriteTimeout:      0, // streaming responses must never be cut
		IdleTimeout:       180 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	// --- Background work --------------------------------------------------
	if mediaEngine != nil {
		mediaEngine.Start(rootCtx, cfg.ScanOnStart)
	}
	if syncWorker != nil {
		syncWorker.Start(rootCtx)
	}
	if localDB != nil {
		go markStaleDevices(rootCtx, localDB, log, int(cfg.HeartbeatTimeout.Seconds()))
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Info().Str("addr", server.Addr).Msg("http server listening")
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("http server: %w", err)
	case <-rootCtx.Done():
		log.Info().Msg("shutdown signal received")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if mediaEngine != nil {
		mediaEngine.Close()
	}

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("graceful shutdown failed")
		return fmt.Errorf("shutdown: %w", err)
	}

	log.Info().Dur("uptime", time.Since(startedAt)).Msg("server stopped cleanly")
	return nil
}

// markStaleDevices flips TVs that stopped sending heartbeats so the CMS never
// shows a dead device as online. Devices live in stayin_local.
func markStaleDevices(ctx context.Context, local *postgres.Local, log zerolog.Logger, timeoutSeconds int) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			affected, err := local.MarkStaleDevicesOffline(ctx, timeoutSeconds)
			if err != nil {
				log.Warn().Err(err).Msg("mark stale devices failed")
				continue
			}
			if affected > 0 {
				log.Info().Int64("devices", affected).Msg("devices marked offline")
			}
		}
	}
}
