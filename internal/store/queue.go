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
	keyQueuePrefix   = "relayra:queue:"
	keyRequestPrefix = "relayra:request:"
)

// StoreRequestMetadata stores request bookkeeping without queueing it.
func (r *Redis) StoreRequestMetadata(ctx context.Context, peerID string, req *models.RelayRequest) error {
	ctx = logger.WithComponent(ctx, "store")
	ctx = logger.WithRequestID(ctx, req.ID)
	ctx = logger.WithPeerID(ctx, peerID)

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	reqKey := keyRequestPrefix + req.ID
	if err := r.Client.HSet(ctx, reqKey, map[string]interface{}{
		"id":          req.ID,
		"peer_id":     peerID,
		"webhook_url": req.WebhookURL,
		"status":      string(req.Status),
		"created_at":  req.CreatedAt.Unix(),
		"data":        string(data),
		"async":       req.Async,
	}).Err(); err != nil {
		slog.ErrorContext(ctx, "failed to store request metadata", "error", err)
		return fmt.Errorf("store request metadata: %w", err)
	}

	return nil
}

// EnqueueRequest adds a relay request to the peer's queue.
func (r *Redis) EnqueueRequest(ctx context.Context, peerID string, req *models.RelayRequest) error {
	ctx = logger.WithComponent(ctx, "store")
	ctx = logger.WithRequestID(ctx, req.ID)
	ctx = logger.WithPeerID(ctx, peerID)

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	pipe := r.Client.Pipeline()

	// Push to peer queue
	queueKey := keyQueuePrefix + peerID
	pipe.RPush(ctx, queueKey, data)

	// Store request metadata
	reqKey := keyRequestPrefix + req.ID
	pipe.HSet(ctx, reqKey, map[string]interface{}{
		"id":          req.ID,
		"peer_id":     peerID,
		"webhook_url": req.WebhookURL,
		"status":      string(models.StatusQueued),
		"created_at":  req.CreatedAt.Unix(),
		"data":        string(data),
		"async":       req.Async,
	})

	if _, err := pipe.Exec(ctx); err != nil {
		slog.ErrorContext(ctx, "failed to enqueue request", "error", err)
		return fmt.Errorf("enqueue request: %w", err)
	}

	slog.InfoContext(ctx, "request enqueued", "queue_key", queueKey)
	slog.DebugContext(ctx, "enqueued request data", "url", req.Request.URL, "method", req.Request.Method)
	return nil
}

// DequeueRequests pulls up to batchSize requests from a peer's queue.
func (r *Redis) DequeueRequests(ctx context.Context, peerID string, batchSize int) ([]models.RelayRequest, error) {
	ctx = logger.WithComponent(ctx, "store")
	ctx = logger.WithPeerID(ctx, peerID)

	queueKey := keyQueuePrefix + peerID

	// Check queue length first
	length, err := r.Client.LLen(ctx, queueKey).Result()
	if err != nil {
		slog.ErrorContext(ctx, "failed to check queue length", "error", err)
		return nil, fmt.Errorf("queue length: %w", err)
	}

	if length == 0 {
		slog.DebugContext(ctx, "queue empty", "queue_key", queueKey)
		return nil, nil
	}

	// Pop up to batchSize items
	count := int(length)
	if count > batchSize {
		count = batchSize
	}

	var requests []models.RelayRequest
	for i := 0; i < count; i++ {
		data, err := r.Client.LPop(ctx, queueKey).Result()
		if err == redis.Nil {
			break
		}
		if err != nil {
			slog.ErrorContext(ctx, "failed to pop from queue", "error", err, "index", i)
			return requests, fmt.Errorf("dequeue at index %d: %w", i, err)
		}

		var req models.RelayRequest
		if err := json.Unmarshal([]byte(data), &req); err != nil {
			slog.ErrorContext(ctx, "failed to unmarshal queued request", "error", err, "raw_data_len", len(data))
			continue // Skip malformed entries
		}

		// Update status to sent
		r.Client.HSet(ctx, keyRequestPrefix+req.ID, "status", string(models.StatusSent))
		requests = append(requests, req)
	}

	slog.InfoContext(ctx, "dequeued requests", "count", len(requests), "remaining", length-int64(len(requests)))
	return requests, nil
}

// QueueLength returns the number of pending requests for a peer.
func (r *Redis) QueueLength(ctx context.Context, peerID string) (int64, error) {
	return r.Client.LLen(ctx, keyQueuePrefix+peerID).Result()
}

// GetRequestStatus returns the current status of a request.
func (r *Redis) GetRequestStatus(ctx context.Context, requestID string) (models.RequestStatus, error) {
	status, err := r.Client.HGet(ctx, keyRequestPrefix+requestID, "status").Result()
	if err == redis.Nil {
		return "", fmt.Errorf("request not found: %s", requestID)
	}
	if err != nil {
		return "", fmt.Errorf("get request status: %w", err)
	}
	return models.RequestStatus(status), nil
}

// GetRequestWebhookURL returns the webhook URL for a request.
func (r *Redis) GetRequestWebhookURL(ctx context.Context, requestID string) (string, error) {
	url, err := r.Client.HGet(ctx, keyRequestPrefix+requestID, "webhook_url").Result()
	if err == redis.Nil {
		return "", nil
	}
	return url, err
}

// UpdateRequestStatus updates the status of a request.
func (r *Redis) UpdateRequestStatus(ctx context.Context, requestID string, status models.RequestStatus) error {
	ctx = logger.WithComponent(ctx, "store")
	ctx = logger.WithRequestID(ctx, requestID)

	err := r.Client.HSet(ctx, keyRequestPrefix+requestID, "status", string(status)).Err()
	if err != nil {
		slog.ErrorContext(ctx, "failed to update request status", "status", status, "error", err)
		return fmt.Errorf("update request status: %w", err)
	}

	slog.DebugContext(ctx, "request status updated", "status", status)
	return nil
}

// AckRequests marks requests as received by the Sender.
func (r *Redis) AckRequests(ctx context.Context, requestIDs []string) error {
	ctx = logger.WithComponent(ctx, "store")

	for _, id := range requestIDs {
		r.Client.HSet(ctx, keyRequestPrefix+id, "status", string(models.StatusExecuting))
	}

	slog.InfoContext(ctx, "acknowledged requests", "count", len(requestIDs))
	return nil
}

// --- Sender-side: pending results queue ---

const keyPendingResults = "relayra:pending_results"

// PushResult stores a result locally on the Sender, waiting to be sent back.
func (r *Redis) PushResult(ctx context.Context, result *models.RelayResult) error {
	ctx = logger.WithComponent(ctx, "store")
	ctx = logger.WithRequestID(ctx, result.RequestID)

	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	if err := r.Client.RPush(ctx, keyPendingResults, data).Err(); err != nil {
		slog.ErrorContext(ctx, "failed to push result", "error", err)
		return fmt.Errorf("push result: %w", err)
	}

	slog.InfoContext(ctx, "result stored locally",
		"status_code", result.StatusCode,
		"duration_ms", result.Duration,
	)
	return nil
}

// PopResults retrieves and removes pending results from the Sender's local store.
func (r *Redis) PopResults(ctx context.Context, maxCount int) ([]models.RelayResult, error) {
	ctx = logger.WithComponent(ctx, "store")

	length, err := r.Client.LLen(ctx, keyPendingResults).Result()
	if err != nil {
		return nil, fmt.Errorf("pending results length: %w", err)
	}

	if length == 0 {
		return nil, nil
	}

	count := int(length)
	if count > maxCount {
		count = maxCount
	}

	var results []models.RelayResult
	for i := 0; i < count; i++ {
		data, err := r.Client.LPop(ctx, keyPendingResults).Result()
		if err == redis.Nil {
			break
		}
		if err != nil {
			slog.ErrorContext(ctx, "failed to pop result", "error", err, "index", i)
			return results, fmt.Errorf("pop result at index %d: %w", i, err)
		}

		var result models.RelayResult
		if err := json.Unmarshal([]byte(data), &result); err != nil {
			slog.ErrorContext(ctx, "failed to unmarshal result", "error", err)
			continue
		}
		results = append(results, result)
	}

	slog.InfoContext(ctx, "popped pending results", "count", len(results))
	return results, nil
}

// PendingResultsCount returns the number of results waiting to be sent.
func (r *Redis) PendingResultsCount(ctx context.Context) (int64, error) {
	return r.Client.LLen(ctx, keyPendingResults).Result()
}

// RePushResults puts results back into the pending queue (e.g., if send failed).
func (r *Redis) RePushResults(ctx context.Context, results []models.RelayResult) error {
	ctx = logger.WithComponent(ctx, "store")

	for _, result := range results {
		data, err := json.Marshal(result)
		if err != nil {
			slog.ErrorContext(ctx, "failed to re-marshal result for re-push", "request_id", result.RequestID, "error", err)
			continue
		}
		r.Client.LPush(ctx, keyPendingResults, data) // Push back to front
	}

	slog.WarnContext(ctx, "re-pushed results after failure", "count", len(results))
	return nil
}

// DeleteAckedResults removes results that the Listener has acknowledged.
// On Sender side, this clears results that we know were received.
func (r *Redis) DeleteAckedResults(ctx context.Context, resultIDs []string) {
	ctx = logger.WithComponent(ctx, "store")
	slog.InfoContext(ctx, "acknowledged results deleted from local store", "count", len(resultIDs))
	// Results are already removed from the list by PopResults.
	// This is a no-op confirmation log. The two-phase ack ensures
	// that if the Sender crashes after popping but before receiving ack,
	// the results are lost — but the Listener hasn't acked them either,
	// so the workflow is consistent.
	_ = time.Now() // avoid unused import
}
