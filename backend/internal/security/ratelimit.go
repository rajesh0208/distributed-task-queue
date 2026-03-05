// File: internal/security/ratelimit.go
package security

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/gofiber/fiber/v2"
)

type RateLimiter struct {
	redis  *redis.Client
	limit  int
	window time.Duration
}

func NewRateLimiter(redis *redis.Client, limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		redis:  redis,
		limit:  limit,
		window: window,
	}
}

func (rl *RateLimiter) Allow(key string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	now := time.Now().Unix()
	windowStart := now - int64(rl.window.Seconds())

	// Optimize string conversion
	windowStartStr := fmt.Sprintf("%d", windowStart)
	pipe := rl.redis.Pipeline()
	pipe.ZRemRangeByScore(ctx, key, "0", windowStartStr)
	pipe.ZCard(ctx, key)
	pipe.ZAdd(ctx, key, &redis.Z{Score: float64(now), Member: now})
	pipe.Expire(ctx, key, rl.window)

	cmds, err := pipe.Exec(ctx)
	if err != nil {
		return false, err
	}

	count := cmds[1].(*redis.IntCmd).Val()
	return count < int64(rl.limit), nil
}

func RateLimitMiddleware(limiter *RateLimiter) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Optimized key generation with string builder
		var keyBuilder strings.Builder
		if userID := c.Locals("user_id"); userID != nil {
			keyBuilder.Grow(len("ratelimit:user:") + 50) // Estimate for userID
			keyBuilder.WriteString("ratelimit:user:")
			keyBuilder.WriteString(fmt.Sprintf("%v", userID))
		} else {
			ip := c.IP()
			keyBuilder.Grow(len("ratelimit:ip:") + len(ip))
			keyBuilder.WriteString("ratelimit:ip:")
			keyBuilder.WriteString(ip)
		}
		key := keyBuilder.String()

		allowed, err := limiter.Allow(key)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "rate limit check failed",
			})
		}

		if !allowed {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error":       "rate limit exceeded",
				"retry_after": limiter.window.Seconds(),
			})
		}

		return c.Next()
	}
}
