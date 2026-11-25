// Package postgres implements the repository contracts using pgx.
//
// The package serves TWO separate databases, mirroring the two applications:
//
//	Store -> stayin_main  (server utama: CMS, katalog master, distribusi)
//	Local -> stayin_local (server lokal kapal: TV, streaming, berkas nyata)
//
// The two types never share a pool. A query written for one database cannot
// accidentally run against the other, because the methods live on different
// receivers.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

// Store is the MAIN database repository aggregate (server utama).
type Store struct {
	pool *pgxpool.Pool
}

// Local is the LOCAL database repository aggregate (server kapal).
type Local struct {
	pool *pgxpool.Pool
}

// New opens the main database connection pool.
func New(ctx context.Context, databaseURL string, maxConns, minConns int32) (*Store, error) {
	pool, err := openPool(ctx, "main", databaseURL, maxConns, minConns)
	if err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

// NewLocal opens the local database connection pool.
func NewLocal(ctx context.Context, databaseURL string, maxConns, minConns int32) (*Local, error) {
	pool, err := openPool(ctx, "local", databaseURL, maxConns, minConns)
	if err != nil {
		return nil, err
	}
	return &Local{pool: pool}, nil
}

func openPool(ctx context.Context, label, databaseURL string, maxConns, minConns int32) (*pgxpool.Pool, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("postgres: %s database url is empty", label)
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse %s database url: %w", label, err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	if minConns > 0 {
		cfg.MinConns = minConns
	}
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 15 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: create %s pool: %w", label, err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping %s: %w", label, err)
	}

	return pool, nil
}

// Pool exposes the main pool for the migrator.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Close releases all main connections.
func (s *Store) Close() { s.pool.Close() }

// Ping verifies main connectivity for the health endpoint.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Pool exposes the local pool for the migrator.
func (l *Local) Pool() *pgxpool.Pool { return l.pool }

// Close releases all local connections.
func (l *Local) Close() { l.pool.Close() }

// Ping verifies local connectivity for the health endpoint.
func (l *Local) Ping(ctx context.Context) error { return l.pool.Ping(ctx) }

// isUniqueViolation reports whether err is a PostgreSQL 23505 error.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

// isForeignKeyViolation reports whether err is a PostgreSQL 23503 error.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23503"
	}
	return false
}

// mapWriteError converts driver errors into domain sentinel errors.
func mapWriteError(entity string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return domain.WrapNotFound(entity)
	case isUniqueViolation(err):
		return fmt.Errorf("%s: %w", entity, domain.ErrConflict)
	case isForeignKeyViolation(err):
		return fmt.Errorf("%s: %w", entity, domain.ErrValidation)
	default:
		return fmt.Errorf("%s: %w", entity, err)
	}
}

// marshalJSON converts a value into JSONB parameters. A nil value becomes an
// empty object so NOT NULL columns stay satisfied.
func marshalJSON(v any) (string, error) {
	if v == nil {
		return "{}", nil
	}
	switch typed := v.(type) {
	case json.RawMessage:
		if len(typed) == 0 {
			return "{}", nil
		}
		return string(typed), nil
	case []byte:
		if len(typed) == 0 {
			return "{}", nil
		}
		return string(typed), nil
	}
	buf, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("postgres: marshal json: %w", err)
	}
	return string(buf), nil
}

// marshalJSONArray is the array counterpart used by JSONB columns that hold
// lists (cast_members, audio_tracks, subtitles).
func marshalJSONArray(v any) (string, error) {
	if v == nil {
		return "[]", nil
	}
	buf, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("postgres: marshal json array: %w", err)
	}
	return string(buf), nil
}

// rawJSON normalises a scanned JSONB column.
func rawJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

// paginate clamps a page/limit pair for SQL LIMIT/OFFSET.
func paginate(page, limit int) (int, int) {
	if page < 1 {
		page = 1
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	return limit, (page - 1) * limit
}
