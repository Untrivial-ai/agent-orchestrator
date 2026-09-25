package modelcatalog

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// The sign-in gate is meant to skip discovery because it *recognized* that Kiro
// is signed out. Before the classifier understood `{"account":null}`, the real
// signed-out payload read as unknown and the skip only happened because the CLI
// also exits non-zero — a generic "check failed", not a recognized sign-in.
//
// That distinction matters: on the generic path the skip is recorded as an
// ordinary discovery failure that spends retry budget, and if Kiro ever exits 0
// while signed out the gate stops firing altogether and the browser sign-in
// returns. Pin the recognized path.
// https://github.com/Untrivial-ai/agent-orchestrator/issues/5048
func TestKiroSignInGateRecognizesTheSignedOutPayload(t *testing.T) {
	previous := signInProbe
	t.Cleanup(func() { signInProbe = previous })
	signInProbe = func(context.Context, string, []string, string, map[string]string) ([]byte, error) {
		// Verbatim signed-out output from kiro-cli 2.21.0, reported with the
		// exit status the CLI actually returns.
		return []byte(`{"account":null}`), errors.New("exit status 1")
	}

	err := kiroSignIn.check(context.Background(), "kiro", "kiro-cli", "", nil)
	if !errors.Is(err, ports.ErrAgentModelDiscoverySignInRequired) {
		t.Fatalf("err = %v, want a recognized ErrAgentModelDiscoverySignInRequired", err)
	}
}

// A signed-in payload must let discovery through, trailer and all.
func TestKiroSignInGateAllowsASignedInPayload(t *testing.T) {
	previous := signInProbe
	t.Cleanup(func() { signInProbe = previous })
	signInProbe = func(context.Context, string, []string, string, map[string]string) ([]byte, error) {
		return []byte("{\"accountType\":\"IamIdentityCenter\",\"email\":\"user@example.com\"}\n\nProfile:\nKiroProfile-us-east-1\n"), nil
	}

	if err := kiroSignIn.check(context.Background(), "kiro", "kiro-cli", "", nil); err != nil {
		t.Fatalf("err = %v, want discovery allowed for a signed-in CLI", err)
	}
}
