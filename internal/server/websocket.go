package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/models"
	"github.com/relayra/relayra/internal/store"
	"github.com/relayra/relayra/internal/transport"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type wsHub struct {
	mu       sync.Mutex
	sessions map[string]*wsServerSession
}

type wsServerSession struct {
	handlers            *Handlers
	peerID              string
	scope               string
	conn                *websocket.Conn
	sendCh              chan models.WSMessage
	notifyCh            chan struct{}
	doneCh              chan struct{}
	closeOnce           sync.Once
	outboxMu            sync.Mutex
	lastSentSeq         int64
	lastReceivedSeq     int64
	lastPeerSeenRefresh time.Time
}

func newWSHub() *wsHub {
	return &wsHub{sessions: make(map[string]*wsServerSession)}
}

func (h *wsHub) Register(peerID string, session *wsServerSession) *wsServerSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	prev := h.sessions[peerID]
	h.sessions[peerID] = session
	return prev
}

func (h *wsHub) Unregister(peerID string, session *wsServerSession) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if current, ok := h.sessions[peerID]; ok && current == session {
		delete(h.sessions, peerID)
	}
}

func (h *wsHub) Notify(peerID string) {
	h.mu.Lock()
	session := h.sessions[peerID]
	h.mu.Unlock()
	if session == nil {
		return
	}
	select {
	case session.notifyCh <- struct{}{}:
	default:
	}
}

// WebSocket handles persistent sender connections using the websocket live protocol.
func (h *Handlers) WebSocket(w http.ResponseWriter, r *http.Request) {
	ctx := logger.WithComponent(r.Context(), "server")
	queryPeerID := r.URL.Query().Get("peer_id")
	if queryPeerID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "peer_id is required"})
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.ErrorContext(ctx, "websocket upgrade failed", "error", err)
		return
	}

	_ = conn.SetReadDeadline(time.Now().Add(h.cfg.WSIdleTimeoutDuration()))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(h.cfg.WSIdleTimeoutDuration()))
	})

	var hello models.WSMessage
	if err := conn.ReadJSON(&hello); err != nil {
		slog.WarnContext(ctx, "failed to read websocket handshake", "error", err)
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseUnsupportedData, "invalid handshake"), time.Now().Add(h.cfg.WSWriteTimeoutDuration()))
		conn.Close()
		return
	}
	if hello.Type != models.WSMessageTypeHello && hello.Type != models.WSMessageTypeResume {
		slog.WarnContext(ctx, "invalid websocket handshake type", "type", hello.Type)
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseUnsupportedData, "invalid handshake type"), time.Now().Add(h.cfg.WSWriteTimeoutDuration()))
		conn.Close()
		return
	}
	if hello.PeerID == "" {
		hello.PeerID = queryPeerID
	}
	if hello.PeerID != queryPeerID {
		slog.WarnContext(ctx, "websocket peer mismatch during handshake", "query_peer_id", queryPeerID, "message_peer_id", hello.PeerID)
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "peer mismatch"), time.Now().Add(h.cfg.WSWriteTimeoutDuration()))
		conn.Close()
		return
	}

	scope := models.ListenerWSScope(queryPeerID)
	state, err := h.rdb.GetWSSequenceState(ctx, scope)
	if err != nil {
		slog.ErrorContext(ctx, "failed to load websocket sequence state", "error", err)
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "state load failed"), time.Now().Add(h.cfg.WSWriteTimeoutDuration()))
		conn.Close()
		return
	}

	session := &wsServerSession{
		handlers:        h,
		peerID:          queryPeerID,
		scope:           scope,
		conn:            conn,
		sendCh:          make(chan models.WSMessage, 16),
		notifyCh:        make(chan struct{}, 1),
		doneCh:          make(chan struct{}),
		lastSentSeq:     hello.LastReceivedSeq,
		lastReceivedSeq: 0,
	}
	if state != nil {
		session.lastReceivedSeq = state.LastReceivedSeq
	}

	if prev := h.wsHub.Register(queryPeerID, session); prev != nil {
		prev.shutdown(websocket.ClosePolicyViolation, "replaced by newer session")
	}
	defer h.wsHub.Unregister(queryPeerID, session)

	go session.writerLoop()

	replyType := models.WSMessageTypeHello
	if hello.Type == models.WSMessageTypeResume {
		replyType = models.WSMessageTypeResume
	}
	if !session.send(models.WSMessage{
		Type:            replyType,
		PeerID:          queryPeerID,
		LastReceivedSeq: session.lastReceivedSeq,
		SentAt:          time.Now().UnixMilli(),
	}) {
		session.shutdown(websocket.CloseInternalServerErr, "handshake reply failed")
		return
	}
	session.touchPeer()
	h.wsHub.Notify(queryPeerID)
	session.readLoop()
}

func (s *wsServerSession) send(msg models.WSMessage) bool {
	select {
	case <-s.doneCh:
		return false
	case s.sendCh <- msg:
		return true
	}
}

func (s *wsServerSession) shutdown(code int, reason string) {
	s.closeOnce.Do(func() {
		close(s.doneCh)
		_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(s.handlers.cfg.WSWriteTimeoutDuration()))
		_ = s.conn.Close()
	})
}

func (s *wsServerSession) touchPeer() {
	if time.Since(s.lastPeerSeenRefresh) < 30*time.Second {
		return
	}
	s.lastPeerSeenRefresh = time.Now()
	_ = s.handlers.rdb.UpdatePeerLastSeen(context.Background(), s.peerID)
}

func (s *wsServerSession) writerLoop() {
	pingTicker := time.NewTicker(s.handlers.cfg.WSPingIntervalDuration())
	defer pingTicker.Stop()
	keepaliveTicker := time.NewTicker(s.handlers.cfg.WSKeepaliveIntervalDuration())
	defer keepaliveTicker.Stop()

	for {
		select {
		case <-s.doneCh:
			return
		case msg := <-s.sendCh:
			if err := s.writeJSON(msg); err != nil {
				slog.WarnContext(context.Background(), "websocket write failed", "error", err, "peer_id", s.peerID)
				s.shutdown(websocket.CloseAbnormalClosure, "write failure")
				return
			}
		case <-s.notifyCh:
			if err := s.flushOutbox(); err != nil {
				slog.WarnContext(context.Background(), "failed to flush websocket outbox", "error", err, "peer_id", s.peerID)
				s.shutdown(websocket.CloseInternalServerErr, "outbox flush failed")
				return
			}
		case <-pingTicker.C:
			if err := s.conn.WriteControl(websocket.PingMessage, []byte("relayra"), time.Now().Add(s.handlers.cfg.WSWriteTimeoutDuration())); err != nil {
				s.shutdown(websocket.CloseAbnormalClosure, "ping failure")
				return
			}
		case <-keepaliveTicker.C:
			_ = s.writeJSON(models.WSMessage{
				Type:   models.WSMessageTypeKeepalive,
				PeerID: s.peerID,
				Probe: &models.ProbeMessage{
					ID:     fmt.Sprintf("listener-%d", time.Now().UnixNano()),
					SentAt: time.Now().UnixMilli(),
				},
				SentAt: time.Now().UnixMilli(),
			})
		}
	}
}

func (s *wsServerSession) flushOutbox() error {
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()

	entries, err := s.handlers.rdb.ListWSOutbox(context.Background(), s.scope, s.lastSentSeq, 64)
	if err != nil {
		if errors.Is(err, store.ErrWSOutboxGap) {
			if repairErr := s.handlers.rebuildListenerWSOutbox(context.Background(), s.peerID, s.lastSentSeq); repairErr != nil {
				return fmt.Errorf("repair listener websocket outbox: %w", repairErr)
			}
			entries, err = s.handlers.rdb.ListWSOutbox(context.Background(), s.scope, s.lastSentSeq, 64)
		}
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		msg, err := transport.DecodeWSOutboxMessage(entry)
		if err != nil {
			return err
		}
		if err := s.writeJSON(*msg); err != nil {
			return err
		}
		s.lastSentSeq = entry.Seq
	}
	return nil
}

func (s *wsServerSession) writeJSON(msg models.WSMessage) error {
	_ = s.conn.SetWriteDeadline(time.Now().Add(s.handlers.cfg.WSWriteTimeoutDuration()))
	return s.conn.WriteJSON(msg)
}

func (s *wsServerSession) readLoop() {
	baseCtx := logger.WithComponent(context.Background(), "server")
	baseCtx = logger.WithPeerID(baseCtx, s.peerID)

	for {
		var msg models.WSMessage
		if err := s.conn.ReadJSON(&msg); err != nil {
			slog.WarnContext(baseCtx, "websocket read failed", "error", err, "classification", classifyServerWebSocketError(err))
			s.shutdown(websocket.CloseGoingAway, "read failure")
			return
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(s.handlers.cfg.WSIdleTimeoutDuration()))
		s.touchPeer()

		if msg.PeerID != "" && msg.PeerID != s.peerID {
			slog.WarnContext(baseCtx, "websocket peer mismatch", "peer_id", msg.PeerID)
			s.shutdown(websocket.ClosePolicyViolation, "peer mismatch")
			return
		}

		if msg.Type == models.WSMessageTypeAck {
			s.outboxMu.Lock()
			_, err := s.handlers.rdb.AckWSOutboxThrough(baseCtx, s.scope, msg.Ack)
			s.outboxMu.Unlock()
			if err != nil {
				slog.WarnContext(baseCtx, "failed to ack websocket outbox", "error", err, "ack", msg.Ack)
				s.shutdown(websocket.CloseInternalServerErr, "ack handling failed")
				return
			}
			continue
		}
		if msg.Type == models.WSMessageTypeKeepalive {
			if msg.Probe != nil && !msg.Probe.Ack {
				_ = s.send(models.WSMessage{
					Type:   models.WSMessageTypeKeepalive,
					PeerID: s.peerID,
					Probe: &models.ProbeMessage{
						ID:       msg.Probe.ID,
						Sequence: msg.Probe.Sequence,
						SentAt:   msg.Probe.SentAt,
						Ack:      true,
					},
					SentAt: time.Now().UnixMilli(),
				})
			}
			continue
		}

		if msg.Seq > 0 {
			if msg.Seq <= s.lastReceivedSeq {
				_ = s.send(models.WSMessage{Type: models.WSMessageTypeAck, PeerID: s.peerID, Ack: s.lastReceivedSeq, SentAt: time.Now().UnixMilli()})
				continue
			}
			if msg.Seq != s.lastReceivedSeq+1 {
				slog.WarnContext(baseCtx, "out-of-order websocket sequence", "expected", s.lastReceivedSeq+1, "got", msg.Seq, "type", msg.Type)
				s.shutdown(websocket.CloseUnsupportedData, "out-of-order sequence")
				return
			}
		}

		if err := s.handleInbound(baseCtx, &msg); err != nil {
			slog.WarnContext(baseCtx, "failed to handle websocket message", "error", err, "type", msg.Type, "seq", msg.Seq)
			s.shutdown(websocket.CloseInternalServerErr, "message handling failed")
			return
		}
		if msg.Seq > 0 {
			if err := s.handlers.rdb.SetWSLastReceivedSeq(baseCtx, s.scope, msg.Seq); err != nil {
				slog.WarnContext(baseCtx, "failed to persist websocket receive cursor", "error", err, "seq", msg.Seq)
				s.shutdown(websocket.CloseInternalServerErr, "receive cursor update failed")
				return
			}
			s.lastReceivedSeq = msg.Seq
			_ = s.send(models.WSMessage{Type: models.WSMessageTypeAck, PeerID: s.peerID, Ack: msg.Seq, SentAt: time.Now().UnixMilli()})
		}
	}
}

func (s *wsServerSession) handleInbound(ctx context.Context, msg *models.WSMessage) error {
	switch msg.Type {
	case models.WSMessageTypeRequestState:
		if msg.RequestState == nil {
			return fmt.Errorf("request_state payload missing")
		}
		return s.handlers.rdb.ApplyRequestStates(ctx, []models.RequestSyncState{*msg.RequestState})
	case models.WSMessageTypeResult:
		if msg.Result == nil {
			return fmt.Errorf("result payload missing")
		}
		return s.handlers.storeSenderResultWS(ctx, s.peerID, msg.Result)
	case models.WSMessageTypeChunkReceipt:
		if msg.ChunkReceipt == nil {
			return fmt.Errorf("chunk_receipt payload missing")
		}
		return s.handlers.applySenderChunkReceiptWS(ctx, s.peerID, msg.ChunkReceipt)
	case models.WSMessageTypeHello, models.WSMessageTypeResume:
		return nil
	default:
		return fmt.Errorf("unsupported websocket message type %q", msg.Type)
	}
}

func classifyServerWebSocketError(err error) string {
	if err == nil {
		return ""
	}
	if closeErr, ok := err.(*websocket.CloseError); ok {
		return fmt.Sprintf("close:%d:%s", closeErr.Code, closeErr.Text)
	}
	if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
		return "idle-timeout"
	}
	return err.Error()
}
