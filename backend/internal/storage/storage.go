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

type Storage interface {
	CreateTask(ctx context.Context, task *models.Task) error
	GetTask(ctx context.Context, taskID string) (*models.Task, error)
	UpdateTask(ctx context.Context, task *models.Task) error
	ListTasks(ctx context.Context, status models.TaskStatus, userID string, limit, offset int) ([]*models.Task, int64, error)
	DeleteTask(ctx context.Context, taskID string) error
	GetMetrics(ctx context.Context) (*models.SystemMetrics, error)
	RecordWorkerHeartbeat(ctx context.Context, metrics *models.WorkerMetrics) error
	CreateUser(ctx context.Context, user *models.User) error
	GetUserByUsername(ctx context.Context, username string) (*models.User, error)
	GetUserByEmail(ctx context.Context, email string) (*models.User, error)
	GetUserByID(ctx context.Context, userID string) (*models.User, error)
	GetUserByOAuth(ctx context.Context, provider, providerID string) (*models.User, error)
	UpdateUser(ctx context.Context, user *models.User) error
	// Batch operations — group multiple tasks under one tracking ID.
	CreateBatch(ctx context.Context, batch *models.Batch) error
	GetBatch(ctx context.Context, batchID string) (*models.Batch, error)
	DeleteUser(ctx context.Context, userID string) error
	ListUsers(ctx context.Context, limit, offset int) ([]*models.User, int64, error)
	GetDB() *sql.DB
	Close() error
}

type PostgresStorage struct {
	db    *sqlx.DB
	cache *redis.Client
}

// NewPostgresStorage opens a PostgreSQL connection pool, connects a Redis client
// (DB 1, separate from the broker's DB 0) for task caching, and runs initSchema
// to create any missing tables. Connection pool sizing can be tuned via
// DB_MAX_OPEN_CONNS and DB_MAX_IDLE_CONNS environment variables.
func NewPostgresStorage(dsn, redisAddr string) (*PostgresStorage, error) {
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}

	maxOpenConns := 25
	maxIdleConns := 5
	connMaxLifetime := 5 * time.Minute
	connMaxIdleTime := 10 * time.Minute

	if envMaxOpen := os.Getenv("DB_MAX_OPEN_CONNS"); envMaxOpen != "" {
		fmt.Sscanf(envMaxOpen, "%d", &maxOpenConns)
	}
	if envMaxIdle := os.Getenv("DB_MAX_IDLE_CONNS"); envMaxIdle != "" {
		fmt.Sscanf(envMaxIdle, "%d", &maxIdleConns)
	}

	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxLifetime(connMaxLifetime)
	db.SetConnMaxIdleTime(connMaxIdleTime)

	cache := redis.NewClient(&redis.Options{
		Addr:               redisAddr,
		DB:                 1,
		PoolSize:           10,
		MinIdleConns:       5,
		DialTimeout:        5 * time.Second,
		ReadTimeout:        3 * time.Second,
		WriteTimeout:       3 * time.Second,
		PoolTimeout:        4 * time.Second,
		IdleTimeout:        5 * time.Minute,
		IdleCheckFrequency: 1 * time.Minute,
		MaxRetries:         3,
	})

	storage := &PostgresStorage{db: db, cache: cache}

	if err := storage.initSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return storage, nil
}

// initSchema runs CREATE TABLE IF NOT EXISTS and CREATE INDEX IF NOT EXISTS
// statements at startup. It is idempotent — safe to call on every boot even
// when the schema already exists.
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
func (s *PostgresStorage) CreateTask(ctx context.Context, task *models.Task) error {
	query := `
		INSERT INTO tasks (id, user_id, batch_id, type, payload, status, priority, retries, max_retries, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	var batchID interface{}
	if task.BatchID != "" {
		batchID = task.BatchID
	}
	_, err := s.db.ExecContext(ctx, query,
		task.ID, task.UserID, batchID, task.Type, task.Payload, task.Status,
		task.Priority, task.Retries, task.MaxRetries, task.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create task: %w", err)
	}
	s.cacheTask(task)
	return nil
}

// GetTask fetches a task by ID, checking the Redis cache first (TTL 5 min) to
// reduce database load. On a cache miss the task is fetched from PostgreSQL and
// written back into the cache before returning.
func (s *PostgresStorage) GetTask(ctx context.Context, taskID string) (*models.Task, error) {
	cacheKey := "task:" + taskID
	cacheCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if cached, err := s.cache.Get(cacheCtx, cacheKey).Result(); err == nil {
		var task models.Task
		if err := json.Unmarshal([]byte(cached), &task); err == nil {
			return &task, nil
		}
	}

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
	}

	var row taskRow
	query := `
		SELECT id, user_id, type, payload, status, priority, result, error, retries, max_retries,
		       created_at, started_at, completed_at, worker_id, processing_time
		FROM tasks WHERE id = $1
	`
	if err := s.db.GetContext(ctx, &row, query, taskID); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("task not found: %s", taskID)
		}
		return nil, fmt.Errorf("failed to get task: %w", err)
	}

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
		Error:          row.Error.String,
		Retries:        row.Retries,
		MaxRetries:     row.MaxRetries,
		CreatedAt:      row.CreatedAt,
		StartedAt:      row.StartedAt,
		CompletedAt:    row.CompletedAt,
		WorkerID:       row.WorkerID.String,
		ProcessingTime: row.ProcessingTime.Int64,
	}

	s.cacheTask(task)
	return task, nil
}

// UpdateTask persists the task's mutable fields (status, result, error, retries,
// timestamps, workerID, processingTime) to PostgreSQL and invalidates the Redis
// cache entry so the next GetTask reads fresh data.
func (s *PostgresStorage) UpdateTask(ctx context.Context, task *models.Task) error {
	query := `
		UPDATE tasks
		SET status = $2, result = $3, error = $4, retries = $5,
		    started_at = $6, completed_at = $7, worker_id = $8, processing_time = $9
		WHERE id = $1
	`
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

	cacheCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.cache.Del(cacheCtx, "task:"+task.ID)
	s.cacheTask(task)
	return nil
}

// ListTasks returns a paginated, optionally filtered page of tasks. Filtering by
// status and/or userID is supported; both may be empty to list all tasks.
// Total row count is returned alongside the page so callers can compute pagination.
func (s *PostgresStorage) ListTasks(ctx context.Context, status models.TaskStatus, userID string, limit, offset int) ([]*models.Task, int64, error) {
	var query string
	var args []interface{}

	switch {
	case status != "" && userID != "":
		query = `
			SELECT id, user_id, type, payload, status, priority, result, error, retries, max_retries,
			       created_at, started_at, completed_at, worker_id, processing_time,
			       COUNT(*) OVER() as total_count
			FROM tasks WHERE status = $1 AND user_id = $2
			ORDER BY created_at DESC LIMIT $3 OFFSET $4
		`
		args = []interface{}{status, userID, limit, offset}
	case status != "":
		query = `
			SELECT id, user_id, type, payload, status, priority, result, error, retries, max_retries,
			       created_at, started_at, completed_at, worker_id, processing_time,
			       COUNT(*) OVER() as total_count
			FROM tasks WHERE status = $1
			ORDER BY created_at DESC LIMIT $2 OFFSET $3
		`
		args = []interface{}{status, limit, offset}
	case userID != "":
		query = `
			SELECT id, user_id, type, payload, status, priority, result, error, retries, max_retries,
			       created_at, started_at, completed_at, worker_id, processing_time,
			       COUNT(*) OVER() as total_count
			FROM tasks WHERE user_id = $1
			ORDER BY created_at DESC LIMIT $2 OFFSET $3
		`
		args = []interface{}{userID, limit, offset}
	default:
		query = `
			SELECT id, user_id, type, payload, status, priority, result, error, retries, max_retries,
			       created_at, started_at, completed_at, worker_id, processing_time,
			       COUNT(*) OVER() as total_count
			FROM tasks ORDER BY created_at DESC LIMIT $1 OFFSET $2
		`
		args = []interface{}{limit, offset}
	}

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
		TotalCount     int64           `db:"total_count"`
	}

	var results []taskRow
	if err := s.db.SelectContext(ctx, &results, query, args...); err != nil {
		return nil, 0, fmt.Errorf("failed to list tasks: %w", err)
	}
	if len(results) == 0 {
		return nil, 0, nil
	}

	tasks := make([]*models.Task, len(results))
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
func (s *PostgresStorage) DeleteTask(ctx context.Context, taskID string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM tasks WHERE id = $1", taskID)
	if err != nil {
		return fmt.Errorf("failed to delete task: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to verify deletion: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("task not found: %s", taskID)
	}

	cacheCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.cache.Del(cacheCtx, "task:"+taskID)
	return nil
}

// GetMetrics aggregates task counts by status, the number of active workers
// (heartbeat within last 30 s), tasks completed in the past hour, and average
// processing time — all in a single SQL query using COUNT(*) FILTER expressions.
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
		Throughput:        float64(m.CompletedLastHour) / 60.0,
		AvgProcessingTime: m.AvgProcessingTime,
		Timestamp:         time.Now(),
	}, nil
}

// RecordWorkerHeartbeat upserts a row in the workers table using the worker's ID
// as the conflict key. This means the first heartbeat inserts a new row and all
// subsequent ones update it, keeping the table lean (one row per worker).
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

// cacheTask serialises the task to JSON and stores it in Redis. Terminal tasks
// (completed/failed) get a 10-minute TTL; non-terminal tasks get 30 seconds so
// stale "processing" entries don't linger if a worker crashes without updating.
func (s *PostgresStorage) cacheTask(task *models.Task) {
	taskJSON, err := json.Marshal(task)
	if err != nil {
		return
	}
	ttl := 30 * time.Second
	if task.Status == models.StatusCompleted || task.Status == models.StatusFailed {
		ttl = 10 * time.Minute
	}
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
