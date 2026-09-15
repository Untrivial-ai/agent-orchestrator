package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

type projectAddOptions struct {
	path              string
	id                string
	name              string
	workerAgent       string
	orchestratorAgent string
	asWorkspace       bool
}

type projectListOptions struct {
	json bool
}

type projectGetOptions struct {
	json bool
}

type projectRemoveOptions struct {
	json bool
	yes  bool
}

// addProjectRequest mirrors the daemon's project AddInput body for
// POST /api/v1/projects. projectId and name are optional (pointers omit them).
type addProjectRequest struct {
	Path        string         `json:"path"`
	ProjectID   *string        `json:"projectId,omitempty"`
	Name        *string        `json:"name,omitempty"`
	Config      *projectConfig `json:"config,omitempty"`
	AsWorkspace bool           `json:"asWorkspace,omitempty"`
}

type projectSummary struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	SessionPrefix string `json:"sessionPrefix"`
	ResolveError  string `json:"resolveError,omitempty"`
}

type projectDetails struct {
	ID             string                 `json:"id"`
	Name           string                 `json:"name"`
	Kind           string                 `json:"kind"`
	Path           string                 `json:"path"`
	Repo           string                 `json:"repo"`
	DefaultBranch  string                 `json:"defaultBranch"`
	Agent          string                 `json:"agent,omitempty"`
	Config         *projectConfig         `json:"config,omitempty"`
	WorkspaceRepos []workspaceRepoDetails `json:"workspaceRepos,omitempty"`
	ResolveError   string                 `json:"resolveError,omitempty"`
}

type workspaceRepoDetails struct {
	Name         string `json:"name"`
	RelativePath string `json:"relativePath"`
	Repo         string `json:"repo"`
}

// agentConfig mirrors the daemon's typed domain.AgentConfig for the CLI client.
type agentConfig struct {
	Model       string `json:"model,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Permissions string `json:"permissions,omitempty"`
}

// roleOverride mirrors domain.RoleOverride.
type roleOverride struct {
	Agent       string      `json:"agent,omitempty"`
	AgentConfig agentConfig `json:"agentConfig,omitempty"`
}

// trackerIntakeConfig mirrors domain.TrackerIntakeConfig.
type trackerIntakeConfig struct {
	Enabled  bool   `json:"enabled,omitempty"`
	Provider string `json:"provider,omitempty"`
	Repo     string `json:"repo,omitempty"`
	Assignee string `json:"assignee,omitempty"`
}

// reviewerConfig mirrors domain.ReviewerConfig.
type reviewerConfig struct {
	Harness     string       `json:"harness"`
	AgentConfig *agentConfig `json:"agentConfig,omitempty"`
}

type containerReapConfig struct {
	Disabled bool `json:"disabled,omitempty"`
}

// projectConfig mirrors the daemon's typed domain.ProjectConfig for the CLI
// client. The CLI sets common fields via flags and the whole object via
// --config-json.
type projectConfig struct {
	ContainerReap     *containerReapConfig `json:"containerReap,omitempty"`
	CanonicalRepoURL  string               `json:"canonicalRepoURL,omitempty"`
	DefaultBranch     string               `json:"defaultBranch,omitempty"`
	SessionPrefix     string               `json:"sessionPrefix,omitempty"`
	Env               map[string]string    `json:"env,omitempty"`
	Symlinks          []string             `json:"symlinks,omitempty"`
	PostCreate        []string             `json:"postCreate,omitempty"`
	AgentRules        string               `json:"agentRules,omitempty"`
	AgentRulesFile    string               `json:"agentRulesFile,omitempty"`
	OrchestratorRules string               `json:"orchestratorRules,omitempty"`
	AgentConfig       agentConfig          `json:"agentConfig,omitempty"`
	Worker            roleOverride         `json:"worker,omitempty"`
	Orchestrator      roleOverride         `json:"orchestrator,omitempty"`
	TrackerIntake     trackerIntakeConfig  `json:"trackerIntake,omitempty"`
	AutoReview        bool                 `json:"autoReview,omitempty"`
	Reviewers         []reviewerConfig     `json:"reviewers,omitempty"`
}

// setConfigRequest mirrors the daemon's SetConfigInput body for
// PUT /api/v1/projects/{id}/config.
type setConfigRequest struct {
	Config projectConfig `json:"config"`
}

type projectSetConfigOptions struct {
	canonicalRepoURL  string
	defaultBranch     string
	sessionPrefix     string
	model             string
	permission        string
	workerAgent       string
	orchestratorAgent string
	agentRules        string
	agentRulesFile    string
	orchestratorRules string
	env               []string
	symlink           []string
	postCreate        []string
	trackerIntake     bool
	trackerRepo       string
	trackerAssignee   string
	reviewers         []string
	configJSON        string
	clear             bool
	json              bool
}

type projectListResult struct {
	Projects []projectSummary `json:"projects"`
}

type projectGetResult struct {
	Status  string         `json:"status"`
	Project projectDetails `json:"project"`
}

type projectResult struct {
	Project projectDetails `json:"project"`
}

type projectRemoveResult struct {
	OK                bool   `json:"ok,omitempty"`
	ID                string `json:"id,omitempty"`
	ProjectID         string `json:"projectId,omitempty"`
	RemovedStorageDir *bool  `json:"removedStorageDir,omitempty"`
}

func newProjectCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage projects",
	}
	cmd.AddCommand(newProjectListCommand(ctx))
	cmd.AddCommand(newProjectGetCommand(ctx))
	cmd.AddCommand(newProjectAddCommand(ctx))
	cmd.AddCommand(newProjectSetConfigCommand(ctx))
	cmd.AddCommand(newProjectRemoveCommand(ctx))
	return cmd
}

func newProjectListCommand(ctx *commandContext) *cobra.Command {
	var opts projectListOptions
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List registered projects",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var res projectListResult
			if err := ctx.getJSON(cmd.Context(), "projects", &res); err != nil {
				return err
			}
			sort.Slice(res.Projects, func(i, j int) bool {
				return res.Projects[i].ID < res.Projects[j].ID
			})
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			return writeProjectList(cmd, res.Projects)
		},
	}
	cmd.Flags().BoolVar(&opts.json, "json", false, "Output projects as JSON")
	return cmd
}

func newProjectGetCommand(ctx *commandContext) *cobra.Command {
	var opts projectGetOptions
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Fetch one registered project",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return usageError{err}
			}
			if strings.TrimSpace(args[0]) == "" {
				return usageError{errors.New("usage: project id is required")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(args[0])
			var res projectGetResult
			if err := ctx.getJSON(cmd.Context(), "projects/"+url.PathEscape(id), &res); err != nil {
				return err
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			return writeProjectDetails(cmd, res)
		},
	}
	cmd.Flags().BoolVar(&opts.json, "json", false, "Output project as JSON")
	return cmd
}

func newProjectAddCommand(ctx *commandContext) *cobra.Command {
	var opts projectAddOptions
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Register a local git repo as a project",
		Long: "Register a local git repo as a project so sessions can be spawned in it.\n\n" +
			"The path must be an existing git repository on disk. With --as-workspace, " +
			"the path may be a parent folder containing direct child git repositories; " +
			"AO initializes/adopts the parent as the root repo and gitignores children.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.path == "" {
				return usageError{fmt.Errorf("--path is required")}
			}
			req := addProjectRequest{Path: opts.path, AsWorkspace: opts.asWorkspace}
			if opts.id != "" {
				req.ProjectID = &opts.id
			}
			if opts.name != "" {
				req.Name = &opts.name
			}
			if opts.workerAgent != "" || opts.orchestratorAgent != "" {
				req.Config = &projectConfig{
					Worker:       roleOverride{Agent: opts.workerAgent},
					Orchestrator: roleOverride{Agent: opts.orchestratorAgent},
				}
			}
			var res projectResult
			if err := ctx.postJSON(cmd.Context(), "projects", req, &res); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "registered project %s at %s\n", res.Project.ID, res.Project.Path)
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.path, "path", "", "Absolute path to the local git repo (required)")
	f.StringVar(&opts.id, "id", "", "Project id (default: derived by the daemon from the path)")
	f.StringVar(&opts.name, "name", "", "Display name")
	f.StringVar(&opts.workerAgent, "worker-agent", "", "Default worker session agent")
	f.StringVar(&opts.orchestratorAgent, "orchestrator-agent", "", "Default orchestrator session agent")
	f.BoolVar(&opts.asWorkspace, "as-workspace", false, "Register a parent folder as a workspace project (root-as-repo plus direct child repos)")
	return cmd
}

func newProjectSetConfigCommand(ctx *commandContext) *cobra.Command {
	var opts projectSetConfigOptions
	cmd := &cobra.Command{
		Use:   "set-config <id>",
		Short: "Set the per-project config",
		Long: "Replace a project's per-project config (branch, session prefix, env, " +
			"symlinks, post-create, rules, agent model/permissions, role overrides, tracker intake, reviewers). The config " +
			"is resolved when a session spawns.\n\n" +
			"Set fields via flags, pass the whole object with --config-json, or --clear " +
			"to remove all config.",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return usageError{err}
			}
			if strings.TrimSpace(args[0]) == "" {
				return usageError{errors.New("usage: project id is required")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(args[0])
			config, err := buildProjectConfig(opts)
			if err != nil {
				return err
			}
			req := setConfigRequest{Config: config}
			var res projectResult
			if err := ctx.putJSON(cmd.Context(), "projects/"+url.PathEscape(id)+"/config", req, &res); err != nil {
				return err
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "updated config for project %s\n", res.Project.ID)
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.defaultBranch, "default-branch", "", "Base branch for new worktrees; auto infers each repository's Git default")
	f.StringVar(&opts.canonicalRepoURL, "canonical-repo-url", "", "Explicit upstream HTTPS repository URL for PR claims (same provider, host, and port as origin)")
	f.StringVar(&opts.sessionPrefix, "session-prefix", "", "Displayed session-id prefix")
	f.StringVar(&opts.model, "model", "", "Agent model override (e.g. claude-opus-4-5)")
	f.StringVar(&opts.permission, "permission", "", "Permission mode: default, accept-edits, auto, bypass-permissions")
	f.StringVar(&opts.workerAgent, "worker-agent", "", "Harness override for worker sessions")
	f.StringVar(&opts.orchestratorAgent, "orchestrator-agent", "", "Harness override for orchestrator sessions")
	f.StringVar(&opts.agentRules, "agent-rules", "", "Project-specific standing instructions for worker sessions")
	f.StringVar(&opts.agentRulesFile, "agent-rules-file", "", "Repo-relative file containing worker standing instructions")
	f.StringVar(&opts.orchestratorRules, "orchestrator-rules", "", "Project-specific standing instructions for orchestrator sessions")
	f.StringArrayVar(&opts.env, "env", nil, "Env var KEY=VALUE forwarded into sessions (repeatable)")
	f.StringArrayVar(&opts.symlink, "symlink", nil, "Repo-relative path to symlink into workspaces (repeatable)")
	f.StringArrayVar(&opts.postCreate, "post-create", nil, "Command to run after workspace creation (repeatable)")
	f.BoolVar(&opts.trackerIntake, "tracker-intake", false, "Enable issue intake for matching issues (GitHub or GitLab; provider inferred from git origin)")
	f.StringVar(&opts.trackerRepo, "tracker-repo", "", "Provider-native repo for issue intake (owner/repo or group/subgroup/repo; default: derive from git origin)")
	f.StringVar(&opts.trackerAssignee, "tracker-assignee", "", "Issue assignee required for intake eligibility")
	f.StringArrayVar(&opts.reviewers, "reviewer", nil, "Reviewer harness that reviews worker PRs (repeatable; e.g. claude-code)")
	f.StringVar(&opts.configJSON, "config-json", "", "Full config as a JSON object (overrides field flags)")
	f.BoolVar(&opts.clear, "clear", false, "Clear all config")
	f.BoolVar(&opts.json, "json", false, "Output the updated project as JSON")
	return cmd
}

// buildProjectConfig turns the set-config flags into the typed config sent to
// the daemon. --clear empties the config; --config-json supplies the whole
// object; otherwise the field flags form the config. The daemon validates the
// values.
func buildProjectConfig(opts projectSetConfigOptions) (projectConfig, error) {
	if opts.clear {
		return projectConfig{}, nil
	}
	if opts.configJSON != "" {
		var cfg projectConfig
		if err := json.Unmarshal([]byte(opts.configJSON), &cfg); err != nil {
			return projectConfig{}, usageError{fmt.Errorf("--config-json is not a valid JSON object: %w", err)}
		}
		return cfg, nil
	}

	env, err := parseEnvPairs(opts.env)
	if err != nil {
		return projectConfig{}, err
	}
	cfg := projectConfig{
		CanonicalRepoURL:  opts.canonicalRepoURL,
		DefaultBranch:     opts.defaultBranch,
		SessionPrefix:     opts.sessionPrefix,
		Env:               env,
		Symlinks:          opts.symlink,
		PostCreate:        opts.postCreate,
		AgentRules:        opts.agentRules,
		AgentRulesFile:    opts.agentRulesFile,
		OrchestratorRules: opts.orchestratorRules,
		AgentConfig:       agentConfig{Model: opts.model, Permissions: opts.permission},
		Worker:            roleOverride{Agent: opts.workerAgent},
		Orchestrator:      roleOverride{Agent: opts.orchestratorAgent},
		TrackerIntake: trackerIntakeConfig{
			Enabled:  opts.trackerIntake,
			Repo:     opts.trackerRepo,
			Assignee: opts.trackerAssignee,
		},
		Reviewers: reviewersForFlags(opts.reviewers),
	}
	if reflect.DeepEqual(cfg, projectConfig{}) {
		return projectConfig{}, usageError{errors.New("usage: provide at least one config flag, --config-json, or --clear")}
	}
	return cfg, nil
}

// reviewersForFlags turns repeated --reviewer harness values into the config
// shape. The daemon validates the harness vocabulary.
func reviewersForFlags(harnesses []string) []reviewerConfig {
	if len(harnesses) == 0 {
		return nil
	}
	reviewers := make([]reviewerConfig, 0, len(harnesses))
	for _, harness := range harnesses {
		harness = strings.TrimSpace(harness)
		if harness == "" {
			continue
		}
		reviewers = append(reviewers, reviewerConfig{Harness: harness})
	}
	if len(reviewers) == 0 {
		return nil
	}
	return reviewers
}

// parseEnvPairs turns repeated KEY=VALUE flags into a map.
func parseEnvPairs(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	env := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, usageError{fmt.Errorf("invalid --env %q: expected KEY=VALUE", pair)}
		}
		env[key] = value
	}
	return env, nil
}

func newProjectRemoveCommand(ctx *commandContext) *cobra.Command {
	var opts projectRemoveOptions
	cmd := &cobra.Command{
		Use:     "rm <id>",
		Aliases: []string{"remove", "delete"},
		Short:   "Remove a registered project",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return usageError{err}
			}
			if strings.TrimSpace(args[0]) == "" {
				return usageError{errors.New("usage: project id is required")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(args[0])
			if !opts.yes {
				confirmed, err := confirmProjectRemoval(cmd, id)
				if err != nil {
					return err
				}
				if !confirmed {
					_, err := fmt.Fprintln(cmd.OutOrStdout(), "aborted")
					return err
				}
			}
			var res projectRemoveResult
			if err := ctx.deleteJSON(cmd.Context(), "projects/"+url.PathEscape(id), &res); err != nil {
				return err
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			removedID := res.ProjectID
			if removedID == "" {
				removedID = res.ID
			}
			if removedID == "" {
				removedID = id
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "removed project %s\n", removedID)
			return err
		},
	}
	cmd.Flags().BoolVarP(&opts.yes, "yes", "y", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&opts.json, "json", false, "Output removal result as JSON")
	return cmd
}

func writeProjectList(cmd *cobra.Command, projects []projectSummary) error {
	out := cmd.OutOrStdout()
	if len(projects) == 0 {
		if _, err := fmt.Fprintln(out, "No projects registered."); err != nil {
			return err
		}
		_, err := fmt.Fprintln(out, "Run `ao project add --path <path>` to register one.")
		return err
	}

	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tNAME\tKIND\tSESSION PREFIX\tSTATUS"); err != nil {
		return err
	}
	for _, p := range projects {
		status := "ok"
		if p.ResolveError != "" {
			status = "degraded: " + p.ResolveError
		}
		kind := p.Kind
		if kind == "" {
			kind = "single_repo"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.ID, p.Name, kind, p.SessionPrefix, status); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func writeProjectDetails(cmd *cobra.Command, res projectGetResult) error {
	out := cmd.OutOrStdout()
	p := res.Project
	if _, err := fmt.Fprintf(out, "Project %s (%s)\n", p.ID, res.Status); err != nil {
		return err
	}
	fields := []struct {
		label string
		value string
	}{
		{label: "name", value: p.Name},
		{label: "kind", value: p.Kind},
		{label: "path", value: p.Path},
		{label: "repo", value: p.Repo},
		{label: "default branch", value: p.DefaultBranch},
		{label: "agent", value: p.Agent},
		{label: "config", value: formatProjectConfig(p.Config)},
		{label: "resolve error", value: p.ResolveError},
	}
	for _, f := range fields {
		if f.value == "" {
			continue
		}
		if _, err := fmt.Fprintf(out, "  %s: %s\n", f.label, f.value); err != nil {
			return err
		}
	}
	if len(p.WorkspaceRepos) > 0 {
		if _, err := fmt.Fprintln(out, "  workspace repos:"); err != nil {
			return err
		}
		for _, repo := range p.WorkspaceRepos {
			desc := repo.RelativePath
			if repo.Repo != "" {
				desc += " (" + repo.Repo + ")"
			}
			if _, err := fmt.Fprintf(out, "    %s: %s\n", repo.Name, desc); err != nil {
				return err
			}
		}
	}
	return nil
}

// formatProjectConfig renders the per-project config as compact JSON for the
// `project get` text view. A nil config returns "" so the row is skipped.
func formatProjectConfig(config *projectConfig) string {
	if config == nil {
		return ""
	}
	data, err := json.Marshal(config)
	if err != nil {
		return ""
	}
	return string(data)
}

func confirmProjectRemoval(cmd *cobra.Command, id string) (bool, error) {
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Remove project %q? Type the project id to confirm: ", id); err != nil {
		return false, err
	}
	reader := bufio.NewReader(cmd.InOrStdin())
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return false, err
	}
	return strings.TrimSpace(line) == id, nil
}
