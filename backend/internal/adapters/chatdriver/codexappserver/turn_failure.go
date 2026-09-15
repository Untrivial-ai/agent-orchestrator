package codexappserver

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver/codexproto"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Mark synthesized explanations so history reconciliation can prefer a stored
// diagnosis without depending on this adapter or comparing display text.
type turnFailureFallback string

func (e turnFailureFallback) Error() string { return string(e) }

// ChatFailureFallback identifies an explanation synthesized without provider details.
func (e turnFailureFallback) ChatFailureFallback() bool { return true }

func nativeTurnError(turn codexproto.Turn) error {
	if turn.Error != nil && strings.TrimSpace(turn.Error.Message) != "" {
		return errors.New(turn.Error.Message)
	}
	status := string(turn.Status)
	if turnStateFrom(status) != domain.TurnStateFailed {
		return nil
	}
	switch status {
	case "failed":
		return turnFailureFallback("Codex reported that the turn failed without providing an error message.")
	case "":
		return turnFailureFallback("Codex ended the turn without reporting a status or an error message.")
	default:
		return turnFailureFallback(fmt.Sprintf("Codex ended the turn with an unrecognized status %q and no error message.", status))
	}
}
