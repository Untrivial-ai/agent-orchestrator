package workertransport

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
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

func TestReservedAgentTerminalRejectsExcessInput(t *testing.T) {
	t.Parallel()
	supervisor := &Supervisor{
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		terminals: map[string]*terminalProcess{},
	}
	supervisor.HoldAgentInputUntilWorkspaceReady()
	if err := supervisor.ConfigureAgent(workerexec.Command{}, "agent-1"); err != nil {
		t.Fatalf("configure agent: %v", err)
	}
	block := make([]byte, 16<<10)
	for index := 0; index < maxPendingAgentInputBytes/len(block); index++ {
		if err := supervisor.writeTerminal(worker.TerminalCommand{
			TerminalID: "agent-1",
			Data:       block,
		}); err != nil {
			t.Fatalf("queue block %d: %v", index, err)
		}
	}
	if err := supervisor.writeTerminal(worker.TerminalCommand{
		TerminalID: "agent-1",
		Data:       []byte("overflow"),
	}); err == nil {
		t.Fatal("accepted input beyond the pending-agent limit")
	}
	if got := supervisor.pendingAgentInputBytes; got != maxPendingAgentInputBytes {
		t.Fatalf("pending bytes = %d, want %d", got, maxPendingAgentInputBytes)
	}
}

func TestStartAgentCleansRejectedCommand(t *testing.T) {
	t.Parallel()
	var cleanups atomic.Int64
	supervisor := &Supervisor{}
	err := supervisor.StartAgent(context.Background(), workerexec.Command{
		Cleanup: func() { cleanups.Add(1) },
	}, "")
	if err == nil {
		t.Fatal("accepted an empty agent terminal id")
	}
	if got := cleanups.Load(); got != 1 {
		t.Fatalf("command cleanup calls = %d, want 1", got)
	}
}

func TestStartAgentCleansProcessLaunchFailure(t *testing.T) {
	t.Parallel()
	invalidBinary := t.TempDir() + "/invalid-binary"
	if err := os.WriteFile(invalidBinary, []byte("not an executable image"), 0o700); err != nil {
		t.Fatalf("write invalid binary: %v", err)
	}
	var cleanups atomic.Int64
	supervisor := &Supervisor{
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		terminals: map[string]*terminalProcess{},
	}
	err := supervisor.StartAgent(context.Background(), workerexec.Command{
		Path:    invalidBinary,
		Dir:     t.TempDir(),
		Env:     map[string]string{},
		Cleanup: func() { cleanups.Add(1) },
	}, "agent-1")
	if err == nil {
		t.Fatal("started an invalid agent executable")
	}
	if got := cleanups.Load(); got != 1 {
		t.Fatalf("command cleanup calls = %d, want 1", got)
	}
	if supervisor.AgentTerminalID != "" || supervisor.agentStarting || supervisor.agentStarted {
		t.Fatalf("agent reservation survived launch failure")
	}
}

type wakeControl struct {
	Control
	claims      atomic.Int64
	waits       atomic.Int64
	secondClaim chan struct{}
	thirdClaim  chan struct{}
	waitStarted chan struct{}
	secondWait  chan struct{}
	firstOnce   sync.Once
	secondOnce  sync.Once
}

func (c *wakeControl) ClaimTransport(context.Context) (*worker.TransportRequest, error) {
	switch c.claims.Add(1) {
	case 2:
		close(c.secondClaim)
	case 3:
		close(c.thirdClaim)
	}
	return nil, nil
}

func (c *wakeControl) ClaimTurn(context.Context) (*worker.Turn, error) {
	return nil, nil
}

func (c *wakeControl) WaitForWork(ctx context.Context) error {
	switch c.waits.Add(1) {
	case 1:
		c.firstOnce.Do(func() { close(c.waitStarted) })
	case 2:
		c.secondOnce.Do(func() { close(c.secondWait) })
	}
	<-ctx.Done()
	return ctx.Err()
}

func (c *wakeControl) PublishTerminalOutput(context.Context, string, int64, []byte) error {
	return nil
}

func (c *wakeControl) PublishTerminalExit(context.Context, string, int) error {
	return nil
}

func TestAgentReadinessWakesWorkWait(t *testing.T) {
	t.Parallel()
	control := &wakeControl{
		secondClaim: make(chan struct{}),
		thirdClaim:  make(chan struct{}),
		waitStarted: make(chan struct{}),
		secondWait:  make(chan struct{}),
	}
	started := make(chan error, 1)
	supervisor := &Supervisor{
		Control:   control,
		Workspace: t.TempDir(),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Started:   started,
	}
	supervisor.HoldAgentInputUntilWorkspaceReady()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()

	if err := <-started; err != nil {
		t.Fatalf("start supervisor: %v", err)
	}
	select {
	case <-control.waitStarted:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not enter work wait")
	}
	supervisor.MarkWorkspaceReady()
	select {
	case <-control.secondClaim:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("workspace readiness did not wake the work claim loop")
	}
	select {
	case <-control.secondWait:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("supervisor did not re-enter work wait before agent start")
	}
	if err := supervisor.StartAgent(ctx, workerexec.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "sleep 5"},
		Dir:  supervisor.Workspace,
		Env:  map[string]string{},
	}, "agent-1"); err != nil {
		t.Fatalf("start agent: %v", err)
	}
	select {
	case <-control.thirdClaim:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("agent start did not wake the work claim loop")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run supervisor: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("supervisor did not stop after cancellation")
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
