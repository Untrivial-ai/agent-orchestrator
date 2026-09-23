package copilot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type captureAgent struct {
	launchConfig  ports.LaunchConfig
	launchErr     error
	profileConfig ports.WorkspaceHookConfig
	profileErr    error
}

func (a *captureAgent) GetConfigSpec(context.Context) (ports.ConfigSpec, error) {
	return ports.ConfigSpec{}, nil
}

func (a *captureAgent) GetLaunchCommand(_ context.Context, cfg ports.LaunchConfig) ([]string, error) {
	a.launchConfig = cfg
	if a.launchErr != nil {
		return nil, a.launchErr
	}
	return []string{"copilot", "--agent=ao-review-w1", "--interactive", cfg.Prompt}, nil
}

func (a *captureAgent) GetPromptDeliveryStrategy(context.Context, ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	return ports.PromptDeliveryInCommand, nil
}

func (a *captureAgent) GetAgentHooks(context.Context, ports.WorkspaceHookConfig) error {
	return nil
}

func (a *captureAgent) InstallAgentProfile(_ context.Context, cfg ports.WorkspaceHookConfig) error {
	a.profileConfig = cfg
	return a.profileErr
}

func (a *captureAgent) GetRestoreCommand(context.Context, ports.RestoreConfig) ([]string, bool, error) {
	return nil, false, nil
}

func (a *captureAgent) SessionInfo(context.Context, ports.SessionRef) (ports.SessionInfo, bool, error) {
	return ports.SessionInfo{}, false, nil
}

func TestPreLaunchInstallsOnlyReviewerProfileInputs(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}
	inv := ports.ReviewInvocation{
		DataDir:          "/ao",
		ReviewerID:       "review-w1",
		WorkspacePath:    "/ws/w1",
		SystemPromptFile: "/ao/prompts/w1/reviewer/system.md",
	}

	if err := r.PreLaunch(context.Background(), inv); err != nil {
		t.Fatalf("PreLaunch: %v", err)
	}
	want := ports.WorkspaceHookConfig{
		DataDir:          inv.DataDir,
		SessionID:        inv.ReviewerID,
		WorkspacePath:    inv.WorkspacePath,
		SystemPromptFile: inv.SystemPromptFile,
	}
	if !reflect.DeepEqual(agent.profileConfig, want) {
		t.Fatalf("profile config = %#v, want %#v", agent.profileConfig, want)
	}
}

func TestPreLaunchPropagatesProfileError(t *testing.T) {
	want := errors.New("profile failed")
	agent := &captureAgent{profileErr: want}
	if err := (&Reviewer{agent: agent}).PreLaunch(context.Background(), ports.ReviewInvocation{}); !errors.Is(err, want) {
		t.Fatalf("PreLaunch error = %v, want %v", err, want)
	}
}

func TestReviewCommandUsesRestrictedPersistentPolicy(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}
	promptRoot := filepath.Join("/ao", "prompts", "w1", "reviewer")
	spec, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		DataDir:          "/ao",
		ReviewerID:       "review-w1",
		WorkspacePath:    "/ws/w1",
		Prompt:           "Read the hidden task.",
		Config:           ports.AgentConfig{Model: "gpt-5"},
		SystemPromptFile: filepath.Join(promptRoot, "system.md"),
		TaskPromptRoot:   promptRoot,
	})
	if err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}

	if agent.launchConfig.Permissions != ports.PermissionModeDefault {
		t.Fatalf("permissions = %q, want default", agent.launchConfig.Permissions)
	}
	if agent.launchConfig.Config.Model != "gpt-5" {
		t.Fatalf("launch config model = %q, want gpt-5", agent.launchConfig.Config.Model)
	}
	if agent.launchConfig.Prompt != "Read the hidden task." ||
		agent.launchConfig.SystemPrompt != "" ||
		agent.launchConfig.SystemPromptFile != filepath.Join(promptRoot, "system.md") {
		t.Fatalf("launch config exposes or loses hidden prompt: %#v", agent.launchConfig)
	}

	interactive := slices.Index(spec.Argv, "--interactive")
	if interactive < 0 || interactive != len(spec.Argv)-2 {
		t.Fatalf("interactive prompt placement = %#v", spec.Argv)
	}
	for i, arg := range spec.Argv {
		if i > interactive && arg != "Read the hidden task." {
			t.Fatalf("policy argument appears after --interactive: %#v", spec.Argv)
		}
	}

	policy := parsePolicy(t, spec.Argv[:interactive])
	if got := policy.single("--available-tools"); got != availableTools {
		t.Fatalf("available tools = %q, want %q", got, availableTools)
	}
	if got := policy.values["--add-dir"]; !slices.Equal(got, []string{promptRoot}) {
		t.Fatalf("prompt root access = %#v, want exactly %#v", got, []string{promptRoot})
	}
	if got := policy.values["--allow-tool"]; !slices.Equal(got, allowedTools) {
		t.Fatalf("allowed tools = %#v, want %#v", got, allowedTools)
	}
	if got := policy.values["--deny-tool"]; !slices.Equal(got, deniedTools) {
		t.Fatalf("denied tools = %#v, want %#v", got, deniedTools)
	}
	if got := policy.values["--allow-url"]; !slices.Equal(got, []string{"github.com", "api.github.com"}) {
		t.Fatalf("allowed urls = %#v", got)
	}
	for _, flag := range []string{"--disable-builtin-mcps", "--no-ask-user"} {
		if !policy.flags[flag] {
			t.Fatalf("missing %s in %#v", flag, spec.Argv)
		}
	}
	for _, broad := range []string{"--allow-all", "--allow-all-tools", "--allow-all-paths"} {
		if slices.Contains(spec.Argv, broad) {
			t.Fatalf("broad permission %q present in %#v", broad, spec.Argv)
		}
	}
}

func TestReviewPolicyAllowsReviewCommandsAndDeniesWritesAndGitMutations(t *testing.T) {
	for _, want := range []string{
		"read",
		"shell(git diff:*)",
		"shell(git log:*)",
		"shell(git show:*)",
		"shell(git status:*)",
		"shell(printf:*)",
		"shell(gh api:*)",
		"shell(ao review submit:*)",
	} {
		if !slices.Contains(allowedTools, want) {
			t.Errorf("allowed policy missing %q: %#v", want, allowedTools)
		}
	}
	for _, want := range []string{
		"write",
		"shell(git push:*)",
		"shell(git commit:*)",
		"shell(git checkout:*)",
		"shell(git reset:*)",
		"shell(git clean:*)",
		"shell(git apply:*)",
		"shell(git merge:*)",
		"shell(git rebase:*)",
		"shell(git cherry-pick:*)",
	} {
		if !slices.Contains(deniedTools, want) {
			t.Errorf("denied policy missing %q: %#v", want, deniedTools)
		}
	}
}

func TestReviewMessageAndCancellation(t *testing.T) {
	r := &Reviewer{}
	msg, err := r.ReviewMessage(context.Background(), ports.ReviewInvocation{Prompt: "next hidden task"})
	if err != nil || msg != "next hidden task" {
		t.Fatalf("ReviewMessage = (%q, %v)", msg, err)
	}
	cancel, err := r.ReviewCancel(context.Background())
	if err != nil {
		t.Fatalf("ReviewCancel: %v", err)
	}
	if cancel.Mode != ports.ReviewCancelInterrupt || cancel.Interrupts != 2 {
		t.Fatalf("cancel = %#v", cancel)
	}
}

func TestReviewCommandReportsMissingCopilotBinary(t *testing.T) {
	agent := &captureAgent{launchErr: ports.ErrAgentBinaryNotFound}
	_, err := (&Reviewer{agent: agent}).ReviewCommand(context.Background(), ports.ReviewInvocation{})
	if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatalf("ReviewCommand error = %v, want ErrAgentBinaryNotFound", err)
	}
}

type parsedPolicy struct {
	values map[string][]string
	flags  map[string]bool
}

func (p parsedPolicy) single(flag string) string {
	values := p.values[flag]
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func parsePolicy(t *testing.T, argv []string) parsedPolicy {
	t.Helper()
	withValue := map[string]bool{
		"--available-tools": true,
		"--allow-tool":      true,
		"--deny-tool":       true,
		"--add-dir":         true,
		"--allow-url":       true,
	}
	got := parsedPolicy{values: map[string][]string{}, flags: map[string]bool{}}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if withValue[arg] {
			if i+1 >= len(argv) {
				t.Fatalf("%s has no value in %#v", arg, argv)
			}
			i++
			got.values[arg] = append(got.values[arg], argv[i])
			continue
		}
		if strings.HasPrefix(arg, "--") && arg != "--agent=ao-review-w1" {
			got.flags[arg] = true
		}
	}
	return got
}

func TestNewReviewerRealCommandSelectsCustomAgent(t *testing.T) {
	binDir := t.TempDir()
	binaryName := "copilot"
	if runtime.GOOS == "windows" {
		binaryName = "copilot.cmd"
	}
	if err := os.WriteFile(filepath.Join(binDir, binaryName), []byte(""), 0o755); err != nil {
		t.Fatalf("write fake copilot: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	spec, err := New().ReviewCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:       "review-w1",
		Prompt:           "review",
		SystemPromptFile: "/ao/system.md",
	})
	if err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}
	if !slices.Contains(spec.Argv, "--agent=ao-review-w1") {
		t.Fatalf("custom agent missing from %#v", spec.Argv)
	}
}
