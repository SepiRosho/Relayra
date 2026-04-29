package models

import "time"

// RelayResult holds the response from executing a relayed request on the Sender.
type RelayResult struct {
	RequestID  string            `json:"request_id"`
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body,omitempty"`
	Error      string            `json:"error,omitempty"`
	Duration   int64             `json:"duration_ms"`
	ExecutedAt time.Time         `json:"executed_at"`
}

// ResultDeliveryStatus tracks sender-side durability for returning results.
type ResultDeliveryStatus string

const (
	ResultPending ResultDeliveryStatus = "pending"
	ResultLeased  ResultDeliveryStatus = "leased"
	ResultAcked   ResultDeliveryStatus = "acked"
)

// ResultStatus tracks webhook delivery state.
type ResultStatus string

const (
	ResultStored      ResultStatus = "stored"
	ResultWebhookSent ResultStatus = "webhook_sent"
	ResultWebhookFail ResultStatus = "webhook_failed"
)
