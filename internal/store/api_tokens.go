package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/models"
)

const (
	keyAPITokenPrefix = "relayra:apitoken:"
	keyAPITokenSet    = "relayra:apitokens"
)

// GenerateAPIToken creates a new API token with a random 32-byte hex string.
func GenerateAPIToken() (token string, hash string) {
	b := make([]byte, 32)
	rand.Read(b)
	token = hex.EncodeToString(b)
	h := sha256.Sum256([]byte(token))
	hash = hex.EncodeToString(h[:])
	return
}

// HashAPIToken returns the SHA256 hex hash of a plaintext token.
func HashAPIToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// StoreAPIToken saves an API token to Redis.
func (r *Redis) StoreAPIToken(ctx context.Context, t *models.APIToken) error {
	ctx = logger.WithComponent(ctx, "store")

	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("marshal api token: %w", err)
	}

	tokenKey := keyAPITokenPrefix + t.ID
	pipe := r.Client.Pipeline()
	pipe.Set(ctx, tokenKey, data, 0)
	pipe.SAdd(ctx, keyAPITokenSet, t.ID)

	// Also store a reverse lookup: hash -> token ID
	pipe.Set(ctx, keyAPITokenPrefix+"hash:"+t.TokenHash, t.ID, 0)

	if _, err := pipe.Exec(ctx); err != nil {
		slog.ErrorContext(ctx, "failed to store api token", "error", err)
		return fmt.Errorf("store api token: %w", err)
	}

	slog.InfoContext(ctx, "API token stored", "id", t.ID, "name", t.Name)
	return nil
}

// ValidateAPIToken checks if a token hash exists and returns the token info. Updates usage stats.
func (r *Redis) ValidateAPIToken(ctx context.Context, tokenHash string) (*models.APIToken, error) {
	ctx = logger.WithComponent(ctx, "store")

	// Lookup token ID by hash
	tokenID, err := r.Client.Get(ctx, keyAPITokenPrefix+"hash:"+tokenHash).Result()
	if err == redis.Nil {
		return nil, fmt.Errorf("invalid token")
	}
	if err != nil {
		return nil, fmt.Errorf("validate token: %w", err)
	}

	// Get token data
	data, err := r.Client.Get(ctx, keyAPITokenPrefix+tokenID).Result()
	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}

	var t models.APIToken
	if err := json.Unmarshal([]byte(data), &t); err != nil {
		return nil, fmt.Errorf("unmarshal token: %w", err)
	}

	// Update usage stats
	t.LastUsed = time.Now()
	t.UsageCount++
	updated, _ := json.Marshal(t)
	r.Client.Set(ctx, keyAPITokenPrefix+tokenID, updated, 0)

	return &t, nil
}

// ListAPITokens returns all API tokens.
func (r *Redis) ListAPITokens(ctx context.Context) ([]*models.APIToken, error) {
	ctx = logger.WithComponent(ctx, "store")

	tokenIDs, err := r.Client.SMembers(ctx, keyAPITokenSet).Result()
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}

	var tokens []*models.APIToken
	for _, id := range tokenIDs {
		data, err := r.Client.Get(ctx, keyAPITokenPrefix+id).Result()
		if err != nil {
			continue
		}
		var t models.APIToken
		if err := json.Unmarshal([]byte(data), &t); err != nil {
			continue
		}
		tokens = append(tokens, &t)
	}

	return tokens, nil
}

// DeleteAPIToken removes an API token.
func (r *Redis) DeleteAPIToken(ctx context.Context, tokenID string) error {
	ctx = logger.WithComponent(ctx, "store")

	// Get the token first to find its hash
	data, err := r.Client.Get(ctx, keyAPITokenPrefix+tokenID).Result()
	if err == redis.Nil {
		return fmt.Errorf("token not found")
	}
	if err != nil {
		return fmt.Errorf("get token for deletion: %w", err)
	}

	var t models.APIToken
	if err := json.Unmarshal([]byte(data), &t); err != nil {
		return fmt.Errorf("unmarshal token: %w", err)
	}

	pipe := r.Client.Pipeline()
	pipe.Del(ctx, keyAPITokenPrefix+tokenID)
	pipe.SRem(ctx, keyAPITokenSet, tokenID)
	if t.TokenHash != "" {
		pipe.Del(ctx, keyAPITokenPrefix+"hash:"+t.TokenHash)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("delete api token: %w", err)
	}

	slog.InfoContext(ctx, "API token deleted", "id", tokenID, "name", t.Name)
	return nil
}

// APITokenCount returns the number of API tokens.
func (r *Redis) APITokenCount(ctx context.Context) (int64, error) {
	return r.Client.SCard(ctx, keyAPITokenSet).Result()
}
