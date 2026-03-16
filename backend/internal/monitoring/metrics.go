// Package monitoring provides Prometheus metrics registration and HTTP middleware
// for the distributed task queue API and worker.
//
// File: internal/monitoring/metrics.go
//
// What this file does:
//   This file declares every Prometheus metric used by the application —
//   counters, gauges, and histograms — as package-level variables. Because
//   they are package-level, any package that imports "monitoring" can call
//   e.g. monitoring.TasksSubmitted.WithLabelValues("image_resize").Inc()
//   without any further initialization step.
//
// How Prometheus scraping works:
//   Prometheus is a pull-based monitoring system. A Prometheus server is
//   configured with a scrape target (e.g. http://api:8080/metrics) and
//   periodically (default every 15 s) sends an HTTP GET to that endpoint.
//   The Go client library automatically exposes all registered metrics at
//   that endpoint in a plain-text format. The server stores the resulting
//   time series and makes them queryable via PromQL. Grafana then reads
//   Prometheus via PromQL to build dashboards and alert rules.
//
// Three metric types used here — Counter vs Gauge vs Histogram:
//   Counter  — a value that can ONLY go up (e.g. total requests, total errors).
//              Counters survive process restarts: Prometheus computes the rate
//              of increase via rate() / irate() over a sliding time window.
//              Use when: you want to measure throughput or cumulative totals.
//   Gauge    — a value that can go up AND down (e.g. queue depth, active
//              workers, open connections). Represents the current state.
//              Use when: you want to know "how many right now".
//   Histogram — records individual observations in pre-defined latency buckets
//               AND exposes a running count and sum. Enables percentile queries
//               via histogram_quantile() and average calculation.
//               Use when: you want distribution, not just a rate.
//
// Why we instrument at this level (queue depth, processing time, error rate):
//   - Queue depth (TaskQueueLength) tells operators when workers are falling
//     behind the submission rate, before users notice slow task completion.
//   - Processing-time histograms (TaskProcessingDuration) power SLO alerting:
//     fire an alert when p99 latency exceeds the agreed threshold.
//   - Error-rate counters (ErrorsTotal, TasksProcessed{status=failure}) feed
//     dashboards and on-call rules without requiring log parsing at query time.
//
// Label cardinality — why task_type label matters and why we must keep it bounded:
//   Every unique combination of label values creates a separate time series in
//   Prometheus's in-memory store. High-cardinality labels (e.g. raw user IDs,
//   task UUIDs, full request URLs) would create millions of series and exhaust
//   Prometheus memory. Labels used here — task type, worker ID, HTTP method,
//   route path, status code — have a small, fixed number of possible values,
//   which is safe. The "type" label is particularly useful because different
//   task types have different SLOs, so separating them allows per-type alerting.
//
// What the /metrics endpoint exposes:
//   The endpoint returns all registered metrics in the Prometheus exposition
//   text format. Each metric family starts with a # HELP line (description)
//   and a # TYPE line (counter/gauge/histogram), followed by one line per
//   labeled series with its current value. Histograms additionally emit
//   _bucket, _count, and _sum series.
//
// Connected to:
//   - cmd/api/main.go        — mounts the /metrics handler via promhttp.Handler()
//   - internal/monitoring/middleware.go — records HTTP request metrics
//   - internal/reliability/circuitbreaker.go — updates CircuitBreakerState
//   - internal/storage/storage.go — increments CacheHits / CacheMisses
//   - cmd/worker/main.go     — updates ActiveWorkers, TasksInProgress, etc.
//
// Called by:
//   - Every package that imports this one to record telemetry.
//   - promauto registers all metrics with the default Prometheus registry at
//     package init time (before main() runs) — no explicit registration needed.
package monitoring

import (
	"github.com/prometheus/client_golang/prometheus"
	// promauto is a convenience wrapper that both creates AND registers a metric
	// with the default global registry in a single call. Without promauto you
	// would have to call prometheus.MustRegister(metric) separately. promauto
	// runs that registration automatically during package initialization.
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// All metrics are declared as package-level variables so they can be incremented
// from any package that imports "monitoring". They are initialized once at startup
// by promauto during package init, which runs before main().
var (
	// ── Task metrics ──────────────────────────────────────────────────────────
	// These metrics track the full lifecycle of individual tasks as they move
	// through the queue: submitted → in-progress → processed (success or failure).

	// TasksSubmitted counts every task creation request accepted by the API,
	// labeled by task type (e.g. "image_resize", "image_compress").
	//
	// Type: Counter — values only increase.
	// PromQL example: rate(taskqueue_tasks_submitted_total[5m]) shows per-second
	// submission throughput, broken down by task type.
	//
	// Label "type": low-cardinality (~8 fixed task types). Allows per-type
	// throughput tracking without needing separate metric names.
	TasksSubmitted = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "taskqueue_tasks_submitted_total",
			Help: "Total number of tasks submitted",
		},
		[]string{"type"}, // label: task type string, e.g. "image_resize"
	)

	// TasksProcessed counts every task that has finished processing, labeled by
	// both task type and final status ("completed" or "failed").
	//
	// Type: Counter — use rate() to calculate throughput and the error ratio:
	//   rate(taskqueue_tasks_processed_total{status="failed"}[5m]) /
	//   rate(taskqueue_tasks_processed_total[5m])
	// A rising error ratio triggers on-call alerts before users file bug reports.
	TasksProcessed = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "taskqueue_tasks_processed_total",
			Help: "Total number of tasks processed",
		},
		// type="image_resize", status="completed" or "failed"
		[]string{"type", "status"},
	)

	// TasksInProgress is the live count of tasks a specific worker has started
	// but not yet finished. Workers call Inc() when they pick up a task and
	// Dec() when they finish (regardless of success or failure).
	//
	// Type: Gauge — values go up and down.
	// Label "worker_id": allows identifying a stuck worker — if a single worker's
	// gauge stays elevated for longer than the processing SLO, it may be hung.
	TasksInProgress = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "taskqueue_tasks_in_progress",
			Help: "Number of tasks currently being processed",
		},
		[]string{"worker_id"}, // label: unique ID of the worker holding the in-flight task
	)

	// TaskQueueLength is the current number of tasks waiting in the Redis Stream
	// to be picked up by a worker. Updated by the queue manager after each
	// enqueue or dequeue operation.
	//
	// Type: Gauge — a real-time snapshot of backlog depth.
	// Alert rule: fire when this value stays above a threshold for > N minutes,
	// meaning workers cannot keep up with the submission rate and need scaling.
	TaskQueueLength = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "taskqueue_queue_length",
			Help: "Current number of tasks in queue",
		},
	)

	// TaskProcessingDuration observes the wall-clock time each task takes from
	// the moment a worker picks it up until it finishes (success or error).
	//
	// Type: Histogram — enables percentile queries:
	//   histogram_quantile(0.99, rate(taskqueue_task_processing_duration_seconds_bucket[5m]))
	// A Gauge would only show current value; a Counter would only show total.
	// Only a Histogram gives the distribution needed for p50/p95/p99 SLO alerting.
	//
	// Buckets (ExponentialBuckets(0.1, factor=2, count=10)):
	//   0.1 s, 0.2 s, 0.4 s, 0.8 s, 1.6 s, 3.2 s, 6.4 s, 12.8 s, 25.6 s, 51.2 s
	// Covers fast in-memory transforms (~100 ms) through slow network downloads
	// (~30 s), with 2× resolution between buckets.
	//
	// Label "type": different task types have wildly different latency profiles
	// (thumbnail generation is fast; responsive image generation with 5 widths is
	// slow). Separating by type prevents mixing unrelated distributions, which
	// would make percentile calculations meaningless.
	TaskProcessingDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "taskqueue_task_processing_duration_seconds",
			Help: "Time taken to process tasks",
			// ExponentialBuckets(start=0.1, factor=2, count=10) — see comment above.
			Buckets: prometheus.ExponentialBuckets(0.1, 2, 10),
		},
		[]string{"type"}, // label: task type — keeps per-type latency distributions separate
	)

	// ── Worker metrics ────────────────────────────────────────────────────────
	// Workers are the goroutines or processes that consume tasks from the queue.
	// These metrics track their liveness and headcount.

	// ActiveWorkers is the number of worker processes currently registered and
	// running. Workers call Inc() on startup and Dec() on graceful shutdown.
	//
	// Type: Gauge — a live headcount.
	// Alert rule: fire when ActiveWorkers == 0, meaning no workers are running
	// and the queue is completely stalled.
	ActiveWorkers = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "taskqueue_active_workers",
			Help: "Number of active workers",
		},
	)

	// WorkerHeartbeats counts periodic liveness pings each worker sends to the
	// coordinator (e.g. every 30 s) to prove it is still alive and processing.
	//
	// Type: Counter — labeled by worker_id.
	// Alert rule: if increase(taskqueue_worker_heartbeats_total{worker_id="X"}[2m]) == 0
	// the worker has likely crashed and should be restarted.
	WorkerHeartbeats = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "taskqueue_worker_heartbeats_total",
			Help: "Total number of worker heartbeats",
		},
		[]string{"worker_id"}, // label: unique ID of the worker sending the heartbeat
	)

	// ── API metrics ───────────────────────────────────────────────────────────
	// These are populated by PrometheusMiddleware() in middleware.go, which wraps
	// every HTTP request. Both metrics share the same label triple so they can
	// be combined in PromQL expressions (e.g. to compute error rate alongside
	// request volume).

	// APIRequestDuration measures end-to-end HTTP handler latency in seconds,
	// bucketed using Prometheus default buckets (0.005 s → 10 s).
	//
	// Type: Histogram — use histogram_quantile() for per-route SLO dashboards.
	// Why measure at middleware level (not inside handlers): middleware wraps the
	// entire handler chain including JSON serialisation and error handling, so
	// it captures true client-perceived latency, not just business-logic time.
	//
	// Labels:
	//   "method"   — HTTP verb: GET, POST, PATCH, DELETE (low cardinality)
	//   "endpoint" — route path like /api/v1/tasks (use the route pattern, NOT
	//                the raw URL with embedded IDs — that would be high cardinality)
	//   "status"   — HTTP response status code as string: "200", "404", "500"
	//                (low cardinality; lets you exclude error responses when
	//                 computing latency SLOs so a wave of 500s doesn't skew p99)
	APIRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "taskqueue_api_request_duration_seconds",
			Help:    "API request duration",
			// DefBuckets: 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10 s
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "endpoint", "status"},
	)

	// APIRequestsTotal counts every HTTP request processed by the API.
	// Labels are identical to APIRequestDuration for easy cross-metric PromQL.
	//
	// Type: Counter.
	// PromQL examples:
	//   rate(taskqueue_api_requests_total[1m])             — requests per second
	//   rate(taskqueue_api_requests_total{status=~"5.."}[5m]) /
	//   rate(taskqueue_api_requests_total[5m])             — server error rate
	APIRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "taskqueue_api_requests_total",
			Help: "Total number of API requests",
		},
		[]string{"method", "endpoint", "status"},
	)

	// ── Database metrics ──────────────────────────────────────────────────────

	// DatabaseConnectionsInUse is the number of PostgreSQL connections currently
	// checked out from the connection pool and executing queries.
	//
	// Type: Gauge — a real-time pool utilisation snapshot.
	// Alert rule: if this value approaches sql.DB's MaxOpenConns setting, new
	// queries will block waiting for a free connection, causing latency spikes
	// that will surface in APIRequestDuration histograms.
	DatabaseConnectionsInUse = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "taskqueue_database_connections_in_use",
			Help: "Number of database connections in use",
		},
	)

	// ── Cache metrics ─────────────────────────────────────────────────────────
	// CacheHits and CacheMisses together let you compute the cache hit ratio:
	//   rate(taskqueue_cache_hits_total[5m]) /
	//   (rate(taskqueue_cache_hits_total[5m]) + rate(taskqueue_cache_misses_total[5m]))
	// A declining hit ratio may indicate cache eviction pressure (cache is too
	// small for the working set) or changed access patterns.

	// CacheHits counts every time a task lookup was served from the Redis cache,
	// avoiding a round-trip to PostgreSQL.
	//
	// Type: Counter — only increases; use rate() for hit throughput.
	CacheHits = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "taskqueue_cache_hits_total",
			Help: "Total number of cache hits",
		},
	)

	// CacheMisses counts every time a task lookup was NOT found in Redis,
	// requiring a full database read.
	//
	// Type: Counter — high miss rate may indicate cache TTL is too short or
	// that Redis memory pressure is evicting entries before they are reused.
	CacheMisses = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "taskqueue_cache_misses_total",
			Help: "Total number of cache misses",
		},
	)

	// ── Circuit breaker metrics ───────────────────────────────────────────────
	// A circuit breaker wraps calls to an external dependency (e.g. Redis or a
	// downstream storage service). When failures exceed a threshold it "opens",
	// rejecting further calls immediately instead of queuing up slow timeouts.
	// After a cooldown it transitions to "half-open" to probe recovery, then
	// either closes (healthy) or re-opens (still failing).
	//
	// State encoding used by CircuitBreakerState:
	//   0 = CLOSED   — healthy; all requests pass through to the dependency
	//   1 = HALF-OPEN — one probe request is allowed; result determines next state
	//   2 = OPEN     — all requests are rejected immediately (fail fast)

	// CircuitBreakerState records the current state of each named circuit breaker
	// as a numeric code (0/1/2 — see above).
	//
	// Type: Gauge — state is instantaneous; you want the current value, not rate.
	// Label "name": e.g. "redis", "postgres" — one series per protected dependency.
	// Alert rule: fire when value == 2 (OPEN) for more than N minutes.
	CircuitBreakerState = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "taskqueue_circuit_breaker_state",
			Help: "Circuit breaker state (0=closed, 1=half-open, 2=open)",
		},
		[]string{"name"}, // label: logical name of the protected dependency
	)

	// CircuitBreakerFailures counts every request that caused a circuit breaker
	// to record a failure event (i.e. the call to the dependency returned an error).
	//
	// Type: Counter — labeled by circuit breaker name.
	// Use: rate() shows how quickly a dependency is degrading. A rising failure
	// rate combined with CircuitBreakerState == 2 confirms the circuit opened due
	// to genuine failures, not a misconfiguration.
	CircuitBreakerFailures = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "taskqueue_circuit_breaker_failures_total",
			Help: "Total number of circuit breaker failures",
		},
		[]string{"name"}, // label: which circuit breaker recorded the failure
	)

	// ── Error metrics ─────────────────────────────────────────────────────────

	// ErrorsTotal counts every error across all system components, labeled by
	// originating component and error category.
	//
	// Type: Counter.
	// Labels:
	//   "component"  — where the error occurred: "broker", "storage", "processor"
	//   "error_type" — short descriptor: "connection_failed", "decode_error", etc.
	//
	// Usage examples:
	//   monitoring.ErrorsTotal.WithLabelValues("processor", "decode_failed").Inc()
	//   monitoring.ErrorsTotal.WithLabelValues("broker", "publish_timeout").Inc()
	//
	// PromQL: rate(taskqueue_errors_total[5m]) by (component) — shows which
	// subsystem is currently generating the most errors, guiding incident triage.
	ErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "taskqueue_errors_total",
			Help: "Total number of errors",
		},
		[]string{"component", "error_type"}, // labels: source component + error category
	)
)
