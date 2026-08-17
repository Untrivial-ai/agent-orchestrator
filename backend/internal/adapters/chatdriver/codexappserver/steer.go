package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver/codexproto"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Steering a running turn, over `turn/steer`.
//
// What this actually does, measured against codex-cli 0.146.0 rather than read off
// the schema (probe transcript summarized here because the answers were unknowns
// and belong with the code that depends on them):
//
//   - The turn id is PRESERVED. `turn/steer` returns {"turnId": "<the same id>"}
//     immediately and no new turn is started: one turn/started and one
//     turn/completed for the whole steered turn.
//   - The provider ACKNOWLEDGES it by replaying the guidance as an ordinary
//     `userMessage` item on that same turn (item/started + item/completed, carrying
//     `clientId` = the clientUserMessageId AO sent). It arrived ~5s after the call
//     returned, at the next model-request boundary, with a userPromptSubmit hook
//     firing for it exactly as a fresh turn's prompt would.
//   - In-flight work is NOT killed by the provider. The shell command AO's turn had
//     running kept streaming output across the steer; the AGENT then chose to abort
//     it (a terminalInteraction carrying ^C) and answered the new instruction. The
//     turn settled `completed`, not `interrupted` — which is the whole point of
//     steering over interrupt-and-resend.
//   - The refusals are two, and distinguishable:
//     -32600 "no active turn to steer" with no data, when nothing is in flight or
//     expectedTurnId is not the active turn; and
//     -32600 "cannot steer a compact turn" with
//     data.codexErrorInfo.activeTurnNotSteerable.turnKind = "compact", when a turn
//     is running but is of a kind that cannot take guidance.
//   - A steer sent after turn/start returned but BEFORE turn/started arrived was
//     refused with "no active turn to steer", even though AO already held the right
//     turn id. That is why the caller must wait for the provider's own
//     acknowledgement rather than trusting the id it was handed.

// Asserted here so a refactor cannot silently drop steering: the service
// feature-detects this interface, and a missed method would present as a steer
// control that reports the agent cannot do it — while the provider can.
var _ ports.ChatSteerer = (*conversation)(nil)

// Steer delivers guidance into the turn named by providerTurnID.
//
// The turn id is sent as the provider's own `expectedTurnId` precondition rather
// than being resolved to "whatever is running now". That is deliberate: between a
// user pressing send and this call landing, a turn can finish and the next queued
// one can start, and putting a correction onto work the user was not looking at is
// worse than refusing. An empty id still falls back to the conversation's last
// known turn, so a caller that has not tracked one is not forced to guess.
//
// No lock is taken. sendMu serializes turn DISPATCH, and a steer is not a dispatch:
// it neither starts nor ends a turn, and the provider's expectedTurnId check is a
// stronger guarantee than a lock AO could hold here — a turn that ends mid-flight
// makes the request fail rather than land somewhere unintended.
func (c *conversation) Steer(
	ctx context.Context,
	providerTurnID string,
	msg ports.ChatUserMessage,
) (ports.ChatTurnRef, error) {
	if strings.TrimSpace(msg.Text) == "" {
		// Same rule as SendTurn: there is no keystroke concept here, so an empty
		// steer is a caller bug rather than a way to nudge the agent.
		return ports.ChatTurnRef{}, errors.New("steer message text is empty")
	}

	if providerTurnID == "" {
		c.mu.Lock()
		providerTurnID = c.activeTurn
		c.mu.Unlock()
	}
	if providerTurnID == "" {
		return ports.ChatTurnRef{}, ports.ErrChatNoSteerableTurn
	}

	input, err := steerInput(msg)
	if err != nil {
		return ports.ChatTurnRef{}, err
	}
	params := codexproto.TurnSteerParams{
		ThreadID:       c.threadID,
		ExpectedTurnID: providerTurnID,
		Input:          input,
	}
	if msg.ClientMessageID != "" {
		// The provider echoes this back as the `clientId` on the userMessage item it
		// replays, which is how a client recognizes its own guidance in the timeline.
		params.ClientUserMessageID = &msg.ClientMessageID
	}

	// Settings are deliberately not applied. turn/steer carries no model, effort or
	// sandbox fields: a running turn's posture is fixed, and quietly dropping a
	// per-turn choice here would be less honest than never offering it.
	var resp codexproto.TurnSteerResponse
	if err := c.conn.request(ctx, codexproto.MethodTurnSteer, params, &resp); err != nil {
		if refusal := steerRefusal(err); refusal != nil {
			return ports.ChatTurnRef{}, refusal
		}
		return ports.ChatTurnRef{}, fmt.Errorf("%s: %w", codexproto.MethodTurnSteer, err)
	}

	turn := resp.TurnID
	if turn == "" {
		// Every observed response carried it, but a steer whose target AO cannot name
		// would be attributed to nothing; the requested turn is the honest fallback.
		turn = providerTurnID
	}
	c.mu.Lock()
	c.activeTurn = turn
	c.mu.Unlock()

	return ports.ChatTurnRef{ProviderTurnID: turn}, nil
}

// steerInput converts the complete provider-neutral message before the request is
// sent. Codex turn/steer accepts the same image URL input shape as turn/start;
// other structured blocks have no honest app-server representation here.
func steerInput(msg ports.ChatUserMessage) ([]codexproto.UserInput, error) {
	input := make([]codexproto.UserInput, 0, 1+len(msg.Content))
	if strings.TrimSpace(msg.Text) != "" {
		text := msg.Text
		input = append(input, codexproto.UserInput{Type: codexproto.UserInputTypeText, Text: &text})
	}
	for _, item := range msg.Content {
		switch item.Type {
		case "image":
			if item.Data == "" || !strings.HasPrefix(strings.ToLower(item.MIMEType), "image/") {
				return nil, fmt.Errorf("%w: image requires data and image MIME type", ports.ErrChatSteerContentUnsupported)
			}
			url := "data:" + item.MIMEType + ";base64," + item.Data
			input = append(input, codexproto.UserInput{Type: codexproto.UserInputTypeImage, URL: &url})
		default:
			return nil, fmt.Errorf("%w: %s", ports.ErrChatSteerContentUnsupported, item.Type)
		}
	}
	if len(input) == 0 {
		return nil, errors.New("steer message has no content")
	}
	return input, nil
}

// steerErrorData is the structured half of a steer refusal. app-server answers
// -32600 for every invalid request, so the code says nothing; this is the only
// machine-readable part.
type steerErrorData struct {
	CodexErrorInfo struct {
		ActiveTurnNotSteerable *struct {
			TurnKind string `json:"turnKind"`
		} `json:"activeTurnNotSteerable"`
	} `json:"codexErrorInfo"`
}

// steerRefusal translates a provider refusal into a typed outcome, or reports nil
// for an error that is not one.
//
// The "not steerable" case is recognized from the structured payload rather than
// from prose: the provider states it as
// `data.codexErrorInfo.activeTurnNotSteerable.turnKind`, and a field is worth
// preferring over a message that can be reworded. The "nothing to steer" case has
// no such payload, so it falls back to the single prose matcher this package
// already keeps for that refusal — the message observed is "no active turn to
// steer", which isNoActiveTurn matches.
func steerRefusal(err error) error {
	var rpcErr *rpcError
	if !errors.As(err, &rpcErr) {
		return nil
	}

	if len(rpcErr.Data) > 0 {
		var data steerErrorData
		if json.Unmarshal(rpcErr.Data, &data) == nil {
			if kind := data.CodexErrorInfo.ActiveTurnNotSteerable; kind != nil {
				// The kind is carried through because it is what makes the refusal
				// actionable: "wait for the compaction to finish" is advice, "this
				// turn cannot be steered" is not.
				return fmt.Errorf("%w: the provider is running a %s turn",
					ports.ErrChatTurnNotSteerable, kind.TurnKind)
			}
		}
	}

	if isNoActiveTurn(err) {
		return ports.ErrChatNoSteerableTurn
	}
	return nil
}
