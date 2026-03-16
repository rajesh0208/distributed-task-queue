// Package reliability — see circuitbreaker.go for package-level documentation.
//
// This file (retry.go) implements exponential backoff with optional jitter for
// retrying transient failures.
//
// What is exponential backoff?
//   Instead of retrying immediately (which hammers a struggling service) or at
//   fixed intervals (which creates wave patterns), exponential backoff doubles
//   the wait time after each failure:
//     attempt 1: wait 1 s
//     attempt 2: wait 2 s
//     attempt 3: wait 4 s  (capped at MaxDelay)
//   This gives the dependency time to recover and reduces retry storms.
//
// What is jitter and why does it prevent thundering herd?
//   If 100 workers all fail and start retrying at the same exponential schedule,
//   they all wake up simultaneously and hit the downstream at the same moment —
//   this is the "thundering herd" problem. Adding ±25% random noise to each
//   delay spreads the retries across a window, reducing peak load on the
//   recovering service.
//
// It connects to: (standalone — no internal imports)
// Called by:
//   - cmd/worker/main.go — wraps DB writes and broker publishes
//   - internal/processor — wraps image download HTTP calls
package reliability

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"
)

// RetryConfig holds all tunable parameters for retry behaviour.
// Pass to Retry() or RetryWithBackoff().
//
// Fields explained:
//
//	MaxAttempts  — total number of attempts (including the first one).
//	               Why 3 as the default? One attempt plus two retries gives a
//	               reasonable balance between persistence and giving up quickly.
//	               Too many retries waste goroutines on a service that's truly down.
//	InitialDelay — wait time before the second attempt (attempt index 1).
//	               Subsequent delays are multiplied by Multiplier each time.
//	               1 s is a safe starting point for most I/O operations.
//	MaxDelay     — upper cap on any single wait. Without this, at Multiplier=2.0
//	               the delay doubles every attempt and could reach minutes.
//	               30 s means a 3-attempt sequence takes at most ~7 s total.
//	Multiplier   — how aggressively to grow the delay.
//	               2.0 = doubles each attempt (classic exponential backoff).
//	               1.5 = gentler growth; appropriate for fast-recovering services.
//	Jitter       — when true, adds ±25% random noise to each delay to prevent
//	               multiple workers from retrying in lockstep (thundering herd).
type RetryConfig struct {
	MaxAttempts  int
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Multiplier   float64
	Jitter       bool
}

// DefaultRetryConfig returns a production-safe retry configuration.
//
//	MaxAttempts  3    — two retries after the initial attempt
//	InitialDelay 1 s  — wait 1 s before the first retry
//	MaxDelay     30 s — no single wait longer than 30 s
//	Multiplier   2.0  — double the delay each retry: 1s → 2s → 4s → (capped)
//	Jitter       true — add randomness to prevent synchronised retry storms
//
// Called by: cmd/worker/main.go, processor/image_processor.go.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 1 * time.Second,
		MaxDelay:     30 * time.Second,
		Multiplier:   2.0,
		Jitter:       true,
	}
}

// Retry executes fn up to config.MaxAttempts times, sleeping between attempts
// using the exponential backoff schedule defined in config.
//
// When to retry vs when to fail permanently:
//   Retry transient errors: network timeouts, 429 Too Many Requests, 503 Service
//   Unavailable, temporary database connection errors.
//   Do NOT retry permanent errors: invalid input, 400 Bad Request, 404 Not Found,
//   permission denied. Retrying permanent errors wastes time and resources.
//   Use IsRetryableError() to classify errors before calling Retry.
//
// Parameters:
//   ctx    — cancellation signal. If the context is cancelled mid-wait (e.g.
//             graceful shutdown), Retry returns ctx.Err() immediately rather
//             than completing the full sleep — this keeps shutdown fast.
//   config — retry policy; use DefaultRetryConfig() for sensible defaults.
//   fn     — the fallible function to execute. fn must be idempotent (safe to
//            call more than once without side effects) for retries to be correct.
//
// Returns:
//   nil   — fn succeeded within MaxAttempts.
//   error — fn's error after exhausting all attempts, wrapped with attempt count.
//           OR ctx.Err() if the context was cancelled.
//
// Called by: cmd/worker/main.go, processor/image_processor.go.
func Retry(ctx context.Context, config RetryConfig, fn func() error) error {
	var lastErr error

	for attempt := 0; attempt < config.MaxAttempts; attempt++ {
		if attempt > 0 {
			// Not the first attempt — wait before retrying.
			delay := calculateDelay(config, attempt)
			select {
			case <-ctx.Done():
				// Context cancelled (graceful shutdown, request timeout).
				// Return immediately rather than sleeping — fast shutdown matters.
				return ctx.Err()
			case <-time.After(delay):
				// Sleep completed — proceed with the next attempt.
			}
		}

		err := fn()
		if err == nil {
			return nil // success — no need to retry
		}

		// Record the error so we can wrap it in the final return.
		lastErr = err
		// Continue to next attempt.
	}

	// All attempts exhausted — surface the last error with context.
	return fmt.Errorf("max attempts (%d) exceeded: %w", config.MaxAttempts, lastErr)
}

// RetryWithBackoff is an alias for Retry provided for readability at call sites
// where the caller wants to emphasise that backoff is involved.
//
// Called by: any code preferring explicit naming over Retry.
func RetryWithBackoff(ctx context.Context, config RetryConfig, fn func() error) error {
	return Retry(ctx, config, fn)
}

// calculateDelay computes the wait duration for a given retry attempt using
// exponential growth with an optional jitter band.
//
// Formula (without jitter):
//   delay = InitialDelay × Multiplier^(attempt-1)
//   e.g. attempt=1: 1s×2^0 = 1s, attempt=2: 1s×2^1 = 2s, attempt=3: 1s×2^2 = 4s
//
// With jitter (±25%):
//   jitterAmount = delay × 0.25
//   delay = delay - jitterAmount + rand[0,1) × jitterAmount × 2
//   This spreads retries across a ±25% band around the nominal delay.
//
// Parameters:
//   config  — provides InitialDelay, MaxDelay, Multiplier, Jitter.
//   attempt — 1-based retry index (1 = first retry after initial failure).
//
// Returns:
//   time.Duration — the computed wait, capped at config.MaxDelay.
//
// Called by: Retry (this file).
func calculateDelay(config RetryConfig, attempt int) time.Duration {
	// Exponential growth: InitialDelay × Multiplier^(attempt-1)
	// math.Pow handles fractional multipliers (e.g. 1.5x growth).
	delay := float64(config.InitialDelay) * math.Pow(config.Multiplier, float64(attempt-1))

	// Cap at MaxDelay to prevent runaway wait times on many-attempt configs.
	if delay > float64(config.MaxDelay) {
		delay = float64(config.MaxDelay)
	}

	if config.Jitter {
		// Add ±25% uniform random noise to spread retries across time.
		// jitter band: [delay-25%, delay+25%]
		jitter := delay * 0.25
		delay = delay - jitter + (jitter * 2 * rand.Float64())
		// rand.Float64() returns [0.0, 1.0), giving a uniform distribution
		// within the jitter band. This is not cryptographically random —
		// that is fine; we only need statistical spread, not security.
	}

	return time.Duration(delay)
}

// IsRetryableError returns true for errors that are likely transient and worth
// retrying. Permanent errors (invalid input, not found, forbidden) return false.
//
// Why string matching instead of error type assertions?
//   Many error types from third-party libraries (HTTP clients, DB drivers,
//   Redis) are not exported as types. String matching on the error message is
//   a pragmatic fallback. For libraries you control, prefer wrapping in a
//   custom error type and using errors.Is().
//
// Retryable patterns (transient errors that typically self-resolve):
//   "timeout"     — network or DB timeout; server may be slow under load.
//   "connection"  — TCP connection refused or reset; server may be restarting.
//   "temporary"   — net.Error.Temporary(); OS-level transient network error.
//   "unavailable" — gRPC/HTTP 503; service is temporarily overloaded.
//   "rate limit"  — HTTP 429; backing off is exactly the right response.
//   "503", "502", "500" — server-side errors; may resolve on retry.
//
// Parameters:
//   err — the error to classify; nil always returns false.
//
// Returns:
//   bool — true if the error is likely transient and retrying is sensible.
//
// Called by: cmd/worker/main.go task dispatch loop before deciding to retry.
func IsRetryableError(err error) bool {
	if err == nil {
		return false // no error — nothing to retry
	}

	// Check the error message against known transient error patterns.
	errStr := err.Error()
	retryablePatterns := []string{
		"timeout",     // network/DB timeout
		"connection",  // TCP connection issue
		"temporary",   // net.Error.Temporary()
		"unavailable", // service unavailable (gRPC status code)
		"rate limit",  // rate limiting — always worth backing off
		"503",         // HTTP Service Unavailable
		"502",         // HTTP Bad Gateway (upstream is down)
		"500",         // HTTP Internal Server Error (may be transient)
	}

	for _, pattern := range retryablePatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	// Error does not match any transient pattern — treat as permanent.
	return false
}
