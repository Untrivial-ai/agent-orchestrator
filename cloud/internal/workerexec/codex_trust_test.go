package workerexec

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// Run with GOWORK=off too: this must exercise the dependency used by images,
// not just the repository workspace's replacement of the backend module.
func TestCodexInteractiveTrustPolicy(t *testing.T) {
	for _, mode := range []string{"read-only", "standard", "trusted"} {
		for _, identity := range []string{"", "restored-native-thread"} {
			t.Run(mode+"/"+identity, func(t *testing.T) {
				builder := HarnessBuilder{DataDir: t.TempDir(), CodexLogin: func(string, string, string, string) error { return nil }}
				command, err := builder.BuildInteractive(worker.LaunchContext{
					Harness: "codex", Mode: mode, SessionID: "fixture", AgentSessionID: identity,
				}, worker.CredentialResponse{Provider: "codex", CredentialType: "api_key", Secret: "fixture"}, filepath.Join(t.TempDir(), "workspace"))
				if mode == "read-only" {
					if !errors.Is(err, ErrUnsupportedPolicy) {
						t.Fatalf("interactive read-only policy must remain refused: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if command.Cleanup != nil {
					t.Cleanup(command.Cleanup)
				}
				approvals := 0
				for _, arg := range command.Args {
					if arg == "--dangerously-bypass-hook-trust" || strings.Contains(arg, "trust_level") {
						t.Fatalf("worker grants repository execution trust: %q", arg)
					}
					if strings.HasPrefix(arg, "hooks.state=") {
						approvals = strings.Count(arg, "trusted_hash=")
					}
				}
				if approvals != 4 {
					t.Fatalf("scoped AO approvals = %d, want 4", approvals)
				}
				if identity != "" && (len(command.Args) == 0 || command.Args[0] != "resume") {
					t.Fatal("restored worker lost its native conversation")
				}
			})
		}
	}
}
