// Package config loads and validates runtime configuration from the
// environment. Every value has a safe default so the service can boot in a
// fresh environment, except the database URLs and JWT_SECRET which are
// mandatory.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Mode selects which application this process is, and therefore which of the
// two databases it opens and which route groups it exposes.
//
// StayIn runs as two separate applications backed by two separate PostgreSQL
// databases:
//
//	main  -> server utama: CMS authoring, master catalog, distribution
//	local -> server lokal kapal: TV API, streaming, physical files, layout
//
// "combined" runs both in one process. It exists for local development only and
// still keeps the two databases completely separate.
type Mode string

const (
	// ModeCombined runs the main and local applications in one process, each
	// against its own database. Development convenience, not a production shape.
	ModeCombined Mode = "combined"
	// ModeLocal serves TV, streaming, websocket, media scan and layout.
	ModeLocal Mode = "local"
	// ModeMain serves the admin CMS, master catalog and distribution queue.
	ModeMain Mode = "main"
)

// Config is the fully resolved application configuration.
type Config struct {
	AppMode  Mode
	HTTPAddr string
	LogLevel string
	Timezone *time.Location

	MainDatabaseURL  string
	LocalDatabaseURL string
	DBMaxConns       int32
	DBMinConns       int32

	JWTSecret     []byte
	JWTAccessTTL  time.Duration
	JWTRefreshTTL time.Duration
	CookieSecure  bool
	CookieDomain  string

	CORSAllowedOrigins []string

	BootstrapSuperAdminEmail     string
	BootstrapSuperAdminPassword  string
	BootstrapSuperAdminName      string
	BootstrapTenantAdminEmail    string
	BootstrapTenantAdminPassword string
	BootstrapTenantAdminName     string

	TenantSlug string
	TenantName string
	NodeCode   string

	MediaRoot       string
	MediaMoviesDir  string
	MediaBannersDir string
	MediaPostersDir string
	MediaAPKDir     string

	ScanOnStart     bool
	ScanInterval    time.Duration
	ScanComputeHash bool
	ScanHashWorkers int
	ScanMaxDepth    int

	StreamBufferBytes  int
	StreamMaxOpenFiles int
	StreamCacheSeconds int

	WSPingInterval   time.Duration
	WSWriteTimeout   time.Duration
	HeartbeatTimeout time.Duration

	MaxUploadBytes int64
	PairingTTL     time.Duration

	SyncInterval time.Duration
	CloudBaseURL string
	CloudAPIKey  string
	NodeAPIKey   string

	TrustedProxies []string
}

// Load reads .env when present, then resolves and validates the configuration.
func Load() (*Config, error) {
	// Best effort: a missing .env is normal inside containers where the
	// environment is injected by compose.
	_ = godotenv.Load()

	cfg := &Config{
		AppMode:  Mode(strings.ToLower(getEnv("APP_MODE", string(ModeCombined)))),
		HTTPAddr: getEnv("HTTP_ADDR", ":8080"),
		LogLevel: getEnv("LOG_LEVEL", "info"),

		MainDatabaseURL:  getEnv("MAIN_DATABASE_URL", getEnv("DATABASE_URL", "")),
		LocalDatabaseURL: getEnv("LOCAL_DATABASE_URL", getEnv("DATABASE_URL", "")),
		DBMaxConns:       int32(getEnvInt("DB_MAX_CONNS", 20)),
		DBMinConns:       int32(getEnvInt("DB_MIN_CONNS", 2)),

		JWTSecret:     []byte(getEnv("JWT_SECRET", "")),
		JWTAccessTTL:  getEnvDuration("JWT_ACCESS_TTL", 8*time.Hour),
		JWTRefreshTTL: getEnvDuration("JWT_REFRESH_TTL", 168*time.Hour),
		CookieSecure:  getEnvBool("COOKIE_SECURE", false),
		CookieDomain:  getEnv("COOKIE_DOMAIN", ""),

		CORSAllowedOrigins: getEnvList("CORS_ALLOWED_ORIGINS", []string{
			"http://localhost:3000",
			"http://127.0.0.1:3000",
		}),

		BootstrapSuperAdminEmail:     getEnv("BOOTSTRAP_SUPER_ADMIN_EMAIL", "owner@stayin.local"),
		BootstrapSuperAdminPassword:  getEnv("BOOTSTRAP_SUPER_ADMIN_PASSWORD", "Owner#2026"),
		BootstrapSuperAdminName:      getEnv("BOOTSTRAP_SUPER_ADMIN_NAME", "StayIn Owner"),
		BootstrapTenantAdminEmail:    getEnv("BOOTSTRAP_TENANT_ADMIN_EMAIL", "admin@dlu-alfateve.local"),
		BootstrapTenantAdminPassword: getEnv("BOOTSTRAP_TENANT_ADMIN_PASSWORD", "Mitra#2026"),
		BootstrapTenantAdminName:     getEnv("BOOTSTRAP_TENANT_ADMIN_NAME", "DLU Alfateve Admin"),

		TenantSlug: getEnv("TENANT_SLUG", "dlu-alfateve"),
		TenantName: getEnv("TENANT_NAME", "DLU X ALFATEVE"),
		NodeCode:   getEnv("NODE_CODE", "DLU-NODE-01"),

		MediaRoot:       getEnv("MEDIA_ROOT", "/DATA/Media"),
		MediaMoviesDir:  getEnv("MEDIA_MOVIES_DIR", "Movies"),
		MediaBannersDir: getEnv("MEDIA_BANNERS_DIR", "Banners"),
		MediaPostersDir: getEnv("MEDIA_POSTERS_DIR", "Posters"),
		MediaAPKDir:     getEnv("MEDIA_APK_DIR", "Apk"),

		ScanOnStart:     getEnvBool("SCAN_ON_START", true),
		ScanInterval:    time.Duration(getEnvInt("SCAN_INTERVAL_SECONDS", 300)) * time.Second,
		ScanComputeHash: getEnvBool("SCAN_COMPUTE_HASH", true),
		ScanHashWorkers: getEnvInt("SCAN_HASH_WORKERS", 2),
		ScanMaxDepth:    getEnvInt("SCAN_MAX_DEPTH", 6),

		StreamBufferBytes:  getEnvInt("STREAM_BUFFER_BYTES", 64*1024),
		StreamMaxOpenFiles: getEnvInt("STREAM_MAX_OPEN_FILES", 256),
		StreamCacheSeconds: getEnvInt("STREAM_CACHE_SECONDS", 86400),

		WSPingInterval:   time.Duration(getEnvInt("WS_PING_INTERVAL_SECONDS", 25)) * time.Second,
		WSWriteTimeout:   time.Duration(getEnvInt("WS_WRITE_TIMEOUT_SECONDS", 10)) * time.Second,
		HeartbeatTimeout: time.Duration(getEnvInt("HEARTBEAT_TIMEOUT_SECONDS", 120)) * time.Second,

		MaxUploadBytes: int64(getEnvInt("MAX_UPLOAD_BYTES", 20*1024*1024)),
		PairingTTL:     time.Duration(getEnvInt("PAIRING_TTL_MINUTES", 30)) * time.Minute,

		SyncInterval: time.Duration(getEnvInt("SYNC_INTERVAL_SECONDS", 15)) * time.Second,
		CloudBaseURL: strings.TrimRight(getEnv("CLOUD_BASE_URL", ""), "/"),
		CloudAPIKey:  getEnv("CLOUD_API_KEY", ""),
		NodeAPIKey:   getEnv("NODE_API_KEY", ""),

		TrustedProxies: getEnvList("TRUSTED_PROXIES", nil),
	}

	tzName := getEnv("TZ", "Asia/Jakarta")
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return nil, fmt.Errorf("config: invalid TZ %q: %w", tzName, err)
	}
	cfg.Timezone = loc

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate enforces the invariants that must hold before the process starts.
func (c *Config) Validate() error {
	switch c.AppMode {
	case ModeCombined, ModeLocal, ModeMain:
	default:
		return fmt.Errorf("config: APP_MODE must be one of combined|local|main, got %q", c.AppMode)
	}

	switch c.AppMode {
	case ModeCombined:
		if c.MainDatabaseURL == "" || c.LocalDatabaseURL == "" {
			return fmt.Errorf("config: MAIN_DATABASE_URL and LOCAL_DATABASE_URL are both required in combined mode")
		}
		if c.MainDatabaseURL == c.LocalDatabaseURL {
			return fmt.Errorf("config: MAIN_DATABASE_URL and LOCAL_DATABASE_URL must point to different databases")
		}
	case ModeMain:
		if c.MainDatabaseURL == "" {
			return fmt.Errorf("config: MAIN_DATABASE_URL is required in main mode")
		}
	case ModeLocal:
		if c.LocalDatabaseURL == "" {
			return fmt.Errorf("config: LOCAL_DATABASE_URL is required in local mode")
		}
	}

	if len(c.JWTSecret) < 16 {
		return fmt.Errorf("config: JWT_SECRET must be at least 16 characters")
	}
	if c.DBMaxConns < c.DBMinConns {
		return fmt.Errorf("config: DB_MAX_CONNS (%d) must be >= DB_MIN_CONNS (%d)", c.DBMaxConns, c.DBMinConns)
	}
	if c.StreamBufferBytes < 32*1024 {
		return fmt.Errorf("config: STREAM_BUFFER_BYTES must be at least 32768")
	}
	if c.ScanHashWorkers < 1 {
		return fmt.Errorf("config: SCAN_HASH_WORKERS must be at least 1")
	}
	return nil
}

// MoviesDir is the absolute path of the video library.
func (c *Config) MoviesDir() string { return joinRoot(c.MediaRoot, c.MediaMoviesDir) }

// BannersDir is the absolute path of shared branding assets.
func (c *Config) BannersDir() string { return joinRoot(c.MediaRoot, c.MediaBannersDir) }

// PostersDir is the absolute path of poster artwork.
func (c *Config) PostersDir() string { return joinRoot(c.MediaRoot, c.MediaPostersDir) }

// APKDir is the absolute path of distributed application packages.
func (c *Config) APKDir() string { return joinRoot(c.MediaRoot, c.MediaAPKDir) }

// ServesLocal reports whether TV, streaming, websocket and media scanning run.
func (c *Config) ServesLocal() bool { return c.AppMode == ModeCombined || c.AppMode == ModeLocal }

// ServesMain reports whether the admin CMS endpoints run.
func (c *Config) ServesMain() bool { return c.AppMode == ModeCombined || c.AppMode == ModeMain }

func joinRoot(root, dir string) string {
	root = strings.TrimRight(root, "/\\")
	dir = strings.Trim(dir, "/\\")
	if dir == "" {
		return root
	}
	return root + "/" + dir
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

func getEnvBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if raw == "" {
		return fallback
	}
	switch raw {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return v
}

func getEnvList(key string, fallback []string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}
