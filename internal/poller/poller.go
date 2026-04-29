package poller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/crypto"
	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/models"
	"github.com/relayra/relayra/internal/proxy"
	"github.com/relayra/relayra/internal/store"
)

const activeWorkPollInterval = 250 * time.Millisecond

// Run starts the Sender runtime.
func Run(ctx context.Context, cfg *config.Config, rdb store.Backend) error {
	ctx = logger.WithComponent(ctx, "poller")

	listenerInfo, err := rdb.GetListenerInfo(ctx)
	if err != nil || listenerInfo == nil {
		return fmt.Errorf("no Listener paired — run 'relayra pair connect <token>' first")
	}

	proxyMgr := proxy.NewManager(rdb, cfg.ProxyCooldown())
	proxyCount, _ := proxyMgr.Count(ctx)
	mode := cfg.NormalizedTransportMode()

	slog.InfoContext(ctx, "Sender starting",
		"listener_name", listenerInfo.Name,
		"listener_addr", listenerInfo.Address,
		"proxy_count", proxyCount,
		"poll_interval", cfg.PollInterval,
		"batch_size", cfg.PollBatchSize,
		"transport_mode", mode,
		"long_poll_wait", cfg.LongPollWait,
		"async_workers", cfg.AsyncWorkers,
		"proxy_cooldown_seconds", cfg.ProxyCooldownSeconds,
	)
	dispatcher := newDispatcher(cfg, rdb)
	defer dispatcher.Close()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	if mode == config.TransportModeWebSocket {
		return runWebSocketMode(ctx, cfg, rdb, listenerInfo, proxyMgr, dispatcher, sigCh)
	}
	return runHTTPMode(ctx, cfg, rdb, listenerInfo, proxyMgr, dispatcher, sigCh, mode == config.TransportModeLongPoll)
}

func runHTTPMode(ctx context.Context, cfg *config.Config, rdb store.Backend,
	listenerInfo *models.Peer, proxyMgr *proxy.Manager, dispatcher *dispatcher,
	sigCh <-chan os.Signal, longPoll bool) error {

	var cycle int64
	failureBackoff := time.Second

	if longPoll {
		for {
			select {
			case sig := <-sigCh:
				slog.InfoContext(ctx, "shutdown signal received", "signal", sig)
				return nil
			case <-ctx.Done():
				slog.InfoContext(ctx, "context cancelled")
				return nil
			default:
			}

			cycle++
			pollCtx := logger.WithPollCycle(ctx, cycle)
			success := doPollCycleHTTP(pollCtx, cfg, rdb, listenerInfo, proxyMgr, dispatcher, true)
			if success {
				failureBackoff = time.Second
				if dispatcher.InFlight() > 0 {
					if !sleepWithCancel(ctx, sigCh, activeWorkPollInterval) {
						return nil
					}
				}
				continue
			}

			if !sleepWithCancel(ctx, sigCh, failureBackoff) {
				return nil
			}
			if failureBackoff < 10*time.Second {
				failureBackoff *= 2
			}
		}
	}

	ticker := time.NewTicker(time.Duration(cfg.PollInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
		case sig := <-sigCh:
			slog.InfoContext(ctx, "shutdown signal received", "signal", sig)
			return nil
		case <-ctx.Done():
			slog.InfoContext(ctx, "context cancelled")
			return nil
		}

		cycle++
		pollCtx := logger.WithPollCycle(ctx, cycle)
		_ = doPollCycleHTTP(pollCtx, cfg, rdb, listenerInfo, proxyMgr, dispatcher, false)
	}
}

func doPollCycleHTTP(ctx context.Context, cfg *config.Config, rdb store.Backend,
	listenerInfo *models.Peer, proxyMgr *proxy.Manager, dispatcher *dispatcher, longPoll bool) bool {

	start := time.Now()
	waitSeconds := requestedPollWait(cfg, dispatcher, longPoll)
	slog.InfoContext(ctx, "poll cycle starting",
		"wait_seconds", waitSeconds,
		"in_flight", dispatcher.InFlight(),
	)

	payloadUp, leasedResults, err := buildPayloadUp(ctx, cfg, rdb)
	if err != nil {
		slog.ErrorContext(ctx, "failed to build outbound sync payload", "error", err)
		_ = leasedResults
		return false
	}

	ciphertext, nonce, timestamp, err := crypto.EncryptJSON(listenerInfo.EncryptionKey, payloadUp)
	if err != nil {
		slog.ErrorContext(ctx, "failed to encrypt poll payload", "error", err)
		return false
	}

	pollReq := models.PollRequest{
		PeerID:      listenerInfo.ID,
		Nonce:       nonce,
		Timestamp:   timestamp,
		Payload:     ciphertext,
		WaitSeconds: waitSeconds,
	}
	reqBody, _ := json.Marshal(pollReq)

	transport, proxyURL, err := proxyMgr.GetTransport(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "no proxy available", "error", err)
		return false
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   pollRequestTimeout(cfg, longPoll),
	}

	pollURL := fmt.Sprintf("http://%s/api/v1/poll", listenerInfo.Address)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, pollURL, bytes.NewReader(reqBody))
	if err != nil {
		slog.ErrorContext(ctx, "failed to create poll request", "error", err)
		return false
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			slog.InfoContext(ctx, "poll request cancelled during shutdown", "error", err)
			return false
		}
		slog.ErrorContext(ctx, "poll request failed",
			"error", err,
			"proxy", proxyURL,
			"listener", listenerInfo.Address,
		)
		proxyMgr.MarkFailed(ctx, proxyURL)
		return false
	}
	defer resp.Body.Close()

	proxyMgr.MarkSuccess(ctx, proxyURL)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.ErrorContext(ctx, "failed to read poll response", "error", err)
		return false
	}
	if resp.StatusCode != http.StatusOK {
		slog.ErrorContext(ctx, "poll response error",
			"status", resp.StatusCode,
			"body", truncateStr(string(respBody), 4096),
		)
		return false
	}

	var pollResp models.PollResponse
	if err := json.Unmarshal(respBody, &pollResp); err != nil {
		slog.ErrorContext(ctx, "failed to parse poll response", "error", err)
		return false
	}

	var payloadDown models.PollPayloadDown
	if err := crypto.DecryptJSON(listenerInfo.EncryptionKey, pollResp.Payload, pollResp.Nonce, pollResp.Timestamp, &payloadDown); err != nil {
		slog.ErrorContext(ctx, "failed to decrypt poll response", "error", err)
		return false
	}

	if err := processPollResponse(ctx, cfg, rdb, dispatcher, &payloadDown); err != nil {
		slog.ErrorContext(ctx, "failed to process poll response", "error", err)
		return false
	}

	duration := time.Since(start)
	slog.InfoContext(ctx, "poll cycle completed",
		"new_requests", len(payloadDown.Requests),
		"acked_results", len(payloadDown.AckResultIDs),
		"results_sent", len(leasedResults),
		"known_request_states", len(payloadUp.RequestStates),
		"duration_ms", duration.Milliseconds(),
	)
	return true
}

func buildPayloadUp(ctx context.Context, cfg *config.Config, rdb store.Backend) (*models.PollPayloadUp, []models.RelayResult, error) {
	results, err := rdb.LeaseResults(ctx, cfg.PollBatchSize, senderResultLeaseDuration(cfg))
	if err != nil {
		return nil, nil, err
	}
	requestStates, err := rdb.ListSenderRequestStates(ctx)
	if err != nil {
		return nil, nil, err
	}
	return &models.PollPayloadUp{
		Results:       results,
		RequestStates: requestStates,
	}, results, nil
}

func processPollResponse(ctx context.Context, cfg *config.Config, rdb store.Backend, dispatcher *dispatcher, payloadDown *models.PollPayloadDown) error {
	if len(payloadDown.AckResultIDs) > 0 {
		if err := rdb.AckResults(ctx, payloadDown.AckResultIDs); err != nil {
			return err
		}
	}

	for _, req := range payloadDown.Requests {
		if err := handleIncomingRequest(ctx, cfg, rdb, dispatcher, req); err != nil {
			slog.ErrorContext(logger.WithRequestID(ctx, req.ID), "failed to handle incoming request", "error", err)
		}
	}
	return nil
}

func handleIncomingRequest(ctx context.Context, cfg *config.Config, rdb store.Backend, dispatcher *dispatcher, req models.RelayRequest) error {
	reqCtx := logger.WithRequestID(ctx, req.ID)
	now := time.Now()
	leaseUntil := now.Add(senderRequestLeaseDuration(cfg))

	state, err := rdb.GetSenderRequestState(reqCtx, req.ID)
	if err != nil {
		return err
	}
	pendingResult, err := rdb.ResultPending(reqCtx, req.ID)
	if err != nil {
		return err
	}

	shouldDispatch := false
	switch {
	case state == nil:
		shouldDispatch = true
	case state.Status == models.StatusCompleted:
		shouldDispatch = false
	case pendingResult:
		shouldDispatch = false
	case state.LeaseUntil.IsZero() || !state.LeaseUntil.After(now):
		shouldDispatch = true
	}

	receivedState := &models.RequestSyncState{
		RequestID:  req.ID,
		Status:     models.StatusReceived,
		LeaseUntil: leaseUntil,
		UpdatedAt:  now,
	}
	if err := rdb.StoreSenderRequestState(reqCtx, receivedState); err != nil {
		return err
	}

	if !shouldDispatch {
		slog.InfoContext(reqCtx, "duplicate request received; keeping durable state without redispatch",
			"status", receivedState.Status,
			"pending_result", pendingResult,
		)
		return nil
	}

	dispatcher.Dispatch(reqCtx, req)
	return nil
}

func truncateStr(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "...(truncated)"
	}
	return s
}

func requestedPollWait(cfg *config.Config, dispatcher *dispatcher, longPoll bool) int {
	if !longPoll {
		return 0
	}
	if dispatcher.InFlight() > 0 {
		return 0
	}
	return cfg.LongPollWait
}

func pollRequestTimeout(cfg *config.Config, longPoll bool) time.Duration {
	if !longPoll {
		return time.Duration(cfg.RequestTimeout) * time.Second
	}

	timeoutSeconds := cfg.LongPollWait + 15
	if timeoutSeconds < cfg.RequestTimeout {
		timeoutSeconds = cfg.RequestTimeout
	}
	return time.Duration(timeoutSeconds) * time.Second
}

func senderRequestLeaseDuration(cfg *config.Config) time.Duration {
	seconds := cfg.RequestTimeout + cfg.LongPollWait + cfg.PollInterval + 30
	if seconds < cfg.RequestTimeout+30 {
		seconds = cfg.RequestTimeout + 30
	}
	return time.Duration(seconds) * time.Second
}

func senderResultLeaseDuration(cfg *config.Config) time.Duration {
	seconds := cfg.RequestTimeout + cfg.LongPollWait + cfg.PollInterval + 30
	if seconds < cfg.RequestTimeout+30 {
		seconds = cfg.RequestTimeout + 30
	}
	return time.Duration(seconds) * time.Second
}

func sleepWithCancel(ctx context.Context, sigCh <-chan os.Signal, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case sig := <-sigCh:
		slog.InfoContext(ctx, "shutdown signal received", "signal", sig)
		return false
	case <-ctx.Done():
		slog.InfoContext(ctx, "context cancelled")
		return false
	}
}
