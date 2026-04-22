package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/models"
	_ "modernc.org/sqlite"
)

type SQLite struct {
	db   *sql.DB
	path string
}

func NewSQLite(path string) (*SQLite, error) {
	ctx := logger.WithComponent(context.Background(), "store")

	if path == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	path = strings.TrimSpace(path)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create sqlite dir %s: %w", filepath.Dir(path), err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.Exec(`PRAGMA journal_mode = WAL;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable sqlite WAL: %w", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("set sqlite busy_timeout: %w", err)
	}

	s := &SQLite{db: db, path: path}
	if err := s.initSchema(ctx); err != nil {
		db.Close()
		return nil, err
	}

	slog.InfoContext(ctx, "SQLite connected", "path", path)
	return s, nil
}

func (s *SQLite) initSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS peers (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			machine_id TEXT NOT NULL,
			role TEXT NOT NULL,
			address TEXT,
			encryption_key TEXT NOT NULL,
			registered_at INTEGER NOT NULL,
			last_seen INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS pairing_tokens (
			secret_hash TEXT PRIMARY KEY,
			data TEXT NOT NULL,
			expires_at INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS listener_info (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			peer_id TEXT NOT NULL,
			name TEXT NOT NULL,
			machine_id TEXT NOT NULL,
			address TEXT,
			encryption_key TEXT NOT NULL,
			registered_at INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS requests (
			id TEXT PRIMARY KEY,
			peer_id TEXT NOT NULL,
			webhook_url TEXT,
			status TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			data TEXT NOT NULL,
			async INTEGER NOT NULL DEFAULT 0,
			webhook_status TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS request_queue (
			seq INTEGER PRIMARY KEY,
			peer_id TEXT NOT NULL,
			request_id TEXT NOT NULL,
			data TEXT NOT NULL,
			queued_at INTEGER NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_request_queue_peer_seq ON request_queue(peer_id, seq);`,
		`CREATE TABLE IF NOT EXISTS results (
			request_id TEXT PRIMARY KEY,
			data TEXT NOT NULL,
			expires_at INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS pending_results (
			seq INTEGER PRIMARY KEY,
			request_id TEXT NOT NULL,
			data TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS api_tokens (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			created_at INTEGER NOT NULL,
			last_used INTEGER,
			usage_count INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS proxies (
			url TEXT PRIMARY KEY,
			priority REAL NOT NULL,
			fail_count INTEGER NOT NULL DEFAULT 0,
			last_checked INTEGER NOT NULL DEFAULT 0
		);`,
	}

	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("init sqlite schema: %w", err)
		}
	}
	return nil
}

func (s *SQLite) Health(ctx context.Context) error {
	ctx = logger.WithComponent(ctx, "store")
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.db.PingContext(pingCtx); err != nil {
		slog.ErrorContext(ctx, "SQLite health check failed", "path", s.path, "error", err)
		return fmt.Errorf("sqlite health check: %w", err)
	}
	return nil
}

func (s *SQLite) FlushAll(ctx context.Context) (int64, error) {
	ctx = logger.WithComponent(ctx, "store")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin flush sqlite tx: %w", err)
	}
	defer tx.Rollback()

	tables := []string{
		"peers", "pairing_tokens", "listener_info", "requests", "request_queue",
		"results", "pending_results", "api_tokens", "proxies",
	}
	var total int64
	for _, table := range tables {
		res, err := tx.ExecContext(ctx, "DELETE FROM "+table)
		if err != nil {
			return total, fmt.Errorf("flush sqlite table %s: %w", table, err)
		}
		affected, _ := res.RowsAffected()
		total += affected
	}
	if err := tx.Commit(); err != nil {
		return total, fmt.Errorf("commit sqlite flush: %w", err)
	}
	return total, nil
}

func (s *SQLite) Close() error {
	ctx := logger.WithComponent(context.Background(), "store")
	slog.InfoContext(ctx, "closing SQLite connection", "path", s.path)
	return s.db.Close()
}

func (s *SQLite) StorePeer(ctx context.Context, peer *models.Peer) error {
	ctx = logger.WithComponent(ctx, "store")
	ctx = logger.WithPeerID(ctx, peer.ID)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO peers (id, name, machine_id, role, address, encryption_key, registered_at, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name,
			machine_id=excluded.machine_id,
			role=excluded.role,
			address=excluded.address,
			encryption_key=excluded.encryption_key,
			registered_at=excluded.registered_at,
			last_seen=excluded.last_seen
	`, peer.ID, peer.Name, peer.MachineID, peer.Role, peer.Address, hex.EncodeToString(peer.EncryptionKey), peer.RegisteredAt.Unix(), peer.LastSeen.Unix())
	if err != nil {
		return fmt.Errorf("store peer: %w", err)
	}
	return nil
}

func (s *SQLite) GetPeer(ctx context.Context, peerID string) (*models.Peer, error) {
	ctx = logger.WithComponent(ctx, "store")
	ctx = logger.WithPeerID(ctx, peerID)

	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, machine_id, role, address, encryption_key, registered_at, last_seen
		FROM peers WHERE id = ?
	`, peerID)

	var peer models.Peer
	var address sql.NullString
	var encKeyHex string
	var registeredAt, lastSeen int64
	if err := row.Scan(&peer.ID, &peer.Name, &peer.MachineID, &peer.Role, &address, &encKeyHex, &registeredAt, &lastSeen); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get peer: %w", err)
	}

	encKey, _ := hex.DecodeString(encKeyHex)
	peer.Address = address.String
	peer.EncryptionKey = encKey
	peer.RegisteredAt = time.Unix(registeredAt, 0)
	peer.LastSeen = time.Unix(lastSeen, 0)
	return &peer, nil
}

func (s *SQLite) ListPeers(ctx context.Context) ([]*models.Peer, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, machine_id, role, address, encryption_key, registered_at, last_seen
		FROM peers ORDER BY registered_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list peers: %w", err)
	}
	defer rows.Close()

	var peers []*models.Peer
	for rows.Next() {
		var peer models.Peer
		var address sql.NullString
		var encKeyHex string
		var registeredAt, lastSeen int64
		if err := rows.Scan(&peer.ID, &peer.Name, &peer.MachineID, &peer.Role, &address, &encKeyHex, &registeredAt, &lastSeen); err != nil {
			return nil, fmt.Errorf("scan peer: %w", err)
		}
		encKey, _ := hex.DecodeString(encKeyHex)
		peer.Address = address.String
		peer.EncryptionKey = encKey
		peer.RegisteredAt = time.Unix(registeredAt, 0)
		peer.LastSeen = time.Unix(lastSeen, 0)
		peers = append(peers, &peer)
	}
	return peers, rows.Err()
}

func (s *SQLite) DeletePeer(ctx context.Context, peerID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete peer tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM peers WHERE id = ?`, peerID); err != nil {
		return fmt.Errorf("delete peer: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM request_queue WHERE peer_id = ?`, peerID); err != nil {
		return fmt.Errorf("delete peer queue: %w", err)
	}
	return tx.Commit()
}

func (s *SQLite) PeerCount(ctx context.Context) (int64, error) {
	return s.count(ctx, `SELECT COUNT(*) FROM peers`)
}

func (s *SQLite) UpdatePeerLastSeen(ctx context.Context, peerID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET last_seen = ? WHERE id = ?`, time.Now().Unix(), peerID)
	return err
}

func (s *SQLite) StorePairingToken(ctx context.Context, secretHash string, data *models.PairingToken, ttl time.Duration) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal pairing token: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO pairing_tokens (secret_hash, data, expires_at)
		VALUES (?, ?, ?)
		ON CONFLICT(secret_hash) DO UPDATE SET data = excluded.data, expires_at = excluded.expires_at
	`, secretHash, string(jsonData), time.Now().Add(ttl).Unix())
	return err
}

func (s *SQLite) GetPairingToken(ctx context.Context, secretHash string) (*models.PairingToken, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin pairing token tx: %w", err)
	}
	defer tx.Rollback()

	var data string
	var expiresAt int64
	err = tx.QueryRowContext(ctx, `SELECT data, expires_at FROM pairing_tokens WHERE secret_hash = ?`, secretHash).Scan(&data, &expiresAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("pairing token not found or expired")
		}
		return nil, fmt.Errorf("get pairing token: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM pairing_tokens WHERE secret_hash = ?`, secretHash); err != nil {
		return nil, fmt.Errorf("consume pairing token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit pairing token consume: %w", err)
	}

	if time.Now().Unix() > expiresAt {
		return nil, fmt.Errorf("pairing token not found or expired")
	}

	var token models.PairingToken
	if err := json.Unmarshal([]byte(data), &token); err != nil {
		return nil, fmt.Errorf("unmarshal pairing token: %w", err)
	}
	return &token, nil
}

func (s *SQLite) StoreListenerInfo(ctx context.Context, peer *models.Peer) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO listener_info (singleton, peer_id, name, machine_id, address, encryption_key, registered_at)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET
			peer_id=excluded.peer_id,
			name=excluded.name,
			machine_id=excluded.machine_id,
			address=excluded.address,
			encryption_key=excluded.encryption_key,
			registered_at=excluded.registered_at
	`, peer.ID, peer.Name, peer.MachineID, peer.Address, hex.EncodeToString(peer.EncryptionKey), peer.RegisteredAt.Unix())
	return err
}

func (s *SQLite) GetListenerInfo(ctx context.Context) (*models.Peer, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT peer_id, name, machine_id, address, encryption_key, registered_at
		FROM listener_info WHERE singleton = 1
	`)

	var peer models.Peer
	var address sql.NullString
	var encKeyHex string
	var registeredAt int64
	if err := row.Scan(&peer.ID, &peer.Name, &peer.MachineID, &address, &encKeyHex, &registeredAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get listener info: %w", err)
	}

	encKey, _ := hex.DecodeString(encKeyHex)
	peer.Address = address.String
	peer.EncryptionKey = encKey
	peer.RegisteredAt = time.Unix(registeredAt, 0)
	return &peer, nil
}

func (s *SQLite) StoreRequestMetadata(ctx context.Context, peerID string, req *models.RelayRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO requests (id, peer_id, webhook_url, status, created_at, data, async)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			peer_id=excluded.peer_id,
			webhook_url=excluded.webhook_url,
			status=excluded.status,
			created_at=excluded.created_at,
			data=excluded.data,
			async=excluded.async
	`, req.ID, peerID, req.WebhookURL, string(req.Status), req.CreatedAt.Unix(), string(data), boolToInt(req.Async))
	return err
}

func (s *SQLite) EnqueueRequest(ctx context.Context, peerID string, req *models.RelayRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin enqueue tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO requests (id, peer_id, webhook_url, status, created_at, data, async)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			peer_id=excluded.peer_id,
			webhook_url=excluded.webhook_url,
			status=excluded.status,
			created_at=excluded.created_at,
			data=excluded.data,
			async=excluded.async
	`, req.ID, peerID, req.WebhookURL, string(models.StatusQueued), req.CreatedAt.Unix(), string(data), boolToInt(req.Async)); err != nil {
		return fmt.Errorf("store request metadata: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO request_queue (peer_id, request_id, data, queued_at)
		VALUES (?, ?, ?, ?)
	`, peerID, req.ID, string(data), time.Now().Unix()); err != nil {
		return fmt.Errorf("queue request: %w", err)
	}

	return tx.Commit()
}

func (s *SQLite) DequeueRequests(ctx context.Context, peerID string, batchSize int) ([]models.RelayRequest, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin dequeue tx: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT seq, data FROM request_queue
		WHERE peer_id = ?
		ORDER BY seq ASC
		LIMIT ?
	`, peerID, batchSize)
	if err != nil {
		return nil, fmt.Errorf("query request queue: %w", err)
	}
	defer rows.Close()

	var (
		seqs     []int64
		requests []models.RelayRequest
	)
	for rows.Next() {
		var seq int64
		var data string
		if err := rows.Scan(&seq, &data); err != nil {
			return nil, fmt.Errorf("scan queued request: %w", err)
		}
		var req models.RelayRequest
		if err := json.Unmarshal([]byte(data), &req); err != nil {
			continue
		}
		seqs = append(seqs, seq)
		requests = append(requests, req)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i, req := range requests {
		if _, err := tx.ExecContext(ctx, `DELETE FROM request_queue WHERE seq = ?`, seqs[i]); err != nil {
			return nil, fmt.Errorf("delete dequeued request: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE requests SET status = ? WHERE id = ?`, string(models.StatusSent), req.ID); err != nil {
			return nil, fmt.Errorf("update request status sent: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit dequeue tx: %w", err)
	}
	return requests, nil
}

func (s *SQLite) QueueLength(ctx context.Context, peerID string) (int64, error) {
	row := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_queue WHERE peer_id = ?`, peerID)
	var count int64
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *SQLite) GetRequestStatus(ctx context.Context, requestID string) (models.RequestStatus, error) {
	row := s.db.QueryRowContext(ctx, `SELECT status FROM requests WHERE id = ?`, requestID)
	var status string
	if err := row.Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("request not found: %s", requestID)
		}
		return "", err
	}
	return models.RequestStatus(status), nil
}

func (s *SQLite) GetRequestWebhookURL(ctx context.Context, requestID string) (string, error) {
	row := s.db.QueryRowContext(ctx, `SELECT webhook_url FROM requests WHERE id = ?`, requestID)
	var url sql.NullString
	if err := row.Scan(&url); err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return url.String, nil
}

func (s *SQLite) UpdateRequestStatus(ctx context.Context, requestID string, status models.RequestStatus) error {
	_, err := s.db.ExecContext(ctx, `UPDATE requests SET status = ? WHERE id = ?`, string(status), requestID)
	return err
}

func (s *SQLite) AckRequests(ctx context.Context, requestIDs []string) error {
	for _, id := range requestIDs {
		if _, err := s.db.ExecContext(ctx, `UPDATE requests SET status = ? WHERE id = ?`, string(models.StatusExecuting), id); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLite) PushResult(ctx context.Context, result *models.RelayResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO pending_results (request_id, data) VALUES (?, ?)`, result.RequestID, string(data))
	return err
}

func (s *SQLite) PopResults(ctx context.Context, maxCount int) ([]models.RelayResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin pop results tx: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT seq, data FROM pending_results
		ORDER BY seq ASC
		LIMIT ?
	`, maxCount)
	if err != nil {
		return nil, fmt.Errorf("query pending results: %w", err)
	}
	defer rows.Close()

	var (
		seqs    []int64
		results []models.RelayResult
	)
	for rows.Next() {
		var seq int64
		var data string
		if err := rows.Scan(&seq, &data); err != nil {
			return nil, fmt.Errorf("scan pending result: %w", err)
		}
		var result models.RelayResult
		if err := json.Unmarshal([]byte(data), &result); err != nil {
			continue
		}
		seqs = append(seqs, seq)
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, seq := range seqs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM pending_results WHERE seq = ?`, seq); err != nil {
			return nil, fmt.Errorf("delete popped pending result: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit pop results tx: %w", err)
	}
	return results, nil
}

func (s *SQLite) PendingResultsCount(ctx context.Context) (int64, error) {
	return s.count(ctx, `SELECT COUNT(*) FROM pending_results`)
}

func (s *SQLite) RePushResults(ctx context.Context, results []models.RelayResult) error {
	if len(results) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin repush results tx: %w", err)
	}
	defer tx.Rollback()

	minSeq := int64(0)
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MIN(seq), 0) FROM pending_results`).Scan(&minSeq); err != nil {
		return fmt.Errorf("query pending result min seq: %w", err)
	}
	nextSeq := minSeq - 1
	for i := len(results) - 1; i >= 0; i-- {
		data, err := json.Marshal(results[i])
		if err != nil {
			return fmt.Errorf("marshal repush result: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO pending_results (seq, request_id, data)
			VALUES (?, ?, ?)
		`, nextSeq, results[i].RequestID, string(data)); err != nil {
			return fmt.Errorf("repush result: %w", err)
		}
		nextSeq--
	}

	return tx.Commit()
}

func (s *SQLite) DeleteAckedResults(ctx context.Context, resultIDs []string) {
	_ = ctx
	_ = resultIDs
}

func (s *SQLite) StoreResult(ctx context.Context, result *models.RelayResult, ttlSeconds int) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin store result tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO results (request_id, data, expires_at)
		VALUES (?, ?, ?)
		ON CONFLICT(request_id) DO UPDATE SET data = excluded.data, expires_at = excluded.expires_at
	`, result.RequestID, string(data), time.Now().Add(time.Duration(ttlSeconds)*time.Second).Unix()); err != nil {
		return fmt.Errorf("store result: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE requests SET status = ? WHERE id = ?`, string(models.StatusCompleted), result.RequestID); err != nil {
		return fmt.Errorf("update completed status: %w", err)
	}

	return tx.Commit()
}

func (s *SQLite) GetResult(ctx context.Context, requestID string) (*models.RelayResult, error) {
	if err := s.cleanupExpiredResult(ctx, requestID); err != nil {
		return nil, err
	}

	row := s.db.QueryRowContext(ctx, `SELECT data FROM results WHERE request_id = ?`, requestID)
	var data string
	if err := row.Scan(&data); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get result: %w", err)
	}

	var result models.RelayResult
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}
	return &result, nil
}

func (s *SQLite) ResultExists(ctx context.Context, requestID string) (bool, error) {
	if err := s.cleanupExpiredResult(ctx, requestID); err != nil {
		return false, err
	}
	count, err := s.count(ctx, `SELECT COUNT(*) FROM results WHERE request_id = ?`, requestID)
	return count > 0, err
}

func (s *SQLite) UpdateResultWebhookStatus(ctx context.Context, requestID string, status models.ResultStatus) error {
	_, err := s.db.ExecContext(ctx, `UPDATE requests SET webhook_status = ? WHERE id = ?`, string(status), requestID)
	return err
}

func (s *SQLite) GetResultTTL(ctx context.Context, requestID string) (int64, error) {
	row := s.db.QueryRowContext(ctx, `SELECT expires_at FROM results WHERE request_id = ?`, requestID)
	var expiresAt int64
	if err := row.Scan(&expiresAt); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("get result TTL: %w", err)
	}

	ttl := expiresAt - time.Now().Unix()
	if ttl <= 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM results WHERE request_id = ?`, requestID); err != nil {
			return 0, err
		}
		return 0, nil
	}
	return ttl, nil
}

func (s *SQLite) StoreAPIToken(ctx context.Context, t *models.APIToken) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO api_tokens (id, name, token_hash, created_at, last_used, usage_count)
		VALUES (?, ?, ?, ?, NULL, ?)
	`, t.ID, t.Name, t.TokenHash, t.CreatedAt.Unix(), t.UsageCount)
	return err
}

func (s *SQLite) ValidateAPIToken(ctx context.Context, tokenHash string) (*models.APIToken, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin validate token tx: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `
		SELECT id, name, token_hash, created_at, last_used, usage_count
		FROM api_tokens WHERE token_hash = ?
	`, tokenHash)
	var token models.APIToken
	var createdAt int64
	var lastUsed sql.NullInt64
	if err := row.Scan(&token.ID, &token.Name, &token.TokenHash, &createdAt, &lastUsed, &token.UsageCount); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("invalid token")
		}
		return nil, fmt.Errorf("validate token: %w", err)
	}

	token.CreatedAt = time.Unix(createdAt, 0)
	if lastUsed.Valid {
		token.LastUsed = time.Unix(lastUsed.Int64, 0)
	}
	token.LastUsed = time.Now()
	token.UsageCount++

	if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET last_used = ?, usage_count = ? WHERE id = ?`,
		token.LastUsed.Unix(), token.UsageCount, token.ID); err != nil {
		return nil, fmt.Errorf("update token usage: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit validate token tx: %w", err)
	}
	return &token, nil
}

func (s *SQLite) ListAPITokens(ctx context.Context) ([]*models.APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, token_hash, created_at, last_used, usage_count
		FROM api_tokens ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	defer rows.Close()

	var tokens []*models.APIToken
	for rows.Next() {
		var token models.APIToken
		var createdAt int64
		var lastUsed sql.NullInt64
		if err := rows.Scan(&token.ID, &token.Name, &token.TokenHash, &createdAt, &lastUsed, &token.UsageCount); err != nil {
			return nil, fmt.Errorf("scan api token: %w", err)
		}
		token.CreatedAt = time.Unix(createdAt, 0)
		if lastUsed.Valid {
			token.LastUsed = time.Unix(lastUsed.Int64, 0)
		}
		tokens = append(tokens, &token)
	}
	return tokens, rows.Err()
}

func (s *SQLite) DeleteAPIToken(ctx context.Context, tokenID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, tokenID)
	if err != nil {
		return fmt.Errorf("delete api token: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("token not found")
	}
	return nil
}

func (s *SQLite) APITokenCount(ctx context.Context) (int64, error) {
	return s.count(ctx, `SELECT COUNT(*) FROM api_tokens`)
}

func (s *SQLite) AddProxy(ctx context.Context, proxyURL string, priority float64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO proxies (url, priority, fail_count, last_checked)
		VALUES (?, ?, 0, 0)
		ON CONFLICT(url) DO UPDATE SET priority = excluded.priority
	`, proxyURL, priority)
	return err
}

func (s *SQLite) RemoveProxy(ctx context.Context, proxyURL string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM proxies WHERE url = ?`, proxyURL)
	return err
}

func (s *SQLite) ListProxyRecords(ctx context.Context) ([]ProxyRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT url, priority, fail_count, last_checked
		FROM proxies
		ORDER BY priority ASC, url ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list proxies: %w", err)
	}
	defer rows.Close()

	var proxies []ProxyRecord
	for rows.Next() {
		var p ProxyRecord
		var lastChecked int64
		if err := rows.Scan(&p.URL, &p.Priority, &p.FailCount, &lastChecked); err != nil {
			return nil, fmt.Errorf("scan proxy: %w", err)
		}
		if lastChecked > 0 {
			p.LastChecked = time.Unix(lastChecked, 0)
		}
		proxies = append(proxies, p)
	}
	return proxies, rows.Err()
}

func (s *SQLite) MarkProxySuccess(ctx context.Context, proxyURL string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE proxies
		SET fail_count = 0, last_checked = ?
		WHERE url = ?
	`, time.Now().Unix(), proxyURL)
	return err
}

func (s *SQLite) MarkProxyFailed(ctx context.Context, proxyURL string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin mark proxy failed tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE proxies
		SET fail_count = fail_count + 1, last_checked = ?
		WHERE url = ?
	`, time.Now().Unix(), proxyURL); err != nil {
		return 0, fmt.Errorf("update proxy fail count: %w", err)
	}

	var failCount int
	if err := tx.QueryRowContext(ctx, `SELECT fail_count FROM proxies WHERE url = ?`, proxyURL).Scan(&failCount); err != nil {
		return 0, fmt.Errorf("get proxy fail count: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit mark proxy failed tx: %w", err)
	}
	return failCount, nil
}

func (s *SQLite) ResetProxyCooldown(ctx context.Context, proxyURL string) error {
	return s.MarkProxySuccess(ctx, proxyURL)
}

func (s *SQLite) ResetAllProxyCooldowns(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE proxies
		SET fail_count = 0, last_checked = ?
		WHERE fail_count > 0
	`, time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("reset all proxy cooldowns: %w", err)
	}
	affected, _ := res.RowsAffected()
	return int(affected), nil
}

func (s *SQLite) UpdateProxyURL(ctx context.Context, oldURL, newURL string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE proxies SET url = ? WHERE url = ?`, newURL, oldURL)
	return err
}

func (s *SQLite) ProxyCount(ctx context.Context) (int64, error) {
	return s.count(ctx, `SELECT COUNT(*) FROM proxies`)
}

func (s *SQLite) cleanupExpiredResult(ctx context.Context, requestID string) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM results
		WHERE request_id = ? AND expires_at <= ?
	`, requestID, time.Now().Unix())
	return err
}

func (s *SQLite) count(ctx context.Context, query string, args ...any) (int64, error) {
	row := s.db.QueryRowContext(ctx, query, args...)
	var count int64
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
