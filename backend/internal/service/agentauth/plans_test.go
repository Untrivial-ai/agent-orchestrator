package agentauth

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

func TestPlansMatchAuthenticationMatrix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		id, title, executable, guidance, docs, terminalInput string
		action                                               Action
		argv                                                 []string
	}{
		{"claude-code", "Log in to Claude Code", "claude", "Native browser/device flow", "https://code.claude.com/docs/en/installation", "", ActionLogin, []string{"claude", "auth", "login"}},
		{"codex", "Log in to Codex", "codex", "Native browser/device-code flow", "https://github.com/openai/codex", "", ActionLogin, []string{"codex", "login"}},
		{"cursor", "Log in to Cursor", "cursor-agent", "Native browser flow", "https://docs.cursor.com/en/cli/installation", "", ActionLogin, []string{"cursor-agent", "login"}},
		{"opencode", "Log in to OpenCode", "opencode", "Native provider chooser", "https://github.com/anomalyco/opencode", "", ActionLogin, []string{"opencode", "auth", "login"}},
		{"aider", "Set up Aider", "", "Configure provider credentials using Aider's documented environment or configuration-file options", "https://aider.chat/docs/config/api-keys.html", "", ActionSetup, nil},
		{"copilot", "Log in to GitHub Copilot", "copilot", "Native GitHub device/browser flow", "https://docs.github.com/en/copilot/how-tos/copilot-cli/set-up-copilot-cli/install-copilot-cli", "", ActionLogin, []string{"copilot", "login"}},
		{"grok", "Log in to Grok", "grok", "Native login; device-auth remains available inside the CLI", "https://docs.x.ai/build/overview", "", ActionLogin, []string{"grok", "login"}},
		{"kimi", "Log in to Kimi", "kimi", "Native browser flow", "https://moonshotai.github.io/kimi-code/en/", "", ActionLogin, []string{"kimi", "login"}},
		{"pi", "Log in to Pi", "pi", "Select Open login after Pi finishes starting", "https://github.com/earendil-works/pi", "/login\r", ActionLogin, []string{"pi"}},
		{"amp", "Log in to Amp", "amp", "Native browser flow", "https://ampcode.com/manual", "", ActionLogin, []string{"amp", "login"}},
		{"auggie", "Log in to Auggie", "auggie", "Native browser flow", "https://docs.augmentcode.com/cli/overview", "", ActionLogin, []string{"auggie", "login"}},
		{"droid", "Log in to Droid", "droid", "Select Open login after Droid finishes starting", "https://docs.factory.ai/droid-cli/cli-reference", "/login\r", ActionLogin, []string{"droid"}},
		{"crush", "Log in to Crush", "crush", "Native Charm Hyper login flow; GitHub Copilot remains available as a platform option", "https://github.com/charmbracelet/crush", "", ActionLogin, []string{"crush", "login"}},
		{"cline", "Log in to Cline", "cline", "Native authentication flow", "https://github.com/cline/cline", "", ActionLogin, []string{"cline", "auth"}},
		{"goose", "Set up Goose", "goose", "Native provider configuration; AO forwards terminal input without persisting or logging the raw input, while Goose controls credential storage", "https://block.github.io/goose/index.html", "", ActionSetup, []string{"goose", "configure"}},
		{"qwen", "Set up Qwen", "qwen", "Select Open setup after Qwen finishes starting to configure a model provider", "https://qwenlm.github.io/qwen-code-docs/en/users/configuration/auth/", "i\x7f/auth\r", ActionSetup, []string{"qwen"}},
		{"continue", "Log in to Continue", "cn", "Native browser flow", "https://docs.continue.dev/cli/quickstart", "", ActionLogin, []string{"cn", "login"}},
		{"devin", "Log in to Devin", "devin", "Native browser flow; manual-token flow remains available from the CLI", "https://docs.devin.ai/get-started/devin-intro", "", ActionLogin, []string{"devin", "auth", "login"}},
		{"kiro", "Log in to Kiro", "kiro-cli", "Native browser flow; device flow remains a CLI option", "https://kiro.dev/docs/getting-started/installation/", "", ActionLogin, []string{"kiro-cli", "login"}},
		{"kilocode", "Log in to Kilo Code", "kilo", "Native browser flow", "https://kilo.ai/docs/code-with-ai/platforms/cli", "", ActionLogin, []string{"kilo", "auth", "login"}},
		{"vibe", "Set up Vibe", "vibe", "Native provider setup; AO forwards terminal input without persisting or logging the raw input, while Vibe controls credential storage", "https://github.com/mistralai/mistral-vibe", "", ActionSetup, []string{"vibe", "--setup"}},
		{"muse", "Log in to Muse", "muse", "Native login flow", "https://ai.meta.com/llama/", "", ActionLogin, []string{"muse", "login"}},
		{"agy", "Log in to Agy", "agy", "Native first-run browser sign-in", "https://github.com/google-antigravity/antigravity-cli", "", ActionLogin, []string{"agy"}},
		{"autohand", "Log in to Autohand", "autohand", "Native Autohand account sign-in", "https://docs.autohand.ai/working-with-autohand-code/cli-reference", "", ActionLogin, []string{"autohand", "login"}},
		{"kimchi", "Log in to Kimchi", "kimchi", "Native browser login flow", "https://docs.kimchi.dev/docs/service-keys", "", ActionLogin, []string{"kimchi", "login"}},
		{"prime-agent", "Log in to Prime Agent", "prime-agent", "Select Open login after Prime Agent finishes starting", "https://github.com/PrimeIntellect-ai/prime-agent/blob/main/packages/coding-agent/docs/quickstart.md", "/login\r", ActionLogin, []string{"prime-agent"}},
		{"omp", "Log in to OMP", "omp", "Select Open login after OMP finishes starting", "https://github.com/can1357/oh-my-pi", "/login\r", ActionLogin, []string{"omp"}},
	}

	svc := New(foundExecutables(cases), nil)
	plans := svc.Plans(context.Background())
	if len(plans) != len(cases) {
		t.Fatalf("Plans() returned %d plans, want %d", len(plans), len(cases))
	}
	seen := make(map[string]bool, len(plans))
	for i, want := range cases {
		got := plans[i]
		wantLaunchMode := LaunchTerminal
		if want.id == "aider" {
			wantLaunchMode = LaunchDocumentation
		}
		if seen[got.AgentID] {
			t.Fatalf("Plans() returned duplicate id %q", got.AgentID)
		}
		seen[got.AgentID] = true
		if got.AgentID != want.id || got.Action != want.action || got.LaunchMode != wantLaunchMode || !got.Available || got.Guidance != want.guidance || got.DocumentationURL != want.docs {
			t.Fatalf("plan %d = %#v, want id=%q action=%q available=true guidance=%q docs=%q", i, got, want.id, want.action, want.guidance, want.docs)
		}
		wantCommand := append([]string(nil), want.argv...)
		if len(wantCommand) > 0 {
			wantCommand[0] = "/test/bin/" + want.executable
		}
		if got.title != want.title || got.DisplayCommand != strings.Join(want.argv, " ") || !reflect.DeepEqual(got.command, wantCommand) || got.terminalInput != want.terminalInput {
			t.Fatalf("plan %q terminal = title %q display %q argv %#v, want title %q display %q argv %#v", want.id, got.title, got.DisplayCommand, got.command, want.title, strings.Join(want.argv, " "), wantCommand)
		}
		data, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "command") || strings.Contains(string(data), "terminalInput") || strings.Contains(string(data), "initialInput") {
			t.Fatalf("plan %q serialized trusted command data: %s", want.id, data)
		}
	}
}

func TestUnknownPlanReturnsStableTargetError(t *testing.T) {
	t.Parallel()

	_, err := New(foundExecutables(nil), nil).Plan(context.Background(), "not-a-harness")
	var targetErr *apierr.Error
	if !errors.As(err, &targetErr) || targetErr.Kind != apierr.KindInvalid || targetErr.Code != "AGENT_AUTH_TARGET_UNKNOWN" {
		t.Fatalf("Plan() error = %v, want AGENT_AUTH_TARGET_UNKNOWN", err)
	}
}

func TestPlanMissingExecutableIsUnavailable(t *testing.T) {
	t.Parallel()

	plan, err := New(foundExecutables(nil), nil).Plan(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Available || plan.Reason == "" {
		t.Fatalf("Plan(codex) = %#v, want unavailable plan with useful reason", plan)
	}
}

func foundExecutables(cases []struct {
	id, title, executable, guidance, docs, terminalInput string
	action                                               Action
	argv                                                 []string
}) ExecutableFinder {
	found := map[string]string{}
	for _, tc := range cases {
		if tc.executable != "" {
			found[tc.executable] = "/test/bin/" + tc.executable
		}
	}
	return executableFinderFunc(func(name string) (string, error) {
		path, ok := found[name]
		if !ok {
			return "", errors.New("not found")
		}
		return path, nil
	})
}
