// File: cmd/worker/main.go
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
type Worker struct {
	id              string
	broker          broker.Broker
	storage         storage.Storage
	processor       *processor.ImageProcessor
	redisClient     *redis.Client    // direct client for cancel-signal checks
	concurrency     int
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	metrics         *models.WorkerMetrics
	metricsLock     sync.RWMutex
	heartbeatTicker *time.Ticker
	circuitBreaker  *reliability.CircuitBreaker
	cbLogCache      map[string]time.Time
	cbLogCacheLock  sync.RWMutex
}

// NewWorker constructs a Worker by connecting to Redis (broker) and PostgreSQL
// (storage), then wires a circuit breaker around task processing so a stream of
// failing tasks cannot cascade into continuous retries.
func NewWorker(concurrency int) (*Worker, error) {
	ctx, cancel := context.WithCancel(context.Background())

	redisBroker, err := broker.NewRedisStreamBroker(
		getEnv("REDIS_ADDR", "localhost:6379"),
		"task-queue",
		"workers",
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to initialize broker: %w", err)
	}

	pgStorage, err := storage.NewPostgresStorage(
		getEnv("POSTGRES_DSN", "postgres://taskqueue_user:password@localhost/taskqueue?sslmode=disable"),
		getEnv("REDIS_ADDR", "localhost:6379"),
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	imageProcessor := processor.NewImageProcessor(
		getEnv("STORAGE_DIR", "./storage/images"),
		getEnv("BASE_URL", "http://localhost:8080/images"),
	)

	var idBuilder strings.Builder
	uuidStr := uuid.New().String()[:8]
	idBuilder.WriteString("worker-")
	idBuilder.WriteString(uuidStr)
	workerID := idBuilder.String()

	cb := reliability.NewCircuitBreaker(reliability.Settings{
		Name:        "task-processing",
		MaxRequests: 3,
		Interval:    10 * time.Second,
		Timeout:     30 * time.Second,
		ReadyToTrip: func(counts reliability.Counts) bool {
			return counts.ConsecutiveFailures > 5
		},
		OnStateChange: func(name string, from, to reliability.State) {
			slog.Info("circuit breaker state change",
				slog.String("name", name),
				slog.String("from", string(from)),
				slog.String("to", string(to)),
			)
			monitoring.CircuitBreakerState.WithLabelValues(name).Set(float64(to))
		},
	})

	return &Worker{
		id:          workerID,
		broker:      redisBroker,
		storage:     pgStorage,
		processor:   imageProcessor,
		redisClient: redisBroker.GetClient(), // reuse broker's connection pool
		concurrency: concurrency,
		ctx:         ctx,
		cancel:      cancel,
		cbLogCache:  make(map[string]time.Time),
		metrics: &models.WorkerMetrics{
			WorkerID:       workerID,
			Status:         "starting",
			TasksProcessed: 0,
			TasksFailed:    0,
			StartTime:      time.Now(),
			LastHeartbeat:  time.Now(),
		},
		heartbeatTicker: time.NewTicker(10 * time.Second),
		circuitBreaker:  cb,
	}, nil
}

// Start launches the heartbeat, cleanup, and N processing goroutines (one per
// concurrency slot) then blocks until SIGINT or SIGTERM is received, at which
// point it calls Shutdown and returns.
func (w *Worker) Start() error {
	slog.Info("worker starting", slog.String("worker_id", w.id), slog.Int("concurrency", w.concurrency))

	w.updateMetrics("active", nil)
	monitoring.ActiveWorkers.Inc()

	w.wg.Add(1)
	go w.heartbeatLoop()

	w.wg.Add(1)
	go w.cleanupLoop()

	for i := range w.concurrency {
		w.wg.Add(1)
		go w.processLoop(i)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	slog.Info("worker received shutdown signal", slog.String("worker_id", w.id))
	return w.Shutdown()
}

// processLoop runs a single consumer goroutine. Each goroutine is a separate
// consumer within the "workers" consumer group so Redis distributes messages
// across all of them — providing concurrency within one worker process.
func (w *Worker) processLoop(goroutineID int) {
	defer w.wg.Done()

	consumerName := fmt.Sprintf("%s-goroutine-%d", w.id, goroutineID)

	for {
		select {
		case <-w.ctx.Done():
			slog.Info("worker goroutine shutting down",
				slog.String("worker_id", w.id),
				slog.Int("goroutine_id", goroutineID),
			)
			return
		default:
			err := w.broker.Subscribe(w.ctx, "workers", consumerName, w.handleTask)
			if err != nil {
				if w.ctx.Err() != nil {
					return
				}
				if err != context.Canceled && err != context.DeadlineExceeded {
					slog.Error("subscription error",
						slog.String("worker_id", w.id),
						slog.Int("goroutine_id", goroutineID),
						slog.String("error", err.Error()),
					)
				}
				time.Sleep(5 * time.Second)
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
func (w *Worker) handleTaskFailure(ctx context.Context, task *models.Task, err error) error {
	task.Retries++
	task.Error = err.Error()

	monitoring.TasksProcessed.WithLabelValues(string(task.Type), "failed").Inc()

	if task.Retries >= task.MaxRetries {
		task.Status = models.StatusFailed
		completedAt := time.Now()
		task.CompletedAt = &completedAt

		slog.Error("task failed permanently",
			slog.String("worker_id", w.id),
			slog.String("task_id", task.ID),
			slog.Int("retries", task.Retries),
		)

		w.metricsLock.Lock()
		w.metrics.TasksFailed++
		w.metrics.CurrentTask = nil
		w.metricsLock.Unlock()

		monitoring.TasksInProgress.WithLabelValues(w.id).Dec()
		return w.storage.UpdateTask(ctx, task)
	}

	task.Status = models.StatusRetrying
	backoffDuration := time.Duration(task.Retries*task.Retries) * time.Second

	if err := w.storage.UpdateTask(ctx, task); err != nil {
		return err
	}

	monitoring.TasksInProgress.WithLabelValues(w.id).Dec()

	go func() {
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(backoffDuration):
			retryCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

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
				updatedTask.Status = models.StatusQueued
				if err := w.storage.UpdateTask(retryCtx, updatedTask); err != nil {
					slog.Error("failed to update task for retry", slog.String("task_id", task.ID), slog.String("error", err.Error()))
					return
				}
				if err := w.broker.Publish(retryCtx, updatedTask); err != nil {
					slog.Error("failed to requeue task", slog.String("task_id", task.ID), slog.String("error", err.Error()))
				}
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

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func main() {
	logging.Init(getEnv("LOG_LEVEL", "info"))

	// Init OTel tracing. If Jaeger is unreachable the error is non-fatal —
	// spans are dropped silently and the worker continues normally.
	shutdownTracing, err := tracing.Init("task-queue-worker")
	if err != nil {
		slog.Warn("tracing init failed — continuing without traces", slog.String("error", err.Error()))
	} else {
		defer shutdownTracing(context.Background())
	}

	concurrency := runtime.NumCPU()
	if envConcurrency := os.Getenv("WORKER_CONCURRENCY"); envConcurrency != "" {
		fmt.Sscanf(envConcurrency, "%d", &concurrency)
	}

	slog.Info("starting worker", slog.Int("concurrency", concurrency))

	worker, err := NewWorker(concurrency)
	if err != nil {
		slog.Error("failed to create worker", slog.String("error", err.Error()))
		os.Exit(1)
	}

	if err := worker.Start(); err != nil {
		slog.Error("worker failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
