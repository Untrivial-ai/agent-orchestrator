package agy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	// Current Antigravity workspace hooks live under .agents/hooks.json.
	agyHooksDirName  = ".agents"
	agyHooksFileName = "hooks.json"

	// AO owns only this top-level hook definition. Other user-defined hooks in
	// the same hooks.json file are preserved.
	agyManagedHookName = "agent-orchestrator"

	agyHookCommandPrefix = "ao hooks agy "
	agyHookTimeout       = 10
)

// Antigravity hooks.json is a map of named hook definitions:
//
//	{
//	  "my-hook": {
//	    "PreInvocation": [...],
//	    "PostToolUse": [...]
//	  }
//	}
//
// RawMessage at the top level lets AO preserve hook definitions it does not own.
type agyHookFile map[string]json.RawMessage

type agyHookDefinition struct {
	PreInvocation  []agyHookHandler      `json:"PreInvocation,omitempty"`
	PostInvocation []agyHookHandler      `json:"PostInvocation,omitempty"`
	PostToolUse    []agyToolMatcherGroup `json:"PostToolUse,omitempty"`
	Stop           []agyHookHandler      `json:"Stop,omitempty"`
}

type agyToolMatcherGroup struct {
	Matcher string           `json:"matcher,omitempty"`
	Hooks   []agyHookHandler `json:"hooks"`
}

type agyHookHandler struct {
	Type    string `json:"type,omitempty"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

func managedAgyHookDefinition() agyHookDefinition {
	return agyHookDefinition{
		PreInvocation: []agyHookHandler{
			{
				Type:    "command",
				Command: agyHookCommandPrefix + "before-agent",
				Timeout: agyHookTimeout,
			},
		},
		PostInvocation: []agyHookHandler{
			{
				Type:    "command",
				Command: agyHookCommandPrefix + "after-agent",
				Timeout: agyHookTimeout,
			},
		},
		PostToolUse: []agyToolMatcherGroup{
			{
				Matcher: "*",
				Hooks: []agyHookHandler{
					{
						Type:    "command",
						Command: agyHookCommandPrefix + "after-tool",
						Timeout: agyHookTimeout,
					},
				},
			},
		},
		// Antigravity Stop means the execution loop has stopped. It does not
		// mean the TUI process/session has exited, so report idle rather than
		// AO's session-end/exited event.
		Stop: []agyHookHandler{
			{
				Type:    "command",
				Command: agyHookCommandPrefix + "after-agent",
				Timeout: agyHookTimeout,
			},
		},
	}
}

// GetAgentHooks installs AO's activity hooks into the current Antigravity
// workspace hook file at .agents/hooks.json.
//
// AO owns only the "agent-orchestrator" top-level definition. Any other
// user-defined hook definitions are preserved.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) == "" {
		return errors.New("agy.GetAgentHooks: WorkspacePath is required")
	}

	hooksPath := agyHooksPath(cfg.WorkspacePath)

	hookFile, err := readAgyHooks(hooksPath)
	if err != nil {
		return fmt.Errorf("agy.GetAgentHooks: %w", err)
	}

	definitionJSON, err := json.Marshal(managedAgyHookDefinition())
	if err != nil {
		return fmt.Errorf("agy.GetAgentHooks: encode managed hooks: %w", err)
	}

	hookFile[agyManagedHookName] = definitionJSON

	if err := writeAgyHooks(hooksPath, hookFile); err != nil {
		return fmt.Errorf("agy.GetAgentHooks: %w", err)
	}

	if err := hookutil.EnsureWorkspaceGitignore(filepath.Dir(hooksPath), agyHooksFileName); err != nil {
		return fmt.Errorf("agy.GetAgentHooks: gitignore: %w", err)
	}

	return nil
}

// UninstallHooks removes only AO's managed Antigravity hook definition.
// User-defined hooks in the same .agents/hooks.json file are left untouched.
func (p *Plugin) UninstallHooks(ctx context.Context, workspacePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return errors.New("agy.UninstallHooks: workspacePath is required")
	}

	hooksPath := agyHooksPath(workspacePath)

	if _, err := os.Stat(hooksPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}

	hookFile, err := readAgyHooks(hooksPath)
	if err != nil {
		return fmt.Errorf("agy.UninstallHooks: %w", err)
	}

	delete(hookFile, agyManagedHookName)

	if err := writeAgyHooks(hooksPath, hookFile); err != nil {
		return fmt.Errorf("agy.UninstallHooks: %w", err)
	}

	return nil
}

// AreHooksInstalled reports whether AO's complete managed Antigravity activity
// hook definition is present in the workspace hook file.
func (p *Plugin) AreHooksInstalled(ctx context.Context, workspacePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return false, errors.New("agy.AreHooksInstalled: workspacePath is required")
	}

	hooksPath := agyHooksPath(workspacePath)

	if _, err := os.Stat(hooksPath); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	hookFile, err := readAgyHooks(hooksPath)
	if err != nil {
		return false, fmt.Errorf("agy.AreHooksInstalled: %w", err)
	}

	raw, ok := hookFile[agyManagedHookName]
	if !ok {
		return false, nil
	}

	var definition agyHookDefinition
	if err := json.Unmarshal(raw, &definition); err != nil {
		return false, fmt.Errorf(
			"agy.AreHooksInstalled: parse %q hook: %w",
			agyManagedHookName,
			err,
		)
	}

	return hasCompleteManagedAgyHooks(definition), nil
}

func agyHooksPath(workspacePath string) string {
	return filepath.Join(workspacePath, agyHooksDirName, agyHooksFileName)
}

func readAgyHooks(hooksPath string) (agyHookFile, error) {
	hookFile := agyHookFile{}

	data, err := os.ReadFile(hooksPath) //nolint:gosec // caller-owned workspace path
	if errors.Is(err, os.ErrNotExist) {
		return hookFile, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", hooksPath, err)
	}

	if strings.TrimSpace(string(data)) == "" {
		return hookFile, nil
	}

	if err := json.Unmarshal(data, &hookFile); err != nil {
		return nil, fmt.Errorf("parse %s: %w", hooksPath, err)
	}

	if hookFile == nil {
		hookFile = agyHookFile{}
	}

	return hookFile, nil
}

func writeAgyHooks(hooksPath string, hookFile agyHookFile) error {
	if hookFile == nil {
		hookFile = agyHookFile{}
	}

	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o750); err != nil {
		return fmt.Errorf("create hook dir: %w", err)
	}

	data, err := json.MarshalIndent(hookFile, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", hooksPath, err)
	}
	data = append(data, '\n')

	if err := hookutil.AtomicWriteFile(hooksPath, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", hooksPath, err)
	}

	return nil
}

func hasCompleteManagedAgyHooks(definition agyHookDefinition) bool {
	return hasAgyHookHandler(
		definition.PreInvocation,
		agyHookCommandPrefix+"before-agent",
	) &&
		hasAgyHookHandler(
			definition.PostInvocation,
			agyHookCommandPrefix+"after-agent",
		) &&
		hasAgyToolHook(
			definition.PostToolUse,
			agyHookCommandPrefix+"after-tool",
		) &&
		hasAgyHookHandler(
			definition.Stop,
			agyHookCommandPrefix+"after-agent",
		)
}

func hasAgyHookHandler(handlers []agyHookHandler, command string) bool {
	for _, handler := range handlers {
		if handler.Command == command {
			return true
		}
	}
	return false
}

func hasAgyToolHook(groups []agyToolMatcherGroup, command string) bool {
	for _, group := range groups {
		for _, handler := range group.Hooks {
			if handler.Command == command {
				return true
			}
		}
	}
	return false
}
