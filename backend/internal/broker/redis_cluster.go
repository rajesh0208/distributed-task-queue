// Package broker provides the message-passing layer between the API server
// and the worker processes.
//
// This file (redis_cluster.go) provides a production-grade alternative to
// redis_stream.go that connects to a Redis Cluster instead of a single node.
//
// What is Redis Cluster?
//   A Redis Cluster spreads data across multiple primary nodes using consistent
//   hashing. Each key is assigned to one of 16 384 hash slots, and each primary
//   owns a contiguous range of slots. If a primary fails, a replica is
//   automatically promoted — this is the High Availability (HA) guarantee.
//
// Why does sharding matter for streams?
//   A stream key like "tasks:stream" is always hashed to the same slot, so all
//   messages for that stream live on one primary. Sharding gives HA (a failover
//   replica takes over) but does NOT spread stream load across nodes. For
//   horizontal throughput scaling you would use multiple stream keys with a
//   hash tag (e.g. "tasks:{0}", "tasks:{1}").
//
// Why the interface is the same as redis_stream.go:
//   Both files implement the Broker interface. The rest of the codebase depends
//   only on Broker, so switching from single-node to cluster is one line in
//   main.go — no other files change.
//
// It connects to:
//   - internal/models — Task struct and TaskHandler type
//
// Called by:
//   - cmd/api/main.go    — when REDIS_CLUSTER_ADDRS env var is set
//   - cmd/worker/main.go — when REDIS_CLUSTER_ADDRS env var is set
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

// RedisClusterBroker implements the Broker interface using Redis Cluster.
//
// Fields explained:
//
//	client    — *redis.ClusterClient from go-redis; unlike *redis.Client it
//	            transparently routes commands to the correct cluster shard,
//	            follows MOVED/ASK redirects, and reconnects on node failure.
//	streamKey — stream key name; cluster hashes this to one slot/shard.
//	            All cluster nodes replicate the key but only one primary owns it.
//	groupName — same consumer-group semantics as the single-node broker.
type RedisClusterBroker struct {
	client    *redis.ClusterClient
	streamKey string
	groupName string
}

// NewRedisClusterBroker creates a RedisClusterBroker, connects to the cluster,
// and ensures the stream + consumer group exist.
//
// Parameters:
//   addrs     — list of "host:port" strings for seed nodes.
//               go-redis only needs a subset; it discovers the full topology
//               automatically via CLUSTER NODES on startup.
//   streamKey — stream key name to publish to and consume from.
//   groupName — consumer group name; all workers join the same group.
//
// Returns:
//   *RedisClusterBroker — ready-to-use broker.
//   error               — if any seed node is unreachable or group creation fails.
//
// Called by: cmd/api/main.go, cmd/worker/main.go when cluster mode is enabled.
func NewRedisClusterBroker(addrs []string, streamKey, groupName string) (*RedisClusterBroker, error) {
	client := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: addrs, // seed nodes; go-redis will discover the rest

		// Retry up to 3 times on network errors before surfacing the error.
		// In a cluster, transient errors are common during failover elections
		// (which take ~1–5 s), so retries help absorb those blips.
		MaxRetries: 3,

		DialTimeout:  5 * time.Second, // how long to wait for TCP connection to a node
		ReadTimeout:  3 * time.Second, // max wait for a reply from a node
		WriteTimeout: 3 * time.Second, // max wait for data to be accepted by a node

		// PoolSize and MinIdleConns apply per-node, so with a 3-node cluster
		// and PoolSize:10 you get up to 30 total connections.
		PoolSize:     10,
		MinIdleConns: 5,

		IdleTimeout:        5 * time.Minute,
		IdleCheckFrequency: 1 * time.Minute, // how often idle connections are reaped
	})

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		// Fail fast — if the cluster is unreachable at startup we cannot queue tasks.
		return nil, fmt.Errorf("failed to connect to Redis cluster: %w", err)
	}

	b := &RedisClusterBroker{
		client:    client,
		streamKey: streamKey,
		groupName: groupName,
	}

	// Create the consumer group (and stream via MKSTREAM) before accepting tasks.
	if err := b.createConsumerGroup(ctx); err != nil {
		return nil, err
	}

	return b, nil
}

// createConsumerGroup issues XGROUP CREATE … MKSTREAM on the cluster.
// BUSYGROUP errors are silently ignored — they mean another instance already
// created the group, which is fine in a multi-replica deployment.
//
// Called by: NewRedisClusterBroker.
func (b *RedisClusterBroker) createConsumerGroup(ctx context.Context) error {
	err := b.client.XGroupCreateMkStream(ctx, b.streamKey, b.groupName, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		// Any error other than "already exists" is a real problem.
		return fmt.Errorf("failed to create consumer group: %w", err)
	}
	return nil
}

// Publish adds a task to the Redis Stream on the cluster.
//
// The ClusterClient automatically routes XADD to the shard that owns
// the hash slot for b.streamKey — no manual routing needed.
//
// Note: unlike the single-node broker, this implementation does NOT inject
// OTel trace context into the message values. Add tracing.InjectToMap if
// distributed tracing across the cluster boundary is needed.
//
// Parameters:
//   ctx  — request context for cancellation.
//   task — task to enqueue; marshalled to JSON before insertion.
//
// Returns:
//   error — if marshalling or XADD fails.
//
// Called by: API submit-task handler.
func (b *RedisClusterBroker) Publish(ctx context.Context, task *models.Task) error {
	taskJSON, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to marshal task: %w", err)
	}

	// XADD with ID "*" lets the cluster shard auto-generate a monotonic ID.
	// The ClusterClient handles MOVED/ASK redirects transparently if a slot
	// is migrating to a new node during re-sharding.
	_, err = b.client.XAdd(ctx, &redis.XAddArgs{
		Stream: b.streamKey,
		ID:     "*",
		Values: map[string]interface{}{
			"task":     taskJSON,    // full task JSON — self-contained for the consumer
			"priority": task.Priority, // stored separately for potential priority-aware routing
		},
	}).Result()
	if err != nil {
		return fmt.Errorf("failed to publish task to stream: %w", err)
	}

	return nil
}

// Subscribe consumes tasks from the Redis Stream on the cluster.
//
// Behaviour differences vs the single-node broker:
//   - No XClaim / PEL reclaim loop. Stalled message recovery would need the
//     same XPendingExt + XClaim logic from redis_stream.go; it is omitted here
//     but should be added for full production parity.
//   - Trace context is NOT extracted because this implementation does not inject
//     it in Publish.
//
// Parameters:
//   ctx           — cancellation; when cancelled the loop returns.
//   consumerGroup — group name; must match NewRedisClusterBroker groupName.
//   consumerName  — unique name for this goroutine (e.g. "worker-1").
//   handler       — called for each task; see TaskHandler type.
//
// Returns:
//   error — context cancellation or unrecoverable cluster error.
//
// Called by: cmd/worker/main.go processLoop.
func (b *RedisClusterBroker) Subscribe(ctx context.Context, consumerGroup, consumerName string, handler TaskHandler) error {
	for {
		select {
		case <-ctx.Done():
			// Graceful shutdown requested — exit the loop cleanly.
			return ctx.Err()
		default:
			// XREADGROUP ">" delivers new, undelivered messages to this consumer.
			// Block:2s means the call returns after 2 seconds even if no messages
			// arrive, keeping the shutdown signal responsive.
			streams, err := b.client.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    consumerGroup,
				Consumer: consumerName,
				Streams:  []string{b.streamKey, ">"}, // ">" = only undelivered messages
				Count:    1,                           // process one at a time
				Block:    2 * time.Second,
			}).Result()

			if err != nil {
				if err == redis.Nil {
					// Timeout with no messages — normal when queue is empty.
					continue
				}
				// Real cluster error (failover in progress, network partition …).
				return fmt.Errorf("failed to read from stream: %w", err)
			}

			for _, stream := range streams {
				for _, message := range stream.Messages {
					// Extract the raw task JSON from the message values map.
					taskData, ok := message.Values["task"].(string)
					if !ok {
						// Message is malformed — ACK and skip rather than blocking the queue.
						log.Printf("Invalid message format, skipping: %v", message.ID)
						b.client.XAck(ctx, b.streamKey, consumerGroup, message.ID)
						continue
					}

					// Deserialise JSON into a Task struct for type-safe handler access.
					var task models.Task
					if err := json.Unmarshal([]byte(taskData), &task); err != nil {
						log.Printf("Failed to unmarshal task: %v, error: %v", message.ID, err)
						b.client.XAck(ctx, b.streamKey, consumerGroup, message.ID)
						continue
					}

					// Call the worker's handler. Errors are logged but the message
					// is always acknowledged — retry logic is in the database layer.
					handlerErr := handler(ctx, &task)

					// XACK removes the message from the PEL regardless of handler success.
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

// Acknowledge explicitly ACKs a message by ID.
// Provided for callers that need to ACK outside the Subscribe loop.
//
// Parameters:
//   ctx       — request context.
//   streamKey — stream that contains the message.
//   messageID — the auto-generated Redis stream message ID.
//
// Called by: external callers that obtain a message ID directly.
func (b *RedisClusterBroker) Acknowledge(ctx context.Context, streamKey, messageID string) error {
	return b.client.XAck(ctx, streamKey, b.groupName, messageID).Err()
}

// GetPendingCount returns the number of unacknowledged (pending) messages in
// the consumer group's PEL. A high and rising count signals worker saturation
// or crashes.
//
// Called by: health checks, Prometheus metrics scraping.
func (b *RedisClusterBroker) GetPendingCount(ctx context.Context) (int64, error) {
	pending, err := b.client.XPending(ctx, b.streamKey, b.groupName).Result()
	if err != nil {
		return 0, err
	}
	return pending.Count, nil
}

// GetClusterClient returns the underlying *redis.ClusterClient.
// Callers that need direct cluster access (health pings, cluster-aware cache
// operations) use this instead of going through the Broker interface.
//
// Note: the Broker interface does not expose this method; callers must
// type-assert to *RedisClusterBroker to call it.
func (b *RedisClusterBroker) GetClusterClient() *redis.ClusterClient {
	return b.client
}

// Close releases all connections in the cluster client pool.
// Must be called during graceful shutdown.
//
// Called by: cmd/api/main.go and cmd/worker/main.go shutdown sequences.
func (b *RedisClusterBroker) Close() error {
	return b.client.Close()
}
