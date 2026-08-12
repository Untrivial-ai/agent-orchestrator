// Package agentbase supplies the defaults an agent adapter would otherwise
// hand-copy. Most adapters implement several ports.Agent methods identically:
// no config keys, prompt delivered in the launch command, and (for the simpler
// harnesses) no hooks, no resume, no session metadata. Embedding Base gives an
// adapter those defaults so it only writes the methods it actually customizes.
package agentbase

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// MaxTranscriptLineBytes bounds a single JSONL line read from a native
// transcript. Agents can emit enormous single-line tool outputs or diffs; an
// oversized line is skipped rather than aborting the whole transcript read.
const MaxTranscriptLineBytes = 8 << 20 // 8 MiB

// ReadTranscriptLine reads the next non-empty line from r, draining (not
// buffering) lines longer than MaxTranscriptLineBytes so one oversized message
// can never fail the rest of the transcript. ok=false marks end of input.
func ReadTranscriptLine(r *bufio.Reader) (line string, ok bool, err error) {
	for {
		var buf []byte
		oversized := false
		for {
			frag, readErr := r.ReadSlice('\n')
			if !oversized && len(buf)+len(frag) > MaxTranscriptLineBytes {
				oversized = true
				buf = nil // oversized: drop what we have and keep draining the line
			}
			if !oversized {
				buf = append(buf, frag...)
			}
			if errors.Is(readErr, bufio.ErrBufferFull) {
				continue
			}
			if readErr == io.EOF && len(buf) == 0 && len(frag) == 0 && !oversized {
				return "", false, nil
			}
			if readErr != nil && readErr != io.EOF {
				return "", false, readErr
			}
			break
		}
		if oversized {
			continue // drop the line entirely; keep reading
		}
		line = strings.TrimSpace(string(buf))
		if line == "" {
			continue
		}
		return line, true, nil
	}
}

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
