// File: internal/broker/redis_stream.go
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

// Broker defines the interface for message brokers
type Broker interface {
	Publish(ctx context.Context, task *models.Task) error
	Subscribe(ctx context.Context, consumerGroup, consumerName string, handler TaskHandler) error
	Acknowledge(ctx context.Context, streamKey, messageID string) error
	GetPendingCount(ctx context.Context) (int64, error)
	Close() error
}

// TaskHandler is a function that processes tasks
type TaskHandler func(ctx context.Context, task *models.Task) error

// RedisStreamBroker implements Broker using Redis Streams
type RedisStreamBroker struct {
	client    *redis.Client
	streamKey string
	groupName string
}

// NewRedisStreamBroker creates a new Redis Streams broker
func NewRedisStreamBroker(redisAddr, streamKey, groupName string) (*RedisStreamBroker, error) {
	client := redis.NewClient(&redis.Options{
		Addr:               redisAddr,
		DB:                 0,
		PoolSize:           10,
		MinIdleConns:       5,
		DialTimeout:        5 * time.Second,
		ReadTimeout:        3 * time.Second,
		WriteTimeout:       3 * time.Second,
		PoolTimeout:        4 * time.Second,
		IdleTimeout:        5 * time.Minute,
		IdleCheckFrequency: 1 * time.Minute,
		MaxRetries:         3,
		MinRetryBackoff:    8 * time.Millisecond,
		MaxRetryBackoff:    512 * time.Millisecond,
	})

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	broker := &RedisStreamBroker{
		client:    client,
		streamKey: streamKey,
		groupName: groupName,
	}

	err := broker.createConsumerGroup(ctx)
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		return nil, fmt.Errorf("failed to create consumer group: %w", err)
	}

	return broker, nil
}

// createConsumerGroup creates the consumer group and the stream (via MKSTREAM) if
// they do not already exist. The caller ignores "BUSYGROUP" errors so this is
// idempotent across multiple worker instances starting simultaneously.
func (b *RedisStreamBroker) createConsumerGroup(ctx context.Context) error {
	return b.client.XGroupCreateMkStream(ctx, b.streamKey, b.groupName, "0").Err()
}

// Publish adds a task to the Redis Stream.
// It also injects the current OTel span context (traceparent/tracestate) into the
// message values so the consuming worker can continue the same distributed trace.
func (b *RedisStreamBroker) Publish(ctx context.Context, task *models.Task) error {
	taskJSON, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to marshal task: %w", err)
	}

	values := map[string]interface{}{
		"task":     string(taskJSON),
		"priority": task.Priority,
	}
	// Inject traceparent + tracestate so the worker can link its processing span
	// to this publish span, forming a complete end-to-end distributed trace.
	tracing.InjectToMap(ctx, values)

	_, err = b.client.XAdd(ctx, &redis.XAddArgs{
		Stream: b.streamKey,
		ID:     "*",
		Values: values,
	}).Result()
	if err != nil {
		return fmt.Errorf("failed to publish task to stream: %w", err)
	}

	return nil
}

// Subscribe consumes tasks from the Redis Stream.
// For each message it extracts the OTel trace context so the handler runs as a child
// span of the original API submit request — linking submission and processing in Jaeger.
func (b *RedisStreamBroker) Subscribe(ctx context.Context, consumerGroup, consumerName string, handler TaskHandler) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			// Claim pending messages from crashed/stalled workers (idle > 30s).
			pending, _ := b.client.XPendingExt(ctx, &redis.XPendingExtArgs{
				Stream: b.streamKey,
				Group:  consumerGroup,
				Start:  "-",
				End:    "+",
				Count:  1,
			}).Result()

			if len(pending) > 0 {
				claimed, _ := b.client.XClaim(ctx, &redis.XClaimArgs{
					Stream:   b.streamKey,
					Group:    consumerGroup,
					Consumer: consumerName,
					MinIdle:  30 * time.Second,
					Messages: []string{pending[0].ID},
				}).Result()

				if len(claimed) > 0 {
					msg := claimed[0]
					b.dispatchMessage(ctx, msg, consumerGroup, handler)
					continue
				}
			}

			// Read new messages from the stream (block up to 2s if empty).
			streams, err := b.client.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    consumerGroup,
				Consumer: consumerName,
				Streams:  []string{b.streamKey, ">"},
				Count:    1,
				Block:    2 * time.Second,
			}).Result()

			if err != nil {
				if err == redis.Nil {
					continue // no messages — loop and wait
				}
				return fmt.Errorf("failed to read from stream: %w", err)
			}

			for _, stream := range streams {
				for _, msg := range stream.Messages {
					b.dispatchMessage(ctx, msg, consumerGroup, handler)
				}
			}
		}
	}
}

// dispatchMessage parses a single stream message, restores the OTel trace context
// from the message values, and calls the task handler. Always acknowledges the
// message — retry logic lives in the worker via the database, not Redis.
func (b *RedisStreamBroker) dispatchMessage(ctx context.Context, msg redis.XMessage, group string, handler TaskHandler) {
	taskData, ok := msg.Values["task"].(string)
	if !ok {
		slog.Warn("broker: invalid message format, skipping",
			slog.String("stream", b.streamKey),
			slog.String("message_id", msg.ID),
		)
		b.client.XAck(ctx, b.streamKey, group, msg.ID)
		return
	}

	var task models.Task
	if err := json.Unmarshal([]byte(taskData), &task); err != nil {
		slog.Error("broker: failed to unmarshal task",
			slog.String("message_id", msg.ID),
			slog.String("error", err.Error()),
		)
		b.client.XAck(ctx, b.streamKey, group, msg.ID)
		return
	}

	// Restore the distributed trace context published by the API server.
	// This makes the worker's span a child of the API's "submit task" span.
	msgCtx := tracing.ExtractFromMap(ctx, msg.Values)

	handlerErr := handler(msgCtx, &task)

	// Always acknowledge — retry logic is database-driven, not stream-driven.
	if err := b.client.XAck(ctx, b.streamKey, group, msg.ID).Err(); err != nil {
		slog.Error("broker: failed to acknowledge message",
			slog.String("message_id", msg.ID),
			slog.String("error", err.Error()),
		)
	}

	if handlerErr != nil {
		slog.Warn("broker: task handler returned error (acknowledged)",
			slog.String("task_id", task.ID),
			slog.String("error", handlerErr.Error()),
		)
	}
}

// Acknowledge marks a message as processed
func (b *RedisStreamBroker) Acknowledge(ctx context.Context, streamKey, messageID string) error {
	return b.client.XAck(ctx, streamKey, b.groupName, messageID).Err()
}

// GetPendingCount returns the number of pending messages
func (b *RedisStreamBroker) GetPendingCount(ctx context.Context) (int64, error) {
	pending, err := b.client.XPending(ctx, b.streamKey, b.groupName).Result()
	if err != nil {
		return 0, err
	}
	return pending.Count, nil
}

// GetStreamLength returns the total number of messages in the stream
func (b *RedisStreamBroker) GetStreamLength(ctx context.Context) (int64, error) {
	return b.client.XLen(ctx, b.streamKey).Result()
}

// Close closes the Redis connection
func (b *RedisStreamBroker) Close() error {
	return b.client.Close()
}

// GetClient returns the underlying Redis client for callers that need direct access
// (e.g. health checks, rate limiting). Not part of the Broker interface so the
// cluster implementation can return *redis.ClusterClient instead.
func (b *RedisStreamBroker) GetClient() *redis.Client {
	return b.client
}
