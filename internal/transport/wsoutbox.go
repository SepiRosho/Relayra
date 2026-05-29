package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/relayra/relayra/internal/models"
	"github.com/relayra/relayra/internal/store"
)

func EnqueueWSMessage(ctx context.Context, rdb store.Backend, scope string, msg *models.WSMessage, refID string) (int64, error) {
	if msg == nil {
		return 0, fmt.Errorf("websocket message is required")
	}
	msg.SentAt = time.Now().UnixMilli()
	payload, err := json.Marshal(msg)
	if err != nil {
		return 0, fmt.Errorf("marshal websocket message: %w", err)
	}
	seq, err := rdb.AppendWSOutbox(ctx, scope, msg.Type, refID, string(payload))
	if err != nil {
		return 0, err
	}
	msg.Seq = seq
	return seq, nil
}

func DecodeWSOutboxMessage(entry models.WSOutboxMessage) (*models.WSMessage, error) {
	var msg models.WSMessage
	if err := json.Unmarshal([]byte(entry.Payload), &msg); err != nil {
		return nil, fmt.Errorf("decode websocket outbox message %d: %w", entry.Seq, err)
	}
	msg.Seq = entry.Seq
	return &msg, nil
}
