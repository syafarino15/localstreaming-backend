package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/alfateve/stayin-local-backend/internal/domain"
	"github.com/alfateve/stayin-local-backend/internal/platform/auth"
	"github.com/alfateve/stayin-local-backend/internal/platform/storage"
)

// TV serves every endpoint the Android TV client needs: pairing, boot payload,
// catalog, detail, resume points and remote command acknowledgement.
//
// Every dependency here points at stayin_local. The TV must keep working with
// no internet, so it can never depend on the main database being reachable.
type TV struct {
	devices       domain.DeviceRepository
	rooms         domain.RoomRepository
	tenants       domain.LocalTenantRepository
	layouts       domain.LayoutRepository
	movies        domain.MovieLocalRepository
	announcements domain.AnnouncementRepository
	commands      domain.CommandRepository
	watchLogs     domain.WatchLogRepository
	store         *storage.Store
	notifier      domain.Notifier
	log           zerolog.Logger
	pairingTTL    time.Duration
	nodeCode      string
	appMode       string
	heartbeatSecs int
}

// TVDeps groups the TV usecase dependencies.
type TVDeps struct {
	Devices       domain.DeviceRepository
	Rooms         domain.RoomRepository
	Tenants       domain.LocalTenantRepository
	Layouts       domain.LayoutRepository
	Movies        domain.MovieLocalRepository
	Announcements domain.AnnouncementRepository
	Commands      domain.CommandRepository
	WatchLogs     domain.WatchLogRepository
	Store         *storage.Store
	Notifier      domain.Notifier
	Log           zerolog.Logger
	PairingTTL    time.Duration
	NodeCode      string
	AppMode       string
	HeartbeatSecs int
}

// NewTV builds the TV usecase.
func NewTV(d TVDeps) *TV {
	if d.PairingTTL <= 0 {
		d.PairingTTL = 30 * time.Minute
	}
	if d.HeartbeatSecs <= 0 {
		d.HeartbeatSecs = 30
	}
	return &TV{
		devices:       d.Devices,
		rooms:         d.Rooms,
		tenants:       d.Tenants,
		layouts:       d.Layouts,
		movies:        d.Movies,
		announcements: d.Announcements,
		commands:      d.Commands,
		watchLogs:     d.WatchLogs,
		store:         d.Store,
		notifier:      d.Notifier,
		log:           d.Log.With().Str("usecase", "tv").Logger(),
		pairingTTL:    d.PairingTTL,
		nodeCode:      d.NodeCode,
		appMode:       d.AppMode,
		heartbeatSecs: d.HeartbeatSecs,
	}
}

// Pair binds a TV to a pre-registered device row using the code shown by the
// crew in the CMS. It returns the long lived device credential.
func (u *TV) Pair(ctx context.Context, req domain.DevicePairRequest) (*domain.DevicePairResponse, error) {
	code := strings.TrimSpace(req.PairingCode)
	if code == "" {
		return nil, domain.NewValidation("pairing_code", "kode pairing wajib diisi")
	}
	if req.DeviceUID == "" {
		return nil, domain.NewValidation("device_uid", "device_uid wajib diisi")
	}

	device, err := u.devices.GetByPairingCode(ctx, code)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, fmt.Errorf("pair: kode pairing tidak valid atau kedaluwarsa: %w", domain.ErrUnauthorized)
		}
		return nil, err
	}

	// The same physical TV may already own a credential for another row.
	if existing, lookupErr := u.devices.GetByUID(ctx, req.DeviceUID); lookupErr == nil && existing.ID != device.ID {
		return nil, fmt.Errorf("pair: device_uid sudah terdaftar pada perangkat lain: %w", domain.ErrConflict)
	}

	token, digest, err := auth.NewDeviceToken()
	if err != nil {
		return nil, err
	}

	if err := u.devices.ClaimDevice(ctx, device.ID, req.DeviceUID, digest); err != nil {
		return nil, err
	}

	u.log.Info().
		Str("device_id", device.ID).
		Str("device_uid", req.DeviceUID).
		Str("room_id", derefString(device.RoomID)).
		Msg("device paired")

	roomID := device.RoomID
	return &domain.DevicePairResponse{
		DeviceToken: token,
		DeviceID:    device.ID,
		TenantID:    device.TenantID,
		Status:      domain.DeviceOnline,
		RoomID:      roomID,
	}, nil
}

// Init builds the full boot payload for a device.
func (u *TV) Init(ctx context.Context, deviceID string, ip string) (*domain.TVInitResponse, error) {
	device, err := u.devices.GetByUID(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	if device.Status == domain.DeviceDisabled {
		return nil, fmt.Errorf("init: perangkat dinonaktifkan: %w", domain.ErrForbidden)
	}

	tenant, err := u.tenants.GetTenantByID(ctx, device.TenantID)
	if err != nil {
		return nil, err
	}
	if tenant.Status != "ACTIVE" {
		return nil, fmt.Errorf("init: tenant tidak aktif: %w", domain.ErrForbidden)
	}

	layout, err := u.layoutConfig(ctx, tenant)
	if err != nil {
		return nil, err
	}

	roomInfo, err := u.roomInfo(ctx, device, layout)
	if err != nil {
		return nil, err
	}

	catalog, err := u.buildCatalog(ctx, device.TenantID)
	if err != nil {
		return nil, err
	}

	announcements, err := u.activeAnnouncements(ctx, device.TenantID)
	if err != nil {
		return nil, err
	}

	catalogVersion, err := u.tenants.CatalogVersion(ctx, device.TenantID)
	if err != nil {
		catalogVersion = 0
	}

	_ = u.devices.TouchSeen(ctx, device.ID, device.AppVersion, ip)

	return &domain.TVInitResponse{
		Device:        *device,
		Room:          roomInfo,
		Tenant:        *tenant,
		Layout:        *layout,
		Catalog:       *catalog,
		Announcements: announcements,
		Server: domain.ServerInfo{
			NodeCode:       u.nodeCode,
			AppMode:        u.appMode,
			ServerTime:     time.Now().UTC(),
			HeartbeatSecs:  u.heartbeatSecs,
			CatalogVersion: catalogVersion,
			StreamingMode:  "byte-range",
		},
	}, nil
}

// Movie returns one catalog entry with full metadata.
func (u *TV) Movie(ctx context.Context, deviceID, movieID string) (*domain.MovieDetailView, error) {
	device, err := u.devices.GetByUID(ctx, deviceID)
	if err != nil {
		return nil, err
	}

	playable, detail, err := u.movies.GetPlayable(ctx, device.TenantID, movieID)
	if err != nil {
		return nil, err
	}
	if playable.Local == nil || !playable.Local.Playable() {
		return nil, fmt.Errorf("movie: film belum siap diputar: %w", domain.ErrNotFound)
	}

	view := &domain.MovieDetailView{
		MovieCard:        u.toCard(playable),
		Synopsis:         detail.Synopsis,
		Tagline:          detail.Tagline,
		Director:         detail.Director,
		Cast:             detail.Cast,
		Country:          detail.Country,
		OriginalLanguage: detail.OriginalLanguage,
		AgeRating:        detail.AgeRating,
		Quality:          detail.Quality,
		AudioTracks:      detail.AudioTracks,
		Subtitles:        u.absoluteSubtitles(detail.Subtitles),
		FileSizeBytes:    playable.Local.FileSizeBytes,
		Container:        playable.Local.Container,
		ChecksumSHA256:   playable.Local.ChecksumSHA256,
	}
	return view, nil
}

// Heartbeat records presence.
func (u *TV) Heartbeat(ctx context.Context, deviceID string, req domain.HeartbeatRequest, ip string) error {
	device, err := u.devices.GetByUID(ctx, deviceID)
	if err != nil {
		return err
	}
	return u.devices.TouchSeen(ctx, device.ID, req.AppVersion, ip)
}

// SaveWatchLog stores resume progress and notifies nothing else.
func (u *TV) SaveWatchLog(ctx context.Context, deviceID string, req domain.WatchLogRequest) error {
	device, err := u.devices.GetByUID(ctx, deviceID)
	if err != nil {
		return err
	}
	if req.MovieID == "" {
		return domain.NewValidation("movie_id", "movie_id wajib diisi")
	}

	entry := &domain.WatchLog{
		TenantID:        device.TenantID,
		DeviceID:        device.ID,
		MovieID:         req.MovieID,
		PositionSeconds: req.PositionSeconds,
		DurationSeconds: req.DurationSeconds,
		Completed:       req.Completed,
	}
	return u.watchLogs.UpsertWatchLog(ctx, entry)
}

// ResumePoints returns the Continue Watching list for a device.
func (u *TV) ResumePoints(ctx context.Context, deviceID string) ([]domain.MovieCard, error) {
	device, err := u.devices.GetByUID(ctx, deviceID)
	if err != nil {
		return nil, err
	}

	logs, err := u.watchLogs.ListByDevice(ctx, device.TenantID, device.ID, 12)
	if err != nil {
		return nil, err
	}

	out := make([]domain.MovieCard, 0, len(logs))
	for _, entry := range logs {
		if entry.Completed {
			continue
		}
		playable, _, err := u.movies.GetPlayable(ctx, device.TenantID, entry.MovieID)
		if err != nil || playable.Local == nil {
			continue
		}
		out = append(out, u.toCard(playable))
	}
	return out, nil
}

// PendingCommands lets a reconnecting TV fetch work queued while it was away.
func (u *TV) PendingCommands(ctx context.Context, deviceID string) ([]domain.RemoteCommand, error) {
	device, err := u.devices.GetByUID(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	return u.commands.ListPendingForDevice(ctx, device.ID)
}

// AckCommand records that the TV executed a command.
func (u *TV) AckCommand(ctx context.Context, deviceID, commandID string) error {
	device, err := u.devices.GetByUID(ctx, deviceID)
	if err != nil {
		return err
	}
	cmd, err := u.commands.GetCommandByID(ctx, commandID)
	if err != nil {
		return err
	}
	if cmd.DeviceID != device.ID {
		return fmt.Errorf("ack: perintah bukan milik perangkat ini: %w", domain.ErrForbidden)
	}
	return u.commands.MarkAcked(ctx, commandID)
}

// StreamTarget resolves the local file that backs a movie.
func (u *TV) StreamTarget(ctx context.Context, movieID string) (*domain.MovieWithLocal, string, error) {
	playable, err := u.movies.GetPlayableGlobal(ctx, movieID)
	if err != nil {
		return nil, "", err
	}
	if playable.Local == nil || !playable.Local.Playable() {
		status := domain.LocalPending
		if playable.Local != nil {
			status = playable.Local.Status
		}
		return nil, "", fmt.Errorf("stream: film berstatus %s: %w", status, domain.ErrNotFound)
	}
	abs, err := u.store.Resolve(playable.Local.FilePath)
	if err != nil {
		return nil, "", fmt.Errorf("stream: resolve path: %w", err)
	}
	return playable, abs, nil
}

func titleCaseWords(s string) string {
	parts := strings.Fields(s)
	for i, p := range parts {
		runes := []rune(p)
		if len(runes) > 0 {
			runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
			parts[i] = string(runes)
		}
	}
	return strings.Join(parts, " ")
}

func (u *TV) layoutConfig(ctx context.Context, tenant *domain.Tenant) (*domain.LayoutConfig, error) {
	record, err := u.layouts.GetActive(ctx, tenant.ID)
	if err == nil {
		cfg := record.Config
		cfg.TenantID = tenant.Slug
		if cfg.AppTitle == "" {
			cfg.AppTitle = tenant.Name
		}
		return &cfg, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	// A tenant without a published layout still gets a usable screen.
	return &domain.LayoutConfig{
		TenantID: tenant.Slug,
		AppTitle: tenant.Name,
		WelcomeScreen: domain.WelcomeScreen{
			Enabled:         true,
			LayoutVariant:   "SPLIT_LEFT_CARD",
			PrimaryColor:    "#0F4C81",
			AccentColor:     "#F59E0B",
			CTAText:         "Masuk ke Hiburan",
			DefaultGreeting: "Selamat berlayar. Nikmati hiburan pilihan di kabin {room_number}.",
			ShowRoomInfo:    true,
			ShowClock:       true,
			OverscanPercent: 4,
		},
	}, nil
}

func (u *TV) roomInfo(ctx context.Context, device *domain.Device, layout *domain.LayoutConfig) (*domain.RoomInfo, error) {
	if device.RoomID == nil {
		return nil, nil
	}

	room, err := u.rooms.GetRoomByID(ctx, device.TenantID, *device.RoomID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}

	greeting := substituteGreeting(layout.WelcomeScreen.DefaultGreeting, room)

	return &domain.RoomInfo{
		RoomNumber:      room.RoomNumber,
		Deck:            room.Deck,
		RoomType:        room.RoomType,
		IsExclusive:     room.IsExclusive,
		GuestName:       room.GuestName,
		GuestSalutation: room.GuestSalutation,
		GuestCheckinAt:  room.GuestCheckinAt,
		GuestCheckoutAt: room.GuestCheckoutAt,
		Greeting:        greeting,
	}, nil
}

// substituteGreeting expands the dynamic variables used by the CMS template.
func substituteGreeting(template string, room *domain.Room) string {
	if template == "" {
		template = "Selamat berlayar. Nikmati hiburan di kabin {room_number}."
	}

	guestName := strings.TrimSpace(room.GuestName)
	salutation := strings.TrimSpace(room.GuestSalutation)
	guestSuffix := ""
	fullGuest := ""

	if guestName != "" {
		if salutation != "" {
			fullGuest = salutation + " " + guestName
		} else {
			fullGuest = guestName
		}
		guestSuffix = ", " + fullGuest
	}

	replacer := strings.NewReplacer(
		"{guest_name}", guestName,
		"{guest_full}", fullGuest,
		"{guest_suffix}", guestSuffix,
		"{guest_salutation}", salutation,
		"{room_number}", room.RoomNumber,
		"{deck}", room.Deck,
	)
	return strings.TrimSpace(replacer.Replace(template))
}

// buildCatalog renders the rows the TV displays. Only entries that have a
// READY file on this ship are included, and every genre present gets a row.
func (u *TV) buildCatalog(ctx context.Context, tenantID string) (*domain.CatalogView, error) {
	playable, err := u.movies.ListPlayable(ctx, tenantID, 500)
	if err != nil {
		return nil, err
	}

	cards := make([]domain.MovieCard, 0, len(playable))
	byGenre := make(map[string][]domain.MovieCard, 16)
	var featured *domain.MovieCard

	for i := range playable {
		card := u.toCard(&playable[i])
		cards = append(cards, card)
		if featured == nil && playable[i].Movie.IsFeatured {
			c := card
			featured = &c
		}
		for _, genre := range playable[i].Movie.Genres {
			key := strings.ToLower(strings.TrimSpace(genre))
			if key == "" {
				continue
			}
			byGenre[key] = append(byGenre[key], card)
		}
	}
	if featured == nil && len(cards) > 0 {
		c := cards[0]
		featured = &c
	}

	rows := make([]domain.CatalogRowView, 0, len(byGenre)+1)
	if len(cards) > 0 {
		rows = append(rows, domain.CatalogRowView{
			Genre:        "Trending di Kapal",
			DisplayStyle: "LANDSCAPE_CARD",
			Movies:       cards,
		})
	}

	order := make([]string, 0, len(byGenre))
	for key := range byGenre {
		order = append(order, key)
	}
	sortStrings(order)

	for _, key := range order {
		rows = append(rows, domain.CatalogRowView{
			Genre:        titleCaseWords(key),
			DisplayStyle: "POSTER_PORTRAIT",
			Movies:       byGenre[key],
		})
	}

	genres, err := u.movies.Genres(ctx, tenantID)
	if err != nil {
		genres = []string{}
	}

	return &domain.CatalogView{
		Featured: featured,
		Rows:     rows,
		Genres:   genres,
		Total:    len(cards),
	}, nil
}

func (u *TV) activeAnnouncements(ctx context.Context, tenantID string) ([]domain.AnnouncementView, error) {
	list, err := u.announcements.ListActive(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.AnnouncementView, 0, len(list))
	for _, a := range list {
		out = append(out, domain.AnnouncementView{ID: a.ID, Message: a.Message, Severity: a.Severity})
	}
	return out, nil
}

func (u *TV) toCard(item *domain.MovieWithLocal) domain.MovieCard {
	source := string(domain.SourceCloud)
	if item.Local != nil && item.Local.Source != "" {
		source = string(item.Local.Source)
	}
	return domain.MovieCard{
		ID:              item.Movie.ID,
		Title:           item.Movie.Title,
		Year:            item.Movie.Year,
		DurationSeconds: item.Movie.DurationSeconds,
		Genres:          item.Movie.Genres,
		PosterURL:       u.assetURL(item.Movie.PosterPath),
		BannerURL:       u.assetURL(item.Movie.BannerPath),
		StreamURL:       "/api/v1/stream/" + item.Movie.ID,
		Source:          source,
		IsFeatured:      item.Movie.IsFeatured,
		HasSubtitle:     false,
	}
}

func (u *TV) assetURL(relative string) string {
	if relative == "" {
		return ""
	}
	return "/api/v1/assets/" + strings.TrimPrefix(relative, "/")
}

func (u *TV) absoluteSubtitles(in []domain.SubtitleTrack) []domain.SubtitleTrack {
	out := make([]domain.SubtitleTrack, 0, len(in))
	for _, sub := range in {
		out = append(out, domain.SubtitleTrack{
			Language: sub.Language,
			Label:    sub.Label,
			Path:     u.assetURL(sub.Path),
		})
	}
	return out
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// MarshalCommandPayload keeps the JSON contract stable for the TV client.
func MarshalCommandPayload(payload json.RawMessage) map[string]any {
	out := map[string]any{}
	if len(payload) == 0 {
		return out
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return map[string]any{"raw": string(payload)}
	}
	return out
}

// sortStrings is a tiny insertion sort so the catalog row order is stable
// without pulling in a comparator dependency.
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		current := values[i]
		j := i - 1
		for j >= 0 && values[j] > current {
			values[j+1] = values[j]
			j--
		}
		values[j+1] = current
	}
}

var _ = strconv.Itoa
