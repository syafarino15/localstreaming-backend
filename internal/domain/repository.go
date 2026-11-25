package domain

import (
	"context"
	"time"
)

// TenantRepository persists tenants.
type TenantRepository interface {
	GetBySlug(ctx context.Context, slug string) (*Tenant, error)
	GetTenantByID(ctx context.Context, id string) (*Tenant, error)
	ListTenants(ctx context.Context, p Pagination) ([]Tenant, int64, error)
	CreateTenant(ctx context.Context, t *Tenant) error
	UpdateTenant(ctx context.Context, t *Tenant) error
	SetTenantStatus(ctx context.Context, id, status string) error
	UpdateLicense(ctx context.Context, id string, quota int) error
	CountActiveTenants(ctx context.Context) (int64, error)
	CatalogVersion(ctx context.Context, tenantID string) (int, error)
	BumpCatalogVersion(ctx context.Context, tenantID string) (int, error)
}

// UserRepository persists admin accounts.
type UserRepository interface {
	GetByEmail(ctx context.Context, email string) (*User, error)
	GetUserByID(ctx context.Context, id string) (*User, error)
	ListUsers(ctx context.Context, tenantID string, p Pagination) ([]User, int64, error)
	CreateUser(ctx context.Context, u *User) error
	UpdateUser(ctx context.Context, u *User) error
	UpdatePassword(ctx context.Context, id, passwordHash string) error
	TouchLogin(ctx context.Context, id string) error
	CountUsers(ctx context.Context) (int64, error)
}

// LocalTenantRepository reads the tenant mirror on the ship database. The local
// application never manages tenants; it only needs branding and a catalog
// version, both of which are replicated from the main server.
type LocalTenantRepository interface {
	GetTenantByID(ctx context.Context, id string) (*Tenant, error)
	GetBySlug(ctx context.Context, slug string) (*Tenant, error)
	CatalogVersion(ctx context.Context, tenantID string) (int, error)
	BumpCatalogVersion(ctx context.Context, tenantID string) (int, error)
	UpsertTenantMirror(ctx context.Context, t *Tenant) error
}

// NodeRepository persists edge node registrations on the main server.
type NodeRepository interface {
	ListNodes(ctx context.Context, tenantID string, p Pagination) ([]EdgeNode, int64, error)
	GetNodeByID(ctx context.Context, tenantID, id string) (*EdgeNode, error)
	GetByCode(ctx context.Context, tenantID, code string) (*EdgeNode, error)
	UpsertNode(ctx context.Context, n *EdgeNode) error
	UpdateMetrics(ctx context.Context, id string, metrics []byte, status string) error
	UpsertInventory(ctx context.Context, tenantID, nodeCode string, inventory []byte) error
}

// RoomRepository persists cabins and their VIP guest state.
type RoomRepository interface {
	ListRooms(ctx context.Context, tenantID string, p Pagination) ([]Room, int64, error)
	GetRoomByID(ctx context.Context, tenantID, id string) (*Room, error)
	GetRoomByNumber(ctx context.Context, tenantID, number string) (*Room, error)
	CreateRoom(ctx context.Context, r *Room) error
	UpdateRoom(ctx context.Context, r *Room) error
	SetGuest(ctx context.Context, r *Room) error
	ClearGuest(ctx context.Context, tenantID, id string) error
	CountRooms(ctx context.Context, tenantID string) (int64, error)
}

// DeviceRepository persists TV devices.
type DeviceRepository interface {
	GetByUID(ctx context.Context, deviceUID string) (*Device, error)
	GetByTokenHash(ctx context.Context, tokenHash string) (*Device, error)
	GetByPairingCode(ctx context.Context, code string) (*Device, error)
	ClaimDevice(ctx context.Context, id, deviceUID, tokenHash string) error
	GetDeviceByID(ctx context.Context, tenantID, id string) (*Device, error)
	ListDevices(ctx context.Context, f DeviceFilter) ([]Device, int64, error)
	ListByRoom(ctx context.Context, tenantID, roomID string) ([]Device, error)
	CreateDevice(ctx context.Context, d *Device) error
	UpdateDeviceStatus(ctx context.Context, id string, status DeviceStatus, ip string) error
	SetPairingCode(ctx context.Context, id, code string, expiresAt time.Time) error
	SetToken(ctx context.Context, id, tokenHash string) error
	TouchSeen(ctx context.Context, id, appVersion, ip string) error
	AssignRoom(ctx context.Context, tenantID, deviceID string, roomID *string) error
	CountByTenant(ctx context.Context, tenantID string) (int64, error)
	CountOnline(ctx context.Context, tenantID string) (int64, error)
}

// LayoutRepository persists TV layout documents.
type LayoutRepository interface {
	GetActive(ctx context.Context, tenantID string) (*LayoutConfigRecord, error)
	Publish(ctx context.Context, tenantID string, cfg LayoutConfig, updatedBy string) (*LayoutConfigRecord, error)
	History(ctx context.Context, tenantID string, p Pagination) ([]LayoutConfigRecord, int64, error)
}

// MovieRepository persists MASTER catalog entries in stayin_main. It never
// touches filesystem columns, because those live in MovieLocalRepository.
type MovieRepository interface {
	ListMovies(ctx context.Context, f MovieFilter) ([]Movie, int64, error)
	GetMovieByID(ctx context.Context, tenantID, id string) (*Movie, *MovieDetail, error)
	GetMovieByIDGlobal(ctx context.Context, id string) (*Movie, *MovieDetail, error)
	GetMovieBySlug(ctx context.Context, tenantID, slug string) (*Movie, error)
	CreateMovie(ctx context.Context, m *Movie, d *MovieDetail) error
	UpdateMovie(ctx context.Context, m *Movie, d *MovieDetail) error
	DeleteMovie(ctx context.Context, tenantID, id string) error
	CountByStatus(ctx context.Context, tenantID string, status MovieStatus) (int64, error)
	Genres(ctx context.Context, tenantID string) ([]string, error)
	MarkPublished(ctx context.Context, tenantID string, ids []string) (int64, error)
	UpsertCatalogFromNode(ctx context.Context, m *Movie, d *MovieDetail) (bool, error)
}

// MovieLocalRepository persists the catalog mirror and the physical file
// mapping in stayin_local. This is the contract the ingest engine and the
// streaming endpoint depend on.
type MovieLocalRepository interface {
	UpsertCatalogMovie(ctx context.Context, m *Movie, d *MovieDetail, source MovieSource) (bool, error)
	GetCatalogMovie(ctx context.Context, tenantID, id string) (*Movie, *MovieDetail, error)
	ListCatalogMovies(ctx context.Context, f MovieFilter) ([]Movie, int64, error)
	Genres(ctx context.Context, tenantID string) ([]string, error)

	UpsertMovieLocal(ctx context.Context, m *MovieLocal) (created bool, err error)
	GetMovieLocalByMovieID(ctx context.Context, tenantID, movieID string) (*MovieLocal, error)
	GetMovieLocalByPath(ctx context.Context, tenantID, filePath string) (*MovieLocal, error)
	GetMovieLocalByExpectedName(ctx context.Context, tenantID, filename string) (*MovieLocal, error)
	ListMovieLocals(ctx context.Context, f MovieLocalFilter) ([]MovieLocal, int64, error)
	ListReadyMovieLocals(ctx context.Context, tenantID string) ([]MovieLocal, error)
	SetMovieLocalChecksum(ctx context.Context, id, checksum string) error
	MarkMovieLocalMissing(ctx context.Context, tenantID string, ids []string) (int64, error)
	DeleteMovieLocal(ctx context.Context, tenantID, movieID string) error
	CountMovieLocalByStatus(ctx context.Context, tenantID string, status LocalMovieStatus) (int64, error)
	StorageUsedBytes(ctx context.Context, tenantID string) (int64, error)

	ListPlayable(ctx context.Context, tenantID string, limit int) ([]MovieWithLocal, error)
	GetPlayable(ctx context.Context, tenantID, movieID string) (*MovieWithLocal, *MovieDetail, error)
	GetPlayableGlobal(ctx context.Context, movieID string) (*MovieWithLocal, error)
	TopMovies(ctx context.Context, tenantID string, days int, limit int) ([]TopMovie, error)
}

// AnnouncementRepository persists running text messages.
type AnnouncementRepository interface {
	ListAnnouncements(ctx context.Context, tenantID string, p Pagination) ([]Announcement, int64, error)
	ListActive(ctx context.Context, tenantID string) ([]Announcement, error)
	CreateAnnouncement(ctx context.Context, a *Announcement) error
	UpdateAnnouncement(ctx context.Context, a *Announcement) error
	DeleteAnnouncement(ctx context.Context, tenantID, id string) error
	CountActiveAnnouncements(ctx context.Context, tenantID string) (int64, error)
}

// SyncJobRepository is the work queue for the sync worker.
type SyncJobRepository interface {
	CreateSyncJob(ctx context.Context, j *SyncJob) error
	ListSyncJobs(ctx context.Context, tenantID string, p Pagination) ([]SyncJob, int64, error)
	GetSyncJobByID(ctx context.Context, tenantID, id string) (*SyncJob, error)
	ClaimNext(ctx context.Context) (*SyncJob, error)
	CompleteSyncJob(ctx context.Context, id string) error
	FailSyncJob(ctx context.Context, id, message string) error
	RequeueSyncJob(ctx context.Context, tenantID, id string) error
	UpdateJobProgress(ctx context.Context, id string, done, total int64) error
}

// CommandRepository persists remote commands and their delivery state.
type CommandRepository interface {
	CreateCommand(ctx context.Context, c *RemoteCommand) error
	ListCommands(ctx context.Context, tenantID string, p Pagination) ([]RemoteCommand, int64, error)
	GetCommandByID(ctx context.Context, id string) (*RemoteCommand, error)
	ListPendingForDevice(ctx context.Context, deviceID string) ([]RemoteCommand, error)
	MarkSent(ctx context.Context, id string) error
	MarkAcked(ctx context.Context, id string) error
	MarkFailed(ctx context.Context, id, message string) error
}

// WatchLogRepository persists playback progress.
type WatchLogRepository interface {
	UpsertWatchLog(ctx context.Context, w *WatchLog) error
	ListByDevice(ctx context.Context, tenantID, deviceID string, limit int) ([]WatchLog, error)
}

// AuditRepository persists the append-only audit trail.
type AuditRepository interface {
	CreateAuditLog(ctx context.Context, a *AuditLog) error
	ListAuditLogs(ctx context.Context, tenantID string, p Pagination) ([]AuditLog, int64, error)
}

// MediaScanRepository records ingest engine executions.
type MediaScanRepository interface {
	StartScan(ctx context.Context, run *MediaScanRun) error
	FinishScan(ctx context.Context, run *MediaScanRun) error
	LastScan(ctx context.Context, tenantID string) (*MediaScanRun, error)
	ListScans(ctx context.Context, tenantID string, p Pagination) ([]MediaScanRun, int64, error)
}

// MainStatsRepository provides authoring-side counters from stayin_main.
type MainStatsRepository interface {
	MainOverviewMetrics(ctx context.Context) (*MainOverview, error)
}

// LocalStatsRepository provides operational counters from stayin_local.
type LocalStatsRepository interface {
	LocalOverviewMetrics(ctx context.Context, tenantID string) (*LocalOverview, error)
	WatchTrend(ctx context.Context, tenantID string, days int) ([]TrendPoint, error)
	WatchLogCount(ctx context.Context, tenantID string) (int64, error)
	MarkStaleDevicesOffline(ctx context.Context, timeoutSeconds int) (int64, error)
	InventorySnapshot(ctx context.Context, tenantID, nodeCode string) ([]InventoryEntry, error)
}

// InventoryEntry is one row of the node inventory report pushed to the main
// server, so the CMS can show ship content state while the node is offline.
type InventoryEntry struct {
	MovieID        string     `json:"movie_id"`
	Status         string     `json:"status"`
	Source         string     `json:"source"`
	FilePath       string     `json:"file_path"`
	FileSizeBytes  int64      `json:"file_size_bytes"`
	ChecksumSHA256 string     `json:"checksum_sha256"`
	ReadyAt        *time.Time `json:"ready_at"`
}

// MainOverview carries authoring counters.
type MainOverview struct {
	TotalTenants  int64 `json:"total_tenants"`
	ActiveTenants int64 `json:"active_tenants"`
	TotalNodes    int64 `json:"total_nodes"`
	OnlineNodes   int64 `json:"online_nodes"`
	TotalMovies   int64 `json:"total_movies"`
	PendingMovies int64 `json:"pending_movies"`
}

// LocalOverview carries ship-side counters.
type LocalOverview struct {
	TotalRooms          int64 `json:"total_rooms"`
	TotalDevices        int64 `json:"total_devices"`
	OnlineDevices       int64 `json:"online_devices"`
	ReadyMovies         int64 `json:"ready_movies"`
	TotalLocalMovies    int64 `json:"total_local_movies"`
	ActiveAnnouncements int64 `json:"active_announcements"`
	StorageUsedBytes    int64 `json:"storage_used_bytes"`
}
