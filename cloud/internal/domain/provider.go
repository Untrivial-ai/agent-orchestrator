package domain

import (
	"encoding/json"
	"time"
)

type ProviderConnection struct {
	ID              string
	OrgID           string
	Provider        string
	Label           string
	Config          json.RawMessage
	ValidationState string
	ValidatedAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// UserProviderConnection is a user-scoped coding-agent credential.
type UserProviderConnection struct {
	ID              string
	UserID          string
	Provider        string
	Label           string
	Config          json.RawMessage
	ValidationState string
	ValidatedAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
