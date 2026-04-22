package models

// PollRequest is sent by the Sender to the Listener during each poll cycle.
// The Payload is encrypted with AES-256-GCM before transmission.
type PollRequest struct {
	PeerID      string `json:"peer_id"`
	Nonce       string `json:"nonce"`     // base64-encoded nonce
	Timestamp   int64  `json:"timestamp"` // Unix timestamp for replay protection
	Payload     string `json:"payload"`   // base64-encoded encrypted PollPayloadUp
	WaitSeconds int    `json:"wait_seconds,omitempty"`
}

// PollResponse is returned by the Listener to the Sender.
type PollResponse struct {
	Nonce     string `json:"nonce"`
	Timestamp int64  `json:"timestamp"`
	Payload   string `json:"payload"` // base64-encoded encrypted PollPayloadDown
}

// PollPayloadUp is the decrypted payload sent by the Sender.
type PollPayloadUp struct {
	Results       []RelayResult `json:"results,omitempty"`
	AckRequestIDs []string      `json:"ack_request_ids,omitempty"` // Sender confirms it received these requests
}

// PollPayloadDown is the decrypted payload sent by the Listener.
type PollPayloadDown struct {
	Requests     []RelayRequest `json:"requests,omitempty"`
	AckResultIDs []string       `json:"ack_result_ids,omitempty"` // Listener confirms it received these results
}
