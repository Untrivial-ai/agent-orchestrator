package cli

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

type sessionTopOptions struct {
	project string
	all     bool
	json    bool
}

// sessionMemoryDTO mirrors controllers.SessionMemoryResponse.
type sessionMemoryDTO struct {
	SessionID    string    `json:"sessionId"`
	RSSBytes     uint64    `json:"rssBytes"`
	ProcessCount int       `json:"processCount"`
	SampledAt    time.Time `json:"sampledAt"`
}

type sessionMemoryListResponse struct {
	Sessions []sessionMemoryDTO `json:"sessions"`
}

type sessionTopEntry struct {
	sessionDTO
	RSSBytes     uint64 `json:"rssBytes"`
	ProcessCount int    `json:"processCount"`
}

type sessionTopOutput struct {
	Data []sessionTopEntry `json:"data"`
	Meta struct {
		TotalRSSBytes uint64 `json:"totalRssBytes"`
	} `json:"meta"`
}

func newSessionTopCommand(ctx *commandContext) *cobra.Command {
	var opts sessionTopOptions
	cmd := &cobra.Command{
		Use:   "top",
		Short: "Show memory used by each live session, largest first",
		Long:  "Show resident memory of each live session's process tree, largest first. Free memory with `ao session kill <id>` or `ao session cleanup`.",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.topSessions(cmd.Context(), cmd, opts)
		},
	}
	f := cmd.Flags()
	addSessionProjectFlag(f, &opts.project, "Filter by project ID")
	f.BoolVarP(&opts.all, "all", "a", false, "Include orchestrator sessions")
	f.BoolVar(&opts.json, "json", false, "Output as JSON")
	return cmd
}

func (c *commandContext) topSessions(ctx context.Context, cmd *cobra.Command, opts sessionTopOptions) error {
	params := url.Values{}
	params.Set("active", "true")
	if opts.project != "" {
		params.Set("project", opts.project)
	}
	var list sessionListResponse
	if err := c.getJSON(ctx, apiPath("sessions", params), &list); err != nil {
		return err
	}
	memParams := url.Values{}
	if opts.project != "" {
		memParams.Set("projectId", opts.project)
	}
	var mem sessionMemoryListResponse
	if err := c.getJSON(ctx, apiPath("usage/sessions/memory", memParams), &mem); err != nil {
		return err
	}
	bySession := make(map[string]sessionMemoryDTO, len(mem.Sessions))
	for _, item := range mem.Sessions {
		bySession[item.SessionID] = item
	}
	entries := make([]sessionTopEntry, 0, len(list.Sessions))
	var total uint64
	for _, sess := range filterAndSortSessions(list.Sessions, opts.all) {
		reading := bySession[sess.ID]
		entries = append(entries, sessionTopEntry{sessionDTO: sess, RSSBytes: reading.RSSBytes, ProcessCount: reading.ProcessCount})
		total += reading.RSSBytes
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].RSSBytes > entries[j].RSSBytes })
	if opts.json {
		out := sessionTopOutput{Data: entries}
		out.Meta.TotalRSSBytes = total
		return writeJSON(cmd.OutOrStdout(), out)
	}
	return writeSessionTop(cmd, entries, total, bySession, c.deps.Now())
}

func writeSessionTop(cmd *cobra.Command, entries []sessionTopEntry, total uint64, readings map[string]sessionMemoryDTO, now time.Time) error {
	out := cmd.OutOrStdout()
	if len(entries) == 0 {
		_, err := fmt.Fprintln(out, "(no active sessions)")
		return err
	}
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "SESSION\tSTATE\tRSS\tPROCS\tIDLE"); err != nil {
		return err
	}
	for _, e := range entries {
		rss, procs := "-", "-"
		if _, ok := readings[e.ID]; ok {
			rss = formatBytes(e.RSSBytes)
			procs = fmt.Sprint(e.ProcessCount)
		}
		idle := "-"
		if e.Activity.State != "working" {
			idle = sessionAge(now, e.Activity.LastActivityAt)
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", e.ID, emptyDash(e.Activity.State), rss, procs, idle); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(table, "TOTAL\t\t%s\t\t\n", formatBytes(total)); err != nil {
		return err
	}
	return table.Flush()
}

// formatBytes renders a byte count the way btop does: one decimal, binary units.
func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit && exp < 3; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGT"[exp])
}
