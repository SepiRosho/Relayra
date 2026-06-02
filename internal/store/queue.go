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
		"id":                   req.ID,
		"peer_id":              peerID,
		"webhook_url":          req.WebhookURL,
		"status":               string(req.Status),
		"created_at":           req.CreatedAt.Unix(),
		"data":                 string(data),
		"async":                req.Async,
		requestLeaseUntilField: 0,
	}).Err(); err != nil {
		slog.ErrorContext(ctx, "failed to store request metadata", "error", err)
		return fmt.Errorf("store request metadata: %w", err)
	}

	return nil
}

// EnqueueRequest adds a relay request to the peer's durable queue.
func (r *Redis) EnqueueRequest(ctx context.Context, peerID string, req *models.RelayRequest) error {
	ctx = logger.WithComponent(ctx, "store")
	ctx = logger.WithRequestID(ctx, req.ID)
	ctx = logger.WithPeerID(ctx, peerID)

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	pipe := r.Client.Pipeline()
	queueKey := keyQueuePrefix + peerID
	reqKey := keyRequestPrefix + req.ID

	pipe.RPush(ctx, queueKey, req.ID)
	pipe.HSet(ctx, reqKey, map[string]interface{}{
		"id":                   req.ID,
		"peer_id":              peerID,
		"webhook_url":          req.WebhookURL,
		"status":               string(models.StatusQueued),
		"created_at":           req.CreatedAt.Unix(),
		"data":                 string(data),
		"async":                req.Async,
		requestLeaseUntilField: 0,
	})

	if _, err := pipe.Exec(ctx); err != nil {
		slog.ErrorContext(ctx, "failed to enqueue request", "error", err)
		return fmt.Errorf("enqueue request: %w", err)
	}

	slog.InfoContext(ctx, "request enqueued", "queue_key", queueKey)
	return nil
}

// DequeueRequests is kept as a compatibility wrapper around LeaseRequests.
func (r *Redis) DequeueRequests(ctx context.Context, peerID string, batchSize int) ([]models.RelayRequest, error) {
	return r.LeaseRequests(ctx, peerID, batchSize, 30*time.Second)
}

// ClearPeerQueue removes queued-only requests for a peer without touching leased work.
func (r *Redis) ClearPeerQueue(ctx context.Context, peerID string) (int64, error) {
	queueKey := keyQueuePrefix + peerID
	ids, err := r.Client.LRange(ctx, queueKey, 0, -1).Result()
	if err != nil {
		return 0, fmt.Errorf("list queue for clear: %w", err)
	}

	var cleared int64
	for _, id := range ids {
		reqKey := keyRequestPrefix + id
		values, err := r.Client.HMGet(ctx, reqKey, "status").Result()
		if err != nil {
			return cleared, fmt.Errorf("lookup queued request %s: %w", id, err)
		}
		if len(values) != 1 || values[0] == nil {
			continue
		}

		status, _ := values[0].(string)
		if models.RequestStatus(status) != models.StatusQueued {
			continue
		}

		pipe := r.Client.Pipeline()
		pipe.LRem(ctx, queueKey, 0, id)
		pipe.HSet(ctx, reqKey, "status", string(models.StatusFailed))
		pipe.Del(ctx, keyChunkCursorPrefix+models.RequestTransferID(id))
		if _, err := pipe.Exec(ctx); err != nil {
			return cleared, fmt.Errorf("clear queued request %s: %w", id, err)
		}
		cleared++
	}

	return cleared, nil
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

// GetRequest retrieves a stored relay request by ID.
func (r *Redis) GetRequest(ctx context.Context, requestID string) (*models.RelayRequest, error) {
	req, _, err := r.loadRequestEnvelope(ctx, requestID)
	if err != nil {
		return nil, err
	}
	return req, nil
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
	return nil
}

// AckRequests is kept as a compatibility wrapper.
func (r *Redis) AckRequests(ctx context.Context, requestIDs []string) error {
	now := time.Now()
	states := make([]models.RequestSyncState, 0, len(requestIDs))
	for _, id := range requestIDs {
		states = append(states, models.RequestSyncState{
			RequestID:  id,
			Status:     models.StatusReceived,
			LeaseUntil: now.Add(30 * time.Second),
			UpdatedAt:  now,
		})
	}
	return r.ApplyRequestStates(ctx, states)
}

// PushResult stores a result locally on the Sender, waiting to be sent back.
func (r *Redis) PushResult(ctx context.Context, result *models.RelayResult) error {
	ctx = logger.WithComponent(ctx, "store")
	ctx = logger.WithRequestID(ctx, result.RequestID)

	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	pipe := r.Client.Pipeline()
	pipe.ZAdd(ctx, keyPendingResultsSet, redis.Z{Score: float64(time.Now().UnixMilli()), Member: result.RequestID})
	pipe.HSet(ctx, keyPendingResultPrefix+result.RequestID, map[string]interface{}{
		"data":                  string(data),
		resultStatusField:       string(models.ResultPending),
		resultLeaseUntilField:   0,
		senderStateUpdatedField: time.Now().Unix(),
	})
	if _, err := pipe.Exec(ctx); err != nil {
		slog.ErrorContext(ctx, "failed to push result", "error", err)
		return fmt.Errorf("push result: %w", err)
	}

	return nil
}

// PopResults is kept as a compatibility wrapper around LeaseResults.
func (r *Redis) PopResults(ctx context.Context, maxCount int) ([]models.RelayResult, error) {
	return r.LeaseResults(ctx, maxCount, 30*time.Second)
}

// PendingResultsCount returns the number of results waiting to be sent.
func (r *Redis) PendingResultsCount(ctx context.Context) (int64, error) {
	return r.Client.ZCard(ctx, keyPendingResultsSet).Result()
}

// RePushResults resets the delivery_status of leased results back to pending so
// they can be picked up immediately by the next poll cycle. Call this after a
// poll cycle fails so that results leased for that cycle are not stuck behind
// their lease TTL.
func (r *Redis) RePushResults(ctx context.Context, results []models.RelayResult) error {
	if len(results) == 0 {
		return nil
	}
	pipe := r.Client.Pipeline()
	for _, result := range results {
		resultKey := keyPendingResultPrefix + result.RequestID
		pipe.HSet(ctx, resultKey, map[string]interface{}{
			resultStatusField:     string(models.ResultPending),
			resultLeaseUntilField: 0,
		})
	}
	_, err := pipe.Exec(ctx)
	return err
}

// DeleteAckedResults removes results that the Listener has acknowledged.
func (r *Redis) DeleteAckedResults(ctx context.Context, resultIDs []string) {
	_ = r.AckResults(ctx, resultIDs)
}
