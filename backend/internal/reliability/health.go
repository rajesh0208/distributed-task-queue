// Package reliability — see circuitbreaker.go for package-level documentation.
//
// This file (health.go) implements a health-check registry and Fiber handler
// that reports the live status of every infrastructure dependency.
//
// Why health checks?
//   Kubernetes, Docker Swarm, and load balancers continuously probe running
//   containers to decide whether to route traffic to them:
//
//   Liveness probe  — "is this process alive?"
//     If liveness fails, Kubernetes restarts the container. A crash-looping pod
//     that passes liveness would never be restarted. Typically just checks that
//     the process is not deadlocked.
//
//   Readiness probe — "is this process ready to accept traffic?"
//     If readiness fails, Kubernetes removes the pod from the load-balancer pool
//     until it recovers. This file's /health endpoint serves as a readiness check:
//     if PostgreSQL or Redis are unreachable, the API is not ready to serve requests.
//
// Why check dependencies separately from the application itself?
//   An app can be "alive" (process is running) but "not ready" (database is down).
//   Splitting checks lets operators see WHICH dependency caused the degradation
//   instead of a generic "unhealthy" response.
//
// Who reads /health?
//   - Kubernetes readiness/liveness probes (kubelet calls it on each pod)
//   - Docker health checks (configured in docker-compose.yml)
//   - Load balancers (nginx upstream health checks)
//   - Monitoring dashboards (Grafana panels showing per-dependency health)
//
// Why checks run on request, not on a timer:
//   Running on-demand means the response always reflects the CURRENT state.
//   Timer-based checks can serve stale "healthy" results during a brief outage
//   window between check intervals. The 5-second timeout prevents slow checks
//   from holding up the health endpoint itself.
//
// It connects to:
//   - database/sql.DB — PostgreSQL ping
//   - github.com/go-redis/redis/v8 — Redis ping
//   - github.com/gofiber/fiber/v2 — HTTP handler
//
// Called by:
//   - cmd/api/main.go — registers checks and attaches HealthCheckHandler to /health
package reliability

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/gofiber/fiber/v2"
)

// ─────────────────────────────────────────────────────────────────────────────
// Status types
// ─────────────────────────────────────────────────────────────────────────────

// HealthStatus is a typed string for the three possible health outcomes.
// Using typed strings prevents accidental misuse (e.g. checking == "Healthy").
type HealthStatus string

const (
	// HealthStatusHealthy means the dependency responded normally.
	// All dependencies healthy → overall status healthy → HTTP 200.
	HealthStatusHealthy HealthStatus = "healthy"

	// HealthStatusDegraded means the dependency is reachable but performing
	// poorly (high latency, partial functionality). Currently unused by the
	// built-in checks but available for custom checks that can distinguish
	// "slow but working" from "completely down".
	// At least one degraded check → overall status degraded → HTTP 200.
	HealthStatusDegraded HealthStatus = "degraded"

	// HealthStatusUnhealthy means the dependency is unreachable or threw an error.
	// Any unhealthy check → overall status unhealthy → HTTP 503.
	// HTTP 503 causes Kubernetes to remove the pod from the load-balancer pool.
	HealthStatusUnhealthy HealthStatus = "unhealthy"
)

// ─────────────────────────────────────────────────────────────────────────────
// Result types
// ─────────────────────────────────────────────────────────────────────────────

// HealthCheck holds the result of a single dependency check.
// One HealthCheck per registered dependency is returned in the /health response.
//
// Fields explained:
//
//	Name      — human-readable identifier (e.g. "postgresql", "redis").
//	            Shown in dashboards and alert messages.
//	Status    — HealthStatusHealthy / HealthStatusDegraded / HealthStatusUnhealthy.
//	Message   — error detail when status is not healthy; empty otherwise.
//	            Operators read this to understand WHY a check failed.
//	Timestamp — when the check was started; helps correlate with other log events.
//	Duration  — how long the check took in milliseconds. High duration on a
//	            "healthy" check signals a slow dependency worth investigating.
type HealthCheck struct {
	Name      string       `json:"name"`
	Status    HealthStatus `json:"status"`
	Message   string       `json:"message,omitempty"`
	Timestamp time.Time    `json:"timestamp"`
	Duration  int64        `json:"duration_ms"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Registry
// ─────────────────────────────────────────────────────────────────────────────

// HealthChecker is a registry of named health-check functions.
// Register checks at startup; the /health handler calls CheckAll on each request.
//
// Fields explained:
//
//	checks — map from dependency name to its check function.
//	         Using a map allows checks to be added/removed without changing
//	         the CheckAll logic.
//	mutex  — RWMutex protects the checks map:
//	         ReadLock for CheckAll (multiple concurrent callers allowed),
//	         WriteLock for RegisterCheck (exclusive write to the map).
type HealthChecker struct {
	checks map[string]func(context.Context) HealthCheck
	mutex  sync.RWMutex
}

// NewHealthChecker creates an empty HealthChecker with no registered checks.
// Call RegisterCheck to add dependencies before starting the HTTP server.
//
// Called by: cmd/api/main.go.
func NewHealthChecker() *HealthChecker {
	return &HealthChecker{
		checks: make(map[string]func(context.Context) HealthCheck),
	}
}

// RegisterCheck adds a named health check function to the registry.
// The function receives a context (with the caller's timeout) and returns
// a HealthCheck result.
//
// Parameters:
//   name  — display name for this dependency (e.g. "postgresql", "redis").
//   check — function that probes the dependency and returns a HealthCheck.
//
// Called by: cmd/api/main.go — registers PostgresHealthCheck and RedisHealthCheck.
func (h *HealthChecker) RegisterCheck(name string, check func(context.Context) HealthCheck) {
	h.mutex.Lock()   // exclusive write — prevents concurrent map mutation
	defer h.mutex.Unlock()
	h.checks[name] = check
}

// CheckAll runs all registered checks concurrently and returns results keyed
// by dependency name.
//
// Why run checks in parallel?
//   A PostgreSQL probe may block for up to 5 seconds if the server is slow.
//   Running checks sequentially would make the health endpoint take N×5 seconds
//   for N dependencies. Parallel execution means the total latency equals the
//   slowest single check, not the sum.
//
// Parameters:
//   ctx — carries the timeout from HealthCheckHandler (5 seconds).
//         If the context expires before all checks finish, in-flight checks
//         get the cancellation signal via their own ctx parameter.
//
// Returns:
//   map[string]HealthCheck — each registered check's result.
//
// Called by: HealthCheckHandler.
func (h *HealthChecker) CheckAll(ctx context.Context) map[string]HealthCheck {
	h.mutex.RLock()   // shared read — multiple callers can run CheckAll simultaneously
	defer h.mutex.RUnlock()

	results := make(map[string]HealthCheck)
	var wg sync.WaitGroup
	var mu sync.Mutex // protects concurrent writes to the results map

	for name, check := range h.checks {
		wg.Add(1)
		// Launch each check in its own goroutine so they run in parallel.
		// Capture loop variables by passing them as function arguments to
		// avoid the classic Go goroutine closure variable capture bug.
		go func(n string, c func(context.Context) HealthCheck) {
			defer wg.Done()
			result := c(ctx) // execute the check; ctx carries the deadline
			mu.Lock()
			results[n] = result // write to the shared map under lock
			mu.Unlock()
		}(name, check)
	}

	wg.Wait() // block until all goroutines complete or ctx expires
	return results
}

// ─────────────────────────────────────────────────────────────────────────────
// Built-in check constructors
// ─────────────────────────────────────────────────────────────────────────────

// PostgresHealthCheck returns a check function that pings the PostgreSQL database.
// Records the round-trip duration so operators can monitor DB latency trends.
//
// Parameters:
//   db — *sql.DB from the application's connection pool.
//
// Returns:
//   func(context.Context) HealthCheck — register with HealthChecker.RegisterCheck.
//
// Called by: cmd/api/main.go.
func PostgresHealthCheck(db *sql.DB) func(context.Context) HealthCheck {
	return func(ctx context.Context) HealthCheck {
		start := time.Now()
		check := HealthCheck{
			Name:      "postgresql",
			Timestamp: start,
		}

		// PingContext sends a no-op query to verify the DB is reachable and the
		// connection pool can acquire a slot. Fails if the DB is down, the pool
		// is exhausted, or the context times out.
		err := db.PingContext(ctx)
		check.Duration = time.Since(start).Milliseconds() // measure before branch

		if err != nil {
			// Database is unreachable — report as unhealthy.
			// This causes HTTP 503 and removes the pod from the load-balancer pool.
			check.Status = HealthStatusUnhealthy
			check.Message = fmt.Sprintf("failed to ping database: %v", err)
		} else {
			check.Status = HealthStatusHealthy
			// No message on success — keeps the JSON response clean.
		}

		return check
	}
}

// RedisHealthCheck returns a check function that pings the Redis server.
// A Redis outage means task caching, rate limiting, and the message broker
// are all affected — this check detects that early.
//
// Parameters:
//   client — *redis.Client from the application's connection pool.
//
// Returns:
//   func(context.Context) HealthCheck — register with HealthChecker.RegisterCheck.
//
// Called by: cmd/api/main.go.
func RedisHealthCheck(client *redis.Client) func(context.Context) HealthCheck {
	return func(ctx context.Context) HealthCheck {
		start := time.Now()
		check := HealthCheck{
			Name:      "redis",
			Timestamp: start,
		}

		// PING sends a one-byte command to Redis; Redis replies "PONG".
		// Failure means Redis is down, the connection pool is exhausted,
		// or the context timed out.
		err := client.Ping(ctx).Err()
		check.Duration = time.Since(start).Milliseconds()

		if err != nil {
			check.Status = HealthStatusUnhealthy
			check.Message = fmt.Sprintf("failed to ping Redis: %v", err)
		} else {
			check.Status = HealthStatusHealthy
		}

		return check
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP handler
// ─────────────────────────────────────────────────────────────────────────────

// HealthCheckHandler returns a Fiber handler for the GET /health endpoint.
//
// Response body:
//
//	{
//	  "status": "healthy" | "degraded" | "unhealthy",
//	  "checks": {
//	    "postgresql": { "name": "postgresql", "status": "healthy", "duration_ms": 2 },
//	    "redis":      { "name": "redis",      "status": "healthy", "duration_ms": 1 }
//	  }
//	}
//
// HTTP status codes:
//   200 OK                — all checks healthy or at most degraded.
//   503 Service Unavailable — at least one check is unhealthy.
//     → Kubernetes readiness probe interprets 503 as "not ready" and stops routing traffic.
//
// The 5-second timeout prevents slow dependencies from blocking the health
// endpoint indefinitely, which would cause the probe to fail by timeout anyway.
//
// Parameters:
//   checker — the populated HealthChecker with all dependencies registered.
//
// Returns:
//   fiber.Handler — attach to app.Get("/health", HealthCheckHandler(checker)).
//
// Called by: cmd/api/main.go.
func HealthCheckHandler(checker *HealthChecker) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Give all checks a 5-second window before declaring a timeout.
		// This matches typical Kubernetes probe timeoutSeconds configuration.
		ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second)
		defer cancel() // always release the timer goroutine

		// Run all registered checks concurrently.
		results := checker.CheckAll(ctx)

		// Compute overall status: unhealthy wins over degraded wins over healthy.
		overallStatus := HealthStatusHealthy
		for _, result := range results {
			if result.Status == HealthStatusUnhealthy {
				overallStatus = HealthStatusUnhealthy
				break // one unhealthy dependency is enough to fail the whole check
			} else if result.Status == HealthStatusDegraded {
				overallStatus = HealthStatusDegraded
				// Continue checking — an unhealthy check may still be found.
			}
		}

		response := fiber.Map{
			"status": overallStatus,
			"checks": results,
		}

		// Return 503 if unhealthy so Kubernetes stops routing traffic here.
		// Return 200 for healthy or degraded — the pod is still useful.
		statusCode := fiber.StatusOK
		if overallStatus == HealthStatusUnhealthy {
			statusCode = fiber.StatusServiceUnavailable
		}

		return c.Status(statusCode).JSON(response)
	}
}
