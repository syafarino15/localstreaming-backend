package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

// movieColumns is the MASTER catalog shape in stayin_main. There are no
// filesystem columns here by design: a master row knows nothing about where a
// file lives. That information belongs to mst_movie_local in stayin_local.
const movieColumns = `id, tenant_id, slug, title, year, duration_seconds, genres, poster_path, banner_path,
	is_featured, sort_order, status, created_at, updated_at`

func scanMovie(row pgx.Row) (*domain.Movie, error) {
	var m domain.Movie
	err := row.Scan(&m.ID, &m.TenantID, &m.Slug, &m.Title, &m.Year, &m.DurationSeconds, &m.Genres,
		&m.PosterPath, &m.BannerPath, &m.IsFeatured, &m.SortOrder, &m.Status, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, mapWriteError("movie", err)
	}
	if m.Genres == nil {
		m.Genres = []string{}
	}
	return &m, nil
}

func scanDetail(row pgx.Row) (*domain.MovieDetail, error) {
	var (
		d     domain.MovieDetail
		cast  []byte
		audio []byte
		subs  []byte
		raw   []byte
	)
	err := row.Scan(&d.MovieID, &d.Synopsis, &d.Tagline, &d.Director, &cast, &d.Country,
		&d.OriginalLanguage, &d.AgeRating, &d.Quality, &audio, &subs, &d.MetadataSource, &raw, &d.UpdatedAt)
	if err != nil {
		return nil, mapWriteError("movie detail", err)
	}
	_ = json.Unmarshal(cast, &d.Cast)
	_ = json.Unmarshal(audio, &d.AudioTracks)
	_ = json.Unmarshal(subs, &d.Subtitles)
	d.Raw = rawJSON(raw)
	if d.Cast == nil {
		d.Cast = []string{}
	}
	if d.AudioTracks == nil {
		d.AudioTracks = []domain.AudioTrack{}
	}
	if d.Subtitles == nil {
		d.Subtitles = []domain.SubtitleTrack{}
	}
	return &d, nil
}

const detailColumns = `movie_id, synopsis, tagline, director, cast_members, country,
	original_language, age_rating, quality, audio_tracks, subtitles, metadata_source, raw, updated_at`

// ListMovies returns master catalog entries matching the filter.
func (s *Store) ListMovies(ctx context.Context, f domain.MovieFilter) ([]domain.Movie, int64, error) {
	limit, offset := paginate(f.Page, f.Limit)

	var (
		conditions []string
		args       []any
	)
	placeholder := func(value any) string {
		args = append(args, value)
		return "$" + itoa(len(args))
	}

	if f.TenantID != "" {
		conditions = append(conditions, "tenant_id = "+placeholder(f.TenantID))
	}
	if len(f.Statuses) > 0 {
		parts := make([]string, 0, len(f.Statuses))
		for _, st := range f.Statuses {
			parts = append(parts, placeholder(string(st)))
		}
		conditions = append(conditions, "status IN ("+strings.Join(parts, ",")+")")
	}
	if f.Genre != "" {
		conditions = append(conditions, placeholder(f.Genre)+" = ANY(genres)")
	}
	if f.Featured != nil {
		conditions = append(conditions, "is_featured = "+placeholder(*f.Featured))
	}
	if f.Search != "" {
		pattern := "%" + strings.ToLower(f.Search) + "%"
		conditions = append(conditions, "lower(title) LIKE "+placeholder(pattern))
	}

	clause := ""
	if len(conditions) > 0 {
		clause = " WHERE " + strings.Join(conditions, " AND ")
	}

	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM mst_movies`+clause, args...).Scan(&total); err != nil {
		return nil, 0, mapWriteError("movie", err)
	}

	query := `SELECT ` + movieColumns + ` FROM mst_movies` + clause +
		` ORDER BY is_featured DESC, sort_order, title LIMIT ` + placeholder(limit) + ` OFFSET ` + placeholder(offset)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, mapWriteError("movie", err)
	}
	defer rows.Close()

	out := make([]domain.Movie, 0, limit)
	for rows.Next() {
		m, err := scanMovie(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, mapWriteError("movie", err)
	}
	return out, total, nil
}

// GetMovieByID returns a master catalog entry and its detail row. A missing
// detail row yields an empty detail instead of an error.
func (s *Store) GetMovieByID(ctx context.Context, tenantID, id string) (*domain.Movie, *domain.MovieDetail, error) {
	m, err := scanMovie(s.pool.QueryRow(ctx, `
		SELECT `+movieColumns+` FROM mst_movies WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	if err != nil {
		return nil, nil, err
	}

	d, err := scanDetail(s.pool.QueryRow(ctx, `
		SELECT `+detailColumns+` FROM mst_movie_details WHERE movie_id = $1`, m.ID))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || strings.Contains(err.Error(), "no rows") {
			return m, &domain.MovieDetail{MovieID: m.ID}, nil
		}
		return nil, nil, err
	}
	return m, d, nil
}

// GetMovieByIDGlobal resolves a master entry without a tenant filter.
func (s *Store) GetMovieByIDGlobal(ctx context.Context, id string) (*domain.Movie, *domain.MovieDetail, error) {
	m, err := scanMovie(s.pool.QueryRow(ctx, `
		SELECT `+movieColumns+` FROM mst_movies WHERE id = $1`, id))
	if err != nil {
		return nil, nil, err
	}

	d, detailErr := scanDetail(s.pool.QueryRow(ctx, `
		SELECT `+detailColumns+` FROM mst_movie_details WHERE movie_id = $1`, m.ID))
	if detailErr != nil {
		if errors.Is(detailErr, domain.ErrNotFound) || strings.Contains(detailErr.Error(), "no rows") {
			return m, &domain.MovieDetail{MovieID: m.ID}, nil
		}
		return nil, nil, detailErr
	}
	return m, d, nil
}

// GetBySlug finds a master entry by its slug.
func (s *Store) GetMovieBySlug(ctx context.Context, tenantID, slug string) (*domain.Movie, error) {
	return scanMovie(s.pool.QueryRow(ctx, `
		SELECT `+movieColumns+` FROM mst_movies WHERE tenant_id = $1 AND lower(slug) = lower($2)`, tenantID, slug))
}

// CreateMovie inserts a master catalog entry and its details atomically.
func (s *Store) CreateMovie(ctx context.Context, m *domain.Movie, d *domain.MovieDetail) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("movie: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	created, err := s.insertMovie(ctx, tx, m)
	if err != nil {
		return err
	}
	if err := s.upsertDetail(ctx, tx, created.ID, d, true); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("movie: commit: %w", err)
	}
	*m = *created
	return nil
}

// UpdateMovie rewrites master metadata.
func (s *Store) UpdateMovie(ctx context.Context, m *domain.Movie, d *domain.MovieDetail) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("movie: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `
		UPDATE mst_movies
		SET slug = $3, title = $4, year = $5, duration_seconds = $6, genres = $7,
		    poster_path = $8, banner_path = $9, is_featured = $10, sort_order = $11, status = $12
		WHERE tenant_id = $1 AND id = $2
		RETURNING `+movieColumns,
		m.TenantID, m.ID, m.Slug, m.Title, m.Year, m.DurationSeconds, m.Genres,
		m.PosterPath, m.BannerPath, m.IsFeatured, m.SortOrder, m.Status)

	updated, err := scanMovie(row)
	if err != nil {
		return err
	}
	if err := s.upsertDetail(ctx, tx, updated.ID, d, true); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("movie: commit: %w", err)
	}
	*m = *updated
	return nil
}

// DeleteMovie removes a master catalog entry.
func (s *Store) DeleteMovie(ctx context.Context, tenantID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM mst_movies WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return mapWriteError("movie", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.WrapNotFound("movie")
	}
	return nil
}

// CountByStatus counts master catalog entries in a state.
func (s *Store) CountByStatus(ctx context.Context, tenantID string, status domain.MovieStatus) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM mst_movies WHERE tenant_id = $1 AND status = $2`, tenantID, status).Scan(&n)
	return n, mapWriteError("movie", err)
}

// Genres returns the distinct genres present in the master catalog.
func (s *Store) Genres(ctx context.Context, tenantID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT g FROM mst_movies, unnest(genres) AS g
		WHERE tenant_id = $1 AND status <> 'ARCHIVED'
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

// MarkPublished flips PENDING entries to PUBLISHED once they reached a node.
func (s *Store) MarkPublished(ctx context.Context, tenantID string, ids []string) (int64, error) {
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
	tag, err := s.pool.Exec(ctx, `
		UPDATE mst_movies SET status = 'PUBLISHED'
		WHERE tenant_id = $1 AND id IN (`+strings.Join(parts, ",")+`) AND status = 'PENDING'`, args...)
	if err != nil {
		return 0, mapWriteError("movie", err)
	}
	return tag.RowsAffected(), nil
}

// UpsertCatalogFromNode records a movie that was discovered on a local server
// (flashdisk ingest) into the master catalog using the SAME id as the local
// mirror. It only fills fields the CMS has not authored yet, so an operator's
// metadata is never overwritten by a file scan.
func (s *Store) UpsertCatalogFromNode(ctx context.Context, m *domain.Movie, d *domain.MovieDetail) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("movie: begin node upsert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if m.ID == "" {
		return false, domain.NewValidation("id", "id film wajib diisi")
	}
	if m.Status == "" {
		m.Status = domain.MoviePublished
	}
	if len(m.Genres) == 0 {
		m.Genres = []string{}
	}

	var created bool
	row := tx.QueryRow(ctx, `
		INSERT INTO mst_movies
			(id, tenant_id, slug, title, year, duration_seconds, genres, poster_path, banner_path,
			 is_featured, sort_order, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (id) DO UPDATE SET
			title = CASE WHEN mst_movies.title = '' THEN EXCLUDED.title ELSE mst_movies.title END,
			slug = CASE WHEN mst_movies.slug = '' THEN EXCLUDED.slug ELSE mst_movies.slug END,
			year = CASE WHEN mst_movies.year = 0 THEN EXCLUDED.year ELSE mst_movies.year END,
			duration_seconds = CASE WHEN mst_movies.duration_seconds = 0 THEN EXCLUDED.duration_seconds ELSE mst_movies.duration_seconds END,
			genres = CASE WHEN cardinality(mst_movies.genres) = 0 THEN EXCLUDED.genres ELSE mst_movies.genres END,
			poster_path = CASE WHEN EXCLUDED.poster_path <> '' THEN EXCLUDED.poster_path ELSE mst_movies.poster_path END,
			banner_path = CASE WHEN EXCLUDED.banner_path <> '' THEN EXCLUDED.banner_path ELSE mst_movies.banner_path END
		RETURNING `+movieColumns+`, (xmax = 0) AS was_created`,
		m.ID, m.TenantID, m.Slug, m.Title, m.Year, m.DurationSeconds, m.Genres,
		m.PosterPath, m.BannerPath, m.IsFeatured, m.SortOrder, m.Status)

	var (
		saved  domain.Movie
		wasNew bool
	)
	if err := row.Scan(&saved.ID, &saved.TenantID, &saved.Slug, &saved.Title, &saved.Year,
		&saved.DurationSeconds, &saved.Genres, &saved.PosterPath, &saved.BannerPath,
		&saved.IsFeatured, &saved.SortOrder, &saved.Status, &saved.CreatedAt, &saved.UpdatedAt,
		&wasNew); err != nil {
		return false, mapWriteError("movie", err)
	}
	created = wasNew

	if err := s.upsertDetail(ctx, tx, saved.ID, d, false); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("movie: commit node upsert: %w", err)
	}
	*m = saved
	return created, nil
}

func (s *Store) insertMovie(ctx context.Context, tx pgx.Tx, m *domain.Movie) (*domain.Movie, error) {
	if m.Status == "" {
		m.Status = domain.MovieDraft
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO mst_movies
			(tenant_id, slug, title, year, duration_seconds, genres, poster_path, banner_path,
			 is_featured, sort_order, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING `+movieColumns,
		m.TenantID, m.Slug, m.Title, m.Year, m.DurationSeconds, m.Genres, m.PosterPath, m.BannerPath,
		m.IsFeatured, m.SortOrder, m.Status)
	return scanMovie(row)
}

func (s *Store) upsertDetail(ctx context.Context, tx pgx.Tx, movieID string, d *domain.MovieDetail, overwrite bool) error {
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

	conflict := `
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
			updated_at = now()`
	if !overwrite {
		// A manual edit made in the CMS always wins over a later sync.
		conflict += ` WHERE mst_movie_details.metadata_source <> 'MANUAL'`
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO mst_movie_details
			(movie_id, synopsis, tagline, director, cast_members, country, original_language,
			 age_rating, quality, audio_tracks, subtitles, metadata_source, raw)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10::jsonb, $11::jsonb, $12, $13::jsonb)`+conflict,
		movieID, d.Synopsis, d.Tagline, d.Director, cast, d.Country, d.OriginalLanguage,
		d.AgeRating, d.Quality, audio, subs, source, raw)
	return mapWriteError("movie detail", err)
}
