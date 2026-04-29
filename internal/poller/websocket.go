package poller

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/gorilla/websocket"
	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/crypto"
	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/models"
	"github.com/relayra/relayra/internal/proxy"
	"github.com/relayra/relayra/internal/store"
	xproxy "golang.org/x/net/proxy"
)

func runWebSocketMode(ctx context.Context, cfg *config.Config, rdb store.Backend,
	listenerInfo *models.Peer, proxyMgr *proxy.Manager, dispatcher *dispatcher,
	sigCh <-chan os.Signal) error {

	var cycle int64
	wsBackoff := time.Second

	for {
		err := runWebSocketSession(ctx, cfg, rdb, listenerInfo, proxyMgr, dispatcher, &cycle, sigCh)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}

		slog.WarnContext(ctx, "websocket transport failed, falling back to long-poll",
			"error", err,
			"retry_in", wsBackoff,
		)

		retryDeadline := time.Now().Add(wsBackoff)
		if wsBackoff < 30*time.Second {
			wsBackoff *= 2
		}

		for time.Now().Before(retryDeadline) {
			cycle++
			pollCtx := logger.WithPollCycle(ctx, cycle)
			_ = doPollCycleHTTP(pollCtx, cfg, rdb, listenerInfo, proxyMgr, dispatcher, true)
			if !sleepWithCancel(ctx, sigCh, activeWorkPollInterval) {
				return nil
			}
		}
	}
}

func runWebSocketSession(ctx context.Context, cfg *config.Config, rdb store.Backend,
	listenerInfo *models.Peer, proxyMgr *proxy.Manager, dispatcher *dispatcher,
	cycle *int64, sigCh <-chan os.Signal) error {

	_, proxyURL, err := proxyMgr.GetTransport(ctx)
	if err != nil {
		return err
	}

	dialer, err := websocketDialerForProxy(proxyURL, cfg.RequestTimeout)
	if err != nil {
		proxyMgr.MarkFailed(ctx, proxyURL)
		return err
	}

	wsURL := fmt.Sprintf("ws://%s/api/v1/ws?peer_id=%s", listenerInfo.Address, url.QueryEscape(listenerInfo.ID))
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		proxyMgr.MarkFailed(ctx, proxyURL)
		return err
	}
	defer conn.Close()

	proxyMgr.MarkSuccess(ctx, proxyURL)
	slog.InfoContext(ctx, "websocket transport established", "proxy", proxyURL, "listener", listenerInfo.Address)

	_ = conn.SetReadDeadline(time.Now().Add(pollRequestTimeout(cfg, true) + 30*time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pollRequestTimeout(cfg, true) + 30*time.Second))
	})

	pingTicker := time.NewTicker(20 * time.Second)
	defer pingTicker.Stop()

	go func() {
		for {
			select {
			case <-pingTicker.C:
				_ = conn.WriteControl(websocket.PingMessage, []byte("relayra"), time.Now().Add(5*time.Second))
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case sig := <-sigCh:
			slog.InfoContext(ctx, "shutdown signal received", "signal", sig)
			return nil
		case <-ctx.Done():
			return nil
		default:
		}

		*cycle = *cycle + 1
		pollCtx := logger.WithPollCycle(ctx, *cycle)

		payloadUp, leasedResults, err := buildPayloadUp(pollCtx, cfg, rdb)
		if err != nil {
			return err
		}
		ciphertext, nonce, timestamp, err := crypto.EncryptJSON(listenerInfo.EncryptionKey, payloadUp)
		if err != nil {
			return err
		}

		req := models.PollRequest{
			PeerID:      listenerInfo.ID,
			Nonce:       nonce,
			Timestamp:   timestamp,
			Payload:     ciphertext,
			WaitSeconds: requestedPollWait(cfg, dispatcher, true),
		}

		_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if err := conn.WriteJSON(req); err != nil {
			proxyMgr.MarkFailed(pollCtx, proxyURL)
			return err
		}

		var resp models.PollResponse
		if err := conn.ReadJSON(&resp); err != nil {
			proxyMgr.MarkFailed(pollCtx, proxyURL)
			return err
		}

		var payloadDown models.PollPayloadDown
		if err := crypto.DecryptJSON(listenerInfo.EncryptionKey, resp.Payload, resp.Nonce, resp.Timestamp, &payloadDown); err != nil {
			return err
		}
		if err := processPollResponse(pollCtx, cfg, rdb, dispatcher, &payloadDown); err != nil {
			return err
		}

		slog.InfoContext(pollCtx, "websocket sync completed",
			"new_requests", len(payloadDown.Requests),
			"acked_results", len(payloadDown.AckResultIDs),
			"results_sent", len(leasedResults),
			"known_request_states", len(payloadUp.RequestStates),
		)

		if dispatcher.InFlight() > 0 {
			if !sleepWithCancel(ctx, sigCh, activeWorkPollInterval) {
				return nil
			}
		}
	}
}

func websocketDialerForProxy(proxyURL string, requestTimeout int) (*websocket.Dialer, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: time.Duration(requestTimeout+15) * time.Second,
	}

	if proxyURL == "" || proxyURL == "direct" {
		return &dialer, nil
	}

	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}

	switch parsed.Scheme {
	case "http", "https":
		dialer.Proxy = http.ProxyURL(parsed)
		dialer.NetDialContext = (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext
	case "socks5", "socks5h":
		var auth *xproxy.Auth
		if parsed.User != nil {
			auth = &xproxy.Auth{User: parsed.User.Username()}
			auth.Password, _ = parsed.User.Password()
		}
		base, err := xproxy.SOCKS5("tcp", parsed.Host, auth, xproxy.Direct)
		if err != nil {
			return nil, err
		}
		ctxDialer, ok := base.(xproxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("SOCKS5 dialer does not support context")
		}
		dialer.NetDialContext = ctxDialer.DialContext
	default:
		return nil, fmt.Errorf("unsupported proxy scheme: %s", parsed.Scheme)
	}

	return &dialer, nil
}
