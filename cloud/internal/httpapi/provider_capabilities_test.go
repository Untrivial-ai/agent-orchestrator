package httpapi

import (
	"slices"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

// A server with coder capability-gated (AO_CLOUD_CAPABILITY_GATED_PROVIDERS=coder).
func gatedServer() *Server {
	return &Server{capabilityGatedProviders: map[string]bool{"coder": true}}
}

func TestOrgAllowsProvider(t *testing.T) {
	s := gatedServer()
	cases := []struct {
		name         string
		provider     string
		capabilities []string
		want         bool
	}{
		{"ungated provider without caps", "nodeops", nil, true},
		{"ungated provider ignores caps", "nodeops", []string{"coder"}, true},
		{"gated coder with capability", "coder", []string{"coder"}, true},
		{"gated coder with capability among others", "coder", []string{"feature-x", "coder"}, true},
		{"gated coder without any caps", "coder", nil, false},
		{"gated coder with unrelated cap only", "coder", []string{"feature-x"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.orgAllowsProvider(domain.Principal{OrgCapabilities: tc.capabilities}, tc.provider)
			if got != tc.want {
				t.Fatalf("orgAllowsProvider(caps=%v, %q) = %v, want %v", tc.capabilities, tc.provider, got, tc.want)
			}
		})
	}
}

// The rollout-safe default: with no providers configured as gated (the empty /
// nil set), every provider is allowed regardless of org capabilities, so
// shipping the code does not disturb existing coder users.
func TestUngatedServerAllowsEverything(t *testing.T) {
	s := &Server{} // capabilityGatedProviders is nil
	if !s.orgAllowsProvider(domain.Principal{}, "coder") {
		t.Fatal("with no gated providers, coder must be allowed (rollout-safe default)")
	}
}

func TestProvidersForOrg(t *testing.T) {
	s := gatedServer()
	available := []string{"nodeops", "coder"}

	withoutCoder := s.providersForOrg(domain.Principal{}, available)
	if !slices.Equal(withoutCoder, []string{"nodeops"}) {
		t.Fatalf("no capability: got %v, want [nodeops]", withoutCoder)
	}

	withCoder := s.providersForOrg(domain.Principal{OrgCapabilities: []string{"coder"}}, available)
	if !slices.Equal(withCoder, []string{"nodeops", "coder"}) {
		t.Fatalf("coder capability: got %v, want [nodeops coder]", withCoder)
	}
}
