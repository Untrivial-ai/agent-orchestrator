package zcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const hookCommandPrefix = "ao hooks zcode "

var managedHooks = []struct{ event, eventArg string }{
	{"SessionStart", "session-start"}, {"UserPromptSubmit", "user-prompt-submit"},
	{"PreToolUse", "pre-tool-use"}, {"PermissionRequest", "permission-request"},
	{"PostToolUse", "post-tool-use"}, {"PostToolUseFailure", "post-tool-use-failure"}, {"Stop", "stop"},
}

// GetAgentHooks merges native hooks.events while preserving user settings.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	return updateHooks(ctx, cfg.WorkspacePath, true)
}

// UninstallHooks removes only AO commands from the workspace configuration.
func (p *Plugin) UninstallHooks(ctx context.Context, workspace string) error {
	return updateHooks(ctx, workspace, false)
}
func updateHooks(ctx context.Context, workspace string, install bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(workspace) == "" {
		return nil
	}
	path := filepath.Join(workspace, ".zcode", "config.json")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("zcode: read hooks: %w", err)
	}
	if errors.Is(err, os.ErrNotExist) && !install {
		return nil
	}
	root := map[string]json.RawMessage{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("zcode: invalid config: %w", err)
		}
		if root == nil {
			return fmt.Errorf("zcode: config must be an object")
		}
	}
	hooks := map[string]json.RawMessage{}
	if raw, ok := root["hooks"]; ok {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return fmt.Errorf("zcode: invalid hooks: %w", err)
		}
		if hooks == nil {
			return fmt.Errorf("zcode: hooks must be an object")
		}
	}
	if install {
		if raw, ok := hooks["enabled"]; ok {
			var enabled bool
			if err := json.Unmarshal(raw, &enabled); err != nil || !enabled {
				return fmt.Errorf("zcode: workspace hooks are disabled or invalid; enable them in native settings before launching an AO session")
			}
		} else {
			// Native hooks default to disabled even when events are present.
			// Declaration-specific trust is still required before execution.
			hooks["enabled"] = json.RawMessage("true")
		}
	}
	events := map[string]json.RawMessage{}
	if raw, ok := hooks["events"]; ok {
		if err := json.Unmarshal(raw, &events); err != nil {
			return fmt.Errorf("zcode: invalid hook events: %w", err)
		}
		if events == nil {
			return fmt.Errorf("zcode: hook events must be an object")
		}
	}
	for _, spec := range managedHooks {
		var groups []hooksjson.MatcherGroup
		if raw, ok := events[spec.event]; ok {
			if err := json.Unmarshal(raw, &groups); err != nil {
				return fmt.Errorf("zcode: invalid %s hooks: %w", spec.event, err)
			}
		}
		kept := make([]hooksjson.MatcherGroup, 0, len(groups)+1)
		for _, group := range groups {
			entries := make([]hooksjson.HookEntry, 0, len(group.Hooks))
			for _, entry := range group.Hooks {
				if !strings.HasPrefix(entry.Command, hookCommandPrefix) {
					entries = append(entries, entry)
				}
			}
			if len(entries) > 0 {
				group.Hooks = entries
				kept = append(kept, group)
			}
		}
		if install {
			kept = append(kept, hooksjson.MatcherGroup{Hooks: []hooksjson.HookEntry{{Type: "command", Command: hookCommandPrefix + spec.eventArg, Timeout: 30}}})
		}
		if len(kept) == 0 {
			delete(events, spec.event)
		} else {
			raw, err := json.Marshal(kept)
			if err != nil {
				return err
			}
			events[spec.event] = raw
		}
	}
	raw, err := json.Marshal(events)
	if err != nil {
		return err
	}
	hooks["events"] = raw
	raw, err = json.Marshal(hooks)
	if err != nil {
		return err
	}
	root["hooks"] = raw
	raw, err = json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := hookutil.AtomicWriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return hookutil.EnsureWorkspaceGitignore(filepath.Dir(path), filepath.Base(path))
}

type trustItem struct {
	Event                 string  `json:"event"`
	Matcher               *string `json:"matcher"`
	DisplayCommand        string  `json:"displayCommand"`
	SourcePath            string  `json:"sourcePath"`
	ConfiguredEnabled     bool    `json:"configuredEnabled"`
	HookDeclarationDigest string  `json:"hookDeclarationDigest"`
	TrustState            string  `json:"trustState"`
}
type trustStatus struct {
	WorkspacePath string      `json:"workspacePath"`
	ReasonCode    string      `json:"reasonCode"`
	Items         []trustItem `json:"items"`
}

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Only exact AO declarations in our file may be granted. Never grant a whole
// workspace bundle: it can include unrelated project-provided shell commands.
func aoHookDigests(status trustStatus, workspace string) ([]string, error) {
	if filepath.Clean(status.WorkspacePath) != filepath.Clean(workspace) {
		return nil, fmt.Errorf("zcode: hook trust response belongs to another workspace")
	}
	if status.ReasonCode == "workspace_hooks_trust_store_corrupt" {
		return nil, fmt.Errorf("zcode: native hook trust store is corrupt")
	}
	var pending []string
	for _, spec := range managedHooks {
		found := false
		for _, item := range status.Items {
			if item.Event != spec.event || item.DisplayCommand != hookCommandPrefix+spec.eventArg || filepath.ToSlash(item.SourcePath) != ".zcode/config.json" || item.Matcher != nil || !item.ConfiguredEnabled {
				continue
			}
			if !digestPattern.MatchString(item.HookDeclarationDigest) {
				return nil, fmt.Errorf("zcode: invalid hook declaration digest")
			}
			found = true
			if item.TrustState != "trusted_persistent" {
				pending = append(pending, item.HookDeclarationDigest)
			}
		}
		if !found {
			return nil, fmt.Errorf("zcode: required AO %s hook is unavailable", spec.event)
		}
	}
	return pending, nil
}

func (p *Plugin) prepareHookTrust(ctx context.Context, binary, workspace string, env map[string]string) error {
	if strings.TrimSpace(workspace) == "" {
		return nil
	}
	absolute, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	run := func(args ...string) (trustStatus, error) {
		command := aoprocess.CommandContext(ctx, binary, args...)
		command.Dir = absolute
		command.Env = os.Environ()
		for key, value := range env {
			command.Env = append(command.Env, key+"="+value)
		}
		command.WaitDelay = 2 * time.Second
		output, err := command.Output()
		if err != nil {
			return trustStatus{}, fmt.Errorf("zcode: native hook trust command failed: %w", err)
		}
		var status trustStatus
		if err := json.Unmarshal(output, &status); err != nil {
			return status, fmt.Errorf("zcode: invalid native hook trust response: %w", err)
		}
		return status, nil
	}
	status, err := run("hooks", "trust", "status", "--workspace", absolute, "--json")
	if err != nil {
		return err
	}
	pending, err := aoHookDigests(status, absolute)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	args := []string{"hooks", "trust", "grant", "--workspace", absolute, "--json"}
	for _, digest := range pending {
		args = append(args, "--hook-digest", digest)
	}
	status, err = run(args...)
	if err != nil {
		return err
	}
	pending, err = aoHookDigests(status, absolute)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		return fmt.Errorf("zcode: native CLI did not trust AO hooks")
	}
	return nil
}
