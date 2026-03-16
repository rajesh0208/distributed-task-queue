// Package monitoring — see metrics.go for package-level documentation.
//
// This file implements the Fiber HTTP middleware that records per-request
// Prometheus metrics (latency and request count) for every API endpoint.
//
// Why measure latency at middleware level?
// Handlers are responsible for business logic, not observability. Placing the
// timer in middleware means every route is instrumented automatically — a new
// handler added to the router is measured without any extra code. It also captures
// the total end-to-end handler time including any other middleware that runs after
// this one.
//
// Connected to:
//   - internal/monitoring/metrics.go — writes to APIRequestDuration and APIRequestsTotal
//   - cmd/api/main.go               — attaches this middleware to all routes
//
// Called by:
//   - cmd/api/main.go — app.Use(monitoring.PrometheusMiddleware())
package monitoring

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
)

// PrometheusMiddleware returns a Fiber middleware that records HTTP request
// latency and counts for every route.
//
// How middleware chaining works in Fiber:
//   When a request arrives, Fiber calls each middleware in the order they were
//   registered. This middleware starts a timer, then calls c.Next() to run the
//   rest of the chain (including the actual route handler). When c.Next() returns,
//   the middleware resumes and records the elapsed time. This "sandwich" pattern
//   measures the total time spent inside everything downstream.
//
// Returns:
//
//	fiber.Handler — a middleware function; register with app.Use() or group.Use()
//
// Called by: cmd/api/main.go before route handlers are registered.
func PrometheusMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Record the time before the request is processed.
		// time.Now() uses a monotonic clock that is unaffected by system clock changes.
		start := time.Now()

		// c.Next() runs all remaining middleware and the final route handler.
		// We capture the error so we can return it after recording metrics —
		// not swallowing it is important for error propagation.
		err := c.Next()

		// Compute elapsed time in seconds (Prometheus convention: use seconds for latency).
		duration := time.Since(start).Seconds()

		// Read the HTTP status code *after* the handler runs so we record the actual
		// response status, including error responses set by handlers.
		status := strconv.Itoa(c.Response().StatusCode())

		// c.Method() returns the HTTP verb (GET, POST, etc.).
		method := c.Method()

		// c.Path() returns the URL path without query parameters.
		// NOTE: for routes with dynamic segments (e.g. /tasks/:id) this returns
		// the literal path like /tasks/abc-123, which creates high-cardinality labels.
		// In high-traffic systems you would normalise this to /tasks/:id here.
		endpoint := c.Path()

		// Record the latency in the histogram for percentile calculations.
		// WithLabelValues returns the specific counter/histogram for this label combination.
		APIRequestDuration.WithLabelValues(method, endpoint, status).Observe(duration)

		// Increment the request count for this method/endpoint/status combination.
		APIRequestsTotal.WithLabelValues(method, endpoint, status).Inc()

		// Return the handler's error so Fiber can render the appropriate error response.
		return err
	}
}
