package kiro

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Both payloads are verbatim from kiro-cli on macOS: signed out measured
// against 2.21.0 (exits 1), signed in reported in #5048 against 2.21.1 with
// identifiers redacted (exits 0). Neither contains a phrase the shared prose
// classifier looks for, so before this both read as unknown and the probe could
// not tell the two states apart.
const (
	signedOutWhoami = `{"account":null}`
	signedInWhoami  = `{"accountType":"IamIdentityCenter","email":"user@example.com","region":"eu-west-1","startUrl":"https://d-abc.awsapps.com/start"}

Profile:
KiroProfile-us-east-1
arn:aws:codewhisperer:us-east-1:111122223333:profile/ABCDEF
`
)

func withWhoami(t *testing.T, out string, err error) {
	t.Helper()
	original := authprobe.CmdRunner
	authprobe.CmdRunner = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(out), err
	}
	t.Cleanup(func() { authprobe.CmdRunner = original })
}

func TestKiroWhoamiAuthStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		err  error
		want ports.AgentAuthStatus
	}{
		// The reported bug: a valid login read as unknown because the JSON is
		// followed by a plain-text profile block.
		{"signed in", signedInWhoami, nil, ports.AgentAuthStatusAuthorized},
		{"signed out", signedOutWhoami, errors.New("exit status 1"), ports.AgentAuthStatusUnauthorized},
		{"account object present", `{"account":{"id":"abc"}}`, nil, ports.AgentAuthStatusAuthorized},
		// Never invent an answer from output we do not recognize. Unknown keeps
		// callers on their existing behavior instead of asserting a wrong state.
		{"unrecognized json", `{"somethingElse":1}`, nil, ports.AgentAuthStatusUnknown},
		{"not json at all", `command not found`, errors.New("exit status 127"), ports.AgentAuthStatusUnknown},
		{"blank identity fields", `{"email":"  "}`, nil, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withWhoami(t, tc.out, tc.err)
			got, err := kiroWhoamiAuthStatus(context.Background(), "kiro-cli")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

// The signed-in payload exits 0 and the signed-out one exits 1, so the status
// must come from reading the output rather than from the exit code.
func TestKiroWhoamiIgnoresExitCode(t *testing.T) {
	withWhoami(t, signedInWhoami, errors.New("exit status 1"))
	got, err := kiroWhoamiAuthStatus(context.Background(), "kiro-cli")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want authorized even on a non-zero exit", got)
	}
}

func TestKiroWhoamiWithoutBinaryIsUnknown(t *testing.T) {
	got, err := kiroWhoamiAuthStatus(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want unknown", got)
	}
}
