package workerexec

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

type controlStub struct {
	turn       *worker.Turn
	credential worker.CredentialResponse

	outputs   []worker.OutputEvent
	activity  []worker.ActivityEvent
	completed bool
	cancelled bool
	failed    string
}

func (c *controlStub) ClaimTurn(context.Context) (*worker.Turn, error) {
	turn := c.turn
	c.turn = nil
	return turn, nil
}

func (c *controlStub) Credential(context.Context) (worker.CredentialResponse, error) {
	return c.credential, nil
}

func (c *controlStub) PublishOutput(_ context.Context, output worker.OutputEvent) error {
	c.outputs = append(c.outputs, output)
	return nil
}

func (c *controlStub) PublishActivity(_ context.Context, activity worker.ActivityEvent) error {
	c.activity = append(c.activity, activity)
	return nil
}

func (c *controlStub) CancellationRequested(context.Context, string, int) (bool, error) {
	return false, nil
}

func (c *controlStub) CompleteTurn(_ context.Context, _ string, _ int, cancelled bool) error {
	c.completed = true
	c.cancelled = cancelled
	return nil
}

func (c *controlStub) FailTurn(_ context.Context, _ string, _ int, message string) error {
	c.failed = message
	return nil
}

type builderStub struct {
	command Command
	err     error
}

func (b builderStub) Build(context.Context, worker.Turn, worker.CredentialResponse, string) (Command, error) {
	return b.command, b.err
}

type runnerStub struct {
	outputs []Output
	err     error
}

func (r runnerStub) Run(_ context.Context, _ Command, emit func(Output) error) error {
	for _, output := range r.outputs {
		if err := emit(output); err != nil {
			return err
		}
	}
	return r.err
}

func testSupervisor(control *controlStub, builder builderStub, runner Runner) *Supervisor {
	return &Supervisor{
		Control:         control,
		Builder:         builder,
		Runner:          runner,
		Workspace:       ".",
		PollInterval:    time.Millisecond,
		CancelInterval:  time.Millisecond,
		CompletionRetry: time.Millisecond,
	}
}

// Every supported harness's headless Chat turn must project exactly one
// assistant reply and publish the provider-native conversation identity so the
// same thread can be resumed by the interactive TUI after a handoff.
func TestExecuteProjectsHeadlessReplyAndIdentityPerHarness(t *testing.T) {
	tests := []struct {
		name       string
		harness    string
		outputs    []Output
		wantTexts  []string
		wantNative string
	}{
		{
			name:    "codex",
			harness: "codex",
			outputs: []Output{
				{Stream: "stdout", Text: `{"type":"thread.started","thread_id":"codex-native-1"}` + "\n"},
				{Stream: "stdout", Text: `{"type":"item.completed","item":{"type":"agent_message","text":"codex reply"}}` + "\n"},
			},
			wantTexts:  []string{"codex reply"},
			wantNative: "codex-native-1",
		},
		{
			name:    "claude-code",
			harness: "claude-code",
			outputs: []Output{
				{Stream: "stdout", Text: `{"type":"system","subtype":"init","session_id":"claude-native-1"}` + "\n"},
				{Stream: "stdout", Text: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"claude reply"}]}}` + "\n"},
				{Stream: "stdout", Text: `{"type":"result","subtype":"success","is_error":false,"result":"claude reply","session_id":"claude-native-1"}` + "\n"},
			},
			wantTexts:  []string{"claude reply"},
			wantNative: "claude-native-1",
		},
		{
			name:    "cursor",
			harness: "cursor",
			outputs: []Output{
				{Stream: "stdout", Text: `{"type":"system","subtype":"init","session_id":"cursor-native-1"}` + "\n"},
				{Stream: "stdout", Text: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"cursor reply"}]}}` + "\n"},
				{Stream: "stdout", Text: `{"type":"result","subtype":"success","is_error":false,"result":"cursor reply","session_id":"cursor-native-1"}` + "\n"},
			},
			wantTexts:  []string{"cursor reply"},
			wantNative: "cursor-native-1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			control := &controlStub{
				turn:       &worker.Turn{ID: "turn-1", Attempt: 1, Harness: test.harness, Prompt: "hi"},
				credential: worker.CredentialResponse{Provider: test.harness, CredentialType: "api_key", Secret: "s"},
			}
			supervisor := testSupervisor(
				control,
				builderStub{command: Command{Path: "/bin/echo", Dir: "."}},
				runnerStub{outputs: test.outputs},
			)
			if err := supervisor.execute(context.Background(), *control.turn); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if !control.completed || control.cancelled {
				t.Fatalf("completed=%v cancelled=%v, want a completed non-cancelled turn", control.completed, control.cancelled)
			}
			if control.failed != "" {
				t.Fatalf("failed with %q, want success", control.failed)
			}
			var gotTexts []string
			for _, output := range control.outputs {
				if output.Stream == "stdout" {
					gotTexts = append(gotTexts, output.Text)
				}
			}
			if !reflect.DeepEqual(gotTexts, test.wantTexts) {
				t.Fatalf("assistant texts = %#v, want %#v", gotTexts, test.wantTexts)
			}
			var identities []string
			for _, activity := range control.activity {
				if activity.Event == "session-start" {
					identities = append(identities, activity.AgentSessionID)
				}
			}
			if !reflect.DeepEqual(identities, []string{test.wantNative}) {
				t.Fatalf("published identities = %#v, want %#v", identities, []string{test.wantNative})
			}
		})
	}
}

func TestExecuteFailsTurnWhenHarnessErrors(t *testing.T) {
	for _, harness := range []string{"codex", "claude-code", "cursor"} {
		t.Run(harness, func(t *testing.T) {
			control := &controlStub{
				turn:       &worker.Turn{ID: "turn-1", Attempt: 1, Harness: harness, Prompt: "hi"},
				credential: worker.CredentialResponse{Provider: harness, CredentialType: "api_key", Secret: "s"},
			}
			supervisor := testSupervisor(
				control,
				builderStub{command: Command{Path: "/bin/echo", Dir: "."}},
				runnerStub{err: errors.New("provider process exited")},
			)
			if err := supervisor.execute(context.Background(), *control.turn); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if control.completed {
				t.Fatal("expected the turn to fail, not complete")
			}
			if control.failed == "" {
				t.Fatal("expected the turn failure to be reported to the control plane")
			}
		})
	}
}

func TestExecuteFailsTurnWhenBuilderRejects(t *testing.T) {
	control := &controlStub{
		turn:       &worker.Turn{ID: "turn-1", Attempt: 1, Harness: "cursor", Prompt: "hi", Mode: "read-only"},
		credential: worker.CredentialResponse{Provider: "cursor", CredentialType: "api_key", Secret: "s"},
	}
	supervisor := testSupervisor(
		control,
		builderStub{err: ErrUnsupportedPolicy},
		runnerStub{},
	)
	if err := supervisor.execute(context.Background(), *control.turn); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if control.failed == "" {
		t.Fatal("expected a policy-rejected command to fail the turn")
	}
}

// A busy Chat controller must not read as idle: the interface coordinator
// drains on this signal before committing a Chat -> TUI handoff. The busy flag
// is owned by the polling loop, so the test drives Run end to end.
func TestSupervisorBusyDuringTurn(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runner := runnerFunc(func(_ context.Context, _ Command, _ func(Output) error) error {
		close(started)
		<-release
		return nil
	})
	control := &controlStub{
		turn:       &worker.Turn{ID: "turn-1", Attempt: 1, Harness: "codex", Prompt: "hi"},
		credential: worker.CredentialResponse{Provider: "codex", CredentialType: "api_key", Secret: "s"},
	}
	supervisor := testSupervisor(control, builderStub{command: Command{Path: "/bin/echo", Dir: "."}}, runner)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("turn did not start executing")
	}
	if supervisor.Idle() {
		cancel()
		t.Fatal("supervisor reported idle while a turn was executing")
	}
	close(release)
	// Wait until the turn completes, then the loop must report idle again.
	deadline := time.After(2 * time.Second)
	for !supervisor.Idle() || !control.completed {
		select {
		case <-deadline:
			cancel()
			t.Fatal("supervisor stayed busy after the turn finished")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not stop after cancel")
	}
}

type runnerFunc func(context.Context, Command, func(Output) error) error

func (f runnerFunc) Run(ctx context.Context, command Command, emit func(Output) error) error {
	return f(ctx, command, emit)
}

// Process-level integration: the real OSRunner streams each harness's NDJSON
// protocol through the projector, so chunked stdout (the OS does not guarantee
// line-aligned reads) still produces exactly one projected reply and one
// native conversation identity per harness.
func TestExecuteWithOSRunnerProjectsHarnessProtocols(t *testing.T) {
	tests := []struct {
		name       string
		harness    string
		script     string
		wantText   string
		wantNative string
	}{
		{
			name:    "codex",
			harness: "codex",
			script: `printf '%s\n' '{"type":"thread.started","thread_id":"codex-native-9"}' ` +
				`'{"type":"item.completed","item":{"type":"agent_message","text":"codex os reply"}}'`,
			wantText:   "codex os reply",
			wantNative: "codex-native-9",
		},
		{
			name:    "claude-code",
			harness: "claude-code",
			script: `printf '%s\n' '{"type":"system","subtype":"init","session_id":"claude-native-9"}' ` +
				`'{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"claude os reply"}]}}' ` +
				`'{"type":"result","subtype":"success","is_error":false,"result":"claude os reply","session_id":"claude-native-9"}'`,
			wantText:   "claude os reply",
			wantNative: "claude-native-9",
		},
		{
			name:    "cursor",
			harness: "cursor",
			script: `printf '%s\n' '{"type":"system","subtype":"init","session_id":"cursor-native-9"}' ` +
				`'{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"cursor os reply"}]}}' ` +
				`'{"type":"result","subtype":"success","is_error":false,"result":"cursor os reply","session_id":"cursor-native-9"}'`,
			wantText:   "cursor os reply",
			wantNative: "cursor-native-9",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			control := &controlStub{
				turn:       &worker.Turn{ID: "turn-1", Attempt: 1, Harness: test.harness, Prompt: "hi"},
				credential: worker.CredentialResponse{Provider: test.harness, CredentialType: "api_key", Secret: "s"},
			}
			supervisor := testSupervisor(
				control,
				builderStub{command: Command{Path: "/bin/sh", Args: []string{"-c", test.script}, Dir: "."}},
				OSRunner{},
			)
			if err := supervisor.execute(context.Background(), *control.turn); err != nil {
				t.Fatalf("execute: %v", err)
			}
			var gotTexts []string
			for _, output := range control.outputs {
				if output.Stream == "stdout" {
					gotTexts = append(gotTexts, output.Text)
				}
			}
			if !reflect.DeepEqual(gotTexts, []string{test.wantText}) {
				t.Fatalf("assistant texts = %#v, want %#v", gotTexts, []string{test.wantText})
			}
			for _, activity := range control.activity {
				if activity.Event == "session-start" && activity.AgentSessionID == test.wantNative {
					return
				}
			}
			t.Fatalf("native identity %q was not published; activity = %#v", test.wantNative, control.activity)
		})
	}
}
