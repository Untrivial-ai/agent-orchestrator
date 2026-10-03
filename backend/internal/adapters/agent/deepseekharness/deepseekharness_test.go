package deepseekharness

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifestUsesConciseDeepSeekDisplayName(t *testing.T) {
	m := (&Plugin{}).Manifest()
	if m.ID != "deepseek-harness" || m.Name != "DeepSeek" {
		t.Fatalf("manifest = %#v", m)
	}
}

// TestGetConfigSpecReportsTheModelOverride pins the config contract every
// shipped adapter must hold: AO's agent settings need one configurable field, and
// for Harness that field is the Chat model override.
func TestGetConfigSpecReportsTheModelOverride(t *testing.T) {
	spec, err := (&Plugin{}).GetConfigSpec(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(spec.Fields) != 1 || spec.Fields[0].Key != "model" {
		t.Fatalf("config fields = %#v, want a single model field", spec.Fields)
	}
	if spec.Fields[0].Type != ports.ConfigFieldString {
		t.Fatalf("field type = %q, want string", spec.Fields[0].Type)
	}
}

func TestGetPromptDeliveryStrategy(t *testing.T) {
	strategy, err := (&Plugin{}).GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strategy != ports.PromptDeliveryInCommand {
		t.Fatalf("strategy = %q, want %q", strategy, ports.PromptDeliveryInCommand)
	}
}

func TestGetLaunchCommandRunsOneHeadlessTask(t *testing.T) {
	p := &Plugin{resolvedBinary: "dsh"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{Prompt: "add a health check"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dsh", "--profile", "headless", "add a health check"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

// TestGetLaunchCommandIgnoresSystemPromptFile documents that AO standing
// instructions have no Harness surface: passing the file as a task argument
// would run it as work.
func TestGetLaunchCommandIgnoresSystemPromptFile(t *testing.T) {
	p := &Plugin{resolvedBinary: "dsh"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: "/tmp/ao-prompt.md",
		Prompt:           "task",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dsh", "--profile", "headless", "task"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

// TestGetLaunchCommandRejectsStdinTaskLiteral covers the one task value Harness
// treats as a flag: "-" reads the task from stdin, which AO never supplies, so
// the launch must fail loudly instead of blocking.
func TestGetLaunchCommandRejectsStdinTaskLiteral(t *testing.T) {
	p := &Plugin{resolvedBinary: "dsh"}
	_, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{Prompt: "-"})
	if err == nil || !strings.Contains(err.Error(), "stdin") {
		t.Fatalf("err = %v, want a stdin refusal", err)
	}
}

// TestGetLaunchCommandOmitsTheAOSessionID pins the flag's meaning: --session-id
// adopts a session Harness has already persisted and is rejected for an id it
// has not seen, while cfg.SessionID is AO's own id. Passing it would fail every
// fresh spawn, because AO always supplies that id.
func TestGetLaunchCommandOmitsTheAOSessionID(t *testing.T) {
	p := &Plugin{resolvedBinary: "dsh"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SessionID:       "ao-session-1",
		NativeSessionID: "native-1",
		Prompt:          "task",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dsh", "--profile", "headless", "task"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

// TestGetLaunchCommandRejectsAPromptlessTask covers the reachable half of the
// stdin trap: AO's --prompt is optional, and Harness reads stdin when the
// headless profile gets no task at all, so such a session would hang instead of
// doing work.
func TestGetLaunchCommandRejectsAPromptlessTask(t *testing.T) {
	p := &Plugin{resolvedBinary: "dsh"}
	for _, prompt := range []string{"", "   \n"} {
		_, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{Prompt: prompt})
		if err == nil || !strings.Contains(err.Error(), "stdin") {
			t.Fatalf("prompt %q: err = %v, want a stdin refusal", prompt, err)
		}
	}
}

func TestGetRestoreCommandRequiresNativeSessionID(t *testing.T) {
	p := &Plugin{resolvedBinary: "dsh"}
	cfg := ports.RestoreConfig{Prompt: "keep going"}
	if _, ok, err := p.GetRestoreCommand(context.Background(), cfg); err != nil || ok {
		t.Fatalf("restore without a native id = (ok %v, err %v), want (false, nil)", ok, err)
	}

	cfg.Session.Metadata = map[string]string{ports.MetadataKeyAgentSessionID: " session-1 "}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), cfg)
	if err != nil || !ok {
		t.Fatalf("restore with a native id = (ok %v, err %v), want (true, nil)", ok, err)
	}
	want := []string{"dsh", "--profile", "headless", "--session-id", "session-1", "keep going"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected restore command\nwant: %#v\n got: %#v", want, cmd)
	}
}

// TestGetRestoreCommandRejectsAPromptlessTask holds the same stdin guarantee on
// the resume path: continuing a persisted session still runs one headless task,
// so an empty turn would block exactly as a promptless launch does.
func TestGetRestoreCommandRejectsAPromptlessTask(t *testing.T) {
	p := &Plugin{resolvedBinary: "dsh"}
	cfg := ports.RestoreConfig{}
	cfg.Session.Metadata = map[string]string{ports.MetadataKeyAgentSessionID: "session-1"}
	if _, _, err := p.GetRestoreCommand(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "stdin") {
		t.Fatalf("err = %v, want a stdin refusal", err)
	}
}

func TestAuthStatusReadsEnvironmentCredential(t *testing.T) {
	t.Setenv(deepseekCredentialKey, "sk-test")
	p := &Plugin{resolvedBinary: "dsh"}
	status, err := p.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want authorized", status)
	}
}

func TestAuthStatusWithoutEvidenceStaysUnknown(t *testing.T) {
	t.Setenv(deepseekCredentialKey, "")
	t.Setenv(credentialsPathEnv, filepath.Join(t.TempDir(), "missing.yaml"))
	p := &Plugin{resolvedBinary: "dsh"}
	status, err := p.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want unknown", status)
	}
}

// TestAuthStatusScopesEvidenceToTheDeepSeekRecord is the failure that matters:
// the store also holds credentials for other providers, and one of those must
// not read as DeepSeek Harness being ready.
func TestAuthStatusScopesEvidenceToTheDeepSeekRecord(t *testing.T) {
	t.Setenv(deepseekCredentialKey, "")
	cases := []struct {
		name    string
		store   string
		want    ports.AgentAuthStatus
		wantErr bool
	}{
		// The fixtures below are the layout a current DeepSeek Harness writes and
		// reads: `version: 1` with credentials under `refs:`, mapping each
		// addressable name straight to its secret. A document without `version`
		// is the pre-release flat layout, which Harness now refuses to load
		// ("uses the pre-release flat layout. Add `version: 1` …"), so it is
		// covered as a rejected document rather than as evidence.
		{
			name: "deepseek ref carries the key",
			store: `
version: 1
refs:
  DEEPSEEK_API_KEY: sk-live
`,
			want: ports.AgentAuthStatusAuthorized,
		},
		{
			name: "deepseek ref alongside another provider",
			store: `
version: 1
refs:
  OPENCODE_GO_API_KEY: sk-other
  DEEPSEEK_API_KEY: sk-live
`,
			want: ports.AgentAuthStatusAuthorized,
		},
		{
			name: "only another provider is populated",
			store: `
version: 1
refs:
  OPENCODE_GO_API_KEY: sk-other
`,
			want: ports.AgentAuthStatusUnknown,
		},
		{
			name: "deepseek ref present but empty",
			store: `
version: 1
refs:
  DEEPSEEK_API_KEY: ""
`,
			want: ports.AgentAuthStatusUnknown,
		},
		{
			// A scoped api-key record is a valid store that carries no
			// DEEPSEEK_API_KEY ref. AO cannot tell which route it serves, so it
			// stays unknown rather than claiming authorization it has not seen.
			name: "scoped record without a deepseek ref",
			store: `
version: 1
records:
  deepseek/official:
    kind: api-key
    key: sk-live
`,
			want: ports.AgentAuthStatusUnknown,
		},
		{
			// The pre-release flat layout. Harness refuses to load a document
			// with no `version: 1`, and the failure takes its whole credentials
			// service down, so reading this as authorization would badge the
			// harness ready for a session that cannot start.
			name: "pre-release flat layout is not evidence",
			store: `
DEEPSEEK_API_KEY: sk-live
`,
			want: ports.AgentAuthStatusUnknown,
		},
		{
			// A version this build does not read is refused by Harness the same
			// way, so it is not evidence either.
			name: "unsupported store version",
			store: `
version: 2
refs:
  DEEPSEEK_API_KEY: sk-live
`,
			want: ports.AgentAuthStatusUnknown,
		},
		{
			name:  "malformed store is reported",
			store: "records: [",
			// An unreadable store is surfaced rather than silently reported as
			// unauthenticated, matching how the other adapters treat a corrupt
			// credential file.
			want:    ports.AgentAuthStatusUnknown,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".credentials.yaml")
			if err := os.WriteFile(path, []byte(tc.store), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(credentialsPathEnv, path)
			p := &Plugin{resolvedBinary: "dsh"}
			status, err := p.AuthStatus(context.Background())
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if status != tc.want {
				t.Fatalf("status = %q, want %q", status, tc.want)
			}
		})
	}
}

func TestAuthStatusHonoursDSHHome(t *testing.T) {
	t.Setenv(deepseekCredentialKey, "")
	t.Setenv(credentialsPathEnv, "")
	home := t.TempDir()
	t.Setenv("DSH_HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".credentials.yaml"),
		[]byte("version: 1\nrefs:\n  DEEPSEEK_API_KEY: sk-live\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{resolvedBinary: "dsh"}
	status, err := p.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want authorized", status)
	}
}

func TestDeepSeekHarnessIsAKnownHarness(t *testing.T) {
	if !domain.HarnessDeepSeek.IsKnown() {
		t.Fatal("domain.HarnessDeepSeek is not in AllHarnesses")
	}
}
