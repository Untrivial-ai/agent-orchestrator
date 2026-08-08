package codex

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/nativeconfig"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const codexHomeEnv = "CODEX_HOME"

var (
	_ ports.AgentContinuationCapabilityProvider = (*Plugin)(nil)
	_ ports.AgentNativeSessionConfigProvider    = (*Plugin)(nil)
	_ ports.AgentNativeSessionProber            = (*Plugin)(nil)
	_ ports.AgentTranscriptLocator              = (*Plugin)(nil)
)

// ContinuationCapabilities reports that Codex accepts current developer
// instructions on fresh launch and `codex resume`. Codex itself assigns fresh
// conversation ids, which AO captures through SessionStart hooks.
func (p *Plugin) ContinuationCapabilities() ports.ContinuationCapabilities {
	return ports.ContinuationCapabilities{
		StandingInstructions: ports.StandingInstructionsCommand,
		FreshNativeSessionID: ports.FreshNativeSessionIDProviderAssigned,
	}
}

// NativeSessionConfigDir returns the exact CODEX_HOME used by the invocation,
// falling back to Codex's standard ~/.codex state root.
func (p *Plugin) NativeSessionConfigDir(ctx context.Context, env map[string]string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	dir, err := nativeconfig.Resolve(env, codexHomeEnv, ".codex")
	if err != nil {
		return "", fmt.Errorf("codex: resolve config dir: %w", err)
	}
	return dir, nil
}

// ProbeNativeSession checks only Codex's active sessions directory. An
// archived transcript remains useful handoff context but is not treated as a
// resumable active conversation.
func (p *Plugin) ProbeNativeSession(ctx context.Context, ref ports.NativeSessionRef) (ports.NativeSessionAvailability, error) {
	if strings.TrimSpace(ref.ConfigDir) == "" {
		return ports.NativeSessionAvailabilityUnknown, nil
	}
	sessionID, err := validateCodexNativeSessionID(ref.NativeSessionID)
	if err != nil {
		return ports.NativeSessionAvailabilityUnknown, err
	}
	_, ok, err := findCodexTranscript(ctx, filepath.Join(ref.ConfigDir, "sessions"), sessionID)
	if err != nil {
		return ports.NativeSessionAvailabilityUnknown, err
	}
	if ok {
		return ports.NativeSessionAvailabilityAvailable, nil
	}
	return ports.NativeSessionAvailabilityUnavailable, nil
}

// LocateTranscript finds Codex's rollout JSONL by native session id. It checks
// active sessions first, then archived sessions so a non-resumable prior
// conversation can still contribute a provenance-labeled handoff reference.
func (p *Plugin) LocateTranscript(ctx context.Context, ref ports.NativeSessionRef) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	sessionID, err := validateCodexNativeSessionID(ref.NativeSessionID)
	if err != nil {
		return "", false, err
	}
	configDir := strings.TrimSpace(ref.ConfigDir)
	if configDir == "" {
		return "", false, nil
	}
	for _, dir := range []string{"sessions", "archived_sessions"} {
		path, ok, err := findCodexTranscript(ctx, filepath.Join(configDir, dir), sessionID)
		if err != nil {
			return "", false, err
		}
		if ok {
			return path, true, nil
		}
	}
	return "", false, nil
}

func findCodexTranscript(ctx context.Context, root, sessionID string) (string, bool, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), "-"+sessionID+".jsonl") {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("codex: scan transcripts: %w", err)
	}
	return found, found != "", nil
}

func validateCodexNativeSessionID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || strings.ContainsAny(value, `/\\`+"\x00") {
		return "", fmt.Errorf("codex: invalid native session id")
	}
	return value, nil
}
