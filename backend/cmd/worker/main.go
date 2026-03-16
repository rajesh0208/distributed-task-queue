// Package main is the entry point for the distributed task queue worker process.
//
// This file (cmd/worker/main.go) starts one or more processing goroutines that
// consume tasks from a Redis Stream, process images, and write results back to
// PostgreSQL.
//
// What is a worker and why is it separate from the API?
//   The API server's job is to receive requests quickly and return responses
//   (target: < 50 ms). Image processing can take seconds to minutes per task.
//   If the API processed tasks inline, every HTTP handler would block for the
//   entire processing duration, exhausting the Fasthttp goroutine pool under load.
//   Separating concerns into a dedicated worker allows:
//   - Independent horizontal scaling (10 API pods, 50 worker pods).
//   - Isolated failure domains (a crashing worker doesn't affect the API).
//   - Back-pressure via the Redis Stream (tasks queue up without dropping requests).
//
// Concurrency model:
//   main() reads WORKER_CONCURRENCY (default: runtime.NumCPU()) and calls
//   NewWorker(concurrency). Start() then launches that many processLoop goroutines.
//   Each goroutine is a *different consumer* in the "workers" consumer group —
//   Redis Streams distributes messages across all registered consumers, so N
//   goroutines can process N tasks in parallel within a single process.
//
//   In production, multiple worker pods run in Kubernetes, each with their own
//   set of processLoop goroutines. The consumer group ensures no message is
//   delivered to more than one consumer at a time.
//
// Failure handling:
//   1. Transient errors → retries with exponential backoff (retries² seconds).
//      The task stays in "retrying" status in the DB; the UI shows progress.
//   2. Exhausted retries → task marked "failed" permanently; error stored in DB.
//   3. Circuit breaker opens → task is requeued to the Redis Stream for later.
//      This prevents a struggling external dependency from consuming all goroutines.
//   4. Cancellation signal → task is skipped if the API set "task:cancel:<id>" in Redis.
//   5. Panic in processor → captured by the circuit breaker's deferred recover and
//      counted as a failure, then requeued or retried.
//
// Graceful shutdown:
//   SIGTERM/SIGINT → cancel context → processLoops exit their for-select loops
//   → WaitGroup drains → broker + storage closed → exit 0.
//   Docker stop sends SIGTERM; the 30 s WaitGroup timeout matches Docker's kill
//   grace period so in-flight tasks finish before the container is hard-killed.
//
// It connects to:
//   - internal/broker     — consumes tasks via XREADGROUP
//   - internal/storage    — reads task state, writes results to PostgreSQL
//   - internal/processor  — runs the image processing pipeline
//   - internal/monitoring — increments Prometheus counters and gauges
//   - internal/reliability — circuit breaker wrapping processor calls
//   - internal/tracing    — creates worker spans as children of API spans
//   - internal/logging    — structured slog initialisation
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"

	"distributed-task-queue/internal/broker"
	"distributed-task-queue/internal/logging"
	"distributed-task-queue/internal/models"
	"distributed-task-queue/internal/monitoring"
	"distributed-task-queue/internal/processor"
	"distributed-task-queue/internal/reliability"
	"distributed-task-queue/internal/storage"
	"distributed-task-queue/internal/tracing"
)

// cancelKeyPrefix is the Redis key prefix used to signal task cancellation.
// The API server sets "task:cancel:{id}" when a client requests cancellation;
// the worker checks it before and after processing to avoid overwriting the status.
const cancelKeyPrefix = "task:cancel:"

// Worker owns all long-lived resources and orchestrates the process goroutines.
//
// Why a single struct instead of global variables?
//   A struct allows the entire worker to be cleanly shut down by cancelling a single
//   context and calling Close on each dependency. Global variables make it impossible
//   to instantiate two workers in the same process (needed for integration tests).
type Worker struct {
	id              string                       // unique worker ID (e.g. "worker-a1b2c3d4") set at startup and reported in heartbeats
	broker          broker.Broker                // Redis Stream consumer — XREADGROUP delivers tasks to handleTask
	storage         storage.Storage              // PostgreSQL + Redis — persists task state and reads it for ownership checks
	processor       *processor.ImageProcessor    // image processing pipeline (resize, compress, etc.)
	redisClient     *redis.Client                // direct Redis client for cancel-signal EXISTS checks (not through the broker interface)
	concurrency     int                          // number of processLoop goroutines to launch; defaults to runtime.NumCPU()
	ctx             context.Context              // cancelled by cancel() during Shutdown; signals all goroutines to stop
	cancel          context.CancelFunc           // called in Shutdown to initiate graceful drain
	wg              sync.WaitGroup               // tracks all goroutines (processLoops + heartbeatLoop + cleanupLoop) for Shutdown to wait on
	metrics         *models.WorkerMetrics        // in-memory snapshot of the worker's current status; flushed to DB on each heartbeat
	metricsLock     sync.RWMutex                 // guards metrics; RLock for reads in sendHeartbeat, Lock for writes in handleTask and Start
	heartbeatTicker *time.Ticker                 // fires every 10 s to trigger sendHeartbeat; stopped in Shutdown before context cancel
	circuitBreaker  *reliability.CircuitBreaker  // wraps processor calls; opens after 5 consecutive failures to prevent retry storms
	cbLogCache      map[string]time.Time         // rate-limits "circuit breaker open" log lines to once per 30 s per task ID
	cbLogCacheLock  sync.RWMutex                 // guards cbLogCache for concurrent reads from multiple processLoop goroutines
}

// NewWorker constructs a Worker by connecting to Redis (broker) and PostgreSQL
// (storage), then wires a circuit breaker around task processing so a stream of
// failing tasks cannot cascade into continuous retries.
//
// Dependency wiring order:
//   1. Redis broker first — the broker's underlying redis.Client is reused for
//      cancel-signal checks; it must exist before storage and auth.
//   2. PostgreSQL storage second — depends on Redis for write-through caching.
//   3. ImageProcessor — stateless; only needs a storage directory path.
//   4. Circuit breaker — wraps the processor so failures don't compound.
//
// Parameters:
//   concurrency — number of parallel processLoop goroutines to start.
//
// Returns:
//   *Worker, nil   — ready to Start().
//   nil, error     — if either Redis or Postgres connection fails; cancel() is called
//                    to clean up the already-created context before returning.
//
// Called by: main().
func NewWorker(concurrency int) (*Worker, error) {
	ctx, cancel := context.WithCancel(context.Background()) // root context; cancelled by Shutdown to stop all goroutines

	redisBroker, err := broker.NewRedisStreamBroker(
		getEnv("REDIS_ADDR", "localhost:6379"), // Redis address from env; falls back to localhost for local dev
		"task-queue",                            // Redis Stream key — same stream the API publishes to
		"workers",                               // consumer group name — all workers share this group; Redis distributes messages across them
	)
	if err != nil {
		cancel() // release the context before returning to prevent goroutine leak
		return nil, fmt.Errorf("failed to initialize broker: %w", err)
	}

	pgStorage, err := storage.NewPostgresStorage(
		getEnv("POSTGRES_DSN", "postgres://taskqueue_user:password@localhost/taskqueue?sslmode=disable"), // Postgres DSN
		getEnv("REDIS_ADDR", "localhost:6379"), // Redis for the storage cache layer (DB 1)
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	imageProcessor := processor.NewImageProcessor(
		getEnv("STORAGE_DIR", "./storage/images"),        // where to write processed output files
		getEnv("BASE_URL", "http://localhost:8080/images"), // base URL prefix for file download links stored in task results
	)

	// Build a unique worker ID for heartbeat tracking and log correlation.
	// Using only the first 8 chars of a UUID keeps IDs short in log lines.
	var idBuilder strings.Builder
	uuidStr := uuid.New().String()[:8]
	idBuilder.WriteString("worker-")
	idBuilder.WriteString(uuidStr)
	workerID := idBuilder.String()

	cb := reliability.NewCircuitBreaker(reliability.Settings{
		Name:        "task-processing",
		MaxRequests: 3,          // allow 3 test requests in HALF-OPEN to confirm recovery
		Interval:    10 * time.Second, // reset consecutive-failure count every 10 s in CLOSED state
		Timeout:     30 * time.Second, // stay OPEN for 30 s before probing with test requests
		ReadyToTrip: func(counts reliability.Counts) bool {
			return counts.ConsecutiveFailures > 5 // open circuit after 5 consecutive failures in a row
		},
		OnStateChange: func(name string, from, to reliability.State) {
			slog.Info("circuit breaker state change",
				slog.String("name", name),
				slog.String("from", fmt.Sprintf("%d", from)),
				slog.String("to", fmt.Sprintf("%d", to)),
			)
			monitoring.CircuitBreakerState.WithLabelValues(name).Set(float64(to)) // expose state as a Prometheus gauge (0=closed, 1=half-open, 2=open)
		},
	})

	return &Worker{
		id:          workerID,
		broker:      redisBroker,
		storage:     pgStorage,
		processor:   imageProcessor,
		redisClient: redisBroker.GetClient(), // reuse the broker's Redis connection pool — no second connection needed
		concurrency: concurrency,
		ctx:         ctx,
		cancel:      cancel,
		cbLogCache:  make(map[string]time.Time), // empty log-rate-limit cache; populated lazily by handleTask
		metrics: &models.WorkerMetrics{
			WorkerID:       workerID,
			Status:         "starting",           // transitions to "active" after Start() calls updateMetrics
			TasksProcessed: 0,                    // incremented by handleTask on each successful completion
			TasksFailed:    0,                    // incremented by handleTaskFailure on permanent failures
			StartTime:      time.Now(),           // used to compute uptime in the metrics dashboard
			LastHeartbeat:  time.Now(),           // updated on every heartbeat tick; stale if > 30 s means the worker is dead
		},
		heartbeatTicker: time.NewTicker(10 * time.Second), // 10-second interval matches the API's "stale worker" threshold
		circuitBreaker:  cb,
	}, nil
}

// Start launches the heartbeat, cleanup, and N processing goroutines (one per
// concurrency slot) then blocks until SIGINT or SIGTERM is received, at which
// point it calls Shutdown and returns.
//
// Goroutine layout after Start() returns from the initial setup:
//   - 1 heartbeatLoop goroutine — sends DB heartbeat every 10 s
//   - 1 cleanupLoop goroutine   — removes old files from disk once per hour
//   - N processLoop goroutines  — each is an independent Redis Stream consumer
//
// Why register goroutines with wg BEFORE launching them?
//   wg.Add(1) must be called before go f() to avoid a race where Shutdown calls
//   wg.Wait() before the goroutine had a chance to call wg.Add(1) itself.
//
// Returns:
//   error — from Shutdown(); nil if all goroutines drain cleanly within 30 s.
//
// Called by: main().
func (w *Worker) Start() error {
	slog.Info("worker starting", slog.String("worker_id", w.id), slog.Int("concurrency", w.concurrency))

	w.updateMetrics("active", nil) // update in-memory status before any goroutine touches it
	monitoring.ActiveWorkers.Inc() // Prometheus gauge: number of live worker processes

	w.wg.Add(1)
	go w.heartbeatLoop() // periodic DB heartbeat; exits when ctx is cancelled

	w.wg.Add(1)
	go w.cleanupLoop()   // periodic disk cleanup; exits when ctx is cancelled

	for i := range w.concurrency {
		w.wg.Add(1)
		go w.processLoop(i) // each gets a unique goroutineID for the consumer name (e.g. "worker-abc-goroutine-2")
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM) // SIGTERM from Docker/k8s stop; SIGINT from Ctrl+C
	<-sigChan // block until a termination signal arrives

	slog.Info("worker received shutdown signal", slog.String("worker_id", w.id))
	return w.Shutdown()
}

// processLoop runs a single consumer goroutine. Each goroutine is a separate
// consumer within the "workers" consumer group so Redis distributes messages
// across all of them — providing concurrency within one worker process.
//
// Why each goroutine has its own consumer name:
//   Redis Streams track which messages each named consumer has acknowledged.
//   If two goroutines shared a consumer name, Redis would consider them the same
//   consumer and could re-deliver a message to both, causing double processing.
//   Unique names (e.g. "worker-abc-goroutine-0", "worker-abc-goroutine-1") let
//   Redis track pending messages per-goroutine and reclaim stale ones correctly.
//
// Error handling:
//   Subscribe blocks while consuming. If it returns an error and the worker
//   context is not yet cancelled (i.e., not a shutdown-driven cancellation),
//   the goroutine sleeps 5 s and retries. This handles transient Redis blips
//   without spinning and without crashing the whole worker.
//
// Parameters:
//   goroutineID — 0-based index used to build the unique consumer name.
//
// Called by: Start() — one goroutine per concurrency slot.
func (w *Worker) processLoop(goroutineID int) {
	defer w.wg.Done() // signal Start's WaitGroup that this goroutine has exited

	consumerName := fmt.Sprintf("%s-goroutine-%d", w.id, goroutineID) // unique per goroutine

	for {
		select {
		case <-w.ctx.Done(): // Shutdown() cancelled the context — stop consuming and exit
			slog.Info("worker goroutine shutting down",
				slog.String("worker_id", w.id),
				slog.Int("goroutine_id", goroutineID),
			)
			return
		default:
			// broker.Subscribe blocks until a task is available, then calls handleTask.
			// When the context is cancelled, Subscribe returns immediately so the select
			// at the top of the next iteration catches ctx.Done().
			err := w.broker.Subscribe(w.ctx, "workers", consumerName, w.handleTask)
			if err != nil {
				if w.ctx.Err() != nil {
					return // context cancelled — normal shutdown path
				}
				if err != context.Canceled && err != context.DeadlineExceeded {
					slog.Error("subscription error",
						slog.String("worker_id", w.id),
						slog.Int("goroutine_id", goroutineID),
						slog.String("error", err.Error()),
					)
				}
				time.Sleep(5 * time.Second) // back off before retrying to avoid a tight error loop
			}
		}
	}
}

// handleTask is the broker.TaskHandler called for every message from the Redis Stream.
// It re-fetches the task from the DB to get its latest status, checks for cancellation,
// runs the processor inside the circuit breaker, and updates the DB on completion.
func (w *Worker) handleTask(ctx context.Context, task *models.Task) error {
	log := slog.With(slog.String("worker_id", w.id), slog.String("task_id", task.ID), slog.String("task_type", string(task.Type)))

	// Skip tasks already in a terminal state (can happen if the stream message was
	// redelivered after a worker crash that had already completed the task).
	if task.Status == models.StatusCompleted || task.Status == models.StatusFailed || task.Status == models.StatusCancelled {
		return nil
	}

	// Re-fetch to get the latest status from the DB — prevents double-processing if
	// another goroutine already picked this task up.
	latestTask, err := w.storage.GetTask(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("failed to fetch latest task status: %w", err)
	}

	// Terminal-state check after re-fetch.
	if latestTask.Status == models.StatusCompleted || latestTask.Status == models.StatusFailed {
		return nil
	}

	// Cancellation check: the API server sets a Redis key when a client cancels a task.
	// Checking both the DB status and the Redis signal covers the race window where the
	// API updates the DB but the worker has already passed the first status check.
	if latestTask.Status == models.StatusCancelled || w.isCancelled(ctx, task.ID) {
		log.Info("task cancelled before processing — skipping")
		if latestTask.Status != models.StatusCancelled {
			latestTask.Status = models.StatusCancelled
			_ = w.storage.UpdateTask(ctx, latestTask)
		}
		return nil
	}

	*task = *latestTask

	// Mark as processing so the UI can show progress.
	task.Status = models.StatusProcessing
	task.WorkerID = w.id
	now := time.Now()
	task.StartedAt = &now

	w.updateMetrics("active", &task.ID)
	monitoring.TasksInProgress.WithLabelValues(w.id).Inc()

	if err := w.storage.UpdateTask(ctx, task); err != nil {
		log.Warn("failed to update task status to processing", slog.String("error", err.Error()))
	}

	// Start an OTel span as a child of the span context extracted from the Redis message
	// by the broker (tracing.ExtractFromMap). This links the worker span to the API span.
	ctx, span := tracing.Start(ctx, "worker.handleTask")
	defer span.End()

	startTime := time.Now()
	var processingErr error

	err = w.circuitBreaker.Execute(func() error {
		processingErr = w.processor.ProcessTask(ctx, task)
		return processingErr
	})

	processingTime := time.Since(startTime).Milliseconds()
	task.ProcessingTime = processingTime
	monitoring.TaskProcessingDuration.WithLabelValues(string(task.Type)).Observe(time.Since(startTime).Seconds())

	if err != nil {
		if err == reliability.ErrCircuitOpen {
			w.cbLogCacheLock.RLock()
			lastLog, logged := w.cbLogCache[task.ID]
			w.cbLogCacheLock.RUnlock()

			if !logged || time.Since(lastLog) > 30*time.Second {
				log.Warn("circuit breaker open — requeuing task")
				w.cbLogCacheLock.Lock()
				w.cbLogCache[task.ID] = time.Now()
				if len(w.cbLogCache) > 1000 {
					for k, v := range w.cbLogCache {
						if time.Since(v) > 5*time.Minute {
							delete(w.cbLogCache, k)
						}
					}
				}
				w.cbLogCacheLock.Unlock()
			}
			return w.requeueTask(ctx, task)
		}

		log.Error("task processing failed", slog.String("error", processingErr.Error()))
		return w.handleTaskFailure(ctx, task, processingErr)
	}

	// Post-processing cancellation check: if the client cancelled while we were processing,
	// don't overwrite the cancelled status with completed.
	if w.isCancelled(ctx, task.ID) {
		log.Info("task cancelled during processing — discarding result")
		monitoring.TasksInProgress.WithLabelValues(w.id).Dec()
		return nil
	}

	task.Status = models.StatusCompleted
	completedAt := time.Now()
	task.CompletedAt = &completedAt

	if err := w.storage.UpdateTask(ctx, task); err != nil {
		log.Error("failed to update task to completed", slog.String("error", err.Error()))
	}

	w.metricsLock.Lock()
	w.metrics.TasksProcessed++
	w.metrics.CurrentTask = nil
	w.metricsLock.Unlock()

	monitoring.TasksInProgress.WithLabelValues(w.id).Dec()
	monitoring.TasksProcessed.WithLabelValues(string(task.Type), "completed").Inc()

	if processingTime > 1000 {
		log.Info("task completed", slog.Int64("processing_time_ms", processingTime))
	}

	return nil
}

// isCancelled checks whether the API server set a cancellation signal for this task.
// The signal is a Redis key with a TTL so it self-cleans even if the worker misses it.
func (w *Worker) isCancelled(ctx context.Context, taskID string) bool {
	n, err := w.redisClient.Exists(ctx, cancelKeyPrefix+taskID).Result()
	return err == nil && n > 0
}

// handleTaskFailure increments the retry counter and either marks the task as
// permanently failed (when MaxRetries is exhausted) or schedules a re-queue after
// an exponential backoff delay (retries² seconds) in a detached goroutine.
//
// Backoff schedule (retries² seconds):
//   attempt 1 → backoff 1 s
//   attempt 2 → backoff 4 s
//   attempt 3 → backoff 9 s
//   attempt N → backoff N² s
// Simple quadratic backoff is used instead of exponential to avoid extremely
// long waits for tasks with high MaxRetries values.
//
// Why a detached goroutine for the backoff + requeue?
//   handleTask is called from broker.Subscribe, which holds the Redis Stream ACK
//   until the handler returns. Sleeping in-line would block the ACK for the entire
//   backoff period, and the message would appear as "pending" in Redis — potentially
//   being reclaimed and re-delivered by another worker's pending-entry reclaimer.
//   By returning immediately (after setting status to "retrying") and re-queueing
//   in a background goroutine, the ACK is sent promptly and no double-delivery occurs.
//
//   The detached goroutine checks w.ctx.Done() so it exits cleanly on Shutdown.
//   It re-fetches the task before publishing to guard against concurrent cancellations
//   that may have changed the status during the backoff sleep.
//
// Parameters:
//   ctx  — request context (used for the initial UpdateTask call only).
//   task — the failing task with Retries already incremented.
//   err  — the processing error to store in task.Error.
//
// Returns:
//   error — from UpdateTask; nil means the retry has been scheduled.
//
// Called by: handleTask.
func (w *Worker) handleTaskFailure(ctx context.Context, task *models.Task, err error) error {
	task.Retries++           // count this attempt
	task.Error = err.Error() // store the error message so the UI and API can surface it

	monitoring.TasksProcessed.WithLabelValues(string(task.Type), "failed").Inc() // count failures by type in Prometheus

	if task.Retries >= task.MaxRetries {
		// All retries exhausted — permanently fail the task.
		task.Status = models.StatusFailed
		completedAt := time.Now()
		task.CompletedAt = &completedAt

		slog.Error("task failed permanently",
			slog.String("worker_id", w.id),
			slog.String("task_id", task.ID),
			slog.Int("retries", task.Retries),
		)

		w.metricsLock.Lock()
		w.metrics.TasksFailed++ // increment the in-memory counter for the next heartbeat
		w.metrics.CurrentTask = nil
		w.metricsLock.Unlock()

		monitoring.TasksInProgress.WithLabelValues(w.id).Dec() // this task is no longer in progress
		return w.storage.UpdateTask(ctx, task)                  // write terminal status to Postgres
	}

	// More retries available — schedule a re-queue after the backoff window.
	task.Status = models.StatusRetrying
	backoffDuration := time.Duration(task.Retries*task.Retries) * time.Second // quadratic: 1s, 4s, 9s, …

	if err := w.storage.UpdateTask(ctx, task); err != nil {
		return err // couldn't write "retrying" status — caller will NACK and the broker will redeliver
	}

	monitoring.TasksInProgress.WithLabelValues(w.id).Dec() // no longer actively processing; sleeping until requeue

	// Detached goroutine: sleep, then reset status to "queued" and re-publish.
	// Uses context.Background() (not the request ctx) so it outlives the handler call.
	go func() {
		select {
		case <-w.ctx.Done(): // worker is shutting down — abandon the retry; task stays in "retrying" until a new worker picks it up
			return
		case <-time.After(backoffDuration): // sleep the backoff window, then requeue
			retryCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// Re-fetch to guard against concurrent cancellations during the sleep window.
			updatedTask, err := w.storage.GetTask(retryCtx, task.ID)
			if err != nil {
				slog.Error("failed to fetch task for retry",
					slog.String("worker_id", w.id),
					slog.String("task_id", task.ID),
					slog.String("error", err.Error()),
				)
				return
			}

			if updatedTask.Status == models.StatusRetrying {
				// Still in "retrying" state — safe to re-queue.
				updatedTask.Status = models.StatusQueued
				if err := w.storage.UpdateTask(retryCtx, updatedTask); err != nil {
					slog.Error("failed to update task for retry", slog.String("task_id", task.ID), slog.String("error", err.Error()))
					return
				}
				if err := w.broker.Publish(retryCtx, updatedTask); err != nil {
					slog.Error("failed to requeue task", slog.String("task_id", task.ID), slog.String("error", err.Error()))
				}
				// If status changed (cancelled, completed by another worker), skip the re-queue silently.
			}
		}
	}()

	return nil
}

// requeueTask resets a task back to "queued" and re-publishes it to the Redis
// Stream when the circuit breaker is open and the worker cannot safely attempt
// processing. Tasks that are already terminal (completed/failed/cancelled) or not
// in an active state are skipped to prevent double-processing.
func (w *Worker) requeueTask(ctx context.Context, task *models.Task) error {
	latestTask, err := w.storage.GetTask(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("failed to fetch task for requeue: %w", err)
	}

	if latestTask.Status == models.StatusCompleted || latestTask.Status == models.StatusFailed || latestTask.Status == models.StatusCancelled {
		return nil
	}

	if latestTask.Status != models.StatusProcessing && latestTask.Status != models.StatusRetrying {
		return nil
	}

	latestTask.Status = models.StatusQueued
	if err := w.storage.UpdateTask(ctx, latestTask); err != nil {
		return err
	}

	monitoring.TasksInProgress.WithLabelValues(w.id).Dec()
	return w.broker.Publish(ctx, latestTask)
}

// cleanupLoop runs on an hourly ticker and removes processed image files that are
// older than 24 hours from the local storage directory. This prevents unbounded
// disk growth on long-running worker instances.
func (w *Worker) cleanupLoop() {
	defer w.wg.Done()
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			maxAge := 24 * time.Hour
			if err := w.processor.CleanupOldFiles(maxAge); err != nil {
				slog.Error("storage cleanup error", slog.String("error", err.Error()))
			} else {
				slog.Info("storage cleanup completed", slog.String("max_age", maxAge.String()))
			}
		}
	}
}

// heartbeatLoop fires sendHeartbeat on every tick of the heartbeatTicker (every
// 10 s). It exits cleanly when the worker context is cancelled during shutdown.
func (w *Worker) heartbeatLoop() {
	defer w.wg.Done()
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.heartbeatTicker.C:
			w.sendHeartbeat()
		}
	}
}

// sendHeartbeat snapshots the current metrics (status, goroutine count, last beat
// timestamp) under the metrics lock, then persists the snapshot to PostgreSQL via
// RecordWorkerHeartbeat so the API can surface live worker health. A Prometheus
// counter is also incremented so scrape gaps are detectable in Grafana.
func (w *Worker) sendHeartbeat() {
	w.metricsLock.Lock()
	w.metrics.LastHeartbeat = time.Now()
	w.metrics.ActiveGoroutines = runtime.NumGoroutine()
	snapshot := *w.metrics
	w.metricsLock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := w.storage.RecordWorkerHeartbeat(ctx, &snapshot); err != nil {
		slog.Warn("failed to send heartbeat", slog.String("worker_id", w.id), slog.String("error", err.Error()))
	}

	monitoring.WorkerHeartbeats.WithLabelValues(w.id).Inc()
}

// updateMetrics safely updates the in-memory status and current-task fields under
// the metrics write lock. It is called from multiple goroutines (Start, handleTask,
// Shutdown) so the lock is required even for simple field assignments.
func (w *Worker) updateMetrics(status string, currentTask *string) {
	w.metricsLock.Lock()
	w.metrics.Status = status
	w.metrics.CurrentTask = currentTask
	w.metricsLock.Unlock()
}

// Shutdown performs a graceful shutdown: it cancels the worker context (which
// signals all goroutines to stop), waits up to 30 s for them to drain, then
// closes the broker and storage connections and decrements the active-worker gauge.
func (w *Worker) Shutdown() error {
	slog.Info("worker shutting down gracefully", slog.String("worker_id", w.id))

	w.updateMetrics("stopping", nil)
	w.heartbeatTicker.Stop()
	w.cancel()

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("all goroutines stopped", slog.String("worker_id", w.id))
	case <-time.After(30 * time.Second):
		slog.Warn("shutdown timeout — forcing exit", slog.String("worker_id", w.id))
	}

	w.broker.Close()
	w.storage.Close()

	monitoring.ActiveWorkers.Dec()
	slog.Info("worker shutdown complete", slog.String("worker_id", w.id))
	return nil
}

// getEnv reads an environment variable and falls back to a default if not set.
// All worker configuration (Redis address, DB DSN, concurrency) is driven by
// environment variables so the binary can be deployed without code changes.
//
// Called by: NewWorker, main.
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" { // os.Getenv returns "" for unset variables
		return value
	}
	return defaultValue // safe default for local development
}

// main is the worker process entry point.
//
// Startup sequence:
//   1. Initialize structured logging (slog with JSON output in production).
//   2. Initialize OTel tracing (non-fatal — worker runs without tracing if Jaeger is unreachable).
//   3. Determine concurrency from WORKER_CONCURRENCY env, defaulting to runtime.NumCPU().
//   4. Create the Worker (connects to Redis and Postgres).
//   5. Call worker.Start() — this blocks until SIGTERM/SIGINT, then shuts down cleanly.
//
// Why default to NumCPU for concurrency?
//   Image processing is CPU-bound (encoding/decoding, resizing). One goroutine per
//   logical CPU maximises throughput without context-switching overhead. For I/O-bound
//   tasks, a higher multiplier (e.g., 4×NumCPU) would be appropriate.
func main() {
	logging.Init(getEnv("LOG_LEVEL", "info")) // configure slog level: "debug", "info", "warn", "error"

	// Init OTel tracing. If Jaeger is unreachable the error is non-fatal —
	// spans are dropped silently and the worker continues normally.
	shutdownTracing, err := tracing.Init("task-queue-worker")
	if err != nil {
		slog.Warn("tracing init failed — continuing without traces", slog.String("error", err.Error()))
	} else {
		defer shutdownTracing(context.Background()) // flush any buffered spans before the process exits
	}

	// Default to one goroutine per logical CPU; override via WORKER_CONCURRENCY env.
	concurrency := runtime.NumCPU()
	if envConcurrency := os.Getenv("WORKER_CONCURRENCY"); envConcurrency != "" {
		fmt.Sscanf(envConcurrency, "%d", &concurrency) // parse integer from env string
	}

	slog.Info("starting worker", slog.Int("concurrency", concurrency))

	worker, err := NewWorker(concurrency)
	if err != nil {
		slog.Error("failed to create worker", slog.String("error", err.Error()))
		os.Exit(1) // fatal: can't start without broker + storage
	}

	// Start() blocks until a signal is received and Shutdown() completes.
	if err := worker.Start(); err != nil {
		slog.Error("worker failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
