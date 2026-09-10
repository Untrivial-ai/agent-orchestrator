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
	CompleteTurn(context.Context, string, int, bool) error
	FailTurn(context.Context, string, int, string) error
	CompleteTransport(context.Context, string, int, any) error
	FailTransport(context.Context, string, int, string, string) error
	PublishTerminalOutput(context.Context, string, []byte) error
	PublishTerminalExit(context.Context, string, int) error
}

type Supervisor struct {
	Control         Control
	Workspace       string
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

	mu        sync.Mutex
	terminals map[string]*terminalProcess
}

type terminalProcess struct {
	cancel  context.CancelFunc
	pty     *os.File
	cleanup func()
	stream  atomic.Pointer[terminalStream]
}

func (s *Supervisor) Run(ctx context.Context) error {
	if s.Control == nil {
		return errors.New("worker transport control is required")
	}
	if s.Workspace == "" {
		return errors.New("worker transport workspace is required")
	}
	if s.PollInterval <= 0 {
		s.PollInterval = 100 * time.Millisecond
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

	ticker := time.NewTicker(s.PollInterval)
	defer ticker.Stop()
	for {
		request, err := s.Control.ClaimTransport(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.Logger.Warn("claim worker transport request", "error", err)
		} else if request != nil {
			// A page usually loads several resources at once. Keep those fetches
			// from blocking shell input while still relying on the durable command
			// queue for the per-session concurrency ceiling.
			if request.Kind == "browser.fetch" {
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
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Supervisor) forwardTurn(ctx context.Context) (bool, error) {
	turn, err := s.Control.ClaimTurn(ctx)
	if err != nil || turn == nil {
		return false, err
	}
	if turn.CancelRequested {
		return true, s.Control.CompleteTurn(ctx, turn.ID, turn.Attempt, true)
	}
	if s.AgentTerminalID == "" {
		return true, s.Control.FailTurn(
			ctx, turn.ID, turn.Attempt, "interactive agent terminal is unavailable",
		)
	}
	if err := s.writeTerminal(worker.TerminalCommand{
		TerminalID: s.AgentTerminalID,
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
	case "workspace.write":
		var input worker.WorkspaceWriteRequest
		err = decodePayload(request.Payload, &input)
		if err == nil {
			response, err = workspace.Write(input)
		}
	case "workspace.diff":
		response, err = workspace.Diff(ctx)
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
			err = s.writeTerminal(input)
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
		columns = 120
	}
	if rows == 0 {
		rows = 40
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
			if stream := terminal.stream.Load(); stream != nil && stream.sendOutput(data) {
				// Persisted (and acked) by the control plane over the stream.
			} else if outputErr := s.Control.PublishTerminalOutput(ctx, terminalID, data); outputErr != nil &&
				ctx.Err() == nil {
				s.Logger.Warn("publish terminal output", "error", outputErr, "terminal_id", terminalID)
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *Supervisor) writeTerminal(input worker.TerminalCommand) error {
	if input.TerminalID == "" || len(input.Data) == 0 || len(input.Data) > 16<<10 {
		return errors.New("invalid terminal input request")
	}
	s.mu.Lock()
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
