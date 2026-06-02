package poller

import (
	"context"

	"github.com/relayra/relayra/internal/models"
	"github.com/relayra/relayra/internal/store"
	"github.com/relayra/relayra/internal/transport"
)

func queueSenderRequestStateWS(ctx context.Context, rdb store.Backend, listenerID string, state models.RequestSyncState) error {
	_, err := transport.EnqueueWSMessage(ctx, rdb, models.SenderWSScope(listenerID), &models.WSMessage{
		Type:         models.WSMessageTypeRequestState,
		PeerID:       listenerID,
		RequestState: &state,
	}, state.RequestID)
	return err
}

func queueSenderResultWS(ctx context.Context, rdb store.Backend, listenerID string, result *models.RelayResult, chunkSize int) error {
	if result == nil {
		return nil
	}
	scope := models.SenderWSScope(listenerID)

	needsChunking, _, err := transport.ResultNeedsChunking(*result, chunkSize)
	if err != nil {
		return err
	}

	if !needsChunking {
		_, err := transport.EnqueueWSMessage(ctx, rdb, scope, &models.WSMessage{
			Type:   models.WSMessageTypeResult,
			PeerID: listenerID,
			Result: result,
		}, result.RequestID)
		return err
	}

	data, err := transport.ResultPayload(*result)
	if err != nil {
		return err
	}
	total := (len(data) + chunkSize - 1) / chunkSize
	for i := 0; i < total; i++ {
		chunk, err := transport.ResultChunkAt(*result, chunkSize, i)
		if err != nil {
			return err
		}
		// Only the last chunk carries refID so AckResults fires exactly once.
		refID := ""
		if i == total-1 {
			refID = result.RequestID
		}
		if _, err := transport.EnqueueWSMessage(ctx, rdb, scope, &models.WSMessage{
			Type:        models.WSMessageTypeResultChunk,
			PeerID:      listenerID,
			ResultChunk: chunk,
		}, refID); err != nil {
			return err
		}
	}
	return nil
}

func queueSenderChunkReceiptWS(ctx context.Context, rdb store.Backend, listenerID string, receipt models.ChunkReceipt) error {
	_, err := transport.EnqueueWSMessage(ctx, rdb, models.SenderWSScope(listenerID), &models.WSMessage{
		Type:         models.WSMessageTypeChunkReceipt,
		PeerID:       listenerID,
		ChunkReceipt: &receipt,
	}, receipt.TransferID)
	return err
}
