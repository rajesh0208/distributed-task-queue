// Package reliability — see circuitbreaker.go for package-level documentation.
//
// This file (deduplication.go) prevents the same task from being processed
// more than once, which is critical in a distributed system where retries,
// network partitions, and replayed messages can cause duplicate submissions.
//
// What problem does deduplication solve?
//   In a distributed system, "at-least-once" delivery is the norm — a message
//   broker guarantees it will be delivered, but may deliver it more than once
//   (e.g. if the worker ACKed the broker but crashed before updating the DB).
//   Without deduplication, a user who double-clicks "Submit" submits the same
//   image twice, wastes storage and compute, and gets two result emails.
//
// How it works:
//   1. The task payload is serialised to JSON and SHA-256 hashed.
//   2. The hash becomes a Redis key with a TTL (the deduplication window).
//   3. Before processing, check if the key exists (IsDuplicate).
//      If yes → skip; if no → proceed and MarkProcessed to set the key.
//   4. After the TTL expires, the same payload can be submitted again as new.
//
// Why SHA-256 of the JSON payload?
//   SHA-256 produces a fixed 32-byte (64 hex char) fingerprint of any input.
//   Two tasks with identical payloads get the same hash — this is the
//   idempotency key. Using JSON serialisation means field order matters;
//   callers should ensure canonical JSON if order varies.
//   SHA-256 is collision-resistant enough that accidental deduplication of
//   distinct tasks is computationally infeasible.
//
// Why idempotency matters for distributed systems:
//   An idempotent operation produces the same result regardless of how many
//   times it is applied. Deduplication enforces idempotency at the task level:
//   submitting the same task 10 times produces exactly one result.
//
// It connects to:
//   - Redis (DB 0 by default; callers pass the client)
//
// Called by:
//   - cmd/worker/main.go — checks IsDuplicate before processing each task
//   - cmd/api/main.go    — optionally checks before accepting a submission
package reliability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
)

// Deduplicator checks and records task fingerprints in Redis to prevent
// the same task from being processed more than once within the TTL window.
//
// Fields explained:
//
//	client — the Redis client used to store deduplication keys.
//	         Same client as the broker is fine; keys are namespaced with "dedup:".
//	ttl    — how long a fingerprint is remembered after MarkProcessed is called.
//	         Within this window, duplicate submissions are ignored.
//	         After TTL expires, the same payload can be resubmitted as fresh.
//	         Typical values: 24 h for user-facing deduplication windows,
//	         5 m for short-lived idempotency keys.
type Deduplicator struct {
	client *redis.Client
	ttl    time.Duration
}

// NewDeduplicator creates a Deduplicator backed by the given Redis client.
//
// Parameters:
//   redisClient — shared Redis client; the deduplicator uses its own key
//                 namespace ("dedup:…") so it does not conflict with other keys.
//   ttl         — how long to remember a processed task. After this duration,
//                 resubmitting the same payload is treated as a new task.
//
// Returns:
//   *Deduplicator — ready to use.
//
// Called by: cmd/worker/main.go, cmd/api/main.go.
func NewDeduplicator(redisClient *redis.Client, ttl time.Duration) *Deduplicator {
	return &Deduplicator{
		client: redisClient,
		ttl:    ttl,
	}
}

// GenerateKey produces a Redis key for the given task value by:
//  1. Serialising the task to canonical JSON.
//  2. Computing SHA-256 of the JSON bytes.
//  3. Returning "dedup:<hex-hash>".
//
// Why SHA-256?
//   It produces a deterministic 64-character hex string from any input.
//   Two tasks with identical content always produce the same key.
//   The probability of two distinct payloads colliding is 1/2^256 ≈ negligible.
//
// Parameters:
//   task — any JSON-serialisable value (typically a *models.Task or payload struct).
//
// Returns:
//   string — Redis key in the form "dedup:<64-char-hex>".
//   error  — if the task cannot be serialised to JSON.
//
// Called by: IsDuplicate, MarkProcessed, Clear (all in this file).
func (d *Deduplicator) GenerateKey(task interface{}) (string, error) {
	// Marshal to JSON first — this gives us a stable byte representation.
	// Note: if the task struct has map fields, JSON serialisation does not
	// guarantee field order within maps. Use sorted-key serialisation if needed.
	data, err := json.Marshal(task)
	if err != nil {
		return "", fmt.Errorf("failed to marshal task: %w", err)
	}

	// SHA-256 the JSON bytes to get a fixed-length fingerprint.
	// sha256.Sum256 returns [32]byte; hex.EncodeToString converts to 64 hex chars.
	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:]) // hash[:] converts [32]byte to []byte

	// Build the namespaced Redis key using strings.Builder to avoid allocations.
	// Pre-allocating the exact size ("dedup:" + 64 chars) avoids reallocation.
	var keyBuilder strings.Builder
	keyBuilder.Grow(len("dedup:") + len(hashStr))
	keyBuilder.WriteString("dedup:")
	keyBuilder.WriteString(hashStr)

	return keyBuilder.String(), nil
}

// IsDuplicate returns true if the task's fingerprint is already present in Redis,
// meaning this task was recently processed (within the TTL window).
//
// What happens when a duplicate is detected:
//   The caller (worker processLoop) skips the task entirely — it does not
//   requeue it, does not fail it, and does not update the DB. The task was
//   already processed successfully; this is just a redundant delivery.
//
// Parameters:
//   ctx  — request context; used for cancellation and timeout.
//   task — the task to check.
//
// Returns:
//   bool  — true = duplicate (skip this task), false = new task (process it).
//   error — if the Redis EXISTS command fails.
//
// Called by: cmd/worker/main.go processLoop before invoking the handler.
func (d *Deduplicator) IsDuplicate(ctx context.Context, task interface{}) (bool, error) {
	key, err := d.GenerateKey(task)
	if err != nil {
		return false, err
	}

	// EXISTS returns the count of matching keys (0 or 1 for a single key).
	exists, err := d.client.Exists(ctx, key).Result()
	if err != nil {
		// Redis failure — err on the side of allowing the task through rather
		// than silently dropping it. The worker may process a duplicate, but
		// at least it will not lose work.
		return false, fmt.Errorf("failed to check duplicate: %w", err)
	}

	return exists > 0, nil
}

// MarkProcessed records the task's fingerprint in Redis with the configured TTL.
// Must be called AFTER successfully processing the task (not before, to avoid
// marking a task as processed when the worker crashes mid-execution).
//
// The value stored is "1" — a minimal placeholder. What matters is the key's
// existence and TTL, not the value.
//
// Parameters:
//   ctx  — request context.
//   task — the just-processed task.
//
// Returns:
//   error — if the Redis SET command fails.
//
// Called by: cmd/worker/main.go after a successful task completion.
func (d *Deduplicator) MarkProcessed(ctx context.Context, task interface{}) error {
	key, err := d.GenerateKey(task)
	if err != nil {
		return err
	}

	// SET key "1" EX <ttl> — store the fingerprint with an automatic expiry.
	// After TTL seconds, the key disappears and the same payload can be
	// resubmitted as a brand-new task.
	err = d.client.Set(ctx, key, "1", d.ttl).Err()
	if err != nil {
		return fmt.Errorf("failed to mark as processed: %w", err)
	}

	return nil
}

// Clear explicitly removes a task's fingerprint from the deduplication cache
// before its TTL expires. Use this when you want to allow resubmission of a
// task that was previously processed — for example, after an admin forces a
// re-run of a specific task.
//
// Parameters:
//   ctx  — request context.
//   task — the task whose fingerprint should be cleared.
//
// Returns:
//   error — if the Redis DEL command fails.
//
// Called by: admin "re-run task" handler.
func (d *Deduplicator) Clear(ctx context.Context, task interface{}) error {
	key, err := d.GenerateKey(task)
	if err != nil {
		return err
	}

	err = d.client.Del(ctx, key).Err()
	if err != nil {
		return fmt.Errorf("failed to clear deduplication: %w", err)
	}

	return nil
}
