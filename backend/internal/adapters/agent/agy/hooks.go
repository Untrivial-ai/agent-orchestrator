package agy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	agyHooksDirName      = ".agents"
	agyHooksFileName     = "hooks.json"
	agyManagedHookName   = "agent-orchestrator"
	agyHookCommandPrefix = "ao hooks agy "
	agyHookTimeout       = 30
)

type agyHookEntry struct {
	Type    string `json:"type,omitempty"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type agyMatcherGroup struct {
	Matcher *string        `json:"matcher,omitempty"`
	Hooks   []agyHookEntry `json:"hooks"`
}

type agyNamedHook struct {
	PreInvocation []agyHookEntry    `json:"PreInvocation"`
	PostToolUse   []agyMatcherGroup `json:"PostToolUse"`
	Stop          []agyHookEntry    `json:"Stop"`
}

// GetAgentHooks installs AO's named hook in the current AGY workspace hook
// file. All other top-level entries are preserved.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) == "" {
		return errors.New("agy.GetAgentHooks: WorkspacePath is required")
	}

	hooksPath := agyHooksPath(cfg.WorkspacePath)
	topLevel, err := readAgyHooks(hooksPath)
	if err != nil {
		return fmt.Errorf("agy.GetAgentHooks: %w", err)
	}
	managedJSON, err := json.Marshal(managedAgyHook())
	if err != nil {
		return fmt.Errorf("agy.GetAgentHooks: encode managed hook: %w", err)
	}
	topLevel[agyManagedHookName] = managedJSON
	if err := writeAgyHooks(hooksPath, topLevel); err != nil {
		return fmt.Errorf("agy.GetAgentHooks: %w", err)
	}
	if err := hookutil.EnsureWorkspaceGitignore(filepath.Dir(hooksPath), agyHooksFileName); err != nil {
		return fmt.Errorf("agy.GetAgentHooks: gitignore: %w", err)
	}
	return nil
}

// UninstallHooks removes only AO's named AGY hook. A missing file is a no-op.
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
	} else if err != nil {
		return fmt.Errorf("agy.UninstallHooks: stat %s: %w", hooksPath, err)
	}
	topLevel, err := readAgyHooks(hooksPath)
	if err != nil {
		return fmt.Errorf("agy.UninstallHooks: %w", err)
	}
	delete(topLevel, agyManagedHookName)
	if err := writeAgyHooks(hooksPath, topLevel); err != nil {
		return fmt.Errorf("agy.UninstallHooks: %w", err)
	}
	return nil
}

// AreHooksInstalled reports whether AO's exact managed AGY hook is installed.
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
	} else if err != nil {
		return false, fmt.Errorf("agy.AreHooksInstalled: stat %s: %w", hooksPath, err)
	}
	topLevel, err := readAgyHooks(hooksPath)
	if err != nil {
		return false, fmt.Errorf("agy.AreHooksInstalled: %w", err)
	}
	raw, ok := topLevel[agyManagedHookName]
	if !ok {
		return false, nil
	}
	var installed agyNamedHook
	if err := json.Unmarshal(raw, &installed); err != nil {
		return false, fmt.Errorf("agy.AreHooksInstalled: unmarshal hook: %w", err)
	}
	return reflect.DeepEqual(installed, managedAgyHook()), nil
}

func agyHooksPath(workspacePath string) string {
	return filepath.Join(workspacePath, agyHooksDirName, agyHooksFileName)
}

func managedAgyHook() agyNamedHook {
	matcher := "*"
	entry := func(event string) agyHookEntry {
		return agyHookEntry{Type: "command", Command: agyHookCommandPrefix + event, Timeout: agyHookTimeout}
	}
	return agyNamedHook{
		PreInvocation: []agyHookEntry{entry("pre-invocation")},
		PostToolUse: []agyMatcherGroup{{
			Matcher: &matcher,
			Hooks:   []agyHookEntry{entry("post-tool-use")},
		}},
		Stop: []agyHookEntry{entry("stop")},
	}
}

// readAgyHooks preserves unowned top-level entries as raw JSON values. Missing
// and blank files are treated as empty configuration.
func readAgyHooks(hooksPath string) (map[string]json.RawMessage, error) {
	topLevel := map[string]json.RawMessage{}
	data, err := os.ReadFile(hooksPath) //nolint:gosec // caller-owned workspace path
	if errors.Is(err, os.ErrNotExist) {
		return topLevel, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", hooksPath, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return topLevel, nil
	}
	if err := json.Unmarshal(data, &topLevel); err != nil {
		return nil, fmt.Errorf("parse %s: %w", hooksPath, err)
	}
	if topLevel == nil {
		return nil, fmt.Errorf("parse %s: top-level value must be an object", hooksPath)
	}
	return topLevel, nil
}

func writeAgyHooks(hooksPath string, topLevel map[string]json.RawMessage) error {
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o750); err != nil {
		return fmt.Errorf("create hook dir: %w", err)
	}
	data, err := json.MarshalIndent(topLevel, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", hooksPath, err)
	}
	data = append(data, '\n')
	if err := hookutil.AtomicWriteFile(hooksPath, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", hooksPath, err)
	}
	return nil
}
