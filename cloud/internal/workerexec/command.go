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
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

var ErrUnsupportedPolicy = errors.New("coding-agent policy cannot be enforced safely")

const orchestratorSystemPrompt = `You are an AO orchestrator running in an isolated Cloud worker. Delegate independent work through the control plane with:
- ao spawn --name NAME --agent HARNESS --prompt TEXT
- ao list
- ao send SESSION_ID MESSAGE
- ao kill SESSION_ID
Workers run in separate sandboxes. Never try to contact a child sandbox directly; use only these ao commands.`

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
	systemPrompt := ""
	if launch.Kind == "orchestrator" {
		systemPrompt = orchestratorSystemPrompt
	}
	var providerArgs []string
	switch launch.Harness {
	case "codex":
		providerArgs = codexActivityHookArgs()
	case "cursor":
		providerArgs = []string{"--trust"}
	}
	harness := agentruntime.Harness(launch.Harness)
	permission := agentruntime.PermissionPolicyForMode(
		agentruntime.SessionMode(launch.Mode),
	)
	var argv []string
	var err error
	if identity := b.interactiveRestoreIdentity(launch); identity != "" {
		var ok bool
		argv, ok, err = agentruntime.BuildRestoreCommand(agentruntime.RestoreConfig{
			Harness:       harness,
			Binary:        binary,
			SessionID:     launch.SessionID,
			Metadata:      map[string]string{agentruntime.MetadataKeyAgentSessionID: identity},
			WorkspacePath: workspace,
			SystemPrompt:  systemPrompt,
			ProviderArgs:  providerArgs,
			Permission:    permission,
		})
		if err == nil && !ok {
			err = errors.New("coding-agent conversation cannot be restored")
		}
	} else {
		argv, err = agentruntime.BuildLaunchCommand(agentruntime.LaunchConfig{
			Harness:       harness,
			Binary:        binary,
			SessionID:     launch.SessionID,
			WorkspacePath: workspace,
			Prompt:        launch.Prompt,
			SystemPrompt:  systemPrompt,
			ProviderArgs:  providerArgs,
			Permission:    permission,
		})
	}
	if err != nil {
		return Command{}, err
	}
	command := Command{
		Path: argv[0],
		Args: argv[1:],
		Dir:  workspace,
		Env:  map[string]string{},
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
		if err := installCursorActivityHooks(workspace); err != nil {
			if command.Cleanup != nil {
				command.Cleanup()
			}
			return Command{}, err
		}
	}
	return command, nil
}

func (b HarnessBuilder) interactiveRestoreIdentity(
	launch worker.LaunchContext,
) string {
	if launch.Harness != "claude-code" {
		return strings.TrimSpace(launch.AgentSessionID)
	}
	if identity := strings.TrimSpace(launch.AgentSessionID); b.claudeConversationAvailable(identity) {
		return identity
	}
	identity := agentruntime.ClaudeSessionID(launch.SessionID)
	if b.claudeConversationAvailable(identity) {
		return identity
	}
	return ""
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
		case "api_key", "access_token":
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
	if err := updateJSONFile(
		filepath.Join(workspace, ".claude", "settings.local.json"),
		installClaudeActivityHooks,
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
	args := []string{"--print", "--output-format", "stream-json"}
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
	return append(args, turn.Prompt), nil
}

func codexArgs(turn worker.Turn) ([]string, error) {
	if len(turn.DeniedCommands) > 0 {
		return nil, fmt.Errorf("%w: Codex has no exact denied-command primitive", ErrUnsupportedPolicy)
	}
	args := []string{"exec", "--json", "--skip-git-repo-check"}
	switch turn.Mode {
	case "read-only":
		args = append(args, "--sandbox", "read-only")
	case "standard":
		args = append(args, "--sandbox", "workspace-write")
	case "trusted":
		args = append(args, "--sandbox", "danger-full-access")
	}
	if turn.AgentSessionID != "" {
		args = append(args, "resume", turn.AgentSessionID)
	}
	return append(args, turn.Prompt), nil
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
	return append(args, turn.Prompt), nil
}

func (b HarnessBuilder) configureCodexCredential(
	command *Command,
	credential worker.CredentialResponse,
) error {
	parent := strings.TrimSpace(b.DataDir)
	if parent == "" {
		return errors.New("worker data directory is required for Codex configuration")
	}
	home := filepath.Join(parent, "codex")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("create Codex home: %w", err)
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
	command.Env = append(os.Environ(), "CODEX_HOME="+home)
	command.Stdin = strings.NewReader(secret)
	if err := command.Run(); err != nil {
		return err
	}
	return nil
}
