package main

import (
	"log"
	"log/slog"
	"os"
)

var (
	// Logger is the structured logger instance
	Logger *slog.Logger
)

// initLogger initializes the structured logger
func initLogger() {
	// Use JSON handler for structured logging
	// Set level to Info by default
	opts := &slog.HandlerOptions{
		Level:     slog.LevelInfo,
		AddSource: false, // Set to true for debugging
	}

	handler := slog.NewJSONHandler(os.Stdout, opts)
	Logger = slog.New(handler)
}

// LogInfo logs an info message with structured fields
func LogInfo(msg string, args ...any) {
	if Logger != nil {
		Logger.Info(msg, args...)
	} else {
		// Fallback to standard logger if not initialized
		log.Printf("INFO: %s", msg)
	}
}

// LogWarn logs a warning message with structured fields
func LogWarn(msg string, args ...any) {
	if Logger != nil {
		Logger.Warn(msg, args...)
	} else {
		log.Printf("⚠️  WARN: %s", msg)
	}
}

// LogError logs an error message with structured fields
func LogError(msg string, args ...any) {
	if Logger != nil {
		Logger.Error(msg, args...)
	} else {
		log.Printf("❌ ERROR: %s", msg)
	}
}

// LogDebug logs a debug message with structured fields (if debug level enabled)
func LogDebug(msg string, args ...any) {
	if Logger != nil {
		Logger.Debug(msg, args...)
	}
}
