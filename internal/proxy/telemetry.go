package proxy

import (
	"log/slog"
	"time"
)

type LogEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	Path       string    `json:"path"`
	TargetHost string    `json:"target_host"`
	StatusCode int       `json:"status_code"`
	LatencyMs  int64     `json:"latency_ms"`
}

type TelemetryEngine struct {
	LogQueue chan LogEntry
}

func NewTelemetryEngine(bufferSize int) *TelemetryEngine {
	engine := &TelemetryEngine{

		LogQueue: make(chan LogEntry, bufferSize),
	}

	go engine.startLogConsumer()

	return engine
}
func (te *TelemetryEngine) startLogConsumer() {
	slog.Info("Asynchronous cloud telemetry background worker initialized cleanly.")

	for entry := range te.LogQueue {

		slog.Info("Telemetry event dispatched asynchronously",
			"path", entry.Path,
			"target", entry.TargetHost,
			"status", entry.StatusCode,
			"latency_ms", entry.LatencyMs,
		)
	}
}
