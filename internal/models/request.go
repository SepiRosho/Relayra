package models

import "time"

// RelayRequest represents a request submitted by a user to be relayed to a peer.
type RelayRequest struct {
	ID            string        `json:"id"`
	DestinationID string        `json:"destination_peer_id"`
	WebhookURL    string        `json:"webhook_url,omitempty"`
	Async         bool          `json:"async,omitempty"`
	Request       HTTPRequest   `json:"request"`
	Status        RequestStatus `json:"status"`
	CreatedAt     time.Time     `json:"created_at"`
}

// HTTPRequest is the actual HTTP request to be executed on the remote peer.
type HTTPRequest struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// RequestStatus tracks the lifecycle of a relay request.
type RequestStatus string

const (
	StatusPending   RequestStatus = "pending"
	StatusQueued    RequestStatus = "queued"
	StatusLeased    RequestStatus = "leased"
	StatusReceived  RequestStatus = "received"
	StatusExecuting RequestStatus = "executing"
	StatusCompleted RequestStatus = "completed"
	StatusFailed    RequestStatus = "failed"
)

// RequestSyncState tracks the Sender-side durable state for a request.
type RequestSyncState struct {
	RequestID   string        `json:"request_id"`
	Status      RequestStatus `json:"status"`
	LeaseUntil  time.Time     `json:"lease_until,omitempty"`
	UpdatedAt   time.Time     `json:"updated_at"`
	DuplicateOK bool          `json:"duplicate_ok,omitempty"`
}
