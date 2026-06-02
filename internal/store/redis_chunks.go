package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/relayra/relayra/internal/models"
)

const (
	keyChunkReceiptSet    = "relayra:chunk_receipts"
	keyChunkReceiptPrefix = "relayra:chunk_receipt:"
	keyChunkCursorPrefix  = "relayra:chunk_cursor:"
	keyInboundChunkPrefix = "relayra:inbound_chunk:"
)

func (r *Redis) StoreInboundChunk(ctx context.Context, chunk models.TransportChunk, ttl time.Duration) (*models.ChunkReceipt, *models.RelayRequest, error) {
	if chunk.TransferID == "" || chunk.RequestID == "" {
		return nil, nil, fmt.Errorf("chunk transfer_id and request_id are required")
	}

	stateKey := keyInboundChunkPrefix + chunk.TransferID
	state, err := r.Client.HGetAll(ctx, stateKey).Result()
	if err != nil && err != redis.Nil {
		return nil, nil, fmt.Errorf("load inbound chunk state: %w", err)
	}

	nextIndex := 0
	assembledData := ""
	if len(state) > 0 {
		nextIndex, _ = strconv.Atoi(state["next_index"])
		assembledData = state["data"]
		if state["checksum"] != "" && state["checksum"] != chunk.Checksum {
			return r.resetInboundChunk(ctx, chunk, "checksum changed")
		}
		if state["total"] != "" {
			total, _ := strconv.Atoi(state["total"])
			if total != chunk.Total {
				return r.resetInboundChunk(ctx, chunk, "chunk total changed")
			}
		}
	}

	if chunk.Index < nextIndex {
		receipt := models.ChunkReceipt{
			TransferID: chunk.TransferID,
			RequestID:  chunk.RequestID,
			NextIndex:  nextIndex,
			Completed:  nextIndex >= chunk.Total,
		}
		if err := r.storeChunkReceipt(ctx, receipt); err != nil {
			return nil, nil, err
		}
		return &receipt, nil, nil
	}
	if chunk.Index > nextIndex {
		return r.resetInboundChunk(ctx, chunk, fmt.Sprintf("out-of-order chunk index %d expected %d", chunk.Index, nextIndex))
	}

	payloadBytes, err := base64.StdEncoding.DecodeString(chunk.Payload)
	if err != nil {
		return r.resetInboundChunk(ctx, chunk, "invalid chunk payload")
	}

	assembledData += string(payloadBytes)
	nextIndex++

	pipe := r.Client.Pipeline()
	pipe.HSet(ctx, stateKey, map[string]interface{}{
		"request_id": chunk.RequestID,
		"kind":       chunk.Kind,
		"next_index": nextIndex,
		"total":      chunk.Total,
		"checksum":   chunk.Checksum,
		"total_size": chunk.TotalSize,
		"data":       assembledData,
		"updated_at": time.Now().Unix(),
	})
	pipe.Expire(ctx, stateKey, ttl)

	receipt := models.ChunkReceipt{
		TransferID: chunk.TransferID,
		RequestID:  chunk.RequestID,
		NextIndex:  nextIndex,
		Completed:  nextIndex >= chunk.Total,
	}
	if err := r.queueChunkReceipt(ctx, pipe, receipt); err != nil {
		return nil, nil, err
	}

	if nextIndex < chunk.Total {
		if _, err := pipe.Exec(ctx); err != nil {
			return nil, nil, fmt.Errorf("store inbound chunk: %w", err)
		}
		return &receipt, nil, nil
	}

	if len(assembledData) != chunk.TotalSize {
		return r.resetInboundChunk(ctx, chunk, "reassembled payload size mismatch")
	}
	if models.SHA256Hex([]byte(assembledData)) != chunk.Checksum {
		return r.resetInboundChunk(ctx, chunk, "reassembled payload checksum mismatch")
	}

	var req models.RelayRequest
	if err := json.Unmarshal([]byte(assembledData), &req); err != nil {
		return r.resetInboundChunk(ctx, chunk, "invalid reassembled request payload")
	}

	pipe.Del(ctx, stateKey)
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, nil, fmt.Errorf("finalize inbound chunk: %w", err)
	}

	return &receipt, &req, nil
}

func (r *Redis) StoreInboundResultChunk(ctx context.Context, chunk models.TransportChunk, ttl time.Duration) (*models.RelayResult, error) {
	stateKey := keyInboundChunkPrefix + chunk.TransferID
	state, err := r.Client.HGetAll(ctx, stateKey).Result()
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("load inbound result chunk state: %w", err)
	}

	nextIndex := 0
	assembledData := ""
	if len(state) > 0 {
		nextIndex, _ = strconv.Atoi(state["next_index"])
		assembledData = state["data"]
		if state["checksum"] != "" && state["checksum"] != chunk.Checksum {
			r.Client.Del(ctx, stateKey)
			return nil, fmt.Errorf("result chunk checksum changed for transfer %s", chunk.TransferID)
		}
		if state["total"] != "" {
			total, _ := strconv.Atoi(state["total"])
			if total != chunk.Total {
				r.Client.Del(ctx, stateKey)
				return nil, fmt.Errorf("result chunk total changed for transfer %s", chunk.TransferID)
			}
		}
	}

	if chunk.Index < nextIndex {
		return nil, nil
	}
	if chunk.Index > nextIndex {
		r.Client.Del(ctx, stateKey)
		return nil, fmt.Errorf("out-of-order result chunk index %d expected %d", chunk.Index, nextIndex)
	}

	payloadBytes, err := base64.StdEncoding.DecodeString(chunk.Payload)
	if err != nil {
		return nil, fmt.Errorf("invalid result chunk payload: %w", err)
	}

	assembledData += string(payloadBytes)
	nextIndex++

	pipe := r.Client.Pipeline()
	pipe.HSet(ctx, stateKey, map[string]interface{}{
		"request_id": chunk.RequestID,
		"kind":       chunk.Kind,
		"next_index": nextIndex,
		"total":      chunk.Total,
		"checksum":   chunk.Checksum,
		"total_size": chunk.TotalSize,
		"data":       assembledData,
		"updated_at": time.Now().Unix(),
	})
	pipe.Expire(ctx, stateKey, ttl)

	if nextIndex < chunk.Total {
		if _, err := pipe.Exec(ctx); err != nil {
			return nil, fmt.Errorf("store inbound result chunk: %w", err)
		}
		return nil, nil
	}

	if len(assembledData) != chunk.TotalSize {
		r.Client.Del(ctx, stateKey)
		return nil, fmt.Errorf("result chunk reassembly size mismatch: got %d want %d", len(assembledData), chunk.TotalSize)
	}
	if models.SHA256Hex([]byte(assembledData)) != chunk.Checksum {
		r.Client.Del(ctx, stateKey)
		return nil, fmt.Errorf("result chunk reassembly checksum mismatch for transfer %s", chunk.TransferID)
	}

	var result models.RelayResult
	if err := json.Unmarshal([]byte(assembledData), &result); err != nil {
		r.Client.Del(ctx, stateKey)
		return nil, fmt.Errorf("invalid reassembled result payload: %w", err)
	}

	pipe.Del(ctx, stateKey)
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("finalize inbound result chunk: %w", err)
	}

	return &result, nil
}

func (r *Redis) ListChunkReceipts(ctx context.Context) ([]models.ChunkReceipt, error) {
	ids, err := r.Client.SMembers(ctx, keyChunkReceiptSet).Result()
	if err != nil {
		return nil, fmt.Errorf("list chunk receipt ids: %w", err)
	}

	receipts := make([]models.ChunkReceipt, 0, len(ids))
	for _, id := range ids {
		data, err := r.Client.HGetAll(ctx, keyChunkReceiptPrefix+id).Result()
		if err == redis.Nil || len(data) == 0 {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get chunk receipt %s: %w", id, err)
		}
		receipt, err := parseChunkReceipt(id, data)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, *receipt)
	}
	return receipts, nil
}

func (r *Redis) DeleteChunkReceipt(ctx context.Context, transferID string) error {
	pipe := r.Client.Pipeline()
	pipe.SRem(ctx, keyChunkReceiptSet, transferID)
	pipe.Del(ctx, keyChunkReceiptPrefix+transferID)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete chunk receipt %s: %w", transferID, err)
	}
	return nil
}

func (r *Redis) ApplyChunkReceipt(ctx context.Context, receipt models.ChunkReceipt) error {
	cursorKey := keyChunkCursorPrefix + receipt.TransferID
	nextIndex := receipt.NextIndex
	if receipt.Reset {
		nextIndex = 0
	}
	return r.Client.HSet(ctx, cursorKey, map[string]interface{}{
		"request_id": receipt.RequestID,
		"next_index": nextIndex,
		"updated_at": time.Now().Unix(),
	}).Err()
}

func (r *Redis) GetChunkCursor(ctx context.Context, transferID string) (*models.ChunkCursor, error) {
	data, err := r.Client.HGetAll(ctx, keyChunkCursorPrefix+transferID).Result()
	if err == redis.Nil || len(data) == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get chunk cursor %s: %w", transferID, err)
	}

	nextIndex, _ := strconv.Atoi(data["next_index"])
	updatedAt, _ := strconv.ParseInt(data["updated_at"], 10, 64)
	return &models.ChunkCursor{
		TransferID: transferID,
		RequestID:  data["request_id"],
		NextIndex:  nextIndex,
		UpdatedAt:  time.Unix(updatedAt, 0),
	}, nil
}

func (r *Redis) DeleteChunkCursor(ctx context.Context, transferID string) error {
	if err := r.Client.Del(ctx, keyChunkCursorPrefix+transferID).Err(); err != nil {
		return fmt.Errorf("delete chunk cursor %s: %w", transferID, err)
	}
	return nil
}

func (r *Redis) resetInboundChunk(ctx context.Context, chunk models.TransportChunk, reason string) (*models.ChunkReceipt, *models.RelayRequest, error) {
	pipe := r.Client.Pipeline()
	pipe.Del(ctx, keyInboundChunkPrefix+chunk.TransferID)
	receipt := models.ChunkReceipt{
		TransferID: chunk.TransferID,
		RequestID:  chunk.RequestID,
		NextIndex:  0,
		Reset:      true,
		Error:      reason,
	}
	if err := r.queueChunkReceipt(ctx, pipe, receipt); err != nil {
		return nil, nil, err
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, nil, fmt.Errorf("reset inbound chunk: %w", err)
	}
	return &receipt, nil, nil
}

func (r *Redis) storeChunkReceipt(ctx context.Context, receipt models.ChunkReceipt) error {
	pipe := r.Client.Pipeline()
	if err := r.queueChunkReceipt(ctx, pipe, receipt); err != nil {
		return err
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("store chunk receipt %s: %w", receipt.TransferID, err)
	}
	return nil
}

func (r *Redis) queueChunkReceipt(ctx context.Context, pipe redis.Pipeliner, receipt models.ChunkReceipt) error {
	pipe.SAdd(ctx, keyChunkReceiptSet, receipt.TransferID)
	pipe.HSet(ctx, keyChunkReceiptPrefix+receipt.TransferID, map[string]interface{}{
		"request_id": receipt.RequestID,
		"next_index": receipt.NextIndex,
		"completed":  receipt.Completed,
		"reset":      receipt.Reset,
		"error":      receipt.Error,
	})
	return nil
}

func parseChunkReceipt(transferID string, data map[string]string) (*models.ChunkReceipt, error) {
	nextIndex, _ := strconv.Atoi(data["next_index"])
	completed, _ := strconv.ParseBool(data["completed"])
	reset, _ := strconv.ParseBool(data["reset"])
	return &models.ChunkReceipt{
		TransferID: transferID,
		RequestID:  data["request_id"],
		NextIndex:  nextIndex,
		Completed:  completed,
		Reset:      reset,
		Error:      data["error"],
	}, nil
}
