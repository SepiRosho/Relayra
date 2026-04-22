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
	"sync/atomic"
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
func Run(ctx context.Context, cfg *config.Config, rdb *store.Redis) error {
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
	)

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var cycle atomic.Int64
	ticker := time.NewTicker(time.Duration(cfg.PollInterval) * time.Second)
	defer ticker.Stop()

	// Track which request IDs we need to ack
	var pendingAcks []string

	// Do first poll immediately
	doFirstPoll := make(chan struct{}, 1)
	doFirstPoll <- struct{}{}

	for {
		select {
		case <-doFirstPoll:
			// Fall through to poll
		case <-ticker.C:
			// Regular poll interval
		case sig := <-sigCh:
			slog.InfoContext(ctx, "shutdown signal received", "signal", sig)
			return nil
		case <-ctx.Done():
			slog.InfoContext(ctx, "context cancelled")
			return nil
		}

		cycleNum := cycle.Add(1)
		pollCtx := logger.WithPollCycle(ctx, cycleNum)
		newAcks := doPollCycle(pollCtx, cfg, rdb, listenerInfo, proxyMgr, pendingAcks)
		pendingAcks = newAcks
	}
}

func doPollCycle(ctx context.Context, cfg *config.Config, rdb *store.Redis,
	listenerInfo *models.Peer, proxyMgr *proxy.Manager, ackRequestIDs []string) []string {

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
		return ackRequestIDs // Keep pending acks for next cycle
	}

	// Use the peer ID assigned to us during pairing (stored in listener info)
	peerID := listenerInfo.ID

	pollReq := models.PollRequest{
		PeerID:    peerID,
		Nonce:     nonce,
		Timestamp: timestamp,
		Payload:   ciphertext,
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
		return ackRequestIDs
	}

	slog.DebugContext(ctx, "using proxy", "proxy_url", proxyURL)

	// Send poll request
	client := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(cfg.RequestTimeout) * time.Second,
	}

	pollURL := fmt.Sprintf("http://%s/api/v1/poll", listenerInfo.Address)
	resp, err := client.Post(pollURL, "application/json", bytes.NewReader(reqBody))
	if err != nil {
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
		return ackRequestIDs
	}
	defer resp.Body.Close()

	// Mark proxy as successful
	proxyMgr.MarkSuccess(ctx, proxyURL)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.ErrorContext(ctx, "failed to read poll response", "error", err)
		return nil // Clear acks since results were sent
	}

	if resp.StatusCode != http.StatusOK {
		slog.ErrorContext(ctx, "poll response error",
			"status", resp.StatusCode,
			"body", truncateStr(string(respBody), 4096),
		)
		return nil
	}

	// Parse response
	var pollResp models.PollResponse
	if err := json.Unmarshal(respBody, &pollResp); err != nil {
		slog.ErrorContext(ctx, "failed to parse poll response", "error", err)
		return nil
	}

	// Decrypt response
	var payloadDown models.PollPayloadDown
	if err := crypto.DecryptJSON(listenerInfo.EncryptionKey, pollResp.Payload, pollResp.Nonce, pollResp.Timestamp, &payloadDown); err != nil {
		slog.ErrorContext(ctx, "failed to decrypt poll response", "error", err)
		return nil
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

	// Execute new requests sequentially
	var newAcks []string
	for _, req := range payloadDown.Requests {
		reqCtx := logger.WithRequestID(ctx, req.ID)
		slog.InfoContext(reqCtx, "executing request",
			"url", req.Request.URL,
			"method", req.Request.Method,
		)

		result := ExecuteRequest(reqCtx, &req, cfg.RequestTimeout)

		if err := rdb.PushResult(reqCtx, result); err != nil {
			slog.ErrorContext(reqCtx, "failed to store result locally", "error", err)
		}

		newAcks = append(newAcks, req.ID)
	}

	return newAcks
}

func truncateStr(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "...(truncated)"
	}
	return s
}
