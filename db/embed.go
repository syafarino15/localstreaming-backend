// Package db embeds the SQL migrations so the binary is self-contained and a
// fresh node can bootstrap its schema without shipping extra files.
//
// The project runs as TWO separate applications with TWO separate databases:
//
//	MainMigrations  -> stayin_main  (server utama: CMS, katalog master, distribusi)
//	LocalMigrations -> stayin_local (server lokal kapal: TV, streaming, berkas nyata)
//
// The migrator applies each set to its own pool and its own schema_migrations
// table, so the two databases never share state.
package db

import "embed"

// MainMigrations is applied to the central authoring database.
//
//go:embed migrations/main/*.sql
var MainMigrations embed.FS

// LocalMigrations is applied to the on-board operational database.
//
//go:embed migrations/local/*.sql
var LocalMigrations embed.FS

// Directory names inside the embed filesystems.
const (
	MainDir  = "migrations/main"
	LocalDir = "migrations/local"
)
