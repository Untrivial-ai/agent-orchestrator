package modelcatalog

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// claudeRequest builds a discovery request isolated from this machine.
//
// The configured-default pass reads ANTHROPIC_MODEL and ~/.claude/settings.json,
// so without this the developer's own configured model is appended to every
// catalog under test and the assertions drift per machine.
func claudeRequest(t *testing.T) ports.AgentModelDiscoveryRequest {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ANTHROPIC_MODEL", "")
	return ports.AgentModelDiscoveryRequest{
		AgentID: "claude-code", WorkingDir: t.TempDir(), Env: map[string]string{},
	}
}

// The picker must show what the configured provider actually serves. On
// Bedrock and Vertex the static aliases are simply wrong IDs.
func TestClaudeCatalogPrefersProviderModels(t *testing.T) {
	list := func(context.Context, ports.AgentModelDiscoveryRequest) ([]string, error) {
		return []string{"us.anthropic.claude-opus-4-5-v1:0", "us.anthropic.claude-sonnet-4-5-v1:0"}, nil
	}
	catalog := discoverClaudeCatalog(context.Background(), claudeRequest(t), list)
	if catalog.Source != "provider" {
		t.Fatalf("source = %q, want provider", catalog.Source)
	}
	if len(catalog.Models) != 2 {
		t.Fatalf("models = %+v, want the two provider IDs", catalog.Models)
	}
	for _, model := range catalog.Models {
		if model.ID == "sonnet" || model.ID == "opus" {
			t.Fatalf("static alias %q leaked into a provider-sourced catalog", model.ID)
		}
	}
}

// Discovery must never empty the picker. Every way of failing to reach the
// provider falls back to the aliases that shipped before.
func TestClaudeCatalogFallsBackWhenTheProviderCannotBeAsked(t *testing.T) {
	tests := []struct {
		name string
		list ClaudeModelListFunc
	}{
		{name: "no lister wired", list: nil},
		{
			name: "provider unreachable",
			list: func(context.Context, ports.AgentModelDiscoveryRequest) ([]string, error) {
				return nil, errors.New("could not reach Anthropic")
			},
		},
		{
			name: "credential rejected",
			list: func(context.Context, ports.AgentModelDiscoveryRequest) ([]string, error) {
				return nil, errors.New("provider rejected the credential")
			},
		},
		{
			name: "chain-sourced credential, nothing readable",
			list: func(context.Context, ports.AgentModelDiscoveryRequest) ([]string, error) {
				return nil, errors.New("no credential could be resolved")
			},
		},
		{
			name: "provider returned an empty list",
			list: func(context.Context, ports.AgentModelDiscoveryRequest) ([]string, error) {
				return []string{}, nil
			},
		},
		{
			name: "provider returned only blanks",
			list: func(context.Context, ports.AgentModelDiscoveryRequest) ([]string, error) {
				return []string{"", "   "}, nil
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			catalog := discoverClaudeCatalog(context.Background(), claudeRequest(t), tc.list)
			if len(catalog.Models) == 0 {
				t.Fatal("the picker must never be emptied by a discovery failure")
			}
			if catalog.Source != "catalog" {
				t.Fatalf("source = %q, want the static catalog", catalog.Source)
			}
			ids := map[string]bool{}
			for _, model := range catalog.Models {
				ids[model.ID] = true
			}
			if !ids["sonnet"] || !ids["opus"] {
				t.Fatalf("fallback lost the static aliases: %+v", catalog.Models)
			}
		})
	}
}

func TestClaudeProviderModelsAreDeduped(t *testing.T) {
	list := func(context.Context, ports.AgentModelDiscoveryRequest) ([]string, error) {
		return []string{"claude-opus-4-5-20251101", "claude-opus-4-5-20251101", " claude-haiku-4-5 "}, nil
	}
	catalog := discoverClaudeCatalog(context.Background(), claudeRequest(t), list)
	if len(catalog.Models) != 2 {
		t.Fatalf("models = %+v, want duplicates collapsed and whitespace trimmed", catalog.Models)
	}
}

// Labels are cosmetic, so the rule stays shallow: an unfamiliar shape must show
// its raw ID rather than a confidently wrong friendly name.
func TestClaudeModelLabel(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"claude-opus-4-5-20251101", "Opus 4.5"},
		{"claude-sonnet-4-5-20250929", "Sonnet 4.5"},
		{"us.anthropic.claude-opus-4-5-v1:0", "Opus 4.5"},
		{"anthropic.claude-haiku-4-5-v1:0", "Haiku 4.5"},
		{"claude-opus-4-5@20251101", "Opus 4.5"},
		{"claude-haiku-4-5", "Haiku 4.5"},
		// Unfamiliar shapes fall back to the ID itself.
		{"some-future-model", "some-future-model"},
		{"gpt-4o", "gpt-4o"},
		{"claude-", "claude-"},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			if got := claudeModelLabel(tc.id); got != tc.want {
				t.Fatalf("claudeModelLabel(%q) = %q, want %q", tc.id, got, tc.want)
			}
		})
	}
}
