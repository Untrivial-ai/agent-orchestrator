package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type sessionOptions struct {
	project string
	json    bool
}

type sessionListOptions struct {
	sessionOptions
	all               bool
	includeTerminated bool
}

type sessionCleanupOptions struct {
	project string
	yes     bool
	dryRun  bool
}

type sessionClaimPROptions struct {
	project    string
	json       bool
	noTakeover bool
}

type sessionRenameRequest struct {
	DisplayName string `json:"displayName"`
}

type sessionDTO struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"projectId"`
	IssueID      string          `json:"issueId,omitempty"`
	Kind         string          `json:"kind"`
	Harness      string          `json:"harness,omitempty"`
	DisplayName  string          `json:"displayName,omitempty"`
	Activity     sessionActivity `json:"activity"`
	IsTerminated bool            `json:"isTerminated"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
	Status       string          `json:"status"`
}

type sessionActivity struct {
	State          string    `json:"state"`
	LastActivityAt time.Time `json:"lastActivityAt"`
}

type sessionListResponse struct {
	Sessions []sessionDTO `json:"sessions"`
}

type sessionResponse struct {
	Session sessionDTO `json:"session"`
}

type killSessionResponse struct {
	SessionID string `json:"sessionId"`
	Freed     bool   `json:"freed"`
}

type restoreSessionResponse struct {
	SessionID string     `json:"sessionId"`
	Session   sessionDTO `json:"session"`
}

type renameSessionResponse struct {
	SessionID   string `json:"sessionId"`
	DisplayName string `json:"displayName"`
}

type cleanupSkippedSession struct {
	SessionID string `json:"sessionId"`
	Reason    string `json:"reason"`
}

type cleanupSessionsResponse struct {
	Cleaned []string                `json:"cleaned"`
	Skipped []cleanupSkippedSession `json:"skipped"`
}

type claimPRRequest struct {
	PR            string `json:"pr"`
	AllowTakeover bool   `json:"allowTakeover"`
}

type sessionPRDTO struct {
	URL            string    `json:"url"`
	Number         int       `json:"number"`
	State          string    `json:"state"`
	CI             string    `json:"ci"`
	Review         string    `json:"review"`
	Mergeability   string    `json:"mergeability"`
	ReviewComments bool      `json:"reviewComments"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type claimPRResponse struct {
	OK            bool           `json:"ok"`
	SessionID     string         `json:"sessionId"`
	PRs           []sessionPRDTO `json:"prs"`
	BranchChanged bool           `json:"branchChanged"`
	TakenOverFrom []string       `json:"takenOverFrom"`
}

type sessionListEntry struct {
	ID             string     `json:"id"`
	ProjectID      string     `json:"projectId"`
	Role           string     `json:"role"`
	Status         string     `json:"status,omitempty"`
	IssueID        string     `json:"issueId,omitempty"`
	Harness        string     `json:"harness,omitempty"`
	IsTerminated   bool       `json:"isTerminated"`
	LastActivityAt *time.Time `json:"lastActivityAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type sessionListOutput struct {
	Data []sessionListEntry `json:"data"`
	Meta struct {
		HiddenTerminatedCount   int `json:"hiddenTerminatedCount"`
		HiddenOrchestratorCount int `json:"hiddenOrchestratorCount"`
	} `json:"meta"`
}

func newSessionCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage agent sessions",
	}
	cmd.AddCommand(newSessionListCommand(ctx))
	cmd.AddCommand(newSessionGetCommand(ctx))
	cmd.AddCommand(newSessionKillCommand(ctx))
	cmd.AddCommand(newSessionRestoreCommand(ctx))
	cmd.AddCommand(newSessionRenameCommand(ctx))
	cmd.AddCommand(newSessionCleanupCommand(ctx))
	cmd.AddCommand(newSessionClaimPRCommand(ctx))
	cmd.AddCommand(newSessionSwitchAgentCommand(ctx))
	cmd.AddCommand(newSessionAgentSwitchCommand(ctx))
	cmd.AddCommand(newSessionHandoffCommand(ctx))
	return cmd
}

func newSessionListCommand(ctx *commandContext) *cobra.Command {
	var opts sessionListOptions
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List sessions",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.listSessions(cmd.Context(), cmd, opts)
		},
	}
	f := cmd.Flags()
	addSessionProjectFlag(f, &opts.project, "Filter by project ID")
	f.BoolVarP(&opts.all, "all", "a", false, "Include orchestrator sessions")
	f.BoolVar(&opts.includeTerminated, "include-terminated", false, "Include terminated sessions")
	f.BoolVar(&opts.json, "json", false, "Output as JSON")
	return cmd
}

func newSessionGetCommand(ctx *commandContext) *cobra.Command {
	var opts sessionOptions
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Fetch one session",
		Args:  oneSessionIDArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := normalizeSessionID(args[0])
			if err != nil {
				return err
			}
			return ctx.getSession(cmd.Context(), cmd, id, opts)
		},
	}
	f := cmd.Flags()
	addSessionProjectFlag(f, &opts.project, "Project id to scope the lookup")
	f.BoolVar(&opts.json, "json", false, "Output as JSON")
	return cmd
}

func newSessionKillCommand(ctx *commandContext) *cobra.Command {
	var opts sessionOptions
	cmd := &cobra.Command{
		Use:   "kill <id>",
		Short: "Terminate a session",
		Args:  oneSessionIDArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := normalizeSessionID(args[0])
			if err != nil {
				return err
			}
			return ctx.killSession(cmd.Context(), cmd, id, opts)
		},
	}
	addSessionProjectFlag(cmd.Flags(), &opts.project, "Project id to scope the lookup")
	return cmd
}

func newSessionRestoreCommand(ctx *commandContext) *cobra.Command {
	var opts sessionOptions
	cmd := &cobra.Command{
		Use:   "restore <id>",
		Short: "Relaunch a terminated session",
		Args:  oneSessionIDArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := normalizeSessionID(args[0])
			if err != nil {
				return err
			}
			return ctx.restoreSession(cmd.Context(), cmd, id, opts)
		},
	}
	addSessionProjectFlag(cmd.Flags(), &opts.project, "Project id to scope the lookup")
	return cmd
}

func newSessionRenameCommand(ctx *commandContext) *cobra.Command {
	var opts sessionOptions
	cmd := &cobra.Command{
		Use:   "rename <id> <name>",
		Short: "Rename a session",
		Args:  sessionRenameArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := normalizeSessionID(args[0])
			if err != nil {
				return err
			}
			return ctx.renameSession(cmd.Context(), cmd, id, args[1], opts)
		},
	}
	addSessionProjectFlag(cmd.Flags(), &opts.project, "Project id to scope the lookup")
	return cmd
}

func newSessionCleanupCommand(ctx *commandContext) *cobra.Command {
	var opts sessionCleanupOptions
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Clean up terminated sessions",
		Long:  "Clean up terminated sessions by reclaiming eligible workspaces. Dirty worktrees are skipped by the daemon.",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.cleanupSessions(cmd.Context(), cmd, opts)
		},
	}
	addSessionProjectFlag(cmd.Flags(), &opts.project, "Filter by project ID")
	cmd.Flags().BoolVarP(&opts.yes, "yes", "y", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Preview which sessions would be cleaned without removing anything")
	return cmd
}

func newSessionClaimPRCommand(ctx *commandContext) *cobra.Command {
	var opts sessionClaimPROptions
	cmd := &cobra.Command{
		Use:   "claim-pr [<session-id>] <pr-ref>",
		Short: "Attach an existing PR to a session",
		Long:  "Attach an existing PR to a session. When session-id is omitted, the current session is read from AO_SESSION_ID.",
		Example: `  # From inside an AO worker session
  ao session claim-pr 88

  # Target a session explicitly
  ao session claim-pr mer-3 88`,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.RangeArgs(1, 2)(cmd, args); err != nil {
				return usageError{err}
			}
			_, _, err := resolveSessionClaimPRArgs(args)
			return err
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			id, ref, err := resolveSessionClaimPRArgs(args)
			if err != nil {
				return err
			}
			return ctx.claimSessionPR(cmd.Context(), cmd, id, ref, opts)
		},
	}
	addSessionProjectFlag(cmd.Flags(), &opts.project, "Project id to scope the lookup")
	cmd.Flags().BoolVar(&opts.noTakeover, "no-takeover", false, "Refuse if another active session owns the PR")
	cmd.Flags().BoolVar(&opts.json, "json", false, "Output as JSON")
	return cmd
}

func resolveSessionClaimPRArgs(args []string) (sessionID, ref string, err error) {
	if len(args) == 2 {
		sessionID, err = normalizeSessionID(args[0])
		return sessionID, args[1], err
	}
	sessionID = strings.TrimSpace(os.Getenv("AO_SESSION_ID"))
	if sessionID == "" {
		return "", "", usageError{errors.New("session id is required (pass <session-id> or set AO_SESSION_ID)")}
	}
	sessionID, err = normalizeSessionID(sessionID)
	return sessionID, args[0], err
}

func addSessionProjectFlag(flags interface {
	StringVarP(*string, string, string, string, string)
}, target *string, usage string) {
	flags.StringVarP(target, "project", "p", "", usage)
}

func oneSessionIDArg(cmd *cobra.Command, args []string) error {
	if err := cobra.ExactArgs(1)(cmd, args); err != nil {
		return usageError{err}
	}
	if _, err := normalizeSessionID(args[0]); err != nil {
		return err
	}
	return nil
}

func sessionRenameArgs(cmd *cobra.Command, args []string) error {
	if err := cobra.ExactArgs(2)(cmd, args); err != nil {
		return usageError{err}
	}
	if _, err := normalizeSessionID(args[0]); err != nil {
		return err
	}
	if strings.TrimSpace(args[1]) == "" {
		return usageError{errors.New("session name is required")}
	}
	return nil
}

func (c *commandContext) claimSessionPR(ctx context.Context, cmd *cobra.Command, id, ref string, opts sessionClaimPROptions) error {
	sess, err := c.fetchScopedSession(ctx, id, opts.project)
	if err != nil {
		return err
	}
	project, err := c.fetchProjectDetails(ctx, sess.ProjectID)
	if err != nil {
		return err
	}
	resolvedRef, err := c.resolvePRRef(ctx, ref, project)
	if err != nil {
		return err
	}
	var res claimPRResponse
	req := claimPRRequest{PR: resolvedRef, AllowTakeover: !opts.noTakeover}
	if err := c.postJSON(ctx, "sessions/"+url.PathEscape(id)+"/pr/claim", req, &res); err != nil {
		return err
	}
	if opts.json {
		return writeJSON(cmd.OutOrStdout(), res)
	}
	return writeClaimPRResult(cmd, res)
}

func (c *commandContext) fetchProjectDetails(ctx context.Context, id string) (projectDetails, error) {
	var res projectGetResult
	if err := c.getJSON(ctx, "projects/"+url.PathEscape(id), &res); err != nil {
		return projectDetails{}, err
	}
	return res.Project, nil
}

func writeClaimPRResult(cmd *cobra.Command, res claimPRResponse) error {
	out := cmd.OutOrStdout()
	if len(res.PRs) == 0 {
		_, err := fmt.Fprintf(out, "session %s claimed PR\n", res.SessionID)
		return err
	}
	pr := res.PRs[0]
	if _, err := fmt.Fprintf(out, "session %s claimed PR #%d\n", res.SessionID, pr.Number); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "  pr:       %s\n", pr.URL); err != nil {
		return err
	}
	checkout := "already on PR branch"
	if res.BranchChanged {
		checkout = "switched to PR branch"
	}
	if _, err := fmt.Fprintf(out, "  checkout: %s\n", checkout); err != nil {
		return err
	}
	for _, owner := range res.TakenOverFrom {
		if _, err := fmt.Fprintf(out, "  taking over from %s\n", owner); err != nil {
			return err
		}
	}
	return nil
}

func (c *commandContext) listSessions(ctx context.Context, cmd *cobra.Command, opts sessionListOptions) error {
	params := url.Values{}
	if opts.project != "" {
		params.Set("project", opts.project)
	}
	if !opts.includeTerminated {
		params.Set("active", "true")
	}
	var res sessionListResponse
	if err := c.getJSON(ctx, apiPath("sessions", params), &res); err != nil {
		return err
	}
	hiddenOrchestratorCount := 0
	if !opts.all {
		for _, sess := range res.Sessions {
			if sess.Kind == "orchestrator" {
				hiddenOrchestratorCount++
			}
		}
	}
	sessions := filterAndSortSessions(res.Sessions, opts.all)
	hiddenTerminatedCount := 0
	if !opts.includeTerminated {
		terminatedCount, terminatedOrchestratorCount, err := c.countHiddenTerminated(ctx, opts.project, opts.all)
		if err != nil {
			return err
		}
		hiddenTerminatedCount = terminatedCount
		hiddenOrchestratorCount += terminatedOrchestratorCount
	}
	if opts.json {
		out := sessionListOutput{Data: sessionListEntries(sessions)}
		out.Meta.HiddenTerminatedCount = hiddenTerminatedCount
		out.Meta.HiddenOrchestratorCount = hiddenOrchestratorCount
		return writeJSON(cmd.OutOrStdout(), out)
	}
	return writeSessionList(cmd, sessions, hiddenTerminatedCount, hiddenOrchestratorCount)
}

func (c *commandContext) countHiddenTerminated(ctx context.Context, project string, includeOrchestrators bool) (int, int, error) {
	params := url.Values{}
	if project != "" {
		params.Set("project", project)
	}
	params.Set("active", "false")
	var res sessionListResponse
	if err := c.getJSON(ctx, apiPath("sessions", params), &res); err != nil {
		return 0, 0, err
	}
	hiddenOrchestratorCount := 0
	if !includeOrchestrators {
		for _, sess := range res.Sessions {
			if sess.Kind == "orchestrator" {
				hiddenOrchestratorCount++
			}
		}
	}
	return len(filterAndSortSessions(res.Sessions, includeOrchestrators)), hiddenOrchestratorCount, nil
}

func (c *commandContext) getSession(ctx context.Context, cmd *cobra.Command, id string, opts sessionOptions) error {
	sess, err := c.fetchScopedSession(ctx, id, opts.project)
	if err != nil {
		return err
	}
	if opts.json {
		return writeJSON(cmd.OutOrStdout(), sessionResponse{Session: sess})
	}
	return writeSessionDetails(cmd, sess)
}

func (c *commandContext) killSession(ctx context.Context, cmd *cobra.Command, id string, opts sessionOptions) error {
	if opts.project != "" {
		if _, err := c.fetchScopedSession(ctx, id, opts.project); err != nil {
			return err
		}
	}
	var res killSessionResponse
	if err := c.postJSON(ctx, "sessions/"+url.PathEscape(id)+"/kill", struct{}{}, &res); err != nil {
		return err
	}
	if res.Freed {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "session %s killed\n", res.SessionID)
		return err
	}
	// freed=false: the workspace was preserved (e.g. uncommitted changes) — the
	// session is terminated either way, but the worktree is left for inspection.
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "session %s killed (workspace preserved)\n", res.SessionID)
	return err
}

func (c *commandContext) restoreSession(ctx context.Context, cmd *cobra.Command, id string, opts sessionOptions) error {
	if opts.project != "" {
		if _, err := c.fetchScopedSession(ctx, id, opts.project); err != nil {
			return err
		}
	}
	var res restoreSessionResponse
	if err := c.postJSON(ctx, "sessions/"+url.PathEscape(id)+"/restore", struct{}{}, &res); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(out, "session %s restored\n", res.SessionID); err != nil {
		return err
	}
	if res.Session.ProjectID != "" {
		if _, err := fmt.Fprintf(out, "  project: %s\n", res.Session.ProjectID); err != nil {
			return err
		}
	}
	return nil
}

func (c *commandContext) renameSession(ctx context.Context, cmd *cobra.Command, id, displayName string, opts sessionOptions) error {
	if opts.project != "" {
		if _, err := c.fetchScopedSession(ctx, id, opts.project); err != nil {
			return err
		}
	}
	name := strings.TrimSpace(displayName)
	var res renameSessionResponse
	if err := c.patchJSON(ctx, "sessions/"+url.PathEscape(id), sessionRenameRequest{DisplayName: name}, &res); err != nil {
		return err
	}
	sessionID := res.SessionID
	if sessionID == "" {
		sessionID = id
	}
	if res.DisplayName != "" {
		name = res.DisplayName
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "session %s renamed to %q\n", sessionID, name)
	return err
}

func (c *commandContext) cleanupSessions(ctx context.Context, cmd *cobra.Command, opts sessionCleanupOptions) error {
	candidates, err := c.previewCleanupSessions(ctx, opts.project)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintln(out, "Checking for completed sessions..."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	if len(candidates) == 0 {
		_, err := fmt.Fprintln(out, "  No sessions to clean up.")
		return err
	}
	labels := cleanupLabels(candidates, opts.project)
	for _, label := range labels {
		if _, err := fmt.Fprintf(out, "  Would clean %s\n", label); err != nil {
			return err
		}
	}
	if opts.dryRun {
		_, err = fmt.Fprintln(out, "\n(dry-run: no sessions were removed)")
		return err
	}
	if !opts.yes {
		confirmed, err := confirmSessionCleanup(cmd, len(candidates), opts.project)
		if err != nil {
			return err
		}
		if !confirmed {
			_, err := fmt.Fprintln(out, "aborted")
			return err
		}
	}
	params := url.Values{}
	if opts.project != "" {
		params.Set("project", opts.project)
	}
	var res cleanupSessionsResponse
	if err := c.postJSON(ctx, apiPath("sessions/cleanup", params), struct{}{}, &res); err != nil {
		return err
	}
	cleaned := res.Cleaned
	labelByID := cleanupLabelByID(candidates, opts.project)
	for _, id := range cleaned {
		label := id
		if mapped := labelByID[id]; mapped != "" {
			label = mapped
		}
		if _, err := fmt.Fprintf(out, "  Cleaned: %s\n", label); err != nil {
			return err
		}
	}
	for _, skip := range res.Skipped {
		label := skip.SessionID
		if mapped := labelByID[skip.SessionID]; mapped != "" {
			label = mapped
		}
		if _, err := fmt.Fprintf(out, "  Skipped: %s (%s)\n", label, skip.Reason); err != nil {
			return err
		}
	}
	summary := fmt.Sprintf("\nCleanup complete. %d session%s cleaned", len(cleaned), pluralS(len(cleaned)))
	if len(res.Skipped) > 0 {
		summary += fmt.Sprintf(", %d skipped", len(res.Skipped))
	}
	_, err = fmt.Fprintf(out, "%s.\n", summary)
	return err
}

func (c *commandContext) previewCleanupSessions(ctx context.Context, project string) ([]sessionDTO, error) {
	params := url.Values{}
	params.Set("active", "false")
	if project != "" {
		params.Set("project", project)
	}
	var res sessionListResponse
	if err := c.getJSON(ctx, apiPath("sessions", params), &res); err != nil {
		return nil, err
	}
	return filterAndSortSessions(res.Sessions, true), nil
}

func (c *commandContext) fetchScopedSession(ctx context.Context, id, project string) (sessionDTO, error) {
	var res sessionResponse
	if err := c.getJSON(ctx, "sessions/"+url.PathEscape(id), &res); err != nil {
		return sessionDTO{}, err
	}
	if project != "" && res.Session.ProjectID != project {
		return sessionDTO{}, usageError{fmt.Errorf("session %s is not in project %s", id, project)}
	}
	return res.Session, nil
}

func filterAndSortSessions(sessions []sessionDTO, includeOrchestrators bool) []sessionDTO {
	out := make([]sessionDTO, 0, len(sessions))
	for _, sess := range sessions {
		if !includeOrchestrators && sess.Kind == "orchestrator" {
			continue
		}
		out = append(out, sess)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProjectID != out[j].ProjectID {
			return out[i].ProjectID < out[j].ProjectID
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func sessionListEntries(sessions []sessionDTO) []sessionListEntry {
	entries := make([]sessionListEntry, 0, len(sessions))
	for _, sess := range sessions {
		var last *time.Time
		if !sess.Activity.LastActivityAt.IsZero() {
			activity := sess.Activity.LastActivityAt
			last = &activity
		}
		entries = append(entries, sessionListEntry{
			ID:             sess.ID,
			ProjectID:      sess.ProjectID,
			Role:           sessionRole(sess),
			Status:         sess.Status,
			IssueID:        sess.IssueID,
			Harness:        sess.Harness,
			IsTerminated:   sess.IsTerminated,
			LastActivityAt: last,
			CreatedAt:      sess.CreatedAt,
			UpdatedAt:      sess.UpdatedAt,
		})
	}
	return entries
}

func cleanupLabels(sessions []sessionDTO, scopedProject string) []string {
	labels := make([]string, 0, len(sessions))
	for _, sess := range sessions {
		labels = append(labels, cleanupLabel(sess, scopedProject))
	}
	return labels
}

func cleanupLabelByID(sessions []sessionDTO, scopedProject string) map[string]string {
	labels := make(map[string]string, len(sessions))
	for _, sess := range sessions {
		labels[sess.ID] = cleanupLabel(sess, scopedProject)
	}
	return labels
}

func cleanupLabel(sess sessionDTO, scopedProject string) string {
	if scopedProject == "" && sess.ProjectID != "" {
		return sess.ProjectID + ":" + sess.ID
	}
	return sess.ID
}

func writeSessionList(cmd *cobra.Command, sessions []sessionDTO, hiddenTerminatedCount, hiddenOrchestratorCount int) error {
	out := cmd.OutOrStdout()
	if len(sessions) == 0 {
		if _, err := fmt.Fprintln(out, "(no active sessions)"); err != nil {
			return err
		}
	} else {
		currentProject := ""
		for _, sess := range sessions {
			if sess.ProjectID != currentProject {
				if currentProject != "" {
					if _, err := fmt.Fprintln(out); err != nil {
						return err
					}
				}
				currentProject = sess.ProjectID
				if _, err := fmt.Fprintf(out, "%s:\n", currentProject); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(out, "  %s", sess.ID); err != nil {
				return err
			}
			parts := sessionLineParts(sess)
			if len(parts) > 0 {
				if _, err := fmt.Fprintf(out, "  %s", strings.Join(parts, "  ")); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(out); err != nil {
				return err
			}
		}
	}
	if hiddenTerminatedCount > 0 {
		if _, err := fmt.Fprintf(out, "%d terminated session%s hidden. Use --include-terminated to show.\n", hiddenTerminatedCount, pluralS(hiddenTerminatedCount)); err != nil {
			return err
		}
	}
	if hiddenOrchestratorCount > 0 {
		_, err := fmt.Fprintf(out, "%d orchestrator session%s hidden. Use --all or `ao orchestrator ls` to show.\n", hiddenOrchestratorCount, pluralS(hiddenOrchestratorCount))
		return err
	}
	return nil
}

func sessionLineParts(sess sessionDTO) []string {
	parts := []string{}
	if !sess.Activity.LastActivityAt.IsZero() {
		parts = append(parts, "("+formatSessionAge(time.Since(sess.Activity.LastActivityAt))+")")
	}
	if sess.Status != "" {
		parts = append(parts, "["+sess.Status+"]")
	}
	if sess.Kind != "" {
		parts = append(parts, sess.Kind)
	}
	if sess.IssueID != "" {
		parts = append(parts, sess.IssueID)
	}
	return parts
}

func writeSessionDetails(cmd *cobra.Command, sess sessionDTO) error {
	out := cmd.OutOrStdout()
	fields := [][2]string{
		{"id", sess.ID},
		{"project", sess.ProjectID},
		{"name", sess.DisplayName},
		{"role", sessionRole(sess)},
		{"status", sess.Status},
		{"activity", sess.Activity.State},
		{"harness", sess.Harness},
		{"issue", sess.IssueID},
		{"terminated", fmt.Sprintf("%t", sess.IsTerminated)},
	}
	for _, field := range fields {
		if field[1] == "" {
			continue
		}
		if _, err := fmt.Fprintf(out, "%s: %s\n", field[0], field[1]); err != nil {
			return err
		}
	}
	if !sess.CreatedAt.IsZero() {
		if _, err := fmt.Fprintf(out, "created: %s\n", sess.CreatedAt.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	if !sess.UpdatedAt.IsZero() {
		if _, err := fmt.Fprintf(out, "updated: %s\n", sess.UpdatedAt.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return nil
}

func sessionRole(sess sessionDTO) string {
	if sess.Kind == "orchestrator" {
		return "orchestrator"
	}
	return "worker"
}

func formatSessionAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func apiPath(path string, params url.Values) string {
	if len(params) == 0 {
		return path
	}
	return path + "?" + params.Encode()
}

func normalizeSessionID(id string) (string, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return "", usageError{errors.New("session id is required")}
	}
	return trimmed, nil
}

func confirmSessionCleanup(cmd *cobra.Command, count int, project string) (bool, error) {
	scope := " across all projects"
	if project != "" {
		scope = fmt.Sprintf(" in project %q", project)
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Clean %d terminated session%s%s? Type yes to confirm: ", count, pluralS(count), scope); err != nil {
		return false, err
	}
	reader := bufio.NewReader(cmd.InOrStdin())
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(line), "yes"), nil
}
