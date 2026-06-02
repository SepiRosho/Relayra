package models

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

const (
	TransportChunkKindRequest = "request"
	TransportChunkKindResult  = "result"
)

// TransportChunk carries part of a larger transport payload.
type TransportChunk struct {
	TransferID string `json:"transfer_id"`
	RequestID  string `json:"request_id"`
	Kind       string `json:"kind"`
	Index      int    `json:"index"`
	Total      int    `json:"total"`
	Payload    string `json:"payload"`
	Checksum   string `json:"checksum"`
	TotalSize  int    `json:"total_size"`
}

// ChunkReceipt reports sender-side progress for a chunked payload.
type ChunkReceipt struct {
	TransferID string `json:"transfer_id"`
	RequestID  string `json:"request_id"`
	NextIndex  int    `json:"next_index"`
	Completed  bool   `json:"completed,omitempty"`
	Reset      bool   `json:"reset,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ProbeMessage is used by websocket reliability checks and runtime keepalives.
type ProbeMessage struct {
	ID        string `json:"id"`
	Sequence  int    `json:"sequence"`
	SentAt    int64  `json:"sent_at"`
	Ack       bool   `json:"ack,omitempty"`
	ProbeOnly bool   `json:"probe_only,omitempty"`
}

// ChunkCursor tracks the next chunk index to send for a transfer.
type ChunkCursor struct {
	TransferID string    `json:"transfer_id"`
	RequestID  string    `json:"request_id"`
	PeerID     string    `json:"peer_id"`
	NextIndex  int       `json:"next_index"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// RequestTransferID returns the transport transfer identifier for a request payload.
func RequestTransferID(requestID string) string {
	return fmt.Sprintf("%s:%s", requestID, TransportChunkKindRequest)
}

// ResultTransferID returns the transport transfer identifier for a result payload.
func ResultTransferID(requestID string) string {
	return fmt.Sprintf("%s:%s", requestID, TransportChunkKindResult)
}

// SHA256Hex returns the SHA-256 checksum for the provided payload.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
