// File: internal/reliability/retry.go
package reliability

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"
)

// RetryConfig holds retry configuration
type RetryConfig struct {
	MaxAttempts int
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Multiplier   float64
	Jitter       bool
}

// DefaultRetryConfig returns default retry configuration
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 1 * time.Second,
		MaxDelay:     30 * time.Second,
		Multiplier:   2.0,
		Jitter:       true,
	}
}

// Retry executes a function with retry logic
func Retry(ctx context.Context, config RetryConfig, fn func() error) error {
	var lastErr error

	for attempt := 0; attempt < config.MaxAttempts; attempt++ {
		if attempt > 0 {
			delay := calculateDelay(config, attempt)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		err := fn()
		if err == nil {
			return nil
		}

		lastErr = err
	}

	return fmt.Errorf("max attempts (%d) exceeded: %w", config.MaxAttempts, lastErr)
}

// RetryWithBackoff executes a function with exponential backoff
func RetryWithBackoff(ctx context.Context, config RetryConfig, fn func() error) error {
	return Retry(ctx, config, fn)
}

func calculateDelay(config RetryConfig, attempt int) time.Duration {
	delay := float64(config.InitialDelay) * math.Pow(config.Multiplier, float64(attempt-1))
	
	if delay > float64(config.MaxDelay) {
		delay = float64(config.MaxDelay)
	}

	if config.Jitter {
		// Add jitter: ±25% of delay using a proper random float in [0, 1)
		jitter := delay * 0.25
		delay = delay - jitter + (jitter * 2 * rand.Float64())
	}

	return time.Duration(delay)
}

// IsRetryableError checks if an error is retryable
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Add logic to determine if error is retryable
	// For example: network errors, timeouts, temporary failures
	errStr := err.Error()
	retryablePatterns := []string{
		"timeout",
		"connection",
		"temporary",
		"unavailable",
		"rate limit",
		"503",
		"502",
		"500",
	}

	for _, pattern := range retryablePatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	return false
}

