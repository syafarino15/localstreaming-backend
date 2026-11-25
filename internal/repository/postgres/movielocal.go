package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

// ---------------------------------------------------------------------------
// Local catalog mirror (stayin_local.mst_movies)
//
// The ship must be able to render the catalog with no internet, so every movie
// distributed to the node is mirrored here together with its metadata. The id
// is identical to the master id in stayin_main, which makes distribution
// idempotent.
// ---------------------------------------------------------------------------

const localMovieColumns = `id, tenant_id, slug, title, year, duration_seconds, genres,
	poster_path, banner_path, is_featured, sort_order, catalog_source, synced_at, created_at, updated_at`

func scanLocalMovie(row pgx.Row) (*domain.Movie, time.Time, string, error) {
	var (
		m      domain.Movie
		synced time.Time
		source string
	)
	err := row.Scan(&m.ID, &m.TenantID, &m.Slug, &m.Title, &m.Year, &m.DurationSeconds, &m.Genres,
		&m.PosterPath, &m.BannerPath, &m.IsFeatured, &m.SortOrder, &source, &synced, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, time.Time{}, "", mapWriteError("movie", err)
	}
	if m.Genres == nil {
		m.Genres = []string{}
	}
	return &m, synced, source, nil
}

// UpsertCatalogMovie mirrors a catalog entry into the local database. The
// caller supplies the id so the master and local rows stay identical. source
// records whether the entry originated centrally or from a flashdisk scan.
func (l *Local) UpsertCatalogMovie(ctx context.Context, m *domain.Movie, d *domain.MovieDetail, source domain.MovieSource) (bool, error) {
	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("local movie: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if m.ID == "" {
		return false, domain.NewValidation("id", "id film wajib diisi untuk cermin lokal")
	}
	if len(m.Genres) == 0 {
		m.Genres = []string{}
	}
	catalogSource := "CLOUD"
	if source == domain.SourceFlashdisk {
		catalogSource = "FLASHDISK"
	}

	var wasCreated bool
	row := tx.QueryRow(ctx, `
		INSERT INTO mst_movies
			(id, tenant_id, slug, title, year, duration_seconds, genres, poster_path, banner_path,
			 is_featured, sort_order, catalog_source, synced_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now())
		ON CONFLICT (id) DO UPDATE SET
			slug = EXCLUDED.slug,
			title = EXCLUDED.title,
			year = EXCLUDED.year,
			duration_seconds = EXCLUDED.duration_seconds,
			genres = EXCLUDED.genres,
			poster_path = CASE WHEN EXCLUDED.poster_path <> '' THEN EXCLUDED.poster_path ELSE mst_movies.poster_path END,
			banner_path = CASE WHEN EXCLUDED.banner_path <> '' THEN EXCLUDED.banner_path ELSE mst_movies.banner_path END,
			is_featured = EXCLUDED.is_featured,
			sort_order = EXCLUDED.sort_order,
			synced_at = now()
		RETURNING `+localMovieColumns+`, (xmax = 0) AS was_created`,
		m.ID, m.TenantID, m.Slug, m.Title, m.Year, m.DurationSeconds, m.Genres,
		m.PosterPath, m.BannerPath, m.IsFeatured, m.SortOrder, catalogSource)

	var (
		saved  domain.Movie
		synced time.Time
		catSrc string
	)
	if err := row.Scan(&saved.ID, &saved.TenantID, &saved.Slug, &saved.Title, &saved.Year,
		&saved.DurationSeconds, &saved.Genres, &saved.PosterPath, &saved.BannerPath,
		&saved.IsFeatured, &saved.SortOrder, &catSrc, &synced, &saved.CreatedAt, &saved.UpdatedAt,
		&wasCreated); err != nil {
		return false, mapWriteError("movie", err)
	}

	if err := l.upsertLocalDetail(ctx, tx, saved.ID, d); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("local movie: commit: %w", err)
	}
	*m = saved
	return wasCreated, nil
}

func (l *Local) upsertLocalDetail(ctx context.Context, tx pgx.Tx, movieID string, d *domain.MovieDetail) error {
	if d == nil {
		d = &domain.MovieDetail{}
	}
	cast, err := marshalJSONArray(d.Cast)
	if err != nil {
		return err
	}
	audio, err := marshalJSONArray(d.AudioTracks)
	if err != nil {
		return err
	}
	subs, err := marshalJSONArray(d.Subtitles)
	if err != nil {
		return err
	}
	raw, err := marshalJSON(d.Raw)
	if err != nil {
		return err
	}
	source := d.MetadataSource
	if source == "" {
		source = "MANUAL"
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO mst_movie_details
			(movie_id, synopsis, tagline, director, cast_members, country, original_language,
			 age_rating, quality, audio_tracks, subtitles, metadata_source, raw)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10::jsonb, $11::jsonb, $12, $13::jsonb)
		ON CONFLICT (movie_id) DO UPDATE SET
			synopsis = EXCLUDED.synopsis,
			tagline = EXCLUDED.tagline,
			director = EXCLUDED.director,
			cast_members = EXCLUDED.cast_members,
			country = EXCLUDED.country,
			original_language = EXCLUDED.original_language,
			age_rating = EXCLUDED.age_rating,
			quality = EXCLUDED.quality,
			audio_tracks = EXCLUDED.audio_tracks,
			subtitles = EXCLUDED.subtitles,
			metadata_source = EXCLUDED.metadata_source,
			raw = EXCLUDED.raw,
			updated_at = now()
		WHERE mst_movie_details.metadata_source <> 'MANUAL' OR EXCLUDED.metadata_source = 'MANUAL'`,
		movieID, d.Synopsis, d.Tagline, d.Director, cast, d.Country, d.OriginalLanguage,
		d.AgeRating, d.Quality, audio, subs, source, raw)
	return mapWriteError("movie detail", err)
}

// GetCatalogMovie returns a mirrored entry with its detail row.
func (l *Local) GetCatalogMovie(ctx context.Context, tenantID, id string) (*domain.Movie, *domain.MovieDetail, error) {
	row := l.pool.QueryRow(ctx, `
		SELECT `+localMovieColumns+` FROM mst_movies WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	m, _, _, err := scanLocalMovie(row)
	if err != nil {
		return nil, nil, err
	}

	d, err := scanDetail(l.pool.QueryRow(ctx, `
		SELECT `+detailColumns+` FROM mst_movie_details WHERE movie_id = $1`, m.ID))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || strings.Contains(err.Error(), "no rows") {
			return m, &domain.MovieDetail{MovieID: m.ID}, nil
		}
		return nil, nil, err
	}
	return m, d, nil
}

// ListCatalogMovies lists mirrored entries (used by the CMS via the local app
// and by diagnostics).
func (l *Local) ListCatalogMovies(ctx context.Context, f domain.MovieFilter) ([]domain.Movie, int64, error) {
	limit, offset := paginate(f.Page, f.Limit)
	args := make([]any, 0, 4)
	conditions := make([]string, 0, 3)
	if f.TenantID != "" {
		args = append(args, f.TenantID)
		conditions = append(conditions, "tenant_id = $"+itoa(len(args)))
	}
	if f.Search != "" {
		args = append(args, "%"+strings.ToLower(f.Search)+"%")
		conditions = append(conditions, "lower(title) LIKE $"+itoa(len(args)))
	}
	clause := ""
	if len(conditions) > 0 {
		clause = " WHERE " + strings.Join(conditions, " AND ")
	}

	var total int64
	if err := l.pool.QueryRow(ctx, `SELECT count(*) FROM mst_movies`+clause, args...).Scan(&total); err != nil {
		return nil, 0, mapWriteError("movie", err)
	}

	args = append(args, limit, offset)
	rows, err := l.pool.Query(ctx, `SELECT `+localMovieColumns+` FROM mst_movies`+clause+
		` ORDER BY is_featured DESC, sort_order, title LIMIT $`+itoa(len(args)-1)+` OFFSET $`+itoa(len(args)), args...)
	if err != nil {
		return nil, 0, mapWriteError("movie", err)
	}
	defer rows.Close()

	out := make([]domain.Movie, 0, limit)
	for rows.Next() {
		m, _, _, err := scanLocalMovie(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *m)
	}
	return out, total, nil
}

// ---------------------------------------------------------------------------
// mst_movie_local — the physical file on this server
// ---------------------------------------------------------------------------

const movieLocalColumns = `id, tenant_id, movie_id, node_code, status, source, expected_filename,
	file_path, file_size_bytes, checksum_sha256, container, progress_bytes, error,
	assigned_at, transfer_started_at, ready_at, scanned_at, created_at, updated_at`

func scanMovieLocal(row pgx.Row) (*domain.MovieLocal, error) {
	var m domain.MovieLocal
	err := row.Scan(&m.ID, &m.TenantID, &m.MovieID, &m.NodeCode, &m.Status, &m.Source,
		&m.ExpectedFilename, &m.FilePath, &m.FileSizeBytes, &m.ChecksumSHA256, &m.Container,
		&m.ProgressBytes, &m.Error, &m.AssignedAt, &m.TransferStartedAt, &m.ReadyAt,
		&m.ScannedAt, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, mapWriteError("movie local", err)
	}
	return &m, nil
}

// GetMovieLocalByMovieID returns the file state for one movie.
func (l *Local) GetMovieLocalByMovieID(ctx context.Context, tenantID, movieID string) (*domain.MovieLocal, error) {
	return scanMovieLocal(l.pool.QueryRow(ctx, `
		SELECT `+movieLocalColumns+` FROM mst_movie_local WHERE tenant_id = $1 AND movie_id = $2`,
		tenantID, movieID))
}

// GetMovieLocalByPath returns the file state for a relative storage path.
func (l *Local) GetMovieLocalByPath(ctx context.Context, tenantID, filePath string) (*domain.MovieLocal, error) {
	return scanMovieLocal(l.pool.QueryRow(ctx, `
		SELECT `+movieLocalColumns+` FROM mst_movie_local WHERE tenant_id = $1 AND file_path = $2`,
		tenantID, filePath))
}

// GetMovieLocalByExpectedName finds the placeholder row created when the CMS
// scheduled a movie for this node. The ingest engine uses it to bind a file
// delivered by flashdisk to the catalog entry the operator authored centrally.
// PENDING and FAILED rows are preferred so a retry adopts the newcomer.
func (l *Local) GetMovieLocalByExpectedName(ctx context.Context, tenantID, filename string) (*domain.MovieLocal, error) {
	return scanMovieLocal(l.pool.QueryRow(ctx, `
		SELECT `+movieLocalColumns+`
		FROM mst_movie_local
		WHERE tenant_id = $1 AND lower(expected_filename) = lower($2)
		ORDER BY CASE status WHEN 'PENDING' THEN 0 WHEN 'FAILED' THEN 1 WHEN 'MISSING' THEN 2 ELSE 3 END,
		         assigned_at
		LIMIT 1`, tenantID, filename))
}

// UpsertMovieLocal inserts or refreshes the physical file row. It reports
// whether the row was created.
func (l *Local) UpsertMovieLocal(ctx context.Context, m *domain.MovieLocal) (bool, error) {
	if m.Status == "" {
		m.Status = domain.LocalPending
	}
	if m.Source == "" {
		m.Source = domain.SourceCloud
	}

	// A READY row always gets a timestamp; any other status clears it. The
	// CASE expressions keep that rule inside one statement so a concurrent
	// scan cannot leave a READY row without ready_at.
	row := l.pool.QueryRow(ctx, `
		INSERT INTO mst_movie_local
			(tenant_id, movie_id, node_code, status, source, expected_filename, file_path,
			 file_size_bytes, checksum_sha256, container, progress_bytes, error, scanned_at, ready_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
		        CASE WHEN $4 = 'READY' THEN now() ELSE NULL END)
		ON CONFLICT (movie_id) DO UPDATE SET
			node_code = EXCLUDED.node_code,
			status = EXCLUDED.status,
			source = EXCLUDED.source,
			expected_filename = CASE WHEN EXCLUDED.expected_filename <> '' THEN EXCLUDED.expected_filename ELSE mst_movie_local.expected_filename END,
			file_path = CASE WHEN EXCLUDED.file_path <> '' THEN EXCLUDED.file_path ELSE mst_movie_local.file_path END,
			file_size_bytes = EXCLUDED.file_size_bytes,
			checksum_sha256 = CASE WHEN EXCLUDED.checksum_sha256 <> '' THEN EXCLUDED.checksum_sha256 ELSE mst_movie_local.checksum_sha256 END,
			container = CASE WHEN EXCLUDED.container <> '' THEN EXCLUDED.container ELSE mst_movie_local.container END,
			progress_bytes = EXCLUDED.progress_bytes,
			error = EXCLUDED.error,
			scanned_at = EXCLUDED.scanned_at,
			ready_at = CASE WHEN EXCLUDED.status = 'READY' THEN now() ELSE mst_movie_local.ready_at END
		RETURNING `+movieLocalColumns+`, (xmax = 0) AS was_created`,
		m.TenantID, m.MovieID, m.NodeCode, m.Status, m.Source, m.ExpectedFilename, m.FilePath,
		m.FileSizeBytes, m.ChecksumSHA256, m.Container, m.ProgressBytes, m.Error, m.ScannedAt)

	var created bool
	saved, err := scanMovieLocalWithCreated(row, &created)
	if err != nil {
		return false, err
	}
	*m = *saved
	return created, nil
}

func scanMovieLocalWithCreated(row pgx.Row, created *bool) (*domain.MovieLocal, error) {
	var m domain.MovieLocal
	err := row.Scan(&m.ID, &m.TenantID, &m.MovieID, &m.NodeCode, &m.Status, &m.Source,
		&m.ExpectedFilename, &m.FilePath, &m.FileSizeBytes, &m.ChecksumSHA256, &m.Container,
		&m.ProgressBytes, &m.Error, &m.AssignedAt, &m.TransferStartedAt, &m.ReadyAt,
		&m.ScannedAt, &m.CreatedAt, &m.UpdatedAt, created)
	if err != nil {
		return nil, mapWriteError("movie local", err)
	}
	return &m, nil
}

// SetMovieLocalChecksum stores the SHA-256 digest computed by the hasher.
func (l *Local) SetMovieLocalChecksum(ctx context.Context, id, checksum string) error {
	_, err := l.pool.Exec(ctx, `UPDATE mst_movie_local SET checksum_sha256 = $2 WHERE id = $1`, id, checksum)
	return mapWriteError("movie local", err)
}

// MarkMovieLocalMissing flags files that disappeared from storage.
func (l *Local) MarkMovieLocalMissing(ctx context.Context, tenantID string, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	parts := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, tenantID)
	for i, id := range ids {
		args = append(args, id)
		parts = append(parts, "$"+itoa(i+2))
	}
	tag, err := l.pool.Exec(ctx, `
		UPDATE mst_movie_local SET status = 'MISSING'
		WHERE tenant_id = $1 AND id IN (`+strings.Join(parts, ",")+`) AND status <> 'MISSING'`, args...)
	if err != nil {
		return 0, mapWriteError("movie local", err)
	}
	return tag.RowsAffected(), nil
}

// ListReadyMovieLocals returns every playable file. The ingest engine uses it
// to detect files that disappeared from storage.
func (l *Local) ListReadyMovieLocals(ctx context.Context, tenantID string) ([]domain.MovieLocal, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT `+movieLocalColumns+`
		FROM mst_movie_local
		WHERE tenant_id = $1 AND status = 'READY'
		ORDER BY file_path`, tenantID)
	if err != nil {
		return nil, mapWriteError("movie local", err)
	}
	defer rows.Close()

	out := make([]domain.MovieLocal, 0, 64)
	for rows.Next() {
		m, err := scanMovieLocal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	if err := rows.Err(); err != nil {
		return nil, mapWriteError("movie local", err)
	}
	return out, nil
}

// ListMovieLocals returns the local inventory for the CMS.
func (l *Local) ListMovieLocals(ctx context.Context, f domain.MovieLocalFilter) ([]domain.MovieLocal, int64, error) {
	limit, offset := paginate(f.Page, f.Limit)
	args := make([]any, 0, 4)
	conditions := make([]string, 0, 3)

	if f.TenantID != "" {
		args = append(args, f.TenantID)
		conditions = append(conditions, "tenant_id = $"+itoa(len(args)))
	}
	if len(f.Statuses) > 0 {
		parts := make([]string, 0, len(f.Statuses))
		for _, st := range f.Statuses {
			args = append(args, string(st))
			parts = append(parts, "$"+itoa(len(args)))
		}
		conditions = append(conditions, "status IN ("+strings.Join(parts, ",")+")")
	}
	if f.Source != "" {
		args = append(args, string(f.Source))
		conditions = append(conditions, "source = $"+itoa(len(args)))
	}
	clause := ""
	if len(conditions) > 0 {
		clause = " WHERE " + strings.Join(conditions, " AND ")
	}

	var total int64
	if err := l.pool.QueryRow(ctx, `SELECT count(*) FROM mst_movie_local`+clause, args...).Scan(&total); err != nil {
		return nil, 0, mapWriteError("movie local", err)
	}

	args = append(args, limit, offset)
	rows, err := l.pool.Query(ctx, `SELECT `+movieLocalColumns+` FROM mst_movie_local`+clause+
		` ORDER BY updated_at DESC LIMIT $`+itoa(len(args)-1)+` OFFSET $`+itoa(len(args)), args...)
	if err != nil {
		return nil, 0, mapWriteError("movie local", err)
	}
	defer rows.Close()

	out := make([]domain.MovieLocal, 0, limit)
	for rows.Next() {
		m, err := scanMovieLocal(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *m)
	}
	return out, total, nil
}

// DeleteMovieLocal removes the file row for a movie.
func (l *Local) DeleteMovieLocal(ctx context.Context, tenantID, movieID string) error {
	tag, err := l.pool.Exec(ctx, `DELETE FROM mst_movie_local WHERE tenant_id = $1 AND movie_id = $2`, tenantID, movieID)
	if err != nil {
		return mapWriteError("movie local", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.WrapNotFound("movie local")
	}
	return nil
}

// CountMovieLocalByStatus counts local files in a state.
func (l *Local) CountMovieLocalByStatus(ctx context.Context, tenantID string, status domain.LocalMovieStatus) (int64, error) {
	var n int64
	err := l.pool.QueryRow(ctx, `SELECT count(*) FROM mst_movie_local WHERE tenant_id = $1 AND status = $2`,
		tenantID, status).Scan(&n)
	return n, mapWriteError("movie local", err)
}

// StorageUsedBytes sums the size of every playable local file.
func (l *Local) StorageUsedBytes(ctx context.Context, tenantID string) (int64, error) {
	var total int64
	err := l.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(file_size_bytes), 0) FROM mst_movie_local
		WHERE tenant_id = $1 AND status = 'READY'`, tenantID).Scan(&total)
	if err != nil && !strings.Contains(err.Error(), "no rows") {
		return 0, mapWriteError("movie local", err)
	}
	return total, nil
}

// ListPlayable returns catalog entries joined with their ready local file.
// This is the single query the TV catalog is built from.
func (l *Local) ListPlayable(ctx context.Context, tenantID string, limit int) ([]domain.MovieWithLocal, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := l.pool.Query(ctx, `
		SELECT `+prefixedColumns("m", localMovieColumns)+`, `+prefixedColumns("l", movieLocalColumns)+`
		FROM mst_movies m
		JOIN mst_movie_local l ON l.movie_id = m.id
		WHERE m.tenant_id = $1 AND l.status = 'READY' AND l.file_path <> ''
		ORDER BY m.is_featured DESC, m.sort_order, m.title
		LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, mapWriteError("movie", err)
	}
	defer rows.Close()

	out := make([]domain.MovieWithLocal, 0, 64)
	for rows.Next() {
		var (
			m      domain.Movie
			local  domain.MovieLocal
			synced time.Time
			catSrc string
		)
		if err := rows.Scan(&m.ID, &m.TenantID, &m.Slug, &m.Title, &m.Year, &m.DurationSeconds,
			&m.Genres, &m.PosterPath, &m.BannerPath, &m.IsFeatured, &m.SortOrder, &catSrc,
			&synced, &m.CreatedAt, &m.UpdatedAt,
			&local.ID, &local.TenantID, &local.MovieID, &local.NodeCode, &local.Status,
			&local.Source, &local.ExpectedFilename, &local.FilePath, &local.FileSizeBytes,
			&local.ChecksumSHA256, &local.Container, &local.ProgressBytes, &local.Error,
			&local.AssignedAt, &local.TransferStartedAt, &local.ReadyAt, &local.ScannedAt,
			&local.CreatedAt, &local.UpdatedAt); err != nil {
			return nil, mapWriteError("movie", err)
		}
		if m.Genres == nil {
			m.Genres = []string{}
		}
		out = append(out, domain.MovieWithLocal{Movie: m, Local: &local})
	}
	if err := rows.Err(); err != nil {
		return nil, mapWriteError("movie", err)
	}
	return out, nil
}

// GetPlayable returns one playable movie with its detail and local file.
func (l *Local) GetPlayable(ctx context.Context, tenantID, movieID string) (*domain.MovieWithLocal, *domain.MovieDetail, error) {
	movie, detail, err := l.GetCatalogMovie(ctx, tenantID, movieID)
	if err != nil {
		return nil, nil, err
	}
	local, err := l.GetMovieLocalByMovieID(ctx, tenantID, movieID)
	if err != nil {
		return nil, nil, err
	}
	return &domain.MovieWithLocal{Movie: *movie, Local: local}, detail, nil
}

// GetPlayableGlobal resolves a movie without a tenant filter for streaming.
func (l *Local) GetPlayableGlobal(ctx context.Context, movieID string) (*domain.MovieWithLocal, error) {
	row := l.pool.QueryRow(ctx, `
		SELECT `+prefixedColumns("m", localMovieColumns)+`, `+prefixedColumns("l", movieLocalColumns)+`
		FROM mst_movies m
		JOIN mst_movie_local l ON l.movie_id = m.id
		WHERE m.id = $1`, movieID)

	var (
		m      domain.Movie
		local  domain.MovieLocal
		synced time.Time
		catSrc string
	)
	if err := row.Scan(&m.ID, &m.TenantID, &m.Slug, &m.Title, &m.Year, &m.DurationSeconds,
		&m.Genres, &m.PosterPath, &m.BannerPath, &m.IsFeatured, &m.SortOrder, &catSrc,
		&synced, &m.CreatedAt, &m.UpdatedAt,
		&local.ID, &local.TenantID, &local.MovieID, &local.NodeCode, &local.Status,
		&local.Source, &local.ExpectedFilename, &local.FilePath, &local.FileSizeBytes,
		&local.ChecksumSHA256, &local.Container, &local.ProgressBytes, &local.Error,
		&local.AssignedAt, &local.TransferStartedAt, &local.ReadyAt, &local.ScannedAt,
		&local.CreatedAt, &local.UpdatedAt); err != nil {
		return nil, mapWriteError("movie", err)
	}
	if m.Genres == nil {
		m.Genres = []string{}
	}
	return &domain.MovieWithLocal{Movie: m, Local: &local}, nil
}

// Genres returns the distinct genres of playable movies.
func (l *Local) Genres(ctx context.Context, tenantID string) ([]string, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT DISTINCT g FROM mst_movies m
		JOIN mst_movie_local l ON l.movie_id = m.id
		, unnest(m.genres) AS g
		WHERE m.tenant_id = $1 AND l.status = 'READY'
		ORDER BY g`, tenantID)
	if err != nil {
		return nil, mapWriteError("movie", err)
	}
	defer rows.Close()

	out := make([]string, 0, 16)
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, mapWriteError("movie", err)
		}
		out = append(out, g)
	}
	return out, nil
}

// TopMovies ranks titles by watch activity. Both tables live in stayin_local.
func (l *Local) TopMovies(ctx context.Context, tenantID string, days int, limit int) ([]domain.TopMovie, error) {
	if days <= 0 {
		days = 30
	}
	if limit <= 0 {
		limit = 10
	}
	rows, err := l.pool.Query(ctx, `
		SELECT m.id::text, m.title, count(w.id), COALESCE(sum(w.position_seconds), 0)
		FROM mst_movies m
		JOIN trn_watch_logs w ON w.movie_id = m.id
		WHERE m.tenant_id = $1 AND w.updated_at > now() - ($2 || ' days')::interval
		GROUP BY m.id, m.title
		ORDER BY count(w.id) DESC, sum(w.position_seconds) DESC
		LIMIT $3`, tenantID, days, limit)
	if err != nil {
		return nil, mapWriteError("movie", err)
	}
	defer rows.Close()

	out := make([]domain.TopMovie, 0, limit)
	for rows.Next() {
		var t domain.TopMovie
		if err := rows.Scan(&t.MovieID, &t.Title, &t.WatchCount, &t.TotalSeconds); err != nil {
			return nil, mapWriteError("movie", err)
		}
		out = append(out, t)
	}
	return out, nil
}

// prefixedColumns prefixes every column in a comma separated list with a table
// alias so a join can select two identically named shapes.
func prefixedColumns(alias, columns string) string {
	parts := strings.Split(columns, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, alias+"."+part)
	}
	return strings.Join(out, ", ")
}

// marshalDetailJSON prepares the mirror of a detail row.
func marshalDetailJSON(d *domain.MovieDetail) (string, error) {
	if d == nil {
		return "{}", nil
	}
	buf, err := json.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("postgres: marshal detail: %w", err)
	}
	return string(buf), nil
}
