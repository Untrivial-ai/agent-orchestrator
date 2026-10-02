package modelcatalog

import (
	"os"
	"path/filepath"
	"testing"
)

func modelJSON(model string) string { return `{"model": "` + model + `"}` }

// openCodeFixture is a git repository with the session working directory at
// repo/pkg and a global config selecting "global/a".
func newOpenCodeFixture(t *testing.T) (repo gitRepo, pkg string) {
	t.Helper()
	home := isolateHome(t)
	repo = newGitRepo(t)
	pkg = filepath.Join(repo.dir, "pkg")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, filepath.Join(home, ".config", "opencode", "opencode.json"), modelJSON("global/a"))
	return repo, pkg
}

func TestOpenCodeV1DefaultUsesUserConfigOnly(t *testing.T) {
	for _, file := range []string{"opencode.json", "pkg/opencode.jsonc", ".opencode/opencode.json", "pkg/.opencode/opencode.jsonc"} {
		t.Run(file, func(t *testing.T) {
			repo, pkg := newOpenCodeFixture(t)
			if got := configuredDefaultModel("opencode", pkg, nil); got != "global/a" {
				t.Fatalf("global only = %q, want global/a", got)
			}
			// Any repository project config may differ in the launch worktree.
			writeConfig(t, filepath.Join(repo.dir, filepath.FromSlash(file)), modelJSON("repo/c"))
			if got := configuredDefaultModel("opencode", pkg, nil); got != "" {
				t.Errorf("repository config %s = %q, want unresolved", file, got)
			}
			// Inline content and managed config outrank every project file.
			if got := configuredDefaultModel("opencode", pkg, map[string]string{"OPENCODE_CONFIG_CONTENT": modelJSON("inline/e")}); got != "inline/e" {
				t.Errorf("OPENCODE_CONFIG_CONTENT = %q, want inline/e", got)
			}
		})
	}
}

// v2 also reads the launch worktree's ancestors above its git root (e.g.
// <AO_DATA_DIR>/opencode.json), which discovery cannot see, so only layers
// that outrank project config are trusted.
func TestOpenCodeV2DefaultTrustsOnlyLayersAboveProjectConfig(t *testing.T) {
	_, pkg := newOpenCodeFixture(t)
	if got := configuredDefaultModel("opencode-v2", pkg, nil); got != "" {
		t.Errorf("v2 global model = %q, want unresolved", got)
	}
	if got := configuredDefaultModel("opencode-v2", pkg, map[string]string{"OPENCODE_CONFIG_CONTENT": modelJSON("inline/e")}); got != "inline/e" {
		t.Errorf("v2 OPENCODE_CONFIG_CONTENT = %q, want inline/e", got)
	}
	managed := t.TempDir()
	openCodeManagedConfigDirs = func() []string { return []string{managed} }
	writeConfig(t, filepath.Join(managed, "opencode.json"), modelJSON("managed/f"))
	if got := configuredDefaultModel("opencode-v2", pkg, map[string]string{"OPENCODE_CONFIG_CONTENT": modelJSON("inline/e")}); got != "managed/f" {
		t.Errorf("v2 managed config = %q, want managed/f", got)
	}
}

func TestOpenCodeDefaultLeavesUncertainWinnersUnresolved(t *testing.T) {
	_, pkg := newOpenCodeFixture(t)

	// AO replaces OPENCODE_CONFIG at TUI launch but keeps it for ACP.
	custom := filepath.Join(t.TempDir(), "custom.json")
	writeConfig(t, custom, modelJSON("custom/g"))
	if got := configuredDefaultModel("opencode", pkg, map[string]string{"OPENCODE_CONFIG": custom}); got != "" {
		t.Errorf("winning OPENCODE_CONFIG = %q, want unresolved", got)
	}

	configDir := t.TempDir()
	writeConfig(t, filepath.Join(configDir, "opencode.json"), modelJSON("dir/h"))
	if got := configuredDefaultModel("opencode", pkg, map[string]string{"OPENCODE_CONFIG_DIR": configDir}); got != "" {
		t.Errorf("OPENCODE_CONFIG_DIR model = %q, want unresolved", got)
	}

	// Sibling global files that disagree have no documented merge order.
	home, _ := os.UserHomeDir()
	writeConfig(t, filepath.Join(home, ".config", "opencode", "opencode.jsonc"), modelJSON("global/other"))
	if got := configuredDefaultModel("opencode", pkg, nil); got != "" {
		t.Errorf("conflicting global configs = %q, want unresolved", got)
	}
}

func TestOpenCodeDefaultWithoutGitRootIsUnresolved(t *testing.T) {
	home := isolateHome(t)
	writeConfig(t, filepath.Join(home, ".config", "opencode", "opencode.json"), modelJSON("global/a"))
	workDir := t.TempDir()
	for _, agentID := range []string{"opencode", "opencode-v2"} {
		if got := configuredDefaultModel(agentID, workDir, nil); got != "" {
			t.Errorf("%s without a git root = %q, want unresolved", agentID, got)
		}
	}
}
