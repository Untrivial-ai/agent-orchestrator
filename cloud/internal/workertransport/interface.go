package workertransport

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// Interface describes the two controller surfaces a cloud session can commit.
const (
	InterfaceTUI  = "tui"
	InterfaceChat = "chat"
)

// interfacePayload is the worker-side payload of an interface command.
type interfacePayload struct {
	SourceInterface      string `json:"sourceInterface"`
	TargetInterface      string `json:"targetInterface"`
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

// InterfaceTransition drives the run-time swap between the interactive agent
// terminal (TUI) and the headless turn-based Chat controller. It owns the
// current committed interface and the chat runner lifecycle.
type InterfaceTransition struct {
	mu             sync.Mutex
	current        string
	chatRun        context.CancelFunc
	chatRunning    bool
	chatGeneration uint64
	agentTermID    string
}

func (t *InterfaceTransition) Current() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.current
}

// handleInterface dispatches an interface command to the worker. kind is one of
// interface.inspect, interface.interrupt, interface.stop, interface.native-id,
// interface.start.
func (s *Supervisor) handleInterface(
	ctx context.Context,
	input interfacePayload,
	kind string,
) (any, error) {
	switch kind {
	case "interface.inspect":
		return s.inspectInterface()
	case "interface.native-id":
		return map[string]any{"nativeConversationId": s.nativeConversationID(input)}, nil
	case "interface.interrupt":
		return map[string]bool{"ok": true}, s.interruptInterface(ctx)
	case "interface.stop":
		return map[string]bool{"ok": true}, s.stopInterface(ctx)
	case "interface.start":
		return map[string]bool{"ok": true}, s.startInterface(ctx, input)
	default:
		return nil, errors.New("unsupported interface command")
	}
}

func (s *Supervisor) inspectInterface() (any, error) {
	s.iface.mu.Lock()
	defer s.iface.mu.Unlock()
	// TUI work is interactive and the worker has no turn execution to drain.
	// Chat work is headless, so it must report its actual turn activity. The old
	// implementation returned idle only for TUI, which made every Chat -> TUI
	// drain transition wait forever even when no prompt was running.
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
	s.mu.Lock()
	terminal := s.terminals[s.AgentTerminalID]
	s.mu.Unlock()
	if terminal == nil {
		return errors.New("agent terminal is not open")
	}
	_, err := terminal.pty.Write([]byte{0x03})
	return err
}

func (s *Supervisor) stopInterface(ctx context.Context) error {
	if s.iface.Current() == InterfaceTUI {
		s.closeTerminalForInterfaceHandoff(s.AgentTerminalID)
	} else {
		s.stopChat()
	}
	// A stopped controller may have published a terminal exit that the control
	// plane already consumed; nothing else is owed here.
	return nil
}

func (s *Supervisor) startInterface(ctx context.Context, input interfacePayload) error {
	if input.TargetInterface == InterfaceChat {
		return s.startChat(ctx)
	}
	// Terminal target: reopen the interactive agent PTY with the resolved
	// conversation identity. The launch command already carries the resume
	// identity, so a fresh PTY restores the same native conversation.
	s.iface.mu.Lock()
	s.iface.current = InterfaceTUI
	s.iface.mu.Unlock()
	return s.openTerminal(ctx, worker.TerminalCommand{
		TerminalID: s.AgentTerminalID,
		Kind:       "agent",
	})
}

func (s *Supervisor) nativeConversationID(input interfacePayload) string {
	if id := strings.TrimSpace(s.AgentSessionID); id != "" {
		return id
	}
	return strings.TrimSpace(input.NativeConversationID)
}

func (s *Supervisor) stopChat() {
	s.iface.mu.Lock()
	cancel := s.iface.chatRun
	s.iface.chatRun = nil
	s.iface.chatRunning = false
	s.iface.mu.Unlock()
	if cancel != nil {
		cancel()
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
	s.iface.chatRun = cancel
	s.iface.chatRunning = true
	s.iface.current = InterfaceChat
	s.iface.mu.Unlock()
	go func() {
		if err := s.ChatRunner.Run(runCtx); err != nil && runCtx.Err() == nil {
			s.Logger.Warn("chat controller stopped", "error", err)
		}
		s.iface.mu.Lock()
		if s.iface.chatGeneration == generation {
			s.iface.chatRun = nil
			s.iface.chatRunning = false
		}
		s.iface.mu.Unlock()
	}()
	return nil
}
