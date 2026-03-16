// Package cache provides a generic distributed cache backed by Redis.
//
// This file (distributed.go) implements a general-purpose key-value cache.
// It is intentionally separate from the task-specific write-through cache in
// storage/storage.go for two important reasons:
//
//   1. Separation of concerns:
//      storage.go caches Task and User structs and knows their TTL rules
//      (active task = 30 s, completed task = 10 min). This package knows
//      nothing about task shapes — it stores any serialisable value.
//
//   2. Redis DB isolation:
//      storage.go uses Redis DB 1. This package uses DB 2. They share the
//      same Redis server but live in separate logical databases, so a
//      FLUSHDB or key collision in one cannot affect the other.
//
// What is a distributed cache vs a local in-memory cache?
//   A local cache (e.g. a Go sync.Map or github.com/patrickmahon/ristretto)
//   lives in the process heap — it is fast but invisible to other processes.
//   When you run two API replicas, each has its own local cache, so an
//   invalidation on replica-1 leaves replica-2 with stale data.
//   A distributed cache (Redis) is a shared network store — all replicas
//   read from and write to the same instance, so invalidations are immediately
//   visible cluster-wide. The trade-off is network latency (~0.1–1 ms locally)
//   vs the 0 µs of a local map lookup.
//
// Cache stampede prevention:
//   A cache stampede happens when a popular key expires and many goroutines
//   simultaneously find a cache miss, all rush to recompute the value, and
//   hammer the database at once. Setting a TTL (even a generous one) helps
//   because the window between expiry and re-population is short. For extremely
//   hot keys, consider a "stale-while-revalidate" pattern or a lock.
//
// It connects to:
//   - Redis DB 2 (separate from the broker on DB 0 and the task cache on DB 1)
//
// Called by:
//   - cmd/api/main.go — instantiates DistributedCache and passes it to handlers
//     that need non-task caching (e.g. metrics snapshots, OAuth state tokens).
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
)

// ErrCacheMiss is returned by Get when the key does not exist in Redis.
// Callers should treat this as a signal to fetch from the authoritative source
// (database, external API) and then call Set to populate the cache.
var ErrCacheMiss = fmt.Errorf("cache miss")

// DistributedCache wraps a Redis client and exposes simple Set/Get/Delete
// operations for any JSON-serialisable value.
//
// Fields explained:
//
//	client — *redis.Client pointed at DB 2. All cache keys live in this
//	          logical database, isolated from the message broker (DB 0) and
//	          the task/user cache in storage.go (DB 1).
//	ttl    — the default TTL applied when Set is called with ttl == 0.
//	          Having a default TTL means cached data always expires eventually,
//	          preventing stale entries from accumulating indefinitely.
type DistributedCache struct {
	client *redis.Client
	ttl    time.Duration
}

// NewDistributedCache creates a DistributedCache connected to Redis DB 2.
//
// Why DB 2?
//   DB 0 is for the message broker queue, DB 1 is for the task/user storage
//   cache. Using DB 2 keeps this general cache logically separate so a
//   FLUSHDB for cache maintenance never accidentally wipes task data.
//
// Parameters:
//   redisAddr  — Redis address in "host:port" form (e.g. "localhost:6379").
//   defaultTTL — TTL used when Set is called with ttl == 0.
//                A short TTL (seconds) prevents stale data;
//                a long TTL (minutes) reduces database load.
//
// Returns:
//   *DistributedCache — ready-to-use cache instance.
//   error             — if Redis is unreachable on startup.
//
// Called by: cmd/api/main.go.
func NewDistributedCache(redisAddr string, defaultTTL time.Duration) (*DistributedCache, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: "", // no password — rely on network-level security (VPC / firewall)
		DB:       2,  // DB 0 = broker, DB 1 = storage cache, DB 2 = this distributed cache

		DialTimeout:  5 * time.Second, // how long to wait for a new TCP connection
		ReadTimeout:  3 * time.Second, // max wait for a Redis GET reply
		WriteTimeout: 3 * time.Second, // max wait for a Redis SET to be acknowledged
		PoolSize:     10,              // max concurrent connections
		MinIdleConns: 5,               // keep 5 warm to avoid connection-setup latency on bursts
	})

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		// Fail fast — if Redis is unavailable at startup the cache cannot work.
		// The caller (main.go) will either exit or fall back to no-cache mode.
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return &DistributedCache{
		client: client,
		ttl:    defaultTTL,
	}, nil
}

// Set stores value at key in Redis, serialised as JSON.
//
// Why JSON serialisation?
//   JSON is self-describing and language-agnostic. Values written by the Go API
//   can be read by debugging tools (redis-cli, RedisInsight) without a schema.
//   For very high-throughput caches, msgpack or protobuf would be faster and
//   smaller, but JSON is good enough for typical rates.
//
// Parameters:
//   ctx   — request context; used for cancellation and OTel tracing.
//   key   — Redis key string; by convention use namespaced keys like "metrics:system".
//   value — any JSON-serialisable Go value (struct, map, slice, primitive).
//   ttl   — expiry duration. Pass 0 to use the DistributedCache default TTL.
//           TTL is important: without it a key lives forever and may serve data
//           that is weeks old. Always set a TTL that matches how fresh the data
//           needs to be.
//
// Returns:
//   error — if JSON marshalling or the Redis SET command fails.
//
// Called by: API handlers that want to cache computed or fetched values.
func (c *DistributedCache) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	// Marshal the value to JSON bytes before storing in Redis.
	// Redis stores strings/bytes — we must serialise structured data ourselves.
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("failed to marshal value: %w", err)
	}

	// Fall back to the cache-wide default TTL if the caller did not specify one.
	if ttl == 0 {
		ttl = c.ttl
	}

	// SET key value EX <seconds> — Redis atomically stores the value with expiry.
	err = c.client.Set(ctx, key, data, ttl).Err()
	if err != nil {
		return fmt.Errorf("failed to set cache: %w", err)
	}

	return nil
}

// Get retrieves the value stored at key and deserialises it into dest.
//
// Cache hit path (fast):  Redis returns the JSON bytes → unmarshal into dest.
// Cache miss path (slow): Redis returns redis.Nil → return ErrCacheMiss.
//                         The caller fetches from the source and calls Set.
//
// This "look-aside" pattern (check cache first, fall back to source) avoids DB
// queries for frequently read data without making the cache mandatory.
//
// Parameters:
//   ctx  — request context.
//   key  — Redis key to look up.
//   dest — pointer to the Go value to fill (e.g. &myStruct). Must be a pointer
//          so json.Unmarshal can write through it.
//
// Returns:
//   error — ErrCacheMiss if the key is absent or expired; other errors on
//           Redis or JSON failures.
//
// Called by: API handlers that implement the look-aside cache pattern.
func (c *DistributedCache) Get(ctx context.Context, key string, dest interface{}) error {
	// GET returns the raw bytes, or redis.Nil if the key does not exist.
	data, err := c.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		// Key not found (or expired) — signal a cache miss to the caller.
		return ErrCacheMiss
	}
	if err != nil {
		// Real Redis error (network, auth, OOM) — different from a miss.
		return fmt.Errorf("failed to get cache: %w", err)
	}

	// Deserialise the JSON bytes into the caller-supplied destination pointer.
	if err := json.Unmarshal(data, dest); err != nil {
		// Corrupt or schema-mismatched entry — treat as a miss by returning error;
		// the caller can refetch and overwrite the bad entry with Set.
		return fmt.Errorf("failed to unmarshal value: %w", err)
	}

	return nil
}

// Delete removes a key from the cache immediately (before its TTL expires).
// Used for cache invalidation after a mutation — e.g. after updating system
// metrics we delete the cached snapshot so the next Get fetches fresh data.
//
// Parameters:
//   ctx — request context.
//   key — the Redis key to remove.
//
// Returns:
//   error — if the DEL command fails (Redis down, network error).
//
// Called by: handlers that mutate data cached by this package.
func (c *DistributedCache) Delete(ctx context.Context, key string) error {
	err := c.client.Del(ctx, key).Err()
	if err != nil {
		return fmt.Errorf("failed to delete cache: %w", err)
	}
	return nil
}

// Exists reports whether a key is currently set in the cache (not expired).
// Useful when you only need to check presence without fetching the full value —
// for example, checking if an idempotency key was already processed.
//
// Parameters:
//   ctx — request context.
//   key — the Redis key to test.
//
// Returns:
//   bool  — true if the key exists and has not expired.
//   error — if the EXISTS command fails.
//
// Called by: idempotency checks, deduplication checks that use this cache layer.
func (c *DistributedCache) Exists(ctx context.Context, key string) (bool, error) {
	// EXISTS returns the number of keys found (0 or 1 for a single key).
	count, err := c.client.Exists(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check existence: %w", err)
	}
	return count > 0, nil
}

// Clear deletes all keys whose names match a Redis glob pattern.
// Uses SCAN to iterate safely over large keyspaces without blocking the server
// (unlike KEYS * which locks Redis until it finishes).
//
// Example: Clear(ctx, "metrics:*") removes all keys starting with "metrics:".
//
// Warning: Clear is a O(N) operation. Use it for cache flush / maintenance
// tasks, not on hot paths.
//
// Parameters:
//   ctx     — request context; each DEL call inherits this context.
//   pattern — Redis glob pattern (e.g. "session:*", "metrics:*").
//
// Returns:
//   error — if SCAN iteration or any DEL fails.
//
// Called by: admin cache-flush handlers, test teardown.
func (c *DistributedCache) Clear(ctx context.Context, pattern string) error {
	// SCAN iterates keys in chunks without blocking. The cursor starts at 0
	// and the iterator automatically follows cursor replies until it wraps back
	// to 0 (end of keyspace).
	iter := c.client.Scan(ctx, 0, pattern, 0).Iterator()
	for iter.Next(ctx) {
		if err := c.client.Del(ctx, iter.Val()).Err(); err != nil {
			// Stop on first DEL error — a partial clear is better than silent
			// failure, and the caller can retry the entire operation.
			return fmt.Errorf("failed to delete key: %w", err)
		}
	}
	// Check if the SCAN itself encountered an error (e.g. network blip).
	return iter.Err()
}

// Close releases all connections in the Redis pool.
// Must be called during graceful shutdown to avoid leaking TCP connections.
//
// Called by: cmd/api/main.go shutdown sequence.
func (c *DistributedCache) Close() error {
	return c.client.Close()
}
