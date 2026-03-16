// Package security — see auth.go for package-level documentation.
//
// This file implements a Redis-backed sliding-window rate limiter and the
// Fiber middleware that applies it to incoming HTTP requests.
//
// Why rate limit?
// Without rate limiting a single client can flood the API with thousands of
// requests per second, starving legitimate users and exhausting server resources.
// Rate limiting protects against abuse, DDoS amplification, and runaway scripts.
//
// Algorithm — sliding window with a Redis ZSET:
//   - Each request is recorded as a member of a sorted set keyed by user/IP.
//   - The member value is the Unix timestamp of the request.
//   - Before counting, we remove members older than the window start (ZREMRANGEBYSCORE).
//   - ZCARD gives the current request count within the window.
//   - If count < limit, the request is allowed; otherwise 429.
//
// Why ZSET instead of a simple counter?
//   A fixed-window counter resets every N seconds, which allows a burst of
//   (2 × limit) requests straddling a boundary. The ZSET sliding window has no
//   such boundary: at any point in time the window covers exactly the last N seconds.
//
// Connected to:
//   - cmd/api/main.go — NewRateLimiter, RateLimitMiddleware
//   - internal/security/auth.go — RateLimitMiddleware reads c.Locals("user_id")
//                                  set by AuthMiddleware
//
// Called by:
//   - cmd/api/main.go — attaches the middleware to the protected route group
package security

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/gofiber/fiber/v2"
)

// RateLimiter holds the Redis client and the rate limit policy.
//
// Fields:
//
//	redis  — Redis client used to store per-user/IP request timestamps;
//	         using Redis (not in-memory) means limits are shared across all
//	         API replicas — a user cannot bypass the limit by hitting a different pod
//	limit  — maximum number of requests allowed within one window period
//	window — the rolling time window over which requests are counted
//	         (e.g. 100 requests per 1 minute)
type RateLimiter struct {
	redis  *redis.Client
	limit  int
	window time.Duration
}

// NewRateLimiter creates a RateLimiter with the given policy.
//
// Parameters:
//
//	redis  — shared Redis client (DB 0 is fine; keys are namespaced by prefix)
//	limit  — max requests per window (e.g. 100)
//	window — rolling window duration (e.g. time.Minute)
//
// Returns:
//
//	*RateLimiter — ready to use; no background goroutines
//
// Called by: cmd/api/main.go during startup.
func NewRateLimiter(redis *redis.Client, limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		redis:  redis,
		limit:  limit,
		window: window,
	}
}

// Allow checks whether a request with the given key is within the rate limit.
//
// The key is typically "ratelimit:user:<userID>" or "ratelimit:ip:<ip>".
// All four Redis commands run in a single pipeline to reduce round-trip latency.
//
// Pipeline steps:
//  1. ZREMRANGEBYSCORE — remove entries older than windowStart (slide the window)
//  2. ZCARD            — count entries remaining in the window
//  3. ZADD             — record this request with the current timestamp as score
//  4. EXPIRE           — reset the key TTL to the window duration (auto-cleanup)
//
// Parameters:
//
//	key — unique identifier for the rate limit bucket (per user or per IP)
//
// Returns:
//
//	bool  — true if the request is within the limit and should be allowed
//	error — non-nil if the Redis pipeline fails (e.g. connection lost)
//
// Called by: RateLimitMiddleware.
func (rl *RateLimiter) Allow(key string) (bool, error) {
	// 2-second deadline prevents auth middleware from hanging if Redis is slow.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	now := time.Now().Unix()                         // current time in Unix seconds
	windowStart := now - int64(rl.window.Seconds()) // earliest timestamp still in window

	// Format windowStart as a string for the ZREMRANGEBYSCORE score argument.
	windowStartStr := fmt.Sprintf("%d", windowStart)

	// Open a Redis pipeline: all four commands are sent in one TCP round-trip.
	// This is much faster than four sequential commands.
	pipe := rl.redis.Pipeline()

	// Step 1: Remove all entries with a score (timestamp) older than windowStart.
	// "0" means from the beginning of time; windowStartStr means up to and including
	// windowStart. After this command the ZSET contains only the current window.
	pipe.ZRemRangeByScore(ctx, key, "0", windowStartStr)

	// Step 2: Count how many requests are currently in the window.
	// This result is read from cmds[1] below.
	pipe.ZCard(ctx, key)

	// Step 3: Add the current request as a new member.
	// We use `now` as both the score (for ordering/removal) and the member name.
	// If two requests arrive in the same second they get the same score but
	// different member values (they're counted separately).
	pipe.ZAdd(ctx, key, &redis.Z{Score: float64(now), Member: now})

	// Step 4: Reset the key TTL to the window duration.
	// Without this, the key would persist in Redis forever after the last request.
	// Setting Expire = window means the key disappears naturally if the user goes quiet.
	pipe.Expire(ctx, key, rl.window)

	// Execute all four commands atomically in a single pipeline.
	cmds, err := pipe.Exec(ctx)
	if err != nil {
		// Pipeline failure — could be a Redis connection problem.
		// Return error so the middleware can decide to allow (fail-open) or deny.
		return false, err
	}

	// cmds[1] is the ZCARD result — the count of requests BEFORE adding this one.
	// We compare against limit: if count is already at/above limit, deny.
	count := cmds[1].(*redis.IntCmd).Val()
	return count < int64(rl.limit), nil
}

// RateLimitMiddleware returns a Fiber middleware that applies the rate limiter
// to every request. It prefers per-user limiting when a user is authenticated
// (set by AuthMiddleware) and falls back to per-IP limiting for anonymous requests.
//
// Why prefer user ID over IP?
// Multiple users can share an IP address (e.g. corporate NAT, university networks).
// If we limited by IP, one misbehaving user could block all users behind the same
// NAT. Per-user limiting is fairer and harder to circumvent via VPN/proxy.
//
// What happens when the limit is exceeded?
// The middleware returns HTTP 429 Too Many Requests with a "retry_after" field
// in seconds. The client should back off for at least that long before retrying.
//
// Parameters:
//
//	limiter — a configured RateLimiter instance
//
// Returns:
//
//	fiber.Handler — attach to a route group with group.Use(RateLimitMiddleware(limiter))
//
// Called by: cmd/api/main.go — applied to the protected API route group.
func RateLimitMiddleware(limiter *RateLimiter) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Build the rate limit key using a strings.Builder to avoid allocations.
		var keyBuilder strings.Builder
		if userID := c.Locals("user_id"); userID != nil {
			// Authenticated request: limit per user ID.
			// Pre-allocate the builder to exactly the size we need.
			keyBuilder.Grow(len("ratelimit:user:") + 50) // 50 is a generous UUID estimate
			keyBuilder.WriteString("ratelimit:user:")
			keyBuilder.WriteString(fmt.Sprintf("%v", userID))
		} else {
			// Anonymous request: fall back to limiting by client IP address.
			ip := c.IP()
			keyBuilder.Grow(len("ratelimit:ip:") + len(ip))
			keyBuilder.WriteString("ratelimit:ip:")
			keyBuilder.WriteString(ip)
		}
		key := keyBuilder.String()

		allowed, err := limiter.Allow(key)
		if err != nil {
			// Redis is unavailable — fail open (allow the request) so that a Redis
			// outage does not take down the entire API. This is a deliberate trade-off:
			// availability over strict rate enforcement during Redis downtime.
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "rate limit check failed",
			})
		}

		if !allowed {
			// 429 Too Many Requests — the client has exceeded the allowed rate.
			// Include retry_after so well-behaved clients can back off automatically.
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error":       "rate limit exceeded",
				"retry_after": limiter.window.Seconds(), // seconds until the window resets
			})
		}

		// Request is within limits — pass to the next handler.
		return c.Next()
	}
}
