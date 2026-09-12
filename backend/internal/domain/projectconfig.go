package domain

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
)

// ProjectConfig is the typed per-project configuration — the SQLite twin of the
// legacy agent-orchestrator.yaml `projects.<id>` block. It is persisted as one
// JSON blob per project and resolved at spawn. Each field is typed and
// validated; there is no free-form map.
//
// Only fields with a live consumer are modeled: DefaultBranch, Env, Symlinks,
// PostCreate, AgentConfig, prompt rules, and the role overrides are consumed at
// spawn; SessionPrefix feeds the display prefix. Settings whose consumers do not
// yet exist (tracker/SCM per-project config) are intentionally absent and land in
// focused follow-up PRs alongside the code that reads them.
type ProjectConfig struct {
	// CanonicalRepoURL explicitly trusts one upstream repository for PR claims.
	// Numeric PR references use this repository when set; checkout/push stays on origin.
	CanonicalRepoURL string `json:"canonicalRepoURL,omitempty"`
	// DefaultBranch is the base branch new session worktrees are created from.
	// Empty and DefaultBranchAuto both mean infer each repository's Git default.
	DefaultBranch string `json:"defaultBranch,omitempty"`
	// SessionPrefix overrides the displayed session-id prefix.
	SessionPrefix string `json:"sessionPrefix,omitempty"`

	// Env are extra environment variables forwarded into worker session
	// runtimes. AO-internal vars (AO_SESSION, AO_PROJECT_ID, …) always win.
	Env map[string]string `json:"env,omitempty"`
	// Symlinks are repo-relative paths symlinked into each session workspace.
	Symlinks []string `json:"symlinks,omitempty"`
	// PostCreate are shell commands run in the workspace after it is created.
	PostCreate []string `json:"postCreate,omitempty"`

	// AgentRules are project-specific standing instructions for worker sessions.
	AgentRules string `json:"agentRules,omitempty"`
	// AgentRulesFile is a repo-relative Markdown/text file whose contents are
	// appended to AgentRules for worker sessions.
	AgentRulesFile string `json:"agentRulesFile,omitempty"`
	// OrchestratorRules are project-specific standing instructions for
	// orchestrator sessions.
	OrchestratorRules string `json:"orchestratorRules,omitempty"`

	// AgentConfig is the default agent config for the project.
	AgentConfig AgentConfig `json:"agentConfig,omitempty"`
	// Worker and Orchestrator are role-specific harness/agent-config overrides.
	Worker       RoleOverride `json:"worker,omitempty"`
	Orchestrator RoleOverride `json:"orchestrator,omitempty"`

	// Reviewers names the agent(s) that review a worker's PR when a review is
	// triggered. It is configured independently of the Worker override; an empty
	// list falls back to claude-code (see ResolveReviewerHarness).
	Reviewers []ReviewerConfig `json:"reviewers,omitempty"`
	// TrackerIntake controls issue-driven worker spawning. It is opt-in and
	// read-only toward the tracker in v1: matching issues spawn sessions, but the
	// tracker is not commented on or transitioned.
	TrackerIntake TrackerIntakeConfig `json:"trackerIntake,omitempty"`

	// ContainerReap controls whether AO reaps a worker session's ao.session-
	// labeled Docker containers on terminal state / kill. Enabled by default;
	// set Disabled to opt a project out entirely. Per-container sparing uses
	// the ao.spare=true label instead (see dockerreap.SpareLabel) so the
	// opt-out travels with the container at `docker run` time rather than
	// drifting out of sync with a project-config list.
	ContainerReap ContainerReapConfig `json:"containerReap,omitempty"`

	// AutoReview controls whether new worker sessions spawned for this project
	// have automatic PR review enabled by default. The default (false) leaves
	// sessions with auto-review off; enabling it copies the setting into each
	// new session at spawn time. Users can still override the per-session toggle
	// after spawn.
	AutoReview bool `json:"autoReview,omitempty"`
}

// ContainerReapConfig is the project-level opt-out for #2652's Docker
// container reaping on session terminal state.
type ContainerReapConfig struct {
	// Disabled turns off container reaping for every session in this project.
	// Per-container sparing (ao.spare=true) is unaffected either way.
	Disabled bool `json:"disabled,omitempty"`
}

// ReviewerConfig names one reviewer agent by harness. The harness is drawn from
// the reviewer vocabulary (ReviewerHarness), which is distinct from the worker
// AgentHarness set.
type ReviewerConfig struct {
	Harness     ReviewerHarness `json:"harness"`
	AgentConfig AgentConfig     `json:"agentConfig,omitempty"`
}

// FallbackReviewerHarness is the reviewer used when a project configures none
// and the worker's harness is not itself a supported reviewer.
const FallbackReviewerHarness = ReviewerClaudeCode

// ResolveReviewerHarness picks the reviewer harness for a worker. A configured
// reviewer wins. Otherwise only the original, unattended-safe reviewer set is
// inherited from the worker. Every other reviewer requires explicit selection,
// so adding an experimental adapter never silently changes an existing project.
func (c ProjectConfig) ResolveReviewerHarness(worker AgentHarness) ReviewerHarness {
	if len(c.Reviewers) > 0 {
		return c.Reviewers[0].Harness
	}
	switch worker {
	case HarnessClaudeCode:
		return ReviewerClaudeCode
	case HarnessCodex:
		return ReviewerCodex
	case HarnessOpenCode:
		return ReviewerOpenCode
	case HarnessMuse:
		return ReviewerMuse
	case HarnessKimchi:
		return ReviewerKimchi
	}
	return FallbackReviewerHarness
}

// RoleOverride overrides the harness and/or agent config for a session role.
type RoleOverride struct {
	Harness     AgentHarness `json:"agent,omitempty"`
	AgentConfig AgentConfig  `json:"agentConfig,omitempty"`
}

const (
	// DefaultBranchAuto tells callers to infer the Git default branch for each
	// repository instead of naming one branch for the whole project.
	DefaultBranchAuto = "auto"
	// DefaultBranchName is the branch AO selects when it creates a repository.
	// Automatic resolution never uses it as a guess for existing repositories.
	DefaultBranchName = "main"
)

// DefaultProjectConfig returns the config a project has when it sets nothing:
// automatic per-repository branch resolution. Every other field defaults to
// its zero value (no env/symlinks/post-create, agent + role defaults).
func DefaultProjectConfig() ProjectConfig {
	return ProjectConfig{
		DefaultBranch: DefaultBranchAuto,
	}
}

// WithDefaults overlays DefaultProjectConfig onto c, filling only fields the
// project left unset. A set field is always preserved.
func (c ProjectConfig) WithDefaults() ProjectConfig {
	def := DefaultProjectConfig()
	if c.DefaultBranch == "" {
		c.DefaultBranch = def.DefaultBranch
	}
	c.TrackerIntake = c.TrackerIntake.WithDefaults()
	return c
}

// WorktreeBaseBranch translates project configuration into the workspace
// interface. An empty value tells the workspace adapter to resolve a remote
// HEAD independently for the repository it is materializing.
func (c ProjectConfig) WorktreeBaseBranch() string {
	branch := c.WithDefaults().DefaultBranch
	if branch == DefaultBranchAuto {
		return ""
	}
	return branch
}

// IsZero reports whether the config carries no settings, so storage can persist
// SQL NULL and resolution can skip an empty config.
func (c ProjectConfig) IsZero() bool {
	return reflect.DeepEqual(c, ProjectConfig{})
}

// Validate rejects values outside the typed vocabulary so a bad config is
// refused when it is set (CLI/API) rather than surfacing at spawn.
func (c ProjectConfig) Validate() error {
	if c.CanonicalRepoURL != "" {
		if err := c.ValidateCanonicalRepository(c.CanonicalRepoURL); err != nil {
			return err
		}
	}
	if err := c.AgentConfig.Validate(); err != nil {
		return err
	}
	if err := validateNameComponent("sessionPrefix", c.SessionPrefix); err != nil {
		return err
	}
	for role, ro := range map[string]RoleOverride{"worker": c.Worker, "orchestrator": c.Orchestrator} {
		if ro.Harness != "" && !ro.Harness.IsKnown() {
			return fmt.Errorf("%s.agent: unknown harness %q", role, ro.Harness)
		}
		if err := ro.AgentConfig.Validate(); err != nil {
			return fmt.Errorf("%s.%w", role, err)
		}
	}
	for _, s := range c.Symlinks {
		if err := validateRepoRelative(s); err != nil {
			return fmt.Errorf("symlink %q: %w", s, err)
		}
	}
	if err := validateRepoRelative(c.AgentRulesFile); err != nil {
		return fmt.Errorf("agentRulesFile %q: %w", c.AgentRulesFile, err)
	}
	for i, rv := range c.Reviewers {
		if !rv.Harness.IsKnown() {
			return fmt.Errorf("reviewers[%d].harness: unknown harness %q", i, rv.Harness)
		}
		if err := rv.AgentConfig.Validate(); err != nil {
			return fmt.Errorf("reviewers[%d].%w", i, err)
		}
	}
	if err := c.TrackerIntake.Validate(); err != nil {
		return err
	}
	return nil
}

func validateNoWhitespaceField(name, value string) error {
	if value == "" {
		return nil
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s: must not have leading or trailing whitespace", name)
	}
	return nil
}

func validateNameComponent(name, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if strings.ContainsAny(trimmed, `/\`) || trimmed == "." || trimmed == ".." {
		return fmt.Errorf("%s: must not contain path separators or traversal components", name)
	}
	return nil
}

// validateRepoRelative refuses paths that would let a project config escape
// its repo root: absolute paths and any ".." segment (before or after Clean).
// The same guard runs at spawn time as defense-in-depth, but enforcing it here
// rejects bad config when it is set rather than at every later spawn.
func validateRepoRelative(p string) error {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return nil
	}
	if filepath.IsAbs(trimmed) || strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, `\`) {
		return fmt.Errorf("path must be repo-relative and must not escape the project root")
	}
	clean := filepath.Clean(trimmed)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path must be repo-relative and must not escape the project root")
	}
	for _, seg := range strings.Split(filepath.ToSlash(clean), "/") {
		if seg == ".." {
			return fmt.Errorf("path must be repo-relative and must not escape the project root")
		}
	}
	return nil
}
