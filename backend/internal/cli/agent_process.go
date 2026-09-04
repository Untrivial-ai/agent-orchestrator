package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const supervisedExitReportTimeout = 5 * time.Second

func newAgentProcessCommand(ctx *commandContext) *cobra.Command {
	root := &cobra.Command{
		Use:    "agent-process",
		Short:  "Run an AO-managed agent process (internal)",
		Hidden: true,
	}
	root.AddCommand(newAgentProcessSuperviseCommand(ctx))
	return root
}

func newAgentProcessSuperviseCommand(ctx *commandContext) *cobra.Command {
	var sessionID string
	var launchID string
	cmd := &cobra.Command{
		Use:    "supervise --session <id> --launch <id> -- <command> [args...]",
		Short:  "Supervise one managed agent process (internal)",
		Hidden: true,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return usageError{fmt.Errorf("agent command is required")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// The exit report names a session owned by the local daemon: AO_URL is
			// ignored, --url is refused, and neither ever reaches the child.
			if err := ctx.pinToLocalDaemon("ao agent-process supervise"); err != nil {
				return err
			}
			sessionID = strings.TrimSpace(sessionID)
			launchID = strings.TrimSpace(launchID)
			if !sessionIDPattern.MatchString(sessionID) {
				return usageError{fmt.Errorf("invalid session id")}
			}
			if !sessionIDPattern.MatchString(launchID) {
				return usageError{fmt.Errorf("invalid launch id")}
			}
			ctx.runSupervisedProcess(cmd.Context(), sessionID, launchID, args)
			return nil
		},
	}
	cmd.Flags().StringVar(&sessionID, "session", "", "AO session id")
	cmd.Flags().StringVar(&launchID, "launch", "", "AO process launch id")
	return cmd
}

func (c *commandContext) runSupervisedProcess(ctx context.Context, sessionID, launchID string, argv []string) {
	child := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv is constructed by the selected agent adapter.
	child.Stdin = c.deps.In
	child.Stdout = c.deps.Out
	child.Stderr = c.deps.Err

	if err := child.Start(); err != nil {
		_, _ = fmt.Fprintf(c.deps.Err, "ao: start managed agent: %v\n", err)
		c.reportSupervisedExit(sessionID, launchID)
		return
	}

	// The child shares the terminal foreground process group and therefore
	// receives Ctrl-C directly. Consume the supervisor's copy so it remains
	// alive long enough to reap the child and publish the exit observation.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	_ = child.Wait()
	signal.Stop(interrupts)

	c.reportSupervisedExit(sessionID, launchID)
}

func (c *commandContext) reportSupervisedExit(sessionID, launchID string) {
	ctx, cancel := context.WithTimeout(context.Background(), supervisedExitReportTimeout)
	defer cancel()
	path := "sessions/" + sessionID + "/activity"
	req := setActivityAPIRequest{State: "exited", Event: "process-exited", LaunchID: launchID}
	if err := c.postJSON(ctx, path, req, nil); err != nil {
		// Reconciliation will recover this event from process absence. Keep the
		// delivery failure visible without preventing the terminal's shell.
		c.reportHookFailure("agent-process", "process-exited", sessionID, err)
	}
}
