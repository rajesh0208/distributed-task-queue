// File: internal/monitoring/metrics.go
package monitoring

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// Task metrics
	TasksSubmitted = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "taskqueue_tasks_submitted_total",
			Help: "Total number of tasks submitted",
		},
		[]string{"type"},
	)

	TasksProcessed = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "taskqueue_tasks_processed_total",
			Help: "Total number of tasks processed",
		},
		[]string{"type", "status"},
	)

	TasksInProgress = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "taskqueue_tasks_in_progress",
			Help: "Number of tasks currently being processed",
		},
		[]string{"worker_id"},
	)

	TaskQueueLength = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "taskqueue_queue_length",
			Help: "Current number of tasks in queue",
		},
	)

	TaskProcessingDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "taskqueue_task_processing_duration_seconds",
			Help:    "Time taken to process tasks",
			Buckets: prometheus.ExponentialBuckets(0.1, 2, 10),
		},
		[]string{"type"},
	)

	// Worker metrics
	ActiveWorkers = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "taskqueue_active_workers",
			Help: "Number of active workers",
		},
	)

	WorkerHeartbeats = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "taskqueue_worker_heartbeats_total",
			Help: "Total number of worker heartbeats",
		},
		[]string{"worker_id"},
	)

	// API metrics
	APIRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "taskqueue_api_request_duration_seconds",
			Help:    "API request duration",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "endpoint", "status"},
	)

	APIRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "taskqueue_api_requests_total",
			Help: "Total number of API requests",
		},
		[]string{"method", "endpoint", "status"},
	)

	// Database metrics
	DatabaseConnectionsInUse = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "taskqueue_database_connections_in_use",
			Help: "Number of database connections in use",
		},
	)

	// Cache metrics
	CacheHits = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "taskqueue_cache_hits_total",
			Help: "Total number of cache hits",
		},
	)

	CacheMisses = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "taskqueue_cache_misses_total",
			Help: "Total number of cache misses",
		},
	)

	// Circuit breaker metrics
	CircuitBreakerState = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "taskqueue_circuit_breaker_state",
			Help: "Circuit breaker state (0=closed, 1=half-open, 2=open)",
		},
		[]string{"name"},
	)

	CircuitBreakerFailures = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "taskqueue_circuit_breaker_failures_total",
			Help: "Total number of circuit breaker failures",
		},
		[]string{"name"},
	)

	// Error metrics
	ErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "taskqueue_errors_total",
			Help: "Total number of errors",
		},
		[]string{"component", "error_type"},
	)
)
