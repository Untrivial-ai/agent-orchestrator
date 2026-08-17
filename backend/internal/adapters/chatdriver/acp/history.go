package acp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// historyCapture receives the session/update replay produced by ACP session/load.
// ACP deliberately replays a flat stream rather than provider turns, so user
// message ids are the durable boundaries from which AO reconstructs settled turns.
type historyCapture struct {
	sessionID       string
	events          []ports.ChatEvent
	occurrences     map[string]int
	turnID          string
	turnUserID      string
	turnHasProvider bool
	pendingUserID   string
	pendingUserText string
	fallbackID      int
}

// beginHistoryReplay diverts provider events away from the live Events channel.
// The controller has not started consuming yet, and a long transcript can be much
// larger than that channel's bounded live-stream buffer.
func (c *conversation) beginHistoryReplay(sessionID string) {
	c.mu.Lock()
	c.sessionID = sessionID
	c.activeTurn = ""
	c.messages = make(map[string]string)
	c.thoughts = make(map[string]string)
	c.nestedMessages = make(map[string]nestedMessageState)
	c.tools = make(map[string]*toolState)
	c.mu.Unlock()

	c.historyMu.Lock()
	c.history = &historyCapture{
		sessionID:   sessionID,
		occurrences: make(map[string]int),
	}
	c.historyEvents = nil
	c.historyErr = nil
	c.historyLoaded = false
	c.historyMu.Unlock()
}

func (c *conversation) abortHistoryReplay() {
	c.historyMu.Lock()
	c.history = nil
	c.historyEvents = nil
	c.historyErr = nil
	c.historyLoaded = false
	c.historyMu.Unlock()

	c.mu.Lock()
	c.activeTurn = ""
	c.mu.Unlock()
}

// finishHistoryReplay settles the last reconstructed turn, then exposes the
// captured facts through ChatHistoryReader for the controller's transactional,
// idempotent import path.
func (c *conversation) finishHistoryReplay() {
	c.historyMu.Lock()
	unsettled := c.history != nil && c.history.turnID != "" &&
		c.history.turnUserID != "" && !c.history.turnHasProvider
	if unsettled {
		c.historyEvents = nil
		c.historyErr = fmt.Errorf("%w: final replayed user turn has no provider output",
			ports.ErrChatHistoryUnsettled)
		c.historyLoaded = true
		c.history = nil
	}
	c.historyMu.Unlock()
	if unsettled {
		c.resetHistoryItems("")
		return
	}

	c.finishHistoryTurn()

	c.historyMu.Lock()
	if c.history != nil {
		c.historyEvents = append([]ports.ChatEvent(nil), c.history.events...)
		c.historyErr = nil
		c.historyLoaded = true
	}
	c.history = nil
	c.historyMu.Unlock()

	c.mu.Lock()
	c.activeTurn = ""
	c.mu.Unlock()
}

// ReadHistory implements ports.ChatHistoryReader. session/load already performed
// the provider I/O during Resume; this method only hands its normalized replay to
// the controller before live event consumption begins.
func (c *conversation) ReadHistory(ctx context.Context) ([]ports.ChatEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.historyMu.Lock()
	defer c.historyMu.Unlock()
	if !c.historyLoaded {
		return nil, fmt.Errorf("%w: ACP session/resume does not replay prior updates; session/load is required",
			ports.ErrChatHistoryUnavailable)
	}
	if c.historyErr != nil {
		return nil, c.historyErr
	}
	return append([]ports.ChatEvent(nil), c.historyEvents...), nil
}

// prepareHistoryUpdate handles the one replay notification that the ordinary live
// path intentionally ignores: user_message_chunk. For all other replay updates it
// establishes the reconstructed turn, then lets SessionUpdate's existing ACP -> AO
// normalization run unchanged.
func (c *conversation) prepareHistoryUpdate(update acpsdk.SessionUpdate) bool {
	if !c.historyReplayActive() {
		return false
	}
	if update.UserMessageChunk != nil {
		c.captureHistoryUserChunk(update.UserMessageChunk)
		return true
	}

	c.flushHistoryUserMessage()
	if seed, needsTurn := historyUpdateTurnSeed(update); needsTurn {
		c.ensureHistoryTurn(seed)
		c.historyMu.Lock()
		if c.history != nil && c.history.turnID != "" {
			c.history.turnHasProvider = true
		}
		c.historyMu.Unlock()
	}
	return false
}

func (c *conversation) historyReplayActive() bool {
	c.historyMu.Lock()
	defer c.historyMu.Unlock()
	return c.history != nil
}

func (c *conversation) captureHistoryUserChunk(chunk *acpsdk.SessionUpdateUserMessageChunk) {
	messageID := ""
	if chunk.MessageId != nil {
		messageID = strings.TrimSpace(*chunk.MessageId)
	}

	c.historyMu.Lock()
	if c.history == nil {
		c.historyMu.Unlock()
		return
	}
	if messageID == "" {
		if c.history.pendingUserID != "" {
			messageID = c.history.pendingUserID
		} else if c.history.turnUserID != "" && c.history.pendingUserText == "" {
			// An absent message id cannot describe a reliable boundary. Keep
			// contiguous chunks together; conforming agents (including Claude) send
			// stable UUIDs, so this is only a compatibility fallback.
			messageID = c.history.turnUserID
		} else {
			c.history.fallbackID++
			messageID = fmt.Sprintf("user-%d", c.history.fallbackID)
		}
	}
	finishPrevious := c.history.turnID != "" && c.history.turnUserID != messageID
	startTurn := c.history.turnID == ""
	c.historyMu.Unlock()

	if finishPrevious {
		c.finishHistoryTurn()
		startTurn = true
	}
	if startTurn {
		c.startHistoryTurn(messageID)
	}

	text := historicalUserContent(chunk.Content)
	c.historyMu.Lock()
	if c.history != nil {
		c.history.pendingUserID = messageID
		c.history.pendingUserText += text
	}
	c.historyMu.Unlock()
}

func (c *conversation) startHistoryTurn(userID string) {
	c.historyMu.Lock()
	if c.history == nil || c.history.turnID != "" {
		c.historyMu.Unlock()
		return
	}
	turnID := "acp-history-turn:" + c.history.sessionID + ":" + userID
	c.history.turnID = turnID
	c.history.turnUserID = userID
	c.history.turnHasProvider = false
	c.historyMu.Unlock()

	c.resetHistoryItems(turnID)
	c.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: turnID})
}

func (c *conversation) ensureHistoryTurn(seed string) {
	c.historyMu.Lock()
	if c.history == nil || c.history.turnID != "" {
		c.historyMu.Unlock()
		return
	}
	if seed == "" {
		c.history.fallbackID++
		seed = fmt.Sprintf("provider-%d", c.history.fallbackID)
	}
	c.historyMu.Unlock()
	c.startHistoryTurn(seed)
}

func (c *conversation) flushHistoryUserMessage() {
	c.historyMu.Lock()
	if c.history == nil || c.history.pendingUserID == "" {
		c.historyMu.Unlock()
		return
	}
	turnID := c.history.turnID
	messageID := c.history.pendingUserID
	text := c.history.pendingUserText
	c.history.pendingUserID = ""
	c.history.pendingUserText = ""
	c.historyMu.Unlock()

	if strings.TrimSpace(text) == "" {
		return
	}
	c.emit(ports.ChatEvent{
		Kind:            ports.ChatEventUserMessageCompleted,
		ProviderTurnID:  turnID,
		ProviderItemID:  messageID,
		ClientMessageID: messageID,
		Text:            text,
	})
}

func (c *conversation) finishHistoryTurn() {
	c.flushHistoryUserMessage()

	c.historyMu.Lock()
	if c.history == nil || c.history.turnID == "" {
		c.historyMu.Unlock()
		return
	}
	turnID := c.history.turnID
	c.historyMu.Unlock()

	c.settleOpenItems(turnID)
	c.emit(ports.ChatEvent{
		Kind:           ports.ChatEventTurnCompleted,
		ProviderTurnID: turnID,
		TurnState:      domain.TurnStateCompleted,
	})

	c.historyMu.Lock()
	if c.history != nil && c.history.turnID == turnID {
		c.history.turnID = ""
		c.history.turnUserID = ""
		c.history.turnHasProvider = false
		c.history.pendingUserID = ""
		c.history.pendingUserText = ""
	}
	c.historyMu.Unlock()
	c.resetHistoryItems("")
}

func (c *conversation) resetHistoryItems(turnID string) {
	c.mu.Lock()
	c.activeTurn = turnID
	c.messages = make(map[string]string)
	c.thoughts = make(map[string]string)
	c.nestedMessages = make(map[string]nestedMessageState)
	c.tools = make(map[string]*toolState)
	c.mu.Unlock()
}

// captureHistoryEvent gives replayed updates stable identities and keeps them out
// of the live stream. The occurrence count distinguishes streamed chunks belonging
// to the same provider item while remaining stable across an identical replay.
func (c *conversation) captureHistoryEvent(event ports.ChatEvent) bool {
	c.historyMu.Lock()
	defer c.historyMu.Unlock()
	if c.history == nil {
		return false
	}
	if event.ProviderEventID == "" {
		base := strings.Join([]string{
			c.history.sessionID,
			string(event.Kind),
			event.ProviderTurnID,
			event.ProviderItemID,
			event.ClientMessageID,
			event.RequestID,
		}, "\x00")
		occurrence := c.history.occurrences[base]
		c.history.occurrences[base] = occurrence + 1
		digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", base, occurrence)))
		event.ProviderEventID = fmt.Sprintf("acp-history:%x", digest)
	}
	c.history.events = append(c.history.events, event)
	return true
}

func historyUpdateTurnSeed(update acpsdk.SessionUpdate) (string, bool) {
	switch {
	case update.AgentMessageChunk != nil:
		return optionalMessageID(update.AgentMessageChunk.MessageId), true
	case update.AgentThoughtChunk != nil:
		return optionalMessageID(update.AgentThoughtChunk.MessageId), true
	case update.ToolCall != nil:
		return string(update.ToolCall.ToolCallId), true
	case update.ToolCallUpdate != nil:
		return string(update.ToolCallUpdate.ToolCallId), true
	case update.Plan != nil:
		return "plan", true
	default:
		return "", false
	}
}

func optionalMessageID(id *string) string {
	if id == nil {
		return ""
	}
	return strings.TrimSpace(*id)
}

func historicalUserContent(content acpsdk.ContentBlock) string {
	switch {
	case content.Text != nil:
		return content.Text.Text
	case content.Image != nil:
		return "[Image]"
	case content.Audio != nil:
		return "[Audio]"
	case content.ResourceLink != nil:
		return fmt.Sprintf("[File: %s]", content.ResourceLink.Name)
	case content.Resource != nil:
		return "[Embedded context]"
	default:
		return ""
	}
}
