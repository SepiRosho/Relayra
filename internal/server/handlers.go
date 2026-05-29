package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/crypto"
	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/models"
	"github.com/relayra/relayra/internal/relayexec"
	"github.com/relayra/relayra/internal/store"
	"github.com/relayra/relayra/internal/transport"
	"github.com/relayra/relayra/internal/webhook"
)

// Handlers holds dependencies for HTTP handlers.
type Handlers struct {
	rdb   store.Backend
	cfg   *config.Config
	wsHub *wsHub
}

func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	ctx := logger.WithComponent(r.Context(), "server")
	if err := h.rdb.Health(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "unhealthy",
			"error":  "redis unavailable",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "healthy",
		"role":     string(h.cfg.Role),
		"instance": h.cfg.InstanceName,
	})
}

func (h *Handlers) Relay(w http.ResponseWriter, r *http.Request) {
	ctx := logger.WithComponent(r.Context(), "server")

	body, err := io.ReadAll(io.LimitReader(r.Body, 50*1024*1024))
	if err != nil {
		slog.ErrorContext(ctx, "failed to read relay request body", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read request body"})
		return
	}

	var req struct {
		DestinationPeerID string             `json:"destination_peer_id"`
		WebhookURL        string             `json:"webhook_url,omitempty"`
		Async             bool               `json:"async,omitempty"`
		Request           models.HTTPRequest `json:"request"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		slog.ErrorContext(ctx, "invalid relay request JSON", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.DestinationPeerID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "destination_peer_id is required"})
		return
	}
	if req.Request.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request.url is required"})
		return
	}
	if req.Request.Method == "" {
		req.Request.Method = http.MethodGet
	}

	idBytes := make([]byte, 16)
	_, _ = rand.Read(idBytes)
	requestID := hex.EncodeToString(idBytes)

	relayReq := &models.RelayRequest{
		ID:            requestID,
		DestinationID: req.DestinationPeerID,
		WebhookURL:    req.WebhookURL,
		Async:         req.Async,
		Request:       req.Request,
		Status:        models.StatusQueued,
		CreatedAt:     time.Now(),
	}

	ctx = logger.WithRequestID(ctx, requestID)
	ctx = logger.WithPeerID(ctx, req.DestinationPeerID)

	if h.isListenerDestination(req.DestinationPeerID) {
		if !h.cfg.AllowListenerExecution {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "listener-side execution is disabled"})
			return
		}

		relayReq.DestinationID = h.cfg.MachineID
		relayReq.Status = models.StatusExecuting
		if err := h.rdb.StoreRequestMetadata(ctx, relayReq.DestinationID, relayReq); err != nil {
			slog.ErrorContext(ctx, "failed to persist local relay request", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to queue local execution"})
			return
		}

		go h.executeLocalRequest(relayReq)
		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"request_id": requestID,
			"status":     "executing",
			"message":    "Request accepted for listener-side execution",
		})
		return
	}

	if peer, err := h.rdb.GetPeer(ctx, req.DestinationPeerID); err != nil || peer == nil {
		slog.WarnContext(ctx, "relay to unknown peer", "peer_id", req.DestinationPeerID)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "peer not found"})
		return
	}

	if err := h.rdb.EnqueueRequest(ctx, req.DestinationPeerID, relayReq); err != nil {
		slog.ErrorContext(ctx, "failed to enqueue relay request", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to queue request"})
		return
	}
	if err := h.queueListenerRequestEvent(ctx, req.DestinationPeerID, *relayReq); err != nil {
		slog.ErrorContext(ctx, "failed to queue websocket delivery event", "error", err)
	}

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"request_id": requestID,
		"status":     "queued",
		"message":    "Request queued for delivery",
	})
}

func (h *Handlers) GetResult(w http.ResponseWriter, r *http.Request) {
	ctx := logger.WithComponent(r.Context(), "server")
	requestID := r.PathValue("requestID")
	if requestID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request_id is required"})
		return
	}

	ctx = logger.WithRequestID(ctx, requestID)
	result, err := h.rdb.GetResult(ctx, requestID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to get result", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if result != nil {
		ttl, _ := h.rdb.GetResultTTL(ctx, requestID)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"request_id":    requestID,
			"status":        "completed",
			"result":        result,
			"ttl_remaining": ttl,
		})
		return
	}

	status, err := h.rdb.GetRequestStatus(ctx, requestID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":      "request not found",
			"request_id": requestID,
		})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"request_id": requestID,
		"status":     status,
		"message":    "Result not yet available",
	})
}

func (h *Handlers) Poll(w http.ResponseWriter, r *http.Request) {
	ctx := logger.WithComponent(r.Context(), "server")

	body, err := io.ReadAll(io.LimitReader(r.Body, 50*1024*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}

	var pollReq models.PollRequest
	if err := json.Unmarshal(body, &pollReq); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	pollResp, err := h.handlePollMessage(ctx, pollReq.PeerID, pollReq.Payload, pollReq.Nonce, pollReq.Timestamp, pollReq.WaitSeconds)
	if err != nil {
		slog.ErrorContext(ctx, "failed to handle poll request", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, pollResp)
}

func (h *Handlers) Pair(w http.ResponseWriter, r *http.Request) {
	ctx := logger.WithComponent(r.Context(), "pairing")

	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}

	var pairReq models.PairingRequest
	if err := json.Unmarshal(body, &pairReq); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	secretHash := crypto.HashSecret(pairReq.Secret)
	token, err := h.rdb.GetPairingToken(ctx, secretHash)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, models.PairingResponse{
			Success: false,
			Error:   "invalid or expired pairing token",
		})
		return
	}
	if time.Now().Unix() > token.ExpiresAt {
		writeJSON(w, http.StatusUnauthorized, models.PairingResponse{
			Success: false,
			Error:   "pairing token expired",
		})
		return
	}

	encKey, err := crypto.DeriveKey(pairReq.Secret, h.cfg.MachineID, pairReq.MachineID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.PairingResponse{
			Success: false,
			Error:   "key derivation failed",
		})
		return
	}

	peerIDBytes := make([]byte, 8)
	_, _ = rand.Read(peerIDBytes)
	peerID := hex.EncodeToString(peerIDBytes)

	peer := &models.Peer{
		ID:            peerID,
		Name:          pairReq.Name,
		MachineID:     pairReq.MachineID,
		Role:          string(config.RoleSender),
		Capabilities:  pairReq.Capabilities,
		EncryptionKey: encKey,
		RegisteredAt:  time.Now(),
		LastSeen:      time.Now(),
	}
	if err := h.rdb.StorePeer(ctx, peer); err != nil {
		writeJSON(w, http.StatusInternalServerError, models.PairingResponse{
			Success: false,
			Error:   "failed to register peer",
		})
		return
	}

	writeJSON(w, http.StatusOK, models.PairingResponse{
		PeerID:       peerID,
		ListenerID:   h.cfg.MachineID,
		ListenerName: h.cfg.InstanceName,
		MachineID:    h.cfg.MachineID,
		Capabilities: h.cfg.Capabilities(),
		Success:      true,
	})
}

func (h *Handlers) ListPeers(w http.ResponseWriter, r *http.Request) {
	ctx := logger.WithComponent(r.Context(), "server")

	peers, err := h.rdb.ListPeers(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list peers"})
		return
	}

	type peerInfo struct {
		ID           string    `json:"id"`
		Name         string    `json:"name"`
		Role         string    `json:"role"`
		Capabilities []string  `json:"capabilities,omitempty"`
		RegisteredAt time.Time `json:"registered_at"`
		LastSeen     time.Time `json:"last_seen"`
		QueueSize    int64     `json:"queue_size"`
	}

	var result []peerInfo
	for _, p := range peers {
		qLen, _ := h.rdb.QueueLength(ctx, p.ID)
		result = append(result, peerInfo{
			ID:           p.ID,
			Name:         p.Name,
			Role:         p.Role,
			Capabilities: p.Capabilities,
			RegisteredAt: p.RegisteredAt,
			LastSeen:     p.LastSeen,
			QueueSize:    qLen,
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"peers": result,
		"count": len(result),
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handlers) executeLocalRequest(req *models.RelayRequest) {
	ctx := logger.WithComponent(context.Background(), "server")
	ctx = logger.WithRequestID(ctx, req.ID)
	ctx = logger.WithPeerID(ctx, h.cfg.MachineID)

	result := relayexec.ExecuteRequest(ctx, req, h.cfg.RequestTimeout)
	if err := h.rdb.StoreResult(ctx, result, h.cfg.ResultTTL); err != nil {
		slog.ErrorContext(ctx, "failed to store listener-side result", "error", err)
		return
	}

	webhookURL, _ := h.rdb.GetRequestWebhookURL(ctx, req.ID)
	if webhookURL != "" {
		resultCopy := *result
		go webhook.Deliver(context.Background(), h.rdb, webhookURL, req.ID, &resultCopy, h.cfg.WebhookMaxRetries)
	}
}

func (h *Handlers) isListenerDestination(destinationID string) bool {
	switch strings.ToLower(strings.TrimSpace(destinationID)) {
	case "listener", "self", strings.ToLower(h.cfg.MachineID):
		return true
	default:
		return false
	}
}

func (h *Handlers) queueListenerRequestEvent(ctx context.Context, peerID string, req models.RelayRequest) error {
	needsChunking, _, err := transport.RequestNeedsChunking(req, h.cfg.TransportChunkSize())
	if err != nil {
		return err
	}
	scope := models.ListenerWSScope(peerID)
	if !needsChunking {
		_, err := transport.EnqueueWSMessage(ctx, h.rdb, scope, &models.WSMessage{
			Type:    models.WSMessageTypePushRequest,
			PeerID:  peerID,
			Request: &req,
		}, req.ID)
		if err == nil && h.wsHub != nil {
			h.wsHub.Notify(peerID)
		}
		return err
	}
	return h.queueNextChunkEvent(ctx, peerID, &req)
}

func (h *Handlers) queueNextChunkEvent(ctx context.Context, peerID string, req *models.RelayRequest) error {
	if req == nil {
		return nil
	}
	cursor, err := h.rdb.GetChunkCursor(ctx, models.RequestTransferID(req.ID))
	if err != nil {
		return err
	}
	nextIndex := 0
	if cursor != nil {
		nextIndex = cursor.NextIndex
	}
	chunk, err := transport.RequestChunkAt(*req, h.cfg.TransportChunkSize(), nextIndex)
	if err != nil {
		if strings.Contains(err.Error(), "out of range") {
			return nil
		}
		return err
	}
	refID := fmt.Sprintf("%s:%d", chunk.TransferID, chunk.Index)
	_, err = transport.EnqueueWSMessage(ctx, h.rdb, models.ListenerWSScope(peerID), &models.WSMessage{
		Type:   models.WSMessageTypePushChunk,
		PeerID: peerID,
		Chunk:  chunk,
	}, refID)
	if err == nil && h.wsHub != nil {
		h.wsHub.Notify(peerID)
	}
	return err
}

func (h *Handlers) storeSenderResultWS(ctx context.Context, peerID string, result *models.RelayResult) error {
	if result == nil {
		return nil
	}
	resultCtx := logger.WithRequestID(ctx, result.RequestID)
	if err := h.rdb.StoreResult(resultCtx, result, h.cfg.ResultTTL); err != nil {
		return err
	}
	webhookURL, _ := h.rdb.GetRequestWebhookURL(resultCtx, result.RequestID)
	if webhookURL != "" {
		webhookCtx := logger.WithComponent(context.Background(), "webhook")
		webhookCtx = logger.WithRequestID(webhookCtx, result.RequestID)
		webhookCtx = logger.WithPeerID(webhookCtx, peerID)
		resultCopy := *result
		go webhook.Deliver(webhookCtx, h.rdb, webhookURL, result.RequestID, &resultCopy, h.cfg.WebhookMaxRetries)
	}
	return nil
}

func (h *Handlers) applySenderChunkReceiptWS(ctx context.Context, peerID string, receipt *models.ChunkReceipt) error {
	if receipt == nil {
		return nil
	}
	if err := h.rdb.ApplyChunkReceipt(ctx, *receipt); err != nil {
		return err
	}
	if receipt.Completed {
		return nil
	}
	req, err := h.rdb.GetRequest(ctx, receipt.RequestID)
	if err != nil || req == nil {
		return err
	}
	return h.queueNextChunkEvent(ctx, peerID, req)
}

func (h *Handlers) rebuildListenerWSOutbox(ctx context.Context, peerID string, afterSeq int64) error {
	scope := models.ListenerWSScope(peerID)
	if err := h.rdb.ResetWSOutbox(ctx, scope, afterSeq); err != nil {
		return err
	}

	remaining := h.cfg.PollBatchSize
	appendRequest := func(req models.RelayRequest) error {
		if remaining < 1 {
			return nil
		}
		needsChunking, _, err := transport.RequestNeedsChunking(req, h.cfg.TransportChunkSize())
		if err != nil {
			return err
		}
		if !needsChunking {
			_, err = transport.EnqueueWSMessage(ctx, h.rdb, scope, &models.WSMessage{
				Type:    models.WSMessageTypePushRequest,
				PeerID:  peerID,
				Request: &req,
			}, req.ID)
			if err == nil {
				remaining--
			}
			return err
		}

		cursor, err := h.rdb.GetChunkCursor(ctx, models.RequestTransferID(req.ID))
		if err != nil {
			return err
		}
		nextIndex := 0
		if cursor != nil {
			nextIndex = cursor.NextIndex
		}
		chunk, err := transport.RequestChunkAt(req, h.cfg.TransportChunkSize(), nextIndex)
		if err != nil {
			if strings.Contains(err.Error(), "out of range") {
				return nil
			}
			return err
		}
		_, err = transport.EnqueueWSMessage(ctx, h.rdb, scope, &models.WSMessage{
			Type:   models.WSMessageTypePushChunk,
			PeerID: peerID,
			Chunk:  chunk,
		}, fmt.Sprintf("%s:%d", chunk.TransferID, chunk.Index))
		if err == nil {
			remaining--
		}
		return err
	}

	active, err := h.rdb.ListActiveLeasedRequests(ctx, peerID, h.cfg.PollBatchSize)
	if err != nil {
		return err
	}
	for _, req := range active {
		if remaining < 1 {
			break
		}
		if err := appendRequest(req); err != nil {
			return err
		}
	}

	if remaining < 1 {
		return nil
	}

	leased, err := h.rdb.LeaseRequests(ctx, peerID, remaining, h.requestLeaseDuration())
	if err != nil {
		return err
	}
	for _, req := range leased {
		if remaining < 1 {
			break
		}
		if err := appendRequest(req); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handlers) waitForQueuedRequests(ctx context.Context, peerID string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		qLen, err := h.rdb.QueueLength(ctx, peerID)
		if err == nil && qLen > 0 {
			return
		}
		if time.Now().After(deadline) {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handlers) requestLeaseDuration() time.Duration {
	seconds := h.cfg.RequestTimeout + h.cfg.PollInterval + 30
	wsWindow := h.cfg.WSIdleTimeout + h.cfg.WSReconnectMaxSeconds
	if wsWindow > h.cfg.LongPollWait {
		seconds += wsWindow
	} else {
		seconds += h.cfg.LongPollWait
	}
	if seconds < h.cfg.RequestTimeout+30 {
		seconds = h.cfg.RequestTimeout + 30
	}
	return time.Duration(seconds) * time.Second
}

func (h *Handlers) handlePollMessage(ctx context.Context, peerID, payload, nonce string, timestamp int64, waitSeconds int) (*models.PollResponse, error) {
	ctx = logger.WithPeerID(ctx, peerID)

	peer, err := h.rdb.GetPeer(ctx, peerID)
	if err != nil || peer == nil {
		return nil, fmt.Errorf("unknown peer")
	}

	_ = h.rdb.UpdatePeerLastSeen(ctx, peerID)

	var payloadUp models.PollPayloadUp
	if err := crypto.DecryptJSON(peer.EncryptionKey, payload, nonce, timestamp, &payloadUp); err != nil {
		return nil, fmt.Errorf("decryption failed")
	}

	slog.InfoContext(ctx, "poll received",
		"results_count", len(payloadUp.Results),
		"request_states", len(payloadUp.RequestStates),
	)

	payloadDown, err := h.processPollPayload(ctx, peerID, &payloadUp, waitSeconds)
	if err != nil {
		return nil, err
	}

	ciphertext, respNonce, respTimestamp, err := crypto.EncryptJSON(peer.EncryptionKey, payloadDown)
	if err != nil {
		return nil, fmt.Errorf("encryption failed")
	}

	return &models.PollResponse{
		Nonce:     respNonce,
		Timestamp: respTimestamp,
		Payload:   ciphertext,
	}, nil
}

func (h *Handlers) processPollPayload(ctx context.Context, peerID string, payloadUp *models.PollPayloadUp, waitSeconds int) (*models.PollPayloadDown, error) {
	if len(payloadUp.ChunkReceipts) > 0 {
		for _, receipt := range payloadUp.ChunkReceipts {
			if err := h.rdb.ApplyChunkReceipt(ctx, receipt); err != nil {
				return nil, err
			}
		}
	}

	probeResp := h.buildProbeAck(payloadUp)
	probeOnly := payloadUp.Probe != nil && payloadUp.Probe.ProbeOnly &&
		len(payloadUp.Results) == 0 && len(payloadUp.RequestStates) == 0 && len(payloadUp.ChunkReceipts) == 0
	if probeOnly {
		return &models.PollPayloadDown{Probe: probeResp}, nil
	}

	if len(payloadUp.RequestStates) > 0 {
		if err := h.rdb.ApplyRequestStates(ctx, payloadUp.RequestStates); err != nil {
			return nil, err
		}
	}

	var ackResultIDs []string
	for _, result := range payloadUp.Results {
		resultCtx := logger.WithRequestID(ctx, result.RequestID)
		if err := h.rdb.StoreResult(resultCtx, &result, h.cfg.ResultTTL); err != nil {
			slog.ErrorContext(resultCtx, "failed to store result from sender", "error", err)
			continue
		}
		ackResultIDs = append(ackResultIDs, result.RequestID)

		webhookURL, _ := h.rdb.GetRequestWebhookURL(resultCtx, result.RequestID)
		if webhookURL != "" {
			webhookCtx := logger.WithComponent(context.Background(), "webhook")
			webhookCtx = logger.WithRequestID(webhookCtx, result.RequestID)
			webhookCtx = logger.WithPeerID(webhookCtx, peerID)
			resultCopy := result
			go webhook.Deliver(webhookCtx, h.rdb, webhookURL, result.RequestID, &resultCopy, h.cfg.WebhookMaxRetries)
		}
	}

	if waitSeconds > 0 && len(ackResultIDs) == 0 {
		if waitSeconds > h.cfg.LongPollWait {
			waitSeconds = h.cfg.LongPollWait
		}
		h.waitForQueuedRequests(ctx, peerID, time.Duration(waitSeconds)*time.Second)
	}

	requests, chunks, err := h.nextOutboundPayload(ctx, peerID)
	if err != nil {
		return nil, err
	}

	return &models.PollPayloadDown{
		Requests:      requests,
		RequestChunks: chunks,
		AckResultIDs:  ackResultIDs,
		Probe:         probeResp,
	}, nil
}

func (h *Handlers) buildProbeAck(payloadUp *models.PollPayloadUp) *models.ProbeMessage {
	if payloadUp == nil || payloadUp.Probe == nil {
		return nil
	}
	return &models.ProbeMessage{
		ID:        payloadUp.Probe.ID,
		Sequence:  payloadUp.Probe.Sequence,
		SentAt:    payloadUp.Probe.SentAt,
		Ack:       true,
		ProbeOnly: payloadUp.Probe.ProbeOnly,
	}
}

func (h *Handlers) nextOutboundPayload(ctx context.Context, peerID string) ([]models.RelayRequest, []models.TransportChunk, error) {
	remaining := h.cfg.PollBatchSize
	requests := make([]models.RelayRequest, 0, remaining)
	chunks := make([]models.TransportChunk, 0, remaining)

	appendPayload := func(req models.RelayRequest) error {
		if remaining < 1 {
			return nil
		}

		needsChunking, _, err := transport.RequestNeedsChunking(req, h.cfg.TransportChunkSize())
		if err != nil {
			return err
		}
		if !needsChunking {
			requests = append(requests, req)
			remaining--
			return nil
		}

		cursor, err := h.rdb.GetChunkCursor(ctx, models.RequestTransferID(req.ID))
		if err != nil {
			return err
		}
		nextIndex := 0
		if cursor != nil {
			nextIndex = cursor.NextIndex
		}
		chunk, err := transport.RequestChunkAt(req, h.cfg.TransportChunkSize(), nextIndex)
		if err != nil {
			if strings.Contains(err.Error(), "out of range") {
				return nil
			}
			return err
		}
		chunks = append(chunks, *chunk)
		remaining--
		return nil
	}

	active, err := h.rdb.ListActiveLeasedRequests(ctx, peerID, h.cfg.PollBatchSize)
	if err != nil {
		return nil, nil, err
	}
	for _, req := range active {
		if remaining < 1 {
			break
		}
		needsChunking, _, err := transport.RequestNeedsChunking(req, h.cfg.TransportChunkSize())
		if err != nil {
			return nil, nil, err
		}
		if !needsChunking {
			continue
		}
		if err := appendPayload(req); err != nil {
			return nil, nil, err
		}
	}

	if remaining < 1 {
		return requests, chunks, nil
	}

	leased, err := h.rdb.LeaseRequests(ctx, peerID, remaining, h.requestLeaseDuration())
	if err != nil {
		return nil, nil, err
	}
	for _, req := range leased {
		if remaining < 1 {
			break
		}
		if err := appendPayload(req); err != nil {
			return nil, nil, err
		}
	}

	return requests, chunks, nil
}
