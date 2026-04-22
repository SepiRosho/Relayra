package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	"github.com/relayra/relayra/internal/webhook"
)

// Handlers holds dependencies for HTTP handlers.
type Handlers struct {
	rdb store.Backend
	cfg *config.Config
}

// Health is a simple health check endpoint.
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

// Relay accepts a new relay request and queues it for a peer.
func (h *Handlers) Relay(w http.ResponseWriter, r *http.Request) {
	ctx := logger.WithComponent(r.Context(), "server")

	body, err := io.ReadAll(io.LimitReader(r.Body, 50*1024*1024)) // 50MB max
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
		req.Request.Method = "GET"
	}

	// Generate request ID
	idBytes := make([]byte, 16)
	rand.Read(idBytes)
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
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "listener-side execution is disabled",
			})
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

		slog.InfoContext(ctx, "listener accepted local relay request",
			"url", req.Request.URL,
			"method", req.Request.Method,
			"has_webhook", req.WebhookURL != "",
			"async", req.Async,
		)

		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"request_id": requestID,
			"status":     "executing",
			"message":    "Request accepted for listener-side execution",
		})
		return
	}

	// Verify peer exists
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

	slog.InfoContext(ctx, "relay request accepted",
		"url", req.Request.URL,
		"method", req.Request.Method,
		"has_webhook", req.WebhookURL != "",
	)

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"request_id": requestID,
		"status":     "queued",
		"message":    "Request queued for delivery",
	})
}

// GetResult returns the result of a relay request.
func (h *Handlers) GetResult(w http.ResponseWriter, r *http.Request) {
	ctx := logger.WithComponent(r.Context(), "server")
	requestID := r.PathValue("requestID")
	if requestID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request_id is required"})
		return
	}

	ctx = logger.WithRequestID(ctx, requestID)

	// Check if result exists
	result, err := h.rdb.GetResult(ctx, requestID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to get result", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	if result != nil {
		ttl, _ := h.rdb.GetResultTTL(ctx, requestID)
		slog.InfoContext(ctx, "result returned to client", "status_code", result.StatusCode, "ttl_remaining", ttl)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"request_id":    requestID,
			"status":        "completed",
			"result":        result,
			"ttl_remaining": ttl,
		})
		return
	}

	// No result yet — check request status
	status, err := h.rdb.GetRequestStatus(ctx, requestID)
	if err != nil {
		slog.DebugContext(ctx, "request not found")
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":      "request not found",
			"request_id": requestID,
		})
		return
	}

	slog.DebugContext(ctx, "result not ready", "status", status)
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"request_id": requestID,
		"status":     status,
		"message":    "Result not yet available",
	})
}

// Poll handles the Sender's polling request — receives results, sends new requests.
func (h *Handlers) Poll(w http.ResponseWriter, r *http.Request) {
	ctx := logger.WithComponent(r.Context(), "server")

	body, err := io.ReadAll(io.LimitReader(r.Body, 50*1024*1024))
	if err != nil {
		slog.ErrorContext(ctx, "failed to read poll body", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}

	var pollReq models.PollRequest
	if err := json.Unmarshal(body, &pollReq); err != nil {
		slog.ErrorContext(ctx, "invalid poll JSON", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	ctx = logger.WithPeerID(ctx, pollReq.PeerID)

	// Get peer for decryption
	peer, err := h.rdb.GetPeer(ctx, pollReq.PeerID)
	if err != nil || peer == nil {
		slog.WarnContext(ctx, "poll from unknown peer")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unknown peer"})
		return
	}

	// Update last seen
	h.rdb.UpdatePeerLastSeen(ctx, pollReq.PeerID)

	// Decrypt incoming payload
	var payloadUp models.PollPayloadUp
	if err := crypto.DecryptJSON(peer.EncryptionKey, pollReq.Payload, pollReq.Nonce, pollReq.Timestamp, &payloadUp); err != nil {
		slog.ErrorContext(ctx, "failed to decrypt poll payload", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "decryption failed"})
		return
	}

	slog.InfoContext(ctx, "poll received",
		"results_count", len(payloadUp.Results),
		"ack_count", len(payloadUp.AckRequestIDs),
	)

	// Process incoming results
	var ackResultIDs []string
	for _, result := range payloadUp.Results {
		resultCtx := logger.WithRequestID(ctx, result.RequestID)

		if err := h.rdb.StoreResult(resultCtx, &result, h.cfg.ResultTTL); err != nil {
			slog.ErrorContext(resultCtx, "failed to store result from poll", "error", err)
			continue
		}
		ackResultIDs = append(ackResultIDs, result.RequestID)

		// Trigger webhook delivery asynchronously
		// Use a detached context — the HTTP request context will be cancelled
		// when this handler returns, which would kill the webhook goroutine.
		webhookURL, _ := h.rdb.GetRequestWebhookURL(resultCtx, result.RequestID)
		if webhookURL != "" {
			webhookCtx := logger.WithComponent(context.Background(), "webhook")
			webhookCtx = logger.WithRequestID(webhookCtx, result.RequestID)
			webhookCtx = logger.WithPeerID(webhookCtx, pollReq.PeerID)
			resultCopy := result // capture loop variable
			go webhook.Deliver(webhookCtx, h.rdb, webhookURL, result.RequestID, &resultCopy, h.cfg.WebhookMaxRetries)
		}
	}

	// Process acks (Sender confirms it received these requests)
	if len(payloadUp.AckRequestIDs) > 0 {
		h.rdb.AckRequests(ctx, payloadUp.AckRequestIDs)
	}

	if pollReq.WaitSeconds > 0 && len(ackResultIDs) == 0 {
		waitSeconds := pollReq.WaitSeconds
		if waitSeconds > h.cfg.LongPollWait {
			waitSeconds = h.cfg.LongPollWait
		}
		h.waitForQueuedRequests(ctx, pollReq.PeerID, time.Duration(waitSeconds)*time.Second)
	}

	// Dequeue new batch for this peer
	requests, err := h.rdb.DequeueRequests(ctx, pollReq.PeerID, h.cfg.PollBatchSize)
	if err != nil {
		slog.ErrorContext(ctx, "failed to dequeue requests", "error", err)
		requests = nil // Continue with empty batch
	}

	// Build response payload
	payloadDown := models.PollPayloadDown{
		Requests:     requests,
		AckResultIDs: ackResultIDs,
	}

	ciphertext, nonce, timestamp, err := crypto.EncryptJSON(peer.EncryptionKey, &payloadDown)
	if err != nil {
		slog.ErrorContext(ctx, "failed to encrypt poll response", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "encryption failed"})
		return
	}

	slog.InfoContext(ctx, "poll response",
		"new_requests", len(requests),
		"acked_results", len(ackResultIDs),
	)

	writeJSON(w, http.StatusOK, models.PollResponse{
		Nonce:     nonce,
		Timestamp: timestamp,
		Payload:   ciphertext,
	})
}

// Pair handles pairing requests from Sender instances.
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

	slog.InfoContext(ctx, "pairing request received",
		"sender_name", pairReq.Name,
		"sender_machine_id", pairReq.MachineID[:12]+"...",
	)

	// Validate pairing token
	secretHash := crypto.HashSecret(pairReq.Secret)
	token, err := h.rdb.GetPairingToken(ctx, secretHash)
	if err != nil {
		slog.WarnContext(ctx, "pairing token validation failed", "error", err)
		writeJSON(w, http.StatusUnauthorized, models.PairingResponse{
			Success: false,
			Error:   "invalid or expired pairing token",
		})
		return
	}

	// Check expiry
	if time.Now().Unix() > token.ExpiresAt {
		slog.WarnContext(ctx, "pairing token expired")
		writeJSON(w, http.StatusUnauthorized, models.PairingResponse{
			Success: false,
			Error:   "pairing token expired",
		})
		return
	}

	// Derive encryption key
	encKey, err := crypto.DeriveKey(pairReq.Secret, h.cfg.MachineID, pairReq.MachineID)
	if err != nil {
		slog.ErrorContext(ctx, "key derivation failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.PairingResponse{
			Success: false,
			Error:   "key derivation failed",
		})
		return
	}

	// Generate peer ID (short, derived from machine ID)
	peerIDBytes := make([]byte, 8)
	rand.Read(peerIDBytes)
	peerID := hex.EncodeToString(peerIDBytes)

	// Store peer
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
		slog.ErrorContext(ctx, "failed to store peer", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.PairingResponse{
			Success: false,
			Error:   "failed to register peer",
		})
		return
	}

	slog.InfoContext(ctx, "peer paired successfully",
		"peer_id", peerID,
		"sender_name", pairReq.Name,
	)

	writeJSON(w, http.StatusOK, models.PairingResponse{
		PeerID:       peerID,
		ListenerID:   h.cfg.MachineID,
		ListenerName: h.cfg.InstanceName,
		MachineID:    h.cfg.MachineID,
		Capabilities: h.cfg.Capabilities(),
		Success:      true,
	})
}

// ListPeers returns all registered peers.
func (h *Handlers) ListPeers(w http.ResponseWriter, r *http.Request) {
	ctx := logger.WithComponent(r.Context(), "server")

	peers, err := h.rdb.ListPeers(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list peers", "error", err)
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
	json.NewEncoder(w).Encode(v)
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
