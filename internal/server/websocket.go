package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/models"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// WebSocket handles persistent sender connections using the same encrypted
// request/response envelopes as the HTTP poll endpoint.
func (h *Handlers) WebSocket(w http.ResponseWriter, r *http.Request) {
	ctx := logger.WithComponent(r.Context(), "server")
	peerID := r.URL.Query().Get("peer_id")
	if peerID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "peer_id is required"})
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.ErrorContext(ctx, "websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	baseCtx = logger.WithComponent(baseCtx, "server")
	baseCtx = logger.WithPeerID(baseCtx, peerID)

	pingTicker := time.NewTicker(20 * time.Second)
	defer pingTicker.Stop()

	readTimeout := 2*h.requestLeaseDuration() + 15*time.Second
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(readTimeout))
	})

	go func() {
		for {
			select {
			case <-pingTicker.C:
				_ = conn.WriteControl(websocket.PingMessage, []byte("relayra"), time.Now().Add(5*time.Second))
			case <-baseCtx.Done():
				return
			}
		}
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			slog.WarnContext(baseCtx, "websocket read failed", "error", err)
			return
		}

		var pollReq models.PollRequest
		if err := json.Unmarshal(message, &pollReq); err != nil {
			slog.WarnContext(baseCtx, "invalid websocket poll message", "error", err)
			return
		}
		if pollReq.PeerID == "" {
			pollReq.PeerID = peerID
		}
		if pollReq.PeerID != peerID {
			slog.WarnContext(baseCtx, "websocket peer mismatch", "peer_id", pollReq.PeerID)
			return
		}

		resp, err := h.handlePollMessage(baseCtx, peerID, pollReq.Payload, pollReq.Nonce, pollReq.Timestamp, pollReq.WaitSeconds)
		if err != nil {
			slog.WarnContext(baseCtx, "failed to handle websocket poll", "error", err)
			return
		}

		_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if err := conn.WriteJSON(resp); err != nil {
			slog.WarnContext(baseCtx, "websocket write failed", "error", err)
			return
		}
	}
}
