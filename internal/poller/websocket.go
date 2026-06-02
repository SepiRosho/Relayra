package poller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/models"
	"github.com/relayra/relayra/internal/proxy"
	"github.com/relayra/relayra/internal/store"
	"github.com/relayra/relayra/internal/transport"
)

type webSocketFailureKind string

const (
	webSocketFailureNone       webSocketFailureKind = ""
	webSocketFailureConnection webSocketFailureKind = "connection"
	webSocketFailureInternal   webSocketFailureKind = "internal"
	webSocketFailureSelection  webSocketFailureKind = "selection"
)

type webSocketSessionResult struct {
	stable      bool
	shutdown    bool
	err         error
	proxyURL    string
	failureKind webSocketFailureKind
}

type senderWebSocketSession struct {
	cfg             *config.Config
	rdb             store.Backend
	listenerInfo    *models.Peer
	dispatcher      *dispatcher
	conn            *websocket.Conn
	scope           string
	sendCh          chan models.WSMessage
	notifyCh        chan struct{}
	doneCh          chan struct{}
	errCh           chan sessionErr
	outboxMu        sync.Mutex
	lastSentSeq     int64
	lastReceivedSeq int64
}

type sessionErr struct {
	err  error
	kind webSocketFailureKind
}

func runWebSocketMode(ctx context.Context, cfg *config.Config, rdb store.Backend,
	listenerInfo *models.Peer, proxyMgr *proxy.Manager, dispatcher *dispatcher,
	sigCh <-chan os.Signal) error {

	var cycle int64
	wsBackoff := cfg.WSReconnectBaseDuration()

	for {
		result := runWebSocketSession(ctx, cfg, rdb, listenerInfo, proxyMgr, dispatcher, &cycle, sigCh)
		if result.shutdown || result.err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}

		classification := classifyRuntimeWebSocketError(result.err)

		if !cfg.WSEnableLongPollFallback && result.stable && result.failureKind == webSocketFailureConnection {
			slog.WarnContext(ctx, "websocket transport dropped, reconnecting immediately",
				"error", result.err,
				"classification", classification,
				"proxy", result.proxyURL,
			)
			wsBackoff = cfg.WSReconnectBaseDuration()
			continue
		}

		if result.failureKind == webSocketFailureConnection && result.proxyURL != "" && !result.stable {
			proxyMgr.MarkFailed(ctx, result.proxyURL)
		}

		if cfg.WSEnableLongPollFallback {
			slog.WarnContext(ctx, "websocket transport failed, falling back to long-poll",
				"error", result.err,
				"classification", classification,
				"retry_in", wsBackoff,
			)

			cycle++
			pollCtx := logger.WithPollCycle(ctx, cycle)
			_ = doPollCycleHTTP(pollCtx, cfg, rdb, listenerInfo, proxyMgr, dispatcher, true)

			if result.stable {
				wsBackoff = cfg.WSReconnectBaseDuration()
			}
			retryDeadline := time.Now().Add(wsBackoff)
			nextBackoff := nextWebSocketBackoff(wsBackoff, cfg.WSReconnectMaxDuration())

			for time.Now().Before(retryDeadline) {
				cycle++
				pollCtx := logger.WithPollCycle(ctx, cycle)
				_ = doPollCycleHTTP(pollCtx, cfg, rdb, listenerInfo, proxyMgr, dispatcher, true)
				if !sleepWithCancel(ctx, sigCh, activeWorkPollInterval) {
					return nil
				}
			}
			wsBackoff = nextBackoff
			continue
		}

		retryDelay := wsBackoff
		if result.stable {
			retryDelay = cfg.WSReconnectBaseDuration()
			wsBackoff = cfg.WSReconnectBaseDuration()
		} else {
			wsBackoff = nextWebSocketBackoff(wsBackoff, cfg.WSReconnectMaxDuration())
		}

		slog.WarnContext(ctx, "websocket transport failed, reconnecting websocket",
			"error", result.err,
			"classification", classification,
			"retry_in", retryDelay,
		)
		if !sleepWithCancel(ctx, sigCh, retryDelay) {
			return nil
		}
	}
}

func runWebSocketSession(ctx context.Context, cfg *config.Config, rdb store.Backend,
	listenerInfo *models.Peer, proxyMgr *proxy.Manager, dispatcher *dispatcher,
	cycle *int64, sigCh <-chan os.Signal) webSocketSessionResult {

	result := webSocketSessionResult{}
	_, proxyURL, err := proxyMgr.GetTransport(ctx)
	if err != nil {
		result.err = err
		result.failureKind = webSocketFailureSelection
		return result
	}
	result.proxyURL = proxyURL

	dialer, err := proxy.WebSocketDialerForProxy(proxyURL, time.Duration(cfg.RequestTimeout+15)*time.Second)
	if err != nil {
		result.err = err
		result.failureKind = webSocketFailureConnection
		return result
	}

	wsURL := fmt.Sprintf("ws://%s/api/v1/ws?peer_id=%s", listenerInfo.Address, url.QueryEscape(listenerInfo.ID))
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		result.err = err
		result.failureKind = webSocketFailureConnection
		return result
	}
	defer conn.Close()

	scope := models.SenderWSScope(listenerInfo.ID)
	state, err := rdb.GetWSSequenceState(ctx, scope)
	if err != nil {
		result.err = err
		result.failureKind = webSocketFailureInternal
		return result
	}
	lastReceivedSeq := int64(0)
	if state != nil {
		lastReceivedSeq = state.LastReceivedSeq
	}

	helloType := models.WSMessageTypeHello
	if lastReceivedSeq > 0 {
		helloType = models.WSMessageTypeResume
	}
	_ = conn.SetWriteDeadline(time.Now().Add(cfg.WSWriteTimeoutDuration()))
	if err := conn.WriteJSON(models.WSMessage{
		Type:            helloType,
		PeerID:          listenerInfo.ID,
		LastReceivedSeq: lastReceivedSeq,
		SentAt:          time.Now().UnixMilli(),
	}); err != nil {
		result.err = err
		result.failureKind = webSocketFailureConnection
		return result
	}

	_ = conn.SetReadDeadline(time.Now().Add(cfg.WSIdleTimeoutDuration()))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(cfg.WSIdleTimeoutDuration()))
	})

	var helloResp models.WSMessage
	if err := conn.ReadJSON(&helloResp); err != nil {
		result.err = err
		result.failureKind = classifyWebSocketReadFailureKind(err)
		return result
	}
	if helloResp.Type != models.WSMessageTypeHello && helloResp.Type != models.WSMessageTypeResume {
		result.err = fmt.Errorf("unexpected websocket handshake response %q", helloResp.Type)
		result.failureKind = webSocketFailureInternal
		return result
	}
	_ = conn.SetReadDeadline(time.Now().Add(cfg.WSIdleTimeoutDuration()))

	session := &senderWebSocketSession{
		cfg:             cfg,
		rdb:             rdb,
		listenerInfo:    listenerInfo,
		dispatcher:      dispatcher,
		conn:            conn,
		scope:           scope,
		sendCh:          make(chan models.WSMessage, 16),
		notifyCh:        dispatcher.outboxSignal,
		doneCh:          make(chan struct{}),
		errCh:           make(chan sessionErr, 1),
		lastSentSeq:     helloResp.LastReceivedSeq,
		lastReceivedSeq: lastReceivedSeq,
	}

	slog.InfoContext(ctx, "websocket transport established", "proxy", proxyURL, "listener", listenerInfo.Address)
	go session.writerLoop()
	session.NotifyFlush()
	result.stable = true
	defer close(session.doneCh)

	for {
		select {
		case sig := <-sigCh:
			slog.InfoContext(ctx, "shutdown signal received", "signal", sig)
			_ = writeWebSocketClose(conn, cfg, websocket.CloseNormalClosure, "shutdown")
			result.shutdown = true
			return result
		case <-ctx.Done():
			_ = writeWebSocketClose(conn, cfg, websocket.CloseNormalClosure, "context cancelled")
			result.shutdown = true
			return result
		default:
		}

		var msg models.WSMessage
		if err := conn.ReadJSON(&msg); err != nil {
			select {
			case writerErr := <-session.errCh:
				result.err = writerErr.err
				result.failureKind = writerErr.kind
				return result
			default:
			}
			result.err = err
			result.failureKind = classifyWebSocketReadFailureKind(err)
			return result
		}
		_ = conn.SetReadDeadline(time.Now().Add(cfg.WSIdleTimeoutDuration()))

		if msg.Type == models.WSMessageTypeAck {
			session.outboxMu.Lock()
			ackErr := handleSenderOutboxAck(ctx, rdb, scope, msg.Ack)
			session.outboxMu.Unlock()
			if ackErr != nil {
				result.err = ackErr
				result.failureKind = webSocketFailureInternal
				return result
			}
			continue
		}

		if msg.Type == models.WSMessageTypeKeepalive {
			if msg.Probe != nil && !msg.Probe.Ack {
				if !session.Send(models.WSMessage{
					Type:   models.WSMessageTypeKeepalive,
					PeerID: listenerInfo.ID,
					Probe: &models.ProbeMessage{
						ID:       msg.Probe.ID,
						Sequence: msg.Probe.Sequence,
						SentAt:   msg.Probe.SentAt,
						Ack:      true,
					},
					SentAt: time.Now().UnixMilli(),
				}) {
					result.err = fmt.Errorf("failed to send keepalive ack")
					result.failureKind = webSocketFailureConnection
					return result
				}
			}
			continue
		}

		if msg.Seq > 0 {
			if msg.Seq <= session.lastReceivedSeq {
				_ = session.Send(models.WSMessage{Type: models.WSMessageTypeAck, PeerID: listenerInfo.ID, Ack: session.lastReceivedSeq, SentAt: time.Now().UnixMilli()})
				continue
			}
			if msg.Seq != session.lastReceivedSeq+1 {
				result.err = fmt.Errorf("out-of-order websocket sequence expected %d got %d", session.lastReceivedSeq+1, msg.Seq)
				result.failureKind = webSocketFailureInternal
				return result
			}
		}

		if err := session.handleInbound(ctx, &msg, cycle); err != nil {
			result.err = err
			result.failureKind = webSocketFailureInternal
			return result
		}
		if msg.Seq > 0 {
			if err := rdb.SetWSLastReceivedSeq(ctx, scope, msg.Seq); err != nil {
				result.err = err
				result.failureKind = webSocketFailureInternal
				return result
			}
			session.lastReceivedSeq = msg.Seq
			if !session.Send(models.WSMessage{Type: models.WSMessageTypeAck, PeerID: listenerInfo.ID, Ack: msg.Seq, SentAt: time.Now().UnixMilli()}) {
				result.err = fmt.Errorf("failed to send websocket ack")
				result.failureKind = webSocketFailureConnection
				return result
			}
			proxyMgr.MarkSuccess(ctx, proxyURL)
		}
	}
}

func (s *senderWebSocketSession) NotifyFlush() {
	select {
	case s.notifyCh <- struct{}{}:
	default:
	}
}

func (s *senderWebSocketSession) Send(msg models.WSMessage) bool {
	select {
	case <-s.doneCh:
		return false
	case s.sendCh <- msg:
		return true
	}
}

func (s *senderWebSocketSession) writerLoop() {
	pingTicker := time.NewTicker(s.cfg.WSPingIntervalDuration())
	defer pingTicker.Stop()
	keepaliveTicker := time.NewTicker(s.cfg.WSKeepaliveIntervalDuration())
	defer keepaliveTicker.Stop()

	for {
		select {
		case <-s.doneCh:
			return
		case msg := <-s.sendCh:
			if err := s.writeJSON(msg); err != nil {
				s.errCh <- sessionErr{err: err, kind: webSocketFailureConnection}
				_ = s.conn.Close()
				return
			}
		case <-s.notifyCh:
			if err := s.flushOutbox(); err != nil {
				s.errCh <- sessionErr{err: err, kind: webSocketFailureConnection}
				_ = s.conn.Close()
				return
			}
		case <-pingTicker.C:
			if err := s.conn.WriteControl(websocket.PingMessage, []byte("relayra"), time.Now().Add(s.cfg.WSWriteTimeoutDuration())); err != nil {
				s.errCh <- sessionErr{err: err, kind: webSocketFailureConnection}
				_ = s.conn.Close()
				return
			}
		case <-keepaliveTicker.C:
			_ = s.writeJSON(models.WSMessage{
				Type:   models.WSMessageTypeKeepalive,
				PeerID: s.listenerInfo.ID,
				Probe: &models.ProbeMessage{
					ID:     fmt.Sprintf("sender-%d", time.Now().UnixNano()),
					SentAt: time.Now().UnixMilli(),
				},
				SentAt: time.Now().UnixMilli(),
			})
		}
	}
}

func (s *senderWebSocketSession) flushOutbox() error {
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()

	for {
		state, err := s.rdb.GetWSSequenceState(context.Background(), s.scope)
		if err != nil {
			return err
		}
		if state == nil || state.NextOutboundSeq <= s.lastSentSeq {
			return nil
		}

		entries, err := s.rdb.ListWSOutbox(context.Background(), s.scope, s.lastSentSeq, 64)
		if err != nil {
			if errors.Is(err, store.ErrWSOutboxGap) {
				if repairErr := rebuildSenderWSOutbox(context.Background(), s.cfg, s.rdb, s.listenerInfo.ID, s.lastSentSeq); repairErr != nil {
					return fmt.Errorf("repair sender websocket outbox: %w", repairErr)
				}
				continue
			}
			return err
		}
		if len(entries) == 0 {
			return nil
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
	}
}

func rebuildSenderWSOutbox(ctx context.Context, cfg *config.Config, rdb store.Backend, listenerID string, afterSeq int64) error {
	scope := models.SenderWSScope(listenerID)
	if err := rdb.ResetWSOutbox(ctx, scope, afterSeq); err != nil {
		return err
	}

	results, err := rdb.LeaseResults(ctx, cfg.PollBatchSize, senderResultLeaseDuration(cfg))
	if err != nil {
		return err
	}
	for i := range results {
		result := results[i]
		if err := queueSenderResultWS(ctx, rdb, listenerID, &result, cfg.TransportChunkSize()); err != nil {
			return err
		}
	}

	requestStates, err := rdb.ListSenderRequestStates(ctx)
	if err != nil {
		return err
	}
	for i := range requestStates {
		state := requestStates[i]
		if _, err := transport.EnqueueWSMessage(ctx, rdb, scope, &models.WSMessage{
			Type:         models.WSMessageTypeRequestState,
			PeerID:       listenerID,
			RequestState: &state,
		}, state.RequestID); err != nil {
			return err
		}
	}

	chunkReceipts, err := rdb.ListChunkReceipts(ctx)
	if err != nil {
		return err
	}
	for i := range chunkReceipts {
		receipt := chunkReceipts[i]
		if _, err := transport.EnqueueWSMessage(ctx, rdb, scope, &models.WSMessage{
			Type:         models.WSMessageTypeChunkReceipt,
			PeerID:       listenerID,
			ChunkReceipt: &receipt,
		}, receipt.TransferID); err != nil {
			return err
		}
	}

	return nil
}

func (s *senderWebSocketSession) writeJSON(msg models.WSMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal websocket message: %w", err)
	}
	// Scale the write deadline by payload size so large results (e.g. multi-MB
	// response bodies) don't time out mid-write. Assume at least 256 KB/s
	// sustained throughput as a conservative floor.
	const minThroughputBPS = 256 * 1024
	extra := time.Duration(len(data)/minThroughputBPS) * time.Second
	_ = s.conn.SetWriteDeadline(time.Now().Add(s.cfg.WSWriteTimeoutDuration() + extra))
	return s.conn.WriteMessage(websocket.TextMessage, data)
}

func (s *senderWebSocketSession) handleInbound(ctx context.Context, msg *models.WSMessage, cycle *int64) error {
	switch msg.Type {
	case models.WSMessageTypePushRequest:
		if msg.Request == nil {
			return fmt.Errorf("push_request payload missing")
		}
		*cycle = *cycle + 1
		reqCtx := logger.WithPollCycle(ctx, *cycle)
		return handleIncomingRequest(reqCtx, s.cfg, s.rdb, s.dispatcher, *msg.Request)
	case models.WSMessageTypePushChunk:
		if msg.Chunk == nil {
			return fmt.Errorf("push_chunk payload missing")
		}
		receipt, req, err := s.rdb.StoreInboundChunk(ctx, *msg.Chunk, senderRequestLeaseDuration(s.cfg))
		if err != nil {
			return err
		}
		if receipt != nil {
			if err := queueSenderChunkReceiptWS(ctx, s.rdb, s.listenerInfo.ID, *receipt); err != nil {
				return err
			}
			s.dispatcher.NotifyOutbox()
		}
		if req != nil {
			*cycle = *cycle + 1
			reqCtx := logger.WithPollCycle(ctx, *cycle)
			if err := handleIncomingRequest(reqCtx, s.cfg, s.rdb, s.dispatcher, *req); err != nil {
				return err
			}
		}
		return nil
	case models.WSMessageTypeHello, models.WSMessageTypeResume:
		return nil
	default:
		return fmt.Errorf("unsupported websocket message type %q", msg.Type)
	}
}

func handleSenderOutboxAck(ctx context.Context, rdb store.Backend, scope string, ackSeq int64) error {
	acked, err := rdb.AckWSOutboxThrough(ctx, scope, ackSeq)
	if err != nil {
		return err
	}
	resultIDs := make([]string, 0)
	for _, entry := range acked {
		switch entry.Type {
		case models.WSMessageTypeResult, models.WSMessageTypeResultChunk:
			if entry.RefID != "" {
				resultIDs = append(resultIDs, entry.RefID)
			}
		case models.WSMessageTypeChunkReceipt:
			if entry.RefID != "" {
				if err := rdb.DeleteChunkReceipt(ctx, entry.RefID); err != nil {
					return err
				}
			}
		}
	}
	if len(resultIDs) > 0 {
		if err := rdb.AckResults(ctx, resultIDs); err != nil {
			return err
		}
	}
	return nil
}

func nextWebSocketBackoff(current, max time.Duration) time.Duration {
	if current < max {
		current *= 2
		if current > max {
			current = max
		}
	}
	return current
}

func classifyWebSocketReadFailureKind(err error) webSocketFailureKind {
	if err == nil {
		return webSocketFailureNone
	}
	if closeErr, ok := err.(*websocket.CloseError); ok {
		switch closeErr.Code {
		case websocket.CloseInternalServerErr, websocket.ClosePolicyViolation, websocket.CloseUnsupportedData:
			return webSocketFailureInternal
		case websocket.CloseNormalClosure:
			return webSocketFailureNone
		default:
			return webSocketFailureConnection
		}
	}
	if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
		return webSocketFailureConnection
	}
	return webSocketFailureConnection
}

func writeWebSocketClose(conn *websocket.Conn, cfg *config.Config, code int, reason string) error {
	return conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(cfg.WSWriteTimeoutDuration()))
}

func classifyRuntimeWebSocketError(err error) string {
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
