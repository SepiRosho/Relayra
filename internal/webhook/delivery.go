package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/models"
	"github.com/relayra/relayra/internal/store"
)

// webhookPayload is the JSON body sent to webhook URLs.
type webhookPayload struct {
	RequestID   string              `json:"request_id"`
	Result      *models.RelayResult `json:"result"`
	DeliveredAt time.Time           `json:"delivered_at"`
}

// Deliver sends a result to a webhook URL with retry logic.
// Retries up to maxRetries times with exponential backoff: 5s, 15s, 45s.
func Deliver(ctx context.Context, rdb *store.Redis, webhookURL string, requestID string, result *models.RelayResult, maxRetries int) {
	ctx = logger.WithComponent(ctx, "webhook")
	ctx = logger.WithRequestID(ctx, requestID)

	backoffs := []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second}

	payload := webhookPayload{
		RequestID:   requestID,
		Result:      result,
		DeliveredAt: time.Now(),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		slog.ErrorContext(ctx, "failed to marshal webhook payload", "error", err)
		rdb.UpdateResultWebhookStatus(ctx, requestID, models.ResultWebhookFail)
		return
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		attemptCtx := logger.WithAttempt(ctx, attempt)

		slog.InfoContext(attemptCtx, "sending webhook",
			"url", webhookURL,
			"attempt", attempt,
			"max_retries", maxRetries,
			"body_size", len(body),
		)

		req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, webhookURL, bytes.NewReader(body))
		if err != nil {
			slog.ErrorContext(attemptCtx, "failed to create webhook request", "error", err)
			rdb.UpdateResultWebhookStatus(ctx, requestID, models.ResultWebhookFail)
			return
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Relayra-Request-ID", requestID)

		resp, err := client.Do(req)
		if err != nil {
			slog.WarnContext(attemptCtx, "webhook delivery failed",
				"error", err,
				"url", webhookURL,
			)

			if attempt < maxRetries {
				backoff := backoffs[attempt-1]
				slog.InfoContext(attemptCtx, "retrying webhook",
					"backoff", backoff,
					"next_attempt", attempt+1,
				)
				time.Sleep(backoff)
				continue
			}

			slog.ErrorContext(attemptCtx, "webhook permanently failed after all retries",
				"url", webhookURL,
				"total_attempts", maxRetries,
			)
			rdb.UpdateResultWebhookStatus(ctx, requestID, models.ResultWebhookFail)
			return
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			slog.InfoContext(attemptCtx, "webhook delivered successfully",
				"url", webhookURL,
				"status", resp.StatusCode,
			)
			rdb.UpdateResultWebhookStatus(ctx, requestID, models.ResultWebhookSent)
			return
		}

		slog.WarnContext(attemptCtx, "webhook received non-2xx response",
			"url", webhookURL,
			"status", resp.StatusCode,
			"response_body", truncate(string(respBody), 1024),
		)

		if attempt < maxRetries {
			backoff := backoffs[attempt-1]
			slog.InfoContext(attemptCtx, "retrying webhook after non-2xx",
				"backoff", backoff,
			)
			time.Sleep(backoff)
			continue
		}

		slog.ErrorContext(attemptCtx, "webhook permanently failed",
			"url", webhookURL,
			"last_status", resp.StatusCode,
			"total_attempts", maxRetries,
		)
		rdb.UpdateResultWebhookStatus(ctx, requestID, models.ResultWebhookFail)
		return
	}

	// Should not reach here
	_ = fmt.Sprintf("unreachable")
}

func truncate(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "...(truncated)"
	}
	return s
}
