// File: internal/reliability/health.go
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

type HealthStatus string

const (
	HealthStatusHealthy   HealthStatus = "healthy"
	HealthStatusDegraded  HealthStatus = "degraded"
	HealthStatusUnhealthy HealthStatus = "unhealthy"
)

type HealthCheck struct {
	Name      string       `json:"name"`
	Status    HealthStatus `json:"status"`
	Message   string       `json:"message,omitempty"`
	Timestamp time.Time    `json:"timestamp"`
	Duration  int64        `json:"duration_ms"`
}

type HealthChecker struct {
	checks map[string]func(context.Context) HealthCheck
	mutex  sync.RWMutex
}

func NewHealthChecker() *HealthChecker {
	return &HealthChecker{
		checks: make(map[string]func(context.Context) HealthCheck),
	}
}

func (h *HealthChecker) RegisterCheck(name string, check func(context.Context) HealthCheck) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.checks[name] = check
}

func (h *HealthChecker) CheckAll(ctx context.Context) map[string]HealthCheck {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	results := make(map[string]HealthCheck)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for name, check := range h.checks {
		wg.Add(1)
		go func(n string, c func(context.Context) HealthCheck) {
			defer wg.Done()
			result := c(ctx)
			mu.Lock()
			results[n] = result
			mu.Unlock()
		}(name, check)
	}

	wg.Wait()
	return results
}

func PostgresHealthCheck(db *sql.DB) func(context.Context) HealthCheck {
	return func(ctx context.Context) HealthCheck {
		start := time.Now()
		check := HealthCheck{
			Name:      "postgresql",
			Timestamp: start,
		}

		err := db.PingContext(ctx)
		check.Duration = time.Since(start).Milliseconds()

		if err != nil {
			check.Status = HealthStatusUnhealthy
			check.Message = fmt.Sprintf("failed to ping database: %v", err)
		} else {
			check.Status = HealthStatusHealthy
		}

		return check
	}
}

func RedisHealthCheck(client *redis.Client) func(context.Context) HealthCheck {
	return func(ctx context.Context) HealthCheck {
		start := time.Now()
		check := HealthCheck{
			Name:      "redis",
			Timestamp: start,
		}

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

func HealthCheckHandler(checker *HealthChecker) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second)
		defer cancel()

		results := checker.CheckAll(ctx)

		overallStatus := HealthStatusHealthy
		for _, result := range results {
			if result.Status == HealthStatusUnhealthy {
				overallStatus = HealthStatusUnhealthy
				break
			} else if result.Status == HealthStatusDegraded {
				overallStatus = HealthStatusDegraded
			}
		}

		response := fiber.Map{
			"status": overallStatus,
			"checks": results,
		}

		statusCode := fiber.StatusOK
		if overallStatus == HealthStatusUnhealthy {
			statusCode = fiber.StatusServiceUnavailable
		}

		return c.Status(statusCode).JSON(response)
	}
}