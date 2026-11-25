package http

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"github.com/alfateve/stayin-local-backend/internal/domain"
	"github.com/alfateve/stayin-local-backend/internal/platform/httpx"
	"github.com/alfateve/stayin-local-backend/internal/platform/storage"
	"github.com/alfateve/stayin-local-backend/internal/usecase"
	"github.com/alfateve/stayin-local-backend/pkg/response"
)

// TVHandler serves every endpoint the Android TV client calls.
type TVHandler struct {
	tv           *usecase.TV
	store        *storage.Store
	pool         *httpx.BufferPool
	log          zerolog.Logger
	cacheSeconds int
}

// NewTVHandler builds the TV handler.
func NewTVHandler(tv *usecase.TV, store *storage.Store, pool *httpx.BufferPool, cacheSeconds int, log zerolog.Logger) *TVHandler {
	if cacheSeconds <= 0 {
		cacheSeconds = 86400
	}
	return &TVHandler{
		tv:           tv,
		store:        store,
		pool:         pool,
		log:          log.With().Str("handler", "tv").Logger(),
		cacheSeconds: cacheSeconds,
	}
}

// Pair binds a TV to a room using the code entered by the crew.
func (h *TVHandler) Pair(c *gin.Context) {
	var req domain.DevicePairRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload pairing tidak valid",
			response.FieldError{Field: "body", Message: err.Error()})
		return
	}

	result, err := h.tv.Pair(c.Request.Context(), req)
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "perangkat berhasil dipasangkan", result)
}

// Init returns the full boot payload.
func (h *TVHandler) Init(c *gin.Context) {
	device := DeviceFrom(c)
	if device == nil {
		response.Unauthorized(c, "perangkat tidak terautentikasi")
		return
	}

	payload, err := h.tv.Init(c.Request.Context(), device.DeviceUID, c.ClientIP())
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "konfigurasi perangkat berhasil dimuat", payload)
}

// Movie returns one catalog entry with metadata.
func (h *TVHandler) Movie(c *gin.Context) {
	device := DeviceFrom(c)
	if device == nil {
		response.Unauthorized(c, "perangkat tidak terautentikasi")
		return
	}

	detail, err := h.tv.Movie(c.Request.Context(), device.DeviceUID, c.Param("movieID"))
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "detail film ditemukan", detail)
}

// Heartbeat records presence.
func (h *TVHandler) Heartbeat(c *gin.Context) {
	device := DeviceFrom(c)
	if device == nil {
		response.Unauthorized(c, "perangkat tidak terautentikasi")
		return
	}

	var req domain.HeartbeatRequest
	_ = c.ShouldBindJSON(&req)

	if err := h.tv.Heartbeat(c.Request.Context(), device.DeviceUID, req, c.ClientIP()); err != nil {
		MapError(c, err)
		return
	}
	response.NoContent(c, "heartbeat diterima")
}

// SaveWatchLog stores resume progress.
func (h *TVHandler) SaveWatchLog(c *gin.Context) {
	device := DeviceFrom(c)
	if device == nil {
		response.Unauthorized(c, "perangkat tidak terautentikasi")
		return
	}

	var req domain.WatchLogRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "payload watch log tidak valid",
			response.FieldError{Field: "body", Message: err.Error()})
		return
	}

	if err := h.tv.SaveWatchLog(c.Request.Context(), device.DeviceUID, req); err != nil {
		MapError(c, err)
		return
	}
	response.NoContent(c, "progres tontonan tersimpan")
}

// Resume returns the Continue Watching list.
func (h *TVHandler) Resume(c *gin.Context) {
	device := DeviceFrom(c)
	if device == nil {
		response.Unauthorized(c, "perangkat tidak terautentikasi")
		return
	}

	cards, err := h.tv.ResumePoints(c.Request.Context(), device.DeviceUID)
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "daftar lanjut tonton dimuat", gin.H{"items": cards})
}

// PendingCommands lets a TV drain the queue after reconnecting.
func (h *TVHandler) PendingCommands(c *gin.Context) {
	device := DeviceFrom(c)
	if device == nil {
		response.Unauthorized(c, "perangkat tidak terautentikasi")
		return
	}

	commands, err := h.tv.PendingCommands(c.Request.Context(), device.DeviceUID)
	if err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "antrean perintah dimuat", gin.H{"items": commands})
}

// AckCommand records execution of a remote command.
func (h *TVHandler) AckCommand(c *gin.Context) {
	device := DeviceFrom(c)
	if device == nil {
		response.Unauthorized(c, "perangkat tidak terautentikasi")
		return
	}

	if err := h.tv.AckCommand(c.Request.Context(), device.DeviceUID, c.Param("commandID")); err != nil {
		MapError(c, err)
		return
	}
	response.NoContent(c, "perintah dikonfirmasi")
}

// Stream serves a movie file with full byte-range support.
//
// This is the hot path: it never buffers a whole film, honours Range so the
// player can seek instantly, and recycles buffers so many concurrent TVs do not
// allocate a fresh buffer each.
func (h *TVHandler) Stream(c *gin.Context) {
	movieID := c.Param("movieID")

	movie, absPath, err := h.tv.StreamTarget(c.Request.Context(), movieID)
	if err != nil {
		MapError(c, err)
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		h.log.Warn().Err(err).Str("movie", movieID).Msg("stream file missing")
		response.NotFound(c, "berkas film tidak ditemukan pada storage")
		return
	}

	contentType := mime.TypeByExtension(filepath.Ext(absPath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	content := httpx.Content{
		Name:         filepath.Base(absPath),
		ContentType:  contentType,
		Size:         info.Size(),
		ModTime:      info.ModTime(),
		ETag:         fmt.Sprintf(`"%s-%d-%d"`, movie.Movie.ID, info.Size(), info.ModTime().Unix()),
		CacheSeconds: h.cacheSeconds,
	}

	opener := func() (io.ReadCloser, error) {
		file, _, release, openErr := h.store.Open(movie.Local.FilePath)
		if openErr != nil {
			return nil, openErr
		}
		return &releasedFile{ReadSeekCloser: file, release: release}, nil
	}

	if err := httpx.Serve(c.Writer, c.Request, content, opener, h.pool); err != nil {
		// Headers are already flushed at this point; only logging is possible.
		h.log.Warn().
			Err(err).
			Str("movie", movieID).
			Str("ip", c.ClientIP()).
			Msg("stream interrupted")
	}
}

// Asset serves posters, banners and APK files from the media root.
func (h *TVHandler) Asset(c *gin.Context) {
	relative := strings.TrimPrefix(c.Param("path"), "/")
	if relative == "" {
		response.NotFound(c, "aset tidak ditemukan")
		return
	}

	abs, err := h.store.Resolve(relative)
	if err != nil {
		response.Forbidden(c, "path aset tidak diizinkan")
		return
	}

	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		response.NotFound(c, "aset tidak ditemukan")
		return
	}

	contentType := mime.TypeByExtension(filepath.Ext(abs))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	content := httpx.Content{
		Name:         filepath.Base(abs),
		ContentType:  contentType,
		Size:         info.Size(),
		ModTime:      info.ModTime(),
		ETag:         fmt.Sprintf(`"%x-%d"`, info.ModTime().UnixNano(), info.Size()),
		CacheSeconds: h.cacheSeconds,
	}

	opener := func() (io.ReadCloser, error) {
		file, _, release, openErr := h.store.Open(relative)
		if openErr != nil {
			return nil, openErr
		}
		return &releasedFile{ReadSeekCloser: file, release: release}, nil
	}

	if err := httpx.Serve(c.Writer, c.Request, content, opener, h.pool); err != nil {
		h.log.Warn().Err(err).Str("asset", relative).Msg("asset stream interrupted")
	}
}

// Health is the liveness and readiness probe. Every database the process
// opened must answer, so a combined deployment reports a single status.
func Health(pingers []Pinger, startedAt time.Time, mode string) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()

		dbStatus := "ok"
		for _, pinger := range pingers {
			if pinger == nil {
				continue
			}
			if err := pinger.Ping(ctx); err != nil {
				dbStatus = "down"
				break
			}
		}

		status := http.StatusOK
		if dbStatus != "ok" {
			status = http.StatusServiceUnavailable
		}

		c.JSON(status, gin.H{
			"success": dbStatus == "ok",
			"message": "stayin server",
			"data": gin.H{
				"database":       dbStatus,
				"mode":           mode,
				"uptime_seconds": int64(time.Since(startedAt).Seconds()),
				"server_time":    time.Now().UTC(),
			},
		})
	}
}

// Pinger is the health check contract.
type Pinger interface {
	Ping(ctx context.Context) error
}

// releasedFile returns the storage slot when the reader is closed.
type releasedFile struct {
	io.ReadSeekCloser
	release func()
}

func (r *releasedFile) Close() error {
	err := r.ReadSeekCloser.Close()
	if r.release != nil {
		r.release()
	}
	return err
}
