package domain

import "time"

type ProviderID string
type ProviderModelID string

type APIProtocol string

const (
	APIProtocolAnthropicCompatible APIProtocol = "anthropic-compatible"
	APIProtocolOpenAICompatible    APIProtocol = "openai-compatible"
)

type Provider struct {
	ID               ProviderID  `json:"id"`
	DisplayName      string      `json:"displayName"`
	APIProtocol      APIProtocol `json:"apiProtocol"`
	BaseURL          string      `json:"baseUrl"`
	SecretRef        string      `json:"-"`
	SecretConfigured bool        `json:"secretConfigured"`
	Enabled          bool        `json:"enabled"`
	CreatedAt        time.Time   `json:"createdAt"`
	UpdatedAt        time.Time   `json:"updatedAt"`
}

type ProviderModel struct {
	ID          ProviderModelID `json:"id"`
	ProviderID  ProviderID      `json:"providerId"`
	DisplayName string          `json:"displayName"`
	ModelName   string          `json:"modelName"`
	Enabled     bool            `json:"enabled"`
	SortOrder   int             `json:"sortOrder"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

type ProviderAudit struct {
	ID         string
	ProviderID ProviderID
	Action     string
	Detail     string
	CreatedAt  time.Time
}
