package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// maxDisplayNameLen caps the sidebar label set by `--name`. Mirrored by the
// daemon's spawn handler so a direct API call is held to the same limit.
const maxDisplayNameLen = 20

type spawnOptions struct {
	project        string
	harness        string
	kind           string
	mode           string
	branch         string
	prompt         string
	issue          string
	name           string
	claimPR        string
	noTakeover     bool
	skipAgentCheck bool
}

// spawnRequest mirrors the daemon's SpawnSessionRequest body for
// POST /api/v1/sessions. The CLI keeps its own copy so it need not import httpd.
type spawnRequest struct {
	ProjectID   string `json:"projectId"`
	IssueID     string `json:"issueId,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Harness     string `json:"harness,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	DisplayName string `json:"displayName"`
	// ParentOrchestratorID carries the caller's own AO_SESSION_ID so the daemon
	// can attribute this spawn to its orchestrator when `ao spawn` runs from
	// inside an orchestrator's own agent process. The daemon verifies the id
	// actually names a live orchestrator session before recording it; it is
	// empty (and ignored) for a plain user/CLI spawn run outside any session.
	ParentOrchestratorID string `json:"parentOrchestratorId,omitempty"`
}

type spawnResult struct {
	Session struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"session"`
	PromptBytes       int `json:"promptBytes,omitempty"`
	SystemPromptBytes int `json:"systemPromptBytes,omitempty"`
}

type agentProbeResult struct {
	Agent     agentInfo `json:"agent"`
	Supported bool      `json:"supported"`
	Installed bool      `json:"installed"`
}

func newSpawnCommand(ctx *commandContext) *cobra.Command {
	var opts spawnOptions
	cmd := &cobra.Command{
		Use:   "spawn",
		Short: "Spawn an agent session in a registered project",
		Long: "Spawn an agent session (worker or orchestrator) in a registered project.\n\n" +
			"The session runs the chosen agent in a\n" +
			"fresh isolated workspace. Git projects use worktrees; Scratch uses an AO-managed directory.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.noTakeover && opts.claimPR == "" {
				return usageError{fmt.Errorf("--no-takeover requires --claim-pr")}
			}
			name := strings.TrimSpace(opts.name)
			if name == "" {
				return usageError{fmt.Errorf("--name is required")}
			}
			if utf8.RuneCountInString(name) > maxDisplayNameLen {
				return usageError{fmt.Errorf("--name must be %d characters or fewer", maxDisplayNameLen)}
			}

			// Rejected here rather than forwarded, so a typo exits 2 as a usage
			// error instead of reaching the daemon as an unsupported mode.
			if opts.mode != "" && opts.mode != "chat" && opts.mode != "tui" {
				return usageError{fmt.Errorf(`--mode must be "chat" or "tui"`)}
			}
			if opts.kind != "" && opts.kind != "worker" && opts.kind != "orchestrator" {
				return usageError{fmt.Errorf(`--kind must be "worker" or "orchestrator"`)}
			}

			project, err := ctx.resolveSpawnProject(cmd.Context(), opts.project)
			if err != nil {
				return err
			}
			opts.project = project.ID

			harness, err := resolveSpawnHarness(opts.harness, opts.kind, project)
			if err != nil {
				return err
			}
			opts.harness = harness

			if isScratchProject(project) {
				if strings.TrimSpace(opts.branch) != "" {
					return usageError{fmt.Errorf("scratch projects do not support --branch")}
				}
				if strings.TrimSpace(opts.claimPR) != "" {
					return usageError{fmt.Errorf("scratch projects do not support --claim-pr")}
				}
			}

			if !opts.skipAgentCheck {
				if err := ctx.preflightSpawnAgentAuth(cmd.Context(), cmd, opts.harness); err != nil {
					return err
				}
			}
			claimRef := ""
			if opts.claimPR != "" {
				claimRef, err = ctx.resolvePRRef(cmd.Context(), opts.claimPR, project)
				if err != nil {
					return err
				}
			}
			req := spawnRequest{
				ProjectID:            opts.project,
				IssueID:              opts.issue,
				Kind:                 opts.kind,
				Harness:              opts.harness,
				Mode:                 opts.mode,
				Branch:               opts.branch,
				Prompt:               opts.prompt,
				DisplayName:          name,
				ParentOrchestratorID: strings.TrimSpace(os.Getenv("AO_SESSION_ID")),
			}
			var res spawnResult
			if err := ctx.postJSON(cmd.Context(), "sessions", req, &res); err != nil {
				return err
			}
			claimed := ""
			if opts.claimPR != "" {
				var claim claimPRResponse
				if err := ctx.postJSON(cmd.Context(), "sessions/"+url.PathEscape(res.Session.ID)+"/pr/claim", claimPRRequest{PR: claimRef, AllowTakeover: !opts.noTakeover}, &claim); err != nil {
					if killErr := ctx.rollbackSpawnedSession(cmd.Context(), res.Session.ID); killErr != nil {
						return fmt.Errorf("failed to claim PR %s: %w; rollback of session %s failed: %w", opts.claimPR, err, res.Session.ID, killErr)
					}
					return fmt.Errorf("failed to claim PR %s: %w; rolled back session %s", opts.claimPR, err, res.Session.ID)
				}
				if len(claim.PRs) > 0 {
					claimed = claim.PRs[0].URL
				}
			}
			out := cmd.OutOrStdout()
			claimLabel := ""
			if claimed != "" {
				claimLabel = fmt.Sprintf(" (claimed %s)", claimed)
			}
			promptSize := ""
			if res.PromptBytes > 0 || res.SystemPromptBytes > 0 {
				promptSize = fmt.Sprintf(" [prompt %d B, system %d B]", res.PromptBytes, res.SystemPromptBytes)
			}
			_, err = fmt.Fprintf(out, "spawned session %s (%s)%s%s\n", res.Session.ID, res.Session.Status, claimLabel, promptSize)
			return err
		},
	}
	f := cmd.Flags()
	// --agent is an alias for --harness so the more intuitive `ao spawn --agent
	// droid` works identically; both resolve to the same harness flag.
	f.SetNormalizeFunc(func(_ *pflag.FlagSet, name string) pflag.NormalizedName {
		if name == "agent" {
			name = "harness"
		}
		return pflag.NormalizedName(name)
	})
	f.StringVar(&opts.project, "project", "", "Project id to spawn the session in (default: AO_PROJECT_ID, current registered repo, or Scratch when it is the only project)")
	f.StringVar(&opts.harness, "harness", "", "Agent harness / --agent: claude-code, codex, aider, opencode, grok, droid, amp, agy, crush, cursor, qwen, copilot, goose, auggie, continue, devin, cline, kimi, muse, kiro, kilocode, vibe, pi, kimchi, prime-agent, autohand (default: project worker.agent; orchestrator spawns default to project orchestrator.agent; required if the project has none)")
	f.StringVar(&opts.kind, "kind", "", "Session role: worker or orchestrator (default: worker)")
	f.StringVar(&opts.mode, "mode", "", "Initial session interface: chat (structured agent connection) or tui (the agent's native terminal). Omitted uses the daemon default; compatible sessions can switch later.")
	f.StringVar(&opts.branch, "branch", "", "Branch for git project sessions (default: ao/<session-id>/root; unsupported for Scratch)")
	f.StringVar(&opts.prompt, "prompt", "", "Initial prompt for the agent")
	f.StringVar(&opts.issue, "issue", "", "Issue id to associate with the session")
	f.StringVar(&opts.name, "name", "", "Display name shown in the sidebar (required, max 20 characters)")
	f.StringVar(&opts.claimPR, "claim-pr", "", "Immediately claim an existing PR for the spawned session")
	f.BoolVar(&opts.noTakeover, "no-takeover", false, "Refuse if another active session owns the claimed PR (requires --claim-pr)")
	f.BoolVar(&opts.skipAgentCheck, "skip-agent-check", false, "Skip advisory agent catalog install/auth preflight before spawning")
	return cmd
}

func (c *commandContext) fetchAgentInventory(ctx context.Context, refresh bool) (agentInventory, error) {
	var inv agentInventory
	if refresh {
		if err := c.postJSON(ctx, "agents/refresh", struct{}{}, &inv); err != nil {
			return agentInventory{}, err
		}
		return inv, nil
	}
	if err := c.getJSON(ctx, "agents", &inv); err != nil {
		return agentInventory{}, err
	}
	return inv, nil
}

func (c *commandContext) resolveSpawnProject(ctx context.Context, explicit string) (projectDetails, error) {
	if id := strings.TrimSpace(explicit); id != "" {
		return c.fetchProjectDetails(ctx, id)
	}
	if id := strings.TrimSpace(os.Getenv("AO_PROJECT_ID")); id != "" {
		return c.fetchProjectDetails(ctx, id)
	}
	if sessionID := strings.TrimSpace(os.Getenv("AO_SESSION_ID")); sessionID != "" {
		project, err := c.resolveProjectFromSession(ctx, sessionID)
		if err != nil {
			return projectDetails{}, err
		}
		return project, nil
	}
	project, ok, err := c.resolveProjectFromCWD(ctx)
	if err != nil {
		return projectDetails{}, err
	}
	if ok {
		return project, nil
	}
	return projectDetails{}, usageError{fmt.Errorf("project could not be resolved; pass --project or run `ao project add --path <repo-path> --worker-agent <agent>`")}
}

func (c *commandContext) resolveProjectFromSession(ctx context.Context, sessionID string) (projectDetails, error) {
	sess, err := c.fetchScopedSession(ctx, sessionID, "")
	if err != nil {
		return projectDetails{}, usageError{fmt.Errorf("project could not be resolved from AO_SESSION_ID %q; pass --project", sessionID)}
	}
	if strings.TrimSpace(sess.ProjectID) == "" {
		return projectDetails{}, usageError{fmt.Errorf("project could not be resolved from AO_SESSION_ID %q; pass --project", sessionID)}
	}
	return c.fetchProjectDetails(ctx, sess.ProjectID)
}

func (c *commandContext) resolveProjectFromCWD(ctx context.Context) (projectDetails, bool, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return projectDetails{}, false, err
	}
	cwd, err = normalizeProjectMatchPath(cwd)
	if err != nil {
		return projectDetails{}, false, err
	}

	var list projectListResult
	if err := c.getJSON(ctx, "projects", &list); err != nil {
		return projectDetails{}, false, err
	}
	sort.Slice(list.Projects, func(i, j int) bool {
		return list.Projects[i].ID < list.Projects[j].ID
	})

	var best projectDetails
	details := make(map[string]projectDetails, len(list.Projects))
	bestLen := -1
	ambiguous := false
	for _, summary := range list.Projects {
		project, err := c.fetchProjectDetails(ctx, summary.ID)
		if err != nil {
			return projectDetails{}, false, err
		}
		details[summary.ID] = project
		if project.Path == "" {
			continue
		}
		projectPath, err := normalizeProjectMatchPath(project.Path)
		if err != nil {
			continue
		}
		if !pathContains(projectPath, cwd) {
			continue
		}
		pathLen := len(projectPath)
		switch {
		case pathLen > bestLen:
			best = project
			bestLen = pathLen
			ambiguous = false
		case pathLen == bestLen:
			ambiguous = true
		}
	}
	if bestLen == -1 {
		if scratch, ok := onlyScratchProject(list.Projects, details); ok {
			return scratch, true, nil
		}
		return projectDetails{}, false, nil
	}
	if ambiguous {
		return projectDetails{}, false, usageError{fmt.Errorf("current directory matches multiple registered projects; pass --project")}
	}
	return best, true, nil
}

func onlyScratchProject(summaries []projectSummary, details map[string]projectDetails) (projectDetails, bool) {
	if len(summaries) != 1 {
		return projectDetails{}, false
	}
	project := details[summaries[0].ID]
	if isScratchProject(project) {
		return project, true
	}
	return projectDetails{}, false
}

func isScratchProject(project projectDetails) bool {
	return project.ID == "scratch" && project.Kind == "scratch"
}

func normalizeProjectMatchPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if realPath, err := filepath.EvalSymlinks(abs); err == nil {
		abs = realPath
	}
	return filepath.Clean(abs), nil
}

func pathContains(root, child string) bool {
	if root == child {
		return true
	}
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func resolveSpawnHarness(explicit, kind string, project projectDetails) (string, error) {
	if harness := strings.TrimSpace(explicit); harness != "" {
		return harness, nil
	}
	if project.Config != nil {
		if kind == "orchestrator" {
			if harness := strings.TrimSpace(project.Config.Orchestrator.Agent); harness != "" {
				return harness, nil
			}
		} else {
			if harness := strings.TrimSpace(project.Config.Worker.Agent); harness != "" {
				return harness, nil
			}
		}
	}
	if kind == "orchestrator" {
		return "", usageError{fmt.Errorf("agent could not be resolved; pass --agent or configure `ao project set-config %s --orchestrator-agent <agent>`", project.ID)}
	}
	return "", usageError{fmt.Errorf("agent could not be resolved; pass --agent or configure `ao project set-config %s --worker-agent <agent>`", project.ID)}
}

func (c *commandContext) preflightSpawnAgentAuth(ctx context.Context, cmd *cobra.Command, agentID string) error {
	inv, err := c.fetchAgentInventory(ctx, true)
	if err != nil {
		return err
	}
	state := agentCatalogStateFor(inv, agentID)
	if !state.supported {
		return fmt.Errorf("agent %q is not supported by this daemon; pass a supported --agent or run `ao agent ls`", agentID)
	}
	if !state.installed || state.authStatus == "unauthorized" {
		fresh, err := c.probeSpawnAgent(ctx, agentID)
		if err != nil {
			if agentProbeUnavailable(err) {
				_, err = fmt.Fprintf(cmd.ErrOrStderr(), "warning: agent %q fresh readiness probe is unavailable; continuing and letting spawn validate runtime readiness\n", agentID)
				return err
			}
			return err
		}
		if !fresh.Supported {
			return fmt.Errorf("agent %q is not supported by this daemon; pass a supported --agent or run `ao agent ls`", agentID)
		}
		if !fresh.Installed {
			return fmt.Errorf("agent %q needs install; install the agent CLI or pass --skip-agent-check to let spawn validate it", agentID)
		}
		state.installed = true
		state.authorized = fresh.Agent.AuthStatus == "authorized"
		state.authStatus = fresh.Agent.AuthStatus
	}
	if state.authorized {
		return nil
	}
	if state.authStatus == "unauthorized" {
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "warning: agent %q may need auth according to a fresh local probe; continuing and letting spawn validate runtime readiness\n", agentID)
		return err
	}
	_, err = fmt.Fprintf(cmd.ErrOrStderr(), "warning: agent %q auth status is unknown; continuing and letting spawn validate runtime readiness\n", agentID)
	return err
}

func (c *commandContext) probeSpawnAgent(ctx context.Context, agentID string) (agentProbeResult, error) {
	var result agentProbeResult
	if err := c.postJSON(ctx, "agents/"+url.PathEscape(agentID)+"/probe", struct{}{}, &result); err != nil {
		return agentProbeResult{}, err
	}
	return result, nil
}

func agentProbeUnavailable(err error) bool {
	var apiErr apiResponseError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == http.StatusNotFound || apiErr.StatusCode == http.StatusNotImplemented
}

type agentCatalogState struct {
	supported  bool
	installed  bool
	authorized bool
	authStatus string
}

func agentCatalogStateFor(inv agentInventory, agentID string) agentCatalogState {
	state := agentCatalogState{}
	for _, info := range inv.Supported {
		if info.ID == agentID {
			state.supported = true
			break
		}
	}
	for _, info := range inv.Authorized {
		if info.ID == agentID {
			state.installed = true
			state.authorized = true
			state.authStatus = "authorized"
			return state
		}
	}
	for _, info := range inv.Installed {
		if info.ID == agentID {
			state.installed = true
			state.authorized = info.AuthStatus == "authorized"
			state.authStatus = info.AuthStatus
			return state
		}
	}
	return state
}

// rollbackSpawnedSession reverses a partial `spawn` whose out-of-band follow-up
// (PR claim) failed. It calls the daemon's `/rollback` endpoint, which deletes
// the seed-state row outright instead of marking it terminated — so the user
// does not see an orphan terminated session under `--include-terminated`. If
// spawn output has already landed (workspace + runtime), the daemon falls back
// to a Kill on the server side so teardown still happens.
func (c *commandContext) rollbackSpawnedSession(ctx context.Context, id string) error {
	var res rollbackSessionResponse
	return c.postJSON(ctx, "sessions/"+url.PathEscape(id)+"/rollback", struct{}{}, &res)
}

// rollbackSessionResponse mirrors the daemon's RollbackSessionResponse body.
type rollbackSessionResponse struct {
	OK        bool   `json:"ok"`
	SessionID string `json:"sessionId"`
	Deleted   bool   `json:"deleted,omitempty"`
	Killed    bool   `json:"killed,omitempty"`
}
