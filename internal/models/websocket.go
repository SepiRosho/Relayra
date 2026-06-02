package models

import "time"

const (
	WSMessageTypeHello        = "hello"
	WSMessageTypeResume       = "resume"
	WSMessageTypeAck          = "ack"
	WSMessageTypePushRequest  = "push_request"
	WSMessageTypePushChunk    = "push_chunk"
	WSMessageTypeRequestState = "request_state"
	WSMessageTypeResult       = "result"
	WSMessageTypeResultChunk  = "result_chunk"
	WSMessageTypeChunkReceipt = "chunk_receipt"
	WSMessageTypeKeepalive    = "keepalive"
	WSMessageTypeError        = "error"
)

// WSMessage is the live websocket frame used by websocket transport mode.
type WSMessage struct {
	Type            string            `json:"type"`
	PeerID          string            `json:"peer_id,omitempty"`
	Seq             int64             `json:"seq,omitempty"`
	Ack             int64             `json:"ack,omitempty"`
	LastReceivedSeq int64             `json:"last_received_seq,omitempty"`
	SentAt          int64             `json:"sent_at,omitempty"`
	Request         *RelayRequest     `json:"request,omitempty"`
	Chunk           *TransportChunk   `json:"chunk,omitempty"`
	RequestState    *RequestSyncState `json:"request_state,omitempty"`
	Result          *RelayResult      `json:"result,omitempty"`
	ResultChunk     *TransportChunk   `json:"result_chunk,omitempty"`
	ChunkReceipt    *ChunkReceipt     `json:"chunk_receipt,omitempty"`
	Probe           *ProbeMessage     `json:"probe,omitempty"`
	Error           string            `json:"error,omitempty"`
}

// WSOutboxMessage stores a durable websocket message awaiting acknowledgement.
type WSOutboxMessage struct {
	Scope     string    `json:"scope"`
	Seq       int64     `json:"seq"`
	Type      string    `json:"type"`
	RefID     string    `json:"ref_id,omitempty"`
	Payload   string    `json:"payload"`
	CreatedAt time.Time `json:"created_at"`
}

// WSSequenceState stores the durable sequence cursors for one websocket scope.
type WSSequenceState struct {
	Scope           string    `json:"scope"`
	NextOutboundSeq int64     `json:"next_outbound_seq"`
	LastReceivedSeq int64     `json:"last_received_seq"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func ListenerWSScope(peerID string) string {
	return "listener:" + peerID
}

func SenderWSScope(listenerPeerID string) string {
	return "sender:" + listenerPeerID
}
