// Package cli implements the user-facing ao command. It stays thin: commands
// discover the local daemon, call its loopback HTTP API, and format output.
package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/daemon"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
	"github.com/aoagents/agent-orchestrator/backend/internal/processalive"
	"github.com/aoagents/agent-orchestrator/backend/internal/telemetrymeta"
)

// Execute runs the ao CLI with process stdio.
func Execute() error {
	return executeWithDeps(DefaultDeps(), os.Args[1:])
}

func executeWithDeps(deps Deps, args []string) error {
	deps = deps.withDefaults()
	cmd := NewRootCommand(deps)
	cmd.SetArgs(args)
	err := cmd.Execute()
	if err != nil && ExitCode(err) == 2 {
		(&commandContext{deps: deps}).emitCLIUsageError(context.Background(), args, err)
	}
	return err
}

// usageError marks a command-line misuse (bad flag, wrong arg count). It lets
// the process entrypoint return exit code 2 for usage errors versus 1 for
// runtime failures, matching the convention CLIs are scripted against.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// ExitCode maps a CLI error to a process exit code: 2 for usage errors, 1 for
// any other failure, 0 for success.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ue usageError
	if errors.As(err, &ue) {
		return 2
	}
	return 1
}

// Deps holds the small set of side effects the CLI needs. Tests replace these
// functions without reaching into process-global state.
type Deps struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer

	HTTPClient         *http.Client
	Executable         func() (string, error)
	StartProcess       func(processStartConfig) error
	ProcessAlive       func(pid int) bool
	LookPath           func(file string) (string, error)
	CommandOutput      func(ctx context.Context, name string, args ...string) ([]byte, error)
	CommandOutputInDir func(ctx context.Context, dir, name string, args ...string) ([]byte, error)
	// DoctorGitHubRESTBase lets tests point the doctor GitHub token probe at
	// httptest without mutating package-global state.
	DoctorGitHubRESTBase string
	// DoctorGitLabRESTBase lets tests point the doctor GitLab token probe at
	// httptest without mutating package-global state.
	DoctorGitLabRESTBase string
	Now                  func() time.Time
	Sleep                func(time.Duration)
}

// DefaultDeps returns production dependencies.
func DefaultDeps() Deps {
	return Deps{
		In:                   os.Stdin,
		Out:                  os.Stdout,
		Err:                  os.Stderr,
		HTTPClient:           &http.Client{Timeout: 2 * time.Second},
		Executable:           os.Executable,
		StartProcess:         startProcess,
		ProcessAlive:         processalive.Alive,
		LookPath:             exec.LookPath,
		CommandOutput:        commandOutput,
		CommandOutputInDir:   commandOutputInDir,
		DoctorGitHubRESTBase: defaultDoctorGitHubRESTBase,
		DoctorGitLabRESTBase: defaultDoctorGitLabRESTBase,
		Now:                  time.Now,
		Sleep:                time.Sleep,
	}
}

func commandOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return aoprocess.CommandContext(ctx, name, args...).CombinedOutput()
}

func commandOutputInDir(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := aoprocess.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func (d Deps) withDefaults() Deps {
	def := DefaultDeps()
	if d.In == nil {
		d.In = def.In
	}
	if d.Out == nil {
		d.Out = def.Out
	}
	if d.Err == nil {
		d.Err = def.Err
	}
	if d.HTTPClient == nil {
		d.HTTPClient = def.HTTPClient
	}
	if d.Executable == nil {
		d.Executable = def.Executable
	}
	if d.StartProcess == nil {
		d.StartProcess = def.StartProcess
	}
	if d.ProcessAlive == nil {
		d.ProcessAlive = def.ProcessAlive
	}
	if d.LookPath == nil {
		d.LookPath = def.LookPath
	}
	if d.CommandOutput == nil {
		d.CommandOutput = def.CommandOutput
	}
	if d.CommandOutputInDir == nil {
		d.CommandOutputInDir = def.CommandOutputInDir
	}
	if d.DoctorGitHubRESTBase == "" {
		d.DoctorGitHubRESTBase = def.DoctorGitHubRESTBase
	}
	if d.DoctorGitLabRESTBase == "" {
		d.DoctorGitLabRESTBase = def.DoctorGitLabRESTBase
	}
	if d.Now == nil {
		d.Now = def.Now
	}
	if d.Sleep == nil {
		d.Sleep = def.Sleep
	}
	return d
}

// NewRootCommand builds a testable root command.
func NewRootCommand(deps Deps) *cobra.Command {
	deps = deps.withDefaults()
	ctx := &commandContext{deps: deps}

	root := &cobra.Command{
		Use:           "ao",
		Short:         "Agent Orchestrator",
		Long:          "Agent Orchestrator manages the local daemon that supervises parallel coding-agent sessions.",
		Version:       VersionString(),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if shouldEmitCLIInvocation(cmd) {
				ctx.emitCLIInvoked(cmd.Context(), cmd)
			}
			return nil
		},
	}
	root.SetIn(deps.In)
	root.SetOut(deps.Out)
	root.SetErr(deps.Err)
	root.CompletionOptions.DisableDefaultCmd = true
	// Tag flag-parse failures as usage errors so the entrypoint can exit 2 for
	// misuse versus 1 for runtime failures. Subcommands inherit this func.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageError{err}
	})

	root.AddCommand(newDaemonCommand())
	root.AddCommand(newStartCommand(ctx))
	root.AddCommand(newStopCommand(ctx))
	root.AddCommand(newStatusCommand(ctx))
	root.AddCommand(newDoctorCommand(ctx))
	root.AddCommand(newAgentCommand(ctx))
	root.AddCommand(newSpawnCommand(ctx))
	root.AddCommand(newSendCommand(ctx))
	root.AddCommand(newPreviewCommand(ctx))
	root.AddCommand(newBrowserCommand(ctx))
	root.AddCommand(newHooksCommand(ctx))
	root.AddCommand(newAgentProcessCommand(ctx))
	root.AddCommand(newLaunchCommand(ctx))
	root.AddCommand(newPtyHostCommand())
	root.AddCommand(newImportCommand(ctx))
	root.AddCommand(newDevCommand(ctx))
	root.AddCommand(newProjectCommand(ctx))
	root.AddCommand(newSessionCommand(ctx))
	root.AddCommand(newOrchestratorCommand(ctx))
	root.AddCommand(newPRCommand(ctx))
	root.AddCommand(newReviewCommand(ctx))
	root.AddCommand(newCompletionCommand())
	root.AddCommand(newVersionCommand())

	return root
}

type commandContext struct {
	deps Deps
}

func shouldEmitCLIInvocation(cmd *cobra.Command) bool {
	commandPath := telemetrymeta.NormalizeCommandPath(cmd.CommandPath())
	if telemetrymeta.IsRoutineInternalCLICommand(commandPath) {
		return false
	}
	switch commandPath {
	// "ao daemon"/"ao start" are supervisor-driven bootstrapping, and
	// "ao completion"/"ao help" are shell setup and self-documentation.
	// "ao pty-host" and "ao agent-process" are internal runtime processes.
	// None reflect user activity.
	case "ao daemon", "ao start", "ao completion", "ao help", "ao pty-host", "ao agent-process", "ao agent-process supervise":
		return false
	default:
		return true
	}
}

func (c *commandContext) emitCLIInvoked(ctx context.Context, cmd *cobra.Command) {
	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	_ = c.postLoopbackJSON(reqCtx, "/internal/telemetry/cli-invoked", map[string]string{
		"command":     cmd.Name(),
		"commandPath": cmd.CommandPath(),
		"actorType":   cliInvocationActorType(cmd),
	})
}

func cliInvocationActorType(cmd *cobra.Command) string {
	if strings.TrimSpace(cmd.CommandPath()) == "ao hooks" {
		return "agent"
	}
	if sessionIDPattern.MatchString(strings.TrimSpace(os.Getenv("AO_SESSION_ID"))) {
		return "agent"
	}
	return "user"
}

func (c *commandContext) emitCLIUsageError(ctx context.Context, args []string, err error) {
	command, commandPath := usageErrorCommand(args)
	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	_ = c.postLoopbackJSON(reqCtx, "/internal/telemetry/cli-usage-error", map[string]string{
		"command":     command,
		"commandPath": commandPath,
		"error":       err.Error(),
	})
}

func usageErrorCommand(args []string) (string, string) {
	// Only tokens that match registered subcommands may enter the reported
	// path: anything past the deepest match is user data (session names,
	// prompts, orchestrator names) and must never ride into telemetry.
	current := NewRootCommand(Deps{})
	tokens := []string{"ao"}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			break
		}
		var next *cobra.Command
		for _, sub := range current.Commands() {
			if sub.Name() == arg || sub.HasAlias(arg) {
				next = sub
				break
			}
		}
		if next == nil {
			break
		}
		tokens = append(tokens, arg)
		current = next
	}
	commandPath := strings.Join(tokens, " ")
	command := "ao"
	if len(tokens) > 1 {
		command = tokens[len(tokens)-1]
	}
	return command, commandPath
}

func usageArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validate(cmd, args); err != nil {
			return usageError{err}
		}
		return nil
	}
}

func noArgs(cmd *cobra.Command, args []string) error {
	if err := cobra.ExactArgs(0)(cmd, args); err != nil {
		return usageError{err}
	}
	return nil
}

func atMostOneArg(cmd *cobra.Command, args []string) error {
	if err := cobra.MaximumNArgs(1)(cmd, args); err != nil {
		return usageError{err}
	}
	return nil
}

func newDaemonCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "daemon",
		Short:  "Run the AO backend daemon",
		Hidden: true,
		Args:   noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return daemon.Run()
		},
	}
}
