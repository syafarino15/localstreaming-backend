// Package domain holds the business entities and the repository contracts.
// It has no dependency on transport, database driver, or framework packages.
package domain

import (
	"encoding/json"
	"time"
)

// Role identifies the authorization level of an admin user.
type Role string

const (
	RoleSuperAdmin  Role = "SUPER_ADMIN"
	RoleTenantAdmin Role = "TENANT_ADMIN"
)

// DeviceStatus is the lifecycle state of a TV device.
type DeviceStatus string

const (
	DeviceUnpaired DeviceStatus = "UNPAIRED"
	DeviceOnline   DeviceStatus = "ONLINE"
	DeviceOffline  DeviceStatus = "OFFLINE"
	DeviceDisabled DeviceStatus = "DISABLED"
)

// MovieStatus reflects the authoring stage of a master catalog entry on the
// main server. It says nothing about whether the file already exists on a ship.
type MovieStatus string

const (
	// MovieDraft is still being authored in the CMS.
	MovieDraft MovieStatus = "DRAFT"
	// MoviePending is published but has not reached any local server yet.
	MoviePending MovieStatus = "PENDING"
	// MoviePublished has been distributed to at least one local server.
	MoviePublished MovieStatus = "PUBLISHED"
	// MovieArchived is withdrawn from circulation.
	MovieArchived MovieStatus = "ARCHIVED"
)

// LocalMovieStatus is the state of the physical file on one local server.
type LocalMovieStatus string

const (
	// LocalPending is scheduled but the file is not on the ship yet.
	LocalPending LocalMovieStatus = "PENDING"
	// LocalTransferring is being copied from the main server.
	LocalTransferring LocalMovieStatus = "TRANSFERRING"
	// LocalReady has a file on disk that can be streamed.
	LocalReady LocalMovieStatus = "READY"
	// LocalMissing had a file that disappeared from storage.
	LocalMissing LocalMovieStatus = "MISSING"
	// LocalFailed could not be copied.
	LocalFailed LocalMovieStatus = "FAILED"
)

// MovieSource records how the file reached the local server.
type MovieSource string

const (
	// SourceFlashdisk is ingested by the media scanner from a shared folder
	// that was filled through a USB flashdisk.
	SourceFlashdisk MovieSource = "FLASHDISK"
	// SourceCloud was transferred from the main server over the network.
	SourceCloud MovieSource = "CLOUD"
)

// CommandStatus tracks delivery of a remote command to a device.
type CommandStatus string

const (
	CommandQueued CommandStatus = "QUEUED"
	CommandSent   CommandStatus = "SENT"
	CommandAcked  CommandStatus = "ACKED"
	CommandFailed CommandStatus = "FAILED"
)

// SyncJobStatus tracks a content synchronisation job.
type SyncJobStatus string

const (
	SyncQueued    SyncJobStatus = "QUEUED"
	SyncRunning   SyncJobStatus = "RUNNING"
	SyncCompleted SyncJobStatus = "COMPLETED"
	SyncFailed    SyncJobStatus = "FAILED"
)

// Tenant is a whitelabel partner running its own node.
type Tenant struct {
	ID           string          `json:"id"`
	Slug         string          `json:"slug"`
	Name         string          `json:"name"`
	Branding     json.RawMessage `json:"branding"`
	LicenseQuota int             `json:"license_quota"`
	Status       string          `json:"status"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// User is an admin account. TenantID is nil for super admins.
type User struct {
	ID           string     `json:"id"`
	TenantID     *string    `json:"tenant_id"`
	Email        string     `json:"email"`
	PasswordHash string     `json:"-"`
	FullName     string     `json:"full_name"`
	Role         Role       `json:"role"`
	Status       string     `json:"status"`
	LastLoginAt  *time.Time `json:"last_login_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// EdgeNode is a physical local server registered on the main server.
// Inventory mirrors the last mst_movie_local report so the CMS can show ship
// content state even while the node is offline.
type EdgeNode struct {
	ID              string          `json:"id"`
	TenantID        string          `json:"tenant_id"`
	NodeCode        string          `json:"node_code"`
	Name            string          `json:"name"`
	BaseURL         string          `json:"base_url"`
	TunnelIP        string          `json:"tunnel_ip"`
	Status          string          `json:"status"`
	AgentVersion    string          `json:"agent_version"`
	Metrics         json.RawMessage `json:"metrics"`
	Inventory       json.RawMessage `json:"inventory"`
	LastHeartbeatAt *time.Time      `json:"last_heartbeat_at"`
	LastInventoryAt *time.Time      `json:"last_inventory_at"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// Room is a cabin on the ship. VIP rooms carry guest personalisation.
type Room struct {
	ID              string     `json:"id"`
	TenantID        string     `json:"tenant_id"`
	RoomNumber      string     `json:"room_number"`
	Deck            string     `json:"deck"`
	RoomType        string     `json:"room_type"`
	IsExclusive     bool       `json:"is_exclusive"`
	GuestName       string     `json:"guest_name"`
	GuestSalutation string     `json:"guest_salutation"`
	GuestCheckinAt  *time.Time `json:"guest_checkin_at"`
	GuestCheckoutAt *time.Time `json:"guest_checkout_at"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// HasGuest reports whether a guest is currently checked in.
func (r Room) HasGuest() bool { return r.GuestName != "" }

// Device is a TV or set-top box bound to a room.
type Device struct {
	ID               string       `json:"id"`
	TenantID         string       `json:"tenant_id"`
	RoomID           *string      `json:"room_id"`
	DeviceUID        string       `json:"device_uid"`
	Label            string       `json:"label"`
	Model            string       `json:"model"`
	AppVersion       string       `json:"app_version"`
	PairingCode      *string      `json:"pairing_code"`
	PairingExpiresAt *time.Time   `json:"pairing_expires_at"`
	DeviceTokenHash  string       `json:"-"`
	Status           DeviceStatus `json:"status"`
	IPAddress        string       `json:"ip_address"`
	LastSeenAt       *time.Time   `json:"last_seen_at"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
}

// LayoutConfig is the JSON document that drives the welcome screen of one
// tenant. It deliberately contains no catalog composition: row order and the
// featured movie belong to content management, not to the opening layout.
type LayoutConfig struct {
	TenantID      string        `json:"tenant_id"`
	AppTitle      string        `json:"app_title"`
	WelcomeScreen WelcomeScreen `json:"welcome_screen"`
}

// WelcomeScreen configures the greeting screen shown before the catalog.
type WelcomeScreen struct {
	Enabled            bool   `json:"enabled"`
	LayoutVariant      string `json:"layout_variant"`
	BackgroundImageURL string `json:"background_image_url"`
	LogoURL            string `json:"logo_url"`
	PrimaryColor       string `json:"primary_color"`
	AccentColor        string `json:"accent_color"`
	CTAText            string `json:"cta_button_text"`
	DefaultGreeting    string `json:"default_greeting"`
	ShowRoomInfo       bool   `json:"show_room_info"`
	ShowClock          bool   `json:"show_clock"`
	OverscanPercent    int    `json:"overscan_percent"`
}

// LayoutConfigRecord is the persisted layout row.
type LayoutConfigRecord struct {
	ID        string       `json:"id"`
	TenantID  string       `json:"tenant_id"`
	Name      string       `json:"name"`
	Config    LayoutConfig `json:"config"`
	Version   int          `json:"version"`
	IsActive  bool         `json:"is_active"`
	UpdatedBy *string      `json:"updated_by"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

// AudioTrack is one audio stream inside a movie file.
type AudioTrack struct {
	Language string `json:"language"`
	Label    string `json:"label"`
}

// SubtitleTrack is one subtitle file paired with a movie.
type SubtitleTrack struct {
	Language string `json:"language"`
	Label    string `json:"label"`
	Path     string `json:"path"`
}

// Movie is a MASTER catalog entry. It lives in stayin_main and carries no
// filesystem information: the physical file belongs to mst_movie_local on a
// specific local server.
type Movie struct {
	ID              string      `json:"id"`
	TenantID        string      `json:"tenant_id"`
	Slug            string      `json:"slug"`
	Title           string      `json:"title"`
	Year            int         `json:"year"`
	DurationSeconds int         `json:"duration_seconds"`
	Genres          []string    `json:"genres"`
	PosterPath      string      `json:"poster_path"`
	BannerPath      string      `json:"banner_path"`
	IsFeatured      bool        `json:"is_featured"`
	SortOrder       int         `json:"sort_order"`
	Status          MovieStatus `json:"status"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
}

// MovieLocal is the physical copy of a movie on ONE local server. It lives in
// stayin_local and is the only place a real path, size and checksum exist.
type MovieLocal struct {
	ID                string           `json:"id"`
	TenantID          string           `json:"tenant_id"`
	MovieID           string           `json:"movie_id"`
	NodeCode          string           `json:"node_code"`
	Status            LocalMovieStatus `json:"status"`
	Source            MovieSource      `json:"source"`
	ExpectedFilename  string           `json:"expected_filename"`
	FilePath          string           `json:"file_path"`
	FileSizeBytes     int64            `json:"file_size_bytes"`
	ChecksumSHA256    string           `json:"checksum_sha256"`
	Container         string           `json:"container"`
	ProgressBytes     int64            `json:"progress_bytes"`
	Error             string           `json:"error"`
	AssignedAt        time.Time        `json:"assigned_at"`
	TransferStartedAt *time.Time       `json:"transfer_started_at"`
	ReadyAt           *time.Time       `json:"ready_at"`
	ScannedAt         *time.Time       `json:"scanned_at"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

// Playable reports whether the local file can be streamed.
func (m MovieLocal) Playable() bool { return m.Status == LocalReady && m.FilePath != "" }

// MovieWithLocal joins a master catalog entry with its state on a local server.
// It is the shape the CMS uses so one screen can show both what the operator
// authored centrally and what the ship actually has.
type MovieWithLocal struct {
	Movie Movie       `json:"movie"`
	Local *MovieLocal `json:"local"`
}

// MovieLocalUpsertRequest is what the local server reports back to the main
// server after a scan or a transfer.
type MovieLocalUpsertRequest struct {
	MovieID          string           `json:"movie_id"`
	Status           LocalMovieStatus `json:"status"`
	Source           MovieSource      `json:"source"`
	ExpectedFilename string           `json:"expected_filename"`
	FilePath         string           `json:"file_path"`
	FileSizeBytes    int64            `json:"file_size_bytes"`
	ChecksumSHA256   string           `json:"checksum_sha256"`
	Container        string           `json:"container"`
	ProgressBytes    int64            `json:"progress_bytes"`
	Error            string           `json:"error"`
}

// MovieDetail is the rich metadata row, one to one with Movie.
type MovieDetail struct {
	MovieID          string          `json:"movie_id"`
	Synopsis         string          `json:"synopsis"`
	Tagline          string          `json:"tagline"`
	Director         string          `json:"director"`
	Cast             []string        `json:"cast"`
	Country          string          `json:"country"`
	OriginalLanguage string          `json:"original_language"`
	AgeRating        string          `json:"age_rating"`
	Quality          string          `json:"quality"`
	AudioTracks      []AudioTrack    `json:"audio_tracks"`
	Subtitles        []SubtitleTrack `json:"subtitles"`
	MetadataSource   string          `json:"metadata_source"`
	Raw              json.RawMessage `json:"raw"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// Announcement is a running text message shown on TVs.
type Announcement struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenant_id"`
	Message    string     `json:"message"`
	Severity   string     `json:"severity"`
	ActiveFrom *time.Time `json:"active_from"`
	ActiveTo   *time.Time `json:"active_to"`
	IsActive   bool       `json:"is_active"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// AppRelease is a distributed Android TV package.
type AppRelease struct {
	ID             string    `json:"id"`
	TenantID       *string   `json:"tenant_id"`
	VersionCode    int       `json:"version_code"`
	VersionName    string    `json:"version_name"`
	FilePath       string    `json:"file_path"`
	FileSizeBytes  int64     `json:"file_size_bytes"`
	ChecksumSHA256 string    `json:"checksum_sha256"`
	Changelog      string    `json:"changelog"`
	IsActive       bool      `json:"is_active"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// SyncJob is a queued content or configuration synchronisation task.
type SyncJob struct {
	ID         string          `json:"id"`
	TenantID   string          `json:"tenant_id"`
	NodeID     *string         `json:"node_id"`
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
	Status     SyncJobStatus   `json:"status"`
	BytesTotal int64           `json:"bytes_total"`
	BytesDone  int64           `json:"bytes_done"`
	Error      string          `json:"error"`
	StartedAt  *time.Time      `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
	CreatedAt  time.Time       `json:"created_at"`
}

// RemoteCommand is an instruction addressed to one device.
type RemoteCommand struct {
	ID       string          `json:"id"`
	TenantID string          `json:"tenant_id"`
	DeviceID string          `json:"device_id"`
	Command  string          `json:"command"`
	Payload  json.RawMessage `json:"payload"`
	Status   CommandStatus   `json:"status"`
	IssuedBy *string         `json:"issued_by"`
	IssuedAt time.Time       `json:"issued_at"`
	AckedAt  *time.Time      `json:"acked_at"`
	Error    string          `json:"error"`
}

// WatchLog stores playback progress and completion for analytics.
type WatchLog struct {
	ID              string    `json:"id"`
	TenantID        string    `json:"tenant_id"`
	DeviceID        string    `json:"device_id"`
	MovieID         string    `json:"movie_id"`
	PositionSeconds int       `json:"position_seconds"`
	DurationSeconds int       `json:"duration_seconds"`
	Completed       bool      `json:"completed"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// AuditLog is an append-only record of administrative actions.
type AuditLog struct {
	ID        string          `json:"id"`
	TenantID  *string         `json:"tenant_id"`
	ActorID   *string         `json:"actor_id"`
	ActorRole string          `json:"actor_role"`
	Action    string          `json:"action"`
	Entity    string          `json:"entity"`
	EntityID  string          `json:"entity_id"`
	IP        string          `json:"ip"`
	Metadata  json.RawMessage `json:"metadata"`
	CreatedAt time.Time       `json:"created_at"`
}

// MediaScanRun records one execution of the ingest engine.
type MediaScanRun struct {
	ID           string     `json:"id"`
	TenantID     string     `json:"tenant_id"`
	Trigger      string     `json:"trigger"`
	RootDir      string     `json:"root_dir"`
	Status       string     `json:"status"`
	FilesFound   int        `json:"files_found"`
	FilesAdded   int        `json:"files_added"`
	FilesUpdated int        `json:"files_updated"`
	FilesMissing int        `json:"files_missing"`
	Error        string     `json:"error"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}
