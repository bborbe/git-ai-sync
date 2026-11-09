// Package logging builds the application's structured logger.
package logging

import (
	"io"
	"log/slog"
)

// New returns a text logger writing to w at the given level.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}
