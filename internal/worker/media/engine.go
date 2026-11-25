package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alfateve/stayin-local-backend/internal/domain"
	"github.com/alfateve/stayin-local-backend/internal/platform/storage"
)

// Broadcaster receives catalog change notifications. The websocket hub
// implements it so every TV refetches after an ingest run.
type Broadcaster interface {
	BroadcastTenant(tenantID string, event string, payload any) int
}

// Options configures the ingest engine.
type Options struct {
	TenantID    string
	NodeCode    string
	MoviesDir   string
	ComputeHash bool
	HashWorkers int
	MaxDepth    int
	Interval    time.Duration
}

// Engine maps the shared storage directory onto the LOCAL database.
//
// This is the heart of the flashdisk flow: a crew member copies videos into
// MEDIA_ROOT/Movies, the engine discovers them, binds each file to a catalog
// entry and writes a mst_movie_local row with the real path, size and checksum.
//
// When a main database handle is supplied (combined mode) the engine also
// mirrors the catalog entry there so the CMS immediately sees content that
// arrived by flashdisk.
type Engine struct {
	opts    Options
	store   *storage.Store
	locals  domain.MovieLocalRepository
	scans   domain.MediaScanRepository
	tenants domain.LocalTenantRepository
	main    domain.MovieRepository
	hub     Broadcaster
	log     zerolog.Logger

	mu     sync.Mutex
	closed chan struct{}
}

// NewEngine builds the ingest engine.
//
// locals, scans and tenants must be backed by stayin_local. main may be nil
// when this process runs in local-only mode; in that case the CMS learns about
// flashdisk content from the inventory report instead.
func NewEngine(
	opts Options,
	store *storage.Store,
	locals domain.MovieLocalRepository,
	scans domain.MediaScanRepository,
	tenants domain.LocalTenantRepository,
	main domain.MovieRepository,
	hub Broadcaster,
	log zerolog.Logger,
) *Engine {
	if opts.HashWorkers < 1 {
		opts.HashWorkers = 1
	}
	if opts.MaxDepth < 1 {
		opts.MaxDepth = 6
	}
	return &Engine{
		opts:    opts,
		store:   store,
		locals:  locals,
		scans:   scans,
		tenants: tenants,
		main:    main,
		hub:     hub,
		log:     log.With().Str("component", "media-engine").Logger(),
		closed:  make(chan struct{}),
	}
}

// Start runs an immediate scan when configured, then scans on the interval.
func (e *Engine) Start(ctx context.Context, runOnStart bool) {
	if runOnStart {
		go func() {
			if _, err := e.Run(ctx, "STARTUP"); err != nil {
				e.log.Error().Err(err).Msg("startup scan failed")
			}
		}()
	}
	if e.opts.Interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(e.opts.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-e.closed:
				return
			case <-ticker.C:
				if _, err := e.Run(ctx, "SCHEDULED"); err != nil {
					e.log.Error().Err(err).Msg("scheduled scan failed")
				}
			}
		}
	}()
}

// Close stops the periodic scanner.
func (e *Engine) Close() {
	select {
	case <-e.closed:
	default:
		close(e.closed)
	}
}

// Run executes one full ingest pass. It is safe to call concurrently with the
// periodic scan: a mutex serialises the passes.
func (e *Engine) Run(ctx context.Context, trigger string) (*domain.MediaScanRun, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	run := &domain.MediaScanRun{
		TenantID: e.opts.TenantID,
		Trigger:  trigger,
		RootDir:  e.opts.MoviesDir,
	}
	if err := e.scans.StartScan(ctx, run); err != nil {
		return nil, err
	}

	started := time.Now()
	files, err := Scan(e.opts.MoviesDir, e.opts.MaxDepth)
	if err != nil {
		run.Status = "FAILED"
		run.Error = err.Error()
		_ = e.scans.FinishScan(ctx, run)
		return run, err
	}

	e.log.Info().Int("files", len(files)).Str("root", e.opts.MoviesDir).Msg("ingest scan started")

	seen := make(map[string]struct{}, len(files))
	checksumJobs := make([]checksumJob, 0, len(files))

	for _, file := range files {
		relPath := e.store.MovieRel(file.RelPath)
		seen[relPath] = struct{}{}

		movieID, matched, lookupErr := e.resolveMovie(ctx, relPath, file)
		if lookupErr != nil {
			e.log.Warn().Err(lookupErr).Str("file", relPath).Msg("resolve catalog entry failed")
			run.Error = lookupErr.Error()
			continue
		}

		movie := &domain.Movie{
			ID:              movieID,
			TenantID:        e.opts.TenantID,
			Slug:            Slug(file.Metadata.Title, file.Metadata.Year),
			Title:           file.Metadata.Title,
			Year:            file.Metadata.Year,
			DurationSeconds: file.Metadata.DurationSeconds,
			Genres:          file.Metadata.Genres,
			PosterPath:      e.assetPath(file.PosterRel),
			BannerPath:      e.assetPath(file.BannerRel),
			Status:          domain.MoviePublished,
		}

		detail := &domain.MovieDetail{
			Synopsis:         file.Metadata.Synopsis,
			Tagline:          file.Metadata.Tagline,
			Director:         file.Metadata.Director,
			Cast:             file.Metadata.Cast,
			Country:          file.Metadata.Country,
			OriginalLanguage: file.Metadata.OriginalLanguage,
			AgeRating:        file.Metadata.AgeRating,
			Quality:          file.Metadata.Quality,
			AudioTracks:      []domain.AudioTrack{},
			Subtitles:        e.assetSubtitles(file.Subtitles),
			MetadataSource:   file.Metadata.Source,
			Raw:              file.Metadata.Raw,
		}

		// 1. Mirror the catalog entry into the local database so the ship can
		//    render it with no internet.
		created, mirrorErr := e.locals.UpsertCatalogMovie(ctx, movie, detail, domain.SourceFlashdisk)
		if mirrorErr != nil {
			e.log.Error().Err(mirrorErr).Str("file", relPath).Msg("mirror catalog entry failed")
			run.Error = mirrorErr.Error()
			continue
		}

		// 2. Record the physical file in mst_movie_local.
		local := &domain.MovieLocal{
			TenantID:         e.opts.TenantID,
			MovieID:          movie.ID,
			NodeCode:         e.opts.NodeCode,
			Status:           domain.LocalReady,
			Source:           domain.SourceFlashdisk,
			ExpectedFilename: filepath.Base(file.RelPath),
			FilePath:         relPath,
			FileSizeBytes:    file.Size,
			Container:        file.Container,
			ScannedAt:        timePtr(started),
		}
		if matched != nil {
			local.ID = matched.ID
			local.ExpectedFilename = matched.ExpectedFilename
			local.ChecksumSHA256 = matched.ChecksumSHA256
		}
		if local.ExpectedFilename == "" {
			local.ExpectedFilename = filepath.Base(file.RelPath)
		}

		needsHash := true
		if matched != nil && matched.FileSizeBytes == file.Size && matched.ChecksumSHA256 != "" {
			needsHash = false
		}

		wasCreated, upsertErr := e.locals.UpsertMovieLocal(ctx, local)
		if upsertErr != nil {
			e.log.Error().Err(upsertErr).Str("file", relPath).Msg("upsert local file failed")
			run.Error = upsertErr.Error()
			continue
		}

		if created || wasCreated {
			run.FilesAdded++
		} else {
			run.FilesUpdated++
		}

		// 3. When running combined, publish the entry to the main catalog so
		//    the CMS sees flashdisk content without waiting for a report.
		if e.main != nil {
			if _, mainErr := e.main.UpsertCatalogFromNode(ctx, movie, detail); mainErr != nil {
				e.log.Warn().Err(mainErr).Str("file", relPath).Msg("mirror to main catalog failed")
			}
		}

		if needsHash && e.opts.ComputeHash {
			checksumJobs = append(checksumJobs, checksumJob{
				LocalID: local.ID,
				AbsPath: file.AbsPath,
			})
		}
	}

	run.FilesFound = len(files)

	// Anything previously READY and now absent becomes MISSING.
	ready, listErr := e.locals.ListReadyMovieLocals(ctx, e.opts.TenantID)
	if listErr != nil {
		e.log.Warn().Err(listErr).Msg("list ready local files failed")
	} else {
		missing := make([]string, 0, 8)
		for _, m := range ready {
			if _, ok := seen[m.FilePath]; ok {
				continue
			}
			// Only files that live inside the movie library are ours to track.
			if !strings.HasPrefix(m.FilePath, e.store.Layout().Movies+"/") {
				continue
			}
			missing = append(missing, m.ID)
		}
		if len(missing) > 0 {
			n, markErr := e.locals.MarkMovieLocalMissing(ctx, e.opts.TenantID, missing)
			if markErr != nil {
				e.log.Warn().Err(markErr).Msg("mark missing failed")
			} else {
				run.FilesMissing = int(n)
			}
		}
	}

	run.Status = "COMPLETED"
	if err := e.scans.FinishScan(ctx, run); err != nil {
		e.log.Warn().Err(err).Msg("finish scan run failed")
	}

	e.log.Info().
		Int("found", run.FilesFound).
		Int("added", run.FilesAdded).
		Int("updated", run.FilesUpdated).
		Int("missing", run.FilesMissing).
		Dur("elapsed", time.Since(started)).
		Msg("ingest scan completed")

	// Hashing runs after the scan so the catalog is usable immediately.
	go e.runHashers(checksumJobs)

	if run.FilesAdded > 0 || run.FilesUpdated > 0 || run.FilesMissing > 0 {
		if version, bumpErr := e.tenants.BumpCatalogVersion(context.WithoutCancel(ctx), e.opts.TenantID); bumpErr == nil {
			if e.hub != nil {
				e.hub.BroadcastTenant(e.opts.TenantID, "CONTENT_UPDATED", map[string]any{
					"catalog_version": version,
					"files_added":     run.FilesAdded,
					"files_updated":   run.FilesUpdated,
					"files_missing":   run.FilesMissing,
				})
			}
		}
	}

	return run, nil
}

// resolveMovie decides which catalog entry a discovered file belongs to.
//
// Three cases, in order of preference:
//  1. A mst_movie_local row already points at this exact path (rescan).
//  2. A placeholder row was scheduled by the CMS for this filename, which is
//     how a flashdisk delivery completes a pending movie.
//  3. Nothing matches, so a brand new catalog entry is created.
func (e *Engine) resolveMovie(ctx context.Context, relPath string, file Scanned) (string, *domain.MovieLocal, error) {
	if existing, err := e.locals.GetMovieLocalByPath(ctx, e.opts.TenantID, relPath); err == nil {
		return existing.MovieID, existing, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return "", nil, err
	}

	filename := filepath.Base(file.RelPath)
	if scheduled, err := e.locals.GetMovieLocalByExpectedName(ctx, e.opts.TenantID, filename); err == nil {
		e.log.Info().
			Str("file", relPath).
			Str("movie_id", scheduled.MovieID).
			Msg("flashdisk file matched a scheduled movie")
		return scheduled.MovieID, scheduled, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return "", nil, err
	}

	return uuid.NewString(), nil, nil
}

type checksumJob struct {
	LocalID string
	AbsPath string
}

func (e *Engine) runHashers(jobs []checksumJob) {
	if len(jobs) == 0 {
		return
	}

	queue := make(chan checksumJob)
	var wg sync.WaitGroup

	workers := e.opts.HashWorkers
	if workers > len(jobs) {
		workers = len(jobs)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range queue {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
				sum, err := hashFile(job.AbsPath)
				if err != nil {
					e.log.Warn().Err(err).Str("file", job.AbsPath).Msg("checksum failed")
					cancel()
					continue
				}
				if err := e.locals.SetMovieLocalChecksum(ctx, job.LocalID, sum); err != nil {
					e.log.Warn().Err(err).Str("movie_local", job.LocalID).Msg("store checksum failed")
				}
				cancel()
			}
		}()
	}

	go func() {
		defer close(queue)
		for _, job := range jobs {
			select {
			case <-e.closed:
				return
			case queue <- job:
			}
		}
	}()

	wg.Wait()
}

// hashFile computes the SHA-256 digest with a bounded buffer.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("media: open %s: %w", filepath.Base(path), err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	buf := make([]byte, 1024*1024)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", fmt.Errorf("media: hash %s: %w", filepath.Base(path), err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// assetPath prefixes a path discovered inside the movie library with the
// library directory so it resolves against the media root.
func (e *Engine) assetPath(relative string) string {
	if relative == "" {
		return ""
	}
	return e.store.MovieRel(relative)
}

// assetSubtitles rewrites subtitle paths into media-root relative paths.
func (e *Engine) assetSubtitles(in []domain.SubtitleTrack) []domain.SubtitleTrack {
	out := make([]domain.SubtitleTrack, 0, len(in))
	for _, sub := range in {
		path := e.assetPath(sub.Path)
		if path == "" {
			continue
		}
		out = append(out, domain.SubtitleTrack{
			Language: sub.Language,
			Label:    sub.Label,
			Path:     path,
		})
	}
	return out
}

func timePtr(t time.Time) *time.Time { return &t }

// DetailToRaw converts a detail row into the JSON blob kept for traceability.
func DetailToRaw(d domain.MovieDetail) json.RawMessage {
	if len(d.Raw) > 0 {
		return d.Raw
	}
	encoded, err := json.Marshal(d)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}
