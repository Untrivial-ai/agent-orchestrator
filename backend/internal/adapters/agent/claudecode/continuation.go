package claudecode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/nativeconfig"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const claudeConfigDirEnv = "CLAUDE_CONFIG_DIR"

var (
	_ ports.AgentContinuationCapabilityProvider = (*Plugin)(nil)
	_ ports.AgentFreshNativeSessionIDProvider   = (*Plugin)(nil)
	_ ports.AgentNativeSessionConfigProvider    = (*Plugin)(nil)
	_ ports.AgentNativeSessionProber            = (*Plugin)(nil)
	_ ports.AgentTranscriptLocator              = (*Plugin)(nil)
)

// ContinuationCapabilities reports the verified Claude Code continuation
// behavior. --append-system-prompt-file (with the inline form as a fallback)
// is accepted on both fresh launch and --resume, and --session-id lets AO
// assign a distinct UUID to each fresh provider conversation owned by one
// stable AO session.
func (p *Plugin) ContinuationCapabilities() ports.ContinuationCapabilities {
	return ports.ContinuationCapabilities{
		StandingInstructions: ports.StandingInstructionsCommand,
		FreshNativeSessionID: ports.FreshNativeSessionIDCallerAssigned,
	}
}

// NewNativeSessionID returns a provider-valid id for a distinct fresh Claude
// conversation owned by an existing AO session.
func (p *Plugin) NewNativeSessionID() string {
	return uuid.NewString()
}

// NativeSessionConfigDir returns Claude Code's session-state root. A project
// environment may explicitly select CLAUDE_CONFIG_DIR; otherwise Claude uses
// ~/.claude.
func (p *Plugin) NativeSessionConfigDir(ctx context.Context, env map[string]string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	dir, err := nativeconfig.Resolve(env, claudeConfigDirEnv, ".claude")
	if err != nil {
		return "", fmt.Errorf("claude-code: resolve config dir: %w", err)
	}
	return dir, nil
}

// ProbeNativeSession uses Claude's local JSONL transcript as authoritative
// backing state for --resume. Claude stores one <session-id>.jsonl below a
// workspace directory in <config>/projects.
func (p *Plugin) ProbeNativeSession(ctx context.Context, ref ports.NativeSessionRef) (ports.NativeSessionAvailability, error) {
	if strings.TrimSpace(ref.ConfigDir) == "" {
		return ports.NativeSessionAvailabilityUnknown, nil
	}
	_, ok, err := p.LocateTranscript(ctx, ref)
	if err != nil {
		return ports.NativeSessionAvailabilityUnknown, err
	}
	if ok {
		return ports.NativeSessionAvailabilityAvailable, nil
	}
	return ports.NativeSessionAvailabilityUnavailable, nil
}

// LocateTranscript finds Claude's provider-owned JSONL without depending on
// the implementation detail that encodes workspace paths into directory names.
// Native session UUIDs are globally unique, so enumerating the immediate
// project directories is deterministic and also survives encoding changes.
func (p *Plugin) LocateTranscript(ctx context.Context, ref ports.NativeSessionRef) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	sessionID, err := canonicalClaudeNativeSessionID(ref.NativeSessionID)
	if err != nil {
		return "", false, err
	}
	configDir := strings.TrimSpace(ref.ConfigDir)
	if configDir == "" {
		return "", false, nil
	}

	projectsDir := filepath.Join(configDir, "projects")
	projects, err := os.ReadDir(projectsDir)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("claude-code: read transcript projects: %w", err)
	}
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		if !project.IsDir() {
			continue
		}
		path := filepath.Join(projectsDir, project.Name(), sessionID+".jsonl")
		info, err := os.Stat(path)
		switch {
		case err == nil && info.Mode().IsRegular():
			return path, true, nil
		case errors.Is(err, os.ErrNotExist):
			continue
		case err != nil:
			return "", false, fmt.Errorf("claude-code: stat transcript: %w", err)
		}
	}
	return "", false, nil
}

func canonicalClaudeNativeSessionID(value string) (string, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("claude-code: invalid native session id: %w", err)
	}
	return parsed.String(), nil
}
