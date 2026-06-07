package proxy

import (
	"log/slog"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	RequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "proxy_requests_total",
			Help: "Cumulative count of all inbound HTTP requests processed by the gateway edge",
		},
		[]string{"upstream", "status_code"},
	)

	LatencyHistogram = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "proxy_request_duration_ms",
			Help:    "End-to-end request latency profile tracking in milliseconds",
			Buckets: []float64{5, 10, 25, 50, 100, 250, 500, 1000},
		},
		[]string{"upstream"},
	)
)

func init() {

	prometheus.MustRegister(RequestsTotal, LatencyHistogram)
}

type LogEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	Path       string    `json:"path"`
	TargetHost string    `json:"target_host"`
	StatusCode int       `json:"status_code"`
	LatencyMs  int64     `json:"latency_ms"`
}

type TelemetryEngine struct {
	LogQueue chan LogEntry
	metrics  *CloudWatchMetrics
}

func NewTelemetryEngine(bufferSize int) *TelemetryEngine {
	engine := &TelemetryEngine{
		LogQueue: make(chan LogEntry, bufferSize),
		metrics:  NewCloudWatchMetrics("ReverseProxyGateway"),
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

		te.metrics.RecordLatency(entry.TargetHost, float64(entry.LatencyMs))

		statusCodeStr := strconv.Itoa(entry.StatusCode)

		RequestsTotal.WithLabelValues(entry.TargetHost, statusCodeStr).Inc()

		LatencyHistogram.WithLabelValues(entry.TargetHost).Observe(float64(entry.LatencyMs))
	}
}
