package workerexec

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// opencode's only lifecycle-extensibility surface is a JS/TS plugin loaded from
// the workspace-local `.opencode/plugins/` directory (it has no native command
// hooks the way claude-code/codex do). AO therefore installs a dedicated,
// fully AO-owned plugin file (see assets/ao-activity.ts) that normalizes
// opencode's native lifecycle events and relays them through
// `ao hooks opencode <event>` — the cloud agent installed as `ao` in the
// worker. This mirrors the desktop opencode adapter's install, so session
// activity reaches the control plane the same way in both worlds.
const (
	// opencodePluginDirName is the opencode config dir opencode scans for
	// plugins (`{plugin,plugins}/*.{ts,js}`); AO writes the plural `plugins/`,
	// matching upstream opencode tooling.
	opencodePluginDirName = ".opencode"
	opencodePluginSubDir  = "plugins"

	// opencodePluginFileName is the AO-owned plugin file. Install overwrites it
	// and refuses to clobber a same-named file that is not AO-managed.
	opencodePluginFileName = "ao-activity.ts"

	// opencodePluginSentinel marks the file as AO-managed. It must appear
	// verbatim in the embedded plugin source.
	opencodePluginSentinel = "agent-orchestrator: managed opencode activity plugin"

	// opencodePluginGitignoreSentinel marks the workspace .gitignore as
	// AO-managed so it can be rewritten idempotently while never touching a
	// user- or repo-provided .gitignore at the same path.
	opencodePluginGitignoreSentinel = "# managed by agent-orchestrator: AO hook files stay out of git status"
)

// opencodePluginSource is the AO-managed opencode plugin, embedded verbatim
// from the desktop adapter (backend/internal/adapters/agent/opencode/assets) so
// sandboxes voice the identical plugin contract regardless of which binary
// installs them. Keep the two copies in sync.
//
//go:embed assets/ao-activity.ts
var opencodePluginSource string

// installOpenCodeActivityHooks writes AO's opencode activity plugin into the
// workspace-local .opencode/plugins/ directory. The write is atomic and
// idempotent: re-installing overwrites AO's own file with identical content and
// fails loudly rather than clobbering a user plugin that occupies the path.
func installOpenCodeActivityHooks(workspace string) error {
	pluginPath := opencodePluginPath(workspace)
	if existing, err := os.ReadFile(pluginPath); err == nil {
		if !strings.Contains(string(existing), opencodePluginSentinel) {
			return fmt.Errorf(
				"refusing to overwrite non-AO opencode plugin at %s — move it so AO can install its plugin",
				pluginPath,
			)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat opencode plugin: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o750); err != nil {
		return fmt.Errorf("create opencode plugin dir: %w", err)
	}
	if err := os.WriteFile(pluginPath, []byte(opencodePluginSource), 0o600); err != nil {
		return fmt.Errorf("write opencode plugin: %w", err)
	}
	if err := ensureOpenCodePluginGitignored(
		filepath.Dir(pluginPath), opencodePluginFileName,
	); err != nil {
		return fmt.Errorf("opencode plugin gitignore: %w", err)
	}
	return nil
}

func opencodePluginPath(workspace string) string {
	return filepath.Join(
		workspace, opencodePluginDirName, opencodePluginSubDir, opencodePluginFileName,
	)
}

// ensureOpenCodePluginGitignored writes a self-ignoring .gitignore beside the
// plugin so the AO-installed hook file never makes the session worktree
// permanently dirty (and un-removable). The patterns are anchored to the plugin
// file only; anything else an agent drops in the same directory still counts as
// dirt and keeps blocking teardown. A .gitignore at the same path that lacks
// the sentinel is left untouched and the install proceeds — the worktree then
// stays dirty and teardown preserves it, which is the safe degradation.
func ensureOpenCodePluginGitignored(dir string, names ...string) error {
	path := filepath.Join(dir, ".gitignore")
	existing, err := os.ReadFile(path) //nolint:gosec // path built from caller-owned workspace dir
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err == nil && !strings.Contains(string(existing), opencodePluginGitignoreSentinel) {
		return nil
	}
	var b strings.Builder
	b.WriteString(opencodePluginGitignoreSentinel)
	b.WriteString("\n/.gitignore\n")
	for _, name := range names {
		b.WriteString("/")
		b.WriteString(filepath.ToSlash(name))
		b.WriteString("\n")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}