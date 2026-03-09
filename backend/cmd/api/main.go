package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-redis/redis/v8"
	"github.com/gofiber/adaptor/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/websocket/v2"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"distributed-task-queue/internal/broker"
	"distributed-task-queue/internal/graphql"
	"distributed-task-queue/internal/logging"
	"distributed-task-queue/internal/models"
	"distributed-task-queue/internal/monitoring"
	"distributed-task-queue/internal/oauth"
	"distributed-task-queue/internal/reliability"
	"distributed-task-queue/internal/security"
	"distributed-task-queue/internal/storage"
	"distributed-task-queue/internal/tracing"
)

// APIServer holds all long-lived dependencies wired together at startup.
// Using a struct avoids global variables and makes testing and shutdown easier.
type APIServer struct {
	app           *fiber.App               // the Fiber HTTP application instance that handles all routes
	broker        broker.Broker            // message broker that publishes tasks to Redis Streams for workers to consume
	storage       storage.Storage          // data layer: persists tasks and users in PostgreSQL, caches reads in Redis
	authService   *security.AuthService    // handles JWT generation, validation, and token blacklisting on logout
	rateLimiter   *security.RateLimiter    // sliding-window rate limiter backed by Redis ZSETs (100 req/min default)
	wsHub         *WebSocketHub            // fan-out hub that pushes real-time task updates to all connected browser clients
	redisClient   *redis.Client            // direct Redis connection reused for health checks (avoids calling broker.GetClient in interface)
	healthChecker *reliability.HealthChecker // pre-built health checker reused on every /health request instead of rebuilding each time
	oauthCfg      *oauth.Config            // Google and GitHub OAuth2 provider configs built from environment variables
	oauthStates   oauth.StateStore         // CSRF state store for the OAuth round-trip (Redis-backed in production)
}

// WebSocketHub manages all active WebSocket connections and routes messages to them.
// It uses a dedicated goroutine (Run) so connection map mutations are serialized
// through channels, avoiding data races without needing a lock for every send.
type WebSocketHub struct {
	clients    map[*websocket.Conn]bool // set of all currently connected WebSocket clients
	broadcast  chan []byte              // incoming messages to fan-out to every client; buffered to 256 so callers don't block
	register   chan *websocket.Conn     // new connection arrives → add to clients map
	unregister chan *websocket.Conn     // connection closes → remove from clients map and close the socket
	mu         sync.RWMutex            // guards the clients map; RLock for reads during broadcast, Lock for writes
	ctx        context.Context         // cancelled by Shutdown() to stop the Run loop cleanly
	cancel     context.CancelFunc      // called by Shutdown() to signal Run() to exit
}

// NewWebSocketHub creates a hub with a cancellable context so it can be stopped
// cleanly during server shutdown without leaving goroutines running.
func NewWebSocketHub() *WebSocketHub {
	ctx, cancel := context.WithCancel(context.Background()) // create a context we can cancel to stop the Run loop
	return &WebSocketHub{
		clients:    make(map[*websocket.Conn]bool), // empty set — no clients yet
		broadcast:  make(chan []byte, 256),          // buffer 256 messages so writers aren't blocked by slow sends
		register:   make(chan *websocket.Conn),      // unbuffered: registration is instant so no buffer needed
		unregister: make(chan *websocket.Conn),      // unbuffered: unregistration is instant so no buffer needed
		ctx:        ctx,                             // store context so Run() can check for shutdown
		cancel:     cancel,                          // store cancel so Shutdown() can trigger cleanup
	}
}

// Run is the hub's event loop — it must run in its own goroutine.
// All mutations to the clients map happen here, making them safe without a global lock
// (we only need RLock when snapshotting the map for a broadcast).
func (h *WebSocketHub) Run() {
	for {
		select {
		case <-h.ctx.Done(): // Shutdown() was called — close all open connections and stop the loop
			h.mu.Lock()
			for client := range h.clients { // iterate every connected client
				client.Close() // forcibly close the WebSocket so the browser sees a disconnect
			}
			h.clients = make(map[*websocket.Conn]bool) // reset the map so GC can collect old connections
			h.mu.Unlock()
			return // exit the goroutine; server shutdown will proceed after this

		case client := <-h.register: // a new WebSocket connection was accepted
			h.mu.Lock()
			h.clients[client] = true // add the connection to the active-clients set
			h.mu.Unlock()

		case client := <-h.unregister: // a connection was closed (by client disconnect or write error)
			h.mu.Lock()
			if _, ok := h.clients[client]; ok { // only act if we still track this connection
				delete(h.clients, client) // remove from set so future broadcasts skip it
				client.Close()            // ensure the underlying TCP connection is closed
			}
			h.mu.Unlock()

		case message := <-h.broadcast: // a task update arrived; send it to every connected client
			h.mu.RLock()
			clients := make([]*websocket.Conn, 0, len(h.clients)) // snapshot the current clients slice
			for client := range h.clients {
				clients = append(clients, client) // copy pointers so we can release the lock before writing
			}
			h.mu.RUnlock() // release lock before writing; writes can be slow and we don't want to block register/unregister
			for _, client := range clients {
				if err := client.WriteMessage(websocket.TextMessage, message); err != nil {
					// write failed → the client likely disconnected; remove it from the hub
					h.mu.Lock()
					delete(h.clients, client) // stop tracking the dead connection
					h.mu.Unlock()
					client.Close() // release OS resources for the broken socket
				}
			}
		}
	}
}

// Shutdown signals the Run loop to stop by cancelling its context.
// Called during server shutdown to cleanly close all WebSocket connections.
func (h *WebSocketHub) Shutdown() {
	h.cancel() // triggers <-h.ctx.Done() in Run(), which closes all clients and exits
}

// NewAPIServer wires all dependencies together and returns a ready-to-listen server.
// It is the application's composition root — the only place where concrete types are created.
func NewAPIServer() (*APIServer, error) {
	// Connect to Redis Streams to publish tasks.
	// "task-queue" is the stream key; "workers" is the consumer group name.
	redisBroker, err := broker.NewRedisStreamBroker(
		getEnv("REDIS_ADDR", "localhost:6379"), // Redis address from env, falls back to localhost for local dev
		"task-queue",                           // Redis Stream key — workers consume from this stream
		"workers",                              // Consumer group name — allows multiple worker instances to share the stream
	)
	if err != nil {
		return nil, err // can't start without a message broker; fail fast
	}

	// Connect to PostgreSQL for task/user persistence, with Redis for caching.
	pgStorage, err := storage.NewPostgresStorage(
		getEnv("POSTGRES_DSN", "postgres://taskqueue_user:password@localhost/taskqueue?sslmode=disable"), // DB connection string
		getEnv("REDIS_ADDR", "localhost:6379"), // Redis address used by the storage layer for task-result caching (DB 1)
	)
	if err != nil {
		return nil, err // can't start without persistent storage; fail fast
	}

	// Reuse the broker's underlying Redis client for auth and rate limiting.
	// We keep this as a separate field so the Broker interface doesn't need GetClient().
	redisClient := redisBroker.GetClient()

	// Auth service signs JWT tokens and blacklists them on logout using Redis SETEX.
	// Tokens expire after 24 hours; the JWT secret must be overridden in production via env.
	authService := security.NewAuthService(
		getEnv("JWT_SECRET", "change-me-in-production"), // signing key — MUST be changed in production
		24*time.Hour,  // token TTL: users stay logged in for 24 hours
		redisClient,   // Redis is used to store blacklisted tokens after logout
	)

	// Rate limiter uses a Redis sorted set (ZSET) as a sliding window counter.
	// Each user IP is limited to 100 requests per minute across all API endpoints.
	rateLimiter := security.NewRateLimiter(redisClient, 100, 1*time.Minute)

	// Create the WebSocket hub and start its event loop in a background goroutine.
	// The goroutine runs for the lifetime of the server.
	wsHub := NewWebSocketHub()
	go wsHub.Run() // start the fan-out event loop in the background

	// Create the Fiber HTTP application with our custom error handler.
	// The custom handler formats errors as JSON and increments the error Prometheus counter.
	app := fiber.New(fiber.Config{
		ErrorHandler: customErrorHandler, // replaces Fiber's default plain-text error responses with JSON
	})

	app.Use(recover.New())        // catches any panic in a handler, logs it, and returns HTTP 500 instead of crashing
	app.Use(tracingMiddleware())  // creates an OTel server span per request; stores enriched context in c.UserContext()
	app.Use(logger.New())         // logs every request: method, path, status code, and response time
	app.Use(cors.New(cors.Config{
		AllowOrigins: getEnv("CORS_ORIGINS", "*"),                         // which frontend origins can call this API
		AllowHeaders: "Origin, Content-Type, Accept, Authorization, X-API-Key", // allowed request headers (X-API-Key for API key auth)
		AllowMethods: "GET,POST,PUT,DELETE,OPTIONS",                        // allowed HTTP methods (OPTIONS required for preflight)
	}))
	app.Use(monitoring.PrometheusMiddleware()) // records request count and latency histograms for every route

	// Build the health checker once at startup and reuse it on every /health call.
	// Rebuilding it on every request would re-allocate check functions unnecessarily.
	healthChecker := reliability.NewHealthChecker()
	healthChecker.RegisterCheck("postgres", reliability.PostgresHealthCheck(pgStorage.GetDB())) // pings Postgres with SELECT 1
	healthChecker.RegisterCheck("redis", reliability.RedisHealthCheck(redisClient))             // pings Redis with PING

	// OAuth config reads GOOGLE_CLIENT_ID/SECRET and GITHUB_CLIENT_ID/SECRET from env.
	// APP_BASE_URL sets the redirect URLs (e.g. https://yourdomain.com).
	oauthCfg := oauth.NewConfig(getEnv("APP_BASE_URL", "http://localhost:3000"))
	oauthStates := oauth.NewMemoryStateStore() // swap for a Redis-backed store in multi-instance deployments

	// Assemble the server with all wired dependencies.
	server := &APIServer{
		app:           app,           // Fiber application that owns all route definitions
		broker:        redisBroker,   // message broker for publishing tasks to the stream
		storage:       pgStorage,     // data layer for persisting and querying tasks and users
		authService:   authService,   // JWT auth — token generation, validation, and blacklisting
		rateLimiter:   rateLimiter,   // enforces per-client request rate limits
		wsHub:         wsHub,         // WebSocket hub for real-time task status push
		redisClient:   redisClient,   // raw Redis client for health checks
		healthChecker: healthChecker, // pre-built health checker to reuse on /health requests
		oauthCfg:      oauthCfg,      // Google + GitHub OAuth2 configs
		oauthStates:   oauthStates,   // CSRF state store for OAuth round-trips
	}

	server.setupRoutes() // register all HTTP routes on the Fiber app
	return server, nil
}

// setupRoutes registers all HTTP routes.
// Routes are grouped by access level: public, authenticated, and admin-only.
func (s *APIServer) setupRoutes() {
	api := s.app.Group("/api/v1") // all versioned API endpoints live under this prefix

	// Health and metrics are public — no auth needed so monitoring tools can poll them.
	api.Get("/health", s.healthCheck)                                 // returns JSON status of Postgres and Redis
	s.app.Get("/metrics", adaptor.HTTPHandler(promhttp.Handler()))    // Prometheus scrape endpoint; adaptor bridges net/http → Fiber

	api.Use(security.RateLimitMiddleware(s.rateLimiter)) // apply rate limiting to all routes below this line

	// Auth endpoints are rate-limited but don't require a token (they produce one).
	api.Post("/auth/login", s.login)       // username+password → JWT token
	api.Post("/auth/register", s.register) // create account + return JWT token immediately

	// OAuth 2.0 — redirect to provider and handle the callback.
	api.Get("/auth/oauth/:provider", s.oauthRedirect)           // redirects browser to Google/GitHub consent screen
	api.Get("/auth/oauth/:provider/callback", s.oauthCallback)  // exchanges code for JWT after provider redirects back

	// All routes below require a valid JWT (checked by AuthMiddleware).
	protected := api.Group("/", security.AuthMiddleware(s.authService))

	protected.Post("/auth/logout", s.logout)          // blacklists the current token in Redis
	protected.Get("/users/me", s.getCurrentUser)       // returns the authenticated user's profile
	protected.Put("/users/me", s.updateCurrentUser)    // lets users change their own email or password
	protected.Post("/upload", s.uploadFile)            // uploads an image and returns a URL for use as task source_url
	protected.Post("/tasks", s.submitTask)             // creates a task record in Postgres and publishes it to Redis Streams
	protected.Post("/tasks/batch", s.submitBatch)      // submits N tasks at once; each image gets its own task fanned out to workers
	protected.Get("/tasks/:id", s.getTaskStatus)       // fetches a single task; enforces ownership (users can only see their own)
	protected.Get("/tasks", s.listTasks)               // paginated task list; admins can filter by user_id
	protected.Delete("/tasks/:id", s.deleteTask)       // removes a task; enforces ownership
	protected.Get("/batches/:id", s.getBatch)          // returns batch progress: total/queued/processing/completed/failed
	protected.Get("/ws", websocket.New(s.handleWebSocket)) // upgrades to WebSocket for real-time task update push

	metricsGroup := protected.Group("/metrics")
	metricsGroup.Get("/system", s.getMetrics) // returns aggregate system metrics (task counts, worker stats) from Postgres

	// Admin-only routes — accessible only to users whose token includes the "admin" role.
	// IMPORTANT: group path must be "/users" not "/" — using "/" as a group prefix in Fiber v2
	// causes RoleMiddleware to match every route under protected, not just these four.
	adminOnly := protected.Group("/users", security.RoleMiddleware("admin"))
	adminOnly.Get("/", s.listUsers)           // GET  /api/v1/users         — paginated list of all users
	adminOnly.Get("/:id", s.getUserByID)      // GET  /api/v1/users/:id     — fetch any user by ID
	adminOnly.Put("/:id", s.updateUser)       // PUT  /api/v1/users/:id     — admin can update any user's email or roles
	adminOnly.Delete("/:id", s.deleteUser)    // DELETE /api/v1/users/:id   — permanently removes a user (cannot delete self)

	// GraphQL is optional — the build tag "graphql" must be set to compile a real server.
	// If NewServer returns an error (e.g., stub build), we log a warning and skip the route.
	// Cancel endpoint — lets owners and admins stop a queued/processing task.
	protected.Post("/tasks/:id/cancel", s.cancelTask)

	// GraphQL is optional — the build tag "graphql" must be set to compile a real server.
	// If NewServer returns an error (e.g., stub build), we log a warning and skip the route.
	gqlServer, err := graphql.NewServer(s.broker, s.storage)
	if err != nil {
		slog.Warn("GraphQL server unavailable", slog.String("error", err.Error())) // non-fatal; REST API still works
	} else {
		s.app.Post("/graphql", adaptor.HTTPHandler(gqlServer)) // GraphQL query/mutation endpoint
	}
	s.app.Get("/graphql", adaptor.HTTPHandlerFunc(graphql.PlaygroundHandler())) // browser-based GraphQL playground (always available)

	// Serve processed images and raw uploads as static files.
	// Workers write output to ./storage/images; uploads land in ./storage/uploads.
	s.app.Static("/images", "./storage/images")   // processed output images (resized, compressed, etc.)
	s.app.Static("/uploads", "./storage/uploads") // original files uploaded by users before processing
}

// submitTask handles POST /api/v1/tasks.
// It validates the request, persists the task in Postgres, and publishes it to Redis Streams
// so a worker picks it up asynchronously.
func (s *APIServer) submitTask(c *fiber.Ctx) error {
	userID, ok := c.Locals("user_id").(string) // read user ID injected by AuthMiddleware into the request context
	if !ok || userID == "" {
		return fiber.NewError(fiber.StatusUnauthorized, "unauthorized") // middleware didn't set user_id — should never happen in protected routes
	}

	var req models.SubmitTaskRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body") // JSON was malformed or missing required fields
	}

	if !isValidTaskType(req.Type) {
		return fiber.NewError(fiber.StatusBadRequest, "invalid task type") // reject unknown task types early before hitting storage
	}

	var payload map[string]any
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid payload") // payload must be valid JSON; reject binary or malformed input
	}

	// Security validation: checks that source_url is HTTPS, from an allowed domain, and points to an image.
	// Prevents SSRF attacks where the worker would fetch attacker-controlled URLs.
	if err := security.ValidateTaskPayload(string(req.Type), payload); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error()) // return specific validation message to the client
	}

	// Build the task model with a new UUID, linking it to the authenticated user.
	task := &models.Task{
		ID:         uuid.New().String(), // globally unique ID for tracking this task
		UserID:     userID,              // links the task to the requesting user for ownership checks
		Type:       req.Type,            // image_resize, image_compress, etc. — determines which processor runs
		Payload:    req.Payload,         // raw JSON passed through to the worker; validated above
		Status:     models.StatusQueued, // initial status — changes to "processing", then "completed" or "failed"
		Priority:   req.Priority,        // higher-priority tasks are consumed first by workers (if supported by broker)
		Retries:    0,                   // no retries yet; worker increments this on each failure up to MaxRetries
		MaxRetries: req.MaxRetries,      // how many times the worker will retry before marking the task as failed
		CreatedAt:  time.Now(),          // timestamp used for sorting and display
	}

	ctx, cancel := context.WithTimeout(c.Context(), 10*time.Second) // don't wait more than 10s for DB + broker combined
	defer cancel()                                                    // always release the context, even on early returns

	// Persist the task first so it's queryable even if the broker publish fails.
	if err := s.storage.CreateTask(ctx, task); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to create task") // DB write failed; task was not created
	}

	// Publish to Redis Streams so a worker picks it up. Workers use XREADGROUP to consume.
	if err := s.broker.Publish(ctx, task); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to queue task") // stream publish failed; task exists in DB but won't be processed
	}

	monitoring.TasksSubmitted.WithLabelValues(string(task.Type)).Inc()
	s.broadcastTaskUpdate(task)

	slog.Info("task submitted",
		slog.String("task_id", task.ID),
		slog.String("task_type", string(task.Type)),
		slog.String("user_id", userID),
	)

	// Return 201 Created with the task ID so the client can poll for status.
	return c.Status(fiber.StatusCreated).JSON(models.SubmitTaskResponse{
		TaskID:    task.ID,         // client uses this to call GET /tasks/:id
		Status:    task.Status,     // always "queued" at this point
		Message:   "Task queued successfully",
		CreatedAt: task.CreatedAt,  // echoed back for the client to display
	})
}

// uploadFile handles POST /api/v1/upload.
// Users upload an image here first, then use the returned URL as source_url in a task payload.
// This avoids the API server fetching arbitrary external URLs for task input.
func (s *APIServer) uploadFile(c *fiber.Ctx) error {
	file, err := c.FormFile("file") // parse the "file" field from the multipart form body
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "no file provided") // form field missing or request wasn't multipart
	}

	// Whitelist of allowed image extensions — prevents uploading scripts or executables.
	allowedTypes := map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
	}
	ext := strings.ToLower(filepath.Ext(file.Filename)) // extract and lowercase the extension (e.g. ".PNG" → ".png")
	if !allowedTypes[ext] {
		return fiber.NewError(fiber.StatusBadRequest, "invalid file type: only images are allowed") // reject non-image uploads
	}

	if file.Size > 10*1024*1024 { // 10 MB hard limit to prevent large file uploads from exhausting disk
		return fiber.NewError(fiber.StatusBadRequest, "file size exceeds 10MB limit")
	}

	uploadDir := "./storage/uploads"
	if err := os.MkdirAll(uploadDir, 0755); err != nil { // create the uploads directory if it doesn't exist yet
		return fiber.NewError(fiber.StatusInternalServerError, "failed to create upload directory")
	}

	filename := uuid.New().String() + ext         // random UUID as filename prevents conflicts and path traversal attacks
	filePath := filepath.Join(uploadDir, filename) // safe path join — filepath.Join cleans ".." components

	if err := c.SaveFile(file, filePath); err != nil { // write the uploaded bytes to disk
		return fiber.NewError(fiber.StatusInternalServerError, "failed to save file")
	}

	// Build a public URL that points back through the API server's /uploads static route.
	fileURL := getEnv("BASE_URL", "http://localhost:8080") + "/uploads/" + filename
	slog.Info("file uploaded", slog.String("original", file.Filename), slog.String("url", fileURL))

	return c.JSON(fiber.Map{
		"url":      fileURL, // client stores this URL and passes it as source_url when submitting a task
		"filename": filename, // the server-assigned filename (UUID-based)
		"size":     file.Size, // file size in bytes, echoed back to the client
	})
}

// getTaskStatus handles GET /api/v1/tasks/:id.
// Returns a single task's full details including current status and result.
// Enforces ownership: regular users can only view their own tasks.
func (s *APIServer) getTaskStatus(c *fiber.Ctx) error {
	taskID := c.Params("id") // extract the task ID from the URL path parameter
	if taskID == "" {
		return fiber.NewError(fiber.StatusBadRequest, "task ID is required") // shouldn't happen with Fiber routing but guard anyway
	}

	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second) // 5s is enough for a single DB lookup
	defer cancel()

	task, err := s.storage.GetTask(ctx, taskID) // look up the task in Postgres (with Redis cache)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "task not found") // either doesn't exist or DB error — both 404 to avoid leaking info
	}

	userID, _ := c.Locals("user_id").(string)   // authenticated user's ID, set by AuthMiddleware
	roles, _ := c.Locals("roles").([]string)     // authenticated user's roles, set by AuthMiddleware
	if task.UserID != userID && !hasRole(roles, "admin") {
		return fiber.NewError(fiber.StatusForbidden, "access denied") // user is trying to view someone else's task
	}

	return c.JSON(models.TaskStatusResponse{Task: task, Message: "Task retrieved successfully"})
}

// listTasks handles GET /api/v1/tasks with optional ?status=, ?page=, ?page_size= query params.
// Regular users see only their own tasks. Admins can pass ?user_id= to filter by any user.
func (s *APIServer) listTasks(c *fiber.Ctx) error {
	status := c.Query("status", "")    // filter by task status: "queued", "processing", "completed", "failed"
	page := c.QueryInt("page", 1)      // current page number, 1-indexed
	pageSize := c.QueryInt("page_size", 20) // results per page

	if page < 1 { // clamp page to at least 1 to prevent negative OFFSET in the SQL query
		page = 1
	}
	if pageSize < 1 || pageSize > 100 { // enforce sane bounds: minimum 1, maximum 100 per page
		pageSize = 20
	}

	userID, _ := c.Locals("user_id").(string) // the authenticated user's ID
	roles, _ := c.Locals("roles").([]string)   // the authenticated user's roles

	// By default filter to only the current user's tasks.
	// Admins can pass ?user_id= to view any user's tasks, or omit it to see all tasks.
	filterUserID := userID
	if hasRole(roles, "admin") {
		filterUserID = c.Query("user_id", "") // empty string means "all users" in the storage query
	}

	ctx, cancel := context.WithTimeout(c.Context(), 10*time.Second) // list queries can be slower than single lookups
	defer cancel()

	// storage.ListTasks returns the page of tasks and the total count for pagination metadata.
	tasks, total, err := s.storage.ListTasks(ctx, models.TaskStatus(status), filterUserID, pageSize, (page-1)*pageSize)
	if err != nil {
		slog.Error("failed to list tasks", slog.String("error", err.Error()))
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list tasks")
	}

	return c.JSON(models.ListTasksResponse{
		Tasks:    tasks,    // the current page of task records
		Total:    total,    // total matching tasks across all pages (for the frontend to compute page count)
		Page:     page,     // current page number (echoed back)
		PageSize: pageSize, // page size used (echoed back in case client's value was clamped)
	})
}

// deleteTask handles DELETE /api/v1/tasks/:id.
// Removes the task from Postgres. Enforces ownership — users can only delete their own tasks.
func (s *APIServer) deleteTask(c *fiber.Ctx) error {
	taskID := c.Params("id") // extract task ID from the URL

	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second)
	defer cancel()

	task, err := s.storage.GetTask(ctx, taskID) // fetch first to enforce ownership before deleting
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "task not found")
	}

	userID, _ := c.Locals("user_id").(string)
	roles, _ := c.Locals("roles").([]string)
	if task.UserID != userID && !hasRole(roles, "admin") {
		return fiber.NewError(fiber.StatusForbidden, "access denied") // can't delete someone else's task
	}

	if err := s.storage.DeleteTask(ctx, taskID); err != nil { // delete from Postgres and invalidate Redis cache
		return fiber.NewError(fiber.StatusInternalServerError, "failed to delete task")
	}

	return c.JSON(fiber.Map{"message": "task deleted successfully"})
}

// getMetrics handles GET /api/v1/metrics/system.
// Returns aggregate stats like total tasks per status, worker heartbeats, etc.
// Used by the frontend dashboard and admin monitoring pages.
func (s *APIServer) getMetrics(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second)
	defer cancel()

	metrics, err := s.storage.GetMetrics(ctx) // queries Postgres for aggregate task and worker stats
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to get metrics")
	}

	return c.JSON(metrics) // returns SystemMetrics struct containing task counts, worker info, etc.
}

// healthCheck handles GET /api/v1/health.
// Runs all registered dependency checks (Postgres, Redis) and returns their individual statuses.
// Returns HTTP 503 if any check is unhealthy so load balancers can remove this instance.
func (s *APIServer) healthCheck(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second) // all checks must complete within 5s total
	defer cancel()

	results := s.healthChecker.CheckAll(ctx) // run all registered checks (postgres + redis) concurrently
	overallStatus := "healthy"               // optimistic default; set to "unhealthy" if any check fails
	for _, result := range results {
		if result.Status == reliability.HealthStatusUnhealthy {
			overallStatus = "unhealthy" // at least one dependency is down — mark the whole server unhealthy
			break                       // no need to check the rest
		}
	}

	statusCode := fiber.StatusOK // 200 if healthy
	if overallStatus == "unhealthy" {
		statusCode = fiber.StatusServiceUnavailable // 503 signals to load balancers to stop sending traffic here
	}

	return c.Status(statusCode).JSON(fiber.Map{"status": overallStatus, "checks": results}) // include per-check details for debugging
}

// login handles POST /api/v1/auth/login.
// Validates credentials against the database and returns a signed JWT token on success.
func (s *APIServer) login(c *fiber.Ctx) error {
	var req struct {
		Username string `json:"username"` // login identifier
		Password string `json:"password"` // plaintext password (compared against bcrypt hash in DB)
	}

	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request") // malformed JSON body
	}

	if req.Username == "" || req.Password == "" {
		return fiber.NewError(fiber.StatusBadRequest, "username and password are required") // validate both fields before hitting the DB
	}

	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second)
	defer cancel()

	user, err := s.storage.GetUserByUsername(ctx, req.Username) // look up user by username in Postgres
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid credentials") // user not found — return same error as wrong password to prevent username enumeration
	}

	if !security.CheckPassword(req.Password, user.Password) {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid credentials") // bcrypt comparison failed — wrong password
	}

	// Credentials are valid — generate a signed JWT containing user ID, email, and roles.
	token, err := s.authService.GenerateToken(user.ID, user.Email, user.Roles)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to generate token")
	}

	return c.JSON(fiber.Map{
		"token": token, // client stores this JWT and sends it as "Authorization: Bearer <token>" on every request
		"user": fiber.Map{
			"id":       user.ID,
			"username": user.Username,
			"email":    user.Email,
			"roles":    user.Roles, // client uses roles to show/hide UI features (e.g. admin panel)
		},
	})
}

// logout handles POST /api/v1/auth/logout.
// Adds the current JWT to a Redis blacklist so it cannot be used again before it expires.
// Without this, JWTs would remain valid until their natural expiry even after logout.
func (s *APIServer) logout(c *fiber.Ctx) error {
	authHeader := c.Get("Authorization")           // read the "Authorization: Bearer <token>" header
	parts := strings.Split(authHeader, " ")        // split into ["Bearer", "<token>"]
	if len(parts) != 2 {
		return fiber.NewError(fiber.StatusBadRequest, "invalid authorization header") // header missing or malformed
	}
	if err := s.authService.BlacklistToken(parts[1]); err != nil { // store token in Redis with TTL equal to its remaining validity
		return fiber.NewError(fiber.StatusInternalServerError, "failed to logout")
	}
	return c.JSON(fiber.Map{"message": "logged out successfully"})
}

// register handles POST /api/v1/auth/register.
// Creates a new user account and immediately returns a JWT so the user is logged in on signup.
func (s *APIServer) register(c *fiber.Ctx) error {
	var req struct {
		Username string `json:"username"` // chosen login identifier
		Email    string `json:"email"`    // contact email and secondary identifier
		Password string `json:"password"` // will be hashed with bcrypt before storage
	}

	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request") // malformed JSON
	}

	if req.Username == "" || req.Email == "" || req.Password == "" {
		return fiber.NewError(fiber.StatusBadRequest, "username, email, and password are required") // all three fields are mandatory
	}

	if !strings.Contains(req.Email, "@") || !strings.Contains(req.Email, ".") {
		return fiber.NewError(fiber.StatusBadRequest, "invalid email format") // basic format check before hitting the DB
	}

	if len(req.Password) < 8 {
		return fiber.NewError(fiber.StatusBadRequest, "password must be at least 8 characters") // minimum length policy
	}

	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second)
	defer cancel()

	// Check uniqueness before inserting to give a friendlier error than a DB constraint violation.
	if _, err := s.storage.GetUserByUsername(ctx, req.Username); err == nil {
		return fiber.NewError(fiber.StatusConflict, "username already exists") // GetUserByUsername returning nil error means the user was found
	}

	if _, err := s.storage.GetUserByEmail(ctx, req.Email); err == nil {
		return fiber.NewError(fiber.StatusConflict, "email already exists") // same pattern — nil error means found → conflict
	}

	passwordHash, err := security.HashPassword(req.Password) // bcrypt-hash the plaintext password; we never store raw passwords
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to process password")
	}

	apiKey, err := s.authService.GenerateAPIKey() // generate a random API key for programmatic access (alternative to JWT)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to generate API key")
	}

	// Build the User model with default "user" role.
	// Only admins can upgrade roles later via PUT /api/v1/users/:id.
	user := &models.User{
		ID:        uuid.New().String(), // globally unique user ID
		Username:  req.Username,        // as provided
		Email:     req.Email,           // as provided
		Password:  passwordHash,        // bcrypt hash — never the raw plaintext
		APIKey:    apiKey,              // random API key for programmatic access
		Roles:     []string{"user"},    // new accounts always start with the "user" role
		CreatedAt: time.Now(),          // creation timestamp
	}

	if err := s.storage.CreateUser(ctx, user); err != nil {
		// Handle race condition: if two requests registered the same user concurrently,
		// the DB unique constraint fires here even though both username/email checks passed.
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "UNIQUE constraint") {
			return fiber.NewError(fiber.StatusConflict, "user already exists")
		}
		return fiber.NewError(fiber.StatusInternalServerError, "failed to create user")
	}

	// Generate a JWT immediately so the user is logged in right after registration.
	token, err := s.authService.GenerateToken(user.ID, user.Email, user.Roles)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to generate token")
	}

	slog.Info("user registered", slog.String("username", req.Username), slog.String("email", req.Email))

	// Return 201 Created with the token, user info, and API key.
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"message": "user registered successfully",
		"token":   token,   // JWT for immediate use in subsequent requests
		"user": fiber.Map{
			"id":       user.ID,
			"username": user.Username,
			"email":    user.Email,
		},
		"api_key": user.APIKey, // returned once on registration; user must save it (can't be retrieved again)
	})
}

// getCurrentUser handles GET /api/v1/users/me.
// Returns the authenticated user's own profile without needing to know their ID.
func (s *APIServer) getCurrentUser(c *fiber.Ctx) error {
	userID, ok := c.Locals("user_id").(string) // injected by AuthMiddleware after token validation
	if !ok {
		return fiber.NewError(fiber.StatusUnauthorized, "unauthorized") // token didn't contain user_id claim
	}

	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second)
	defer cancel()

	user, err := s.storage.GetUserByID(ctx, userID) // fetch from Postgres (potentially cached in Redis)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user not found") // user was deleted after issuing the token
	}

	return c.JSON(fiber.Map{
		"id":         user.ID,
		"username":   user.Username,
		"email":      user.Email,
		"roles":      user.Roles,      // so the client knows if the user has admin privileges
		"created_at": user.CreatedAt,  // when the account was created
	})
}

// updateCurrentUser handles PUT /api/v1/users/me.
// Allows users to change their own email or password. Only fields provided are updated.
func (s *APIServer) updateCurrentUser(c *fiber.Ctx) error {
	userID, ok := c.Locals("user_id").(string)
	if !ok {
		return fiber.NewError(fiber.StatusUnauthorized, "unauthorized")
	}

	var req struct {
		Email    string `json:"email"`    // optional: new email address
		Password string `json:"password"` // optional: new plaintext password (will be hashed)
	}
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second)
	defer cancel()

	user, err := s.storage.GetUserByID(ctx, userID) // fetch current state to apply partial updates
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user not found")
	}

	if req.Email != "" { // only update email if a new one was provided
		if !strings.Contains(req.Email, "@") {
			return fiber.NewError(fiber.StatusBadRequest, "invalid email format") // basic format check
		}
		existing, err := s.storage.GetUserByEmail(ctx, req.Email) // check if another account already uses this email
		if err == nil && existing.ID != userID {
			return fiber.NewError(fiber.StatusConflict, "email already in use") // another user owns this email
		}
		user.Email = req.Email // apply the email change to the fetched user model
	}

	if req.Password != "" { // only update password if a new one was provided
		if len(req.Password) < 8 {
			return fiber.NewError(fiber.StatusBadRequest, "password must be at least 8 characters")
		}
		hash, err := security.HashPassword(req.Password) // bcrypt the new password before storing
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "failed to process password")
		}
		user.Password = hash // replace the old hash with the new one
	}

	if err := s.storage.UpdateUser(ctx, user); err != nil { // persist the updated user to Postgres
		return fiber.NewError(fiber.StatusInternalServerError, "failed to update user")
	}

	return c.JSON(fiber.Map{
		"message": "user updated successfully",
		"user": fiber.Map{
			"id":       user.ID,
			"username": user.Username,
			"email":    user.Email,
			"roles":    user.Roles,
		},
	})
}

// listUsers handles GET /api/v1/users (admin only).
// Returns a paginated list of all user accounts in the system.
func (s *APIServer) listUsers(c *fiber.Ctx) error {
	page := c.QueryInt("page", 1)
	pageSize := c.QueryInt("page_size", 20)
	if page < 1 { // clamp to valid range
		page = 1
	}
	if pageSize < 1 || pageSize > 100 { // cap at 100 to prevent very large responses
		pageSize = 20
	}

	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second)
	defer cancel()

	users, total, err := s.storage.ListUsers(ctx, pageSize, (page-1)*pageSize) // fetch page of users with total count
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list users")
	}

	return c.JSON(fiber.Map{
		"users":     users,     // current page of user records
		"total":     total,     // total user count for pagination
		"page":      page,      // current page
		"page_size": pageSize,  // effective page size
	})
}

// getUserByID handles GET /api/v1/users/:id (admin only).
// Admins can look up any user's profile by their UUID.
func (s *APIServer) getUserByID(c *fiber.Ctx) error {
	userID := c.Params("id") // target user ID from the URL
	if userID == "" {
		return fiber.NewError(fiber.StatusBadRequest, "user ID is required")
	}

	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second)
	defer cancel()

	user, err := s.storage.GetUserByID(ctx, userID)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user not found")
	}

	return c.JSON(fiber.Map{
		"id":         user.ID,
		"username":   user.Username,
		"email":      user.Email,
		"roles":      user.Roles,
		"created_at": user.CreatedAt,
	})
}

// updateUser handles PUT /api/v1/users/:id (admin only).
// Admins can change any user's email or roles (e.g., to promote someone to "admin").
func (s *APIServer) updateUser(c *fiber.Ctx) error {
	userID := c.Params("id") // target user ID from the URL
	if userID == "" {
		return fiber.NewError(fiber.StatusBadRequest, "user ID is required")
	}

	var req struct {
		Email string   `json:"email"` // optional: new email
		Roles []string `json:"roles"` // optional: replace the user's entire roles list
	}
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second)
	defer cancel()

	user, err := s.storage.GetUserByID(ctx, userID) // fetch current user to apply changes
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user not found")
	}

	if req.Email != "" {
		if !strings.Contains(req.Email, "@") {
			return fiber.NewError(fiber.StatusBadRequest, "invalid email format")
		}
		user.Email = req.Email // apply new email
	}
	if req.Roles != nil {
		user.Roles = req.Roles // replace entire roles slice (e.g. ["user", "admin"])
	}

	if err := s.storage.UpdateUser(ctx, user); err != nil { // persist changes to Postgres
		return fiber.NewError(fiber.StatusInternalServerError, "failed to update user")
	}

	return c.JSON(fiber.Map{
		"message": "user updated successfully",
		"user": fiber.Map{
			"id":       user.ID,
			"username": user.Username,
			"email":    user.Email,
			"roles":    user.Roles,
		},
	})
}

// deleteUser handles DELETE /api/v1/users/:id (admin only).
// Permanently removes a user from the database.
// Prevents self-deletion to avoid accidentally removing the last admin account.
func (s *APIServer) deleteUser(c *fiber.Ctx) error {
	userID := c.Params("id") // target user ID from the URL
	if userID == "" {
		return fiber.NewError(fiber.StatusBadRequest, "user ID is required")
	}

	currentUserID, _ := c.Locals("user_id").(string) // the admin performing the deletion
	if currentUserID == userID {
		return fiber.NewError(fiber.StatusBadRequest, "cannot delete your own account") // safety guard: admins can't delete themselves
	}

	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second)
	defer cancel()

	// storage.DeleteUser issues DELETE FROM users WHERE id = $1 and returns an error if not found.
	if err := s.storage.DeleteUser(ctx, userID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return fiber.NewError(fiber.StatusNotFound, "user not found") // ID doesn't exist in the DB
		}
		return fiber.NewError(fiber.StatusInternalServerError, "failed to delete user")
	}

	return c.JSON(fiber.Map{"message": "user deleted successfully"})
}

// handleWebSocket is called when a client upgrades to a WebSocket connection on GET /api/v1/ws.
// It registers the connection with the hub, then blocks reading messages until the connection closes.
// We don't currently act on client-sent messages, but reading is required to detect disconnections.
func (s *APIServer) handleWebSocket(c *websocket.Conn) {
	defer func() {
		s.wsHub.unregister <- c // on any exit (disconnect or error), tell the hub to remove this connection
		c.Close()               // ensure the underlying TCP socket is closed
	}()
	s.wsHub.register <- c // add this connection to the hub so it starts receiving task update broadcasts
	for {
		if _, _, err := c.ReadMessage(); err != nil { // block waiting for client messages; error means client disconnected
			break // exit the loop → defer runs → connection is unregistered and closed
		}
	}
}

// broadcastTaskUpdate sends a task status update to all connected WebSocket clients.
// Called after every task submission and status change so the browser UI updates in real-time.
func (s *APIServer) broadcastTaskUpdate(task *models.Task) {
	msg := models.WebSocketMessage{
		Type:      "task_update", // message type — clients use this to route to the right handler
		Data:      task,          // the full task object so clients can update their local state
		Timestamp: time.Now(),    // when this update was broadcast
	}
	msgJSON, err := json.Marshal(msg) // serialize to JSON for WebSocket transmission
	if err != nil {
		return // marshaling should never fail for this struct; if it does, silently skip the broadcast
	}
	select {
	case s.wsHub.broadcast <- msgJSON: // send to the hub's broadcast channel for fan-out to all clients
	default: // if the channel buffer is full (256 slots), drop this message rather than blocking the HTTP request
	}
}

// customErrorHandler replaces Fiber's default plain-text error responses with structured JSON.
// It also increments the Prometheus error counter for every error, broken down by HTTP status code.
// tracingMiddleware creates an OTel server span for every HTTP request.
// It extracts incoming W3C trace context from request headers (set by upstream load balancers
// or the browser via OpenTelemetry JS SDK) and stores the enriched context in c.UserContext()
// so downstream handlers can start child spans or inject the context into broker messages.
func tracingMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Collect request headers into a plain map for OTel extraction.
		headers := make(map[string]string)
		c.Request().Header.VisitAll(func(key, val []byte) {
			headers[string(key)] = string(val)
		})
		ctx := tracing.ExtractFromHeaders(c.UserContext(), headers)

		// Start a server-kind span named after the HTTP method + path.
		ctx, span := tracing.Start(ctx, c.Method()+" "+c.Path(),
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.method", c.Method()),
				attribute.String("http.target", c.OriginalURL()),
			),
		)
		defer span.End()

		// Store the enriched context so handlers can use it via c.UserContext().
		c.SetUserContext(ctx)

		err := c.Next()

		span.SetAttributes(attribute.Int("http.status_code", c.Response().StatusCode()))
		if err != nil {
			span.RecordError(err)
		}
		return err
	}
}

// cancelTask handles POST /api/v1/tasks/:id/cancel.
// Sets a Redis cancellation signal and updates the DB status so workers skip the task.
// Works for tasks in queued, processing, or retrying states.
func (s *APIServer) cancelTask(c *fiber.Ctx) error {
	taskID := c.Params("id")
	if taskID == "" {
		return fiber.NewError(fiber.StatusBadRequest, "task ID is required")
	}

	userID, _ := c.Locals("user_id").(string)
	roles, _ := c.Locals("roles").([]string)

	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second)
	defer cancel()

	task, err := s.storage.GetTask(ctx, taskID)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "task not found")
	}

	if task.UserID != userID && !hasRole(roles, "admin") {
		return fiber.NewError(fiber.StatusForbidden, "access denied")
	}

	// Only cancellable while the task hasn't reached a terminal state.
	switch task.Status {
	case models.StatusCompleted, models.StatusFailed, models.StatusCancelled:
		return fiber.NewError(fiber.StatusConflict,
			"task cannot be cancelled: already in "+string(task.Status)+" state")
	}

	// Set a Redis key as a fast-path signal for the worker.
	// TTL of 2h covers the maximum realistic processing window; the key self-cleans after that.
	cancelKey := "task:cancel:" + taskID
	if err := s.redisClient.Set(ctx, cancelKey, 1, 2*time.Hour).Err(); err != nil {
		slog.Error("failed to set cancel signal", slog.String("task_id", taskID), slog.String("error", err.Error()))
		// Non-fatal: the DB status update below is the authoritative cancellation record.
	}

	task.Status = models.StatusCancelled
	if err := s.storage.UpdateTask(ctx, task); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to cancel task")
	}

	slog.Info("task cancelled", slog.String("task_id", taskID), slog.String("cancelled_by", userID))
	s.broadcastTaskUpdate(task)

	return c.JSON(fiber.Map{
		"message": "task cancelled successfully",
		"task_id": taskID,
		"status":  task.Status,
	})
}

// submitBatch handles POST /api/v1/tasks/batch.
//
// The caller sends one task type plus a slice of per-image payloads:
//
//	{ "type": "image_resize", "images": [{...}, {...}], "priority": 0, "max_retries": 3 }
//
// For each payload the handler creates an independent Task row in Postgres, tags
// it with the same batch_id, and publishes it to the Redis Stream.  Workers pick
// up the tasks concurrently via the consumer group — no worker code changes are
// needed because each individual task looks identical to a single submission.
// A Batch metadata row is written first so the batch_id exists before any task
// references it.
func (s *APIServer) submitBatch(c *fiber.Ctx) error {
	userID, ok := c.Locals("user_id").(string)
	if !ok || userID == "" {
		return fiber.NewError(fiber.StatusUnauthorized, "unauthorized")
	}

	var req models.BatchSubmitRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}
	if !isValidTaskType(req.Type) {
		return fiber.NewError(fiber.StatusBadRequest, "unsupported task type: "+string(req.Type))
	}
	if len(req.Images) == 0 {
		return fiber.NewError(fiber.StatusBadRequest, "images array must not be empty")
	}
	const maxBatchSize = 500
	if len(req.Images) > maxBatchSize {
		return fiber.NewError(fiber.StatusBadRequest,
			fmt.Sprintf("batch too large: maximum %d images per request", maxBatchSize))
	}

	ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
	defer cancel()

	batchID := uuid.New().String()
	now := time.Now()

	// Write the batch metadata row first so all tasks can reference the batch_id.
	batch := &models.Batch{
		ID:        batchID,
		UserID:    userID,
		Type:      req.Type,
		Total:     len(req.Images),
		CreatedAt: now,
	}
	if err := s.storage.CreateBatch(ctx, batch); err != nil {
		slog.Error("failed to create batch", slog.String("error", err.Error()))
		return fiber.NewError(fiber.StatusInternalServerError, "failed to create batch")
	}

	// Create one Task per image payload and publish each to the Redis Stream.
	// Tasks are independent — if one fails, the rest continue unaffected.
	for i, payload := range req.Images {
		task := &models.Task{
			ID:         uuid.New().String(),
			UserID:     userID,
			BatchID:    batchID,
			Type:       req.Type,
			Payload:    payload,
			Status:     models.StatusQueued,
			Priority:   req.Priority,
			MaxRetries: req.MaxRetries,
			CreatedAt:  now,
		}
		if err := s.storage.CreateTask(ctx, task); err != nil {
			slog.Error("batch: failed to create task",
				slog.String("batch_id", batchID),
				slog.Int("index", i),
				slog.String("error", err.Error()),
			)
			return fiber.NewError(fiber.StatusInternalServerError, "failed to create task in batch")
		}
		if err := s.broker.Publish(ctx, task); err != nil {
			slog.Error("batch: failed to publish task",
				slog.String("batch_id", batchID),
				slog.Int("index", i),
				slog.String("error", err.Error()),
			)
			return fiber.NewError(fiber.StatusInternalServerError, "failed to queue task in batch")
		}
	}

	slog.Info("batch submitted",
		slog.String("batch_id", batchID),
		slog.String("user_id", userID),
		slog.Int("total", len(req.Images)),
		slog.String("type", string(req.Type)),
	)

	return c.Status(fiber.StatusCreated).JSON(models.BatchSubmitResponse{
		BatchID:   batchID,
		Total:     len(req.Images),
		Status:    "queued",
		CreatedAt: now,
	})
}

// getBatch handles GET /api/v1/batches/:id.
//
// It enforces ownership (users can only see their own batches) and returns a
// live progress snapshot computed by aggregating task statuses in a single SQL
// query — no separate counter columns that could go out of sync.
func (s *APIServer) getBatch(c *fiber.Ctx) error {
	batchID := c.Params("id")
	userID, _ := c.Locals("user_id").(string)

	ctx, cancel := context.WithTimeout(c.UserContext(), 5*time.Second)
	defer cancel()

	batch, err := s.storage.GetBatch(ctx, batchID)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "batch not found")
	}
	if batch.UserID != userID && !hasRole(c.Locals("roles").([]string), "admin") {
		return fiber.NewError(fiber.StatusForbidden, "access denied")
	}
	return c.JSON(batch)
}

// oauthRedirect handles GET /api/v1/auth/oauth/:provider.
// It generates a random CSRF state, saves it, then redirects the browser to
// the provider's consent screen.
func (s *APIServer) oauthRedirect(c *fiber.Ctx) error {
	providerName := c.Params("provider")
	provider := oauth.Provider(providerName)

	state, err := oauth.GenerateState()
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to generate OAuth state")
	}

	if err := s.oauthStates.Save(c.UserContext(), state, providerName, 5*time.Minute); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to save OAuth state")
	}

	url, err := s.oauthCfg.AuthCodeURL(provider, state)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "unsupported OAuth provider: "+providerName)
	}

	return c.Redirect(url, fiber.StatusTemporaryRedirect)
}

// oauthCallback handles GET /api/v1/auth/oauth/:provider/callback.
// It verifies the state, exchanges the authorization code for a token, fetches
// user info from the provider, finds or creates a local user, then returns a JWT.
func (s *APIServer) oauthCallback(c *fiber.Ctx) error {
	providerName := c.Params("provider")
	provider := oauth.Provider(providerName)
	state := c.Query("state")
	code := c.Query("code")

	if state == "" || code == "" {
		return fiber.NewError(fiber.StatusBadRequest, "missing state or code parameter")
	}

	ctx, cancel := context.WithTimeout(c.UserContext(), 15*time.Second)
	defer cancel()

	// Verify and consume the state to prevent CSRF.
	if err := s.oauthStates.Verify(ctx, state, providerName); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid or expired OAuth state")
	}

	// Exchange authorization code for an access token.
	token, err := s.oauthCfg.Exchange(ctx, provider, code)
	if err != nil {
		slog.Error("oauth code exchange failed", slog.String("provider", providerName), slog.String("error", err.Error()))
		return fiber.NewError(fiber.StatusBadRequest, "failed to exchange OAuth code")
	}

	// Fetch normalized user info from the provider.
	userInfo, err := s.oauthCfg.FetchUserInfo(ctx, provider, token)
	if err != nil {
		slog.Error("oauth userinfo fetch failed", slog.String("provider", providerName), slog.String("error", err.Error()))
		return fiber.NewError(fiber.StatusInternalServerError, "failed to fetch user info from provider")
	}

	// Look up an existing user tied to this (provider, provider-scoped ID) pair.
	user, err := s.storage.GetUserByOAuth(ctx, providerName, userInfo.ID)
	if err != nil {
		// No existing OAuth user — check if the email is already registered locally.
		if userInfo.Email != "" {
			existing, lookupErr := s.storage.GetUserByEmail(ctx, userInfo.Email)
			if lookupErr == nil {
				// Link the OAuth identity to the existing account.
				existing.OAuthProvider = providerName
				existing.OAuthID = userInfo.ID
				if updateErr := s.storage.UpdateUser(ctx, existing); updateErr != nil {
					slog.Error("failed to link oauth to existing user", slog.String("error", updateErr.Error()))
				}
				user = existing
			}
		}

		// Still no user — create a new one.
		if user == nil {
			username := deriveUsername(userInfo.Name)
			user = &models.User{
				ID:            uuid.New().String(),
				Username:      username,
				Email:         userInfo.Email,
				Roles:         []string{"user"},
				OAuthProvider: providerName,
				OAuthID:       userInfo.ID,
				CreatedAt:     time.Now(),
			}
			if createErr := s.storage.CreateUser(ctx, user); createErr != nil {
				slog.Error("failed to create oauth user", slog.String("error", createErr.Error()))
				return fiber.NewError(fiber.StatusInternalServerError, "failed to create user account")
			}
		}
	}

	// Issue a JWT the same way as the password login flow.
	jwtToken, err := s.authService.GenerateToken(user.ID, user.Email, user.Roles)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to generate token")
	}

	slog.Info("oauth login successful",
		slog.String("provider", providerName),
		slog.String("user_id", user.ID),
	)

	return c.JSON(fiber.Map{
		"token": jwtToken,
		"user": fiber.Map{
			"id":       user.ID,
			"username": user.Username,
			"email":    user.Email,
		},
	})
}

// deriveUsername turns a provider display name (e.g. "John Doe") into a
// URL-safe username by lowercasing and replacing spaces with underscores.
func deriveUsername(name string) string {
	if name == "" {
		return "user_" + uuid.New().String()[:8]
	}
	result := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		ch := name[i]
		switch {
		case ch >= 'A' && ch <= 'Z':
			result = append(result, ch+32) // lowercase
		case ch == ' ' || ch == '-':
			result = append(result, '_')
		case (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_':
			result = append(result, ch)
		}
	}
	if len(result) == 0 {
		return "user_" + uuid.New().String()[:8]
	}
	return string(result)
}

func customErrorHandler(c *fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError // default to 500 if the error isn't a fiber.Error
	message := "internal server error"

	if e, ok := err.(*fiber.Error); ok { // fiber.NewError() wraps errors with a specific HTTP status code
		code = e.Code       // use the specific HTTP status (400, 401, 404, 500, etc.)
		message = e.Message // use the specific error message set by the handler
	}

	monitoring.ErrorsTotal.WithLabelValues("api", fmt.Sprintf("%d", code)).Inc() // count errors by status code in Prometheus

	return c.Status(code).JSON(fiber.Map{
		"error": message,  // human-readable error description
		"code":  code,     // numeric HTTP status code (useful for clients that parse the body instead of the status line)
		"path":  c.Path(), // the request path that triggered the error (helps with debugging)
	})
}

// isValidTaskType returns true only for task types the worker knows how to process.
// This prevents workers from receiving unknown task types that would always fail.
func isValidTaskType(taskType models.TaskType) bool {
	switch taskType {
	case models.TaskImageResize,    // shrink or enlarge an image to specific dimensions
		models.TaskImageCompress,   // reduce file size while preserving quality (or target a specific byte size)
		models.TaskImageWatermark,  // overlay a watermark image on the source
		models.TaskImageFilter,     // apply visual filters (grayscale, sepia, blur, sharpen)
		models.TaskImageThumbnail,  // create a thumbnail with fit mode (fill, fit, stretch) and optional non-square dimensions
		models.TaskImageFormat,     // convert between image formats (jpg, png, webp)
		models.TaskImageCrop,       // crop a rectangular region from the source image
		models.TaskImageResponsive: // generate multiple resized variants in parallel (for responsive HTML img srcset)
		return true
	}
	return false // unknown task type — reject before creating any DB record
}

// hasRole checks whether a slice of role strings contains the target role.
// Used to decide whether a user has admin access to protected resources.
func hasRole(roles []string, role string) bool {
	return slices.Contains(roles, role)
}

// Start begins listening for HTTP connections on the given port.
// It blocks until the server is stopped or encounters a fatal error.
func (s *APIServer) Start(port string) error {
	slog.Info("API server starting", slog.String("port", port))
	return s.app.Listen(":" + port)
}

// Shutdown gracefully stops the server.
// Order matters: stop the WebSocket hub first, then close the broker and storage,
// then tell Fiber to stop accepting new requests and wait for in-flight requests to finish.
func (s *APIServer) Shutdown() error {
	slog.Info("API server shutting down")
	if s.wsHub != nil {
		s.wsHub.Shutdown()
	}
	if s.broker != nil {
		if err := s.broker.Close(); err != nil {
			slog.Error("error closing broker", slog.String("error", err.Error()))
		}
	}
	if s.storage != nil {
		if err := s.storage.Close(); err != nil {
			slog.Error("error closing storage", slog.String("error", err.Error()))
		}
	}
	return s.app.ShutdownWithTimeout(30 * time.Second)
}

// getEnv reads an environment variable, falling back to a default if it isn't set.
// Used for all configuration so the binary can be deployed without code changes.
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" { // os.Getenv returns "" for unset variables
		return value
	}
	return defaultValue // use the default value for local development
}

// main is the application entry point.
// It initializes the server, starts it in a goroutine, and waits for a shutdown signal.
func main() {
	logging.Init(getEnv("LOG_LEVEL", "info"))

	// Init OTel tracing. Non-fatal — if Jaeger is unreachable, spans are dropped silently.
	shutdownTracing, err := tracing.Init("task-queue-api")
	if err != nil {
		slog.Warn("tracing init failed — continuing without traces", slog.String("error", err.Error()))
	} else {
		defer shutdownTracing(context.Background())
	}

	server, err := NewAPIServer()
	if err != nil {
		slog.Error("failed to initialize server", slog.String("error", err.Error()))
		os.Exit(1)
	}

	port := getEnv("API_PORT", "8080")

	go func() {
		if err := server.Start(port); err != nil {
			slog.Error("server failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	slog.Info("shutdown signal received")
	if err := server.Shutdown(); err != nil {
		slog.Error("forced shutdown", slog.String("error", err.Error()))
		os.Exit(1)
	}
	slog.Info("server stopped")
}
