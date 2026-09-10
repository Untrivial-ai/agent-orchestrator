package domain

import "testing"

func TestProjectConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ProjectConfig
		wantErr bool
	}{
		{"empty ok", ProjectConfig{}, false},
		{"good agent config", ProjectConfig{AgentConfig: AgentConfig{Model: "m", Permissions: PermissionModeAuto}}, false},
		{"good agent mode", ProjectConfig{AgentConfig: AgentConfig{Mode: "ultra"}}, false},
		{"bad agent mode", ProjectConfig{AgentConfig: AgentConfig{Mode: "turbo"}}, true},
		{"bad permission", ProjectConfig{AgentConfig: AgentConfig{Permissions: "yolo"}}, true},
		{"good session prefix", ProjectConfig{SessionPrefix: "ao"}, false},
		{"session prefix with slash", ProjectConfig{SessionPrefix: "ao/project"}, true},
		{"session prefix with backslash", ProjectConfig{SessionPrefix: `ao\project`}, true},
		{"session prefix traversal component", ProjectConfig{SessionPrefix: ".."}, true},
		{"good role override", ProjectConfig{Worker: RoleOverride{Harness: HarnessCodex}}, false},
		{"unknown role harness", ProjectConfig{Orchestrator: RoleOverride{Harness: "nope"}}, true},
		{"bad role agent config", ProjectConfig{Worker: RoleOverride{AgentConfig: AgentConfig{Permissions: "nope"}}}, true},
		{"good symlinks", ProjectConfig{Symlinks: []string{".env", "configs/dev.toml"}}, false},
		{"symlink absolute path", ProjectConfig{Symlinks: []string{"/etc/passwd"}}, true},
		{"symlink parent escape", ProjectConfig{Symlinks: []string{"../escape"}}, true},
		{"symlink embedded parent", ProjectConfig{Symlinks: []string{"a/../../b"}}, true},
		{"symlink bare ..", ProjectConfig{Symlinks: []string{".."}}, true},
		{"good prompt rules", ProjectConfig{AgentRules: "Run tests.", AgentRulesFile: "docs/agent-rules.md", OrchestratorRules: "Delegate work."}, false},
		{"agent rules file absolute path", ProjectConfig{AgentRulesFile: "/etc/passwd"}, true},
		{"agent rules file parent escape", ProjectConfig{AgentRulesFile: "../rules.md"}, true},
		{"agent rules file cleans to dot", ProjectConfig{AgentRulesFile: "docs/.."}, true},
		{"agent rules file bare dot", ProjectConfig{AgentRulesFile: "."}, true},
		{"good reviewers", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerClaudeCode}}}, false},
		{"good codex reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerCodex}}}, false},
		{"good copilot reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerCopilot}}}, false},
		{"good cursor reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerCursor}}}, false},
		{"good Kilo Code reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerKiloCode}}}, false},
		{"good kimchi reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerKimchi}}}, false},
		{"good opencode reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerOpenCode}}}, false},
		{"good kiro reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerKiro}}}, false},
		{"good pi reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerPi}}}, false},
		{"good experimental qwen reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerQwen}}}, false},
		{"good experimental agy reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerAgy}}}, false},
		{"good experimental continue reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerContinue}}}, false},
		{"good experimental goose reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerGoose}}}, false},
		{"good experimental vibe reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerVibe}}}, false},
		{"good experimental Devin reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerDevin}}}, false},
		{"good experimental Droid reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerDroid}}}, false},
		{"good experimental Kimi reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerKimi}}}, false},
		{"good Muse reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerMuse}}}, false},
		{"unknown reviewer harness", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: "nope"}}}, true},
		{"good interactive Amp reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerAmp}}}, false},
		{"good interactive Aider reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerAider}}}, false},
		{"good experimental Grok reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerGrok}}}, false},
		{"good experimental Crush reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerCrush}}}, false},
		{"good experimental Auggie reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerAuggie}}}, false},
		{"good experimental Cline reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerCline}}}, false},
		{"good experimental Autohand reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerAutohand}}}, false},
		{"empty reviewer harness", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ""}}}, true},
		{"tracker intake assignee rule", ProjectConfig{TrackerIntake: TrackerIntakeConfig{Enabled: true, Assignee: "alice"}}, false},
		{"tracker intake explicit github", ProjectConfig{TrackerIntake: TrackerIntakeConfig{Enabled: true, Provider: TrackerProviderGitHub, Assignee: "alice"}}, false},
		{"tracker intake no rule", ProjectConfig{TrackerIntake: TrackerIntakeConfig{Enabled: true}}, true},
		{"tracker intake unknown provider", ProjectConfig{TrackerIntake: TrackerIntakeConfig{Enabled: true, Provider: "linear", Assignee: "alice"}}, true},
		{"tracker intake repo with whitespace", ProjectConfig{TrackerIntake: TrackerIntakeConfig{Enabled: true, Repo: " acme/demo", Assignee: "alice"}}, true},
		{"tracker intake assignee with whitespace", ProjectConfig{TrackerIntake: TrackerIntakeConfig{Enabled: true, Assignee: " alice"}}, true},
		{"auto review enabled", ProjectConfig{AutoReview: true}, false},
		{"auto review disabled", ProjectConfig{AutoReview: false}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestDefaultProjectConfig(t *testing.T) {
	def := DefaultProjectConfig()

	// The one documented non-empty default.
	if def.DefaultBranch != DefaultBranchAuto {
		t.Fatalf("default DefaultBranch = %q, want %q", def.DefaultBranch, DefaultBranchAuto)
	}

	// Every other field defaults to its zero value: clearing the documented
	// default must leave the config completely empty.
	def.DefaultBranch = ""
	if !def.IsZero() {
		t.Fatalf("default config has unexpected non-zero fields: %#v", def)
	}
}

func TestProjectConfigWithDefaults(t *testing.T) {
	// An unset config gets the documented defaults.
	got := (ProjectConfig{}).WithDefaults()
	if got.DefaultBranch != DefaultBranchAuto {
		t.Fatalf("WithDefaults = %#v, want branch=%s", got, DefaultBranchAuto)
	}

	// Set fields are preserved, not overwritten.
	got = (ProjectConfig{
		DefaultBranch: "develop",
		AgentConfig:   AgentConfig{Model: "m"},
	}).WithDefaults()
	if got.DefaultBranch != "develop" {
		t.Fatalf("WithDefaults overwrote set fields: %#v", got)
	}
	if got.AgentConfig.Model != "m" {
		t.Fatalf("WithDefaults dropped a set field: %#v", got.AgentConfig)
	}
	if got.WorktreeBaseBranch() != "develop" {
		t.Fatalf("WorktreeBaseBranch = %q, want develop", got.WorktreeBaseBranch())
	}
	if got := (ProjectConfig{}).WorktreeBaseBranch(); got != "" {
		t.Fatalf("automatic WorktreeBaseBranch = %q, want empty for adapter inference", got)
	}
	if got := (ProjectConfig{DefaultBranch: DefaultBranchAuto}).WorktreeBaseBranch(); got != "" {
		t.Fatalf("explicit auto WorktreeBaseBranch = %q, want empty for adapter inference", got)
	}

	got = (ProjectConfig{TrackerIntake: TrackerIntakeConfig{Enabled: true, Assignee: "alice"}}).WithDefaults()
	if got.TrackerIntake.Provider != "" {
		t.Fatalf("TrackerIntake.Provider = %q, want empty (inferred at use time)", got.TrackerIntake.Provider)
	}

	got = (ProjectConfig{}).WithDefaults()
	if got.TrackerIntake.Provider != "" {
		t.Fatalf("disabled TrackerIntake.Provider = %q, want empty", got.TrackerIntake.Provider)
	}
}

func TestInferTrackerProvider(t *testing.T) {
	tests := []struct {
		name    string
		repoURL string
		want    TrackerProvider
	}{
		{"empty", "", TrackerProviderGitHub},
		{"https github", "https://github.com/acme/demo.git", TrackerProviderGitHub},
		{"ssh github", "git@github.com:acme/demo.git", TrackerProviderGitHub},
		{"ghe host", "https://ghe.corp.ghe.io/acme/demo.git", TrackerProviderGitHub},
		{"github with port", "https://github.com:443/org/repo.git", TrackerProviderGitHub},
		{"ssh github with port", "ssh://git@github.com:2222/org/repo.git", TrackerProviderGitHub},
		{"https gitlab.com", "https://gitlab.com/group/repo.git", TrackerProviderGitLab},
		{"ssh gitlab.com", "git@gitlab.com:group/repo.git", TrackerProviderGitLab},
		{"self-managed gitlab", "https://gitlab.internal/group/repo.git", TrackerProviderGitLab},
		{"ssh self-managed", "git@gitlab.internal:group/repo.git", TrackerProviderGitLab},
		{"self-managed with port", "https://gitlab.local:8443/group/repo.git", TrackerProviderGitLab},
		{"non-gitlab custom host", "https://dev.company.com/group/repo.git", TrackerProviderGitLab},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InferTrackerProvider(tt.repoURL)
			if got != tt.want {
				t.Errorf("InferTrackerProvider(%q) = %q, want %q", tt.repoURL, got, tt.want)
			}
		})
	}
}

func TestResolveReviewerHarness(t *testing.T) {
	// A configured reviewer always wins, regardless of the worker harness.
	cfg := ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerClaudeCode}}}
	if got := cfg.ResolveReviewerHarness(HarnessAider); got != ReviewerClaudeCode {
		t.Fatalf("configured reviewer = %q, want claude-code", got)
	}

	// No reviewer configured: preserve automatic inheritance only for the
	// original unattended-safe reviewer set.
	if got := (ProjectConfig{}).ResolveReviewerHarness(HarnessClaudeCode); got != ReviewerClaudeCode {
		t.Fatalf("claude-code worker = %q, want reviewer claude-code", got)
	}
	if got := (ProjectConfig{}).ResolveReviewerHarness(HarnessCodex); got != ReviewerCodex {
		t.Fatalf("codex worker = %q, want reviewer codex", got)
	}
	if got := (ProjectConfig{}).ResolveReviewerHarness(HarnessOpenCode); got != ReviewerOpenCode {
		t.Fatalf("opencode worker = %q, want reviewer opencode", got)
	}
	if got := (ProjectConfig{}).ResolveReviewerHarness(HarnessMuse); got != ReviewerMuse {
		t.Fatalf("muse worker = %q, want reviewer muse", got)
	}
	if got := (ProjectConfig{}).ResolveReviewerHarness(HarnessKimchi); got != ReviewerKimchi {
		t.Fatalf("kimchi worker = %q, want reviewer kimchi", got)
	}

	// A worker harness that is not itself a reviewer (e.g. crush, aider) falls
	// back to claude-code.
	if got := (ProjectConfig{}).ResolveReviewerHarness(HarnessCrush); got != FallbackReviewerHarness {
		t.Fatalf("crush worker = %q, want %q", got, FallbackReviewerHarness)
	}
	for _, worker := range []AgentHarness{
		HarnessCopilot, HarnessCursor, HarnessKilocode, HarnessKiro, HarnessPi,
		HarnessAider, HarnessAmp, HarnessQwen, HarnessAgy, HarnessContinue,
		HarnessGoose, HarnessVibe, HarnessDevin, HarnessDroid, HarnessKimi,
		HarnessGrok, HarnessCrush, HarnessAuggie, HarnessCline, HarnessAutohand,
	} {
		if got := (ProjectConfig{}).ResolveReviewerHarness(worker); got != FallbackReviewerHarness {
			t.Errorf("%s worker = %q, want explicit-selection fallback %q", worker, got, FallbackReviewerHarness)
		}
	}
}

func TestProjectConfigIsZero(t *testing.T) {
	if !(ProjectConfig{}).IsZero() {
		t.Fatal("empty config should be zero")
	}
	if (ProjectConfig{DefaultBranch: "main"}).IsZero() {
		t.Fatal("populated config should not be zero")
	}
	if (ProjectConfig{Env: map[string]string{"A": "b"}}).IsZero() {
		t.Fatal("config with env should not be zero")
	}
	if (ProjectConfig{AutoReview: true}).IsZero() {
		t.Fatal("config with autoReview enabled should not be zero")
	}
}
