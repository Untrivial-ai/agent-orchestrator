package modelcatalog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// isolateHome points every config root discovery consults at an empty temp dir.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT",
		"COPILOT_HOME", "COPILOT_MODEL", "AIDER_MODEL"} {
		t.Setenv(key, "")
	}
	managed := openCodeManagedConfigDirs
	openCodeManagedConfigDirs = func() []string { return nil }
	t.Cleanup(func() { openCodeManagedConfigDirs = managed })
	return home
}

// gitRepo is a real git repository with one empty commit, so HEAD resolves.
type gitRepo struct {
	t   *testing.T
	dir string
}

func newGitRepo(t *testing.T) gitRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	r := gitRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q")
	r.commit("init")
	return r
}

func (r gitRepo) git(args ...string) {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (r gitRepo) commit(message string) {
	r.t.Helper()
	r.git("-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", message)
}

func TestOpenCodeDiscoveryMarksUserConfiguredModelAsDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI is a shell script")
	}
	home := isolateHome(t)
	repo := newGitRepo(t)
	writeConfig(t, filepath.Join(home, ".config", "opencode", "opencode.jsonc"), `{
		// user choice
		"$schema": "https://opencode.ai/config.json",
		"model": "openai/gpt-5.4",
	}`)
	binary := filepath.Join(t.TempDir(), "opencode")
	writeConfig(t, binary, "#!/bin/sh\nprintf 'anthropic/claude-sonnet-4-6\\nopenai/gpt-5.4\\n'\n")
	if err := os.Chmod(binary, 0o755); err != nil {
		t.Fatal(err)
	}

	catalog, err := Discover(context.Background(), "opencode", binary, repo.dir, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	var defaults []string
	for _, item := range catalog.Models {
		if item.IsDefault {
			defaults = append(defaults, item.ID)
		}
	}
	if len(defaults) != 1 || defaults[0] != "openai/gpt-5.4" {
		t.Fatalf("defaults = %v, want [openai/gpt-5.4]", defaults)
	}
}

// A session can be attached to an existing local branch or seeded from a
// remote branch or tag, so config that exists only on such a ref, not in the
// checkout or at HEAD, must still leave the default unresolved.
func TestConfiguredDefaultUnresolvedWhenAnySeedRefHoldsConfig(t *testing.T) {
	setup := func(t *testing.T) (gitRepo, string) {
		t.Helper()
		home := isolateHome(t)
		writeConfig(t, filepath.Join(home, ".copilot", "settings.json"), `{"model": "user"}`)
		repo := newGitRepo(t)
		repo.git("branch", "-M", "main")
		return repo, filepath.Join(repo.dir, ".github", "copilot", "settings.json")
	}
	// commitOnBranch commits the config on branch and returns to main, whose
	// tree and working copy have none.
	commitOnBranch := func(repo gitRepo, config, branch string) {
		repo.git("checkout", "-q", "-b", branch)
		writeConfig(repo.t, config, `{"model": "branch"}`)
		repo.git("add", ".github/copilot/settings.json")
		repo.commit("branch config")
		repo.git("checkout", "-q", "main")
		if _, err := os.Stat(config); !os.IsNotExist(err) {
			repo.t.Fatalf("checkout should not hold the branch config: %v", err)
		}
	}

	t.Run("local branch attached by git worktree add", func(t *testing.T) {
		repo, config := setup(t)
		if got := configuredDefaultModel("copilot", repo.dir, nil); got != "user" {
			t.Fatalf("no config on any ref = %q, want user", got)
		}
		commitOnBranch(repo, config, "feature")
		worktree := filepath.Join(t.TempDir(), "wt")
		repo.git("worktree", "add", "-q", worktree, "feature")
		if _, err := os.Stat(filepath.Join(worktree, ".github", "copilot", "settings.json")); err != nil {
			t.Fatalf("session worktree should hold the branch config: %v", err)
		}
		if got := configuredDefaultModel("copilot", repo.dir, nil); got != "" {
			t.Errorf("config only on a local branch = %q, want unresolved", got)
		}
	})

	t.Run("remote-tracking branch", func(t *testing.T) {
		repo, config := setup(t)
		commitOnBranch(repo, config, "feature")
		repo.git("update-ref", "refs/remotes/origin/feature", "feature")
		repo.git("branch", "-q", "-D", "feature")
		if got := configuredDefaultModel("copilot", repo.dir, nil); got != "" {
			t.Errorf("config only on a remote branch = %q, want unresolved", got)
		}
	})

	t.Run("tag", func(t *testing.T) {
		repo, config := setup(t)
		commitOnBranch(repo, config, "feature")
		repo.git("tag", "v1", "feature")
		repo.git("branch", "-q", "-D", "feature")
		if got := configuredDefaultModel("copilot", repo.dir, nil); got != "" {
			t.Errorf("config only on a tag = %q, want unresolved", got)
		}
	})
}

func TestApplyConfiguredDefault(t *testing.T) {
	listed := func() []ports.AgentModelInfo {
		return []ports.AgentModelInfo{{ID: "a/one", Label: "one"}, {ID: "b/two", Label: "two"}}
	}

	got := applyConfiguredDefault(listed(), "B/TWO")
	if got[0].IsDefault || !got[1].IsDefault || len(got) != 2 {
		t.Errorf("case-insensitive match = %#v", got)
	}

	got = applyConfiguredDefault(listed(), "c/three")
	if len(got) != 3 || got[2].ID != "c/three" || !got[2].IsDefault {
		t.Errorf("unlisted configured model should be appended as default: %#v", got)
	}

	reported := listed()
	reported[0].IsDefault = true
	got = applyConfiguredDefault(reported, "b/two")
	if !got[0].IsDefault || got[1].IsDefault {
		t.Errorf("a CLI-reported default must win over config: %#v", got)
	}

	for _, configured := range []string{"", "  ", "default", "Default"} {
		if got := applyConfiguredDefault(listed(), configured); len(got) != 2 || got[0].IsDefault || got[1].IsDefault {
			t.Errorf("placeholder %q must not select a default: %#v", configured, got)
		}
	}
}

func TestConfiguredDefaultModelSources(t *testing.T) {
	home := isolateHome(t)
	workDir := newGitRepo(t).dir

	writeConfig(t, filepath.Join(home, ".aider.conf.yml"), "model: sonnet\n")
	if got := configuredDefaultModel("aider", workDir, nil); got != "sonnet" {
		t.Errorf("aider user config = %q, want sonnet", got)
	}
	if got := configuredDefaultModel("aider", workDir, map[string]string{"AIDER_MODEL": "opus"}); got != "opus" {
		t.Errorf("aider AIDER_MODEL = %q, want opus", got)
	}

	writeConfig(t, filepath.Join(home, ".pi", "agent", "settings.json"), `{"defaultProvider": "anthropic", "defaultModel": "claude-opus-4-6"}`)
	if got := configuredDefaultModel("pi", workDir, nil); got != "anthropic/claude-opus-4-6" {
		t.Errorf("pi = %q", got)
	}

	writeConfig(t, filepath.Join(home, ".local", "share", "crush", "crush.json"), `{"models": {"large": {"provider": "openai", "model": "gpt-5.4"}}}`)
	if got := configuredDefaultModel("crush", workDir, nil); got != "openai/gpt-5.4" {
		t.Errorf("crush = %q", got)
	}

	writeConfig(t, filepath.Join(home, ".factory", "settings.json"), `{"model": "claude-sonnet-4-6"}`)
	if got := configuredDefaultModel("droid", workDir, nil); got != "claude-sonnet-4-6" {
		t.Errorf("droid = %q", got)
	}

	copilotHome := t.TempDir()
	copilotEnv := map[string]string{"COPILOT_HOME": copilotHome}
	// Legacy config.json is managed state, not the user's model choice.
	writeConfig(t, filepath.Join(copilotHome, "config.json"), `{"model": "stale-legacy-model"}`)
	if got := configuredDefaultModel("copilot", workDir, copilotEnv); got != "" {
		t.Errorf("copilot must ignore legacy config.json, got %q", got)
	}
	writeConfig(t, filepath.Join(copilotHome, "settings.json"), `{"model": "gpt-5.4"}`)
	if got := configuredDefaultModel("copilot", workDir, copilotEnv); got != "gpt-5.4" {
		t.Errorf("copilot user settings = %q, want gpt-5.4", got)
	}
	copilotEnv["COPILOT_MODEL"] = "gpt-5.5"
	if got := configuredDefaultModel("copilot", workDir, copilotEnv); got != "gpt-5.5" {
		t.Errorf("COPILOT_MODEL must override settings files, got %q", got)
	}

	if got := configuredDefaultModel("cursor", workDir, nil); got != "" {
		t.Errorf("agent without a config source = %q, want empty", got)
	}
}

// Repository config is never read: the launch worktree's copy is chosen per
// session and can differ from the checkout's. Wherever the repository may
// hold one, the default stays unresolved; an env override still wins.
func TestConfiguredDefaultUnresolvedWhenRepositoryMayHoldConfig(t *testing.T) {
	for _, tc := range []struct {
		agentID, file, content string
		userConfig             func(home string) (path, content string)
		env                    string
	}{
		{
			agentID: "copilot", file: ".github/copilot/settings.json", content: `{"model": "repo"}`,
			userConfig: func(home string) (string, string) {
				return filepath.Join(home, ".copilot", "settings.json"), `{"model": "user"}`
			},
			env: "COPILOT_MODEL",
		},
		{
			agentID: "copilot", file: ".github/copilot/settings.local.json", content: `{"model": "local"}`,
			userConfig: func(home string) (string, string) {
				return filepath.Join(home, ".copilot", "settings.json"), `{"model": "user"}`
			},
			env: "COPILOT_MODEL",
		},
		{
			agentID: "aider", file: ".aider.conf.yml", content: "model: repo\n",
			userConfig: func(home string) (string, string) {
				return filepath.Join(home, ".aider.conf.yml"), "model: user\n"
			},
			env: "AIDER_MODEL",
		},
		{
			agentID: "crush", file: "crush.json", content: `{"models": {"large": {"model": "repo"}}}`,
			userConfig: func(home string) (string, string) {
				return filepath.Join(home, ".config", "crush", "crush.json"), `{"models": {"large": {"model": "user"}}}`
			},
		},
	} {
		t.Run(tc.agentID+" "+tc.file, func(t *testing.T) {
			home := isolateHome(t)
			path, content := tc.userConfig(home)
			writeConfig(t, path, content)
			repo := newGitRepo(t)
			sub := filepath.Join(repo.dir, "pkg")
			if err := os.MkdirAll(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			if got := configuredDefaultModel(tc.agentID, sub, nil); got != "user" {
				t.Fatalf("no repository config = %q, want user", got)
			}

			// Untracked, staged, or committed: the checkout copy may not be the
			// launch worktree's, so the default is unresolved in every state.
			repoFile := filepath.Join(repo.dir, filepath.FromSlash(tc.file))
			writeConfig(t, repoFile, tc.content)
			if got := configuredDefaultModel(tc.agentID, sub, nil); got != "" {
				t.Errorf("untracked repository config = %q, want unresolved", got)
			}
			repo.git("add", "-f", tc.file)
			if got := configuredDefaultModel(tc.agentID, sub, nil); got != "" {
				t.Errorf("staged repository config = %q, want unresolved", got)
			}
			repo.commit("config")
			// Deleted locally but still committed: a worktree seeded from HEAD
			// would contain it.
			if err := os.Remove(repoFile); err != nil {
				t.Fatal(err)
			}
			if got := configuredDefaultModel(tc.agentID, sub, nil); got != "" {
				t.Errorf("committed but locally deleted config = %q, want unresolved", got)
			}
			if tc.env != "" {
				if got := configuredDefaultModel(tc.agentID, sub, map[string]string{tc.env: "env"}); got != "env" {
					t.Errorf("%s over repository config = %q, want env", tc.env, got)
				}
			}
		})
	}
}

// A repository config file present only in a launch worktree (committed on
// the seed commit) must not be missed: an actual `git worktree add` from HEAD
// carries it even when the checkout has deleted it.
func TestConfiguredDefaultMatchesActualWorktree(t *testing.T) {
	home := isolateHome(t)
	writeConfig(t, filepath.Join(home, ".copilot", "settings.json"), `{"model": "user"}`)
	repo := newGitRepo(t)
	writeConfig(t, filepath.Join(repo.dir, ".github", "copilot", "settings.json"), `{"model": "repo"}`)
	repo.git("add", ".github/copilot/settings.json")
	repo.commit("config")
	if err := os.Remove(filepath.Join(repo.dir, ".github", "copilot", "settings.json")); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(t.TempDir(), "wt")
	repo.git("worktree", "add", "-q", "-b", "session", worktree, "HEAD")
	if _, err := os.Stat(filepath.Join(worktree, ".github", "copilot", "settings.json")); err != nil {
		t.Fatalf("worktree should contain the committed config: %v", err)
	}
	// The session would run "repo", so "user" must not be shown as default.
	if got := configuredDefaultModel("copilot", repo.dir, nil); got != "" {
		t.Errorf("default with config in the launch worktree = %q, want unresolved", got)
	}
}

func TestStripJSONCKeepsStringsAndDropsTrailingCommas(t *testing.T) {
	got := parseJSONCModelKey([]byte(`{
		/* block */ "url": "https://example.com//x", // line
		"model": "a/b",
	}`))
	if got != "a/b" {
		t.Errorf("model = %q, want a/b", got)
	}
}

func TestConfiguredDefaultFingerprint(t *testing.T) {
	home := isolateHome(t)
	workDir := newGitRepo(t).dir
	if got := discoveryConfigInputs(context.Background(), "opencode", workDir, nil); got != "" {
		t.Fatalf("no configured default must keep the binary-only fingerprint, got %q", got)
	}
	writeConfig(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{"model": "openai/gpt-5.4"}`)
	if got := discoveryConfigInputs(context.Background(), "opencode", workDir, nil); got != "default=openai/gpt-5.4" {
		t.Fatalf("configured default must feed the fingerprint, got %q", got)
	}
}
