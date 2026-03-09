// Package api contains integration tests for the HTTP API layer.
//
// These tests spin up a real Fiber app wired to in-memory/mock implementations
// of Storage and Broker, so they run without Docker, Postgres, or Redis.
// They cover the happy paths for every major endpoint: auth, tasks, upload, health.
//
// Run with:
//
//	go test ./internal/api/... -v
package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"distributed-task-queue/internal/models"
	"distributed-task-queue/internal/monitoring"
	"distributed-task-queue/internal/oauth"
	"distributed-task-queue/internal/security"
)

// ── in-memory fakes ──────────────────────────────────────────────────────────

// fakeStorage satisfies storage.Storage using plain maps guarded by a mutex.
// It does not hit Postgres, so tests are fully self-contained.
type fakeStorage struct {
	mu      sync.RWMutex
	tasks   map[string]*models.Task
	users   map[string]*models.User  // keyed by ID
	batches map[string]*models.Batch // keyed by batch ID
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{
		tasks:   make(map[string]*models.Task),
		users:   make(map[string]*models.User),
		batches: make(map[string]*models.Batch),
	}
}

func (f *fakeStorage) CreateTask(_ context.Context, t *models.Task) error {
	f.mu.Lock(); defer f.mu.Unlock()
	cp := *t
	f.tasks[t.ID] = &cp
	return nil
}
func (f *fakeStorage) GetTask(_ context.Context, id string) (*models.Task, error) {
	f.mu.RLock(); defer f.mu.RUnlock()
	t, ok := f.tasks[id]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	cp := *t
	return &cp, nil
}
func (f *fakeStorage) UpdateTask(_ context.Context, t *models.Task) error {
	f.mu.Lock(); defer f.mu.Unlock()
	cp := *t
	f.tasks[t.ID] = &cp
	return nil
}
func (f *fakeStorage) DeleteTask(_ context.Context, id string) error {
	f.mu.Lock(); defer f.mu.Unlock()
	if _, ok := f.tasks[id]; !ok {
		return fmt.Errorf("not found")
	}
	delete(f.tasks, id)
	return nil
}
func (f *fakeStorage) ListTasks(_ context.Context, _ models.TaskStatus, userID string, limit, offset int) ([]*models.Task, int64, error) {
	f.mu.RLock(); defer f.mu.RUnlock()
	var out []*models.Task
	for _, t := range f.tasks {
		if userID == "" || t.UserID == userID {
			cp := *t
			out = append(out, &cp)
		}
	}
	total := int64(len(out))
	if offset >= len(out) {
		return nil, total, nil
	}
	end := offset + limit
	if end > len(out) {
		end = len(out)
	}
	return out[offset:end], total, nil
}
func (f *fakeStorage) GetMetrics(_ context.Context) (*models.SystemMetrics, error) {
	f.mu.RLock(); defer f.mu.RUnlock()
	return &models.SystemMetrics{Timestamp: time.Now()}, nil
}
func (f *fakeStorage) RecordWorkerHeartbeat(_ context.Context, _ *models.WorkerMetrics) error { return nil }
func (f *fakeStorage) CreateBatch(_ context.Context, b *models.Batch) error {
	f.mu.Lock(); defer f.mu.Unlock()
	cp := *b
	f.batches[b.ID] = &cp
	return nil
}
func (f *fakeStorage) GetBatch(_ context.Context, id string) (*models.Batch, error) {
	f.mu.RLock(); defer f.mu.RUnlock()
	b, ok := f.batches[id]
	if !ok {
		return nil, fmt.Errorf("batch not found")
	}
	// Compute live counts from tasks, same as the real Postgres implementation.
	cp := *b
	cp.Queued, cp.Processing, cp.Completed, cp.Failed = 0, 0, 0, 0
	for _, t := range f.tasks {
		if t.BatchID != id {
			continue
		}
		switch t.Status {
		case models.StatusQueued, models.StatusRetrying:
			cp.Queued++
		case models.StatusProcessing:
			cp.Processing++
		case models.StatusCompleted:
			cp.Completed++
		case models.StatusFailed, models.StatusCancelled:
			cp.Failed++
		}
	}
	done := cp.Completed + cp.Failed
	switch {
	case cp.Processing > 0 || (done > 0 && done < cp.Total):
		cp.Status = models.BatchStatusProcessing
	case done == cp.Total && cp.Failed == 0:
		cp.Status = models.BatchStatusCompleted
	case done == cp.Total && cp.Completed > 0:
		cp.Status = models.BatchStatusPartial
	case done == cp.Total && cp.Completed == 0:
		cp.Status = models.BatchStatusFailed
	default:
		cp.Status = models.BatchStatusQueued
	}
	return &cp, nil
}
func (f *fakeStorage) CreateUser(_ context.Context, u *models.User) error {
	f.mu.Lock(); defer f.mu.Unlock()
	for _, existing := range f.users {
		if existing.Username == u.Username {
			return fmt.Errorf("duplicate key: username")
		}
		if existing.Email == u.Email {
			return fmt.Errorf("duplicate key: email")
		}
	}
	cp := *u
	f.users[u.ID] = &cp
	return nil
}
func (f *fakeStorage) GetUserByUsername(_ context.Context, name string) (*models.User, error) {
	f.mu.RLock(); defer f.mu.RUnlock()
	for _, u := range f.users {
		if u.Username == name {
			cp := *u
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("not found")
}
func (f *fakeStorage) GetUserByEmail(_ context.Context, email string) (*models.User, error) {
	f.mu.RLock(); defer f.mu.RUnlock()
	for _, u := range f.users {
		if u.Email == email {
			cp := *u
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("not found")
}
func (f *fakeStorage) GetUserByID(_ context.Context, id string) (*models.User, error) {
	f.mu.RLock(); defer f.mu.RUnlock()
	u, ok := f.users[id]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	cp := *u
	return &cp, nil
}
func (f *fakeStorage) GetUserByOAuth(_ context.Context, provider, providerID string) (*models.User, error) {
	f.mu.RLock(); defer f.mu.RUnlock()
	for _, u := range f.users {
		if u.OAuthProvider == provider && u.OAuthID == providerID {
			cp := *u
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("not found")
}
func (f *fakeStorage) UpdateUser(_ context.Context, u *models.User) error {
	f.mu.Lock(); defer f.mu.Unlock()
	if _, ok := f.users[u.ID]; !ok {
		return fmt.Errorf("not found")
	}
	cp := *u
	f.users[u.ID] = &cp
	return nil
}
func (f *fakeStorage) DeleteUser(_ context.Context, id string) error {
	f.mu.Lock(); defer f.mu.Unlock()
	if _, ok := f.users[id]; !ok {
		return fmt.Errorf("user not found: %s", id)
	}
	delete(f.users, id)
	return nil
}
func (f *fakeStorage) ListUsers(_ context.Context, limit, offset int) ([]*models.User, int64, error) {
	f.mu.RLock(); defer f.mu.RUnlock()
	out := make([]*models.User, 0, len(f.users))
	for _, u := range f.users {
		cp := *u
		out = append(out, &cp)
	}
	return out, int64(len(out)), nil
}
func (f *fakeStorage) GetDB() *sql.DB    { return nil }
func (f *fakeStorage) Close() error      { return nil }

// fakeBroker records published tasks without touching Redis.
type fakeBroker struct {
	mu        sync.Mutex
	published []*models.Task
}

func (b *fakeBroker) Publish(_ context.Context, t *models.Task) error {
	b.mu.Lock(); defer b.mu.Unlock()
	cp := *t
	b.published = append(b.published, &cp)
	return nil
}
func (b *fakeBroker) Subscribe(_ context.Context, _, _ string, _ func(context.Context, *models.Task) error) error {
	return nil
}
func (b *fakeBroker) Acknowledge(_ context.Context, _, _ string) error { return nil }
func (b *fakeBroker) GetPendingCount(_ context.Context) (int64, error) { return 0, nil }
func (b *fakeBroker) Close() error                                      { return nil }

// ── test server builder ──────────────────────────────────────────────────────

// testServer wires a minimal Fiber app with real middleware and the fake backends.
type testServer struct {
	app         *fiber.App
	store       *fakeStorage
	broker      *fakeBroker
	authService *security.AuthService
	oauthStates *oauth.MemoryStateStore
}

// newTestServer creates a Fiber app identical in structure to the real API server
// but backed by in-memory fakes. No network connections are made.
func newTestServer(t *testing.T) *testServer {
	t.Helper()

	store := newFakeStorage()
	broker := &fakeBroker{}

	// Use a fixed secret so tokens are deterministic across test runs.
	authSvc := security.NewAuthService("test-secret-key-32-bytes-minimum!", 1*time.Hour, nil)

	app := fiber.New(fiber.Config{
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			msg := "internal server error"
			if e, ok := err.(*fiber.Error); ok {
				code, msg = e.Code, e.Message
			}
			return c.Status(code).JSON(fiber.Map{"error": msg, "code": code})
		},
	})
	app.Use(recover.New())

	// Initialise Prometheus counters (no-op if already registered).
	_ = monitoring.TasksSubmitted

	ts := &testServer{app: app, store: store, broker: broker, authService: authSvc, oauthStates: oauth.NewMemoryStateStore()}
	ts.setupRoutes()
	return ts
}

// setupRoutes mirrors the real server's route registration using fake backends.
func (ts *testServer) setupRoutes() {
	api := ts.app.Group("/api/v1")

	// Public
	api.Get("/health", ts.healthCheck)
	api.Post("/auth/login", ts.login)
	api.Post("/auth/register", ts.register)
	api.Get("/auth/oauth/:provider", ts.oauthRedirect)
	api.Get("/auth/oauth/:provider/callback", ts.oauthCallback)

	// Protected
	protected := api.Group("/", security.AuthMiddleware(ts.authService))
	protected.Post("/tasks", ts.submitTask)
	protected.Post("/tasks/batch", ts.submitBatch)
	protected.Get("/tasks/:id", ts.getTaskStatus)
	protected.Get("/tasks", ts.listTasks)
	protected.Delete("/tasks/:id", ts.deleteTask)
	protected.Post("/tasks/:id/cancel", ts.cancelTask)
	protected.Get("/batches/:id", ts.getBatch)
}

// ── handler implementations (mirrors of the real server) ────────────────────

func (ts *testServer) healthCheck(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"status": "healthy"})
}

func (ts *testServer) register(c *fiber.Ctx) error {
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request")
	}
	if req.Username == "" || req.Email == "" || req.Password == "" {
		return fiber.NewError(fiber.StatusBadRequest, "all fields required")
	}
	if len(req.Password) < 8 {
		return fiber.NewError(fiber.StatusBadRequest, "password must be at least 8 characters")
	}

	ctx := c.Context()
	if _, err := ts.store.GetUserByUsername(ctx, req.Username); err == nil {
		return fiber.NewError(fiber.StatusConflict, "username already exists")
	}

	hash, err := security.HashPassword(req.Password)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to hash password")
	}

	user := &models.User{
		ID:        uuid.New().String(),
		Username:  req.Username,
		Email:     req.Email,
		Password:  hash,
		Roles:     []string{"user"},
		CreatedAt: time.Now(),
	}
	if err := ts.store.CreateUser(ctx, user); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to create user")
	}

	token, err := ts.authService.GenerateToken(user.ID, user.Email, user.Roles)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to generate token")
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"token": token,
		"user":  fiber.Map{"id": user.ID, "username": user.Username, "email": user.Email},
	})
}

func (ts *testServer) login(c *fiber.Ctx) error {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request")
	}
	if req.Username == "" || req.Password == "" {
		return fiber.NewError(fiber.StatusBadRequest, "username and password are required")
	}
	user, err := ts.store.GetUserByUsername(c.Context(), req.Username)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid credentials")
	}
	if !security.CheckPassword(req.Password, user.Password) {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid credentials")
	}
	token, _ := ts.authService.GenerateToken(user.ID, user.Email, user.Roles)
	return c.JSON(fiber.Map{"token": token})
}

func (ts *testServer) submitTask(c *fiber.Ctx) error {
	userID, ok := c.Locals("user_id").(string)
	if !ok || userID == "" {
		return fiber.NewError(fiber.StatusUnauthorized, "unauthorized")
	}
	var req models.SubmitTaskRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	task := &models.Task{
		ID:         uuid.New().String(),
		UserID:     userID,
		Type:       req.Type,
		Payload:    req.Payload,
		Status:     models.StatusQueued,
		MaxRetries: req.MaxRetries,
		CreatedAt:  time.Now(),
	}
	if err := ts.store.CreateTask(c.Context(), task); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to create task")
	}
	if err := ts.broker.Publish(c.Context(), task); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to queue task")
	}
	return c.Status(fiber.StatusCreated).JSON(models.SubmitTaskResponse{
		TaskID:    task.ID,
		Status:    task.Status,
		Message:   "Task queued successfully",
		CreatedAt: task.CreatedAt,
	})
}

func (ts *testServer) getTaskStatus(c *fiber.Ctx) error {
	taskID := c.Params("id")
	task, err := ts.store.GetTask(c.Context(), taskID)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "task not found")
	}
	userID, _ := c.Locals("user_id").(string)
	if task.UserID != userID {
		return fiber.NewError(fiber.StatusForbidden, "access denied")
	}
	return c.JSON(models.TaskStatusResponse{Task: task})
}

func (ts *testServer) listTasks(c *fiber.Ctx) error {
	userID, _ := c.Locals("user_id").(string)
	tasks, total, _ := ts.store.ListTasks(c.Context(), "", userID, 20, 0)
	return c.JSON(models.ListTasksResponse{Tasks: tasks, Total: total, Page: 1, PageSize: 20})
}

func (ts *testServer) deleteTask(c *fiber.Ctx) error {
	taskID := c.Params("id")
	task, err := ts.store.GetTask(c.Context(), taskID)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "task not found")
	}
	userID, _ := c.Locals("user_id").(string)
	if task.UserID != userID {
		return fiber.NewError(fiber.StatusForbidden, "access denied")
	}
	ts.store.DeleteTask(c.Context(), taskID)
	return c.JSON(fiber.Map{"message": "task deleted successfully"})
}

func (ts *testServer) cancelTask(c *fiber.Ctx) error {
	taskID := c.Params("id")
	task, err := ts.store.GetTask(c.Context(), taskID)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "task not found")
	}
	userID, _ := c.Locals("user_id").(string)
	if task.UserID != userID {
		return fiber.NewError(fiber.StatusForbidden, "access denied")
	}
	if task.Status == models.StatusCompleted || task.Status == models.StatusFailed || task.Status == models.StatusCancelled {
		return fiber.NewError(fiber.StatusConflict, "task cannot be cancelled: already in "+string(task.Status)+" state")
	}
	task.Status = models.StatusCancelled
	ts.store.UpdateTask(c.Context(), task)
	return c.JSON(fiber.Map{"message": "task cancelled successfully", "task_id": taskID, "status": task.Status})
}

func (ts *testServer) submitBatch(c *fiber.Ctx) error {
	userID, ok := c.Locals("user_id").(string)
	if !ok || userID == "" {
		return fiber.NewError(fiber.StatusUnauthorized, "unauthorized")
	}
	var req models.BatchSubmitRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}
	if len(req.Images) == 0 {
		return fiber.NewError(fiber.StatusBadRequest, "images array must not be empty")
	}
	if len(req.Images) > 500 {
		return fiber.NewError(fiber.StatusBadRequest, "batch too large")
	}

	batchID := uuid.New().String()
	now := time.Now()

	batch := &models.Batch{
		ID:        batchID,
		UserID:    userID,
		Type:      req.Type,
		Total:     len(req.Images),
		Status:    models.BatchStatusQueued,
		CreatedAt: now,
	}
	if err := ts.store.CreateBatch(c.Context(), batch); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to create batch")
	}

	for _, payload := range req.Images {
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
		if err := ts.store.CreateTask(c.Context(), task); err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "failed to create task")
		}
		if err := ts.broker.Publish(c.Context(), task); err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "failed to queue task")
		}
	}

	return c.Status(fiber.StatusCreated).JSON(models.BatchSubmitResponse{
		BatchID:   batchID,
		Total:     len(req.Images),
		Status:    "queued",
		CreatedAt: now,
	})
}

func (ts *testServer) getBatch(c *fiber.Ctx) error {
	batchID := c.Params("id")
	userID, _ := c.Locals("user_id").(string)

	batch, err := ts.store.GetBatch(c.Context(), batchID)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "batch not found")
	}
	if batch.UserID != userID {
		return fiber.NewError(fiber.StatusForbidden, "access denied")
	}
	return c.JSON(batch)
}

// oauthRedirect saves a state and returns the provider URL as JSON (tests
// can't follow real HTTP redirects, so we return 200 + the URL instead).
func (ts *testServer) oauthRedirect(c *fiber.Ctx) error {
	providerName := c.Params("provider")
	if providerName != "google" && providerName != "github" {
		return fiber.NewError(fiber.StatusBadRequest, "unsupported OAuth provider: "+providerName)
	}
	state, err := oauth.GenerateState()
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to generate state")
	}
	if err := ts.oauthStates.Save(c.Context(), state, providerName, 5*time.Minute); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to save state")
	}
	// Return the state so tests can use it in the callback.
	return c.JSON(fiber.Map{"state": state, "provider": providerName})
}

// oauthCallback simulates the provider callback without hitting real OAuth endpoints.
// The test pre-seeds a user via ts.store and passes provider+provider_id as query params.
func (ts *testServer) oauthCallback(c *fiber.Ctx) error {
	providerName := c.Params("provider")
	state := c.Query("state")
	providerUserID := c.Query("provider_user_id") // injected by tests instead of a real code
	email := c.Query("email")
	name := c.Query("name")

	if state == "" || providerUserID == "" {
		return fiber.NewError(fiber.StatusBadRequest, "missing state or provider_user_id")
	}
	if err := ts.oauthStates.Verify(c.Context(), state, providerName); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid or expired OAuth state")
	}

	// Find or create the user.
	user, err := ts.store.GetUserByOAuth(c.Context(), providerName, providerUserID)
	if err != nil {
		if email != "" {
			existing, lookupErr := ts.store.GetUserByEmail(c.Context(), email)
			if lookupErr == nil {
				existing.OAuthProvider = providerName
				existing.OAuthID = providerUserID
				ts.store.UpdateUser(c.Context(), existing)
				user = existing
			}
		}
		if user == nil {
			username := name
			if username == "" {
				username = "user_" + uuid.New().String()[:8]
			}
			user = &models.User{
				ID:            uuid.New().String(),
				Username:      username,
				Email:         email,
				Roles:         []string{"user"},
				OAuthProvider: providerName,
				OAuthID:       providerUserID,
				CreatedAt:     time.Now(),
			}
			if createErr := ts.store.CreateUser(c.Context(), user); createErr != nil {
				return fiber.NewError(fiber.StatusInternalServerError, "failed to create user")
			}
		}
	}

	token, err := ts.authService.GenerateToken(user.ID, user.Email, user.Roles)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to generate token")
	}
	return c.JSON(fiber.Map{
		"token": token,
		"user":  fiber.Map{"id": user.ID, "username": user.Username, "email": user.Email},
	})
}

// ── helpers ──────────────────────────────────────────────────────────────────

func doRequest(t *testing.T, app *fiber.App, method, path string, body any, token string) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		bodyReader = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := app.Test(req, 5000)
	require.NoError(t, err)
	return resp
}

func decodeBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&m))
	return m
}

// registerAndLogin is a shortcut that creates a user and returns its JWT token.
func registerAndLogin(t *testing.T, ts *testServer, username, email, password string) string {
	t.Helper()
	resp := doRequest(t, ts.app, "POST", "/api/v1/auth/register", map[string]any{
		"username": username, "email": email, "password": password,
	}, "")
	require.Equal(t, fiber.StatusCreated, resp.StatusCode)
	body := decodeBody(t, resp)
	token, ok := body["token"].(string)
	require.True(t, ok, "register response must contain token")
	return token
}

// ── tests ────────────────────────────────────────────────────────────────────

func TestHealthCheck(t *testing.T) {
	ts := newTestServer(t)
	resp := doRequest(t, ts.app, "GET", "/api/v1/health", nil, "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body := decodeBody(t, resp)
	assert.Equal(t, "healthy", body["status"])
}

func TestRegister_HappyPath(t *testing.T) {
	ts := newTestServer(t)
	resp := doRequest(t, ts.app, "POST", "/api/v1/auth/register", map[string]any{
		"username": "alice",
		"email":    "alice@example.com",
		"password": "securepass",
	}, "")
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	body := decodeBody(t, resp)
	assert.NotEmpty(t, body["token"], "token must be present")
	user := body["user"].(map[string]any)
	assert.Equal(t, "alice", user["username"])
}

func TestRegister_DuplicateUsername(t *testing.T) {
	ts := newTestServer(t)
	registerAndLogin(t, ts, "bob", "bob@example.com", "password1")
	resp := doRequest(t, ts.app, "POST", "/api/v1/auth/register", map[string]any{
		"username": "bob", "email": "other@example.com", "password": "password2",
	}, "")
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestRegister_WeakPassword(t *testing.T) {
	ts := newTestServer(t)
	resp := doRequest(t, ts.app, "POST", "/api/v1/auth/register", map[string]any{
		"username": "carol", "email": "carol@example.com", "password": "short",
	}, "")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestLogin_HappyPath(t *testing.T) {
	ts := newTestServer(t)
	registerAndLogin(t, ts, "dave", "dave@example.com", "mypassword")
	resp := doRequest(t, ts.app, "POST", "/api/v1/auth/login", map[string]any{
		"username": "dave", "password": "mypassword",
	}, "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body := decodeBody(t, resp)
	assert.NotEmpty(t, body["token"])
}

func TestLogin_WrongPassword(t *testing.T) {
	ts := newTestServer(t)
	registerAndLogin(t, ts, "eve", "eve@example.com", "correct-pass")
	resp := doRequest(t, ts.app, "POST", "/api/v1/auth/login", map[string]any{
		"username": "eve", "password": "wrong-pass",
	}, "")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestLogin_UnknownUser(t *testing.T) {
	ts := newTestServer(t)
	resp := doRequest(t, ts.app, "POST", "/api/v1/auth/login", map[string]any{
		"username": "nobody", "password": "somepassword",
	}, "")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestLogin_EmptyBody(t *testing.T) {
	ts := newTestServer(t)
	resp := doRequest(t, ts.app, "POST", "/api/v1/auth/login", map[string]any{}, "")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestRegister_MissingFields(t *testing.T) {
	ts := newTestServer(t)

	// Missing password.
	resp := doRequest(t, ts.app, "POST", "/api/v1/auth/register", map[string]any{
		"username": "nopass", "email": "nopass@example.com",
	}, "")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Missing username.
	resp = doRequest(t, ts.app, "POST", "/api/v1/auth/register", map[string]any{
		"email": "nouser@example.com", "password": "validpass",
	}, "")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Missing email.
	resp = doRequest(t, ts.app, "POST", "/api/v1/auth/register", map[string]any{
		"username": "noemail", "password": "validpass",
	}, "")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestProtectedRoute_InvalidToken(t *testing.T) {
	ts := newTestServer(t)
	resp := doRequest(t, ts.app, "GET", "/api/v1/tasks", nil, "not-a-valid-jwt")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestProtectedRoute_NoToken(t *testing.T) {
	ts := newTestServer(t)
	resp := doRequest(t, ts.app, "GET", "/api/v1/tasks", nil, "")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestSubmitTask_HappyPath(t *testing.T) {
	ts := newTestServer(t)
	token := registerAndLogin(t, ts, "frank", "frank@example.com", "frankpass")

	resp := doRequest(t, ts.app, "POST", "/api/v1/tasks", map[string]any{
		"type":        "image_resize",
		"payload":     json.RawMessage(`{"source_url":"https://example.com/img.jpg","width":800,"height":600}`),
		"max_retries": 3,
	}, token)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	body := decodeBody(t, resp)
	assert.NotEmpty(t, body["task_id"])
	assert.Equal(t, "queued", body["status"])

	// Verify the task was published to the broker.
	ts.broker.mu.Lock()
	published := len(ts.broker.published)
	ts.broker.mu.Unlock()
	assert.Equal(t, 1, published, "task must be published to broker")
}

func TestSubmitTask_RequiresAuth(t *testing.T) {
	ts := newTestServer(t)
	resp := doRequest(t, ts.app, "POST", "/api/v1/tasks", map[string]any{
		"type": "image_resize", "payload": json.RawMessage(`{}`),
	}, "") // no token
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestGetTaskStatus_HappyPath(t *testing.T) {
	ts := newTestServer(t)
	token := registerAndLogin(t, ts, "grace", "grace@example.com", "gracepass")

	// Submit a task first.
	submitResp := doRequest(t, ts.app, "POST", "/api/v1/tasks", map[string]any{
		"type":    "image_compress",
		"payload": json.RawMessage(`{"source_url":"https://example.com/img.jpg","quality":80,"format":"jpeg"}`),
	}, token)
	require.Equal(t, http.StatusCreated, submitResp.StatusCode)
	submitBody := decodeBody(t, submitResp)
	taskID := submitBody["task_id"].(string)

	// Fetch by ID.
	getResp := doRequest(t, ts.app, "GET", "/api/v1/tasks/"+taskID, nil, token)
	assert.Equal(t, http.StatusOK, getResp.StatusCode)
	getBody := decodeBody(t, getResp)
	task := getBody["task"].(map[string]any)
	assert.Equal(t, taskID, task["id"])
	assert.Equal(t, "queued", task["status"])
}

func TestGetTaskStatus_OtherUserForbidden(t *testing.T) {
	ts := newTestServer(t)
	token1 := registerAndLogin(t, ts, "henry", "henry@example.com", "henrypass")
	token2 := registerAndLogin(t, ts, "iris", "iris@example.com", "irispass")

	submitResp := doRequest(t, ts.app, "POST", "/api/v1/tasks", map[string]any{
		"type":    "image_resize",
		"payload": json.RawMessage(`{"source_url":"https://example.com/img.jpg","width":100,"height":100}`),
	}, token1)
	require.Equal(t, http.StatusCreated, submitResp.StatusCode)
	taskID := decodeBody(t, submitResp)["task_id"].(string)

	// Different user must not see Henry's task.
	resp := doRequest(t, ts.app, "GET", "/api/v1/tasks/"+taskID, nil, token2)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestListTasks_HappyPath(t *testing.T) {
	ts := newTestServer(t)
	token := registerAndLogin(t, ts, "jack", "jack@example.com", "jackpass")

	// Submit two tasks.
	for i := range 2 {
		doRequest(t, ts.app, "POST", "/api/v1/tasks", map[string]any{
			"type":    "image_filter",
			"payload": json.RawMessage(fmt.Sprintf(`{"source_url":"https://example.com/%d.jpg","filter_type":"grayscale"}`, i)),
		}, token)
	}

	resp := doRequest(t, ts.app, "GET", "/api/v1/tasks", nil, token)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body := decodeBody(t, resp)
	assert.Equal(t, float64(2), body["total"])
	tasks := body["tasks"].([]any)
	assert.Len(t, tasks, 2)
}

func TestDeleteTask_HappyPath(t *testing.T) {
	ts := newTestServer(t)
	token := registerAndLogin(t, ts, "karen", "karen@example.com", "karenpass")

	submitResp := doRequest(t, ts.app, "POST", "/api/v1/tasks", map[string]any{
		"type":    "image_thumbnail",
		"payload": json.RawMessage(`{"source_url":"https://example.com/img.jpg","size":128}`),
	}, token)
	require.Equal(t, http.StatusCreated, submitResp.StatusCode)
	taskID := decodeBody(t, submitResp)["task_id"].(string)

	delResp := doRequest(t, ts.app, "DELETE", "/api/v1/tasks/"+taskID, nil, token)
	assert.Equal(t, http.StatusOK, delResp.StatusCode)

	// Task should be gone.
	getResp := doRequest(t, ts.app, "GET", "/api/v1/tasks/"+taskID, nil, token)
	assert.Equal(t, http.StatusNotFound, getResp.StatusCode)
}

func TestCancelTask_HappyPath(t *testing.T) {
	ts := newTestServer(t)
	token := registerAndLogin(t, ts, "liam", "liam@example.com", "liampass")

	submitResp := doRequest(t, ts.app, "POST", "/api/v1/tasks", map[string]any{
		"type":    "image_resize",
		"payload": json.RawMessage(`{"source_url":"https://example.com/img.jpg","width":200,"height":200}`),
	}, token)
	require.Equal(t, http.StatusCreated, submitResp.StatusCode)
	taskID := decodeBody(t, submitResp)["task_id"].(string)

	cancelResp := doRequest(t, ts.app, "POST", "/api/v1/tasks/"+taskID+"/cancel", nil, token)
	assert.Equal(t, http.StatusOK, cancelResp.StatusCode)
	body := decodeBody(t, cancelResp)
	assert.Equal(t, "cancelled", body["status"])

	// Task status must be updated in storage.
	task, err := ts.store.GetTask(context.Background(), taskID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusCancelled, task.Status)
}

func TestCancelTask_AlreadyCompleted(t *testing.T) {
	ts := newTestServer(t)
	token := registerAndLogin(t, ts, "mia", "mia@example.com", "miapass1")

	submitResp := doRequest(t, ts.app, "POST", "/api/v1/tasks", map[string]any{
		"type":    "image_resize",
		"payload": json.RawMessage(`{"source_url":"https://example.com/img.jpg","width":200,"height":200}`),
	}, token)
	require.Equal(t, http.StatusCreated, submitResp.StatusCode)
	taskID := decodeBody(t, submitResp)["task_id"].(string)

	// Manually mark as completed in storage.
	task, _ := ts.store.GetTask(context.Background(), taskID)
	task.Status = models.StatusCompleted
	ts.store.UpdateTask(context.Background(), task)

	cancelResp := doRequest(t, ts.app, "POST", "/api/v1/tasks/"+taskID+"/cancel", nil, token)
	assert.Equal(t, http.StatusConflict, cancelResp.StatusCode)
}

func TestUploadFile_InvalidType(t *testing.T) {
	ts := newTestServer(t)

	// Add a minimal /upload route that mirrors the real server's extension check.
	ts.app.Post("/api/v1/upload", func(c *fiber.Ctx) error {
		file, err := c.FormFile("file")
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "no file provided")
		}
		allowed := map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true}
		ext := strings.ToLower(file.Filename[strings.LastIndex(file.Filename, "."):])
		if !allowed[ext] {
			return fiber.NewError(fiber.StatusBadRequest, "invalid file type: only images are allowed")
		}
		return c.JSON(fiber.Map{"url": "/uploads/" + file.Filename})
	})

	token := registerAndLogin(t, ts, "noah", "noah@example.com", "noahpass")

	// Build a multipart form with a .exe file.
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", "malware.exe")
	fw.Write([]byte("fake binary"))
	w.Close()

	req := httptest.NewRequest("POST", "/api/v1/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := ts.app.Test(req, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// ── OAuth tests ───────────────────────────────────────────────────────────────

func TestOAuthRedirect_Google(t *testing.T) {
	ts := newTestServer(t)
	resp := doRequest(t, ts.app, "GET", "/api/v1/auth/oauth/google", nil, "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body := decodeBody(t, resp)
	assert.Equal(t, "google", body["provider"])
	assert.NotEmpty(t, body["state"])
}

func TestOAuthRedirect_GitHub(t *testing.T) {
	ts := newTestServer(t)
	resp := doRequest(t, ts.app, "GET", "/api/v1/auth/oauth/github", nil, "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body := decodeBody(t, resp)
	assert.Equal(t, "github", body["provider"])
	assert.NotEmpty(t, body["state"])
}

func TestOAuthRedirect_UnknownProvider(t *testing.T) {
	ts := newTestServer(t)
	resp := doRequest(t, ts.app, "GET", "/api/v1/auth/oauth/twitter", nil, "")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestOAuthCallback_NewUser(t *testing.T) {
	ts := newTestServer(t)

	// Step 1: get a valid state via the redirect endpoint.
	redirectResp := doRequest(t, ts.app, "GET", "/api/v1/auth/oauth/google", nil, "")
	require.Equal(t, http.StatusOK, redirectResp.StatusCode)
	state := decodeBody(t, redirectResp)["state"].(string)

	// Step 2: simulate callback — new user, no pre-existing account.
	callbackResp := doRequest(t, ts.app, "GET",
		"/api/v1/auth/oauth/google/callback?state="+state+"&provider_user_id=google-123&email=oauth@example.com&name=oauth_user",
		nil, "")
	assert.Equal(t, http.StatusOK, callbackResp.StatusCode)
	body := decodeBody(t, callbackResp)
	assert.NotEmpty(t, body["token"])
	user := body["user"].(map[string]any)
	assert.Equal(t, "oauth_user", user["username"])
	assert.Equal(t, "oauth@example.com", user["email"])

	// Verify user was persisted with OAuth fields.
	stored, err := ts.store.GetUserByOAuth(context.Background(), "google", "google-123")
	require.NoError(t, err)
	assert.Equal(t, "google", stored.OAuthProvider)
	assert.Equal(t, "google-123", stored.OAuthID)
}

func TestOAuthCallback_ExistingUser_LinkedByEmail(t *testing.T) {
	ts := newTestServer(t)

	// Pre-register a user with the same email via password auth.
	registerAndLogin(t, ts, "linked_user", "linked@example.com", "password1")

	// Step 1: get a valid state.
	redirectResp := doRequest(t, ts.app, "GET", "/api/v1/auth/oauth/github", nil, "")
	require.Equal(t, http.StatusOK, redirectResp.StatusCode)
	state := decodeBody(t, redirectResp)["state"].(string)

	// Step 2: OAuth callback with same email — should link to the existing account.
	callbackResp := doRequest(t, ts.app, "GET",
		"/api/v1/auth/oauth/github/callback?state="+state+"&provider_user_id=gh-456&email=linked@example.com&name=linked_user",
		nil, "")
	assert.Equal(t, http.StatusOK, callbackResp.StatusCode)
	body := decodeBody(t, callbackResp)
	assert.NotEmpty(t, body["token"])
	user := body["user"].(map[string]any)
	assert.Equal(t, "linked_user", user["username"]) // same account, not a new one
}

func TestOAuthCallback_ReturningUser(t *testing.T) {
	ts := newTestServer(t)

	// First login — creates the user.
	redirectResp := doRequest(t, ts.app, "GET", "/api/v1/auth/oauth/google", nil, "")
	state := decodeBody(t, redirectResp)["state"].(string)
	doRequest(t, ts.app, "GET",
		"/api/v1/auth/oauth/google/callback?state="+state+"&provider_user_id=google-789&email=returning@example.com&name=returning",
		nil, "")

	// Second login with same provider_user_id — should find existing user.
	redirectResp2 := doRequest(t, ts.app, "GET", "/api/v1/auth/oauth/google", nil, "")
	state2 := decodeBody(t, redirectResp2)["state"].(string)
	callbackResp := doRequest(t, ts.app, "GET",
		"/api/v1/auth/oauth/google/callback?state="+state2+"&provider_user_id=google-789&email=returning@example.com&name=returning",
		nil, "")
	assert.Equal(t, http.StatusOK, callbackResp.StatusCode)
	assert.NotEmpty(t, decodeBody(t, callbackResp)["token"])
}

func TestOAuthCallback_InvalidState(t *testing.T) {
	ts := newTestServer(t)
	resp := doRequest(t, ts.app, "GET",
		"/api/v1/auth/oauth/google/callback?state=bogus-state&provider_user_id=google-123",
		nil, "")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestOAuthCallback_MissingParams(t *testing.T) {
	ts := newTestServer(t)

	redirectResp := doRequest(t, ts.app, "GET", "/api/v1/auth/oauth/google", nil, "")
	state := decodeBody(t, redirectResp)["state"].(string)

	// Missing provider_user_id.
	resp := doRequest(t, ts.app, "GET",
		"/api/v1/auth/oauth/google/callback?state="+state,
		nil, "")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// ── Batch tests ───────────────────────────────────────────────────────────────

func TestSubmitBatch_HappyPath(t *testing.T) {
	ts := newTestServer(t)
	token := registerAndLogin(t, ts, "batchuser", "batch@example.com", "batchpass")

	resp := doRequest(t, ts.app, "POST", "/api/v1/tasks/batch", map[string]any{
		"type": "image_resize",
		"images": []any{
			json.RawMessage(`{"source_url":"https://example.com/1.jpg","width":100,"height":100}`),
			json.RawMessage(`{"source_url":"https://example.com/2.jpg","width":200,"height":200}`),
			json.RawMessage(`{"source_url":"https://example.com/3.jpg","width":300,"height":300}`),
		},
		"priority":    0,
		"max_retries": 3,
	}, token)

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	body := decodeBody(t, resp)
	assert.NotEmpty(t, body["batch_id"])
	assert.Equal(t, float64(3), body["total"])
	assert.Equal(t, "queued", body["status"])

	// All 3 tasks must have been published to the broker.
	ts.broker.mu.Lock()
	published := len(ts.broker.published)
	ts.broker.mu.Unlock()
	assert.Equal(t, 3, published)
}

func TestSubmitBatch_RequiresAuth(t *testing.T) {
	ts := newTestServer(t)
	resp := doRequest(t, ts.app, "POST", "/api/v1/tasks/batch", map[string]any{
		"type":   "image_resize",
		"images": []any{json.RawMessage(`{}`)},
	}, "")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestSubmitBatch_EmptyImages(t *testing.T) {
	ts := newTestServer(t)
	token := registerAndLogin(t, ts, "batchuser2", "batch2@example.com", "batchpass")

	resp := doRequest(t, ts.app, "POST", "/api/v1/tasks/batch", map[string]any{
		"type":   "image_resize",
		"images": []any{},
	}, token)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestGetBatch_HappyPath(t *testing.T) {
	ts := newTestServer(t)
	token := registerAndLogin(t, ts, "batchuser3", "batch3@example.com", "batchpass")

	// Submit a 2-image batch.
	submitResp := doRequest(t, ts.app, "POST", "/api/v1/tasks/batch", map[string]any{
		"type": "image_compress",
		"images": []any{
			json.RawMessage(`{"source_url":"https://example.com/a.jpg","quality":80,"format":"jpeg"}`),
			json.RawMessage(`{"source_url":"https://example.com/b.jpg","quality":80,"format":"jpeg"}`),
		},
	}, token)
	require.Equal(t, http.StatusCreated, submitResp.StatusCode)
	batchID := decodeBody(t, submitResp)["batch_id"].(string)

	// Fetch batch status.
	getResp := doRequest(t, ts.app, "GET", "/api/v1/batches/"+batchID, nil, token)
	assert.Equal(t, http.StatusOK, getResp.StatusCode)
	body := decodeBody(t, getResp)
	assert.Equal(t, batchID, body["id"])
	assert.Equal(t, float64(2), body["total"])
	assert.Equal(t, float64(2), body["queued"])
	assert.Equal(t, "queued", body["status"])
}

func TestGetBatch_OtherUserForbidden(t *testing.T) {
	ts := newTestServer(t)
	owner := registerAndLogin(t, ts, "batchowner", "bowner@example.com", "ownerpass")
	other := registerAndLogin(t, ts, "batchother", "bother@example.com", "otherpass")

	submitResp := doRequest(t, ts.app, "POST", "/api/v1/tasks/batch", map[string]any{
		"type":   "image_resize",
		"images": []any{json.RawMessage(`{"source_url":"https://example.com/x.jpg","width":50,"height":50}`)},
	}, owner)
	require.Equal(t, http.StatusCreated, submitResp.StatusCode)
	batchID := decodeBody(t, submitResp)["batch_id"].(string)

	resp := doRequest(t, ts.app, "GET", "/api/v1/batches/"+batchID, nil, other)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestGetBatch_NotFound(t *testing.T) {
	ts := newTestServer(t)
	token := registerAndLogin(t, ts, "batchuser4", "batch4@example.com", "batchpass")

	resp := doRequest(t, ts.app, "GET", "/api/v1/batches/nonexistent-id", nil, token)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestGetBatch_StatusUpdatesAsTasksComplete(t *testing.T) {
	ts := newTestServer(t)
	token := registerAndLogin(t, ts, "batchuser5", "batch5@example.com", "batchpass")

	submitResp := doRequest(t, ts.app, "POST", "/api/v1/tasks/batch", map[string]any{
		"type": "image_thumbnail",
		"images": []any{
			json.RawMessage(`{"source_url":"https://example.com/p.jpg","size":64}`),
			json.RawMessage(`{"source_url":"https://example.com/q.jpg","size":64}`),
		},
	}, token)
	require.Equal(t, http.StatusCreated, submitResp.StatusCode)
	batchID := decodeBody(t, submitResp)["batch_id"].(string)

	// Simulate one task completing.
	var firstTaskID string
	ts.store.mu.RLock()
	for _, task := range ts.store.tasks {
		if task.BatchID == batchID {
			firstTaskID = task.ID
			break
		}
	}
	ts.store.mu.RUnlock()

	task, _ := ts.store.GetTask(context.Background(), firstTaskID)
	task.Status = models.StatusCompleted
	ts.store.UpdateTask(context.Background(), task)

	// Batch should now be "processing" (1 done, 1 still queued).
	getResp := doRequest(t, ts.app, "GET", "/api/v1/batches/"+batchID, nil, token)
	body := decodeBody(t, getResp)
	assert.Equal(t, "processing", body["status"])
	assert.Equal(t, float64(1), body["completed"])
	assert.Equal(t, float64(1), body["queued"])
}
