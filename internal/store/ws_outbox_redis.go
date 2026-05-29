package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/relayra/relayra/internal/models"
)

const (
	keyWSStatePrefix       = "relayra:ws_state:"
	keyWSOutboxIndexPrefix = "relayra:ws_outbox_index:"
	keyWSOutboxMsgPrefix   = "relayra:ws_outbox_msg:"
)

var ErrWSOutboxGap = errors.New("websocket outbox sequence gap")

func (r *Redis) NextWSOutboundSeq(ctx context.Context, scope string) (int64, error) {
	stateKey := keyWSStatePrefix + scope
	nextSeq, err := r.Client.HIncrBy(ctx, stateKey, "next_outbound_seq", 1).Result()
	if err != nil {
		return 0, fmt.Errorf("increment websocket next seq: %w", err)
	}
	_ = r.Client.HSet(ctx, stateKey, "updated_at", time.Now().Unix()).Err()
	return nextSeq, nil
}

func (r *Redis) EnqueueWSOutbox(ctx context.Context, scope string, seq int64, msgType, refID, payload string) error {
	msgKey := fmt.Sprintf("%s%s:%d", keyWSOutboxMsgPrefix, scope, seq)
	indexKey := keyWSOutboxIndexPrefix + scope
	pipe := r.Client.Pipeline()
	pipe.ZAdd(ctx, indexKey, redis.Z{Score: float64(seq), Member: strconv.FormatInt(seq, 10)})
	pipe.HSet(ctx, msgKey, map[string]interface{}{
		"scope":      scope,
		"seq":        seq,
		"type":       msgType,
		"ref_id":     refID,
		"payload":    payload,
		"created_at": time.Now().Unix(),
	})
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("enqueue websocket outbox message: %w", err)
	}
	return nil
}

func (r *Redis) ListWSOutbox(ctx context.Context, scope string, afterSeq int64, limit int) ([]models.WSOutboxMessage, error) {
	indexKey := keyWSOutboxIndexPrefix + scope
	members, err := r.Client.ZRangeByScore(ctx, indexKey, &redis.ZRangeBy{
		Min:    fmt.Sprintf("(%d", afterSeq),
		Max:    "+inf",
		Offset: 0,
		Count:  int64(limit),
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("list websocket outbox index: %w", err)
	}

	out := make([]models.WSOutboxMessage, 0, len(members))
	expectedSeq := afterSeq + 1
	for _, member := range members {
		seq, err := strconv.ParseInt(member, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid websocket outbox seq %q for scope %s", ErrWSOutboxGap, member, scope)
		}
		if seq != expectedSeq {
			return nil, fmt.Errorf("%w: scope %s expected seq %d but found %d", ErrWSOutboxGap, scope, expectedSeq, seq)
		}
		data, err := r.Client.HGetAll(ctx, fmt.Sprintf("%s%s:%d", keyWSOutboxMsgPrefix, scope, seq)).Result()
		if err == redis.Nil || len(data) == 0 {
			return nil, fmt.Errorf("%w: scope %s missing payload for seq %d", ErrWSOutboxGap, scope, seq)
		}
		if err != nil {
			return nil, fmt.Errorf("get websocket outbox message %d: %w", seq, err)
		}
		createdAt, _ := strconv.ParseInt(data["created_at"], 10, 64)
		out = append(out, models.WSOutboxMessage{
			Scope:     scope,
			Seq:       seq,
			Type:      data["type"],
			RefID:     data["ref_id"],
			Payload:   data["payload"],
			CreatedAt: time.Unix(createdAt, 0),
		})
		expectedSeq++
	}
	return out, nil
}

func (r *Redis) AckWSOutboxThrough(ctx context.Context, scope string, seq int64) ([]models.WSOutboxMessage, error) {
	indexKey := keyWSOutboxIndexPrefix + scope
	members, err := r.Client.ZRangeByScore(ctx, indexKey, &redis.ZRangeBy{
		Min: "-inf",
		Max: fmt.Sprintf("%d", seq),
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("list websocket ack set: %w", err)
	}

	acked := make([]models.WSOutboxMessage, 0, len(members))
	pipe := r.Client.Pipeline()
	for _, member := range members {
		msgSeq, err := strconv.ParseInt(member, 10, 64)
		if err != nil {
			continue
		}
		msgKey := fmt.Sprintf("%s%s:%d", keyWSOutboxMsgPrefix, scope, msgSeq)
		data, err := r.Client.HGetAll(ctx, msgKey).Result()
		if err != nil && err != redis.Nil {
			return nil, fmt.Errorf("get websocket ack message %d: %w", msgSeq, err)
		}
		if len(data) > 0 {
			createdAt, _ := strconv.ParseInt(data["created_at"], 10, 64)
			acked = append(acked, models.WSOutboxMessage{
				Scope:     scope,
				Seq:       msgSeq,
				Type:      data["type"],
				RefID:     data["ref_id"],
				Payload:   data["payload"],
				CreatedAt: time.Unix(createdAt, 0),
			})
		}
		pipe.Del(ctx, msgKey)
	}
	if len(members) > 0 {
		pipe.ZRem(ctx, indexKey, toAnySlice(members)...)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("ack websocket outbox through %d: %w", seq, err)
	}
	return acked, nil
}

func (r *Redis) GetWSSequenceState(ctx context.Context, scope string) (*models.WSSequenceState, error) {
	data, err := r.Client.HGetAll(ctx, keyWSStatePrefix+scope).Result()
	if err == redis.Nil || len(data) == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get websocket sequence state: %w", err)
	}
	nextSeq, _ := strconv.ParseInt(data["next_outbound_seq"], 10, 64)
	lastReceived, _ := strconv.ParseInt(data["last_received_seq"], 10, 64)
	updatedAt, _ := strconv.ParseInt(data["updated_at"], 10, 64)
	return &models.WSSequenceState{
		Scope:           scope,
		NextOutboundSeq: nextSeq,
		LastReceivedSeq: lastReceived,
		UpdatedAt:       time.Unix(updatedAt, 0),
	}, nil
}

func (r *Redis) SetWSLastReceivedSeq(ctx context.Context, scope string, seq int64) error {
	stateKey := keyWSStatePrefix + scope
	current, err := r.Client.HGet(ctx, stateKey, "last_received_seq").Int64()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("get websocket last received seq: %w", err)
	}
	if seq < current {
		seq = current
	}
	if err := r.Client.HSet(ctx, stateKey, map[string]interface{}{
		"last_received_seq": seq,
		"updated_at":        time.Now().Unix(),
	}).Err(); err != nil {
		return fmt.Errorf("set websocket last received seq: %w", err)
	}
	return nil
}

func toAnySlice(values []string) []interface{} {
	out := make([]interface{}, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}
