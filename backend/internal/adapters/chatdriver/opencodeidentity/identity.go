// Package opencodeidentity keeps saved OpenCode ACP conversation handles bound
// to the major-version harness that created them.
package opencodeidentity

import (
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const prefix = "ao-harness:"

// Encode records the harness beside an otherwise opaque provider conversation
// ID. The ACP transport continues to use the raw ID internally.
func Encode(harness domain.AgentHarness, providerID string) string {
	if providerID == "" {
		return ""
	}
	return prefix + string(harness) + ":" + providerID
}

// Decode verifies the durable harness identity and returns the provider's raw
// conversation ID. Existing OpenCode 1 rows predate the envelope and remain
// valid only for the v1 harness.
func Decode(expected domain.AgentHarness, stored string) (string, error) {
	value, tagged := strings.CutPrefix(stored, prefix)
	if !tagged {
		if expected == domain.HarnessOpenCode && stored != "" {
			return stored, nil
		}
		return "", fmt.Errorf("%w: provider conversation has no %s harness identity", ports.ErrChatResumeFailed, expected)
	}
	harness, providerID, found := strings.Cut(value, ":")
	if !found || providerID == "" {
		return "", fmt.Errorf("%w: invalid OpenCode provider conversation identity", ports.ErrChatResumeFailed)
	}
	if domain.AgentHarness(harness) != expected {
		return "", fmt.Errorf("%w: provider conversation belongs to harness %q, not %q", ports.ErrChatResumeFailed, harness, expected)
	}
	return providerID, nil
}
