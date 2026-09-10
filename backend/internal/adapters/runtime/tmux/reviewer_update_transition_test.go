package tmux

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/testutil/reviewerupdate"
)

// This runner models tmux/process observations while retaining the actual
// Create launch command. It never starts tmux, Codex, or system process probes.
type reviewerTransitionRunner struct {
	workloadProbeRunner
	cwd, launch string
	created     bool
	mutations   int
	paneChanged bool
	paneReads   int
}

func (r *reviewerTransitionRunner) Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name != "ps" && len(args) > 0 {
		switch args[0] {
		case "new-session":
			r.created = true
			r.mutations++
			r.cwd = args[9]
			r.launch = args[len(args)-1]
			r.processes = "100 1 /bin/sh -c " + r.launch + "\n101 100 /fixture/codex\n"
			return nil, nil
		case "kill-session":
			r.mutations++
			return nil, nil
		case "set-option":
			return nil, nil
		case "display-message":
			if args[len(args)-1] == "#{pane_current_path}" {
				return []byte(r.cwd), nil
			}
		case "list-panes":
			if !r.created {
				return nil, nil
			}
			if args[len(args)-1] == "#{pane_pid}" {
				return []byte("100"), nil
			}
			r.paneReads++
			if r.paneChanged && r.paneReads%2 == 0 {
				return []byte("200 0"), nil
			}
		}
	}
	return r.workloadProbeRunner.Run(ctx, env, name, args...)
}

func TestReviewerWorkloadUpdateTransition(t *testing.T) {
	for _, mode := range []string{"fresh", "resume", "restore"} {
		t.Run(mode, func(t *testing.T) {
			rt, _ := newTestRuntime(0)
			runner := &reviewerTransitionRunner{workloadProbeRunner: workloadProbeRunner{panes: "100 0"}}
			rt.runner = runner
			ctx := context.Background()
			f := reviewerupdate.New(ctx, t, rt, mode)
			if !strings.HasSuffix(runner.launch, `; exec "${SHELL:-/bin/sh}" -i`) || strings.Contains(runner.launch, "AO_SUPERVISED_PROCESS") {
				t.Fatalf("wrong reviewer launch shape: %s", runner.launch)
			}
			if mode != "fresh" && !strings.Contains(runner.launch, "'/fixture/codex' 'resume' 'native-history'") {
				t.Fatal("native resume argv not preserved")
			}
			mutations := runner.mutations
			f.Blocked(ctx, t, nil) // fresh/resumed Codex waiting for a task remains live
			for _, processes := range []string{
				"100 1 /bin/sh -c " + runner.launch + "\n", // startup before child registration
				"100 1 /bin/sh -i\n101 100 /fixture/codex resume native-history\n",
				"100 1 /fixture/codex resume native-history\n", // manual exec replaces root
				"100 1 /bin/sh -i\n101 100 ao agent-process supervise --session other --launch other -- codex\n",
			} {
				runner.processes = processes
				f.Blocked(ctx, t, nil)
			}
			runner.processes = "100 1 /bin/sh -i\n"
			runner.panes = "100 0\n200 0"
			f.Blocked(ctx, t, ports.ErrRuntimeProbeInconclusive)
			runner.panes = "100 0"
			runner.processes = ""
			f.Blocked(ctx, t, ports.ErrRuntimeProbeInconclusive)
			runner.processes = "100 1 /bin/sh -i\n"
			runner.paneReads = 0
			runner.paneChanged = true
			f.Blocked(ctx, t, ports.ErrRuntimeProbeInconclusive)
			runner.paneChanged = false
			for _, err := range []error{ports.ErrRuntimeProbeInconclusive, context.DeadlineExceeded} {
				runner.failOperation = "ps"
				runner.err = err
				f.Blocked(ctx, t, err)
			}
			runner.failOperation = ""
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			f.Blocked(canceled, t, context.Canceled)
			f.Ready(ctx, t) // the same launched command has exited into its retained shell
			if runner.mutations != mutations {
				t.Fatalf("snapshot/update mutated reviewer runtime: %d -> %d", mutations, runner.mutations)
			}
			if alive, err := rt.IsAlive(ctx, ports.RuntimeHandle{ID: f.Result.HandleID}); err != nil || !alive {
				t.Fatal(fmt.Sprint("retained host lost: ", alive, " ", err))
			}
		})
	}
}

func TestReviewerWorkloadUpdateRechecksActualRuntime(t *testing.T) {
	rt, _ := newTestRuntime(0)
	runner := &reviewerTransitionRunner{workloadProbeRunner: workloadProbeRunner{panes: "100 0"}}
	rt.runner = runner
	ctx := context.Background()
	f := reviewerupdate.New(ctx, t, rt, "resume")
	runner.processes = "100 1 /bin/sh -i\n"
	f.RecheckBlocked(ctx, t, func() { runner.processes = "100 1 /bin/sh -i\n101 100 /fixture/codex resume native-history\n" })
}
