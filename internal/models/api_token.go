package models

import "time"

// APIToken represents an API token for authenticating relay requests.
type APIToken struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Token      string    `json:"token,omitempty"` // Only shown on creation
	TokenHash  string    `json:"-"`               // SHA256 hash stored in Redis
	CreatedAt  time.Time `json:"created_at"`
	LastUsed   time.Time `json:"last_used,omitempty"`
	UsageCount int64     `json:"usage_count"`
}
