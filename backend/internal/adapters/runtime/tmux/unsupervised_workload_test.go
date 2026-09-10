package tmux

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type workloadProbeRunner struct {
	panes, processes string
	failOperation    string
	err              error
	calls            []runnerCall
}

func (r *workloadProbeRunner) Run(ctx context.Context, _ []string, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, runnerCall{name: name, args: args})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	op := name
	if name != "ps" && len(args) > 0 {
		op = args[0]
	}
	if op == r.failOperation {
		return nil, r.err
	}
	switch op {
	case "has-session":
		return nil, nil
	case "display-message":
		return []byte("100\n"), nil
	case "list-panes":
		return []byte(r.panes), nil
	case "ps":
		return []byte(r.processes), nil
	default:
		return nil, fmt.Errorf("unexpected runtime operation %s", op)
	}
}

func TestUnsupervisedWorkloadLaunchShapes(t *testing.T) {
	launch := "/bin/sh -c " + buildLaunchCommand(ports.RuntimeConfig{WorkspacePath: "/work", Argv: []string{"codex"}})
	for _, tc := range []struct {
		name, panes, processes string
		alive, wantErr         bool
	}{
		{name: "live reviewer under launch wrapper", panes: "100 0", processes: "100 1 " + launch + "\n101 100 codex\n", alive: true},
		{name: "launch wrapper before child registration", panes: "100 0", processes: "100 1 " + launch + "\n", alive: true},
		{name: "exited reviewer preserved shell", panes: "100 0", processes: "100 1 /bin/zsh -i\n"},
		{name: "kernel process is unrelated", panes: "100 0", processes: "0 0 kernel_task\n100 1 /bin/zsh -i\n"},
		{name: "login shell", panes: "100 0", processes: "100 1 -bash\n"},
		{name: "manual descendant", panes: "100 0", processes: "100 1 /bin/zsh -i\n101 100 codex resume native-review\n", alive: true},
		{name: "manual exec replaces root", panes: "100 0", processes: "100 1 /opt/codex resume native-review\n", alive: true},
		{name: "manual supervisor is still a workload", panes: "100 0", processes: "100 1 /bin/zsh -i\n101 100 ao agent-process supervise --session other --launch old -- codex\n102 101 codex\n", alive: true},
		{name: "workload in another pane", panes: "100 0\n200 0", processes: "100 1 /bin/zsh -i\n200 1 /bin/bash -i\n201 200 codex\n", alive: true},
		{name: "all panes bare shells", panes: "100 0\n200 0", processes: "100 1 /bin/zsh -i\n200 1 /bin/bash -i\n"},
		{name: "retained dead pane", panes: "100 1"},
		{name: "unrelated workload", panes: "100 0", processes: "100 1 /bin/sh -i\n201 200 codex\n"},
		{name: "missing root", panes: "100 0", processes: "201 200 codex\n", wantErr: true},
		{name: "empty process snapshot", panes: "100 0", wantErr: true},
		{name: "malformed process snapshot", panes: "100 0", processes: "100 1\n", wantErr: true},
		{name: "duplicate root identity", panes: "100 0", processes: "100 1 /bin/sh -i\n100 1 codex\n", wantErr: true},
		{name: "invalid root parent", panes: "100 0", processes: "100 100 /bin/sh -i\n", wantErr: true},
		{name: "missing pane status", processes: "100 1 /bin/sh -i\n", wantErr: true},
		{name: "invalid pane status", panes: "100 unknown", processes: "100 1 /bin/sh -i\n", wantErr: true},
		{name: "invalid live pane pid", panes: "0 0", processes: "100 1 /bin/sh -i\n", wantErr: true},
		{name: "incomplete pane status", panes: "100", processes: "100 1 /bin/sh -i\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newTestRuntime(0)
			probe := &workloadProbeRunner{panes: tc.panes, processes: tc.processes}
			r.runner = probe
			alive, err := r.IsSupervisedProcessAlive(context.Background(), ports.RuntimeHandle{ID: "review-worker"}, ports.SupervisedProcessRef{})
			if alive != tc.alive || (err != nil) != tc.wantErr {
				t.Fatalf("workload = %v, %v; want alive %v, error %v", alive, err, tc.alive, tc.wantErr)
			}
			if len(probe.calls) < 2 || !reflect.DeepEqual(probe.calls[1].args, []string{"list-panes", "-s", "-t", "=review-worker", "-F", "#{pane_pid}\t#{pane_dead}"}) {
				t.Fatalf("workload probe did not inspect every pane of the exact session: %+v", probe.calls)
			}
		})
	}
}

func TestUnsupervisedWorkloadProbeErrorsBlock(t *testing.T) {
	for _, operation := range []string{"has-session", "list-panes", "ps"} {
		for _, probeErr := range []error{ports.ErrRuntimeProbeInconclusive, context.Canceled, context.DeadlineExceeded} {
			r, _ := newTestRuntime(0)
			r.runner = &workloadProbeRunner{panes: "100 0", processes: "100 1 /bin/sh -i\n", failOperation: operation, err: probeErr}
			alive, err := r.IsSupervisedProcessAlive(context.Background(), ports.RuntimeHandle{ID: "review-worker"}, ports.SupervisedProcessRef{})
			if alive || !errors.Is(err, probeErr) {
				t.Fatalf("%s failure = %v, %v; want %v", operation, alive, err, probeErr)
			}
		}
	}
}
