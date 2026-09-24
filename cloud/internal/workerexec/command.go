package workerexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/agentruntime"
	"github.com/aoagents/agent-orchestrator/cloud/internal/skillassets"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

var ErrUnsupportedPolicy = errors.New("coding-agent policy cannot be enforced safely")

type Command struct {
	Path    string
	Args    []string
	Dir     string
	Env     map[string]string
	Cleanup func()
}

type CommandBuilder interface {
	Build(context.Context, worker.Turn, worker.CredentialResponse, string) (Command, error)
}

// HarnessBuilder owns Cloud's headless streaming flags and fail-closed policy
// mapping; process lifecycle is shared with desktop AO through agentruntime.
type HarnessBuilder struct {
	Binaries   map[string]string
	DataDir    string
	CodexLogin func(binary, home, credentialType, secret string) error
}

// extraReposPromptNote describes the additional repositories the worker checked
// out beside the primary workspace, so the agent knows they exist and where to
// find them. Paths mirror worker.ExtraRepoPath (siblings of the primary
// checkout), so what the agent is told matches what is on disk.
func extraReposPromptNote(workspace string, repos []worker.RepoRef) string {
	if len(repos) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Additional repositories\n\n")
	b.WriteString("This session was set up with extra repositories checked out alongside your primary repository. Your working directory is the primary repository; the others are sibling directories you can read and edit directly:\n\n")
	wrote := false
	for _, repo := range repos {
		if strings.TrimSpace(repo.URL) == "" {
			continue
		}
		abs := worker.ExtraRepoPath(workspace, repo.URL)
		name := filepath.Base(abs)
		branch := ""
		if strings.TrimSpace(repo.Branch) != "" {
			branch = fmt.Sprintf(" (branch %s)", repo.Branch)
		}
		fmt.Fprintf(&b, "- %s%s: %s (relative to your working directory: ../%s)\n", name, branch, abs, name)
		wrote = true
	}
	if !wrote {
		return ""
	}
	b.WriteString("\nMake changes, commit, and open pull requests per repository as appropriate. If one is missing it could not be cloned (for example it is outside this session's GitHub access); continue with the primary repository.")
	return b.String()
}

// BuildInteractive prepares the provider's native TUI command. Unlike Build,
// it deliberately omits headless print/JSON flags so the browser terminal is
// the conversation surface.
func (b HarnessBuilder) BuildInteractive(
	launch worker.LaunchContext,
	credential worker.CredentialResponse,
	workspace string,
) (Command, error) {
	if credential.Provider != launch.Harness ||
		strings.TrimSpace(credential.Secret) == "" {
		return Command{}, errors.New("credential does not match the selected harness")
	}
	switch launch.Mode {
	case "standard", "trusted":
	case "read-only":
		return Command{}, fmt.Errorf(
			"%w: interactive read-only mode requires OS filesystem confinement",
			ErrUnsupportedPolicy,
		)
	default:
		return Command{}, fmt.Errorf(
			"%w: unknown session mode %q", ErrUnsupportedPolicy, launch.Mode,
		)
	}
	if len(launch.DeniedCommands) > 0 {
		return Command{}, fmt.Errorf(
			"%w: interactive terminals cannot enforce command-prefix deny rules",
			ErrUnsupportedPolicy,
		)
	}
	binary := b.binary(launch.Harness)
	skillDir := skillassets.Dir(b.DataDir)
	systemPrompt := workerSystemPrompt(skillDir, launch.ParentSessionID != "")
	if launch.Kind == "orchestrator" {
		systemPrompt = orchestratorSystemPrompt(skillDir)
	}
	if projectPrompt := strings.TrimSpace(launch.SystemPrompt); projectPrompt != "" {
		systemPrompt += "\n\n" + projectPrompt
	}
	// Multi-repo dev kit: tell a worker about the additional repositories checked
	// out beside its primary repo, and where to find them, so it can edit them
	// directly. This concrete sibling-path note is worker-only: an orchestrator
	// codes nothing itself, so it gets multi-repo awareness from the shared
	// project context (roleprompt) instead — enough to coordinate work across the
	// repos without being pointed at sibling directories to edit.
	if launch.Kind != "orchestrator" {
		if note := extraReposPromptNote(workspace, launch.ExtraRepos); note != "" {
			systemPrompt += "\n\n" + note
		}
	}
	systemPromptFile, err := b.writeSystemPromptFile(launch.SessionID, systemPrompt)
	if err != nil {
		return Command{}, err
	}
	var providerArgs []string
	switch launch.Harness {
	case "codex":
		providerArgs = codexActivityHookArgs(hookHelperPath(b.DataDir))
	case "cursor":
		pluginDir, err := b.writeCursorPromptPlugin(launch.SessionID, systemPrompt)
		if err != nil {
			return Command{}, err
		}
		providerArgs = []string{"--trust", "--plugin-dir", pluginDir}
	}
	harness := agentruntime.Harness(launch.Harness)
	permission := agentruntime.PermissionPolicyForMode(
		agentruntime.SessionMode(launch.Mode),
	)
	var argv []string
	if identity := b.interactiveRestoreIdentity(launch); identity != "" {
		var ok bool
		argv, ok, err = agentruntime.BuildRestoreCommand(agentruntime.RestoreConfig{
			Harness:          harness,
			Binary:           binary,
			SessionID:        launch.SessionID,
			Metadata:         map[string]string{agentruntime.MetadataKeyAgentSessionID: identity},
			Model:            launch.Model,
			WorkspacePath:    workspace,
			SystemPrompt:     systemPrompt,
			SystemPromptFile: systemPromptFile,
			ProviderArgs:     providerArgs,
			Permission:       permission,
		})
		if err == nil && !ok {
			err = errors.New("coding-agent conversation cannot be restored")
		}
	} else {
		argv, err = agentruntime.BuildLaunchCommand(agentruntime.LaunchConfig{
			Harness:          harness,
			Binary:           binary,
			SessionID:        launch.SessionID,
			WorkspacePath:    workspace,
			Prompt:           launch.Prompt,
			Model:            launch.Model,
			SystemPrompt:     systemPrompt,
			SystemPromptFile: systemPromptFile,
			ProviderArgs:     providerArgs,
			Permission:       permission,
		})
	}
	if err != nil {
		return Command{}, err
	}
	if launch.Harness == "codex" && strings.TrimSpace(launch.ReasoningEffort) != "" {
		argv = appendCodexEffort(argv, strings.TrimSpace(launch.ReasoningEffort))
	}
	command := Command{
		Path: argv[0],
		Args: argv[1:],
		Dir:  workspace,
		// This marker lets the hook bridge distinguish interactive TUI facts
		// from any provider hooks a headless Chat invocation may inherit from
		// the workspace. It also survives a TUI -> Chat handoff: the final stop
		// hook can arrive after the control plane commits the new interface.
		Env: map[string]string{"AO_CLOUD_SOURCE_INTERFACE": "tui"},
	}
	if err := b.configureCredential(&command, launch.Harness, credential); err != nil {
		if command.Cleanup != nil {
			command.Cleanup()
		}
		return Command{}, err
	}
	if launch.Harness == "claude-code" {
		if err := b.prepareClaudeCloudExperience(&command, workspace); err != nil {
			if command.Cleanup != nil {
				command.Cleanup()
			}
			return Command{}, err
		}
	}
	if launch.Harness == "cursor" {
		if err := installCursorActivityHooks(hookHelperPath(b.DataDir), workspace); err != nil {
			if command.Cleanup != nil {
				command.Cleanup()
			}
			return Command{}, err
		}
	}
	return command, nil
}

// appendCodexEffort keeps Cloud's optional provider setting local to the Cloud
// module; its Docker build intentionally consumes a pinned backend module.
func appendCodexEffort(argv []string, effort string) []string {
	for i, arg := range argv {
		if arg == "--" {
			return append(argv[:i], append([]string{"-c", "model_reasoning_effort=" + effort}, argv[i:]...)...)
		}
	}
	return append(argv, "-c", "model_reasoning_effort="+effort)
}

func (b HarnessBuilder) interactiveRestoreIdentity(
	launch worker.LaunchContext,
) string {
	if launch.Harness != "claude-code" {
		return strings.TrimSpace(launch.AgentSessionID)
	}
	// A newly provisioned worker has no native conversation yet. Do not infer
	// one from the deterministic Claude session id: a stale or partially
	// created JSONL file can make Claude start with --resume for a conversation
	// that does not exist, leaving the PTY open with no agent process/output.
	// The control plane's agentSessionID is populated only after a real native
	// conversation has been observed, so it is the only safe restore hint here.
	identity := strings.TrimSpace(launch.AgentSessionID)
	if identity == "" || !b.claudeConversationAvailable(identity) {
		return ""
	}
	return identity
}

func (b HarnessBuilder) claudeConfigDir() (string, error) {
	configDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if configDir != "" {
		return configDir, nil
	}
	dataDir := strings.TrimSpace(b.DataDir)
	if dataDir == "" {
		return "", errors.New("worker data directory is required for Claude Code configuration")
	}
	return filepath.Join(dataDir, "claude"), nil
}

func (b HarnessBuilder) claudeConversationAvailable(identity string) bool {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return false
	}
	configDir, err := b.claudeConfigDir()
	if err != nil {
		return false
	}
	matches, _ := filepath.Glob(
		filepath.Join(configDir, "projects", "*", identity+".jsonl"),
	)
	return len(matches) > 0
}

func (b HarnessBuilder) Build(
	_ context.Context,
	turn worker.Turn,
	credential worker.CredentialResponse,
	workspace string,
) (Command, error) {
	if credential.Provider != turn.Harness || strings.TrimSpace(credential.Secret) == "" {
		return Command{}, errors.New("credential does not match the selected harness")
	}
	if turn.Mode != "read-only" && turn.Mode != "standard" && turn.Mode != "trusted" {
		return Command{}, fmt.Errorf("%w: unknown session mode %q", ErrUnsupportedPolicy, turn.Mode)
	}
	command := Command{
		Path: b.binary(turn.Harness),
		Dir:  workspace,
		Env:  map[string]string{},
	}
	var err error
	switch turn.Harness {
	case "claude-code":
		configDir, configErr := b.claudeConfigDir()
		if configErr != nil {
			return Command{}, configErr
		}
		command.Env["CLAUDE_CONFIG_DIR"] = configDir
		if !b.claudeConversationAvailable(turn.AgentSessionID) {
			turn.AgentSessionID = ""
		}
		command.Args, err = claudeArgs(turn)
	case "codex":
		command.Args, err = codexArgs(turn)
	case "cursor":
		command.Args, err = cursorArgs(turn)
	default:
		err = fmt.Errorf("unsupported coding-agent harness %q", turn.Harness)
	}
	if err == nil {
		err = b.configureCredential(&command, turn.Harness, credential)
	}
	if err != nil {
		if command.Cleanup != nil {
			command.Cleanup()
		}
		return Command{}, err
	}
	return command, nil
}

func (b HarnessBuilder) configureCredential(
	command *Command,
	harness string,
	credential worker.CredentialResponse,
) error {
	switch harness {
	case "claude-code":
		switch credential.CredentialType {
		case "api_key":
			command.Env["ANTHROPIC_API_KEY"] = credential.Secret
		case "oauth_token":
			command.Env["CLAUDE_CODE_OAUTH_TOKEN"] = credential.Secret
		default:
			return errors.New("unsupported Claude Code credential type")
		}
	case "codex":
		switch credential.CredentialType {
		case "api_key", "access_token", "auth_json":
			return b.configureCodexCredential(command, credential)
		default:
			return errors.New("unsupported Codex credential type")
		}
	case "cursor":
		if credential.CredentialType != "api_key" {
			return errors.New("unsupported Cursor credential type")
		}
		command.Env["CURSOR_API_KEY"] = credential.Secret
	default:
		return fmt.Errorf("unsupported coding-agent harness %q", harness)
	}
	return nil
}

func (b HarnessBuilder) binary(harness string) string {
	if binary := strings.TrimSpace(b.Binaries[harness]); binary != "" {
		return binary
	}
	switch harness {
	case "claude-code":
		return "claude"
	case "codex":
		return "codex"
	case "cursor":
		return "cursor-agent"
	default:
		return harness
	}
}

func (b HarnessBuilder) prepareClaudeCloudExperience(command *Command, workspace string) error {
	configDir, err := b.claudeConfigDir()
	if err != nil {
		return err
	}
	command.Env["CLAUDE_CONFIG_DIR"] = configDir
	if err := updateJSONFile(filepath.Join(configDir, ".claude.json"), func(root map[string]any) {
		root["hasCompletedOnboarding"] = true
		root["theme"] = "dark"
		projects, _ := root["projects"].(map[string]any)
		if projects == nil {
			projects = map[string]any{}
			root["projects"] = projects
		}
		project, _ := projects[workspace].(map[string]any)
		if project == nil {
			project = map[string]any{}
			projects[workspace] = project
		}
		project["hasTrustDialogAccepted"] = true
	}); err != nil {
		return fmt.Errorf("prepare Claude onboarding: %w", err)
	}
	if err := updateJSONFile(filepath.Join(configDir, "settings.json"), func(settings map[string]any) {
		removeGlobalClaudeActivityHooks(settings)
		settings["theme"] = "dark"
		settings["skipDangerousModePermissionPrompt"] = true
		permissions, _ := settings["permissions"].(map[string]any)
		if permissions == nil {
			permissions = map[string]any{}
			settings["permissions"] = permissions
		}
		permissions["defaultMode"] = "bypassPermissions"
	}); err != nil {
		return fmt.Errorf("prepare Claude settings: %w", err)
	}
	helperBinary := hookHelperPath(b.DataDir)
	if err := updateJSONFile(
		filepath.Join(workspace, ".claude", "settings.local.json"),
		func(settings map[string]any) { installClaudeActivityHooks(helperBinary, settings) },
	); err != nil {
		return fmt.Errorf("install Claude activity hooks: %w", err)
	}
	return nil
}

func updateJSONFile(path string, update func(map[string]any)) error {
	root := map[string]any{}
	contents, err := os.ReadFile(path)
	switch {
	case err == nil && len(contents) > 0:
		if err := json.Unmarshal(contents, &root); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	case err == nil || errors.Is(err, os.ErrNotExist):
	default:
		return fmt.Errorf("read %s: %w", path, err)
	}
	update(root)
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	temporary, err := os.CreateTemp(dir, ".ao-cloud-config-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure temporary config: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func claudeArgs(turn worker.Turn) ([]string, error) {
	// Claude Code hard-errors on `--print --output-format stream-json` without
	// --verbose. Streamed assistant/result events are also the only machine
	// readable projection source for the Chat UI, so both flags are load-bearing.
	args := []string{"--print", "--output-format", "stream-json", "--verbose"}
	switch turn.Mode {
	case "read-only":
		args = append(args, "--permission-mode", "plan")
	case "standard":
		args = append(args, "--permission-mode", "acceptEdits")
	case "trusted":
		args = append(args, "--dangerously-skip-permissions")
	}
	if len(turn.DeniedCommands) > 0 {
		deny := make([]string, 0, len(turn.DeniedCommands))
		for _, pattern := range turn.DeniedCommands {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" {
				return nil, fmt.Errorf("%w: empty denied command", ErrUnsupportedPolicy)
			}
			deny = append(deny, "Bash("+pattern+")")
		}
		settings, err := json.Marshal(map[string]any{
			"permissions": map[string]any{"deny": deny},
		})
		if err != nil {
			return nil, err
		}
		args = append(args, "--settings", string(settings))
	}
	if turn.AgentSessionID != "" {
		args = append(args, "--resume", turn.AgentSessionID)
	}
	if strings.TrimSpace(turn.Model) != "" {
		args = append(args, "--model", strings.TrimSpace(turn.Model))
	}
	return append(args, turn.Prompt), nil
}

func codexArgs(turn worker.Turn) ([]string, error) {
	if len(turn.DeniedCommands) > 0 {
		return nil, fmt.Errorf("%w: Codex has no exact denied-command primitive", ErrUnsupportedPolicy)
	}
	// Match the interactive Codex policy for the same Cloud session. In
	// particular, a trusted TUI runs in YOLO mode; launching headless Chat with
	// only `--sandbox danger-full-access` can leave Codex waiting for an
	// approval that no terminal is available to answer.
	args := []string{
		"exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-hook-trust",
	}
	switch turn.Mode {
	case "read-only":
		args = append(args, "--sandbox", "read-only", "--ask-for-approval", "on-request")
	case "standard":
		args = append(args,
			"--sandbox", "workspace-write",
			"--ask-for-approval", "on-request",
			"-c", `approvals_reviewer="auto_review"`,
		)
	case "trusted":
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	if turn.AgentSessionID != "" {
		args = append(args, "resume", turn.AgentSessionID)
	}
	if strings.TrimSpace(turn.Model) != "" {
		args = append(args, "--model", strings.TrimSpace(turn.Model))
	}
	if strings.TrimSpace(turn.ReasoningEffort) != "" {
		args = append(args, "-c", "model_reasoning_effort="+strings.TrimSpace(turn.ReasoningEffort))
	}
	return append(args, "--", turn.Prompt), nil
}

func cursorArgs(turn worker.Turn) ([]string, error) {
	if len(turn.DeniedCommands) > 0 {
		return nil, fmt.Errorf("%w: Cursor has no exact denied-command primitive", ErrUnsupportedPolicy)
	}
	if turn.Mode == "read-only" {
		return nil, fmt.Errorf("%w: Cursor has no verified read-only mode", ErrUnsupportedPolicy)
	}
	args := []string{"agent", "--print", "--output-format", "stream-json"}
	if turn.Mode == "trusted" {
		args = append(args, "--force")
	}
	if turn.AgentSessionID != "" {
		args = append(args, "--resume", turn.AgentSessionID)
	}
	if strings.TrimSpace(turn.Model) != "" {
		args = append(args, "--model", strings.TrimSpace(turn.Model))
	}
	return append(args, turn.Prompt), nil
}

func (b HarnessBuilder) configureCodexCredential(
	command *Command,
	credential worker.CredentialResponse,
) error {
	home, err := b.codexHome()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("create Codex home: %w", err)
	}
	if credential.CredentialType == "auth_json" {
		path := filepath.Join(home, "auth.json")
		tmp, err := os.CreateTemp(home, ".ao-codex-auth-*")
		if err != nil {
			return fmt.Errorf("create temporary Codex authentication: %w", err)
		}
		tmpPath := tmp.Name()
		defer func() { _ = os.Remove(tmpPath) }()
		if err := tmp.Chmod(0o600); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("secure temporary Codex authentication: %w", err)
		}
		if _, err := tmp.Write([]byte(credential.Secret)); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("write temporary Codex authentication: %w", err)
		}
		if err := tmp.Close(); err != nil {
			return fmt.Errorf("close temporary Codex authentication: %w", err)
		}
		if err := os.Rename(tmpPath, path); err != nil {
			return fmt.Errorf("replace Codex authentication: %w", err)
		}
		command.Env["CODEX_HOME"] = home
		return nil
	}
	login := b.CodexLogin
	if login == nil {
		login = loginCodex
	}
	if err := login(command.Path, home, credential.CredentialType, credential.Secret); err != nil {
		return fmt.Errorf("configure Codex credential: %w", err)
	}
	command.Env["CODEX_HOME"] = home
	return nil
}

func (b HarnessBuilder) codexHome() (string, error) {
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return home, nil
	}
	parent := strings.TrimSpace(b.DataDir)
	if parent == "" {
		return "", errors.New("worker data directory is required for Codex configuration")
	}
	return filepath.Join(parent, "codex"), nil
}

func loginCodex(binary, home, credentialType, secret string) error {
	option := ""
	switch credentialType {
	case "api_key":
		option = "--with-api-key"
	case "access_token":
		option = "--with-access-token"
	default:
		return errors.New("unsupported Codex credential type")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "login", option)
	command.Env = []string{
		"CODEX_HOME=" + home,
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
	}
	command.Stdin = strings.NewReader(secret)
	if err := command.Run(); err != nil {
		return err
	}
	return nil
}
