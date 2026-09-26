// Package agentbase supplies the defaults an agent adapter would otherwise
// hand-copy. Most adapters implement several ports.Agent methods identically:
// no config keys, prompt delivered in the launch command, and (for the simpler
// harnesses) no hooks, no resume, no session metadata. Embedding Base gives an
// adapter those defaults so it only writes the methods it actually customizes.
package agentbase

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ModelConfigSpec returns the common optional model config field used by
// adapters that forward a --model-style argument.
func ModelConfigSpec(ctx context.Context, description string) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{Fields: []ports.ConfigField{{
		Key: "model", Type: ports.ConfigFieldString, Description: description,
	}}}, nil
}

// AppendModelFlag appends a trimmed model override using the adapter-owned
// static flag name.
func AppendModelFlag(cmd *[]string, cfg ports.AgentConfig, flag string) {
	if model := strings.TrimSpace(cfg.Model); model != "" {
		*cmd = append(*cmd, flag, model)
	}
}

// ProviderHomeDir resolves an agent's data directory from the session
// environment, then the daemon environment, then the user's home directory.
func ProviderHomeDir(env map[string]string, envName, dotDir string) (string, error) {
	if dir := strings.TrimSpace(env[envName]); dir != "" {
		return dir, nil
	}
	if dir := strings.TrimSpace(os.Getenv(envName)); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, dotDir), nil
}

// TranscriptInProjects checks one relative transcript path beneath each
// provider project directory. A leading * on the filename matches provider
// prefixes such as timestamps; all other characters are literal. The lookup
// never walks recursively. Only non-empty regular files count as persisted.
func TranscriptInProjects(ctx context.Context, projectsDir, relativePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	projects, err := os.ReadDir(projectsDir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !project.IsDir() {
			continue
		}
		name := filepath.Base(relativePath)
		if !strings.HasPrefix(name, "*") {
			info, err := os.Stat(filepath.Join(projectsDir, project.Name(), relativePath))
			switch {
			case err == nil && info.Mode().IsRegular() && info.Size() > 0:
				return true, nil
			case err == nil, os.IsNotExist(err):
				continue
			default:
				return false, err
			}
		}
		candidateDir := filepath.Join(projectsDir, project.Name(), filepath.Dir(relativePath))
		entries, err := os.ReadDir(candidateDir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			matched := strings.HasSuffix(entry.Name(), strings.TrimPrefix(name, "*"))
			if !matched {
				continue
			}
			info, err := entry.Info()
			switch {
			case err == nil && info.Mode().IsRegular() && info.Size() > 0:
				return true, nil
			case err == nil:
				continue
			default:
				return false, err
			}
		}
	}
	return false, nil
}

// HookOrProviderConversationID selects the provider conversation id in Chat
// mode and the hook-captured native id in terminal mode.
func HookOrProviderConversationID(
	ctx context.Context,
	session ports.SessionRef,
	currentMode domain.SessionMode,
	providerConversationID string,
) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	id := strings.TrimSpace(session.Metadata[ports.MetadataKeyAgentSessionID])
	if currentMode == domain.SessionModeChat {
		id = strings.TrimSpace(providerConversationID)
	}
	return id, id != "", nil
}

// Base provides no-op defaults for the optional ports.Agent methods. Embed it in
// a Plugin struct (`agentbase.Base`) and override only what the harness needs.
// Every method honors ctx cancellation and otherwise does nothing, matching what
// the adapters previously wrote by hand.
type Base struct{}

// GetConfigSpec reports no agent-specific config keys.
func (Base) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	return ports.ConfigSpec{}, ctx.Err()
}

// GetPromptDeliveryStrategy reports that the agent receives its prompt in the
// launch command itself, which is true for every shipped adapter.
func (Base) GetPromptDeliveryStrategy(ctx context.Context, _ ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return ports.PromptDeliveryInCommand, nil
}

// GetAgentHooks is a no-op for harnesses without a native hook surface.
func (Base) GetAgentHooks(ctx context.Context, _ ports.WorkspaceHookConfig) error {
	return ctx.Err()
}

// GetRestoreCommand reports that no existing native session can be continued.
func (Base) GetRestoreCommand(ctx context.Context, _ ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return nil, false, nil
}

// SessionInfo reports no agent-owned session metadata.
func (Base) SessionInfo(ctx context.Context, _ ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	return ports.SessionInfo{}, false, nil
}

// StandardSessionInfo returns the normalized session metadata (native session
// id, title, summary) an adapter's hooks persisted under the shared
// ports.MetadataKey* keys. ok is false when none of the three is present. An
// adapter whose SessionInfo just reads those keys delegates here.
func StandardSessionInfo(session ports.SessionRef) (ports.SessionInfo, bool) {
	info := ports.SessionInfo{
		AgentSessionID: session.Metadata[ports.MetadataKeyAgentSessionID],
		Title:          session.Metadata[ports.MetadataKeyTitle],
		Summary:        session.Metadata[ports.MetadataKeySummary],
	}
	if info.AgentSessionID == "" && info.Title == "" && info.Summary == "" {
		return ports.SessionInfo{}, false
	}
	return info, true
}
