// File: internal/broker/redis_cluster.go
package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"distributed-task-queue/internal/models"

	"github.com/go-redis/redis/v8"
)

// RedisClusterBroker implements Broker using Redis Cluster
type RedisClusterBroker struct {
	client    *redis.ClusterClient
	streamKey string
	groupName string
}

// NewRedisClusterBroker creates a new Redis Cluster broker
func NewRedisClusterBroker(addrs []string, streamKey, groupName string) (*RedisClusterBroker, error) {
	client := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:              addrs,
		MaxRetries:         3,
		DialTimeout:        5 * time.Second,
		ReadTimeout:        3 * time.Second,
		WriteTimeout:       3 * time.Second,
		PoolSize:           10,
		MinIdleConns:       5,
		IdleTimeout:        5 * time.Minute,
		IdleCheckFrequency: 1 * time.Minute,
	})

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis cluster: %w", err)
	}

	b := &RedisClusterBroker{
		client:    client,
		streamKey: streamKey,
		groupName: groupName,
	}

	if err := b.createConsumerGroup(ctx); err != nil {
		return nil, err
	}

	return b, nil
}

func (b *RedisClusterBroker) createConsumerGroup(ctx context.Context) error {
	err := b.client.XGroupCreateMkStream(ctx, b.streamKey, b.groupName, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		return fmt.Errorf("failed to create consumer group: %w", err)
	}
	return nil
}

// Publish adds a task to the Redis Stream
func (b *RedisClusterBroker) Publish(ctx context.Context, task *models.Task) error {
	taskJSON, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to marshal task: %w", err)
	}

	_, err = b.client.XAdd(ctx, &redis.XAddArgs{
		Stream: b.streamKey,
		ID:     "*",
		Values: map[string]interface{}{
			"task":     taskJSON,
			"priority": task.Priority,
		},
	}).Result()
	if err != nil {
		return fmt.Errorf("failed to publish task to stream: %w", err)
	}

	return nil
}

// Subscribe consumes tasks from the Redis Stream
func (b *RedisClusterBroker) Subscribe(ctx context.Context, consumerGroup, consumerName string, handler TaskHandler) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			streams, err := b.client.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    consumerGroup,
				Consumer: consumerName,
				Streams:  []string{b.streamKey, ">"},
				Count:    1,
				Block:    2 * time.Second,
			}).Result()

			if err != nil {
				if err == redis.Nil {
					continue
				}
				return fmt.Errorf("failed to read from stream: %w", err)
			}

			for _, stream := range streams {
				for _, message := range stream.Messages {
					taskData, ok := message.Values["task"].(string)
					if !ok {
						log.Printf("Invalid message format, skipping: %v", message.ID)
						b.client.XAck(ctx, b.streamKey, consumerGroup, message.ID)
						continue
					}

					var task models.Task
					if err := json.Unmarshal([]byte(taskData), &task); err != nil {
						log.Printf("Failed to unmarshal task: %v, error: %v", message.ID, err)
						b.client.XAck(ctx, b.streamKey, consumerGroup, message.ID)
						continue
					}

					handlerErr := handler(ctx, &task)
					if err := b.client.XAck(ctx, b.streamKey, consumerGroup, message.ID).Err(); err != nil {
						log.Printf("Failed to acknowledge message %s: %v", message.ID, err)
					}
					if handlerErr != nil {
						log.Printf("Task handler failed for task %s: %v (acknowledged)", task.ID, handlerErr)
					}
				}
			}
		}
	}
}

// Acknowledge marks a message as processed
func (b *RedisClusterBroker) Acknowledge(ctx context.Context, streamKey, messageID string) error {
	return b.client.XAck(ctx, streamKey, b.groupName, messageID).Err()
}

// GetPendingCount returns the number of pending messages
func (b *RedisClusterBroker) GetPendingCount(ctx context.Context) (int64, error) {
	pending, err := b.client.XPending(ctx, b.streamKey, b.groupName).Result()
	if err != nil {
		return 0, err
	}
	return pending.Count, nil
}

// GetClusterClient returns the underlying Redis cluster client
func (b *RedisClusterBroker) GetClusterClient() *redis.ClusterClient {
	return b.client
}

// Close closes the Redis cluster connection
func (b *RedisClusterBroker) Close() error {
	return b.client.Close()
}
