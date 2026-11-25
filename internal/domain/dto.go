package domain

import "time"

// Pagination is the shared list paging input.
type Pagination struct {
	Page  int
	Limit int
}

// Offset converts the page into a SQL offset.
func (p Pagination) Offset() int {
	page := p.Page
	if page < 1 {
		page = 1
	}
	return (page - 1) * p.NormalizedLimit()
}

// NormalizedLimit clamps the limit to a sane window.
func (p Pagination) NormalizedLimit() int {
	switch {
	case p.Limit <= 0:
		return 20
	case p.Limit > 200:
		return 200
	default:
		return p.Limit
	}
}

// MovieFilter narrows a master catalog query on the main server.
type MovieFilter struct {
	TenantID string
	Statuses []MovieStatus
	Genre    string
	Search   string
	Featured *bool
	Page     int
	Limit    int
}

// MovieLocalFilter narrows a local inventory query.
type MovieLocalFilter struct {
	TenantID string
	Statuses []LocalMovieStatus
	Source   MovieSource
	Search   string
	Page     int
	Limit    int
}

// DeviceFilter narrows a device query.
type DeviceFilter struct {
	TenantID string
	RoomID   string
	Status   string
	Search   string
	Page     int
	Limit    int
}

// LoginRequest is the admin credential payload.
type LoginRequest struct {
	Email    string `json:"email" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// LoginResponse carries the issued tokens.
type LoginResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	User         User   `json:"user"`
}

// AuthClaims is the identity attached to an authenticated request.
type AuthClaims struct {
	UserID   string
	TenantID string
	Role     Role
	Email    string
}

// DevicePairRequest is sent by a TV that displays a pairing code.
type DevicePairRequest struct {
	DeviceUID   string `json:"device_uid"`
	PairingCode string `json:"pairing_code"`
	Model       string `json:"model"`
	AppVersion  string `json:"app_version"`
}

// DevicePairResponse returns the long lived device credential.
type DevicePairResponse struct {
	DeviceToken string       `json:"device_token"`
	DeviceID    string       `json:"device_id"`
	TenantID    string       `json:"tenant_id"`
	Status      DeviceStatus `json:"status"`
	RoomID      *string      `json:"room_id"`
}

// HeartbeatRequest is the periodic status ping from a TV.
type HeartbeatRequest struct {
	AppVersion   string `json:"app_version"`
	PlayingMovie string `json:"playing_movie_id"`
	State        string `json:"state"`
}

// RoomInfo is the per-device personalisation block.
type RoomInfo struct {
	RoomNumber      string     `json:"room_number"`
	Deck            string     `json:"deck"`
	RoomType        string     `json:"room_type"`
	IsExclusive     bool       `json:"is_exclusive"`
	GuestName       string     `json:"guest_name"`
	GuestSalutation string     `json:"guest_salutation"`
	GuestCheckinAt  *time.Time `json:"guest_checkin_at"`
	GuestCheckoutAt *time.Time `json:"guest_checkout_at"`
	Greeting        string     `json:"greeting"`
}

// MovieCard is the compact catalog representation used by the TV.
type MovieCard struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Year            int      `json:"year"`
	DurationSeconds int      `json:"duration_seconds"`
	Genres          []string `json:"genres"`
	PosterURL       string   `json:"poster_url"`
	BannerURL       string   `json:"banner_url"`
	StreamURL       string   `json:"stream_url"`
	Source          string   `json:"source"`
	IsFeatured      bool     `json:"is_featured"`
	HasSubtitle     bool     `json:"has_subtitle"`
}

// MovieDetailView is the detail screen representation.
type MovieDetailView struct {
	MovieCard
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
	FileSizeBytes    int64           `json:"file_size_bytes"`
	Container        string          `json:"container"`
	ChecksumSHA256   string          `json:"checksum_sha256"`
}

// CatalogRowView is one rendered row of the catalog.
type CatalogRowView struct {
	Genre        string      `json:"genre"`
	DisplayStyle string      `json:"display_style"`
	Movies       []MovieCard `json:"movies"`
}

// CatalogView is the whole catalog payload for a device.
type CatalogView struct {
	Featured *MovieCard       `json:"featured"`
	Rows     []CatalogRowView `json:"rows"`
	Genres   []string         `json:"genres"`
	Total    int              `json:"total"`
}

// AnnouncementView is the compact announcement sent to devices.
type AnnouncementView struct {
	ID       string `json:"id"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

// ServerInfo tells the TV where it is connected and how fresh the data is.
type ServerInfo struct {
	NodeCode       string    `json:"node_code"`
	AppMode        string    `json:"app_mode"`
	ServerTime     time.Time `json:"server_time"`
	HeartbeatSecs  int       `json:"heartbeat_interval_seconds"`
	CatalogVersion int       `json:"catalog_version"`
	StreamingMode  string    `json:"streaming_mode"`
}

// TVInitResponse is the single payload that boots the TV application.
type TVInitResponse struct {
	Device        Device             `json:"device"`
	Room          *RoomInfo          `json:"room"`
	Tenant        Tenant             `json:"tenant"`
	Layout        LayoutConfig       `json:"layout_config"`
	Catalog       CatalogView        `json:"catalog"`
	Announcements []AnnouncementView `json:"announcements"`
	Server        ServerInfo         `json:"server"`
}

// WatchLogRequest reports playback progress from a TV.
type WatchLogRequest struct {
	MovieID         string `json:"movie_id" binding:"required"`
	PositionSeconds int    `json:"position_seconds"`
	DurationSeconds int    `json:"duration_seconds"`
	Completed       bool   `json:"completed"`
}

// CommandRequest is the admin payload for a remote command.
type CommandRequest struct {
	Command string         `json:"command" binding:"required"`
	Payload map[string]any `json:"payload"`
}

// GuestCheckinRequest sets the VIP guest for a room.
type GuestCheckinRequest struct {
	GuestName       string     `json:"guest_name" binding:"required"`
	GuestSalutation string     `json:"guest_salutation"`
	CheckinAt       *time.Time `json:"checkin_at"`
	CheckoutAt      *time.Time `json:"checkout_at"`
}

// MovieUpsertRequest is the CMS metadata form payload for an existing entry.
type MovieUpsertRequest struct {
	Title            string          `json:"title"`
	Slug             string          `json:"slug"`
	Year             int             `json:"year"`
	DurationSeconds  int             `json:"duration_seconds"`
	Genres           []string        `json:"genres"`
	Synopsis         string          `json:"synopsis"`
	Tagline          string          `json:"tagline"`
	Director         string          `json:"director"`
	Cast             []string        `json:"cast"`
	Country          string          `json:"country"`
	OriginalLanguage string          `json:"original_language"`
	AgeRating        string          `json:"age_rating"`
	Quality          string          `json:"quality"`
	IsFeatured       bool            `json:"is_featured"`
	SortOrder        int             `json:"sort_order"`
	Status           MovieStatus     `json:"status"`
	AudioTracks      []AudioTrack    `json:"audio_tracks"`
	Subtitles        []SubtitleTrack `json:"subtitles"`
}

// MovieCreateRequest registers a new master catalog entry from the CMS.
//
// No video file is uploaded here. The entry is created as PENDING and waits
// for either a network transfer to the ship or a flashdisk delivery. The
// expected filename tells the local ingest engine which physical file to bind
// to this catalog row when it eventually appears in storage.
type MovieCreateRequest struct {
	Title            string          `json:"title" binding:"required"`
	Slug             string          `json:"slug"`
	Year             int             `json:"year"`
	DurationSeconds  int             `json:"duration_seconds"`
	Genres           []string        `json:"genres"`
	Synopsis         string          `json:"synopsis"`
	Tagline          string          `json:"tagline"`
	Director         string          `json:"director"`
	Cast             []string        `json:"cast"`
	Country          string          `json:"country"`
	OriginalLanguage string          `json:"original_language"`
	AgeRating        string          `json:"age_rating"`
	Quality          string          `json:"quality"`
	IsFeatured       bool            `json:"is_featured"`
	SortOrder        int             `json:"sort_order"`
	AudioTracks      []AudioTrack    `json:"audio_tracks"`
	Subtitles        []SubtitleTrack `json:"subtitles"`
	ExpectedFilename string          `json:"expected_filename"`
	Publish          bool            `json:"publish"`
	// TenantID is only read for a super admin. A tenant admin always writes to
	// its own tenant, taken from the JWT.
	TenantID string `json:"tenant_id"`
}

// AdminOverview is the super admin landing dashboard. Counters are merged from
// the main database (authoring) and the local database (operations) by the
// usecase, never by a cross-database SQL query.
type AdminOverview struct {
	TotalTenants     int64 `json:"total_tenants"`
	ActiveTenants    int64 `json:"active_tenants"`
	TotalNodes       int64 `json:"total_nodes"`
	OnlineNodes      int64 `json:"online_nodes"`
	TotalDevices     int64 `json:"total_devices"`
	OnlineDevices    int64 `json:"online_devices"`
	TotalMovies      int64 `json:"total_movies"`
	PendingMovies    int64 `json:"pending_movies"`
	ReadyMovies      int64 `json:"ready_movies"`
	TotalWatchLogs   int64 `json:"total_watch_logs"`
	StorageUsedBytes int64 `json:"storage_used_bytes"`
	LocalDBConnected bool  `json:"local_db_connected"`
}

// TenantOverview is the partner landing dashboard.
type TenantOverview struct {
	TotalRooms          int64 `json:"total_rooms"`
	TotalDevices        int64 `json:"total_devices"`
	OnlineDevices       int64 `json:"online_devices"`
	TotalMovies         int64 `json:"total_movies"`
	ReadyMovies         int64 `json:"ready_movies"`
	PendingLocalMovies  int64 `json:"pending_local_movies"`
	ActiveAnnouncements int64 `json:"active_announcements"`
	StorageUsedBytes    int64 `json:"storage_used_bytes"`
	LocalDBConnected    bool  `json:"local_db_connected"`
}

// TrendPoint is one bucket of a time series.
type TrendPoint struct {
	Date  string `json:"date"`
	Count int64  `json:"count"`
}

// TopMovie is a ranked catalog entry for dashboards.
type TopMovie struct {
	MovieID      string `json:"movie_id"`
	Title        string `json:"title"`
	WatchCount   int64  `json:"watch_count"`
	TotalSeconds int64  `json:"total_seconds"`
}

// LayoutPublishRequest activates a layout version.
type LayoutPublishRequest struct {
	Config LayoutConfig `json:"config" binding:"required"`
}

// RoomUpsertRequest is the room form payload.
type RoomUpsertRequest struct {
	RoomNumber  string `json:"room_number" binding:"required"`
	Deck        string `json:"deck"`
	RoomType    string `json:"room_type"`
	IsExclusive bool   `json:"is_exclusive"`
}

// AnnouncementUpsertRequest is the announcement form payload.
type AnnouncementUpsertRequest struct {
	Message    string     `json:"message" binding:"required"`
	Severity   string     `json:"severity"`
	ActiveFrom *time.Time `json:"active_from"`
	ActiveTo   *time.Time `json:"active_to"`
	IsActive   *bool      `json:"is_active"`
}

// TenantUpsertRequest is the tenant form payload.
type TenantUpsertRequest struct {
	Slug         string         `json:"slug" binding:"required"`
	Name         string         `json:"name" binding:"required"`
	Branding     map[string]any `json:"branding"`
	LicenseQuota int            `json:"license_quota"`
}

// UserUpsertRequest is the admin account form payload.
type UserUpsertRequest struct {
	Email    string `json:"email" binding:"required"`
	Password string `json:"password"`
	FullName string `json:"full_name" binding:"required"`
	Role     Role   `json:"role" binding:"required"`
	TenantID string `json:"tenant_id"`
	Status   string `json:"status"`
}

// SyncJobRequest enqueues a synchronisation task.
type SyncJobRequest struct {
	Type    string         `json:"type" binding:"required"`
	NodeID  string         `json:"node_id"`
	Payload map[string]any `json:"payload"`
}

// MediaScanRequest triggers a manual ingest run.
type MediaScanRequest struct {
	Trigger string `json:"trigger"`
}
