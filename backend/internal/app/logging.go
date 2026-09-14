package app

import (
	"io"
	"log/slog"

	"github.com/freezxp/syslogc/backend/internal/config"
)

// NewLogger creates the structured process logger.
func NewLogger(w io.Writer, cfg config.LogConfig, nodeID string) *slog.Logger {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.Format == "text" {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h).With("node", nodeID)
}
