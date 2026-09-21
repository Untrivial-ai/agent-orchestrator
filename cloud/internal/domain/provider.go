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

// WorkerGitHubPAT is the encrypted personal access token the current worker
// may use only for its own session's GitHub repository.
type WorkerGitHubPAT struct {
	OwnerUserID     string
	CloneURL        string
	EncryptedSecret []byte
	Nonce           []byte
}
