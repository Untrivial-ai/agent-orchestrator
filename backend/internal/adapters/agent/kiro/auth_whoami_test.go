package kiro

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// The two payloads below are verbatim from kiro-cli on macOS: the signed-out
// one measured against 2.21.0, the signed-in one reported in #5048 against
// 2.21.1 (identifiers redacted). Neither contains a phrase the shared prose
// classifier looks for, so before this both answered "unknown" and the probe
// could not tell the states apart.
const (
	signedOutWhoami = `{"account":null}`
	signedInWhoami  = `{"accountType":"IamIdentityCenter","email":"user@example.com","region":"eu-west-1","startUrl":"https://d-abc.awsapps.com/start"}

Profile:
KiroProfile-us-east-1
arn:aws:codewhisperer:us-east-1:111122223333:profile/ABCDEF
`
)

func withRunner(t *testing.T, out string, err error) *map[string]string {
	t.Helper()
	seen := new(map[string]string)
	original := authprobe.CmdRunnerEnv
	authprobe.CmdRunnerEnv = func(_ context.Context, env map[string]string, _ string, _ ...string) ([]byte, error) {
		*seen = env
		return []byte(out), err
	}
	t.Cleanup(func() { authprobe.CmdRunnerEnv = original })
	return seen
}

func TestKiroWhoamiAuthStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want ports.AgentAuthStatus
	}{
		{"signed out reports unauthorized", signedOutWhoami, ports.AgentAuthStatusUnauthorized},
		{"signed in reports authorized despite the non-JSON profile trailer", signedInWhoami, ports.AgentAuthStatusAuthorized},
		{"account object present means signed in", `{"account":{"id":"abc"}}`, ports.AgentAuthStatusAuthorized},
		// Never invent a denial from output we do not recognize: unknown keeps
		// discovery working rather than silently disabling it.
		{"unrecognized shape stays unknown", `{"somethingElse":1}`, ports.AgentAuthStatusUnknown},
		{"non-JSON stays unknown", `command not found`, ports.AgentAuthStatusUnknown},
		{"empty identity fields stay unknown", `{"email":"  "}`, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = withRunner(t, tc.out, nil)
			got, err := kiroWhoamiAuthStatus(context.Background(), "kiro-cli", nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

// A signed-out answer is worth trusting even when the CLI exits non-zero,
// because that is the state AO must not run `chat --list-models` in.
func TestKiroWhoamiClassifiesSignedOutDespiteExitCode(t *testing.T) {
	_ = withRunner(t, signedOutWhoami, context.DeadlineExceeded)
	got, err := kiroWhoamiAuthStatus(context.Background(), "kiro-cli", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != ports.AgentAuthStatusUnauthorized {
		t.Fatalf("status = %q, want unauthorized", got)
	}
}

func TestKiroWhoamiWithoutBinaryIsUnknown(t *testing.T) {
	got, err := kiroWhoamiAuthStatus(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want unknown", got)
	}
}

// The probe must run under the same environment the gated command would use,
// or it reports on credentials that command would never have seen.
// https://github.com/Untrivial-ai/agent-orchestrator/pull/5321#discussion_r3999115728
func TestKiroWhoamiRunsUnderTheGivenEnvironment(t *testing.T) {
	seen := withRunner(t, signedOutWhoami, nil)
	env := map[string]string{"AWS_PROFILE": "work"}
	if _, err := kiroWhoamiAuthStatus(context.Background(), "kiro-cli", env); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if (*seen)["AWS_PROFILE"] != "work" {
		t.Fatalf("probe ran with env %#v, want the caller's overlay", *seen)
	}
}

// A project-scoped KIRO_API_KEY authenticates the agent even though the
// daemon's own environment has none, so the probe must not shell out at all.
func TestKiroAPIKeyFromProjectEnvIsAuthorized(t *testing.T) {
	t.Setenv("KIRO_API_KEY", "")
	if got := kiroAPIKey(map[string]string{"KIRO_API_KEY": "project-key"}); got != "project-key" {
		t.Fatalf("kiroAPIKey = %q, want the project overlay value", got)
	}
	if got := kiroAPIKey(nil); got != "" {
		t.Fatalf("kiroAPIKey = %q, want empty when neither source sets it", got)
	}
}
