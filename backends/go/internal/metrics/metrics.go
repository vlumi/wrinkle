// Package metrics defines the backend's Prometheus metrics. The names are
// part of the comparison: every backend exposes the same ones, documented
// in docs/metrics.md.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	Connections = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "wrinkle_connections",
		Help: "Open WebSocket connections on this gateway.",
	})
	Requests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "wrinkle_requests_total",
		Help: "Requests handled, by type and result (ok or an error code).",
	}, []string{"type", "code"})
	RequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "wrinkle_request_duration_seconds",
		Help:    "Time from reading a request to queueing its response.",
		Buckets: prometheus.ExponentialBuckets(0.0005, 2, 16),
	}, []string{"type"})
	EventsPushed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "wrinkle_events_pushed_total",
		Help: "Events written to connected devices.",
	})
	FanoutRecords = promauto.NewCounter(prometheus.CounterOpts{
		Name: "wrinkle_fanout_records_total",
		Help: "Outbox records consumed from Kafka.",
	})
	FanoutDeliveries = promauto.NewCounter(prometheus.CounterOpts{
		Name: "wrinkle_fanout_deliveries_total",
		Help: "Events appended to a user's log.",
	})
	PushSent = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "wrinkle_push_sent_total",
		Help: "Push notifications to devices not connected, by result: ok, unregistered, error, dropped.",
	}, []string{"result"})
	FanoutLag = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "wrinkle_fanout_lag_seconds",
		Help:    "Time from an event being stored to its append to a user's log.",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 16),
	})
)

// Handler serves the metrics uncompressed: the scrapers are local, and the
// gzip writers would otherwise show up in heap profiles.
func Handler() http.Handler {
	return promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{DisableCompression: true})
}
