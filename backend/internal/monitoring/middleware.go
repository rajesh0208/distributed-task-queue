// File: internal/monitoring/middleware.go
package monitoring

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
)

// PrometheusMiddleware records API metrics
func PrometheusMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()

		err := c.Next()

		duration := time.Since(start).Seconds()
		status := strconv.Itoa(c.Response().StatusCode())
		method := c.Method()
		endpoint := c.Path()

		APIRequestDuration.WithLabelValues(method, endpoint, status).Observe(duration)
		APIRequestsTotal.WithLabelValues(method, endpoint, status).Inc()

		return err
	}
}
