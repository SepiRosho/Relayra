package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/models"
)

const (
	keyResultPrefix = "relayra:result:"
)

// StoreResult saves a relay result with a TTL (in seconds).
func (r *Redis) StoreResult(ctx context.Context, result *models.RelayResult, ttlSeconds int) error {
	ctx = logger.WithComponent(ctx, "store")
	ctx = logger.WithRequestID(ctx, result.RequestID)

	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	resultKey := keyResultPrefix + result.RequestID
	ttl := time.Duration(ttlSeconds) * time.Second

	pipe := r.Client.Pipeline()
	pipe.Set(ctx, resultKey, data, ttl)

	// Also update request status to completed and remove it from the durable queue.
	reqKey := keyRequestPrefix + result.RequestID
	pipe.HSet(ctx, reqKey, "status", string(models.StatusCompleted))
	peerID := ""
	if values, err := r.Client.HMGet(ctx, reqKey, "peer_id").Result(); err == nil && len(values) == 1 && values[0] != nil {
		if v, ok := values[0].(string); ok {
			peerID = v
		}
	}
	if peerID != "" {
		pipe.LRem(ctx, keyQueuePrefix+peerID, 0, result.RequestID)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		slog.ErrorContext(ctx, "failed to store result", "error", err)
		return fmt.Errorf("store result: %w", err)
	}

	slog.InfoContext(ctx, "result stored",
		"status_code", result.StatusCode,
		"ttl_seconds", ttlSeconds,
		"body_size", len(result.Body),
	)
	return nil
}

// GetResult retrieves a stored result by request ID.
func (r *Redis) GetResult(ctx context.Context, requestID string) (*models.RelayResult, error) {
	ctx = logger.WithComponent(ctx, "store")
	ctx = logger.WithRequestID(ctx, requestID)

	resultKey := keyResultPrefix + requestID
	data, err := r.Client.Get(ctx, resultKey).Result()
	if err == redis.Nil {
		slog.DebugContext(ctx, "result not found")
		return nil, nil // Not found
	}
	if err != nil {
		slog.ErrorContext(ctx, "failed to get result", "error", err)
		return nil, fmt.Errorf("get result: %w", err)
	}

	var result models.RelayResult
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	slog.DebugContext(ctx, "result retrieved", "status_code", result.StatusCode)
	return &result, nil
}

// ResultExists checks if a result exists for the given request ID.
func (r *Redis) ResultExists(ctx context.Context, requestID string) (bool, error) {
	exists, err := r.Client.Exists(ctx, keyResultPrefix+requestID).Result()
	if err != nil {
		return false, fmt.Errorf("check result exists: %w", err)
	}
	return exists > 0, nil
}

// UpdateResultWebhookStatus updates the webhook delivery status metadata on the request.
func (r *Redis) UpdateResultWebhookStatus(ctx context.Context, requestID string, status models.ResultStatus) error {
	ctx = logger.WithComponent(ctx, "store")
	ctx = logger.WithRequestID(ctx, requestID)

	reqKey := keyRequestPrefix + requestID
	if err := r.Client.HSet(ctx, reqKey, "webhook_status", string(status)).Err(); err != nil {
		slog.ErrorContext(ctx, "failed to update webhook status", "status", status, "error", err)
		return fmt.Errorf("update webhook status: %w", err)
	}

	slog.InfoContext(ctx, "webhook status updated", "webhook_status", status)
	return nil
}

// GetResultTTL returns the remaining TTL for a result in seconds.
func (r *Redis) GetResultTTL(ctx context.Context, requestID string) (int64, error) {
	dur, err := r.Client.TTL(ctx, keyResultPrefix+requestID).Result()
	if err != nil {
		return 0, fmt.Errorf("get result TTL: %w", err)
	}
	return int64(dur.Seconds()), nil
}
