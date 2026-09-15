package acp

import (
	"errors"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestFinishPromptInterruptOutcome(t *testing.T) {
	for _, tt := range []struct {
		name    string
		reason  acpsdk.StopReason
		stopErr error
		state   domain.TurnState
		wantErr bool
	}{
		{"accepted refusal", acpsdk.StopReasonRefusal, nil, domain.TurnStateInterrupted, false},
		{"accepted missing reason", "", nil, domain.TurnStateInterrupted, false},
		{"accepted completion race", acpsdk.StopReasonEndTurn, nil, domain.TurnStateCompleted, false},
		{"rejected refusal", acpsdk.StopReasonRefusal, errors.New("cancel not sent"), domain.TurnStateFailed, true},
		{"rejected missing reason", "", errors.New("cancel not sent"), domain.TurnStateFailed, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			attempt := &interruptAttempt{turnID: "turn-1", done: make(chan struct{}), err: tt.stopErr}
			close(attempt.done)
			conv := &conversation{
				activeTurn: "turn-1", interrupt: attempt, events: make(chan ports.ChatEvent, 4),
			}
			conv.finishPrompt("turn-1", acpsdk.PromptResponse{StopReason: tt.reason}, nil)
			event := nextEvent(t, conv.Events())
			if event.Kind != ports.ChatEventTurnCompleted || event.TurnState != tt.state || (event.Err != nil) != tt.wantErr {
				t.Fatalf("completion = %+v, want state %s and error=%v", event, tt.state, tt.wantErr)
			}
			if conv.activeTurn != "" || conv.interrupt != nil {
				t.Fatal("completed prompt retained active turn or interrupt attempt")
			}
		})
	}
}
