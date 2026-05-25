package logger

import (
	"log/slog"
	"os"
)

// InitLogger initializes a standardized global JSON slog configuration
func InitLogger() {
	// Create a new structured JSON handler writing output to standard out (stdout)
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo, // Only print Info level logs and higher (Warn, Error)
	})

	// Set this handler as the default logging engine for the entire application execution life
	logger := slog.New(handler)
	slog.SetDefault(logger)
}
