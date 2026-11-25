// Package migrator applies the embedded SQL migrations exactly once per file,
// guarded by a PostgreSQL advisory lock so multiple containers starting at the
// same time cannot race.
package migrator

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// advisoryLockKey is an arbitrary but stable key for this application.
const advisoryLockKey int64 = 7_241_905_331_004

// advisoryLockKeyLocal uses a different key so the main and local migrators can
// run concurrently inside one process without blocking each other.
const advisoryLockKeyLocal int64 = 7_241_905_331_005

// Run applies every pending migration in lexical filename order.
//
// dir is the sub-directory inside fsys that holds the SQL files ("main" or
// "local"). Each database keeps its own schema_migrations table, so the two
// applications can evolve independently.
func Run(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, dir string, log zerolog.Logger) error {
	lockKey := advisoryLockKey
	if dir == "local" {
		lockKey = advisoryLockKeyLocal
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrator: acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		return fmt.Errorf("migrator: acquire advisory lock: %w", err)
	}
	defer func() {
		if _, err := conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lockKey); err != nil {
			log.Error().Err(err).Msg("migrator: release advisory lock failed")
		}
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("migrator: create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return fmt.Errorf("migrator: read %s migrations dir: %w", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	applied := make(map[string]struct{}, len(names))
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("migrator: list applied migrations: %w", err)
	}
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return fmt.Errorf("migrator: scan applied migration: %w", err)
		}
		applied[version] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("migrator: iterate applied migrations: %w", err)
	}

	for _, name := range names {
		if _, ok := applied[name]; ok {
			continue
		}
		body, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return fmt.Errorf("migrator: read %s: %w", name, err)
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("migrator: begin %s: %w", name, err)
		}

		// Simple protocol is required because a migration file contains many
		// statements and pgx otherwise uses the extended protocol.
		if _, err := tx.Exec(ctx, string(body), pgx.QueryExecModeSimpleProtocol); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migrator: apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migrator: record %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("migrator: commit %s: %w", name, err)
		}
		log.Info().Str("migration", name).Msg("migration applied")
	}

	return nil
}
