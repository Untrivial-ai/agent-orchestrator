package workerexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
	Path         string
	Args         []string
	Dir          string
	Env          map[string]string
	SystemPrompt string
	Cleanup      func()
}

type CommandBuilder interface {
	Build(context.Context, worker.Turn, worker.CredentialResponse, string) (Command, error)
}

// HarnessBuilder owns Cloud's headless streaming flags and fail-closed policy
// mapping; process lifecycle is shared with desktop AO through agentruntime.
type HarnessBuilder struct {
	Binaries   map[string]string
	DataDir    string
	Launch     worker.LaunchContext
	Env        map[string]string
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
	if launch.Harness == "cursor" {
		if err := removeCursorACPStandingRule(workspace, launch.SessionID); err != nil {
			return Command{}, err
		}
	}
	binary := b.binary(launch.Harness)
	systemPrompt := b.systemPrompt(launch, workspace)
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
	identity := b.interactiveRestoreIdentity(launch)
	if identity != "" {
		var ok bool
		argv, ok, err = agentruntime.BuildRestoreCommand(agentruntime.RestoreConfig{
			Harness:          harness,
			Binary:           binary,
			SessionID:        launch.SessionID,
			Model:            launch.Model,
			Metadata:         map[string]string{agentruntime.MetadataKeyAgentSessionID: identity},
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
			Model:            launch.Model,
			WorkspacePath:    workspace,
			Prompt:           launch.Prompt,
			SystemPrompt:     systemPrompt,
			SystemPromptFile: systemPromptFile,
			ProviderArgs:     providerArgs,
			Permission:       permission,
		})
	}
	if err != nil {
		return Command{}, err
	}
	command := Command{
		Path: argv[0],
		Args: argv[1:],
		Dir:  workspace,
		Env:  map[string]string{"AO_CLOUD_SOURCE_INTERFACE": "tui"},
	}
	maps.Copy(command.Env, b.Env)
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
	if launch.Harness == "opencode" {
		// opencode v2 has no CLI flag for a system prompt, model, or agent; the argv
		// (built by agentruntime above) is just the approval flag plus the prompt.
		// The standing instructions, model override, and approval overlay ride an
		// AO-owned OPENCODE_CONFIG document that selects the AO agent via
		// `default_agent`. Write it beside the prompt file and export the env var.
		configPath, err := writeOpenCodeConfig(systemPromptFile, permission, launch.SessionID, launch.Model)
		if err != nil {
			if command.Cleanup != nil {
				command.Cleanup()
			}
			return Command{}, err
		}
		if configPath != "" {
			command.Env["OPENCODE_CONFIG"] = configPath
		}
		// opencode has no native command-hook config; its only lifecycle surface is
		// a workspace plugin. Install AO's activity plugin so opencode reports
		// session-start/prompt/active/stop/permission events through
		// `ao hooks opencode <event>`, the same bridge the other harnesses use.
		if err := installOpenCodeActivityPlugin(workspace); err != nil {
			if command.Cleanup != nil {
				command.Cleanup()
			}
			return Command{}, err
		}
		// Warm opencode's models.dev cache from the baked catalog so the TUI is not
		// blocked on a ~5MB startup download on a fresh sandbox.
		seedOpenCodeModelsCache(command.Env)
	}
	return command, nil
}

func (b HarnessBuilder) systemPrompt(launch worker.LaunchContext, workspace string) string {
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
	return systemPrompt
}

func (b HarnessBuilder) interactiveRestoreIdentity(
	launch worker.LaunchContext,
) string {
	if launch.Harness != "claude-code" {
		return strings.TrimSpace(launch.AgentSessionID)
	}
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
	if err := validateApprovalMode(turn); err != nil {
		return Command{}, err
	}
	if strings.TrimSpace(b.Launch.SessionID) == "" ||
		(b.Launch.Kind != "worker" && b.Launch.Kind != "orchestrator") {
		return Command{}, errors.New("cloud Chat requires session role context")
	}
	if b.Launch.Harness != turn.Harness {
		return Command{}, errors.New("turn harness does not match session role context")
	}
	command := Command{
		SystemPrompt: b.systemPrompt(b.Launch, workspace),
		Path:         b.binary(turn.Harness),
		Dir:          workspace,
		Env:          map[string]string{},
	}
	maps.Copy(command.Env, b.Env)
	var err error
	switch turn.Harness {
	case "claude-code":
		configDir, configErr := b.claudeConfigDir()
		if configErr != nil {
			return Command{}, configErr
		}
		command.Env["CLAUDE_CONFIG_DIR"] = configDir
		setClaudeNonEssentialTrafficDisabled(&command)
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
		// Native protocol runners consume SystemPrompt or the Cursor ancestor rule.
		// Keep headless CLI delivery intact for supervisors using OSRunner.
		if turn.Harness != "cursor" {
			var promptFile string
			promptFile, err = b.writeSystemPromptFile(b.Launch.SessionID, command.SystemPrompt)
			if err == nil {
				if turn.Harness == "claude-code" {
					command.Args = append([]string{"--append-system-prompt-file", promptFile}, command.Args...)
				} else {
					command.Args = append([]string{command.Args[0], "-c", "model_instructions_file=" + promptFile}, command.Args[1:]...)
				}
			}
		}
	}
	if err == nil {
		err = b.configureCredential(&command, turn.Harness, credential)
	}
	if err == nil && turn.Harness == "cursor" {
		err = writeCursorACPStandingRule(workspace, b.Launch.SessionID, command.SystemPrompt)
		if err == nil {
			credentialCleanup := command.Cleanup
			command.Cleanup = func() {
				_ = removeCursorACPStandingRule(workspace, b.Launch.SessionID)
				if credentialCleanup != nil {
					credentialCleanup()
				}
			}
		}
	}
	if err != nil {
		if command.Cleanup != nil {
			command.Cleanup()
		}
		return Command{}, err
	}
	return command, nil
}

// configureCredential injects a resolved credential into the launch command by
// dispatching to the harness's own business logic (see harness_credentials.go).
func (b HarnessBuilder) configureCredential(
	command *Command,
	harness string,
	credential worker.CredentialResponse,
) error {
	h, ok := harnessCredentialFor(harness)
	if !ok {
		return fmt.Errorf("unsupported coding-agent harness %q", harness)
	}
	return h.configure(b, command, credential)
}

func (b HarnessBuilder) binary(harness string) string {
	if binary := strings.TrimSpace(b.Binaries[harness]); binary != "" {
		return binary
	}
	if h, ok := harnessCredentialFor(harness); ok {
		return h.defaultBinary()
	}
	return harness
}

// setClaudeNonEssentialTrafficDisabled stops Claude Code from making its
// non-essential network calls (Statsig feature-flags, telemetry, error
// reporting, auto-update check) on launch. On a locked-down coder/Azure VM those
// hosts are blackholed, so each connect hangs ~30s before timing out — ~50s of
// dead time before the first frame on a fresh VM (codex makes no such calls,
// which is why only claude sessions felt slow). The essential model API
// (api.anthropic.com) is a separate host and is unaffected.
func setClaudeNonEssentialTrafficDisabled(command *Command) {
	if command.Env == nil {
		command.Env = map[string]string{}
	}
	command.Env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
}

func (b HarnessBuilder) prepareClaudeCloudExperience(command *Command, workspace string) error {
	configDir, err := b.claudeConfigDir()
	if err != nil {
		return err
	}
	command.Env["CLAUDE_CONFIG_DIR"] = configDir
	setClaudeNonEssentialTrafficDisabled(command)
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
	args := []string{"--print", "--output-format", "stream-json", "--verbose"}
	switch turn.Mode {
	case "read-only":
		args = append(args, "--permission-mode", "plan")
	case "standard":
		args = append(args, "--permission-mode", "acceptEdits")
	case "trusted":
		args = append(args, "--dangerously-skip-permissions")
	}
	if turn.Mode != "read-only" && turn.ApprovalMode != "" {
		permission := map[string]string{"default": "default", "accept-edits": "acceptEdits", "auto": "auto", "bypass-permissions": "bypassPermissions"}[turn.ApprovalMode]
		args = []string{"--print", "--output-format", "stream-json", "--verbose", "--permission-mode", permission}
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
	return append(args, turn.Prompt), nil
}

func codexArgs(turn worker.Turn) ([]string, error) {
	if len(turn.DeniedCommands) > 0 {
		return nil, fmt.Errorf("%w: Codex has no exact denied-command primitive", ErrUnsupportedPolicy)
	}
	args := []string{"exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-hook-trust"}
	if turn.ApprovalMode == "" {
		switch turn.Mode {
		case "read-only":
			args = append(args, "--sandbox", "read-only", "--ask-for-approval", "on-request")
		case "standard":
			args = append(args, "--sandbox", "workspace-write", "--ask-for-approval", "on-request", "-c", `approvals_reviewer="auto_review"`)
		case "trusted":
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		}
	} else {
		switch turn.ApprovalMode {
		case "default", "bypass-permissions":
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		case "accept-edits":
			args = append(args, "--sandbox", "workspace-write", "--ask-for-approval", "on-request")
		case "auto":
			args = append(args, "--sandbox", "workspace-write", "--ask-for-approval", "on-request", "-c", `approvals_reviewer="auto_review"`)
		}
	}
	if turn.Model != "" {
		args = append(args, "-m", turn.Model)
	}
	if turn.ReasoningEffort != "" {
		args = append(args, "-c", "model_reasoning_effort="+turn.ReasoningEffort)
	}
	if turn.AgentSessionID != "" {
		args = append(args, "resume", turn.AgentSessionID)
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
	if turn.ApprovalMode == "bypass-permissions" || (turn.ApprovalMode == "" && turn.Mode == "trusted") {
		args = append(args, "--force")
	} else if turn.ApprovalMode == "auto" {
		args = append(args, "--auto-review")
	}
	if turn.AgentSessionID != "" {
		args = append(args, "--resume", turn.AgentSessionID)
	}
	return append(args, turn.Prompt), nil
}

func validateApprovalMode(turn worker.Turn) error {
	switch turn.ApprovalMode {
	case "":
		return nil // older queued turns retain their launch-time policy
	case "default", "accept-edits", "auto", "bypass-permissions":
	default:
		return fmt.Errorf("%w: unknown approval mode %q", ErrUnsupportedPolicy, turn.ApprovalMode)
	}
	if turn.Mode == "read-only" {
		return fmt.Errorf("%w: approval policy cannot widen read-only mode", ErrUnsupportedPolicy)
	}
	if turn.Mode == "standard" && turn.ApprovalMode == "bypass-permissions" {
		return fmt.Errorf("%w: bypass exceeds session permission cap", ErrUnsupportedPolicy)
	}
	if turn.Mode == "standard" && turn.Harness == "codex" && turn.ApprovalMode == "default" {
		return fmt.Errorf("%w: Codex full access exceeds session permission cap", ErrUnsupportedPolicy)
	}
	return nil
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
