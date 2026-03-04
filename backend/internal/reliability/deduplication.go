// File: internal/reliability/deduplication.go
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

// Deduplicator prevents duplicate task processing
type Deduplicator struct {
	client *redis.Client
	ttl    time.Duration
}

// NewDeduplicator creates a new task deduplicator
func NewDeduplicator(redisClient *redis.Client, ttl time.Duration) *Deduplicator {
	return &Deduplicator{
		client: redisClient,
		ttl:    ttl,
	}
}

// GenerateKey generates a unique key for a task based on its content
func (d *Deduplicator) GenerateKey(task interface{}) (string, error) {
	data, err := json.Marshal(task)
	if err != nil {
		return "", fmt.Errorf("failed to marshal task: %w", err)
	}

	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])
	
	// Optimized key generation
	var keyBuilder strings.Builder
	keyBuilder.Grow(len("dedup:") + len(hashStr))
	keyBuilder.WriteString("dedup:")
	keyBuilder.WriteString(hashStr)
	
	return keyBuilder.String(), nil
}

// IsDuplicate checks if a task has been processed before
func (d *Deduplicator) IsDuplicate(ctx context.Context, task interface{}) (bool, error) {
	key, err := d.GenerateKey(task)
	if err != nil {
		return false, err
	}

	exists, err := d.client.Exists(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check duplicate: %w", err)
	}

	return exists > 0, nil
}

// MarkProcessed marks a task as processed
func (d *Deduplicator) MarkProcessed(ctx context.Context, task interface{}) error {
	key, err := d.GenerateKey(task)
	if err != nil {
		return err
	}

	err = d.client.Set(ctx, key, "1", d.ttl).Err()
	if err != nil {
		return fmt.Errorf("failed to mark as processed: %w", err)
	}

	return nil
}

// Clear removes a task from the deduplication cache
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

