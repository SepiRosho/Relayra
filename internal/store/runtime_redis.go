package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/relayra/relayra/internal/models"
)

const (
	keyPendingResultsSet     = "relayra:pending_results"
	keyPendingResultPrefix   = "relayra:pending_result:"
	keySenderRequestSet      = "relayra:sender_requests"
	keySenderRequestPrefix   = "relayra:sender_request:"
	requestLeaseUntilField   = "lease_until"
	resultStatusField        = "delivery_status"
	resultLeaseUntilField    = "lease_until"
	senderStateStatusField   = "status"
	senderStateLeaseField    = "lease_until"
	senderStateUpdatedField  = "updated_at"
)

func (r *Redis) LeaseRequests(ctx context.Context, peerID string, batchSize int, leaseTTL time.Duration) ([]models.RelayRequest, error) {
	queueKey := keyQueuePrefix + peerID
	ids, err := r.Client.LRange(ctx, queueKey, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("lease requests list: %w", err)
	}

	now := time.Now()
	leaseUntil := now.Add(leaseTTL).Unix()
	requests := make([]models.RelayRequest, 0, batchSize)

	for _, id := range ids {
		if len(requests) >= batchSize {
			break
		}

		reqKey := keyRequestPrefix + id
		data, err := r.Client.HGetAll(ctx, reqKey).Result()
		if err == redis.Nil || len(data) == 0 {
			_ = r.Client.LRem(ctx, queueKey, 0, id).Err()
			continue
		}
		if err != nil {
			return requests, fmt.Errorf("get request metadata: %w", err)
		}

		status := models.RequestStatus(data["status"])
		if status == models.StatusCompleted {
			_ = r.Client.LRem(ctx, queueKey, 0, id).Err()
			continue
		}

		currentLease, _ := r.Client.HGet(ctx, reqKey, requestLeaseUntilField).Int64()
		if currentLease > now.Unix() {
			continue
		}

		raw := data["data"]
		var req models.RelayRequest
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			continue
		}

		req.Status = models.StatusLeased
		if err := r.Client.HSet(ctx, reqKey, map[string]interface{}{
			"status":      string(models.StatusLeased),
			requestLeaseUntilField: leaseUntil,
			"data":        mustJSON(req),
		}).Err(); err != nil {
			return requests, fmt.Errorf("lease request: %w", err)
		}

		requests = append(requests, req)
	}

	return requests, nil
}

func (r *Redis) ApplyRequestStates(ctx context.Context, requestStates []models.RequestSyncState) error {
	now := time.Now().Unix()
	for _, state := range requestStates {
		reqKey := keyRequestPrefix + state.RequestID
		exists, err := r.Client.Exists(ctx, reqKey).Result()
		if err != nil || exists == 0 {
			if err != nil {
				return fmt.Errorf("lookup request state %s: %w", state.RequestID, err)
			}
			continue
		}

		status := state.Status
		if status == models.StatusCompleted {
			resultExists, err := r.ResultExists(ctx, state.RequestID)
			if err != nil {
				return err
			}
			if !resultExists {
				status = models.StatusExecuting
			}
		}

		leaseUntil := now
		if !state.LeaseUntil.IsZero() {
			leaseUntil = state.LeaseUntil.Unix()
		}

		if err := r.Client.HSet(ctx, reqKey, map[string]interface{}{
			"status":      string(status),
			requestLeaseUntilField: leaseUntil,
		}).Err(); err != nil {
			return fmt.Errorf("apply request state %s: %w", state.RequestID, err)
		}
	}
	return nil
}

func (r *Redis) LeaseResults(ctx context.Context, maxCount int, leaseTTL time.Duration) ([]models.RelayResult, error) {
	ids, err := r.Client.ZRange(ctx, keyPendingResultsSet, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("lease results list: %w", err)
	}

	now := time.Now()
	leaseUntil := now.Add(leaseTTL).Unix()
	results := make([]models.RelayResult, 0, maxCount)

	for _, id := range ids {
		if len(results) >= maxCount {
			break
		}

		resultKey := keyPendingResultPrefix + id
		data, err := r.Client.HGetAll(ctx, resultKey).Result()
		if err == redis.Nil || len(data) == 0 {
			_ = r.Client.ZRem(ctx, keyPendingResultsSet, id).Err()
			continue
		}
		if err != nil {
			return results, fmt.Errorf("get pending result: %w", err)
		}

		deliveryStatus := models.ResultDeliveryStatus(data[resultStatusField])
		currentLease, _ := r.Client.HGet(ctx, resultKey, resultLeaseUntilField).Int64()
		if deliveryStatus == models.ResultLeased && currentLease > now.Unix() {
			continue
		}

		var result models.RelayResult
		if err := json.Unmarshal([]byte(data["data"]), &result); err != nil {
			continue
		}

		if err := r.Client.HSet(ctx, resultKey, map[string]interface{}{
			resultStatusField:     string(models.ResultLeased),
			resultLeaseUntilField: leaseUntil,
		}).Err(); err != nil {
			return results, fmt.Errorf("lease result %s: %w", id, err)
		}

		results = append(results, result)
	}

	return results, nil
}

func (r *Redis) AckResults(ctx context.Context, resultIDs []string) error {
	if len(resultIDs) == 0 {
		return nil
	}

	pipe := r.Client.Pipeline()
	for _, id := range resultIDs {
		pipe.ZRem(ctx, keyPendingResultsSet, id)
		pipe.Del(ctx, keyPendingResultPrefix+id)
		pipe.SRem(ctx, keySenderRequestSet, id)
		pipe.Del(ctx, keySenderRequestPrefix+id)
	}
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("ack results: %w", err)
	}
	return nil
}

func (r *Redis) ResultPending(ctx context.Context, requestID string) (bool, error) {
	exists, err := r.Client.Exists(ctx, keyPendingResultPrefix+requestID).Result()
	if err != nil {
		return false, fmt.Errorf("check pending result %s: %w", requestID, err)
	}
	return exists > 0, nil
}

func (r *Redis) StoreSenderRequestState(ctx context.Context, state *models.RequestSyncState) error {
	if state == nil {
		return nil
	}

	updatedAt := state.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}

	leaseUntil := int64(0)
	if !state.LeaseUntil.IsZero() {
		leaseUntil = state.LeaseUntil.Unix()
	}

	pipe := r.Client.Pipeline()
	pipe.SAdd(ctx, keySenderRequestSet, state.RequestID)
	pipe.HSet(ctx, keySenderRequestPrefix+state.RequestID, map[string]interface{}{
		senderStateStatusField:  string(state.Status),
		senderStateLeaseField:   leaseUntil,
		senderStateUpdatedField: updatedAt.Unix(),
	})
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("store sender request state %s: %w", state.RequestID, err)
	}
	return nil
}

func (r *Redis) GetSenderRequestState(ctx context.Context, requestID string) (*models.RequestSyncState, error) {
	data, err := r.Client.HGetAll(ctx, keySenderRequestPrefix+requestID).Result()
	if err == redis.Nil || len(data) == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get sender request state %s: %w", requestID, err)
	}

	return parseSenderRequestState(requestID, data)
}

func (r *Redis) ListSenderRequestStates(ctx context.Context) ([]models.RequestSyncState, error) {
	ids, err := r.Client.SMembers(ctx, keySenderRequestSet).Result()
	if err != nil {
		return nil, fmt.Errorf("list sender request state ids: %w", err)
	}

	states := make([]models.RequestSyncState, 0, len(ids))
	for _, id := range ids {
		state, err := r.GetSenderRequestState(ctx, id)
		if err != nil {
			return nil, err
		}
		if state != nil {
			states = append(states, *state)
		}
	}
	return states, nil
}

func (r *Redis) DeleteSenderRequestStates(ctx context.Context, requestIDs []string) error {
	if len(requestIDs) == 0 {
		return nil
	}

	pipe := r.Client.Pipeline()
	for _, id := range requestIDs {
		pipe.SRem(ctx, keySenderRequestSet, id)
		pipe.Del(ctx, keySenderRequestPrefix+id)
	}
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete sender request states: %w", err)
	}
	return nil
}

func parseSenderRequestState(requestID string, data map[string]string) (*models.RequestSyncState, error) {
	state := &models.RequestSyncState{
		RequestID: requestID,
		Status:    models.RequestStatus(data[senderStateStatusField]),
	}

	if lease, ok := data[senderStateLeaseField]; ok && lease != "" {
		leaseUnix, err := strconv.ParseInt(lease, 10, 64)
		if err != nil {
			return nil, err
		}
		if leaseUnix > 0 {
			state.LeaseUntil = time.Unix(leaseUnix, 0)
		}
	}
	if updated, ok := data[senderStateUpdatedField]; ok && updated != "" {
		updatedUnix, err := strconv.ParseInt(updated, 10, 64)
		if err != nil {
			return nil, err
		}
		if updatedUnix > 0 {
			state.UpdatedAt = time.Unix(updatedUnix, 0)
		}
	}

	return state, nil
}

func mustJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}
