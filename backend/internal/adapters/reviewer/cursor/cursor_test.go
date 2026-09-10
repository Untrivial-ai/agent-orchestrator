package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type captureAgent struct {
	got ports.LaunchConfig
	err error
}

func (a *captureAgent) GetConfigSpec(context.Context) (ports.ConfigSpec, error) {
	return ports.ConfigSpec{}, nil
}
func (a *captureAgent) GetLaunchCommand(_ context.Context, cfg ports.LaunchConfig) ([]string, error) {
	a.got = cfg
	if a.err != nil {
		return nil, a.err
	}
	argv := []string{"cursor-agent"}
	if cfg.Permissions == ports.PermissionModeAuto {
		argv = append(argv, "--force")
	}
	return append(argv, "--", cfg.Prompt), nil
}
func (a *captureAgent) GetPromptDeliveryStrategy(context.Context, ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	return ports.PromptDeliveryInCommand, nil
}
func (a *captureAgent) GetAgentHooks(context.Context, ports.WorkspaceHookConfig) error { return nil }
func (a *captureAgent) InstallWorkspaceTrust(context.Context, ports.WorkspaceHookConfig) error {
	return nil
}
func (a *captureAgent) GetRestoreCommand(context.Context, ports.RestoreConfig) ([]string, bool, error) {
	return nil, false, nil
}
func (a *captureAgent) SessionInfo(context.Context, ports.SessionRef) (ports.SessionInfo, bool, error) {
	return ports.SessionInfo{}, false, nil
}

func TestReviewCommandBuildsPersistentInteractiveInvocation(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}
	dataDir := t.TempDir()

	got, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:       "review-w1",
		WorkspacePath:    "/ws/w1",
		DataDir:          dataDir,
		Prompt:           "complete the AO review task in `/ao/prompts/reviewer/requests/batch-1/run-1/task.md`.",
		SystemPrompt:     "secret system content must not enter argv",
		Config:           ports.AgentConfig{Model: "gpt-5"},
		SystemPromptFile: "/ao/prompts/reviewer/system.md",
		TaskPromptFile:   "/ao/prompts/reviewer/requests/batch-1/run-1/task.md",
		TaskPromptRoot:   "/ao/prompts/reviewer",
	})
	if err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}

	wantPrompt := "Read and follow the AO reviewer role in `/ao/prompts/reviewer/system.md`, then complete the AO review task in `/ao/prompts/reviewer/requests/batch-1/run-1/task.md`."
	want := []string{
		"cursor-agent", "--trust",
		"--add-dir", "/ao/prompts/reviewer",
		"--", wantPrompt,
	}
	if !reflect.DeepEqual(got.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", got.Argv, want)
	}
	if agent.got.Permissions != ports.PermissionModeDefault {
		t.Fatalf("permissions = %q, want default", agent.got.Permissions)
	}
	if agent.got.Config.Model != "gpt-5" {
		t.Fatalf("launch config model = %q, want gpt-5", agent.got.Config.Model)
	}
	if agent.got.WorkspacePath != "/ws/w1" || agent.got.SessionID != "review-w1" {
		t.Fatalf("launch config = %+v", agent.got)
	}
	for _, forbidden := range []string{
		"--print", "-p", "--output-format",
		"--force", "--yolo",
		"--mode", "--mode=ask", "--mode=plan", "--plan",
		"--sandbox",
	} {
		if slicesContain(got.Argv, forbidden) {
			t.Fatalf("argv contains non-interactive/plan flag %q: %#v", forbidden, got.Argv)
		}
	}
	if strings.Contains(strings.Join(got.Argv, " "), "secret system content") {
		t.Fatalf("argv exposes system prompt content: %#v", got.Argv)
	}
	profileDir := filepath.Join(dataDir, "cursor-reviewers", "c442045692db6092")
	if !reflect.DeepEqual(got.Env, map[string]string{cursorDataDirEnv: profileDir}) {
		t.Fatalf("env = %#v, want AO-owned profile %q", got.Env, profileDir)
	}
}

func TestReviewCommandOmitsAddDirWhenPromptRootIsEmpty(t *testing.T) {
	agent := &captureAgent{}
	got, err := (&Reviewer{agent: agent}).ReviewCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:       "review-w1",
		DataDir:          t.TempDir(),
		Prompt:           "complete the task in `/ao/task.md`.",
		SystemPromptFile: filepath.Join("ao", "system.md"),
	})
	if err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}
	if slicesContain(got.Argv, "--add-dir") {
		t.Fatalf("argv contains conditional --add-dir without a root: %#v", got.Argv)
	}
}

func TestReviewCommandNeverFallsBackToInlineSystemPrompt(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	if _, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		SystemPrompt: "review only",
		Prompt:       "review it",
	}); err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}
	if agent.got.Prompt != "review it" {
		t.Fatalf("prompt = %q", agent.got.Prompt)
	}
}

func TestReviewCommandPropagatesAgentFailure(t *testing.T) {
	r := &Reviewer{agent: &captureAgent{err: errors.New("cursor: binary unavailable")}}

	_, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{Prompt: "review it"})
	if err == nil || err.Error() != "cursor: binary unavailable" {
		t.Fatalf("err = %v, want binary-unavailable error", err)
	}
}

func TestReviewMessageReturnsTaskPrompt(t *testing.T) {
	got, err := (&Reviewer{}).ReviewMessage(context.Background(), ports.ReviewInvocation{Prompt: "next review"})
	if err != nil {
		t.Fatalf("ReviewMessage: %v", err)
	}
	if got != "next review" {
		t.Fatalf("message = %q", got)
	}
}

func TestReviewCancelUsesTwoInterrupts(t *testing.T) {
	got, err := (&Reviewer{}).ReviewCancel(context.Background())
	if err != nil {
		t.Fatalf("ReviewCancel: %v", err)
	}
	if got.Mode != ports.ReviewCancelInterrupt || got.Interrupts != 2 {
		t.Fatalf("cancel = %+v, want two interrupts", got)
	}
}

func TestPreLaunchWritesIsolatedReviewerConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userConfigPath := filepath.Join(home, ".cursor", cursorConfigFileName)
	if err := os.MkdirAll(filepath.Dir(userConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	userConfig := []byte(`{"version":1,"model":{"modelId":"kept"},"permissions":{"allow":["Shell(user-owned)"],"deny":[]}}`)
	if err := os.WriteFile(userConfigPath, userConfig, 0o600); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	workspace := t.TempDir()
	inv := ports.ReviewInvocation{
		ReviewerID:     "review-w1",
		DataDir:        dataDir,
		WorkspacePath:  workspace,
		TaskPromptRoot: filepath.Join(dataDir, "prompts", "w1", "reviewer"),
	}
	if err := New().PreLaunch(context.Background(), inv); err != nil {
		t.Fatalf("PreLaunch: %v", err)
	}

	if got, err := os.ReadFile(userConfigPath); err != nil || !reflect.DeepEqual(got, userConfig) {
		t.Fatalf("user config changed: got %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".cursor", "cli.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkout config exists or stat failed: %v", err)
	}
	profileDir := filepath.Join(dataDir, "cursor-reviewers", "c442045692db6092")
	if got := reviewerProfileDir(inv); got != profileDir {
		t.Fatalf("profile dir = %q, want %q", got, profileDir)
	}
	profileInfo, err := os.Stat(profileDir)
	if err != nil {
		t.Fatal(err)
	}
	if profileInfo.Mode().Perm() != 0o700 {
		t.Fatalf("profile mode = %o, want 700", profileInfo.Mode().Perm())
	}
	configPath := filepath.Join(profileDir, cursorConfigFileName)
	configInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if configInfo.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o, want 600", configInfo.Mode().Perm())
	}
	trustPaths, err := filepath.Glob(filepath.Join(profileDir, "projects", "*", ".workspace-trusted"))
	if err != nil {
		t.Fatal(err)
	}
	if len(trustPaths) != 1 {
		t.Fatalf("reviewer trust markers = %#v, want exactly one", trustPaths)
	}
	trustData, err := os.ReadFile(trustPaths[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(trustData), workspace) || !strings.Contains(string(trustData), `"aoManaged": true`) {
		t.Fatalf("reviewer trust marker = %s", trustData)
	}
	config := readReviewerConfig(t, configPath)
	if config.Version != 1 {
		t.Fatalf("version = %d, want 1", config.Version)
	}
	wantAllow := append(append([]string(nil), reviewerAllowedPermissions...),
		"Read("+filepath.ToSlash(filepath.Join(inv.TaskPromptRoot, "**"))+")")
	if !reflect.DeepEqual(config.Permissions.Allow, wantAllow) {
		t.Fatalf("allow = %#v, want %#v", config.Permissions.Allow, wantAllow)
	}
	if !reflect.DeepEqual(config.Permissions.Deny, reviewerDeniedPermissions) {
		t.Fatalf("deny = %#v, want %#v", config.Permissions.Deny, reviewerDeniedPermissions)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"sandbox", "approvalMode", "user-owned", "model"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("config contains forbidden seeded/extra field %q: %s", forbidden, data)
		}
	}
}

func TestPreLaunchSeedsAuthInfoIntoIsolatedReviewerConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userConfigPath := filepath.Join(home, ".cursor", cursorConfigFileName)
	if err := os.MkdirAll(filepath.Dir(userConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userConfigPath, []byte(`{"version":1,"authInfo":{"email":"user@example.com","userId":"user-1","authId":"auth-1"},"model":{"modelId":"host-model"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	inv := ports.ReviewInvocation{
		ReviewerID:     "review-w1",
		DataDir:        t.TempDir(),
		WorkspacePath:  t.TempDir(),
		TaskPromptRoot: filepath.Join(t.TempDir(), "prompts"),
	}

	if err := New().PreLaunch(context.Background(), inv); err != nil {
		t.Fatalf("PreLaunch: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(reviewerProfileDir(inv), cursorConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		AuthInfo map[string]any `json:"authInfo"`
		Model    any            `json:"model"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.AuthInfo["email"] != "user@example.com" || config.AuthInfo["userId"] != "user-1" || config.AuthInfo["authId"] != "auth-1" {
		t.Fatalf("authInfo was not seeded: %s", data)
	}
	if config.Model != nil {
		t.Fatalf("reviewer config should not seed host model settings: %s", data)
	}
}

func TestPreLaunchWithoutPromptRootOmitsExternalRead(t *testing.T) {
	inv := ports.ReviewInvocation{ReviewerID: "review-w1", DataDir: t.TempDir(), WorkspacePath: t.TempDir()}
	if err := New().PreLaunch(context.Background(), inv); err != nil {
		t.Fatalf("PreLaunch: %v", err)
	}
	config := readReviewerConfig(t, filepath.Join(reviewerProfileDir(inv), cursorConfigFileName))
	if !reflect.DeepEqual(config.Permissions.Allow, reviewerAllowedPermissions) {
		t.Fatalf("allow = %#v, want %#v", config.Permissions.Allow, reviewerAllowedPermissions)
	}
}

func TestPreLaunchHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inv := ports.ReviewInvocation{ReviewerID: "review-w1", DataDir: t.TempDir()}
	if err := New().PreLaunch(ctx, inv); !errors.Is(err, context.Canceled) {
		t.Fatalf("PreLaunch err = %v, want context cancellation", err)
	}
	if _, err := os.Stat(reviewerProfileDir(inv)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("profile created after cancellation: %v", err)
	}
}

func readReviewerConfig(t *testing.T, path string) reviewerConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config reviewerConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func slicesContain(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
