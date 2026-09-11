package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

type mockProviderConnectionStore struct {
	connections map[string][]domain.ProviderConnection
	err         error
}

func (m *mockProviderConnectionStore) ListProviderConnections(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
) ([]domain.ProviderConnection, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.connections[orgID], nil
}

func (m *mockProviderConnectionStore) UpsertProviderConnection(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	provider string,
	label string,
	encrypted []byte,
	nonce []byte,
	config json.RawMessage,
) (domain.ProviderConnection, error) {
	return domain.ProviderConnection{}, nil
}

func (m *mockProviderConnectionStore) DeleteProviderConnection(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	provider string,
	label string,
) error {
	return nil
}

func TestAgentConnectionAvailable(t *testing.T) {
	validatedAt := time.Now()

	tests := []struct {
		name        string
		connections []domain.ProviderConnection
		provider    string
		expected    bool
	}{
		{
			name: "agent with valid credential",
			connections: []domain.ProviderConnection{
				{
					Provider:        "claude-code",
					Label:           "default",
					ValidationState: "valid",
					ValidatedAt:     &validatedAt,
				},
			},
			provider: "claude-code",
			expected: true,
		},
		{
			name: "agent without credential",
			connections: []domain.ProviderConnection{
				{
					Provider:        "cursor",
					Label:           "default",
					ValidationState: "valid",
				},
			},
			provider: "claude-code",
			expected: false,
		},
		{
			name: "agent with expired credential",
			connections: []domain.ProviderConnection{
				{
					Provider:        "claude-code",
					Label:           "default",
					ValidationState: "expired",
				},
			},
			provider: "claude-code",
			expected: false,
		},
		{
			name:        "no connections",
			connections: []domain.ProviderConnection{},
			provider:    "claude-code",
			expected:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := agentConnectionAvailable(tt.connections, tt.provider)
			if got != tt.expected {
				t.Errorf("agentConnectionAvailable() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestValidAgentsList(t *testing.T) {
	expected := []string{"claude-code", "codex", "cursor"}
	if len(validAgentsList) != len(expected) {
		t.Errorf("validAgentsList has %d agents, want %d", len(validAgentsList), len(expected))
	}
	for _, agent := range expected {
		found := false
		for _, a := range validAgentsList {
			if a == agent {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("agent %q not found in validAgentsList", agent)
		}
	}
}


func TestValidAgentProvider(t *testing.T) {
	tests := []struct {
		agent    string
		expected bool
	}{
		{"claude-code", true},
		{"codex", true},
		{"cursor", true},
		{"invalid-agent", false},
		{"", false},
		{"Claude-Code", false},
	}

	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			got := validAgentProvider(tt.agent)
			if got != tt.expected {
				t.Errorf("validAgentProvider(%q) = %v, want %v", tt.agent, got, tt.expected)
			}
		})
	}
}
