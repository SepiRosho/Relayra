package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/models"
)

// ProxyRecord stores proxy configuration and health state independent of backend.
type ProxyRecord struct {
	URL         string
	Priority    float64
	FailCount   int
	LastChecked time.Time
}

// Backend describes the storage operations Relayra needs from Redis or SQLite.
type Backend interface {
	Health(ctx context.Context) error
	FlushAll(ctx context.Context) (int64, error)
	Close() error

	StorePeer(ctx context.Context, peer *models.Peer) error
	GetPeer(ctx context.Context, peerID string) (*models.Peer, error)
	ListPeers(ctx context.Context) ([]*models.Peer, error)
	DeletePeer(ctx context.Context, peerID string) error
	PeerCount(ctx context.Context) (int64, error)
	UpdatePeerLastSeen(ctx context.Context, peerID string) error

	StorePairingToken(ctx context.Context, secretHash string, data *models.PairingToken, ttl time.Duration) error
	GetPairingToken(ctx context.Context, secretHash string) (*models.PairingToken, error)

	StoreListenerInfo(ctx context.Context, peer *models.Peer) error
	GetListenerInfo(ctx context.Context) (*models.Peer, error)

	StoreRequestMetadata(ctx context.Context, peerID string, req *models.RelayRequest) error
	EnqueueRequest(ctx context.Context, peerID string, req *models.RelayRequest) error
	DequeueRequests(ctx context.Context, peerID string, batchSize int) ([]models.RelayRequest, error)
	QueueLength(ctx context.Context, peerID string) (int64, error)
	GetRequestStatus(ctx context.Context, requestID string) (models.RequestStatus, error)
	GetRequestWebhookURL(ctx context.Context, requestID string) (string, error)
	UpdateRequestStatus(ctx context.Context, requestID string, status models.RequestStatus) error
	AckRequests(ctx context.Context, requestIDs []string) error

	PushResult(ctx context.Context, result *models.RelayResult) error
	PopResults(ctx context.Context, maxCount int) ([]models.RelayResult, error)
	PendingResultsCount(ctx context.Context) (int64, error)
	RePushResults(ctx context.Context, results []models.RelayResult) error
	DeleteAckedResults(ctx context.Context, resultIDs []string)

	StoreResult(ctx context.Context, result *models.RelayResult, ttlSeconds int) error
	GetResult(ctx context.Context, requestID string) (*models.RelayResult, error)
	ResultExists(ctx context.Context, requestID string) (bool, error)
	UpdateResultWebhookStatus(ctx context.Context, requestID string, status models.ResultStatus) error
	GetResultTTL(ctx context.Context, requestID string) (int64, error)

	StoreAPIToken(ctx context.Context, t *models.APIToken) error
	ValidateAPIToken(ctx context.Context, tokenHash string) (*models.APIToken, error)
	ListAPITokens(ctx context.Context) ([]*models.APIToken, error)
	DeleteAPIToken(ctx context.Context, tokenID string) error
	APITokenCount(ctx context.Context) (int64, error)

	AddProxy(ctx context.Context, proxyURL string, priority float64) error
	RemoveProxy(ctx context.Context, proxyURL string) error
	ListProxyRecords(ctx context.Context) ([]ProxyRecord, error)
	MarkProxySuccess(ctx context.Context, proxyURL string) error
	MarkProxyFailed(ctx context.Context, proxyURL string) (int, error)
	ResetProxyCooldown(ctx context.Context, proxyURL string) error
	ResetAllProxyCooldowns(ctx context.Context) (int, error)
	UpdateProxyURL(ctx context.Context, oldURL, newURL string) error
	ProxyCount(ctx context.Context) (int64, error)
}

// Open selects the configured storage backend.
func Open(cfg *config.Config) (Backend, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.StorageBackend)) {
	case "", "redis":
		return NewRedis(cfg.RedisURL(), cfg.RedisPassword, cfg.RedisDB)
	case "sqlite":
		return NewSQLite(cfg.SQLitePath)
	default:
		return nil, fmt.Errorf("unsupported storage backend: %s", cfg.StorageBackend)
	}
}
