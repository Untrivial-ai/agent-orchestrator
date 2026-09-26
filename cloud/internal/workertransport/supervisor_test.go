package workertransport

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workerexec"
)

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
	claims    int
	turn      *worker.Turn
	completed int
}

func (s *turnClaimSpy) ClaimTurn(context.Context) (*worker.Turn, error) {
	s.claims++
	return s.turn, nil
}

func (s *turnClaimSpy) CompleteTurn(context.Context, string, int, bool) error {
	s.completed++
	return nil
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

func TestForwardTurnEncodesMultilinePromptForAgentPTY(t *testing.T) {
	t.Parallel()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()

	prompt := "Review summary:\n- fix the race\n- add a test"
	control := &turnClaimSpy{turn: &worker.Turn{ID: "turn-1", Prompt: prompt, Attempt: 1}}
	supervisor := &Supervisor{
		Control:         control,
		AgentTerminalID: "agent-1",
		agentStarted:    true,
		workspaceReady:  true,
		terminals: map[string]*terminalProcess{
			"agent-1": {pty: writer, cancel: func() {}, cleanup: func() {}},
		},
	}

	handled, err := supervisor.forwardTurn(context.Background())
	if err != nil || !handled {
		t.Fatalf("forward multiline turn = (%v, %v), want (true, nil)", handled, err)
	}
	want := worker.EncodeTerminalInput(prompt)
	got := make([]byte, len(want))
	if err := reader.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatalf("read forwarded prompt: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("forwarded prompt = %q, want %q", got, want)
	}
	if control.completed != 1 {
		t.Fatalf("completed turns = %d, want 1", control.completed)
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
