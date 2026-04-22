package relayexec

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/relayra/relayra/internal/models"
)

// ExecuteRequest executes a single relay request and returns the result.
func ExecuteRequest(ctx context.Context, req *models.RelayRequest, timeoutSeconds int) *models.RelayResult {
	start := time.Now()

	slog.InfoContext(ctx, "executing request",
		"url", req.Request.URL,
		"method", req.Request.Method,
		"async", req.Async,
		"has_body", req.Request.Body != "",
		"headers_count", len(req.Request.Headers),
	)

	for k, v := range req.Request.Headers {
		slog.DebugContext(ctx, "request header", "key", k, "value", v)
	}

	var bodyReader io.Reader
	if req.Request.Body != "" {
		bodyReader = strings.NewReader(req.Request.Body)
		slog.DebugContext(ctx, "request body", "size", len(req.Request.Body),
			"preview", truncateStr(req.Request.Body, 4096))
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Request.Method, req.Request.URL, bodyReader)
	if err != nil {
		duration := time.Since(start)
		slog.ErrorContext(ctx, "failed to create HTTP request",
			"error", err,
			"duration_ms", duration.Milliseconds(),
		)
		return &models.RelayResult{
			RequestID:  req.ID,
			StatusCode: 0,
			Error:      fmt.Sprintf("create request: %v", err),
			Duration:   duration.Milliseconds(),
			ExecutedAt: time.Now(),
		}
	}

	for k, v := range req.Request.Headers {
		httpReq.Header.Set(k, v)
	}

	client := &http.Client{
		Timeout: time.Duration(timeoutSeconds) * time.Second,
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		duration := time.Since(start)
		slog.ErrorContext(ctx, "request execution failed",
			"error", err,
			"duration_ms", duration.Milliseconds(),
		)
		return &models.RelayResult{
			RequestID:  req.ID,
			StatusCode: 0,
			Error:      fmt.Sprintf("execute request: %v", err),
			Duration:   duration.Milliseconds(),
			ExecutedAt: time.Now(),
		}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		duration := time.Since(start)
		slog.ErrorContext(ctx, "failed to read response body",
			"error", err,
			"status_code", resp.StatusCode,
			"duration_ms", duration.Milliseconds(),
		)
		return &models.RelayResult{
			RequestID:  req.ID,
			StatusCode: resp.StatusCode,
			Error:      fmt.Sprintf("read response: %v", err),
			Duration:   duration.Milliseconds(),
			ExecutedAt: time.Now(),
		}
	}

	duration := time.Since(start)
	headers := make(map[string]string)
	for k, v := range resp.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}

	slog.InfoContext(ctx, "request executed successfully",
		"status_code", resp.StatusCode,
		"body_size", len(respBody),
		"headers_count", len(headers),
		"duration_ms", duration.Milliseconds(),
	)
	slog.DebugContext(ctx, "response body preview",
		"preview", truncateStr(string(respBody), 4096),
	)

	return &models.RelayResult{
		RequestID:  req.ID,
		StatusCode: resp.StatusCode,
		Headers:    headers,
		Body:       string(respBody),
		Duration:   duration.Milliseconds(),
		ExecutedAt: time.Now(),
	}
}

func truncateStr(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "...(truncated)"
	}
	return s
}
