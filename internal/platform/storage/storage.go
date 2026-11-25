// Package storage owns every filesystem access for media. All paths crossing
// this boundary are relative to the media root, so a database row can never
// address a file outside the shared storage directory.
package storage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// ErrEscape is returned when a relative path tries to leave the media root.
var ErrEscape = errors.New("storage: path escapes media root")

// Layout describes the media directory structure.
type Layout struct {
	Root    string
	Movies  string
	Banners string
	Posters string
	APK     string
}

// Store is the media filesystem gateway.
type Store struct {
	layout Layout
	root   string

	slots chan struct{}
}

// New builds a Store and verifies that the root is an absolute path.
func New(layout Layout, maxOpenFiles int) (*Store, error) {
	root, err := filepath.Abs(layout.Root)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve media root: %w", err)
	}
	if maxOpenFiles < 1 {
		maxOpenFiles = 64
	}
	return &Store{
		layout: layout,
		root:   root,
		slots:  make(chan struct{}, maxOpenFiles),
	}, nil
}

// Root is the absolute media root.
func (s *Store) Root() string { return s.root }

// Layout returns the configured directory names.
func (s *Store) Layout() Layout { return s.layout }

// MoviesDir is the absolute path of the video library.
func (s *Store) MoviesDir() string { return filepath.Join(s.root, s.layout.Movies) }

// BannersDir is the absolute path of shared branding assets.
func (s *Store) BannersDir() string { return filepath.Join(s.root, s.layout.Banners) }

// PostersDir is the absolute path of poster artwork.
func (s *Store) PostersDir() string { return filepath.Join(s.root, s.layout.Posters) }

// APKDir is the absolute path of application packages.
func (s *Store) APKDir() string { return filepath.Join(s.root, s.layout.APK) }

// EnsureDirs creates every managed directory when missing.
func (s *Store) EnsureDirs() error {
	for _, dir := range []string{s.root, s.MoviesDir(), s.BannersDir(), s.PostersDir(), s.APKDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("storage: create %s: %w", dir, err)
		}
	}
	return nil
}

// Resolve converts a relative media path into an absolute path, rejecting
// traversal attempts such as "../../etc/passwd".
func (s *Store) Resolve(relative string) (string, error) {
	cleaned, err := s.normalize(relative)
	if err != nil {
		return "", err
	}
	abs := filepath.Join(s.root, filepath.FromSlash(cleaned))
	if !s.within(abs) {
		return "", ErrEscape
	}
	return abs, nil
}

// Relative converts an absolute path inside the root into a slash separated
// relative path suitable for storing in PostgreSQL.
func (s *Store) Relative(absolute string) (string, error) {
	abs, err := filepath.Abs(absolute)
	if err != nil {
		return "", fmt.Errorf("storage: resolve absolute path: %w", err)
	}
	if !s.within(abs) {
		return "", ErrEscape
	}
	rel, err := filepath.Rel(s.root, abs)
	if err != nil {
		return "", fmt.Errorf("storage: compute relative path: %w", err)
	}
	return filepath.ToSlash(rel), nil
}

// MovieRel builds the relative path of a file inside the movie library.
func (s *Store) MovieRel(name string) string {
	return path.Join(s.layout.Movies, strings.TrimPrefix(filepath.ToSlash(name), "/"))
}

// Acquire reserves one open-file slot, blocking when the limit is reached. This
// keeps 50 concurrent streamers from exhausting the process file descriptor
// budget on a small mini PC.
func (s *Store) Acquire() { s.slots <- struct{}{} }

// Release returns an open-file slot.
func (s *Store) Release() { <-s.slots }

// Open reserves a slot and opens the file. The caller must call the returned
// release function after closing the reader.
func (s *Store) Open(relative string) (io.ReadSeekCloser, os.FileInfo, func(), error) {
	abs, err := s.Resolve(relative)
	if err != nil {
		return nil, nil, nil, err
	}
	s.Acquire()
	f, err := os.Open(abs)
	if err != nil {
		s.Release()
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil, os.ErrNotExist
		}
		return nil, nil, nil, fmt.Errorf("storage: open %s: %w", relative, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		s.Release()
		return nil, nil, nil, fmt.Errorf("storage: stat %s: %w", relative, err)
	}
	if info.IsDir() {
		_ = f.Close()
		s.Release()
		return nil, nil, nil, fmt.Errorf("storage: %s is a directory", relative)
	}
	return f, info, s.Release, nil
}

// Stat reports file information without holding a slot.
func (s *Store) Stat(relative string) (os.FileInfo, error) {
	abs, err := s.Resolve(relative)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("storage: stat %s: %w", relative, err)
	}
	return info, nil
}

// Exists reports whether the relative path points at a regular file.
func (s *Store) Exists(relative string) bool {
	info, err := s.Stat(relative)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func (s *Store) normalize(relative string) (string, error) {
	relative = strings.TrimSpace(relative)
	relative = strings.ReplaceAll(relative, "\\", "/")
	relative = strings.TrimPrefix(relative, "/")
	if relative == "" {
		return "", fmt.Errorf("storage: empty path")
	}
	cleaned := path.Clean(relative)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", ErrEscape
	}
	if strings.Contains(cleaned, "\x00") {
		return "", fmt.Errorf("storage: invalid path")
	}
	return cleaned, nil
}

func (s *Store) within(abs string) bool {
	rel, err := filepath.Rel(s.root, abs)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// Guard serialises directory creation for concurrent ingest and upload paths.
var dirMu sync.Mutex

// EnsureParent creates the parent directory of an absolute path.
func EnsureParent(abs string) error {
	dirMu.Lock()
	defer dirMu.Unlock()
	return os.MkdirAll(filepath.Dir(abs), 0o755)
}
