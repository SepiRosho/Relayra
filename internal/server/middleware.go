package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/store"
)

type contextKeyType string

const contextKeyRequestID contextKeyType = "request_id"

// requestIDMiddleware generates a unique request ID for each incoming request.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 8)
		rand.Read(b)
		reqID := hex.EncodeToString(b)

		ctx := context.WithValue(r.Context(), contextKeyRequestID, reqID)
		ctx = logger.WithRequestID(ctx, reqID)

		w.Header().Set("X-Request-ID", reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// loggingMiddleware logs every HTTP request with method, path, status, and duration.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx := logger.WithComponent(r.Context(), "server")

		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)

		duration := time.Since(start)
		slog.InfoContext(ctx, "HTTP request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.statusCode,
			"duration_ms", duration.Milliseconds(),
			"remote_addr", r.RemoteAddr,
		)
	})
}

// apiTokenAuthMiddleware validates Bearer tokens on protected API endpoints.
// Endpoints: /api/v1/relay, /api/v1/result/*, /api/v1/peers
// Exempt: /health, /api/v1/poll (uses peer encryption), /api/v1/pair (uses pairing token)
func apiTokenAuthMiddleware(rdb store.Backend) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path

			// Skip auth for non-protected endpoints
			if path == "/health" || path == "/api/v1/poll" || path == "/api/v1/pair" {
				next.ServeHTTP(w, r)
				return
			}

			// Check if any tokens exist — if none, auth is disabled (first-time setup)
			count, _ := rdb.APITokenCount(r.Context())
			if count == 0 {
				next.ServeHTTP(w, r)
				return
			}

			// Extract Bearer token
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"Authorization header required. Use: Authorization: Bearer <token>"}`))
				return
			}

			if !strings.HasPrefix(authHeader, "Bearer ") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"Invalid authorization format. Use: Bearer <token>"}`))
				return
			}

			token := strings.TrimPrefix(authHeader, "Bearer ")
			tokenHash := store.HashAPIToken(token)

			_, err := rdb.ValidateAPIToken(r.Context(), tokenHash)
			if err != nil {
				ctx := logger.WithComponent(r.Context(), "auth")
				slog.WarnContext(ctx, "invalid API token", "remote_addr", r.RemoteAddr, "path", path)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"Invalid API token"}`))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
