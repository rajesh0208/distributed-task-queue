// Package broker provides the message-passing layer between the API server
// and the worker processes.
//
// This file (redis_stream.go) implements the Broker interface using Redis Streams
// — a persistent, append-only log introduced in Redis 5.0.
//
// Why Redis Streams instead of a simple LPUSH / BRPOP list?
//   - Persistence: stream entries survive a Redis restart (with AOF/RDB enabled),
//     whereas a list entry popped by a worker is gone forever.
//   - Consumer groups: multiple workers can read from the same stream without
//     competing; Redis assigns each message to exactly one consumer.
//   - PEL (Pending Entries List): Redis tracks every message delivered to a
//     consumer but not yet acknowledged. If the worker crashes, the message
//     stays in the PEL and can be re-claimed by another worker.
//
// It connects to:
//   - internal/models   — Task and TaskHandler types
//   - internal/tracing  — injects / extracts OTel trace context in messages
//
// Called by:
//   - cmd/api/main.go    — creates the broker and passes it to API handlers
//   - cmd/worker/main.go — calls Subscribe to consume tasks
package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"distributed-task-queue/internal/models"
	"distributed-task-queue/internal/tracing"

	"github.com/go-redis/redis/v8"
)

// ─────────────────────────────────────────────────────────────────────────────
// Interface and handler type
// ─────────────────────────────────────────────────────────────────────────────

// Broker defines the contract for message brokers.
// Using an interface (not a concrete type) means:
//   - Tests can substitute a fake/mock broker without touching Redis.
//   - We can swap from a single-node Redis stream (RedisStreamBroker) to
//     a cluster-aware implementation (RedisClusterBroker) by changing one
//     constructor call in main.go.
//
// Methods:
//   Publish         — push a task onto the queue (called by the API handler).
//   Subscribe       — consume tasks in a blocking loop (called by workers).
//   Acknowledge     — tell Redis a message was processed successfully.
//   GetPendingCount — how many messages are delivered-but-unacknowledged.
//   Close           — release the underlying Redis connection pool.
type Broker interface {
	Publish(ctx context.Context, task *models.Task) error
	Subscribe(ctx context.Context, consumerGroup, consumerName string, handler TaskHandler) error
	Acknowledge(ctx context.Context, streamKey, messageID string) error
	GetPendingCount(ctx context.Context) (int64, error)
	Close() error
}

// TaskHandler is the function signature workers must implement to process a task.
// The worker's main loop passes this function to Subscribe, which calls it for
// every message received from the stream.
//
// Parameters:
//   ctx  — context carries cancellation (shutdown signal) and the OTel trace span.
//   task — pointer to the fully decoded Task ready for processing.
//
// Returns:
//   error — non-nil causes the worker to mark the task as failed/retrying.
type TaskHandler func(ctx context.Context, task *models.Task) error

// ─────────────────────────────────────────────────────────────────────────────
// Concrete implementation — single-node Redis Streams
// ─────────────────────────────────────────────────────────────────────────────

// RedisStreamBroker implements Broker using a single-node Redis Streams client.
//
// Fields explained:
//
//	client    — the go-redis client that owns the connection pool; reused for
//	            all operations so we don't open a new connection per call.
//	streamKey — the Redis key for the stream (e.g. "tasks:stream").
//	            All XADD / XREADGROUP calls target this key.
//	groupName — the consumer-group name (e.g. "workers").
//	            All workers join the same group so each message goes to exactly
//	            one worker — no duplicates, automatic fan-out.
type RedisStreamBroker struct {
	client    *redis.Client
	streamKey string
	groupName string
}

// NewRedisStreamBroker creates a new Redis Streams broker, connects to Redis,
// and ensures the stream + consumer group exist before returning.
//
// Parameters:
//   redisAddr — Redis address in "host:port" form (e.g. "localhost:6379").
//   streamKey — stream name to publish to and consume from.
//   groupName — consumer group name; all workers share one group.
//
// Returns:
//   *RedisStreamBroker — ready-to-use broker.
//   error              — if Redis is unreachable or group creation fails.
//
// Called by: cmd/api/main.go, cmd/worker/main.go.
func NewRedisStreamBroker(redisAddr, streamKey, groupName string) (*RedisStreamBroker, error) {
	client := redis.NewClient(&redis.Options{
		Addr: redisAddr,
		DB:   0, // DB 0 is for the task stream; DB 1 is the task cache; DB 2 is general cache

		// PoolSize controls how many concurrent TCP connections the pool maintains.
		// 10 is plenty for typical throughput; increase if you see "pool exhausted" errors.
		PoolSize: 10,

		// MinIdleConns keeps 5 connections warm so the first requests after a
		// quiet period do not pay connection-setup latency.
		MinIdleConns: 5,

		DialTimeout:  5 * time.Second,  // how long to wait for a new TCP connection
		ReadTimeout:  3 * time.Second,  // max time to wait for a Redis reply
		WriteTimeout: 3 * time.Second,  // max time to wait for data to flush to Redis
		PoolTimeout:  4 * time.Second,  // how long to wait for a free pool slot

		// Idle connections are closed after 5 minutes of inactivity to avoid
		// hitting Redis's maxclients limit on quiet nights.
		IdleTimeout:        5 * time.Minute,
		IdleCheckFrequency: 1 * time.Minute, // how often to scan for idle connections

		// go-redis automatically retries on network-level errors up to MaxRetries
		// times, sleeping between attempts with exponential backoff.
		MaxRetries:      3,
		MinRetryBackoff: 8 * time.Millisecond,
		MaxRetryBackoff: 512 * time.Millisecond,
	})

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		// Fail fast — if Redis is down at startup, we cannot queue or process tasks.
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	broker := &RedisStreamBroker{
		client:    client,
		streamKey: streamKey,
		groupName: groupName,
	}

	// Create the consumer group (and the stream itself via MKSTREAM) now so
	// workers never race on creation.
	err := broker.createConsumerGroup(ctx)
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		// "BUSYGROUP" means another instance already created it — that is fine.
		// Any other error means Redis rejected the command entirely.
		return nil, fmt.Errorf("failed to create consumer group: %w", err)
	}

	return broker, nil
}

// createConsumerGroup issues XGROUP CREATE … MKSTREAM.
//
// MKSTREAM: creates the stream key if it does not already exist, so we
// never need to XADD a dummy message just to make the key appear.
//
// "0": start reading from message ID "0", meaning the consumer group will
// see every message already in the stream — important for reprocessing after
// a full outage where the stream outlived the group.
//
// Called by: NewRedisStreamBroker.
func (b *RedisStreamBroker) createConsumerGroup(ctx context.Context) error {
	return b.client.XGroupCreateMkStream(ctx, b.streamKey, b.groupName, "0").Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Publishing
// ─────────────────────────────────────────────────────────────────────────────

// Publish adds a task to the Redis Stream.
// It also injects the current OTel span context (traceparent/tracestate) into
// the message values so the consuming worker can continue the same distributed
// trace — the API's "submit" span and the worker's "process" span will appear
// connected in Jaeger.
//
// Parameters:
//   ctx  — carries the active OTel span; also used for request cancellation.
//   task — the task to queue; serialised to JSON before insertion.
//
// Returns:
//   error — if JSON marshalling or the XADD command fails.
//
// Called by: internal/api handlers (submit task endpoint).
func (b *RedisStreamBroker) Publish(ctx context.Context, task *models.Task) error {
	// Serialise the entire task struct to JSON so the stream entry is self-contained.
	// The worker deserialises this back to *models.Task with no schema drift.
	taskJSON, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to marshal task: %w", err)
	}

	values := map[string]interface{}{
		"task":     string(taskJSON),
		"priority": task.Priority, // stored as a top-level field so priority-aware
		// consumers could route high-priority tasks first without full decode
	}

	// Inject traceparent + tracestate so the worker can link its processing span
	// to this publish span, forming a complete end-to-end distributed trace.
	tracing.InjectToMap(ctx, values)

	// XADD appends the message to the stream.
	// ID "*" tells Redis to auto-generate a monotonically increasing ID using
	// millisecond timestamp + sequence number (e.g. "1700000000000-0").
	// This guarantees ordering and uniqueness without coordination.
	_, err = b.client.XAdd(ctx, &redis.XAddArgs{
		Stream: b.streamKey,
		ID:     "*",     // let Redis generate the ID
		Values: values,
	}).Result()
	if err != nil {
		// XADD failure usually means Redis is unreachable or OOM.
		return fmt.Errorf("failed to publish task to stream: %w", err)
	}

	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Consuming
// ─────────────────────────────────────────────────────────────────────────────

// Subscribe consumes tasks from the Redis Stream in a blocking loop.
//
// How consumer groups work:
//   - Multiple worker goroutines call Subscribe concurrently, each with a
//     unique consumerName (e.g. "worker-1", "worker-2").
//   - XREADGROUP delivers each message to exactly ONE consumer — no duplication.
//   - Delivered-but-unacknowledged messages sit in the PEL (Pending Entries List).
//   - If a worker crashes, its PEL entries stay there forever (until re-claimed).
//
// This loop handles two sources of messages:
//   1. Stalled messages (idle > 30 s in the PEL) — reclaimed from crashed workers.
//   2. New messages  — fetched with XREADGROUP using the special ">" ID.
//
// Parameters:
//   ctx           — cancellation; when cancelled the loop exits cleanly.
//   consumerGroup — the group to join; must match the group created in NewRedisStreamBroker.
//   consumerName  — unique name for this goroutine; used to identify the consumer in the PEL.
//   handler       — function called for each task; see TaskHandler.
//
// Returns:
//   error — context cancellation or unrecoverable Redis error.
//
// Called by: cmd/worker/main.go processLoop.
func (b *RedisStreamBroker) Subscribe(ctx context.Context, consumerGroup, consumerName string, handler TaskHandler) error {
	for {
		select {
		case <-ctx.Done():
			// Graceful shutdown — the caller cancelled the context.
			return ctx.Err()
		default:
			// ── Phase 1: reclaim stalled messages ────────────────────────────
			// XPendingExt lists messages in the PEL that have been idle (not
			// acknowledged) for more than 30 seconds. A message is "idle" when
			// its owner worker stopped heartbeating — usually a crash.
			pending, _ := b.client.XPendingExt(ctx, &redis.XPendingExtArgs{
				Stream: b.streamKey,
				Group:  consumerGroup,
				Start:  "-",   // earliest possible ID
				End:    "+",   // latest possible ID
				Count:  1,     // only grab one at a time to keep latency low
			}).Result()

			if len(pending) > 0 {
				// XClaim transfers ownership of the idle message from the original
				// (dead) consumer to this consumer. MinIdle: 30s means we only
				// steal messages that have been sitting untouched for ≥ 30 seconds,
				// giving the original worker a grace period to finish and ACK.
				claimed, _ := b.client.XClaim(ctx, &redis.XClaimArgs{
					Stream:   b.streamKey,
					Group:    consumerGroup,
					Consumer: consumerName,
					MinIdle:  30 * time.Second, // steal if idle longer than this
					Messages: []string{pending[0].ID},
				}).Result()

				if len(claimed) > 0 {
					msg := claimed[0]
					b.dispatchMessage(ctx, msg, consumerGroup, handler)
					continue // go back to check for more stalled messages before reading new ones
				}
			}

			// ── Phase 2: read new messages ────────────────────────────────────
			// XREADGROUP with ID ">" means "give me messages not yet delivered
			// to any consumer in this group". The Block: 2s makes this a
			// long-poll — the call returns immediately if there are messages,
			// or after 2 seconds if the stream is empty (avoiding a busy-loop).
			streams, err := b.client.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    consumerGroup,
				Consumer: consumerName,
				Streams:  []string{b.streamKey, ">"}, // ">" = only undelivered messages
				Count:    1,                           // one message per iteration keeps per-task latency low
				Block:    2 * time.Second,             // block up to 2 s if queue is empty
			}).Result()

			if err != nil {
				if err == redis.Nil {
					// redis.Nil is returned when Block timeout expires with no messages.
					// This is normal — loop and wait again.
					continue
				}
				// Any other error (network, auth) is unrecoverable.
				return fmt.Errorf("failed to read from stream: %w", err)
			}

			// Dispatch every message returned in this batch (Count:1 means at most 1).
			for _, stream := range streams {
				for _, msg := range stream.Messages {
					b.dispatchMessage(ctx, msg, consumerGroup, handler)
				}
			}
		}
	}
}

// dispatchMessage parses a single stream message, restores the OTel trace context
// from the message values, calls the task handler, and always acknowledges the
// message regardless of handler success or failure.
//
// Why always acknowledge even on failure?
//   Retry logic is database-driven: the worker increments Task.Retries in
//   PostgreSQL and re-publishes the task if Retries < MaxRetries. Leaving the
//   message un-ACKed would cause it to re-appear in the PEL and be re-delivered
//   indefinitely — bypassing the MaxRetries guard and potentially creating an
//   infinite retry loop.
//
// Parameters:
//   ctx     — carries cancellation and the parent OTel span.
//   msg     — the raw stream message from Redis.
//   group   — consumer group name needed for XACK.
//   handler — the worker's task-processing function.
//
// Called by: Subscribe (this file).
func (b *RedisStreamBroker) dispatchMessage(ctx context.Context, msg redis.XMessage, group string, handler TaskHandler) {
	// Pull the JSON task string out of the message values map.
	taskData, ok := msg.Values["task"].(string)
	if !ok {
		// Message is malformed — log and ACK to remove it from the PEL; retrying
		// a bad message will never succeed and would block the consumer forever.
		slog.Warn("broker: invalid message format, skipping",
			slog.String("stream", b.streamKey),
			slog.String("message_id", msg.ID),
		)
		b.client.XAck(ctx, b.streamKey, group, msg.ID) // remove bad message from PEL
		return
	}

	// Deserialise the JSON back into a Task struct so the handler works with
	// typed fields rather than raw strings.
	var task models.Task
	if err := json.Unmarshal([]byte(taskData), &task); err != nil {
		slog.Error("broker: failed to unmarshal task",
			slog.String("message_id", msg.ID),
			slog.String("error", err.Error()),
		)
		b.client.XAck(ctx, b.streamKey, group, msg.ID) // remove corrupted message
		return
	}

	// Restore the distributed trace context published by the API server.
	// This makes the worker's processing span a child of the API's submit span,
	// so a single Jaeger trace shows the full lifecycle: submit → queue → process.
	msgCtx := tracing.ExtractFromMap(ctx, msg.Values)

	// Invoke the worker's handler. Errors are logged but do NOT prevent ACK.
	handlerErr := handler(msgCtx, &task)

	// Always acknowledge — removes the message from the PEL.
	// The worker is responsible for updating Task.Status in PostgreSQL if it
	// needs to retry; the broker just clears the delivery record.
	if err := b.client.XAck(ctx, b.streamKey, group, msg.ID).Err(); err != nil {
		slog.Error("broker: failed to acknowledge message",
			slog.String("message_id", msg.ID),
			slog.String("error", err.Error()),
		)
		// Even if ACK fails (Redis blip), we do not crash — the message will
		// be re-delivered next time but the worker will detect the duplicate via
		// the deduplication layer.
	}

	if handlerErr != nil {
		slog.Warn("broker: task handler returned error (acknowledged anyway)",
			slog.String("task_id", task.ID),
			slog.String("error", handlerErr.Error()),
		)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Utility methods
// ─────────────────────────────────────────────────────────────────────────────

// Acknowledge marks a stream message as processed in a specific consumer group.
// Normally called internally by dispatchMessage, but exposed so callers with
// a direct message ID (e.g. the API's cancel endpoint) can ACK manually.
//
// Parameters:
//   ctx       — request context.
//   streamKey — the stream containing the message.
//   messageID — the auto-generated Redis stream message ID (e.g. "1700000000000-0").
//
// Returns:
//   error — if XACK fails (Redis down, wrong stream key, etc.).
//
// Called by: dispatchMessage, and optionally by API cancel handlers.
func (b *RedisStreamBroker) Acknowledge(ctx context.Context, streamKey, messageID string) error {
	return b.client.XAck(ctx, streamKey, b.groupName, messageID).Err()
}

// GetPendingCount returns the number of messages in the PEL for this consumer
// group — i.e. messages delivered to a consumer but not yet acknowledged.
// A consistently non-zero count indicates workers are slow or stalled.
//
// Parameters:
//   ctx — request context.
//
// Returns:
//   int64 — pending message count; 0 means all messages were acknowledged.
//   error — if XPENDING fails.
//
// Called by: health check, monitoring metrics collection.
func (b *RedisStreamBroker) GetPendingCount(ctx context.Context) (int64, error) {
	pending, err := b.client.XPending(ctx, b.streamKey, b.groupName).Result()
	if err != nil {
		return 0, err
	}
	return pending.Count, nil
}

// GetStreamLength returns the total number of entries currently in the stream,
// including both undelivered and already-acknowledged messages.
// Use GetPendingCount for queue depth; use this for stream growth monitoring.
//
// Called by: monitoring/metrics.go for Prometheus gauge "stream_length".
func (b *RedisStreamBroker) GetStreamLength(ctx context.Context) (int64, error) {
	return b.client.XLen(ctx, b.streamKey).Result()
}

// Close releases all connections in the Redis connection pool.
// Must be called during graceful shutdown before the process exits.
//
// Called by: cmd/api/main.go and cmd/worker/main.go shutdown sequences.
func (b *RedisStreamBroker) Close() error {
	return b.client.Close()
}

// GetClient returns the underlying *redis.Client for callers that need direct
// Redis access beyond the Broker interface — for example:
//   - health checks (PING)
//   - rate limiting (ZADD / ZCOUNT on a sorted set in security/ratelimit.go)
//   - distributed cache (SET / GET in cache/distributed.go)
//
// Not part of the Broker interface because the cluster implementation would
// return *redis.ClusterClient instead. Callers that use this must type-assert
// to the concrete broker type.
func (b *RedisStreamBroker) GetClient() *redis.Client {
	return b.client
}
