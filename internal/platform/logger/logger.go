// Package logger builds the structured JSON logger used across the service.
package logger

import (
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// New returns a zerolog logger configured for production JSON output.
// The writer is injectable so tests can capture log records.
func New(level string, w io.Writer) zerolog.Logger {
	if w == nil {
		w = os.Stdout
	}
	zerolog.TimeFieldFormat = time.RFC3339Nano

	lvl, err := zerolog.ParseLevel(strings.ToLower(strings.TrimSpace(level)))
	if err != nil || lvl == zerolog.NoLevel {
		lvl = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(lvl)

	return zerolog.New(w).
		With().
		Timestamp().
		Str("service", "stayin-local-backend").
		Logger()
}
