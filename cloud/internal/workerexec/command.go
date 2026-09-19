package workerexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	Binaries map[string]string
	DataDir  string
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
harness := agentruntime.Harness(launch.Harness)
	permission := agentruntime.PermissionPolicyForMode(
		agentruntime.SessionMode(launch.Mode),
	)
	var argv []string
	var err error
	if identity := strings.TrimSpace(launch.AgentSessionID); identity != "" {
		var ok bool
		argv, ok, err = agentruntime.BuildRestoreCommand(agentruntime.RestoreConfig{
			Harness:      harness,
			Binary:       binary,
			SessionID:    launch.SessionID,
			Metadata:     map[string]string{agentruntime.MetadataKeyAgentSessionID: identity},
			SystemPrompt: systemPrompt,
			Permission:   permission,
		})
		if err == nil && !ok {
			err = errors.New("coding-agent conversation cannot be restored")
		}
	} else {
		argv, err = agentruntime.BuildLaunchCommand(agentruntime.LaunchConfig{
			Harness:      harness,
			Binary:       binary,
			SessionID:    launch.SessionID,
			Prompt:       launch.Prompt,
			SystemPrompt: systemPrompt,
			Permission:   permission,
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
	if err := b.prepareOpenCodeEnvironment(
		&command, launch.SessionID, systemPrompt,
	); err != nil {
		if command.Cleanup != nil {
			command.Cleanup()
		}
		return Command{}, err
	}
if err := installOpenCodeActivityHooks(workspace); err != nil {
		if command.Cleanup != nil {
			command.Cleanup()
		}
		return Command{}, err
	}
	return command, nil
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
	args, err := openCodeRunArgs(turn)
	if err != nil {
		return Command{}, err
	}
	command := Command{
		Path: b.binary(turn.Harness),
		Args: args,
		Dir:  workspace,
		Env:  map[string]string{},
	}
	if err := b.configureCredential(&command, turn.Harness, credential); err != nil {
		if command.Cleanup != nil {
			command.Cleanup()
		}
		return Command{}, err
	}
	// opencode loads workspace-local plugins in both run and interactive modes,
	// so the activity plugin reports events for headless turns too.
	if err := installOpenCodeActivityHooks(workspace); err != nil {
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
	case "opencode":
		if credential.CredentialType != "api_key" {
			return errors.New("unsupported opencode credential type")
		}
		command.Env["OPENCODE_API_KEY"] = credential.Secret
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
	case "opencode":
		return "opencode"
	default:
		return harness
	}
}

// prepareOpenCodeEnvironment writes an AO-owned opencode config when the
// session carries standing instructions. opencode has no system-prompt flag, so
// standing instructions travel as an AO-generated agent (see
// agentruntime.OpenCodeAgentName, selected with --agent by the launch builder)
// and OPENCODE_CONFIG points opencode at the generated config.
func (b HarnessBuilder) prepareOpenCodeEnvironment(
	command *Command,
	sessionID, systemPrompt string,
) error {
	if systemPrompt == "" {
		return nil
	}
	dataDir := strings.TrimSpace(b.DataDir)
	if dataDir == "" {
		return errors.New("worker data directory is required for opencode configuration")
	}
	agentName := agentruntime.OpenCodeAgentName(sessionID)
	configPath := filepath.Join(dataDir, "opencode-config", "opencode.json")
	if err := updateJSONFile(configPath, func(root map[string]any) {
		for key := range root {
			delete(root, key)
		}
		root["$schema"] = "https://opencode.ai/config.json"
		root["agent"] = map[string]any{
			agentName: map[string]any{
				"mode":   "primary",
				"prompt": systemPrompt,
			},
		}
	}); err != nil {
return fmt.Errorf("prepare opencode config: %w", err)
	}
	command.Env["OPENCODE_CONFIG"] = configPath
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

// openCodeRunArgs builds the argv for a headless `opencode run` turn:
//
//	opencode run [--dangerously-skip-permissions] [--session <id>] --format json <prompt>
//
// Read-only confinement is the worker sandbox's job (opencode has no
// filesystem containment flag); the permission flag is applied exactly as the
// desktop adapter applies it, so trusted turns auto-approve while standard and
// read-only turns keep opencode's own permission config. opencode resumes a
// conversation by its plugin-captured session id.
func openCodeRunArgs(turn worker.Turn) ([]string, error) {
	args := []string{"run"}
	args = append(args, agentruntime.OpenCodePermissionArgs(
		agentruntime.PermissionPolicyForMode(agentruntime.SessionMode(turn.Mode)),
	)...)
	if turn.AgentSessionID != "" {
		args = append(args, "--session", turn.AgentSessionID)
	}
	args = append(args, "--format", "json")
	return append(args, turn.Prompt), nil
}
