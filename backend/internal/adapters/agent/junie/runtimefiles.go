package junie

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
)

// RuntimeFileRequest describes one session's isolated Junie runtime files.
type RuntimeFileRequest struct{ DataDir, SessionID, SystemPrompt, SystemPromptFile string }

// RuntimeFiles contains the paths prepared for one Junie session.
type RuntimeFiles struct{ ConfigPath, GuidelinesPath string }

// RuntimeFileBuilder prepares isolated Junie configuration and guidelines.
type RuntimeFileBuilder interface {
	Prepare(context.Context, RuntimeFileRequest) (RuntimeFiles, error)
}
type runtimeFileBuilder struct{}

// NewRuntimeFileBuilder returns the production Junie runtime file builder.
func NewRuntimeFileBuilder() RuntimeFileBuilder { return runtimeFileBuilder{} }

type hookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}
type hookGroup struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookCommand `json:"hooks"`
}

func (runtimeFileBuilder) Prepare(ctx context.Context, req RuntimeFileRequest) (RuntimeFiles, error) {
	if err := ctx.Err(); err != nil {
		return RuntimeFiles{}, err
	}
	if strings.TrimSpace(req.DataDir) == "" || strings.TrimSpace(req.SessionID) == "" {
		return RuntimeFiles{}, errors.New("junie runtime files require data directory and session ID")
	}
	if req.SessionID == "." || req.SessionID == ".." || filepath.Base(req.SessionID) != req.SessionID || strings.ContainsAny(req.SessionID, `/\\`) {
		return RuntimeFiles{}, errors.New("invalid Junie session ID")
	}
	dir := filepath.Join(req.DataDir, "agent-runtime", "junie", req.SessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return RuntimeFiles{}, fmt.Errorf("create Junie runtime directory: %w", err)
	}
	// #nosec G302 -- directories require execute permission for private owner access.
	if err := os.Chmod(dir, 0o700); err != nil {
		return RuntimeFiles{}, err
	}
	text := req.SystemPrompt
	if text == "" && req.SystemPromptFile != "" {
		data, err := os.ReadFile(req.SystemPromptFile)
		if err != nil {
			return RuntimeFiles{}, err
		}
		text = string(data)
	}
	text = strings.TrimRight(text, "\n") + "\n"
	guidelines := filepath.Join(dir, "guidelines.md")
	if err := hookutil.AtomicWriteFile(guidelines, []byte(text), 0o600); err != nil {
		return RuntimeFiles{}, err
	}
	config := map[string]any{"hooks": junieHooks(false)} // PermissionRequest is gated off while hooks are EAP.
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return RuntimeFiles{}, err
	}
	data = append(data, '\n')
	configPath := filepath.Join(dir, "config.json")
	if err := hookutil.AtomicWriteFile(configPath, data, 0o600); err != nil {
		return RuntimeFiles{}, err
	}
	return RuntimeFiles{ConfigPath: configPath, GuidelinesPath: guidelines}, nil
}

func junieHooks(permissionSafe bool) map[string][]hookGroup {
	entry := func(event string) hookCommand {
		return hookCommand{Type: "command", Command: "ao hooks junie " + event, Timeout: 2}
	}
	h := map[string][]hookGroup{
		"SessionStart":     {{Matcher: "startup|resume|clear", Hooks: []hookCommand{entry("session-start")}}},
		"UserPromptSubmit": {{Hooks: []hookCommand{entry("user-prompt-submit")}}},
		"PreToolUse":       {{Matcher: ".*", Hooks: []hookCommand{entry("pre-tool-use")}}},
		"Stop":             {{Hooks: []hookCommand{entry("stop")}}},
		"StopFailure":      {{Matcher: ".*", Hooks: []hookCommand{entry("stop-failure")}}},
		"SessionEnd":       {{Matcher: "prompt_input_exit|logout|other", Hooks: []hookCommand{entry("session-end")}}},
	}
	if permissionSafe {
		h["PermissionRequest"] = []hookGroup{{Matcher: "Bash|Edit|Read|.*", Hooks: []hookCommand{entry("permission-request")}}}
	}
	return h
}
