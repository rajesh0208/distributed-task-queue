// Package storage implements the persistence layer for the distributed task queue.
//
// This file is the single file that owns all database and cache I/O for tasks,
// users, workers, and batches. It sits between the HTTP handlers / workers and
// the underlying infrastructure (PostgreSQL + Redis).
//
// Architecture position:
//
//   HTTP handlers / workers
//          |
//          v
//   storage.Storage  (this interface / file)
//          |          |
//          v          v
//      PostgreSQL   Redis (DB 1)
//
// Why PostgreSQL is the source of truth, NOT Redis:
//   PostgreSQL gives ACID guarantees — a committed write is durable even if the
//   process crashes immediately afterwards. Redis is an in-memory store; data
//   can be lost on restart unless persistence (AOF/RDB) is enabled, and even
//   then it may lag. Therefore every mutation goes to PostgreSQL first. Redis is
//   used only as a read-through cache to serve hot GET requests faster, never as
//   the authoritative record.
//
// Write-through cache strategy (all four mutation operations):
//   CreateTask  — writes to PG, then caches the new task in Redis.
//   GetTask     — checks Redis first (cache hit returns immediately, no PG query);
//                 on cache miss, reads from PG and writes the result into Redis.
//   UpdateTask  — writes to PG, then DELetes the stale Redis key and re-caches
//                 the updated task so subsequent reads see the fresh value.
//   DeleteTask  — deletes from PG, then DELetes the Redis key so stale data
//                 cannot be served after the row is gone.
//
// Redis DB isolation:
//   DB 0 — message broker (Asynq / raw queue lists). Managed by the broker package.
//   DB 1 — task/user cache managed by this file (PostgresStorage).
//   DB 2 — generic distributed cache managed by cache.DistributedCache.
//   Keeping them in separate logical databases means a FLUSHDB on one namespace
//   never touches the others, and monitoring can inspect each namespace independently.
//
// Called by:
//   internal/api/handlers — HTTP layer reads/writes tasks and users via the Storage interface.
//   internal/worker       — workers update task status via UpdateTask.
//   cmd/server/main.go    — constructs a *PostgresStorage at startup.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"distributed-task-queue/internal/models"
)

// Storage is an interface, not a concrete struct, for two important reasons:
//
//  1. Testability: test code can provide a mock (e.g. an in-memory map) that
//     satisfies this interface without needing a real PostgreSQL instance.
//  2. Swappability: if the backing store ever changes (e.g. to MySQL or a
//     cloud-managed DB), only the concrete type changes; every caller that
//     depends on the interface requires zero modifications.
//
// Every method that could fail returns an error so callers can handle failures
// without inspecting internal state.
type Storage interface {
	// CreateTask persists a new task. Returns an error if the insert fails
	// (e.g. duplicate ID, constraint violation).
	CreateTask(ctx context.Context, task *models.Task) error

	// GetTask fetches a task by its UUID. Returns an error if not found.
	GetTask(ctx context.Context, taskID string) (*models.Task, error)

	// UpdateTask overwrites the mutable fields of an existing task (status,
	// result, error, retries, timestamps, workerID, processingTime).
	UpdateTask(ctx context.Context, task *models.Task) error

	// ListTasks returns a filtered, paginated slice of tasks and the total
	// number of matching rows (needed by pagination UI).
	ListTasks(ctx context.Context, status models.TaskStatus, userID string, limit, offset int) ([]*models.Task, int64, error)

	// DeleteTask hard-deletes a task row. Returns an error if not found.
	DeleteTask(ctx context.Context, taskID string) error

	// GetMetrics returns an aggregated system snapshot (queue depths, worker
	// count, throughput, average processing time).
	GetMetrics(ctx context.Context) (*models.SystemMetrics, error)

	// RecordWorkerHeartbeat upserts a worker's liveness record. Called by each
	// worker on a regular interval so the metrics query can distinguish alive
	// workers from stale ones.
	RecordWorkerHeartbeat(ctx context.Context, metrics *models.WorkerMetrics) error

	// CreateUser inserts a new user. Supports both password-auth and OAuth users.
	CreateUser(ctx context.Context, user *models.User) error

	// GetUserByUsername looks up a user by their unique username (login form).
	GetUserByUsername(ctx context.Context, username string) (*models.User, error)

	// GetUserByEmail looks up a user by email (used during OAuth account linking).
	GetUserByEmail(ctx context.Context, email string) (*models.User, error)

	// GetUserByID retrieves a user by their UUID primary key (session validation).
	GetUserByID(ctx context.Context, userID string) (*models.User, error)

	// GetUserByOAuth looks up a user by their OAuth (provider, providerID) pair
	// (OAuth callback — find existing linked account before creating a new one).
	GetUserByOAuth(ctx context.Context, provider, providerID string) (*models.User, error)

	// UpdateUser persists changes to username, email, password, api_key, roles.
	UpdateUser(ctx context.Context, user *models.User) error

	// Batch operations — group multiple tasks under one tracking ID.
	CreateBatch(ctx context.Context, batch *models.Batch) error
	GetBatch(ctx context.Context, batchID string) (*models.Batch, error)

	// DeleteUser hard-deletes a user record.
	DeleteUser(ctx context.Context, userID string) error

	// ListUsers returns a paginated user list (admin endpoints).
	ListUsers(ctx context.Context, limit, offset int) ([]*models.User, int64, error)

	// GetDB exposes the raw *sql.DB handle. This is intentionally narrow — most
	// callers should use the typed methods above. The raw handle is needed by
	// external tooling such as the Prometheus sql_db_stats collector that
	// interrogates pool statistics directly via the standard library interface.
	GetDB() *sql.DB

	// Close gracefully shuts down both the PostgreSQL connection pool and the
	// Redis client. Called once on process shutdown.
	Close() error
}

// PostgresStorage is the production implementation of the Storage interface.
// It owns a PostgreSQL connection pool (via sqlx) and a Redis client for caching.
//
// Fields:
//   db    — sqlx-wrapped *sql.DB for the PostgreSQL connection pool. sqlx is
//            chosen over raw database/sql because it adds struct-scanning
//            (GetContext / SelectContext), eliminating manual Scan() calls and
//            reducing mapping boilerplate without changing the underlying driver.
//   cache — Redis client pointed at DB 1. This is the task/user cache layer.
//            DB 1 is isolated from the message broker (DB 0) and the generic
//            distributed cache (DB 2) so operations in one logical database
//            cannot pollute or accidentally flush another.
type PostgresStorage struct {
	db    *sqlx.DB      // PostgreSQL connection pool — source of truth for all data
	cache *redis.Client // Redis DB 1 — read-through cache for hot task lookups
}

// NewPostgresStorage opens a PostgreSQL connection pool, connects a Redis client
// (DB 1, separate from the broker's DB 0) for task caching, and runs initSchema
// to create any missing tables. Connection pool sizing can be tuned via
// DB_MAX_OPEN_CONNS and DB_MAX_IDLE_CONNS environment variables.
//
// Parameters:
//   dsn       — PostgreSQL connection string (e.g. "postgres://user:pass@host/db?sslmode=disable")
//   redisAddr — Redis address in host:port form (e.g. "localhost:6379")
//
// Returns:
//   *PostgresStorage — ready-to-use storage instance
//   error            — non-nil if the DB or Redis connection fails, or schema init fails
//
// Called by: cmd/server/main.go at startup.
func NewPostgresStorage(dsn, redisAddr string) (*PostgresStorage, error) {
	// sqlx.Connect opens the driver AND immediately verifies the connection with
	// a Ping. Failing fast here surfaces misconfigured DSNs at startup rather
	// than at the first request.
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		// Wrap the error so callers see both the context ("failed to connect to
		// PostgreSQL") and the root cause from the driver.
		return nil, fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}

	// Sensible defaults for the connection pool. These match the PostgreSQL
	// server's default max_connections (100) while leaving headroom for other
	// clients. The defaults can be overridden per-deployment via env vars.
	maxOpenConns := 25          // maximum total connections kept open
	maxIdleConns := 5           // connections kept idle (ready to reuse) in the pool
	connMaxLifetime := 5 * time.Minute  // hard upper bound on connection age
	connMaxIdleTime := 10 * time.Minute // how long an idle connection stays in the pool

	// Allow operators to tune pool size at deploy time without recompiling.
	if envMaxOpen := os.Getenv("DB_MAX_OPEN_CONNS"); envMaxOpen != "" {
		fmt.Sscanf(envMaxOpen, "%d", &maxOpenConns)
	}
	if envMaxIdle := os.Getenv("DB_MAX_IDLE_CONNS"); envMaxIdle != "" {
		fmt.Sscanf(envMaxIdle, "%d", &maxIdleConns)
	}

	// Apply pool settings to the underlying *sql.DB.
	// For detailed reasoning on each value, see internal/database/pool.go.
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxLifetime(connMaxLifetime)
	db.SetConnMaxIdleTime(connMaxIdleTime)

	// Connect to Redis DB 1 (task/user cache).
	// DB isolation rationale:
	//   DB 0 is used by the message broker (Asynq queue lists, job state). If
	//   the cache and the broker shared the same DB, a FLUSHDB for cache
	//   maintenance would also destroy the job queue — catastrophic. DB 1 is
	//   dedicated to PostgresStorage cache entries only.
	cache := redis.NewClient(&redis.Options{
		Addr:               redisAddr,
		DB:                 1,           // DB 1: task/user cache — isolated from broker (DB 0)
		PoolSize:           10,          // max simultaneous Redis connections from this process
		MinIdleConns:       5,           // pre-warm at least 5 connections so the first requests don't wait
		DialTimeout:        5 * time.Second, // give up trying to open a new TCP connection after 5 s
		ReadTimeout:        3 * time.Second, // max time to wait for a Redis reply
		WriteTimeout:       3 * time.Second, // max time to wait for a write to be acknowledged
		PoolTimeout:        4 * time.Second, // how long to wait for a connection from the pool if all are busy
		IdleTimeout:        5 * time.Minute, // close idle connections older than this to free Redis server resources
		IdleCheckFrequency: 1 * time.Minute, // how often to scan the pool for idle-timed-out connections
		MaxRetries:         3,           // automatically retry transient network errors up to 3 times
	})

	storage := &PostgresStorage{db: db, cache: cache}

	// Ensure all required tables and indexes exist. initSchema is idempotent —
	// using CREATE TABLE IF NOT EXISTS / CREATE INDEX IF NOT EXISTS means it is
	// safe to call on every startup, including against an already-migrated DB.
	if err := storage.initSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return storage, nil
}

// initSchema runs CREATE TABLE IF NOT EXISTS and CREATE INDEX IF NOT EXISTS
// statements at startup. It is idempotent — safe to call on every boot even
// when the schema already exists.
//
// Design notes:
//   - ALTER TABLE ... ADD COLUMN IF NOT EXISTS statements handle schema
//     evolution for existing deployments without a separate migration tool.
//   - Partial indexes (WHERE worker_id IS NOT NULL, WHERE batch_id IS NOT NULL)
//     are smaller and faster than full indexes when the indexed column is sparse.
//   - The composite index idx_tasks_status_created supports the most common
//     ListTasks query pattern: filter by status, sort by created_at DESC.
//
// Called by: NewPostgresStorage.
func (s *PostgresStorage) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS tasks (
		id VARCHAR(36) PRIMARY KEY,
		user_id VARCHAR(36) NOT NULL DEFAULT '',
		type VARCHAR(50) NOT NULL,
		payload JSONB NOT NULL,
		status VARCHAR(20) NOT NULL DEFAULT 'queued',
		priority INTEGER DEFAULT 0,
		result JSONB,
		error TEXT,
		retries INTEGER DEFAULT 0,
		max_retries INTEGER DEFAULT 3,
		created_at TIMESTAMP NOT NULL DEFAULT NOW(),
		started_at TIMESTAMP,
		completed_at TIMESTAMP,
		worker_id VARCHAR(50),
		processing_time BIGINT
	);

	ALTER TABLE tasks ADD COLUMN IF NOT EXISTS user_id VARCHAR(36) NOT NULL DEFAULT '';

	CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
	CREATE INDEX IF NOT EXISTS idx_tasks_created_at ON tasks(created_at);
	CREATE INDEX IF NOT EXISTS idx_tasks_type ON tasks(type);
	CREATE INDEX IF NOT EXISTS idx_tasks_user_id ON tasks(user_id);
	CREATE INDEX IF NOT EXISTS idx_tasks_status_created ON tasks(status, created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_tasks_worker_status ON tasks(worker_id, status) WHERE worker_id IS NOT NULL;

	CREATE TABLE IF NOT EXISTS workers (
		worker_id VARCHAR(50) PRIMARY KEY,
		status VARCHAR(20) NOT NULL,
		tasks_processed INTEGER DEFAULT 0,
		tasks_failed INTEGER DEFAULT 0,
		last_heartbeat TIMESTAMP NOT NULL,
		start_time TIMESTAMP NOT NULL,
		current_task VARCHAR(36),
		active_goroutines INTEGER DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS users (
		id VARCHAR(36) PRIMARY KEY,
		username VARCHAR(50) UNIQUE NOT NULL,
		email VARCHAR(100) UNIQUE NOT NULL,
		password_hash VARCHAR(255),
		api_key VARCHAR(64) UNIQUE,
		roles TEXT[] DEFAULT ARRAY['user'],
		oauth_provider VARCHAR(20),
		oauth_id VARCHAR(200),
		created_at TIMESTAMP NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMP NOT NULL DEFAULT NOW()
	);

	ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;
	ALTER TABLE users ADD COLUMN IF NOT EXISTS oauth_provider VARCHAR(20);
	ALTER TABLE users ADD COLUMN IF NOT EXISTS oauth_id VARCHAR(200);

	CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
	CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
	CREATE INDEX IF NOT EXISTS idx_users_api_key ON users(api_key);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_users_oauth ON users(oauth_provider, oauth_id) WHERE oauth_provider IS NOT NULL;

	CREATE TABLE IF NOT EXISTS batches (
		id           VARCHAR(36) PRIMARY KEY,
		user_id      VARCHAR(36) NOT NULL,
		type         VARCHAR(50) NOT NULL,
		total        INTEGER NOT NULL,
		created_at   TIMESTAMP NOT NULL DEFAULT NOW(),
		completed_at TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_batches_user_id ON batches(user_id);

	ALTER TABLE tasks ADD COLUMN IF NOT EXISTS batch_id VARCHAR(36);
	CREATE INDEX IF NOT EXISTS idx_tasks_batch_id ON tasks(batch_id) WHERE batch_id IS NOT NULL;
	`
	_, err := s.db.Exec(schema)
	return err
}

// CreateTask inserts a new task row into PostgreSQL and immediately caches it in Redis.
// batch_id is stored as NULL when the task was not submitted as part of a batch.
//
// Write-through cache for CREATE:
//   The task is inserted into PostgreSQL first (authoritative write), then a
//   copy is pushed to Redis via cacheTask. This means the very next GetTask call
//   for this ID will be served from Redis without hitting the database — useful
//   when a producer and consumer are close in time (common in high-throughput
//   pipelines).
//
// Parameters:
//   ctx  — request-scoped context; cancellation aborts the DB write.
//   task — fully populated task including ID, UserID, Type, Payload, Status, etc.
//
// Returns error if the INSERT fails (e.g. duplicate primary key).
//
// Called by: internal/api/handlers.CreateTaskHandler.
func (s *PostgresStorage) CreateTask(ctx context.Context, task *models.Task) error {
	query := `
		INSERT INTO tasks (id, user_id, batch_id, type, payload, status, priority, retries, max_retries, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	// batch_id is optional: when the task is not part of a batch, passing nil
	// writes a SQL NULL instead of an empty string, keeping the column semantics
	// clean and allowing the partial index (WHERE batch_id IS NOT NULL) to remain small.
	var batchID interface{}
	if task.BatchID != "" {
		batchID = task.BatchID
	}
	_, err := s.db.ExecContext(ctx, query,
		task.ID, task.UserID, batchID, task.Type, task.Payload, task.Status,
		task.Priority, task.Retries, task.MaxRetries, task.CreatedAt,
	)
	if err != nil {
		// Wrapping preserves the original driver error (e.g. pq: duplicate key)
		// while adding context for log aggregation.
		return fmt.Errorf("failed to create task: %w", err)
	}

	// Write-through: cache the task immediately after a successful DB insert so
	// the next GetTask call for this ID is a cache hit.
	s.cacheTask(task)
	return nil
}

// GetTask fetches a task by ID, checking the Redis cache first (cache hit path)
// to reduce database load. On a cache miss the task is fetched from PostgreSQL
// and written back into the cache before returning.
//
// Hot path (cache hit):
//   Redis GET completes in sub-millisecond time on a local network. When a task
//   is frequently polled (e.g. a client polling for completion), cache hits mean
//   zero PostgreSQL queries — the DB is never touched. This is the primary
//   purpose of the write-through cache strategy.
//
// Cold path (cache miss):
//   Reads from PostgreSQL. The result is re-cached so the second and all
//   subsequent requests for the same task ID become cache hits.
//
// Parameters:
//   ctx    — request-scoped context.
//   taskID — UUID of the task to retrieve.
//
// Returns:
//   *models.Task — populated task or nil on error.
//   error        — "task not found: <id>" if the row does not exist in PG.
//
// Called by: internal/api/handlers, internal/worker.
func (s *PostgresStorage) GetTask(ctx context.Context, taskID string) (*models.Task, error) {
	// Build the Redis key in the format "task:<uuid>". Namespacing with "task:"
	// prevents key collisions with other entity types stored in the same DB 1.
	cacheKey := "task:" + taskID

	// Use a short, separate context for the Redis call so a slow or unavailable
	// Redis does not block the entire request for longer than 2 seconds. If
	// Redis is down, we fall through to PostgreSQL gracefully.
	cacheCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Attempt cache hit — if Redis returns a value and we can deserialise it,
	// return immediately without touching PostgreSQL (hot path).
	if cached, err := s.cache.Get(cacheCtx, cacheKey).Result(); err == nil {
		var task models.Task
		if err := json.Unmarshal([]byte(cached), &task); err == nil {
			// Cache hit: return the cached task. Zero DB queries.
			return &task, nil
		}
		// If unmarshal fails the cached bytes are corrupt; fall through to DB.
	}
	// Cache miss (or Redis error): continue to the PostgreSQL query below.

	// taskRow is a local struct that maps exactly to the SQL result set.
	// We use sql.NullString / sql.NullInt64 for columns that can be NULL in the
	// database (result, error, worker_id, processing_time) so that sqlx does not
	// panic trying to scan NULL into a plain string or int.
	type taskRow struct {
		ID             string          `db:"id"`
		UserID         string          `db:"user_id"`
		Type           string          `db:"type"`
		Payload        json.RawMessage `db:"payload"`
		Status         string          `db:"status"`
		Priority       int             `db:"priority"`
		Result         sql.NullString  `db:"result"`          // NULL when task has not produced a result yet
		Error          sql.NullString  `db:"error"`           // NULL when task has not errored
		Retries        int             `db:"retries"`
		MaxRetries     int             `db:"max_retries"`
		CreatedAt      time.Time       `db:"created_at"`
		StartedAt      *time.Time      `db:"started_at"`      // pointer: NULL until a worker picks up the task
		CompletedAt    *time.Time      `db:"completed_at"`    // pointer: NULL until the task finishes
		WorkerID       sql.NullString  `db:"worker_id"`       // NULL when not yet assigned to a worker
		ProcessingTime sql.NullInt64   `db:"processing_time"` // NULL until the task completes
	}

	var row taskRow
	query := `
		SELECT id, user_id, type, payload, status, priority, result, error, retries, max_retries,
		       created_at, started_at, completed_at, worker_id, processing_time
		FROM tasks WHERE id = $1
	`
	if err := s.db.GetContext(ctx, &row, query, taskID); err != nil {
		if err == sql.ErrNoRows {
			// Distinguish "not found" from other DB errors so callers can return
			// HTTP 404 instead of HTTP 500.
			return nil, fmt.Errorf("task not found: %s", taskID)
		}
		return nil, fmt.Errorf("failed to get task: %w", err)
	}

	// Convert sql.NullString to json.RawMessage for the Result field only when
	// the DB column is not NULL; otherwise leave resultJSON nil.
	var resultJSON json.RawMessage
	if row.Result.Valid {
		resultJSON = json.RawMessage(row.Result.String)
	}

	task := &models.Task{
		ID:             row.ID,
		UserID:         row.UserID,
		Type:           models.TaskType(row.Type),
		Payload:        row.Payload,
		Status:         models.TaskStatus(row.Status),
		Priority:       row.Priority,
		Result:         resultJSON,
		Error:          row.Error.String,           // empty string when sql.NullString.Valid == false
		Retries:        row.Retries,
		MaxRetries:     row.MaxRetries,
		CreatedAt:      row.CreatedAt,
		StartedAt:      row.StartedAt,
		CompletedAt:    row.CompletedAt,
		WorkerID:       row.WorkerID.String,
		ProcessingTime: row.ProcessingTime.Int64,
	}

	// Write-through: populate the cache so the next GetTask for this ID is a
	// cache hit. cacheTask applies the appropriate TTL based on task status.
	s.cacheTask(task)
	return task, nil
}

// UpdateTask persists the task's mutable fields (status, result, error, retries,
// timestamps, workerID, processingTime) to PostgreSQL and invalidates the Redis
// cache entry so the next GetTask reads fresh data.
//
// Write-through cache for UPDATE:
//   Step 1 — Write to PostgreSQL (source of truth updated atomically).
//   Step 2 — DEL the old Redis key so stale data cannot be served.
//   Step 3 — Re-cache the updated task with the correct TTL for its new status.
//   (Steps 2 and 3 are done via Del + cacheTask to ensure atomicity of the
//    cache refresh; a plain SET without DEL could race with a concurrent reader.)
//
// Parameters:
//   ctx  — request-scoped context.
//   task — task with updated fields. The ID must match an existing row.
//
// Returns error if the UPDATE query fails.
//
// Called by: internal/worker (when a task transitions state).
func (s *PostgresStorage) UpdateTask(ctx context.Context, task *models.Task) error {
	query := `
		UPDATE tasks
		SET status = $2, result = $3, error = $4, retries = $5,
		    started_at = $6, completed_at = $7, worker_id = $8, processing_time = $9
		WHERE id = $1
	`
	// Pass nil instead of an empty slice for result so the DB column is set to
	// NULL rather than a zero-length JSON value when the task has no result yet.
	var result interface{}
	if len(task.Result) > 0 {
		result = task.Result
	}
	_, err := s.db.ExecContext(ctx, query,
		task.ID, task.Status, result, task.Error, task.Retries,
		task.StartedAt, task.CompletedAt, task.WorkerID, task.ProcessingTime,
	)
	if err != nil {
		return fmt.Errorf("failed to update task: %w", err)
	}

	// Invalidate the stale cache entry first so that any concurrent read sees
	// either the old value (before the DEL) or the new value (after cacheTask),
	// never a corrupt in-between state.
	cacheCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.cache.Del(cacheCtx, "task:"+task.ID) // DEL before re-caching; avoids a race window with stale data

	// Re-cache with the updated task. TTL is re-evaluated based on new status —
	// a task that just transitioned to "completed" now gets the longer 10-minute TTL.
	s.cacheTask(task)
	return nil
}

// ListTasks returns a paginated, optionally filtered page of tasks. Filtering by
// status and/or userID is supported; both may be empty to list all tasks.
// Total row count is returned alongside the page so callers can compute pagination.
//
// COUNT(*) OVER() window function — why one query beats two:
//   A naive approach uses two queries: SELECT COUNT(*) and SELECT ... LIMIT/OFFSET.
//   That requires two round trips and two query plan executions. The window
//   function COUNT(*) OVER() computes the total count across the full filtered
//   result set in the same query plan as the paginated rows. PostgreSQL only
//   scans the relevant index once. The total is available from results[0].TotalCount
//   — every row carries it, so reading the first element is sufficient.
//
// Parameters:
//   ctx    — request-scoped context.
//   status — filter by task status; empty string means no filter.
//   userID — filter by owning user; empty string means no filter.
//   limit  — page size.
//   offset — number of rows to skip (page * limit).
//
// Returns:
//   []*models.Task — the page of tasks.
//   int64          — total matching rows (for pagination math).
//   error          — non-nil if the query fails.
//
// Called by: internal/api/handlers.ListTasksHandler.
func (s *PostgresStorage) ListTasks(ctx context.Context, status models.TaskStatus, userID string, limit, offset int) ([]*models.Task, int64, error) {
	var query string
	var args []interface{}

	// Four query variants are selected based on which filter fields are non-empty.
	// Using a switch avoids building the WHERE clause dynamically with string
	// concatenation, which is harder to read and more prone to SQL injection risk
	// if filters are ever expanded. All placeholders use positional $N parameters
	// (the pq driver does not support named parameters).
	switch {
	case status != "" && userID != "":
		// Both filters active: return tasks for a specific user in a specific status.
		query = `
			SELECT id, user_id, type, payload, status, priority, result, error, retries, max_retries,
			       created_at, started_at, completed_at, worker_id, processing_time,
			       COUNT(*) OVER() as total_count
			FROM tasks WHERE status = $1 AND user_id = $2
			ORDER BY created_at DESC LIMIT $3 OFFSET $4
		`
		args = []interface{}{status, userID, limit, offset}
	case status != "":
		// Status filter only: used by admin endpoints to view all tasks in a given state.
		query = `
			SELECT id, user_id, type, payload, status, priority, result, error, retries, max_retries,
			       created_at, started_at, completed_at, worker_id, processing_time,
			       COUNT(*) OVER() as total_count
			FROM tasks WHERE status = $1
			ORDER BY created_at DESC LIMIT $2 OFFSET $3
		`
		args = []interface{}{status, limit, offset}
	case userID != "":
		// UserID filter only: return all tasks for a given user regardless of status.
		query = `
			SELECT id, user_id, type, payload, status, priority, result, error, retries, max_retries,
			       created_at, started_at, completed_at, worker_id, processing_time,
			       COUNT(*) OVER() as total_count
			FROM tasks WHERE user_id = $1
			ORDER BY created_at DESC LIMIT $2 OFFSET $3
		`
		args = []interface{}{userID, limit, offset}
	default:
		// No filters: full table scan with pagination (admin list-all).
		query = `
			SELECT id, user_id, type, payload, status, priority, result, error, retries, max_retries,
			       created_at, started_at, completed_at, worker_id, processing_time,
			       COUNT(*) OVER() as total_count
			FROM tasks ORDER BY created_at DESC LIMIT $1 OFFSET $2
		`
		args = []interface{}{limit, offset}
	}

	// taskRow extends the single-task row type with TotalCount which carries the
	// window function result. Every row in the result set has the same
	// total_count value — we only read it once from results[0].
	type taskRow struct {
		ID             string          `db:"id"`
		UserID         string          `db:"user_id"`
		Type           string          `db:"type"`
		Payload        json.RawMessage `db:"payload"`
		Status         string          `db:"status"`
		Priority       int             `db:"priority"`
		Result         sql.NullString  `db:"result"`
		Error          sql.NullString  `db:"error"`
		Retries        int             `db:"retries"`
		MaxRetries     int             `db:"max_retries"`
		CreatedAt      time.Time       `db:"created_at"`
		StartedAt      *time.Time      `db:"started_at"`
		CompletedAt    *time.Time      `db:"completed_at"`
		WorkerID       sql.NullString  `db:"worker_id"`
		ProcessingTime sql.NullInt64   `db:"processing_time"`
		TotalCount     int64           `db:"total_count"` // window function result — same on every row
	}

	var results []taskRow
	if err := s.db.SelectContext(ctx, &results, query, args...); err != nil {
		return nil, 0, fmt.Errorf("failed to list tasks: %w", err)
	}
	if len(results) == 0 {
		// Empty result set is not an error; return zero total so the caller
		// renders an empty page rather than showing a stale count.
		return nil, 0, nil
	}

	tasks := make([]*models.Task, len(results))
	// Read total count from the first row — all rows carry the same value from
	// the window function so reading index 0 is always correct.
	total := results[0].TotalCount
	for i := range results {
		var resultJSON json.RawMessage
		if results[i].Result.Valid {
			resultJSON = json.RawMessage(results[i].Result.String)
		}
		tasks[i] = &models.Task{
			ID:             results[i].ID,
			UserID:         results[i].UserID,
			Type:           models.TaskType(results[i].Type),
			Payload:        results[i].Payload,
			Status:         models.TaskStatus(results[i].Status),
			Priority:       results[i].Priority,
			Result:         resultJSON,
			Error:          results[i].Error.String,
			Retries:        results[i].Retries,
			MaxRetries:     results[i].MaxRetries,
			CreatedAt:      results[i].CreatedAt,
			StartedAt:      results[i].StartedAt,
			CompletedAt:    results[i].CompletedAt,
			WorkerID:       results[i].WorkerID.String,
			ProcessingTime: results[i].ProcessingTime.Int64,
		}
	}
	return tasks, total, nil
}

// DeleteTask hard-deletes a task row and evicts it from the Redis cache.
// Returns an error if the task does not exist.
//
// Write-through cache for DELETE:
//   After a successful PostgreSQL DELETE, the corresponding Redis key is removed.
//   If the cache eviction is skipped, stale "deleted" task data could be served
//   to callers for up to the TTL duration after the row is gone from PG.
//
// Parameters:
//   ctx    — request-scoped context.
//   taskID — UUID of the task to delete.
//
// Returns:
//   error — "task not found: <id>" if no row was deleted (0 rows affected).
//
// Called by: internal/api/handlers.DeleteTaskHandler.
func (s *PostgresStorage) DeleteTask(ctx context.Context, taskID string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM tasks WHERE id = $1", taskID)
	if err != nil {
		return fmt.Errorf("failed to delete task: %w", err)
	}
	// RowsAffected is used to distinguish "task not found" (0 rows) from a
	// successful delete, without needing a prior SELECT.
	n, err := res.RowsAffected()
	if err != nil {
		// This error is unlikely (the pq driver always supports RowsAffected),
		// but it indicates the DB could not confirm whether the DELETE succeeded.
		return fmt.Errorf("failed to verify deletion: %w", err)
	}
	if n == 0 {
		// No rows were deleted — the task ID did not exist. Return a clear error
		// so the HTTP handler can respond with 404.
		return fmt.Errorf("task not found: %s", taskID)
	}

	// Evict the cache entry. A 2-second timeout prevents a slow Redis from
	// blocking the response — even if the DEL fails, the key will expire on its
	// own TTL. Accepting this small window is preferable to hanging the request.
	cacheCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.cache.Del(cacheCtx, "task:"+taskID)
	return nil
}

// GetMetrics aggregates task counts by status, the number of active workers
// (heartbeat within last 30 s), tasks completed in the past hour, and average
// processing time — all in a single SQL query using COUNT(*) FILTER expressions.
//
// Using a single query instead of multiple separate queries avoids multiple
// round trips and ensures the counts are taken from a consistent snapshot of
// the table at the same point in time (within a single implicit transaction).
//
// Parameters:
//   ctx — request-scoped context.
//
// Returns:
//   *models.SystemMetrics — snapshot of current system state.
//   error                 — non-nil if the DB query fails.
//
// Called by: internal/api/handlers.GetMetricsHandler, Prometheus scrape path.
func (s *PostgresStorage) GetMetrics(ctx context.Context) (*models.SystemMetrics, error) {
	metricsQuery := `
		SELECT
			COUNT(*) FILTER (WHERE status = 'queued') as queued,
			COUNT(*) FILTER (WHERE status = 'processing') as processing,
			COUNT(*) FILTER (WHERE status = 'completed') as completed,
			COUNT(*) FILTER (WHERE status = 'failed') as failed,
			(SELECT COUNT(*) FROM workers
			 WHERE status = 'active' AND last_heartbeat > NOW() - INTERVAL '30 seconds') as active_workers,
			(SELECT COUNT(*) FROM tasks
			 WHERE completed_at > NOW() - INTERVAL '1 hour') as completed_last_hour,
			(SELECT COALESCE(AVG(processing_time), 0) FROM tasks
			 WHERE status = 'completed' AND processing_time IS NOT NULL) as avg_processing_time
		FROM tasks
	`
	type allMetrics struct {
		Queued            int64   `db:"queued"`
		Processing        int64   `db:"processing"`
		Completed         int64   `db:"completed"`
		Failed            int64   `db:"failed"`
		ActiveWorkers     int     `db:"active_workers"`
		CompletedLastHour int64   `db:"completed_last_hour"`
		AvgProcessingTime float64 `db:"avg_processing_time"`
	}
	var m allMetrics
	if err := s.db.GetContext(ctx, &m, metricsQuery); err != nil {
		return nil, fmt.Errorf("failed to query metrics: %w", err)
	}
	return &models.SystemMetrics{
		QueuedTasks:       m.Queued,
		ProcessingTasks:   m.Processing,
		CompletedTasks:    m.Completed,
		FailedTasks:       m.Failed,
		ActiveWorkers:     m.ActiveWorkers,
		// Throughput is expressed as tasks-per-minute: divide completed-in-last-hour by 60.
		Throughput:        float64(m.CompletedLastHour) / 60.0,
		AvgProcessingTime: m.AvgProcessingTime,
		Timestamp:         time.Now(),
	}, nil
}

// RecordWorkerHeartbeat upserts a row in the workers table using the worker's ID
// as the conflict key. This means the first heartbeat inserts a new row and all
// subsequent ones update it, keeping the table lean (one row per worker).
//
// The ON CONFLICT DO UPDATE (UPSERT) pattern avoids a SELECT-then-INSERT race
// condition that could occur if two goroutines in the same worker tried to
// register at the same time. PostgreSQL handles the concurrency atomically.
//
// Note: start_time is intentionally excluded from the ON CONFLICT UPDATE clause
// because it represents when the worker process started, which should not change
// on subsequent heartbeats.
//
// Parameters:
//   ctx     — request-scoped context.
//   metrics — current worker state snapshot (status, counts, goroutines, etc.).
//
// Returns error if the upsert fails.
//
// Called by: internal/worker on a regular heartbeat ticker.
func (s *PostgresStorage) RecordWorkerHeartbeat(ctx context.Context, metrics *models.WorkerMetrics) error {
	query := `
		INSERT INTO workers (worker_id, status, tasks_processed, tasks_failed, last_heartbeat, start_time, current_task, active_goroutines)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (worker_id) DO UPDATE SET
			status = $2,
			tasks_processed = $3,
			tasks_failed = $4,
			last_heartbeat = $5,
			current_task = $7,
			active_goroutines = $8
	`
	_, err := s.db.ExecContext(ctx, query,
		metrics.WorkerID, metrics.Status, metrics.TasksProcessed, metrics.TasksFailed,
		metrics.LastHeartbeat, metrics.StartTime, metrics.CurrentTask, metrics.ActiveGoroutines,
	)
	return err
}

// cacheTask serialises the task to JSON and stores it in Redis under the key
// "task:<id>". It applies a TTL based on whether the task is in a terminal state.
//
// TTL differentiation — why active tasks get 30 s and completed tasks get 10 min:
//
//   Active tasks (queued, processing) change frequently. A worker picks up a
//   "queued" task and transitions it to "processing" within seconds. If the cache
//   held a "queued" snapshot for 10 minutes, callers would see stale status long
//   after the task moved on. A 30-second TTL limits the staleness window and acts
//   as a safety net: even if a worker crashes without calling UpdateTask (so the
//   DEL never happens), the cache entry expires in at most 30 seconds.
//
//   Completed and failed tasks are immutable — their status, result, and error
//   fields will never change again. It is safe to cache them for 10 minutes
//   because the data cannot go stale. A longer TTL reduces DB read pressure for
//   recently-finished tasks that clients continue polling.
//
// This function is a best-effort operation: if marshalling or the Redis SET fails,
// it logs nothing and returns silently. A cache write failure is non-fatal because
// the database remains the authoritative source — the next GetTask will simply
// incur a cache miss and query PostgreSQL.
//
// Called by: CreateTask, GetTask, UpdateTask (internal helper — not part of the interface).
func (s *PostgresStorage) cacheTask(task *models.Task) {
	taskJSON, err := json.Marshal(task)
	if err != nil {
		// Marshalling should never fail for a well-formed Task struct.
		// If it does, silently skip caching — the DB is still consistent.
		return
	}

	// Default TTL for non-terminal states (queued, processing, retrying).
	// Short TTL ensures stale "processing" entries don't linger if a worker
	// crashes without sending a final UpdateTask.
	ttl := 30 * time.Second

	// Terminal states (completed, failed) are immutable — use a much longer TTL
	// to keep frequently-accessed results in the cache and off the database.
	if task.Status == models.StatusCompleted || task.Status == models.StatusFailed {
		ttl = 10 * time.Minute
	}

	// Use a dedicated short context for the Redis write so a slow Redis does not
	// block the caller. cacheTask is always called after the DB operation
	// succeeds, so blocking here would add latency to CreateTask/UpdateTask.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.cache.Set(ctx, "task:"+task.ID, taskJSON, ttl)
}

// CreateUser inserts a new user record. password_hash and oauth fields accept
// NULL so that OAuth-only users don't need a password and password users don't
// need an OAuth identity. Roles are stored as a PostgreSQL text array.
func (s *PostgresStorage) CreateUser(ctx context.Context, user *models.User) error {
	query := `
		INSERT INTO users (id, username, email, password_hash, api_key, roles, oauth_provider, oauth_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
	`
	var passwordHash interface{}
	if user.Password != "" {
		passwordHash = user.Password
	}
	var oauthProvider, oauthID interface{}
	if user.OAuthProvider != "" {
		oauthProvider = user.OAuthProvider
		oauthID = user.OAuthID
	}
	_, err := s.db.ExecContext(ctx, query,
		user.ID, user.Username, user.Email, passwordHash,
		user.APIKey, pq.Array(user.Roles), oauthProvider, oauthID, user.CreatedAt,
	)
	if err != nil {
		log.Printf("CreateUser error: %v, username: %s, email: %s", err, user.Username, user.Email)
		return fmt.Errorf("failed to create user: %w", err)
	}
	return nil
}

// GetUserByUsername looks up a user by their unique username.
func (s *PostgresStorage) GetUserByUsername(ctx context.Context, username string) (*models.User, error) {
	return s.getUser(ctx, "username", username)
}

// GetUserByEmail looks up a user by email — used during OAuth account linking
// to find an existing password-auth account with the same address.
func (s *PostgresStorage) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	return s.getUser(ctx, "email", email)
}

// GetUserByID retrieves a user by their UUID primary key.
func (s *PostgresStorage) GetUserByID(ctx context.Context, userID string) (*models.User, error) {
	return s.getUser(ctx, "id", userID)
}

// getUser is the shared implementation for all GetUserBy* methods. It constructs
// a parameterised query selecting on the named column (field) and maps the result
// to a models.User, handling nullable SQL columns appropriately.
func (s *PostgresStorage) getUser(ctx context.Context, field, value string) (*models.User, error) {
	type userRow struct {
		ID            string         `db:"id"`
		Username      string         `db:"username"`
		Email         string         `db:"email"`
		PasswordHash  sql.NullString `db:"password_hash"`
		APIKey        sql.NullString `db:"api_key"`
		Roles         pq.StringArray `db:"roles"`
		OAuthProvider sql.NullString `db:"oauth_provider"`
		OAuthID       sql.NullString `db:"oauth_id"`
		CreatedAt     time.Time      `db:"created_at"`
	}
	var row userRow
	query := fmt.Sprintf(
		"SELECT id, username, email, password_hash, api_key, roles, oauth_provider, oauth_id, created_at FROM users WHERE %s = $1",
		field,
	)
	if err := s.db.GetContext(ctx, &row, query, value); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	return &models.User{
		ID:            row.ID,
		Username:      row.Username,
		Email:         row.Email,
		Password:      row.PasswordHash.String,
		APIKey:        row.APIKey.String,
		Roles:         []string(row.Roles),
		OAuthProvider: row.OAuthProvider.String,
		OAuthID:       row.OAuthID.String,
		CreatedAt:     row.CreatedAt,
	}, nil
}

// GetUserByOAuth looks up a user by their OAuth (provider, providerID) pair.
// Used in the OAuth callback to find an existing linked account before creating a new one.
func (s *PostgresStorage) GetUserByOAuth(ctx context.Context, provider, providerID string) (*models.User, error) {
	type userRow struct {
		ID            string         `db:"id"`
		Username      string         `db:"username"`
		Email         string         `db:"email"`
		PasswordHash  sql.NullString `db:"password_hash"`
		APIKey        sql.NullString `db:"api_key"`
		Roles         pq.StringArray `db:"roles"`
		OAuthProvider sql.NullString `db:"oauth_provider"`
		OAuthID       sql.NullString `db:"oauth_id"`
		CreatedAt     time.Time      `db:"created_at"`
	}
	var row userRow
	query := `SELECT id, username, email, password_hash, api_key, roles, oauth_provider, oauth_id, created_at
	          FROM users WHERE oauth_provider = $1 AND oauth_id = $2`
	if err := s.db.GetContext(ctx, &row, query, provider, providerID); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("failed to get oauth user: %w", err)
	}
	return &models.User{
		ID:            row.ID,
		Username:      row.Username,
		Email:         row.Email,
		Password:      row.PasswordHash.String,
		APIKey:        row.APIKey.String,
		Roles:         []string(row.Roles),
		OAuthProvider: row.OAuthProvider.String,
		OAuthID:       row.OAuthID.String,
		CreatedAt:     row.CreatedAt,
	}, nil
}

// UpdateUser persists changes to username, email, password_hash, api_key, and
// roles. updated_at is set to the current time automatically.
func (s *PostgresStorage) UpdateUser(ctx context.Context, user *models.User) error {
	query := `
		UPDATE users
		SET username = $2, email = $3, password_hash = $4, api_key = $5, roles = $6, updated_at = $7
		WHERE id = $1
	`
	_, err := s.db.ExecContext(ctx, query,
		user.ID, user.Username, user.Email, user.Password,
		user.APIKey, pq.Array(user.Roles), time.Now(),
	)
	if err != nil {
		return fmt.Errorf("failed to update user: %w", err)
	}
	return nil
}

// DeleteUser hard-deletes a user record. Returns an error if the user does not exist.
func (s *PostgresStorage) DeleteUser(ctx context.Context, userID string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM users WHERE id = $1", userID)
	if err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to verify deletion: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("user not found: %s", userID)
	}
	return nil
}

// ListUsers returns a paginated list of all users ordered by creation date (newest
// first). The total row count is returned for pagination UI.
func (s *PostgresStorage) ListUsers(ctx context.Context, limit, offset int) ([]*models.User, int64, error) {
	type userRow struct {
		ID        string         `db:"id"`
		Username  string         `db:"username"`
		Email     string         `db:"email"`
		APIKey    sql.NullString `db:"api_key"`
		Roles     pq.StringArray `db:"roles"`
		CreatedAt time.Time      `db:"created_at"`
		Total     int64          `db:"total_count"`
	}
	query := `
		SELECT id, username, email, api_key, roles, created_at, COUNT(*) OVER() as total_count
		FROM users ORDER BY created_at DESC LIMIT $1 OFFSET $2
	`
	var rows []userRow
	if err := s.db.SelectContext(ctx, &rows, query, limit, offset); err != nil {
		return nil, 0, fmt.Errorf("failed to list users: %w", err)
	}
	if len(rows) == 0 {
		return nil, 0, nil
	}
	users := make([]*models.User, len(rows))
	total := rows[0].Total
	for i, r := range rows {
		users[i] = &models.User{
			ID:        r.ID,
			Username:  r.Username,
			Email:     r.Email,
			APIKey:    r.APIKey.String,
			Roles:     []string(r.Roles),
			CreatedAt: r.CreatedAt,
		}
	}
	return users, total, nil
}

// CreateBatch inserts a batch metadata row. Task counts (queued/completed/failed)
// are not stored here — they are derived live in GetBatch via aggregate SQL.
func (s *PostgresStorage) CreateBatch(ctx context.Context, batch *models.Batch) error {
	query := `
		INSERT INTO batches (id, user_id, type, total, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`
	_, err := s.db.ExecContext(ctx, query,
		batch.ID, batch.UserID, batch.Type, batch.Total, batch.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create batch: %w", err)
	}
	return nil
}

// GetBatch retrieves a batch by ID and computes live task-status counts using a
// single aggregate query over the tasks table, so counts are always up-to-date
// without any additional synchronisation.
func (s *PostgresStorage) GetBatch(ctx context.Context, batchID string) (*models.Batch, error) {
	type batchRow struct {
		ID          string         `db:"id"`
		UserID      string         `db:"user_id"`
		Type        string         `db:"type"`
		Total       int            `db:"total"`
		CreatedAt   time.Time      `db:"created_at"`
		CompletedAt *time.Time     `db:"completed_at"`
		Queued      int            `db:"queued"`
		Processing  int            `db:"processing"`
		Completed   int            `db:"completed"`
		Failed      int            `db:"failed"`
	}
	query := `
		SELECT
			b.id, b.user_id, b.type, b.total, b.created_at, b.completed_at,
			COUNT(*) FILTER (WHERE t.status = 'queued')     AS queued,
			COUNT(*) FILTER (WHERE t.status = 'processing') AS processing,
			COUNT(*) FILTER (WHERE t.status = 'completed')  AS completed,
			COUNT(*) FILTER (WHERE t.status IN ('failed','cancelled')) AS failed
		FROM batches b
		LEFT JOIN tasks t ON t.batch_id = b.id
		WHERE b.id = $1
		GROUP BY b.id, b.user_id, b.type, b.total, b.created_at, b.completed_at
	`
	var row batchRow
	if err := s.db.GetContext(ctx, &row, query, batchID); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("batch not found: %s", batchID)
		}
		return nil, fmt.Errorf("failed to get batch: %w", err)
	}

	done := row.Completed + row.Failed
	status := models.BatchStatusQueued
	switch {
	case row.Processing > 0 || (done > 0 && done < row.Total):
		status = models.BatchStatusProcessing
	case done == row.Total && row.Failed == 0:
		status = models.BatchStatusCompleted
	case done == row.Total && row.Completed > 0:
		status = models.BatchStatusPartial
	case done == row.Total && row.Completed == 0:
		status = models.BatchStatusFailed
	}

	return &models.Batch{
		ID:          row.ID,
		UserID:      row.UserID,
		Type:        models.TaskType(row.Type),
		Status:      status,
		Total:       row.Total,
		Queued:      row.Queued,
		Processing:  row.Processing,
		Completed:   row.Completed,
		Failed:      row.Failed,
		CreatedAt:   row.CreatedAt,
		CompletedAt: row.CompletedAt,
	}, nil
}

// GetDB exposes the underlying *sql.DB for callers that need direct access
// (e.g. the Prometheus SQL metrics collector).
func (s *PostgresStorage) GetDB() *sql.DB {
	return s.db.DB
}

// Close shuts down the Redis cache connection and the PostgreSQL connection pool.
// Called once during graceful shutdown.
func (s *PostgresStorage) Close() error {
	s.cache.Close()
	return s.db.Close()
}
