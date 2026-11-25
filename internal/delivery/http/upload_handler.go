package http

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/alfateve/stayin-local-backend/internal/domain"
	"github.com/alfateve/stayin-local-backend/internal/platform/storage"
	"github.com/alfateve/stayin-local-backend/pkg/response"
)

// Uploader stores multipart files inside the managed media tree.
type Uploader struct {
	store    *storage.Store
	maxBytes int64
}

// NewUploader builds the upload helper.
func NewUploader(store *storage.Store, maxBytes int64) *Uploader {
	if maxBytes <= 0 {
		maxBytes = 20 << 20
	}
	return &Uploader{store: store, maxBytes: maxBytes}
}

// savedFile is the outcome of one upload.
type savedFile struct {
	RelPath string
	Size    int64
	Sum     string
	Name    string
}

// save persists a multipart file into dir with a sanitised, prefixed name.
func (u *Uploader) save(c *gin.Context, field, dir, prefix string, allowed map[string]bool) (*savedFile, error) {
	header, err := c.FormFile(field)
	if err != nil {
		return nil, fmt.Errorf("berkas %q wajib diunggah", field)
	}
	if header.Size <= 0 {
		return nil, fmt.Errorf("berkas %q kosong", field)
	}
	if header.Size > u.maxBytes {
		return nil, fmt.Errorf("berkas melebihi batas %d MB", u.maxBytes/(1024*1024))
	}

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if len(allowed) > 0 && !allowed[ext] {
		return nil, fmt.Errorf("ekstensi %s tidak diizinkan", ext)
	}

	name := fmt.Sprintf("%s-%d%s", prefix, timeNowUnix(), ext)
	dest := filepath.Join(dir, name)

	if err := storage.EnsureParent(dest); err != nil {
		return nil, fmt.Errorf("siapkan direktori: %w", err)
	}

	if err := c.SaveUploadedFile(header, dest); err != nil {
		return nil, fmt.Errorf("simpan berkas: %w", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		return nil, fmt.Errorf("baca berkas: %w", err)
	}

	sum, err := digestFile(dest)
	if err != nil {
		return nil, err
	}

	rel, err := u.store.Relative(dest)
	if err != nil {
		_ = os.Remove(dest)
		return nil, fmt.Errorf("hitung path relatif: %w", err)
	}

	return &savedFile{RelPath: rel, Size: info.Size(), Sum: sum, Name: name}, nil
}

// digestFile computes the SHA-256 of a file with a bounded buffer.
func digestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("buka berkas: %w", err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	buf := make([]byte, 256*1024)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", fmt.Errorf("hitung checksum: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

var (
	imageExts = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true}
	apkExts   = map[string]bool{".apk": true}
)

// UploadMovieArtwork handles POST /tenant/movies/:movieID/poster and /banner.
func (h *AdminHandler) UploadMovieArtwork(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims := ClaimsFrom(c)
		if claims == nil {
			response.Unauthorized(c, "sesi tidak valid")
			return
		}

		dir := h.uploader.store.PostersDir()
		prefix := "poster"
		if kind == "banner" {
			dir = h.uploader.store.BannersDir()
			prefix = "banner"
		}

		saved, err := h.uploader.save(c, "file", dir, prefix, imageExts)
		if err != nil {
			response.BadRequest(c, err.Error(), response.FieldError{Field: "file", Message: err.Error()})
			return
		}

		movie, err := h.admin.SetMovieArtwork(c.Request.Context(), claims, c.Param("movieID"), kind, saved.RelPath, c.ClientIP())
		if err != nil {
			_ = os.Remove(filepath.Join(h.uploader.store.Root(), filepath.FromSlash(saved.RelPath)))
			MapError(c, err)
			return
		}

		response.OK(c, "aset film tersimpan", gin.H{
			"movie": movie,
			"asset": gin.H{"path": saved.RelPath, "url": "/api/v1/assets/" + saved.RelPath},
		})
	}
}

// UploadRelease handles POST /admin/releases as multipart.
func (h *AdminHandler) UploadRelease(c *gin.Context) {
	claims := ClaimsFrom(c)
	if claims == nil {
		response.Unauthorized(c, "sesi tidak valid")
		return
	}

	versionCode, err := parseIntField(c, "version_code")
	if err != nil {
		response.BadRequest(c, "version_code tidak valid", response.FieldError{Field: "version_code", Message: err.Error()})
		return
	}
	versionName := strings.TrimSpace(c.PostForm("version_name"))
	if versionName == "" {
		response.BadRequest(c, "version_name wajib diisi", response.FieldError{Field: "version_name", Message: "wajib diisi"})
		return
	}

	saved, err := h.uploader.save(c, "file", h.uploader.store.APKDir(), "dlu-tv", apkExts)
	if err != nil {
		response.BadRequest(c, err.Error(), response.FieldError{Field: "file", Message: err.Error()})
		return
	}

	req := domain.ReleaseUpsertRequest{
		VersionCode: versionCode,
		VersionName: versionName,
		Changelog:   strings.TrimSpace(c.PostForm("changelog")),
		FilePath:    saved.RelPath,
		FileSize:    saved.Size,
		Checksum:    saved.Sum,
	}
	release, err := h.admin.CreateRelease(c.Request.Context(), claims, c.PostForm("tenant_id"), req, c.ClientIP())
	if err != nil {
		_ = os.Remove(filepath.Join(h.uploader.store.Root(), filepath.FromSlash(saved.RelPath)))
		MapError(c, err)
		return
	}

	response.Created(c, "rilis APK tersimpan", gin.H{
		"release":      release,
		"download_url": "/api/v1/assets/" + release.FilePath,
	})
}

// timeNowUnix is used to build unique upload filenames.
func timeNowUnix() int64 { return time.Now().UnixNano() / int64(time.Millisecond) }

// parseIntField reads a numeric multipart field.
func parseIntField(c *gin.Context, field string) (int, error) {
	raw := strings.TrimSpace(c.PostForm(field))
	if raw == "" {
		return 0, fmt.Errorf("wajib diisi")
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("harus berupa angka")
	}
	return value, nil
}
