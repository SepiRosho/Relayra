package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/store"
)

// Run starts the Listener HTTP server.
func Run(ctx context.Context, cfg *config.Config, rdb *store.Redis) error {
	ctx = logger.WithComponent(ctx, "server")

	// Log startup diagnostics
	peerCount, _ := rdb.PeerCount(ctx)
	slog.InfoContext(ctx, "Listener starting",
		"listen", cfg.ListenAddress(),
		"peers_count", peerCount,
	)

	mux := http.NewServeMux()
	h := &Handlers{rdb: rdb, cfg: cfg}

	// Register routes
	mux.HandleFunc("GET /health", h.Health)
	mux.HandleFunc("POST /api/v1/relay", h.Relay)
	mux.HandleFunc("GET /api/v1/result/{requestID}", h.GetResult)
	mux.HandleFunc("POST /api/v1/poll", h.Poll)
	mux.HandleFunc("POST /api/v1/pair", h.Pair)
	mux.HandleFunc("GET /api/v1/peers", h.ListPeers)

	// Wrap with middleware: logging -> requestID -> auth
	authMiddleware := apiTokenAuthMiddleware(rdb)
	handler := loggingMiddleware(requestIDMiddleware(authMiddleware(mux)))

	srv := &http.Server{
		Addr:         cfg.ListenAddress(),
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		slog.InfoContext(ctx, "HTTP server listening", "addr", cfg.ListenAddress())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	case sig := <-sigCh:
		slog.InfoContext(ctx, "shutdown signal received", "signal", sig)
	case <-ctx.Done():
		slog.InfoContext(ctx, "context cancelled")
	}

	// Graceful shutdown with timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slog.InfoContext(ctx, "shutting down HTTP server")
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown: %w", err)
	}

	slog.InfoContext(ctx, "HTTP server stopped")
	return nil
}
