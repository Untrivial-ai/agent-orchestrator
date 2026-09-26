package workertransport

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

const (
	InterfaceTUI  = "tui"
	InterfaceChat = "chat"
)

type interfacePayload struct {
	SourceInterface      string `json:"sourceInterface"`
	TargetInterface      string `json:"targetInterface"`
	Policy               string `json:"policy"`
	Rollback             bool   `json:"rollback"`
	NativeConversationID string `json:"nativeConversationId"`
	SessionID            string `json:"sessionId"`
}

type interfaceInspectResult struct {
	Idle                 bool `json:"idle"`
	WaitingForInput      bool `json:"waitingForInput"`
	DecisionPending      bool `json:"decisionPending"`
	DraftPresent         bool `json:"draftPresent"`
	QuiescenceUnverified bool `json:"quiescenceUnverified"`
}

type interfaceReadyResult struct {
	Ready     bool   `json:"ready"`
	Interface string `json:"interface"`
}

// InterfaceTransition drives the run-time swap between the interactive agent
// terminal (TUI) and the headless turn-based Chat controller. It owns the
// current committed interface and the chat runner lifecycle.
type InterfaceTransition struct {
	mu             sync.Mutex
	current        string
	chatRun        context.CancelFunc
	chatDone       chan struct{}
	chatRunning    bool
	chatReady      bool
	chatGeneration uint64
	agentTermID    string
}

func (t *InterfaceTransition) Current() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.current
}

func (s *Supervisor) handleInterface(
	ctx context.Context,
	input interfacePayload,
	kind string,
) (any, error) {
	switch kind {
	case "interface.inspect":
		return s.inspectInterface()
	case "interface.native-id":
		return map[string]any{"nativeConversationId": s.nativeConversationID(ctx, input)}, nil
	case "interface.interrupt":
		return map[string]bool{"ok": true}, s.interruptInterface(ctx)
	case "interface.stop":
		if input.Rollback && s.iface.Current() != input.SourceInterface {
			return map[string]bool{"ok": true}, nil
		}
		return map[string]bool{"ok": true}, s.stopInterface(ctx, input.Policy)
	case "interface.start":
		return map[string]bool{"ok": true}, s.startInterface(ctx, input)
	case "interface.ready":
		return s.controllerReady(input.TargetInterface), nil
	default:
		return nil, errors.New("unsupported interface command")
	}
}

func (s *Supervisor) controllerReady(expected string) interfaceReadyResult {
	s.iface.mu.Lock()
	current := s.iface.current
	chatReady := s.iface.chatRunning && s.iface.chatReady
	s.iface.mu.Unlock()
	if expected != "" && current != expected {
		return interfaceReadyResult{Interface: current}
	}

	ready := false
	switch current {
	case InterfaceChat:
		ready = chatReady
	case InterfaceTUI:
		s.mu.Lock()
		agentTerminalID := s.AgentTerminalID
		_, terminalOpen := s.terminals[agentTerminalID]
		ready = s.agentStarted && terminalOpen && (!s.holdAgentInput || s.workspaceReady)
		s.mu.Unlock()
	}
	return interfaceReadyResult{Ready: ready, Interface: current}
}

func (s *Supervisor) inspectInterface() (any, error) {
	s.iface.mu.Lock()
	defer s.iface.mu.Unlock()
	if s.iface.current == InterfaceTUI {
		idle, unverified := s.tuiSourceIdle()
		return interfaceInspectResult{
			Idle:                 idle,
			QuiescenceUnverified: unverified,
		}, nil
	}
	// Chat work is headless, so it can report its actual turn activity.
	idle := true
	if s.iface.current == InterfaceChat {
		if activity, ok := s.ChatRunner.(chatActivity); ok {
			idle = activity.Idle()
		}
	}
	return interfaceInspectResult{
		Idle:            idle,
		WaitingForInput: false,
	}, nil
}

func (s *Supervisor) interruptInterface(ctx context.Context) error {
	if s.iface.Current() == InterfaceChat {
		if runner, ok := s.ChatRunner.(chatInterrupter); ok {
			runner.Interrupt()
			return nil
		}
		return errors.New("chat controller cannot interrupt the active turn")
	}
	agentTerminalID := s.agentTerminalID()
	s.mu.Lock()
	terminal := s.terminals[agentTerminalID]
	s.mu.Unlock()
	if terminal == nil {
		return errors.New("agent terminal is not open")
	}
	_, err := terminal.pty.Write([]byte{0x03})
	return err
}

func (s *Supervisor) tuiSourceIdle() (bool, bool) {
	stopAt, err := worker.ReadTUIStop(s.DataDir)
	s.mu.Lock()
	_, open := s.terminals[s.AgentTerminalID]
	startedAt, inputAt := s.tuiStartedAt, s.lastTUIInputAt
	s.mu.Unlock()
	if !open {
		return true, false
	}
	verified := err == nil && !startedAt.IsZero() && stopAt.After(startedAt) && stopAt.After(inputAt)
	return verified, !verified
}

func (s *Supervisor) stopInterface(ctx context.Context, policy string) error {
	if s.iface.Current() == InterfaceTUI {
		// The drain verdict is checked again at the actual stop boundary. A
		// keystroke arriving between inspect and stop must not be interrupted.
		stopAt, stopErr := worker.ReadTUIStop(s.DataDir)
		s.mu.Lock()
		if policy != "interrupt" {
			verified := stopErr == nil && !s.tuiStartedAt.IsZero() &&
				stopAt.After(s.tuiStartedAt) && stopAt.After(s.lastTUIInputAt)
			if !verified {
				s.mu.Unlock()
				return errors.New("terminal activity changed before stop; retry after the agent finishes")
			}
		}
		s.tuiHandoffClosing = true
		id := s.AgentTerminalID
		s.mu.Unlock()
		err := s.closeTerminalForInterfaceHandoff(ctx, id)
		if err != nil {
			s.mu.Lock()
			s.tuiHandoffClosing = false
			s.mu.Unlock()
		}
		return err
	} else {
		return s.stopChat(ctx)
	}
}

func (s *Supervisor) startInterface(ctx context.Context, input interfacePayload) error {
	if input.TargetInterface == InterfaceChat {
		return s.startChat(ctx)
	}
	// Terminal target: rebuild the interactive command only when a real native
	// conversation was observed after ChatUI's turn. For a fresh session there
	// is no identity to resume; keep the plain bootstrap command instead of
	// manufacturing a resume command for a conversation that does not exist.
	nativeConversationID := s.nativeConversationID(ctx, input)
	s.mu.Lock()
	needsFreshCommand := s.AgentCommand.Path == ""
	s.mu.Unlock()
	if nativeConversationID != "" || needsFreshCommand {
		if err := s.refreshAgentCommand(ctx, nativeConversationID); err != nil {
			return err
		}
	}
	// Chat -> TUI closes the old agent terminal record. Obtain the replacement
	// handle from the control plane before starting the PTY; otherwise the
	// worker starts a process on the stale bootstrap handle while the renderer
	// attaches to a newly ticketed handle. The two processes then diverge (and
	// Codex can report an active thread writer).
	terminal, err := s.Control.EnsureAgentTerminal(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(terminal.TerminalID) == "" {
		return errors.New("control plane returned no agent terminal")
	}
	s.setAgentTerminalID(terminal.TerminalID)
	s.iface.mu.Lock()
	s.iface.current = InterfaceTUI
	s.iface.mu.Unlock()
	if err := s.openTerminal(ctx, worker.TerminalCommand{
		TerminalID:         terminal.TerminalID,
		NextOutputSequence: terminal.NextOutputSequence,
		Kind:               "agent",
	}); err != nil {
		return err
	}
	s.mu.Lock()
	s.agentStarted = true
	s.mu.Unlock()
	s.flushReadyAgentTerminal()
	return nil
}

func (s *Supervisor) agentTerminalID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.AgentTerminalID
}

func (s *Supervisor) setAgentTerminalID(id string) {
	s.mu.Lock()
	s.AgentTerminalID = id
	s.mu.Unlock()
}

func (s *Supervisor) refreshAgentCommand(ctx context.Context, nativeConversationID string) error {
	if s.AgentCommandFactory == nil {
		return nil
	}
	command, err := s.AgentCommandFactory(ctx, nativeConversationID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.AgentCommand = command
	s.mu.Unlock()
	return nil
}

func (s *Supervisor) nativeConversationID(ctx context.Context, input interfacePayload) string {
	if id, err := s.Control.AgentSessionID(ctx); err == nil && strings.TrimSpace(id) != "" {
		return strings.TrimSpace(id)
	}
	// The worker's bootstrap value is a fallback only. Hooks can discover a
	// newer provider conversation while this worker is running (for example
	// after a ChatUI turn), so prefer the control-plane value above.
	if id := strings.TrimSpace(s.AgentSessionID); id != "" {
		return id
	}
	return strings.TrimSpace(input.NativeConversationID)
}

func (s *Supervisor) stopChat(ctx context.Context) error {
	s.iface.mu.Lock()
	cancel := s.iface.chatRun
	done := s.iface.chatDone
	s.iface.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	// The Chat runner owns the provider's native thread writer. Cancelling its
	// context only requests shutdown; do not let the TUI start until the
	// headless provider process has actually exited and released that writer.
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Supervisor) startChat(ctx context.Context) error {
	if s.ChatRunner == nil {
		return errors.New("chat controller is unavailable for this session")
	}
	s.iface.mu.Lock()
	if s.iface.chatRunning {
		s.iface.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.iface.chatGeneration++
	generation := s.iface.chatGeneration
	done := make(chan struct{})
	s.iface.chatRun = cancel
	s.iface.chatDone = done
	s.iface.chatRunning = true
	s.iface.chatReady = false
	s.iface.current = InterfaceChat
	s.iface.mu.Unlock()
	go func() {
		if ready := s.ChatWorkspaceReady; ready != nil {
			select {
			case <-runCtx.Done():
			case <-ready:
			}
		}
		if runCtx.Err() == nil {
			s.iface.mu.Lock()
			if s.iface.chatGeneration == generation {
				s.iface.chatReady = true
			}
			s.iface.mu.Unlock()
			if err := s.ChatRunner.Run(runCtx); err != nil && runCtx.Err() == nil {
				s.Logger.Warn("chat controller stopped", "error", err)
			}
		}
		s.iface.mu.Lock()
		if s.iface.chatGeneration == generation {
			s.iface.chatRun = nil
			s.iface.chatDone = nil
			s.iface.chatRunning = false
			s.iface.chatReady = false
		}
		s.iface.mu.Unlock()
		close(done)
	}()
	return nil
}
