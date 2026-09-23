package workertransport

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workerexec"
	"github.com/creack/pty"
)

type Control interface {
	ClaimTransport(context.Context) (*worker.TransportRequest, error)
	ClaimTurn(context.Context) (*worker.Turn, error)
	// WaitForWork blocks until the control plane signals a new turn/transport
	// enqueue for this session (or a short server-side timeout), replacing the
	// old busy-poll. It returns no work; the caller re-runs the claim RPCs.
	WaitForWork(context.Context) error
	CompleteTurn(context.Context, string, int, bool) error
	FailTurn(context.Context, string, int, string) error
	CompleteTransport(context.Context, string, int, any) error
	FailTransport(context.Context, string, int, string, string) error
	PublishTerminalOutput(context.Context, string, int64, []byte) error
	PublishTerminalExit(context.Context, string, int) error
}

// workWaitFallback bounds the loop's back-off when WaitForWork is unavailable
// (an older control plane without the endpoint) or errors transiently, so the
// worker degrades to a slow poll rather than a tight spin.
const workWaitFallback = 2 * time.Second

// Fallback PTY geometry when an open request carries no client dimensions. The
// agent terminal is spawned at worker boot (StartAgent), autonomously, long
// before any human attaches — so there is no viewer width to honor yet, and the
// coding agent draws its full-screen intro (the welcome box, "What's new") once,
// committing that fixed-layout box art to scrollback. Committed scrollback never
// reflows: a viewer whose pane is NARROWER than the boot width sees every line
// overflow and wrap, garbling the banner permanently (the live region still
// repaints correctly on the client's resize — only history is stuck).
//
// So the fallback must be a width the viewer is essentially always at least as
// wide as. 80 is the canonical terminal width every agent TUI is designed to
// render at, and every realistic AO viewer pane is >= 80 columns, so the banner
// renders cleanly (under-filling at worst, never overflowing). The client's
// authoritative resize immediately expands the live UI to the full pane width.
// A client-provided size, when present, always wins over these.
const (
	fallbackTerminalColumns = 80
	fallbackTerminalRows    = 24
)

type Supervisor struct {
	Control         Control
	Workspace       string
	CompareBase     string
	Shell           string
	AgentCommand    workerexec.Command
	AgentTerminalID string
	Started         chan<- error
	PollInterval    time.Duration
	Logger          *slog.Logger
	// Streams, when non-nil, holds a persistent duplex terminal stream per
	// open terminal for low-latency input/output. The polled transport stays
	// authoritative whenever a stream is absent or unhealthy.
	Streams StreamDialer

	mu                       sync.Mutex
	terminals                map[string]*terminalProcess
	holdAgentInput           bool
	workspaceReady           bool
	agentStarting            bool
	agentStarted             bool
	pendingAgentTerminalData [][]byte
	pendingAgentTerminalSize *worker.TerminalCommand
}

// HoldAgentInputUntilWorkspaceReady preserves user input and durable turns
// until the checkout has completed. Call this before Run.
func (s *Supervisor) HoldAgentInputUntilWorkspaceReady() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.holdAgentInput = true
	s.workspaceReady = false
}

// MarkWorkspaceReady releases prompts collected while the agent was waiting for
// its checkout. If the agent PTY has not started yet, the prompts remain queued
// until StartAgent makes the terminal live.
func (s *Supervisor) MarkWorkspaceReady() {
	s.mu.Lock()
	s.workspaceReady = true
	s.mu.Unlock()
	s.flushReadyAgentTerminal()
}

type terminalProcess struct {
	cancel  context.CancelFunc
	pty     *os.File
	cleanup func()
	stream  atomic.Pointer[terminalStream]
	// outputID belongs to the terminal rather than a WebSocket connection. A
	// stream redial must continue its sequence so direct relay frames and the
	// durable replay log use the same cursor.
	outputID atomic.Int64
}

func (s *Supervisor) Run(ctx context.Context) error {
	if s.Control == nil {
		return errors.New("worker transport control is required")
	}
	if s.Workspace == "" {
		return errors.New("worker transport workspace is required")
	}
	if s.Shell == "" {
		s.Shell = "/bin/sh"
	}
	if s.Logger == nil {
		s.Logger = slog.Default()
	}
	s.terminals = make(map[string]*terminalProcess)
	workspace, err := openWorkspace(s.Workspace)
	if err != nil {
		return err
	}
	workspace.compareBase = s.CompareBase
	defer workspace.Close()
	defer s.closeAllTerminals()
	if s.AgentTerminalID != "" {
		err := s.openTerminal(ctx, worker.TerminalCommand{
			TerminalID: s.AgentTerminalID,
			Kind:       "agent",
		})
		if s.Started != nil {
			s.Started <- err
		}
		if err != nil {
			return err
		}
	} else if s.Started != nil {
		s.Started <- nil
	}

	for {
		request, err := s.Control.ClaimTransport(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.Logger.Warn("claim worker transport request", "error", err)
		} else if request != nil {
			// Read-only workspace/review requests and browser fetches run off the
			// serial loop so a large-repo review (which reads every file and spawns
			// many git procs per call) or a burst of page resources cannot wedge
			// shell input and turn forwarding. Writes (workspace.write /
			// workspace.review.write) and terminal control stay serial, so the
			// review write-guard's read-check-write keeps its serialization against
			// other writes. See isConcurrentlyHandledKind.
			if isConcurrentlyHandledKind(request.Kind) {
				go s.handle(ctx, workspace, request)
				continue
			}
			s.handle(ctx, workspace, request)
			continue
		}
		handled, err := s.forwardTurn(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.Logger.Warn("forward agent message", "error", err)
		} else if handled {
			continue
		}
		// No work right now. Block until the control plane wakes us on a new
		// turn/transport enqueue (NOTIFY), or a short server-side timeout,
		// instead of busy-polling the claim routes. The claims above remain the
		// source of truth; WaitForWork is only an accelerant.
		if err := s.Control.WaitForWork(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.Logger.Warn("wait for worker work", "error", err)
			// An older control plane without the wait endpoint, or a transient
			// error: back off briefly so the loop never spins.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(workWaitFallback):
			}
		}
	}
}

// ConfigureAgent reserves an agent terminal before its PTY may be started. A
// browser can attach during checkout, so keeping this identity lets the worker
// buffer its early input and latest viewport instead of acknowledging requests
// that no process can handle yet.
func (s *Supervisor) ConfigureAgent(command workerexec.Command, terminalID string) error {
	if terminalID == "" {
		return errors.New("agent terminal id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.AgentTerminalID != "" || s.agentStarting || s.agentStarted {
		return errors.New("interactive agent terminal is already configured")
	}
	s.AgentCommand = command
	s.AgentTerminalID = terminalID
	s.agentStarting = true
	return nil
}

// DiscardConfiguredAgent releases the command cleanup when checkout fails
// before the reserved agent terminal could start.
func (s *Supervisor) DiscardConfiguredAgent(terminalID string) {
	s.mu.Lock()
	if !s.agentStarting || s.agentStarted || s.AgentTerminalID != terminalID {
		s.mu.Unlock()
		return
	}
	cleanup := s.AgentCommand.Cleanup
	s.AgentCommand = workerexec.Command{}
	s.AgentTerminalID = ""
	s.agentStarting = false
	s.pendingAgentTerminalData = nil
	s.pendingAgentTerminalSize = nil
	s.mu.Unlock()
	if cleanup != nil {
		cleanup()
	}
}

// StartAgent adds the coding-agent PTY after the workspace transport is already
// serving. ConfigureAgent may have reserved its identity while checkout ran;
// otherwise this method keeps the original one-step setup behavior.
func (s *Supervisor) StartAgent(ctx context.Context, command workerexec.Command, terminalID string) error {
	if terminalID == "" {
		return errors.New("agent terminal id is required")
	}
	s.mu.Lock()
	if s.AgentTerminalID == "" {
		s.AgentCommand = command
		s.AgentTerminalID = terminalID
		s.agentStarting = true
	} else if s.AgentTerminalID != terminalID || !s.agentStarting || s.agentStarted {
		s.mu.Unlock()
		return errors.New("interactive agent terminal is already configured")
	}
	s.mu.Unlock()
	if err := s.openTerminal(ctx, worker.TerminalCommand{TerminalID: terminalID, Kind: "agent"}); err != nil {
		s.mu.Lock()
		s.AgentCommand = workerexec.Command{}
		s.AgentTerminalID = ""
		s.agentStarting = false
		s.pendingAgentTerminalData = nil
		s.pendingAgentTerminalSize = nil
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	s.agentStarting = false
	s.agentStarted = true
	s.mu.Unlock()
	s.flushReadyAgentTerminal()
	return nil
}

func (s *Supervisor) forwardTurn(ctx context.Context) (bool, error) {
	// Do not claim a queued user turn until the agent PTY is actually live. The
	// workspace transport starts first, so claiming here would otherwise mark
	// the initial task failed while the coding agent is still booting.
	s.mu.Lock()
	agentTerminalID := s.AgentTerminalID
	workspaceReady := !s.holdAgentInput || s.workspaceReady
	agentStarted := s.agentStarted
	s.mu.Unlock()
	if agentTerminalID == "" || !agentStarted || !workspaceReady {
		return false, nil
	}
	turn, err := s.Control.ClaimTurn(ctx)
	if err != nil || turn == nil {
		return false, err
	}
	if turn.CancelRequested {
		return true, s.Control.CompleteTurn(ctx, turn.ID, turn.Attempt, true)
	}
	if err := s.writeTerminal(worker.TerminalCommand{
		TerminalID: agentTerminalID,
		Data:       []byte(turn.Prompt + "\r"),
	}); err != nil {
		if failErr := s.Control.FailTurn(
			ctx, turn.ID, turn.Attempt, err.Error(),
		); failErr != nil {
			return true, errors.Join(err, failErr)
		}
		return true, err
	}
	return true, s.Control.CompleteTurn(ctx, turn.ID, turn.Attempt, false)
}

// isConcurrentlyHandledKind reports whether a transport request is a read-only
// workspace/review request or a browser fetch that may run off the serial loop.
// Offloading these keeps an expensive review (ReviewSummary reads every tracked
// file and spawns hundreds of git procs) from blocking terminal input and turn
// forwarding. It is safe: the workspace struct is immutable after construction,
// file writes are atomic (write-temp + rename) so a concurrent read never sees a
// torn file, and every mutating kind (workspace.write, workspace.review.write)
// plus terminal control stays on the serial loop, so writes remain serialized
// against each other and the review write-guard keeps its TOCTOU-free property.
func isConcurrentlyHandledKind(kind string) bool {
	switch kind {
	case "browser.fetch",
		"workspace.list", "workspace.read", "workspace.diff", "workspace.diff-file",
		"workspace.review.summary", "workspace.review.tree", "workspace.review.search",
		"workspace.review.file", "workspace.review.diffs", "workspace.review.revision":
		return true
	default:
		return false
	}
}

func (s *Supervisor) handle(
	ctx context.Context,
	workspace *workspace,
	request *worker.TransportRequest,
) {
	var response any
	var err error
	switch request.Kind {
	case "workspace.list":
		var input worker.WorkspaceListRequest
		err = decodePayload(request.Payload, &input)
		if err == nil {
			response, err = workspace.List(input)
		}
	case "workspace.read":
		var input worker.WorkspaceReadRequest
		err = decodePayload(request.Payload, &input)
		if err == nil {
			response, err = workspace.Read(input)
		}
	case "workspace.diff-file":
		var input worker.WorkspaceDiffFileRequest
		err = decodePayload(request.Payload, &input)
		if err == nil {
			s.Logger.Info("workspace diff-file request started", "request_id", request.ID, "path", input.Path)
			response, err = workspace.DiffFile(ctx, input)
			if err != nil {
				failureCode, _ := transportError(err)
				s.Logger.Warn("workspace diff-file request failed", "request_id", request.ID, "path", input.Path, "failure_code", failureCode)
			} else {
				file := response.(worker.WorkspaceDiffFile)
				s.Logger.Info("workspace diff-file request completed", "request_id", request.ID, "path", file.Path, "size", file.Size, "binary", file.Binary, "deleted", file.Deleted, "diff_truncated", file.DiffTruncated)
			}
		}
	case "workspace.write":
		var input worker.WorkspaceWriteRequest
		err = decodePayload(request.Payload, &input)
		if err == nil {
			response, err = workspace.Write(input)
		}
	case "workspace.diff":
		response, err = workspace.Diff(ctx)
	case "workspace.review.summary":
		response, err = workspace.ReviewSummary(ctx)
	case "workspace.review.tree":
		var input worker.WorkspaceReviewTreeRequest
		err = decodePayload(request.Payload, &input)
		if err == nil {
			response, err = workspace.ReviewTree(ctx, input)
		}
	case "workspace.review.search":
		var input worker.WorkspaceReviewSearchRequest
		err = decodePayload(request.Payload, &input)
		if err == nil {
			response, err = workspace.ReviewSearch(ctx, input)
		}
	case "workspace.review.file":
		var input worker.WorkspaceReviewFileRequest
		err = decodePayload(request.Payload, &input)
		if err == nil {
			response, err = workspace.ReviewFile(ctx, input)
		}
	case "workspace.review.diffs":
		var input worker.WorkspaceReviewDiffsRequest
		err = decodePayload(request.Payload, &input)
		if err == nil {
			response, err = workspace.ReviewDiffs(ctx, input)
		}
	case "workspace.review.revision":
		var input worker.WorkspaceReviewRevisionRequest
		err = decodePayload(request.Payload, &input)
		if err == nil {
			response, err = workspace.ReviewRevision(ctx, input)
		}
	case "workspace.review.write":
		var input worker.WorkspaceReviewWriteRequest
		err = decodePayload(request.Payload, &input)
		if err == nil {
			response, err = workspace.ReviewWrite(ctx, input)
		}
	case "browser.fetch":
		var input worker.BrowserFetchRequest
		err = decodePayload(request.Payload, &input)
		if err == nil {
			response, err = fetchBrowser(ctx, input)
		}
	case "terminal.open":
		var input worker.TerminalCommand
		err = decodePayload(request.Payload, &input)
		if err == nil {
			err = s.openTerminal(ctx, input)
			response = map[string]bool{"open": err == nil}
		}
	case "terminal.input":
		var input worker.TerminalCommand
		err = decodePayload(request.Payload, &input)
		if err == nil {
			if input.TerminalID == s.AgentTerminalID {
				err = s.writeAgentPrompt(input.TerminalID, input.Data)
			} else {
				err = s.writeTerminal(input)
			}
			response = map[string]bool{"accepted": err == nil}
		}
	case "terminal.resize":
		var input worker.TerminalCommand
		err = decodePayload(request.Payload, &input)
		if err == nil {
			err = s.resizeTerminal(input)
			response = map[string]bool{"resized": err == nil}
		}
	case "terminal.close":
		var input worker.TerminalCommand
		err = decodePayload(request.Payload, &input)
		if err == nil {
			s.closeTerminal(input.TerminalID)
			response = map[string]bool{"closed": true}
		}
	default:
		err = errors.New("unsupported worker transport request")
	}
	if err == nil {
		if completeErr := s.Control.CompleteTransport(
			ctx, request.ID, request.Attempt, response,
		); completeErr != nil {
			s.Logger.Warn("complete worker transport request", "error", completeErr, "kind", request.Kind)
		}
		return
	}
	code, message := transportError(err)
	if failErr := s.Control.FailTransport(
		ctx, request.ID, request.Attempt, code, message,
	); failErr != nil {
		s.Logger.Warn("fail worker transport request", "error", failErr, "kind", request.Kind)
	}
}

func (s *Supervisor) openTerminal(ctx context.Context, input worker.TerminalCommand) error {
	if input.TerminalID == "" ||
		(input.Kind != "workspace" && input.Kind != "agent") {
		return errors.New("invalid terminal open request")
	}
	s.mu.Lock()
	if _, exists := s.terminals[input.TerminalID]; exists {
		s.mu.Unlock()
		return nil
	}
	processCtx, cancel := context.WithCancel(ctx)
	command, cleanup, err := s.terminalCommand(processCtx, input.Kind)
	if err != nil {
		cancel()
		s.mu.Unlock()
		return err
	}
	columns, rows := input.Columns, input.Rows
	if columns == 0 {
		columns = fallbackTerminalColumns
	}
	if rows == 0 {
		rows = fallbackTerminalRows
	}
	terminalPTY, err := pty.StartWithSize(command, &pty.Winsize{
		Cols: columns,
		Rows: rows,
	})
	if err != nil {
		cancel()
		cleanup()
		s.mu.Unlock()
		return err
	}
	terminal := &terminalProcess{
		cancel:  cancel,
		pty:     terminalPTY,
		cleanup: cleanup,
	}
	s.terminals[input.TerminalID] = terminal
	s.mu.Unlock()

	go s.copyTerminalOutput(processCtx, input.TerminalID, terminal)
	if s.Streams != nil {
		go s.runTerminalStream(processCtx, input.TerminalID, terminal)
	}
	go func() {
		_ = command.Wait()
		s.mu.Lock()
		current := s.terminals[input.TerminalID]
		delete(s.terminals, input.TerminalID)
		s.mu.Unlock()
		if current != nil {
			_ = current.pty.Close()
			current.cancel()
			current.cleanup()
		}
		exitCtx, exitCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer exitCancel()
		if err := s.Control.PublishTerminalExit(
			exitCtx,
			input.TerminalID,
			command.ProcessState.ExitCode(),
		); err != nil && exitCtx.Err() == nil {
			s.Logger.Warn("publish terminal exit", "error", err, "terminal_id", input.TerminalID)
		}
	}()
	return nil
}

func (s *Supervisor) terminalCommand(
	ctx context.Context,
	kind string,
) (*exec.Cmd, func(), error) {
	if kind == "agent" {
		if s.AgentCommand.Path == "" {
			return nil, func() {}, errors.New("interactive agent command is unavailable")
		}
		command := exec.CommandContext(ctx, s.AgentCommand.Path, s.AgentCommand.Args...)
		command.Dir = s.AgentCommand.Dir
		command.Env = terminalEnvironment(s.AgentCommand.Env)
		cleanup := s.AgentCommand.Cleanup
		if cleanup == nil {
			cleanup = func() {}
		}
		return command, cleanup, nil
	}
	command := exec.CommandContext(ctx, s.Shell)
	command.Dir = s.Workspace
	command.Env = terminalEnvironment(nil)
	return command, func() {}, nil
}

func terminalEnvironment(extra map[string]string) []string {
	environment := append([]string{}, os.Environ()...)
	environment = append(environment, "TERM=xterm-256color", "COLORTERM=truecolor")
	for key, value := range extra {
		environment = append(environment, key+"="+value)
	}
	return environment
}

func (s *Supervisor) copyTerminalOutput(
	ctx context.Context,
	terminalID string,
	terminal *terminalProcess,
) {
	buffer := make([]byte, 16<<10)
	for {
		count, err := terminal.pty.Read(buffer)
		if count > 0 {
			data := append([]byte(nil), buffer[:count]...)
			id := terminal.outputID.Add(1)
			if stream := terminal.stream.Load(); stream != nil && stream.sendOutput(id, data) {
				// Sent over the persistent stream. The control plane acknowledges it
				// after its durable mirror has accepted the same sequence.
			} else if outputErr := s.Control.PublishTerminalOutput(ctx, terminalID, id, data); outputErr != nil &&
				ctx.Err() == nil {
				s.Logger.Warn("publish terminal output", "error", outputErr, "terminal_id", terminalID)
			}
		}
		if err != nil {
			return
		}
	}
}

// promptEnterDelay mirrors the desktop runtimes' paste-then-Enter pause (tmux
// defaultEnterDelay, conpty ptyInputEnterDelay): a harness TUI that receives
// message text and the trailing carriage return in one write treats the whole
// burst as a paste and leaves the prompt unsubmitted (issue #2342). Splitting
// the Enter off and pausing makes it a distinct submit keypress.
const promptEnterDelay = 300 * time.Millisecond

// writeAgentPrompt delivers an injected message to the agent terminal: body
// first, a beat, then the submitting carriage return. Single keystrokes and
// data without a trailing return pass through unchanged.
func (s *Supervisor) writeAgentPrompt(terminalID string, data []byte) error {
	if len(data) < 2 || data[len(data)-1] != '\r' {
		return s.writeTerminal(worker.TerminalCommand{TerminalID: terminalID, Data: data})
	}
	if err := s.writeTerminal(worker.TerminalCommand{
		TerminalID: terminalID, Data: data[:len(data)-1],
	}); err != nil {
		return err
	}
	time.Sleep(promptEnterDelay)
	return s.writeTerminal(worker.TerminalCommand{
		TerminalID: terminalID, Data: []byte("\r"),
	})
}

func (s *Supervisor) writeTerminal(input worker.TerminalCommand) error {
	if input.TerminalID == "" || len(input.Data) == 0 || len(input.Data) > 16<<10 {
		return errors.New("invalid terminal input request")
	}
	s.mu.Lock()
	if s.agentStarting && input.TerminalID == s.AgentTerminalID {
		s.pendingAgentTerminalData = append(s.pendingAgentTerminalData, append([]byte(nil), input.Data...))
		s.mu.Unlock()
		return nil
	}
	terminal := s.terminals[input.TerminalID]
	s.mu.Unlock()
	if terminal == nil {
		return errors.New("terminal is not open")
	}
	_, err := terminal.pty.Write(input.Data)
	return err
}

func (s *Supervisor) resizeTerminal(input worker.TerminalCommand) error {
	if input.TerminalID == "" || input.Columns == 0 || input.Rows == 0 {
		return errors.New("invalid terminal resize request")
	}
	s.mu.Lock()
	if s.agentStarting && input.TerminalID == s.AgentTerminalID {
		pending := input
		s.pendingAgentTerminalSize = &pending
		s.mu.Unlock()
		return nil
	}
	terminal := s.terminals[input.TerminalID]
	s.mu.Unlock()
	if terminal == nil {
		return errors.New("terminal is not open")
	}
	return pty.Setsize(terminal.pty, &pty.Winsize{
		Cols: input.Columns,
		Rows: input.Rows,
	})
}

// flushReadyAgentTerminal delivers the input and viewport collected while the
// agent terminal was reserved but its checkout/PTY was not ready. Keep only the
// newest size: intermediate resizes are stale by definition and sending them
// would needlessly redraw the TUI before its first prompt.
func (s *Supervisor) flushReadyAgentTerminal() {
	s.mu.Lock()
	if !s.workspaceReady || !s.agentStarted {
		s.mu.Unlock()
		return
	}
	terminal := s.terminals[s.AgentTerminalID]
	if terminal == nil {
		s.mu.Unlock()
		return
	}
	pendingSize := s.pendingAgentTerminalSize
	pendingData := s.pendingAgentTerminalData
	s.pendingAgentTerminalSize = nil
	s.pendingAgentTerminalData = nil
	s.mu.Unlock()
	if pendingSize != nil {
		if err := pty.Setsize(terminal.pty, &pty.Winsize{
			Cols: pendingSize.Columns,
			Rows: pendingSize.Rows,
		}); err != nil {
			s.Logger.Warn("flush queued agent terminal resize", "error", err)
		}
	}
	for _, data := range pendingData {
		if _, err := terminal.pty.Write(data); err != nil {
			s.Logger.Warn("flush queued agent terminal input", "error", err)
			return
		}
	}
}

func (s *Supervisor) closeTerminal(id string) {
	s.mu.Lock()
	terminal := s.terminals[id]
	delete(s.terminals, id)
	s.mu.Unlock()
	if terminal != nil {
		_ = terminal.pty.Close()
		terminal.cancel()
		terminal.cleanup()
	}
}

func (s *Supervisor) closeAllTerminals() {
	s.mu.Lock()
	terminals := s.terminals
	s.terminals = make(map[string]*terminalProcess)
	s.mu.Unlock()
	for _, terminal := range terminals {
		_ = terminal.pty.Close()
		terminal.cancel()
		terminal.cleanup()
	}
}

func decodePayload(payload any, target any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func transportError(err error) (string, string) {
	switch {
	case errors.Is(err, ErrWorkspaceSnapshotStale):
		return "WORKSPACE_SNAPSHOT_STALE", "The workspace changed while it was being reviewed."
	case errors.Is(err, ErrWorkspaceFingerprintStale):
		return "WORKSPACE_FILE_STALE", "The file changed after it was opened."
	case errors.Is(err, ErrWorkspaceCommitNotFound):
		return "WORKSPACE_COMMIT_NOT_FOUND", "The requested commit is outside the workspace review range."
	case errors.Is(err, errUnsafePath):
		return "INVALID_WORKSPACE_PATH", "The requested path is outside the workspace."
	case errors.Is(err, os.ErrNotExist):
		return "WORKSPACE_NOT_FOUND", "The requested workspace path does not exist."
	case errors.Is(err, os.ErrPermission):
		return "WORKSPACE_PERMISSION_DENIED", "The worker denied access to the workspace path."
	default:
		return "WORKER_OPERATION_FAILED", "The worker could not complete the operation."
	}
}
