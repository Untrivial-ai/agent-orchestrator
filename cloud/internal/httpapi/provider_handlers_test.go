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

func TestListAvailableAgentsLogicWithValidCredentials(t *testing.T) {
	validatedAt := time.Now()
	connections := []domain.ProviderConnection{
		{
			Provider:        "claude-code",
			Label:           "default",
			ValidationState: "valid",
			ValidatedAt:     &validatedAt,
		},
		{
			Provider:        "codex",
			Label:           "default",
			ValidationState: "invalid",
		},
		{
			Provider:        "cursor",
			Label:           "default",
			ValidationState: "valid",
			ValidatedAt:     &validatedAt,
		},
	}

	// Test that agents are correctly built with validation status
	type agentResult struct {
		ID              string `json:"id"`
		Provider        string `json:"provider"`
		HasValidCred    bool   `json:"hasValidCred"`
		ValidationState string `json:"validationState"`
	}
	agents := []agentResult{}

	for _, provider := range validAgentsList {
		hasValid := agentConnectionAvailable(connections, provider)
		state := "not_configured"
		for _, conn := range connections {
			if conn.Provider == provider && conn.Label == defaultAgentConnectionLabel {
				state = conn.ValidationState
				break
			}
		}
		agents = append(agents, agentResult{
			ID:              provider,
			Provider:        provider,
			HasValidCred:    hasValid,
			ValidationState: state,
		})
	}

	if len(agents) != 3 {
		t.Errorf("expected 3 agents, got %d", len(agents))
	}

	// Check claude-code has valid credential
	if agents[0].HasValidCred != true {
		t.Errorf("expected claude-code to have valid credential")
	}
	if agents[0].ValidationState != "valid" {
		t.Errorf("expected claude-code validationState to be 'valid', got %s", agents[0].ValidationState)
	}

	// Check codex doesn't have valid credential
	if agents[1].HasValidCred != false {
		t.Errorf("expected codex to not have valid credential")
	}
	if agents[1].ValidationState != "invalid" {
		t.Errorf("expected codex validationState to be 'invalid', got %s", agents[1].ValidationState)
	}

	// Check cursor has valid credential
	if agents[2].HasValidCred != true {
		t.Errorf("expected cursor to have valid credential")
	}
}

func TestListAvailableAgentsLogicWithoutCredentials(t *testing.T) {
	connections := []domain.ProviderConnection{}

	// Test agent list building with no connections
	type agentResult struct {
		ID              string `json:"id"`
		Provider        string `json:"provider"`
		HasValidCred    bool   `json:"hasValidCred"`
		ValidationState string `json:"validationState"`
	}
	agents := []agentResult{}

	for _, provider := range validAgentsList {
		hasValid := agentConnectionAvailable(connections, provider)
		state := "not_configured"
		for _, conn := range connections {
			if conn.Provider == provider && conn.Label == defaultAgentConnectionLabel {
				state = conn.ValidationState
				break
			}
		}
		agents = append(agents, agentResult{
			ID:              provider,
			Provider:        provider,
			HasValidCred:    hasValid,
			ValidationState: state,
		})
	}

	if len(agents) != 3 {
		t.Errorf("expected 3 agents, got %d", len(agents))
	}

	// All agents should have no valid credential and state "not_configured"
	for i, agent := range agents {
		if agent.HasValidCred != false {
			t.Errorf("agent %d should not have valid credential", i)
		}
		if agent.ValidationState != "not_configured" {
			t.Errorf("agent %d should have validationState 'not_configured', got %s", i, agent.ValidationState)
		}
	}
}
