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

// Run starts the Sender polling loop.
func Run(ctx context.Context, cfg *config.Config, rdb store.Backend) error {
	ctx = logger.WithComponent(ctx, "poller")

	// Get Listener info
	listenerInfo, err := rdb.GetListenerInfo(ctx)
	if err != nil || listenerInfo == nil {
		return fmt.Errorf("no Listener paired — run 'relayra pair connect <token>' first")
	}

	proxyMgr := proxy.NewManager(rdb)
	proxyCount, _ := proxyMgr.Count(ctx)

	slog.InfoContext(ctx, "Sender starting",
		"listener_name", listenerInfo.Name,
		"listener_addr", listenerInfo.Address,
		"proxy_count", proxyCount,
		"poll_interval", cfg.PollInterval,
		"batch_size", cfg.PollBatchSize,
		"long_polling", cfg.LongPolling,
		"long_poll_wait", cfg.LongPollWait,
		"async_workers", cfg.AsyncWorkers,
	)
	dispatcher := newDispatcher(cfg, rdb)
	defer dispatcher.Close()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	// Track which request IDs we need to ack
	var pendingAcks []string
	var cycle int64
	var failureBackoff time.Duration = time.Second

	if cfg.LongPolling {
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
			newAcks, success := doPollCycle(pollCtx, cfg, rdb, listenerInfo, proxyMgr, dispatcher, pendingAcks)
			pendingAcks = newAcks

			if success {
				failureBackoff = time.Second
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
		newAcks, _ := doPollCycle(pollCtx, cfg, rdb, listenerInfo, proxyMgr, dispatcher, pendingAcks)
		pendingAcks = newAcks
	}
}

func doPollCycle(ctx context.Context, cfg *config.Config, rdb store.Backend,
	listenerInfo *models.Peer, proxyMgr *proxy.Manager, dispatcher *dispatcher, ackRequestIDs []string) ([]string, bool) {

	start := time.Now()
	slog.InfoContext(ctx, "poll cycle starting", "pending_acks", len(ackRequestIDs))

	// Get pending results to send back
	results, err := rdb.PopResults(ctx, cfg.PollBatchSize)
	if err != nil {
		slog.ErrorContext(ctx, "failed to pop pending results", "error", err)
		results = nil
	}

	// Build poll payload
	payloadUp := models.PollPayloadUp{
		Results:       results,
		AckRequestIDs: ackRequestIDs,
	}

	// Encrypt
	ciphertext, nonce, timestamp, err := crypto.EncryptJSON(listenerInfo.EncryptionKey, &payloadUp)
	if err != nil {
		slog.ErrorContext(ctx, "failed to encrypt poll payload", "error", err)
		// Re-push results so they're not lost
		if len(results) > 0 {
			rdb.RePushResults(ctx, results)
		}
		return ackRequestIDs, false // Keep pending acks for next cycle
	}

	// Use the peer ID assigned to us during pairing (stored in listener info)
	peerID := listenerInfo.ID

	pollReq := models.PollRequest{
		PeerID:      peerID,
		Nonce:       nonce,
		Timestamp:   timestamp,
		Payload:     ciphertext,
		WaitSeconds: longPollWait(cfg),
	}

	reqBody, _ := json.Marshal(pollReq)

	// Get proxy transport
	transport, proxyURL, err := proxyMgr.GetTransport(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "no proxy available", "error", err)
		// Re-push results
		if len(results) > 0 {
			rdb.RePushResults(ctx, results)
		}
		return ackRequestIDs, false
	}

	slog.DebugContext(ctx, "using proxy", "proxy_url", proxyURL)

	// Send poll request
	client := &http.Client{
		Transport: transport,
		Timeout:   pollRequestTimeout(cfg),
	}

	pollURL := fmt.Sprintf("http://%s/api/v1/poll", listenerInfo.Address)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, pollURL, bytes.NewReader(reqBody))
	if err != nil {
		slog.ErrorContext(ctx, "failed to create poll request", "error", err)
		if len(results) > 0 {
			rdb.RePushResults(ctx, results)
		}
		return ackRequestIDs, false
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			slog.InfoContext(ctx, "poll request cancelled during shutdown", "error", err)
			if len(results) > 0 {
				rdb.RePushResults(ctx, results)
			}
			return ackRequestIDs, false
		}
		slog.ErrorContext(ctx, "poll request failed",
			"error", err,
			"proxy", proxyURL,
			"listener", listenerInfo.Address,
		)
		proxyMgr.MarkFailed(ctx, proxyURL)
		// Re-push results
		if len(results) > 0 {
			rdb.RePushResults(ctx, results)
		}
		return ackRequestIDs, false
	}
	defer resp.Body.Close()

	// Mark proxy as successful
	proxyMgr.MarkSuccess(ctx, proxyURL)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.ErrorContext(ctx, "failed to read poll response", "error", err)
		return ackRequestIDs, false
	}

	if resp.StatusCode != http.StatusOK {
		slog.ErrorContext(ctx, "poll response error",
			"status", resp.StatusCode,
			"body", truncateStr(string(respBody), 4096),
		)
		return ackRequestIDs, false
	}

	// Parse response
	var pollResp models.PollResponse
	if err := json.Unmarshal(respBody, &pollResp); err != nil {
		slog.ErrorContext(ctx, "failed to parse poll response", "error", err)
		return ackRequestIDs, false
	}

	// Decrypt response
	var payloadDown models.PollPayloadDown
	if err := crypto.DecryptJSON(listenerInfo.EncryptionKey, pollResp.Payload, pollResp.Nonce, pollResp.Timestamp, &payloadDown); err != nil {
		slog.ErrorContext(ctx, "failed to decrypt poll response", "error", err)
		return ackRequestIDs, false
	}

	duration := time.Since(start)
	slog.InfoContext(ctx, "poll cycle completed",
		"new_requests", len(payloadDown.Requests),
		"acked_results", len(payloadDown.AckResultIDs),
		"results_sent", len(results),
		"duration_ms", duration.Milliseconds(),
	)

	// Process acked results (Listener confirmed receipt)
	if len(payloadDown.AckResultIDs) > 0 {
		rdb.DeleteAckedResults(ctx, payloadDown.AckResultIDs)
	}

	// Dispatch new requests. Synchronous requests keep FIFO order through a
	// single worker; async requests run concurrently and can complete independently.
	var newAcks []string
	for _, req := range payloadDown.Requests {
		reqCtx := logger.WithRequestID(ctx, req.ID)
		dispatcher.Dispatch(reqCtx, req)
		newAcks = append(newAcks, req.ID)
	}

	return newAcks, true
}

func truncateStr(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "...(truncated)"
	}
	return s
}

func longPollWait(cfg *config.Config) int {
	if !cfg.LongPolling {
		return 0
	}
	return cfg.LongPollWait
}

func pollRequestTimeout(cfg *config.Config) time.Duration {
	if !cfg.LongPolling {
		return time.Duration(cfg.RequestTimeout) * time.Second
	}

	timeoutSeconds := cfg.LongPollWait + 15
	if timeoutSeconds < cfg.RequestTimeout {
		timeoutSeconds = cfg.RequestTimeout
	}
	return time.Duration(timeoutSeconds) * time.Second
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
