package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/relayra/relayra/internal/logger"
)

// Redis wraps a go-redis client with connection management and logging.
type Redis struct {
	Client *redis.Client
	addr   string
}

// NewRedis creates a new Redis client and verifies the connection.
func NewRedis(addr string, password string, db int) (*Redis, error) {
	ctx := logger.WithComponent(context.Background(), "store")

	slog.InfoContext(ctx, "connecting to Redis", "addr", addr, "db", db)

	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolSize:     10,
	})

	// Verify connection
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := client.Ping(pingCtx).Err(); err != nil {
		client.Close()
		slog.ErrorContext(ctx, "Redis connection failed", "addr", addr, "error", err)
		return nil, fmt.Errorf("redis ping %s: %w", addr, err)
	}

	slog.InfoContext(ctx, "Redis connected", "addr", addr, "db", db)

	return &Redis{
		Client: client,
		addr:   addr,
	}, nil
}

// Health checks if Redis is reachable.
func (r *Redis) Health(ctx context.Context) error {
	ctx = logger.WithComponent(ctx, "store")
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := r.Client.Ping(pingCtx).Err(); err != nil {
		slog.ErrorContext(ctx, "Redis health check failed", "addr", r.addr, "error", err)
		return fmt.Errorf("redis health check: %w", err)
	}
	return nil
}

// FlushAll deletes all Relayra keys (relayra:*) from Redis using SCAN.
func (r *Redis) FlushAll(ctx context.Context) (int64, error) {
	ctx = logger.WithComponent(ctx, "store")
	slog.InfoContext(ctx, "flushing all Relayra keys from Redis")

	var totalDeleted int64
	var cursor uint64

	for {
		keys, nextCursor, err := r.Client.Scan(ctx, cursor, "relayra:*", 100).Result()
		if err != nil {
			return totalDeleted, fmt.Errorf("scan relayra keys: %w", err)
		}

		if len(keys) > 0 {
			deleted, err := r.Client.Del(ctx, keys...).Result()
			if err != nil {
				slog.ErrorContext(ctx, "failed to delete keys", "count", len(keys), "error", err)
				return totalDeleted, fmt.Errorf("delete relayra keys: %w", err)
			}
			totalDeleted += deleted
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	slog.InfoContext(ctx, "flushed Relayra keys", "deleted", totalDeleted)
	return totalDeleted, nil
}

// Close shuts down the Redis client.
func (r *Redis) Close() error {
	ctx := logger.WithComponent(context.Background(), "store")
	slog.InfoContext(ctx, "closing Redis connection", "addr", r.addr)
	return r.Client.Close()
}

// Addr returns the Redis address.
func (r *Redis) Addr() string {
	return r.addr
}
