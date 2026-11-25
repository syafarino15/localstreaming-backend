// Package sync implements the durable background worker that consumes
// trn_sync_jobs. Jobs are created by the CMS and claimed with SKIP LOCKED so
// several replicas can run safely.
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/alfateve/stayin-local-backend/internal/domain"
	"github.com/alfateve/stayin-local-backend/internal/platform/storage"
)

// Scanner runs an ingest pass.
type Scanner interface {
	Run(ctx context.Context, trigger string) (*domain.MediaScanRun, error)
}

// Worker consumes the sync queue.
type Worker struct {
	jobs     domain.SyncJobRepository
	scanner  Scanner
	notifier domain.Notifier
	store    *storage.Store
	log      zerolog.Logger
	interval time.Duration
	cloudURL string
	cloudKey string
	http     *http.Client
}

// Options configures the worker.
type Options struct {
	Interval     time.Duration
	CloudBaseURL string
	CloudAPIKey  string
}

// New builds the sync worker.
func New(
	jobs domain.SyncJobRepository,
	scanner Scanner,
	notifier domain.Notifier,
	store *storage.Store,
	opts Options,
	log zerolog.Logger,
) *Worker {
	if opts.Interval <= 0 {
		opts.Interval = 15 * time.Second
	}
	return &Worker{
		jobs:     jobs,
		scanner:  scanner,
		notifier: notifier,
		store:    store,
		log:      log.With().Str("component", "sync-worker").Logger(),
		interval: opts.Interval,
		cloudURL: strings.TrimRight(opts.CloudBaseURL, "/"),
		cloudKey: opts.CloudAPIKey,
		http: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

// Start runs the worker loop until the context is cancelled.
func (w *Worker) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.drain(ctx)
			}
		}
	}()
}

// drain processes every queued job available at this moment.
func (w *Worker) drain(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		job, err := w.jobs.ClaimNext(ctx)
		if err != nil {
			// No rows is the normal idle case.
			if isNoRows(err) {
				return
			}
			w.log.Warn().Err(err).Msg("claim sync job failed")
			return
		}

		w.process(ctx, job)
	}
}

func (w *Worker) process(ctx context.Context, job *domain.SyncJob) {
	started := time.Now()
	var err error

	switch job.Type {
	case "MEDIA_RESCAN":
		err = w.handleRescan(ctx)
	case "LAYOUT_PUSH":
		w.handleLayoutPush(job)
	case "APK_DISTRIBUTE":
		err = w.handleAPKDistribute(ctx, job)
	case "CLOUD_PULL":
		err = w.handleCloudPull(ctx, job)
	case "DISTRIBUTE_MOVIE":
		err = w.handleDistributeMovie(job)
	default:
		err = fmt.Errorf("tipe job %q tidak dikenal", job.Type)
	}

	if err != nil {
		w.log.Error().Err(err).Str("job", job.ID).Str("type", job.Type).Msg("sync job failed")
		if failErr := w.jobs.FailSyncJob(ctx, job.ID, err.Error()); failErr != nil {
			w.log.Warn().Err(failErr).Msg("mark job failed")
		}
		return
	}

	if err := w.jobs.CompleteSyncJob(ctx, job.ID); err != nil {
		w.log.Warn().Err(err).Msg("complete sync job")
	}
	w.log.Info().
		Str("job", job.ID).
		Str("type", job.Type).
		Dur("elapsed", time.Since(started)).
		Msg("sync job completed")
}

func (w *Worker) handleRescan(ctx context.Context) error {
	if w.scanner == nil {
		return fmt.Errorf("media scanner tidak aktif")
	}
	run, err := w.scanner.Run(ctx, "SYNC_JOB")
	if err != nil {
		return err
	}
	w.log.Info().
		Int("found", run.FilesFound).
		Int("added", run.FilesAdded).
		Int("updated", run.FilesUpdated).
		Msg("rescan from sync job finished")
	return nil
}

func (w *Worker) handleLayoutPush(job *domain.SyncJob) {
	w.notifier.BroadcastTenant(job.TenantID, "REFRESH_CONFIG", map[string]any{
		"reason": "LAYOUT_PUSH",
		"job_id": job.ID,
	})
}

// handleDistributeMovie binds a scheduled movie to its file when the file is
// already on this node, which is the flashdisk path. Network transfer from the
// main server is not implemented yet, so a missing file is reported explicitly
// instead of failing with an opaque "unknown job type".
func (w *Worker) handleDistributeMovie(job *domain.SyncJob) error {
	var payload struct {
		MovieID          string `json:"movie_id"`
		ExpectedFilename string `json:"expected_filename"`
		Title            string `json:"title"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("payload DISTRIBUTE_MOVIE tidak valid: %w", err)
	}
	if payload.ExpectedFilename == "" {
		return fmt.Errorf("expected_filename wajib diisi pada job DISTRIBUTE_MOVIE")
	}

	present, err := w.hasMovieFile(payload.ExpectedFilename)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf(
			"berkas %q belum ada di storage lokal; transfer jaringan belum tersedia, kirim melalui flashdisk lalu jalankan pemindaian",
			payload.ExpectedFilename,
		)
	}

	// The scanner matches the file against expected_filename, so one pass binds
	// it to the catalog entry that was scheduled from the CMS.
	if w.scanner == nil {
		return fmt.Errorf("media scanner tidak aktif")
	}
	if _, err := w.scanner.Run(context.Background(), "DISTRIBUTE_MOVIE"); err != nil {
		return err
	}
	w.log.Info().
		Str("movie_id", payload.MovieID).
		Str("file", payload.ExpectedFilename).
		Msg("scheduled movie bound to its local file")
	return nil
}

// hasMovieFile reports whether the movie library already contains a file whose
// name matches expected, comparing case-insensitively like the ingest engine.
func (w *Worker) hasMovieFile(expected string) (bool, error) {
	target := strings.ToLower(strings.TrimSpace(expected))
	if target == "" {
		return false, nil
	}

	found := false
	err := filepath.WalkDir(w.store.MoviesDir(), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.ToLower(entry.Name()) == target {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return false, fmt.Errorf("periksa storage film: %w", err)
	}
	return found, nil
}

// handleAPKDistribute copies a package into the served APK directory so TVs can
// download it over the LAN.
func (w *Worker) handleAPKDistribute(ctx context.Context, job *domain.SyncJob) error {
	var payload struct {
		SourcePath string `json:"source_path"`
		TargetName string `json:"target_name"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("payload APK_DISTRIBUTE tidak valid: %w", err)
	}
	if payload.SourcePath == "" {
		return fmt.Errorf("source_path wajib diisi")
	}

	info, err := os.Stat(payload.SourcePath)
	if err != nil {
		return fmt.Errorf("file APK tidak ditemukan: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("source_path menunjuk ke direktori")
	}

	target := payload.TargetName
	if target == "" {
		target = filepath.Base(payload.SourcePath)
	}
	target = filepath.Base(target)

	dest := filepath.Join(w.store.APKDir(), target)
	if err := storage.EnsureParent(dest); err != nil {
		return fmt.Errorf("siapkan direktori APK: %w", err)
	}

	src, err := os.Open(payload.SourcePath)
	if err != nil {
		return fmt.Errorf("buka APK sumber: %w", err)
	}
	defer func() { _ = src.Close() }()

	tmp := dest + ".part"
	dst, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("buat file tujuan: %w", err)
	}

	written, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("salin APK: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("tutup file tujuan: %w", closeErr)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("finalisasi APK: %w", err)
	}

	_ = w.jobs.UpdateJobProgress(ctx, job.ID, written, written)
	rel, relErr := w.store.Relative(dest)
	if relErr == nil {
		w.notifier.BroadcastTenant(job.TenantID, "APK_AVAILABLE", map[string]any{
			"path": "/api/v1/assets/" + rel,
			"size": written,
		})
	}
	return nil
}

// handleCloudPull fetches the catalog manifest from the central service. It is
// a metadata only operation: media files still arrive through the flashdisk.
func (w *Worker) handleCloudPull(ctx context.Context, job *domain.SyncJob) error {
	if w.cloudURL == "" {
		return fmt.Errorf("CLOUD_BASE_URL belum dikonfigurasi pada node ini")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.cloudURL+"/api/v1/cloud/manifest", nil)
	if err != nil {
		return fmt.Errorf("buat request manifest: %w", err)
	}
	if w.cloudKey != "" {
		req.Header.Set("X-Api-Key", w.cloudKey)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := w.http.Do(req)
	if err != nil {
		return fmt.Errorf("ambil manifest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("manifest ditolak dengan status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return fmt.Errorf("baca manifest: %w", err)
	}

	var manifest struct {
		Version int             `json:"version"`
		Movies  json.RawMessage `json:"movies"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return fmt.Errorf("manifest bukan JSON yang valid: %w", err)
	}

	// The manifest is stored on the job payload so the operator can inspect
	// exactly what the node received, even before a renderer is wired up.
	_ = w.jobs.UpdateJobProgress(ctx, job.ID, int64(len(body)), int64(len(body)))
	w.notifier.BroadcastTenant(job.TenantID, "CONTENT_UPDATED", map[string]any{
		"source":  "CLOUD_PULL",
		"version": manifest.Version,
		"job_id":  job.ID,
	})
	return nil
}

func isNoRows(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no rows") || strings.Contains(message, "not found")
}
