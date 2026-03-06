// Package logging provides a global structured JSON logger built on log/slog.
// All services call Init() once at startup; every subsequent log call produces
// a JSON line that tools like Loki, Datadog, and CloudWatch can index and query.
package logging

import (
	"context"
	"log/slog"
	"os"
)

// ctxKey is an unexported type used as the context key for per-request loggers.
// Using a private type prevents key collisions with other packages.
type ctxKey struct{}

// Init creates a JSON-format slog logger at the requested level, sets it as the
// global slog default, and returns it so callers can store it for structured use.
// Call once in main() before serving any requests.
//
// level accepts "debug", "info", "warn", or "error" (default: info).
func Init(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}

	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: l,
		// AddSource adds the file:line that emitted the log — useful for debugging.
		AddSource: l == slog.LevelDebug,
	})
	logger := slog.New(h)
	slog.SetDefault(logger) // replaces the global log package default too
	return logger
}

// NewContext embeds logger into ctx so request handlers can retrieve it
// without threading the logger as an explicit parameter through every call.
func NewContext(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, logger)
}

// FromContext retrieves the logger stored by NewContext.
// Falls back to slog.Default() if no logger was stored (e.g. background goroutines).
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
