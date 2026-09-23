package cli

import (
	"fmt"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

type agentListOptions struct {
	refresh bool
	json    bool
}

// agentInfo mirrors the daemon's agent Info body for the CLI client.
type agentInfo struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	AuthStatus string `json:"authStatus,omitempty"`
}

// agentInventory mirrors GET /api/v1/agents and POST /api/v1/agents/refresh.
type agentInventory struct {
	Supported  []agentInfo `json:"supported"`
	Installed  []agentInfo `json:"installed"`
	Authorized []agentInfo `json:"authorized"`
}

type agentReadinessObservation struct {
	State      string `json:"state"`
	Freshness  string `json:"freshness"`
	ReasonCode string `json:"reasonCode"`
	Reason     string `json:"reason"`
}

type agentReadinessSnapshot struct {
	ID                 string                    `json:"id"`
	Label              string                    `json:"label"`
	Installation       agentReadinessObservation `json:"installation"`
	Authentication     agentReadinessObservation `json:"authentication"`
	EffectiveReadiness string                    `json:"effectiveReadiness"`
	UsageCount         int                       `json:"usageCount"`
	LastUsedAt         *string                   `json:"lastUsedAt,omitempty"`
}

type agentReadinessResponse struct {
	Agents []agentReadinessSnapshot `json:"agents"`
}

type ensureAgentReadinessRequest struct {
	AgentIDs []string `json:"agentIds,omitempty"`
	Purpose  string   `json:"purpose"`
}

func newAgentCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Inspect agent catalog readiness",
	}
	cmd.AddCommand(newAgentListCommand(ctx))
	return cmd
}

func newAgentListCommand(ctx *commandContext) *cobra.Command {
	var opts agentListOptions
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List supported agents and local auth readiness",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			inv, err := ctx.fetchAgentInventory(cmd.Context(), opts.refresh)
			if err != nil {
				return err
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), inv)
			}
			return writeAgentList(cmd, inv)
		},
	}
	cmd.Flags().BoolVar(&opts.refresh, "refresh", false, "Force fresh local install and auth checks before listing")
	cmd.Flags().BoolVar(&opts.json, "json", false, "Output raw agent catalog JSON")
	return cmd
}

func readinessInventory(readiness agentReadinessResponse) agentInventory {
	inv := agentInventory{Supported: make([]agentInfo, 0, len(readiness.Agents)), Installed: []agentInfo{}, Authorized: []agentInfo{}}
	for _, snapshot := range readiness.Agents {
		authStatus := snapshot.Authentication.State
		if authStatus == "not_applicable" {
			authStatus = "authorized"
		}
		info := agentInfo{ID: snapshot.ID, Label: snapshot.Label, AuthStatus: authStatus}
		inv.Supported = append(inv.Supported, info)
		if snapshot.Installation.State == "installed" {
			inv.Installed = append(inv.Installed, info)
		}
		if snapshot.Authentication.State == "authorized" || snapshot.Authentication.State == "not_applicable" {
			inv.Authorized = append(inv.Authorized, info)
		}
	}
	return inv
}

func writeAgentList(cmd *cobra.Command, inv agentInventory) error {
	out := cmd.OutOrStdout()
	if len(inv.Supported) == 0 {
		_, err := fmt.Fprintln(out, "No agents supported by this daemon.")
		return err
	}

	sort.Slice(inv.Supported, func(i, j int) bool {
		return inv.Supported[i].ID < inv.Supported[j].ID
	})
	installed := agentInfoByID(inv.Installed)
	authorized := agentInfoByID(inv.Authorized)

	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tLABEL\tINSTALL\tAUTH"); err != nil {
		return err
	}
	for _, info := range inv.Supported {
		installLabel := "needs install"
		authLabel := "auth unknown"
		if installedInfo, ok := installed[info.ID]; ok {
			installLabel = "installed"
			switch installedInfo.AuthStatus {
			case "authorized":
				authLabel = "authorized"
			case "unauthorized":
				authLabel = "needs auth"
			default:
				authLabel = "auth unknown"
			}
		}
		if _, ok := authorized[info.ID]; ok {
			installLabel = "installed"
			authLabel = "authorized"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", info.ID, info.Label, installLabel, authLabel); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func agentInfoByID(infos []agentInfo) map[string]agentInfo {
	out := make(map[string]agentInfo, len(infos))
	for _, info := range infos {
		out[info.ID] = info
	}
	return out
}
