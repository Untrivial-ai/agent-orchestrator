package workertransport

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workerexec"
)

type supervisorControlStub struct {
	claimTurnCalls int
	turn           *worker.Turn
	agentSessionID string
	agentTerminal  string
}

type chatRunnerStub struct{ idle bool }

func (s chatRunnerStub) Run(context.Context) error { return nil }
func (s chatRunnerStub) Idle() bool                { return s.idle }

type interruptibleChatRunner struct{ interrupted bool }

func (s *interruptibleChatRunner) Run(context.Context) error { return nil }
func (s *interruptibleChatRunner) Interrupt() bool           { s.interrupted = true; return true }

type blockingChatRunner struct{ started chan struct{} }

func (r blockingChatRunner) Run(ctx context.Context) error {
	close(r.started)
	<-ctx.Done()
	return nil
}

type delayedStoppingChatRunner struct {
	started chan struct{}
	stopped chan struct{}
	release chan struct{}
}

func (r delayedStoppingChatRunner) Run(ctx context.Context) error {
	close(r.started)
	<-ctx.Done()
	close(r.stopped)
	<-r.release
	return nil
}

func (s *supervisorControlStub) ClaimTransport(context.Context) (*worker.TransportRequest, error) {
	return nil, nil
}

func (s *supervisorControlStub) ClaimTurn(context.Context) (*worker.Turn, error) {
	s.claimTurnCalls++
	return s.turn, nil
}
func (s *supervisorControlStub) WaitForWork(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *supervisorControlStub) CompleteTurn(context.Context, string, int, bool) error { return nil }
func (s *supervisorControlStub) FailTurn(context.Context, string, int, string) error   { return nil }
func (s *supervisorControlStub) CompleteTransport(context.Context, string, int, any) error {
	return nil
}
func (s *supervisorControlStub) FailTransport(context.Context, string, int, string, string) error {
	return nil
}
func (s *supervisorControlStub) PublishTerminalOutput(context.Context, string, int64, []byte) error {
	return nil
}
func (s *supervisorControlStub) PublishTerminalExit(context.Context, string, int, bool) error {
	return nil
}
func (s *supervisorControlStub) AgentSessionID(context.Context) (string, error) {
	return s.agentSessionID, nil
}
func (s *supervisorControlStub) EnsureAgentTerminal(context.Context) (worker.AgentTerminalResponse, error) {
	return worker.AgentTerminalResponse{TerminalID: s.agentTerminal}, nil
}

func TestRefreshAgentCommandUsesLatestConversationID(t *testing.T) {
	var gotID string
	supervisor := &Supervisor{
		AgentCommand: workerexec.Command{Path: "stale-codex"},
		AgentCommandFactory: func(_ context.Context, nativeConversationID string) (workerexec.Command, error) {
			gotID = nativeConversationID
			return workerexec.Command{Path: "codex", Args: []string{"resume", nativeConversationID}}, nil
		},
	}
	if err := supervisor.refreshAgentCommand(context.Background(), "native-chat"); err != nil {
		t.Fatalf("refresh agent command: %v", err)
	}
	if gotID != "native-chat" {
		t.Fatalf("factory native conversation id = %q, want native-chat", gotID)
	}
	if supervisor.AgentCommand.Path != "codex" {
		t.Fatalf("refreshed command path = %q, want codex", supervisor.AgentCommand.Path)
	}
	if len(supervisor.AgentCommand.Args) != 2 || supervisor.AgentCommand.Args[1] != "native-chat" {
		t.Fatalf("refreshed command args = %v, want resume native-chat", supervisor.AgentCommand.Args)
	}
}

func TestStartInterfaceKeepsBootstrapCommandWithoutConversation(t *testing.T) {
	workspace := t.TempDir()
	called := false
	supervisor := &Supervisor{
		Control:   &supervisorControlStub{agentTerminal: "00000000-0000-0000-0000-000000000004"},
		Workspace: workspace,
		AgentCommand: workerexec.Command{
			Path: "/bin/sh",
			Args: []string{"-c", "sleep 30"},
			Dir:  workspace,
		},
		AgentCommandFactory: func(context.Context, string) (workerexec.Command, error) {
			called = true
			return workerexec.Command{Path: "unexpected"}, nil
		},
		AgentTerminalID: "00000000-0000-0000-0000-000000000003",
		terminals:       make(map[string]*terminalProcess),
	}

	if err := supervisor.startInterface(context.Background(), interfacePayload{
		TargetInterface: InterfaceTUI,
	}); err != nil {
		t.Fatalf("start interface: %v", err)
	}
	if called {
		t.Fatal("refreshed the agent command without a native conversation")
	}
	if supervisor.AgentTerminalID != "00000000-0000-0000-0000-000000000004" {
		t.Fatalf("agent terminal id = %q, want replacement handle", supervisor.AgentTerminalID)
	}
	supervisor.closeAllTerminals()
}

func TestOpenAgentTerminalStartsWithoutDeadlocking(t *testing.T) {
	workspace := t.TempDir()
	supervisor := &Supervisor{
		Control:   &supervisorControlStub{},
		Workspace: workspace,
		AgentCommand: workerexec.Command{
			Path: "/bin/sh",
			Args: []string{"-c", "sleep 30"},
			Dir:  workspace,
		},
		terminals: make(map[string]*terminalProcess),
	}

	done := make(chan error, 1)
	go func() {
		done <- supervisor.openTerminal(context.Background(), worker.TerminalCommand{
			TerminalID: "00000000-0000-0000-0000-000000000001",
			Kind:       "agent",
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("open agent terminal: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("open agent terminal deadlocked before starting the PTY")
	}

	supervisor.closeAllTerminals()
}

func TestStopInterfaceWaitsForInteractiveProcessExit(t *testing.T) {
	workspace := t.TempDir()
	supervisor := &Supervisor{
		Control:   &supervisorControlStub{},
		Workspace: workspace,
		AgentCommand: workerexec.Command{
			Path: "/bin/sh",
			Args: []string{"-c", "sleep 30"},
			Dir:  workspace,
		},
		AgentTerminalID: "00000000-0000-0000-0000-000000000002",
		terminals:       make(map[string]*terminalProcess),
	}
	supervisor.iface.current = InterfaceTUI

	if err := supervisor.openTerminal(context.Background(), worker.TerminalCommand{
		TerminalID: supervisor.AgentTerminalID,
		Kind:       "agent",
	}); err != nil {
		t.Fatalf("open agent terminal: %v", err)
	}
	supervisor.mu.Lock()
	process := supervisor.terminals[supervisor.AgentTerminalID]
	supervisor.mu.Unlock()
	if process == nil {
		t.Fatal("agent terminal was not registered")
	}

	if err := supervisor.stopInterface(context.Background()); err != nil {
		t.Fatalf("stop interface: %v", err)
	}
	select {
	case <-process.done:
	default:
		t.Fatal("stop interface returned before the interactive process exited")
	}
}

func TestStopInterfaceWaitsForChatProcessExit(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	release := make(chan struct{})
	supervisor := &Supervisor{
		ChatRunner: delayedStoppingChatRunner{
			started: started, stopped: stopped, release: release,
		},
	}
	supervisor.iface.current = InterfaceChat
	if err := supervisor.startChat(context.Background()); err != nil {
		t.Fatalf("start chat: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("chat controller did not start")
	}

	done := make(chan error, 1)
	go func() { done <- supervisor.stopInterface(context.Background()) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("chat controller did not receive stop")
	}
	select {
	case err := <-done:
		t.Fatalf("stop interface returned before chat process exit: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stop interface: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop interface did not wait for chat process exit")
	}
}

func TestNativeConversationIDRefreshesFromControlPlane(t *testing.T) {
	supervisor := &Supervisor{Control: &supervisorControlStub{agentSessionID: "native-chat"}}
	got := supervisor.nativeConversationID(context.Background(), interfacePayload{})
	if got != "native-chat" {
		t.Fatalf("native conversation id = %q, want native-chat", got)
	}
}

func TestNativeConversationIDPrefersRefreshedControlPlaneID(t *testing.T) {
	supervisor := &Supervisor{
		Control:        &supervisorControlStub{agentSessionID: "native-chat"},
		AgentSessionID: "stale-bootstrap-id",
	}
	if got := supervisor.nativeConversationID(context.Background(), interfacePayload{}); got != "native-chat" {
		t.Fatalf("native conversation id = %q, want refreshed control-plane id", got)
	}
}

func TestForwardTurnLeavesQueueToChatController(t *testing.T) {
	control := &supervisorControlStub{turn: &worker.Turn{ID: "turn-1", Attempt: 1}}
	supervisor := &Supervisor{Control: control}
	supervisor.iface.current = InterfaceChat

	handled, err := supervisor.forwardTurn(context.Background())
	if err != nil {
		t.Fatalf("forward turn: %v", err)
	}
	if handled {
		t.Fatal("expected transport supervisor not to handle turns in Chat mode")
	}
	if control.claimTurnCalls != 0 {
		t.Fatalf("expected Chat controller to own the queue, got %d claims", control.claimTurnCalls)
	}
}

func TestInspectInterfaceDrainsOnlyActiveChatWork(t *testing.T) {
	tests := []struct {
		name string
		idle bool
		want bool
	}{
		{name: "idle chat controller", idle: true, want: true},
		{name: "running chat turn", idle: false, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			supervisor := &Supervisor{ChatRunner: chatRunnerStub{idle: test.idle}}
			supervisor.iface.current = InterfaceChat

			result, err := supervisor.inspectInterface()
			if err != nil {
				t.Fatalf("inspect interface: %v", err)
			}
			inspection := result.(interfaceInspectResult)
			if inspection.Idle != test.want {
				t.Fatalf("idle = %v, want %v", inspection.Idle, test.want)
			}
		})
	}
}

func TestInspectInterfaceDoesNotTreatOpenTUIAsIdle(t *testing.T) {
	supervisor := &Supervisor{AgentTerminalID: "agent", terminals: map[string]*terminalProcess{"agent": {}}}
	supervisor.iface.current = InterfaceTUI
	result, err := supervisor.inspectInterface()
	if err != nil {
		t.Fatalf("inspect interface: %v", err)
	}
	inspection := result.(interfaceInspectResult)
	if inspection.Idle || !inspection.QuiescenceUnverified {
		t.Fatalf("inspection = %+v, want active/unverified", inspection)
	}
}

func TestInterruptInterfaceUsesActiveChatController(t *testing.T) {
	runner := &interruptibleChatRunner{}
	supervisor := &Supervisor{ChatRunner: runner}
	supervisor.iface.current = InterfaceChat
	if err := supervisor.interruptInterface(context.Background()); err != nil {
		t.Fatalf("interrupt chat: %v", err)
	}
	if !runner.interrupted {
		t.Fatal("chat runner was not interrupted")
	}
}

func TestRunRestartsChatControllerForChatSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan error, 1)
	runnerStarted := make(chan struct{})
	supervisor := &Supervisor{
		Control:          &supervisorControlStub{},
		Workspace:        t.TempDir(),
		Started:          started,
		InitialInterface: InterfaceChat,
		ChatRunner:       blockingChatRunner{started: runnerStarted},
		PollInterval:     time.Millisecond,
	}
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	if err := <-started; err != nil {
		t.Fatalf("start chat worker: %v", err)
	}
	select {
	case <-runnerStarted:
	case <-time.After(time.Second):
		t.Fatal("chat controller was not started for a Chat session")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("transport supervisor did not stop")
	}
}

func TestChatControllerWaitsForWorkspaceRestore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	runnerStarted := make(chan struct{})
	started := make(chan error, 1)
	supervisor := &Supervisor{
		Control: &supervisorControlStub{}, Workspace: t.TempDir(),
		InitialInterface: InterfaceChat, ChatWorkspaceReady: ready,
		ChatRunner: blockingChatRunner{started: runnerStarted},
		Started:    started,
	}
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	if err := <-started; err != nil {
		t.Fatalf("start transport: %v", err)
	}
	select {
	case <-runnerStarted:
		t.Fatal("chat controller started before workspace restore")
	default:
	}
	close(ready)
	select {
	case <-runnerStarted:
	case <-time.After(time.Second):
		t.Fatal("chat controller did not start after workspace restore")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("transport did not stop")
	}
}

// A Chat -> TUI handoff must rebuild the interactive command from the shared
// native conversation identity using each harness's own resume syntax, so the
// reopened TUI continues the same provider thread the Chat controller used.
func TestAgentCommandFactoryBuildsHarnessResumeCommands(t *testing.T) {
	tests := []struct {
		name           string
		harness        string
		mode           string
		credentialType string
		wantContains   []string
	}{
		{
			name:           "codex",
			harness:        "codex",
			mode:           "trusted",
			credentialType: "api_key",
			wantContains:   []string{"resume", "native-thread-1"},
		},
		{
			name:           "claude-code",
			harness:        "claude-code",
			mode:           "trusted",
			credentialType: "api_key",
			wantContains:   []string{"--resume", "native-claude-1"},
		},
		{
			name:           "cursor",
			harness:        "cursor",
			mode:           "trusted",
			credentialType: "api_key",
			wantContains:   []string{"--resume", "native-cursor-1"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			builder := workerexec.HarnessBuilder{
				DataDir:    dataDir,
				CodexLogin: func(_, _, _, _ string) error { return nil },
			}
			nativeID := map[string]string{
				"codex":       "native-thread-1",
				"claude-code": "native-claude-1",
				"cursor":      "native-cursor-1",
			}[test.harness]
			if test.harness == "claude-code" {
				// Claude only resumes a conversation whose JSONL transcript
				// exists under the worker's CLAUDE_CONFIG_DIR; a real handoff
				// always has one because the Chat turn wrote it.
				path := filepath.Join(
					dataDir, "claude", "projects", "workspace", nativeID+".jsonl",
				)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatalf("create claude project directory: %v", err)
				}
				if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
					t.Fatalf("write claude transcript: %v", err)
				}
			}
			launch := worker.LaunchContext{
				Harness:   test.harness,
				SessionID: "session-1",
				Mode:      test.mode,
			}
			credential := worker.CredentialResponse{
				Provider:       test.harness,
				CredentialType: test.credentialType,
				Secret:         "test-secret",
			}
			command, err := builder.BuildInteractive(launch, credential, t.TempDir())
			if err != nil {
				t.Fatalf("build interactive command: %v", err)
			}
			// Simulate the factory's restore path: the same builder, but with a
			// native conversation observed by the control plane.
			launch.AgentSessionID = nativeID
			restored, err := builder.BuildInteractive(launch, credential, t.TempDir())
			if err != nil {
				t.Fatalf("build restored command: %v", err)
			}
			argv := append([]string{restored.Path}, restored.Args...)
			joined := strings.Join(argv, " ")
			for _, want := range test.wantContains {
				if !strings.Contains(joined, want) {
					t.Fatalf("restored argv %q missing %q", joined, want)
				}
			}
			// The fresh command must not carry a resume flag: a newly
			// provisioned worker has no conversation to resume yet.
			fresh := append([]string{command.Path}, command.Args...)
			freshJoined := strings.Join(fresh, " ")
			if strings.Contains(freshJoined, "--resume") || strings.Contains(freshJoined, "resume ") {
				t.Fatalf("fresh interactive argv resumed a nonexistent conversation: %q", freshJoined)
			}
		})
	}
}

func TestReservedAgentTerminalBuffersEarlyInputAndResize(t *testing.T) {
	t.Parallel()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()

	supervisor := &Supervisor{
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		terminals: map[string]*terminalProcess{},
	}
	supervisor.HoldAgentInputUntilWorkspaceReady()
	if err := supervisor.ConfigureAgent(workerexec.Command{}, "agent-1"); err != nil {
		t.Fatalf("configure agent: %v", err)
	}
	if err := supervisor.writeTerminal(worker.TerminalCommand{
		TerminalID: "agent-1", Data: []byte("queued input"),
	}); err != nil {
		t.Fatalf("queue early input: %v", err)
	}
	if err := supervisor.resizeTerminal(worker.TerminalCommand{
		TerminalID: "agent-1", Columns: 120, Rows: 40,
	}); err != nil {
		t.Fatalf("queue early resize: %v", err)
	}

	// Checkout can finish before StartAgent has opened the PTY. That must retain
	// the buffered terminal requests rather than silently dropping them.
	supervisor.MarkWorkspaceReady()
	if got := len(supervisor.pendingAgentTerminalData); got != 1 {
		t.Fatalf("pending inputs after checkout = %d, want 1", got)
	}
	if got := supervisor.pendingAgentTerminalSize; got == nil || got.Columns != 120 || got.Rows != 40 {
		t.Fatalf("pending resize after checkout = %+v, want 120x40", got)
	}

	supervisor.mu.Lock()
	supervisor.terminals["agent-1"] = &terminalProcess{pty: writer, cancel: func() {}, cleanup: func() {}}
	supervisor.agentStarting = false
	supervisor.agentStarted = true
	supervisor.mu.Unlock()
	supervisor.flushReadyAgentTerminal()

	buffer := make([]byte, len("queued input"))
	if err := reader.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if _, err := io.ReadFull(reader, buffer); err != nil {
		t.Fatalf("read flushed input: %v", err)
	}
	if got := string(buffer); got != "queued input" {
		t.Fatalf("flushed input = %q, want %q", got, "queued input")
	}
	if supervisor.pendingAgentTerminalSize != nil || len(supervisor.pendingAgentTerminalData) != 0 {
		t.Fatalf("pending agent terminal work was not drained")
	}
}

type turnClaimSpy struct {
	Control
	claims int
}

func (s *turnClaimSpy) ClaimTurn(context.Context) (*worker.Turn, error) {
	s.claims++
	return nil, nil
}

func TestForwardTurnWaitsForReservedAgentPTY(t *testing.T) {
	t.Parallel()
	control := &turnClaimSpy{}
	supervisor := &Supervisor{
		Control:         control,
		AgentTerminalID: "agent-1",
		workspaceReady:  true,
		agentStarting:   true,
		holdAgentInput:  true,
	}

	handled, err := supervisor.forwardTurn(context.Background())
	if err != nil || handled {
		t.Fatalf("forward turn while agent starts = (%v, %v), want (false, nil)", handled, err)
	}
	if control.claims != 0 {
		t.Fatalf("claimed %d turns before the agent PTY started", control.claims)
	}

	supervisor.agentStarting = false
	supervisor.agentStarted = true
	handled, err = supervisor.forwardTurn(context.Background())
	if err != nil || handled {
		t.Fatalf("forward turn after agent start = (%v, %v), want (false, nil)", handled, err)
	}
	if control.claims != 1 {
		t.Fatalf("claimed %d turns after the agent PTY started, want 1", control.claims)
	}
}

type reviewDispatchControl struct {
	Control
	completed   any
	failureCode string
}

func (c *reviewDispatchControl) CompleteTransport(_ context.Context, _ string, _ int, response any) error {
	c.completed = response
	return nil
}

func (c *reviewDispatchControl) FailTransport(_ context.Context, _ string, _ int, code, _ string) error {
	c.failureCode = code
	return nil
}

func TestWorkspaceReviewDispatchHandlesEveryOperation(t *testing.T) {
	repo := newGitWorkspace(t)
	writeWorkspaceFile(t, repo, "README.md", "base\n")
	gitWorkspace(t, repo, "add", ".")
	gitWorkspace(t, repo, "commit", "-m", "base")
	gitWorkspace(t, repo, "update-ref", worker.WorkspaceReviewBaseRef, "HEAD")
	workspace, err := openWorkspace(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	review, err := workspace.ReviewSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		kind       string
		payload    any
		assertType func(any) bool
	}{
		{"workspace.review.summary", map[string]any{}, func(value any) bool { _, ok := value.(worker.WorkspaceReviewResponse); return ok }},
		{"workspace.review.tree", worker.WorkspaceReviewTreeRequest{}, func(value any) bool { _, ok := value.(worker.WorkspaceReviewTreeResponse); return ok }},
		{"workspace.review.search", worker.WorkspaceReviewSearchRequest{Query: "README"}, func(value any) bool { _, ok := value.(worker.WorkspaceReviewSearchResponse); return ok }},
		{"workspace.review.file", worker.WorkspaceReviewFileRequest{Path: "README.md"}, func(value any) bool { _, ok := value.(worker.WorkspaceReviewFileResponse); return ok }},
		{"workspace.review.diffs", worker.WorkspaceReviewDiffsRequest{Scope: worker.WorkspaceReviewCombined, Paths: []string{"README.md"}, ContextLines: 3, WorkspaceVersion: review.WorkspaceVersion}, func(value any) bool { _, ok := value.(worker.WorkspaceReviewDiffsResponse); return ok }},
		{"workspace.review.revision", worker.WorkspaceReviewRevisionRequest{Path: "README.md", Scope: worker.WorkspaceReviewCombined, Side: worker.WorkspaceReviewAfter, WorkspaceVersion: review.WorkspaceVersion}, func(value any) bool { _, ok := value.(worker.WorkspaceReviewRevisionResponse); return ok }},
		{"workspace.review.write", worker.WorkspaceReviewWriteRequest{Path: "README.md", Content: "changed\n", ExpectedFileFingerprint: review.Files[0].FileFingerprint}, func(value any) bool { _, ok := value.(worker.WorkspaceReviewWriteResponse); return ok }},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			control := &reviewDispatchControl{}
			supervisor := &Supervisor{Control: control, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			supervisor.handle(context.Background(), workspace, &worker.TransportRequest{ID: "request-1", Attempt: 1, Kind: tc.kind, Payload: tc.payload})
			if control.failureCode != "" || !tc.assertType(control.completed) {
				t.Fatalf("completed=%T failure=%q", control.completed, control.failureCode)
			}
		})
	}
}

func TestWorkspaceReviewDispatchMapsStaleSnapshot(t *testing.T) {
	repo := newGitWorkspace(t)
	writeWorkspaceFile(t, repo, "README.md", "base\n")
	gitWorkspace(t, repo, "add", ".")
	gitWorkspace(t, repo, "commit", "-m", "base")
	gitWorkspace(t, repo, "update-ref", worker.WorkspaceReviewBaseRef, "HEAD")
	workspace, err := openWorkspace(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	control := &reviewDispatchControl{}
	supervisor := &Supervisor{Control: control, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	supervisor.handle(context.Background(), workspace, &worker.TransportRequest{
		ID: "request-1", Attempt: 1, Kind: "workspace.review.diffs",
		Payload: worker.WorkspaceReviewDiffsRequest{Scope: worker.WorkspaceReviewCombined, Paths: []string{"README.md"}, WorkspaceVersion: "stale"},
	})
	if control.failureCode != "WORKSPACE_SNAPSHOT_STALE" || control.completed != nil {
		t.Fatalf("completed=%T failure=%q", control.completed, control.failureCode)
	}
}
