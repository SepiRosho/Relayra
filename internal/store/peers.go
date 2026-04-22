package store

import (
	"context"
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
	keyPeerPrefix    = "relayra:peer:"
	keyPeerSet       = "relayra:peers"
	keyPairingPrefix = "relayra:pairing:"
	keyListenerInfo  = "relayra:listener:info"
)

// StorePeer saves a peer record and adds it to the peer set.
func (r *Redis) StorePeer(ctx context.Context, peer *models.Peer) error {
	ctx = logger.WithComponent(ctx, "store")
	ctx = logger.WithPeerID(ctx, peer.ID)

	peerKey := keyPeerPrefix + peer.ID
	pipe := r.Client.Pipeline()

	pipe.HSet(ctx, peerKey, map[string]interface{}{
		"id":             peer.ID,
		"name":           peer.Name,
		"machine_id":     peer.MachineID,
		"role":           peer.Role,
		"address":        peer.Address,
		"encryption_key": hex.EncodeToString(peer.EncryptionKey),
		"registered_at":  peer.RegisteredAt.Unix(),
		"last_seen":      peer.LastSeen.Unix(),
	})
	pipe.SAdd(ctx, keyPeerSet, peer.ID)

	if _, err := pipe.Exec(ctx); err != nil {
		slog.ErrorContext(ctx, "failed to store peer", "error", err)
		return fmt.Errorf("store peer: %w", err)
	}

	slog.InfoContext(ctx, "peer stored", "name", peer.Name, "role", peer.Role)
	return nil
}

// GetPeer retrieves a peer by ID.
func (r *Redis) GetPeer(ctx context.Context, peerID string) (*models.Peer, error) {
	ctx = logger.WithComponent(ctx, "store")
	ctx = logger.WithPeerID(ctx, peerID)

	peerKey := keyPeerPrefix + peerID
	data, err := r.Client.HGetAll(ctx, peerKey).Result()
	if err != nil {
		slog.ErrorContext(ctx, "failed to get peer", "error", err)
		return nil, fmt.Errorf("get peer: %w", err)
	}
	if len(data) == 0 {
		slog.DebugContext(ctx, "peer not found")
		return nil, nil
	}

	encKey, _ := hex.DecodeString(data["encryption_key"])
	registeredAt, _ := time.Parse("", data["registered_at"])
	lastSeen, _ := time.Parse("", data["last_seen"])

	// Parse unix timestamps
	if ts, err := r.Client.HGet(ctx, peerKey, "registered_at").Int64(); err == nil {
		registeredAt = time.Unix(ts, 0)
	}
	if ts, err := r.Client.HGet(ctx, peerKey, "last_seen").Int64(); err == nil {
		lastSeen = time.Unix(ts, 0)
	}

	peer := &models.Peer{
		ID:            data["id"],
		Name:          data["name"],
		MachineID:     data["machine_id"],
		Role:          data["role"],
		Address:       data["address"],
		EncryptionKey: encKey,
		RegisteredAt:  registeredAt,
		LastSeen:      lastSeen,
	}

	return peer, nil
}

// ListPeers returns all registered peers.
func (r *Redis) ListPeers(ctx context.Context) ([]*models.Peer, error) {
	ctx = logger.WithComponent(ctx, "store")

	peerIDs, err := r.Client.SMembers(ctx, keyPeerSet).Result()
	if err != nil {
		slog.ErrorContext(ctx, "failed to list peer IDs", "error", err)
		return nil, fmt.Errorf("list peers: %w", err)
	}

	var peers []*models.Peer
	for _, id := range peerIDs {
		peer, err := r.GetPeer(ctx, id)
		if err != nil {
			slog.WarnContext(ctx, "failed to load peer", "peer_id", id, "error", err)
			continue
		}
		if peer != nil {
			peers = append(peers, peer)
		}
	}

	slog.DebugContext(ctx, "listed peers", "count", len(peers))
	return peers, nil
}

// DeletePeer removes a peer record and its queue.
func (r *Redis) DeletePeer(ctx context.Context, peerID string) error {
	ctx = logger.WithComponent(ctx, "store")
	ctx = logger.WithPeerID(ctx, peerID)

	pipe := r.Client.Pipeline()
	pipe.Del(ctx, keyPeerPrefix+peerID)
	pipe.SRem(ctx, keyPeerSet, peerID)
	pipe.Del(ctx, keyQueuePrefix+peerID)

	if _, err := pipe.Exec(ctx); err != nil {
		slog.ErrorContext(ctx, "failed to delete peer", "error", err)
		return fmt.Errorf("delete peer: %w", err)
	}

	slog.InfoContext(ctx, "peer deleted")
	return nil
}

// PeerCount returns the number of registered peers.
func (r *Redis) PeerCount(ctx context.Context) (int64, error) {
	return r.Client.SCard(ctx, keyPeerSet).Result()
}

// UpdatePeerLastSeen updates the last_seen timestamp for a peer.
func (r *Redis) UpdatePeerLastSeen(ctx context.Context, peerID string) error {
	ctx = logger.WithComponent(ctx, "store")
	ctx = logger.WithPeerID(ctx, peerID)

	now := time.Now().Unix()
	if err := r.Client.HSet(ctx, keyPeerPrefix+peerID, "last_seen", now).Err(); err != nil {
		slog.ErrorContext(ctx, "failed to update peer last_seen", "error", err)
		return fmt.Errorf("update last_seen: %w", err)
	}

	slog.DebugContext(ctx, "peer last_seen updated")
	return nil
}

// --- Pairing token storage ---

// StorePairingToken saves a one-time pairing token with expiry.
func (r *Redis) StorePairingToken(ctx context.Context, secretHash string, data *models.PairingToken, ttl time.Duration) error {
	ctx = logger.WithComponent(ctx, "pairing")

	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal pairing token: %w", err)
	}

	pairingKey := keyPairingPrefix + secretHash
	if err := r.Client.Set(ctx, pairingKey, jsonData, ttl).Err(); err != nil {
		slog.ErrorContext(ctx, "failed to store pairing token", "error", err)
		return fmt.Errorf("store pairing token: %w", err)
	}

	slog.InfoContext(ctx, "pairing token stored", "ttl", ttl, "secret_hash", secretHash[:12]+"...")
	return nil
}

// GetPairingToken retrieves and deletes a one-time pairing token.
func (r *Redis) GetPairingToken(ctx context.Context, secretHash string) (*models.PairingToken, error) {
	ctx = logger.WithComponent(ctx, "pairing")

	pairingKey := keyPairingPrefix + secretHash

	// Get and delete atomically
	data, err := r.Client.GetDel(ctx, pairingKey).Result()
	if err == redis.Nil {
		slog.WarnContext(ctx, "pairing token not found or expired", "secret_hash", secretHash[:12]+"...")
		return nil, fmt.Errorf("pairing token not found or expired")
	}
	if err != nil {
		slog.ErrorContext(ctx, "failed to get pairing token", "error", err)
		return nil, fmt.Errorf("get pairing token: %w", err)
	}

	var token models.PairingToken
	if err := json.Unmarshal([]byte(data), &token); err != nil {
		return nil, fmt.Errorf("unmarshal pairing token: %w", err)
	}

	slog.InfoContext(ctx, "pairing token retrieved and consumed")
	return &token, nil
}

// --- Sender-side: Listener connection info ---

// StoreListenerInfo saves the Listener connection details on the Sender side.
func (r *Redis) StoreListenerInfo(ctx context.Context, peer *models.Peer) error {
	ctx = logger.WithComponent(ctx, "store")

	pipe := r.Client.Pipeline()
	pipe.HSet(ctx, keyListenerInfo, map[string]interface{}{
		"id":             peer.ID,
		"name":           peer.Name,
		"machine_id":     peer.MachineID,
		"address":        peer.Address,
		"encryption_key": hex.EncodeToString(peer.EncryptionKey),
		"registered_at":  peer.RegisteredAt.Unix(),
	})

	if _, err := pipe.Exec(ctx); err != nil {
		slog.ErrorContext(ctx, "failed to store listener info", "error", err)
		return fmt.Errorf("store listener info: %w", err)
	}

	slog.InfoContext(ctx, "listener info stored", "listener_name", peer.Name, "address", peer.Address)
	return nil
}

// GetListenerInfo retrieves the Listener connection details on the Sender side.
func (r *Redis) GetListenerInfo(ctx context.Context) (*models.Peer, error) {
	ctx = logger.WithComponent(ctx, "store")

	data, err := r.Client.HGetAll(ctx, keyListenerInfo).Result()
	if err != nil {
		return nil, fmt.Errorf("get listener info: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}

	encKey, _ := hex.DecodeString(data["encryption_key"])
	var registeredAt time.Time
	if ts, err := r.Client.HGet(ctx, keyListenerInfo, "registered_at").Int64(); err == nil {
		registeredAt = time.Unix(ts, 0)
	}

	peer := &models.Peer{
		ID:            data["id"],
		Name:          data["name"],
		MachineID:     data["machine_id"],
		Address:       data["address"],
		EncryptionKey: encKey,
		RegisteredAt:  registeredAt,
	}

	return peer, nil
}
