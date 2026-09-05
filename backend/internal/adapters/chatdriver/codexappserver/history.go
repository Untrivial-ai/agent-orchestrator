package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver/codexproto"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Conversation history operations: rollback, fork, and the thread title.
//
// Each wire shape below was measured against a live `codex app-server` 0.146.0
// rather than read off the generated schema, because two of them do not do what
// their names suggest:
//
//   - thread/rollback takes {threadId, numTurns} and drops numTurns turns from the
//     END of the thread. It takes no turn id at all, which is why rolling back to a
//     turn the user pointed at has to read the thread's own turn list and count
//     backwards from it.
//   - thread/fork takes {threadId, lastTurnId?} and answers with a NEW thread id.
//     Omitting cwd makes the fork inherit the source thread's working directory,
//     which is the only directory whose files match the history being copied.
//
// The provider's refusals are as load-bearing as its successes, so they are
// recorded here too. All three arrive as code -32600:
//
//	rollback mid-turn      "Cannot rollback while a turn is in progress."
//	rollback numTurns < 1  "numTurns must be >= 1"
//	fork through live turn "lastTurnId '...' identifies an in-progress turn"
//	blank title            "thread name must not be empty"
//	unloaded thread        "thread not found: <id>"
//
// thread/rollback is annotated DEPRECATED in the protocol schema, and the
// provider's own `undo` experimental feature already reports stage "removed". It
// works on 0.146.0. When it goes, the replacement is thread/fork with lastTurnId,
// which yields a different thread id — something ChatRollbacker cannot report, so
// that day needs a port change and not a swap of the call in here.

// Asserted so a refactor cannot silently drop one of these: the service
// feature-detects each interface, and a missing method would read as "the provider
// cannot do this" with nothing anywhere to notice.
var (
	_ ports.ChatRollbacker            = (*conversation)(nil)
	_ ports.ChatForker                = (*conversation)(nil)
	_ ports.ChatRenamer               = (*conversation)(nil)
	_ ports.ChatHistoryReader         = (*conversation)(nil)
	_ ports.ChatHistoryRefresher      = (*conversation)(nil)
	_ ports.ChatLiveRecoveryReader    = (*conversation)(nil)
	_ ports.ChatLiveRecoveryFinalizer = (*conversation)(nil)
)

// providerRefusal marks a request the provider rejected on its own terms, as
// opposed to one that never got through.
//
// The distinction has to survive the trip out of this package: a refusal is a
// conflict the user can act on ("stop the turn first"), while a transport failure
// is an internal error. Reporting the first as the second is how a perfectly
// ordinary "not right now" reaches someone as "Internal server error".
//
// It travels as a behaviour rather than a sentinel because the layer that needs to
// classify it — the Chat service — must not import an adapter, and this adapter
// must not own service vocabulary. A one-method interface satisfied structurally
// is what lets both stay put.
type providerRefusal struct{ err error }

func (e *providerRefusal) Error() string { return e.err.Error() }
func (e *providerRefusal) Unwrap() error { return e.err }

// ChatRefusal reports that the provider declined the request and the conversation
// is otherwise healthy.
func (e *providerRefusal) ChatRefusal() bool { return true }

// refuse builds a refusal AO decided on itself, for the cases where the provider
// would refuse anyway and saying so locally keeps the message about the caller's
// input instead of about the protocol.
func refuse(format string, args ...any) error {
	return &providerRefusal{err: fmt.Errorf(format, args...)}
}

// asRefusal reclassifies a provider error when the provider itself said no.
//
// app-server answers -32600 for anything it considers invalid or not permitted at
// this moment, and that is the only class where the connection is fine and the
// request is the thing at fault. Everything else — a dead process, a decode
// failure, a context deadline — stays an error.
func asRefusal(err error) error {
	var rpcErr *rpcError
	if errors.As(err, &rpcErr) && rpcErr.Code == -32600 {
		return &providerRefusal{err: err}
	}
	return err
}

// providerTurn is the subset of a thread's turn list AO reads.
type providerTurn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Newer app-server builds page native history instead of hydrating it through
// thread/read. These shapes intentionally live beside the compatibility logic
// rather than in codexproto: that package is generated from the oldest Codex
// version AO still supports, where this method does not exist.
type threadTurnsListParams struct {
	ThreadID      string                   `json:"threadId"`
	Cursor        *string                  `json:"cursor,omitempty"`
	Limit         uint32                   `json:"limit"`
	SortDirection string                   `json:"sortDirection"`
	ItemsView     codexproto.TurnItemsView `json:"itemsView"`
}

type threadTurnsListResponse struct {
	Data       []codexproto.Turn `json:"data"`
	NextCursor *string           `json:"nextCursor,omitempty"`
}

const (
	methodThreadTurnsList = "thread/turns/list"
	historyTurnPageSize   = 100
)

var errThreadHistoryUnmaterialized = errors.New("codex thread history is not materialized")

// ReadHistory returns a settled, provider-neutral replay of the native Codex
// thread. It is how a TUI -> Chat handoff makes the work already visible in the
// terminal visible in AO's structured timeline as well; resuming the thread alone
// preserves model context but does not make app-server re-emit old notifications.
func (c *conversation) ReadHistory(ctx context.Context) ([]ports.ChatEvent, error) {
	turns, err := c.readHistoryTurns(ctx)
	if err != nil {
		return nil, err
	}
	return c.historyEvents(codexproto.Thread{ID: c.threadID, Turns: turns}, false)
}

// RecoverLive establishes an ordered boundary on the initialized connection
// that survived the daemon. thread/read is processed by Codex after all earlier
// provider output; requestThreadReadAfterInboundCapture additionally waits
// until AO has normalized every earlier notification and registered every
// earlier server request. No persistent-host protocol change is required.
func (c *conversation) RecoverLive(ctx context.Context) (ports.ChatLiveRecoverySnapshot, error) {
	if !c.proc.reconnected {
		return ports.ChatLiveRecoverySnapshot{}, fmt.Errorf(
			"%w: provider process was not reconnected", ports.ErrChatRecoveryInconclusive)
	}
	c.recoveryMu.Lock()
	recovering := c.recoveringLive && !c.recoveryStarted
	if recovering {
		c.recoveryStarted = true
	}
	c.recoveryMu.Unlock()
	if !recovering {
		return ports.ChatLiveRecoverySnapshot{}, fmt.Errorf(
			"%w: live provider recovery was already started or finalized",
			ports.ErrChatRecoveryInconclusive)
	}
	turns, paginated, turnsErr := c.readPaginatedTurns(ctx)
	var resp codexproto.ThreadReadResponse
	params := codexproto.ThreadReadParams{ThreadID: c.threadID}
	if !paginated {
		// Codex 0.146 has no paginated history method. Its thread/read response is
		// the final ordered boundary and must include the turns in the same RPC.
		includeTurns := true
		params.IncludeTurns = &includeTurns
	}
	boundary, established, requestErr := c.conn.requestThreadReadAfterInboundCapture(
		ctx,
		params,
		&resp,
	)
	snapshot := ports.ChatLiveRecoverySnapshot{}
	if established {
		// An RPC/decode error still follows a real response boundary. Transfer the
		// captured prefix before reporting that the authoritative snapshot failed,
		// otherwise the host has already replayed frames that no retry can recover.
		snapshot.ReplayEvents = c.captureLiveRecoveryBoundary(boundary)
	} else {
		c.abandonLiveRecovery()
	}
	if requestErr != nil {
		return snapshot, fmt.Errorf(
			"%w: read surviving provider state: %w",
			ports.ErrChatRecoveryInconclusive, requestErr)
	}
	historyUnmaterialized := errors.Is(turnsErr, errThreadHistoryUnmaterialized)
	if turnsErr != nil && !historyUnmaterialized {
		return snapshot, fmt.Errorf(
			"%w: read surviving provider history: %w",
			ports.ErrChatRecoveryInconclusive, turnsErr)
	}
	if resp.Thread.ID != c.threadID {
		return snapshot, fmt.Errorf(
			"%w: thread/read returned %q for %q",
			ports.ErrChatRecoveryInconclusive, resp.Thread.ID, c.threadID)
	}
	if paginated {
		// The metadata read is deliberately after every page. It is the ordered
		// provider boundary for notifications that raced with pagination; inject
		// the complete page result only after that boundary is established.
		resp.Thread.Turns = turns
	}
	activity, err := liveRecoveryActivity(resp.Thread.Status)
	if err != nil {
		return snapshot, err
	}
	if historyUnmaterialized && activity != domain.ActivityIdle {
		return snapshot, fmt.Errorf(
			"%w: unmaterialized provider history reported activity %s",
			ports.ErrChatRecoveryInconclusive, activity)
	}
	history, err := c.historyEvents(resp.Thread, true)
	if err != nil {
		return snapshot, fmt.Errorf(
			"%w: %w", ports.ErrChatRecoveryInconclusive, err)
	}
	if activity == domain.ActivityIdle {
		for _, turn := range resp.Thread.Turns {
			state := turnStateFrom(string(turn.Status))
			if state == domain.TurnStateRunning || state == domain.TurnStateQueued {
				return snapshot, fmt.Errorf(
					"%w: provider reports idle with live turn %s",
					ports.ErrChatRecoveryInconclusive, turn.ID)
			}
		}
	}
	snapshot.HistoryEvents = history
	snapshot.ActivityState = activity
	return snapshot, nil
}

// readHistoryTurns prefers the current paginated protocol and falls back only
// when the provider explicitly reports that the method does not exist. Any
// other paging failure is inconclusive; silently accepting a prefix would erase
// older prompts or tool work from AO's authoritative replay.
func (c *conversation) readHistoryTurns(ctx context.Context) ([]codexproto.Turn, error) {
	turns, paginated, err := c.readPaginatedTurns(ctx)
	if errors.Is(err, errThreadHistoryUnmaterialized) {
		// Codex creates a thread identity before the first user message, but has no
		// rollout to page until that message materializes it. The provider's exact
		// refusal therefore proves this thread has no native history to import.
		return nil, nil
	}
	if err != nil {
		return nil, asRefusal(err)
	}
	if paginated {
		return turns, nil
	}

	var resp codexproto.ThreadReadResponse
	includeTurns := true
	if err := c.conn.request(ctx, codexproto.MethodThreadRead, codexproto.ThreadReadParams{
		ThreadID:     c.threadID,
		IncludeTurns: &includeTurns,
	}, &resp); err != nil {
		return nil, asRefusal(fmt.Errorf("thread/read history: %w", err))
	}
	if resp.Thread.ID != "" && resp.Thread.ID != c.threadID {
		return nil, fmt.Errorf("thread/read returned %q for %q", resp.Thread.ID, c.threadID)
	}
	return resp.Thread.Turns, nil
}

// readPaginatedTurns returns (nil, false, nil) only for the exact -32601 method
// absence reported by older app-server builds on the first page. A supported
// provider must return every page oldest-first with full item hydration.
func (c *conversation) readPaginatedTurns(
	ctx context.Context,
) ([]codexproto.Turn, bool, error) {
	var (
		cursor      *string
		turns       []codexproto.Turn
		seenCursors = make(map[string]struct{})
		seenTurns   = make(map[string]struct{})
	)
	for page := 0; ; page++ {
		var resp threadTurnsListResponse
		err := c.conn.request(ctx, methodThreadTurnsList, threadTurnsListParams{
			ThreadID:      c.threadID,
			Cursor:        cursor,
			Limit:         historyTurnPageSize,
			SortDirection: "asc",
			ItemsView:     codexproto.TurnItemsViewFull,
		}, &resp)
		if err != nil {
			var rpcErr *rpcError
			if page == 0 && errors.As(err, &rpcErr) && rpcErr.Code == -32601 {
				return nil, false, nil
			}
			if page == 0 && isUnmaterializedHistoryRefusal(err) {
				return nil, true, errThreadHistoryUnmaterialized
			}
			return nil, true, fmt.Errorf("thread/turns/list page %d: %w", page+1, err)
		}

		for _, turn := range resp.Data {
			if strings.TrimSpace(turn.ID) == "" {
				return nil, true, errors.New("thread/turns/list returned a turn with no id")
			}
			if turn.ItemsView == nil || *turn.ItemsView != codexproto.TurnItemsViewFull {
				return nil, true, fmt.Errorf(
					"thread/turns/list returned turn %s with items view %q, want full",
					turn.ID, derefTurnItemsView(turn.ItemsView))
			}
			if _, duplicate := seenTurns[turn.ID]; duplicate {
				return nil, true, fmt.Errorf(
					"thread/turns/list returned duplicate turn %s", turn.ID)
			}
			seenTurns[turn.ID] = struct{}{}
			turns = append(turns, turn)
		}

		if resp.NextCursor == nil {
			return turns, true, nil
		}
		next := strings.TrimSpace(*resp.NextCursor)
		if next == "" {
			return nil, true, errors.New("thread/turns/list returned a blank next cursor")
		}
		if _, duplicate := seenCursors[next]; duplicate {
			return nil, true, fmt.Errorf(
				"thread/turns/list repeated cursor %q", next)
		}
		seenCursors[next] = struct{}{}
		cursor = &next
	}
}

func isUnmaterializedHistoryRefusal(err error) bool {
	var rpcErr *rpcError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32600 {
		return false
	}
	message := strings.ToLower(rpcErr.Message)
	return strings.Contains(message, "not materialized") &&
		strings.Contains(message, "before first user message")
}

func derefTurnItemsView(view *codexproto.TurnItemsView) codexproto.TurnItemsView {
	if view == nil {
		return ""
	}
	return *view
}

func liveRecoveryActivity(status codexproto.ThreadStatus) (domain.ActivityState, error) {
	switch status.Type {
	case codexproto.ThreadStatusTypeIdle:
		if len(status.ActiveFlags) != 0 {
			return "", fmt.Errorf("%w: idle provider reported active wait flags",
				ports.ErrChatRecoveryInconclusive)
		}
		return domain.ActivityIdle, nil
	case codexproto.ThreadStatusTypeActive:
		waiting := false
		for _, flag := range status.ActiveFlags {
			switch flag {
			case codexproto.ThreadActiveFlagWaitingOnApproval,
				codexproto.ThreadActiveFlagWaitingOnUserInput:
				waiting = true
			default:
				return "", fmt.Errorf(
					"%w: active provider reported unknown wait flag %q",
					ports.ErrChatRecoveryInconclusive, flag)
			}
		}
		if waiting {
			return domain.ActivityWaitingInput, nil
		}
		return domain.ActivityActive, nil
	default:
		return "", fmt.Errorf("%w: provider thread status is %q",
			ports.ErrChatRecoveryInconclusive, status.Type)
	}
}

func (c *conversation) historyEvents(
	thread codexproto.Thread,
	allowUnsettled bool,
) ([]ports.ChatEvent, error) {
	for _, turn := range thread.Turns {
		if strings.TrimSpace(turn.ID) == "" {
			return nil, errors.New("codex history contains a turn with no id")
		}
		if allowUnsettled && !knownHistoryTurnStatus(string(turn.Status)) {
			return nil, fmt.Errorf("codex turn %s has unknown status %q", turn.ID, turn.Status)
		}
		state := turnStateFrom(string(turn.Status))
		if state == domain.TurnStateRunning || state == domain.TurnStateQueued {
			if allowUnsettled {
				continue
			}
			return nil, fmt.Errorf("%w: Codex turn %s is %s",
				ports.ErrChatHistoryUnsettled, turn.ID, turn.Status)
		}
	}

	events := make([]ports.ChatEvent, 0, len(thread.Turns)*4)
	for _, turn := range thread.Turns {
		state := turnStateFrom(string(turn.Status))

		events = append(events, ports.ChatEvent{
			Kind:            ports.ChatEventTurnStarted,
			ProviderEventID: historyEventID(c.threadID, turn.ID, "started"),
			ProviderTurnID:  turn.ID,
		})
		if state == domain.TurnStateRunning || state == domain.TurnStateQueued {
			// Items on an in-progress turn are not a settled restatement. Detached
			// notifications above carry its progress; the authoritative history
			// contributes only the live ownership fact.
			continue
		}

		for itemIndex, nativeItem := range turn.Items {
			item := nativeItem
			itemID := deref(item.ID)
			eventItemID := itemID
			if itemID == "" {
				// Current persisted Codex history can omit item ids. The projection's
				// provider-item key is conversation-wide, so an index alone would make
				// item 1 in every turn overwrite item 1 from the first turn. Include the
				// turn in that key while keeping the event identity position-based.
				eventItemID = fmt.Sprintf("item-%d", itemIndex)
				itemID = fmt.Sprintf("%s-%s", turn.ID, eventItemID)
				item.ID = &itemID
			}

			if item.Type == codexproto.ThreadItemTypeUserMessage {
				text := historicalUserText(item)
				if text == "" {
					continue
				}
				clientID := deref(item.ClientID)
				if clientID == "" {
					clientID = historyEventID(c.threadID, turn.ID, "user", eventItemID)
				}
				events = append(events, ports.ChatEvent{
					Kind:            ports.ChatEventUserMessageCompleted,
					ProviderEventID: historyEventID(c.threadID, turn.ID, "user", eventItemID),
					ProviderTurnID:  turn.ID,
					ProviderItemID:  itemID,
					ClientMessageID: clientID,
					Text:            text,
				})
				continue
			}

			params, err := json.Marshal(map[string]any{
				"threadId": c.threadID,
				"turnId":   turn.ID,
				"item":     item,
			})
			if err != nil {
				return nil, fmt.Errorf("encode history item %s: %w", itemID, err)
			}
			for eventIndex, event := range normalizeItem(params, true) {
				event.ProviderEventID = historyEventID(
					c.threadID, turn.ID, "item", eventItemID, string(event.Kind), fmt.Sprint(eventIndex))
				events = append(events, event)
			}
		}

		completed := ports.ChatEvent{
			Kind:            ports.ChatEventTurnCompleted,
			ProviderEventID: historyEventID(c.threadID, turn.ID, "completed"),
			ProviderTurnID:  turn.ID,
			TurnState:       state,
		}
		if turn.Error != nil && turn.Error.Message != "" {
			completed.Err = errors.New(turn.Error.Message)
		}
		events = append(events, completed)
	}
	return events, nil
}

func knownHistoryTurnStatus(status string) bool {
	switch status {
	case string(codexproto.TurnStatusCompleted),
		string(codexproto.TurnStatusInterrupted),
		string(codexproto.TurnStatusFailed),
		string(codexproto.TurnStatusInProgress),
		"in_progress", "running", "active", "queued", "pending", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

// RefreshHistory performs another authoritative thread/read. Codex exposes a
// repeatable native read rather than a replay captured during resume, so bounded
// settle polling can observe provider progress here.
func (c *conversation) RefreshHistory(ctx context.Context) ([]ports.ChatEvent, error) {
	return c.ReadHistory(ctx)
}

func historyEventID(parts ...string) string {
	return "codex-history:" + strings.Join(parts, ":")
}

// historicalUserText renders the provider's structured prompt blocks without
// leaking Codex DTOs above the adapter. Text is preserved verbatim; non-text
// blocks get a compact readable marker so an attachment-only prompt is not lost.
func historicalUserText(item codexproto.ThreadItem) string {
	var inputs []codexproto.UserInput
	if len(item.Content) > 0 && json.Unmarshal(item.Content, &inputs) == nil {
		parts := make([]string, 0, len(inputs))
		for _, input := range inputs {
			if text := strings.TrimSpace(deref(input.Text)); text != "" {
				parts = append(parts, text)
				continue
			}
			switch input.Type {
			case codexproto.UserInputTypeSkill:
				if name := strings.TrimSpace(deref(input.Name)); name != "" {
					parts = append(parts, "/"+name)
				} else {
					parts = append(parts, "[Skill]")
				}
			case codexproto.UserInputTypeMention:
				mention := firstNonEmpty(deref(input.Name), deref(input.Path))
				if mention == "" {
					parts = append(parts, "[Mention]")
				} else {
					parts = append(parts, "@"+mention)
				}
			case codexproto.UserInputTypeImage, codexproto.UserInputTypeLocalImage:
				parts = append(parts, "[Image]")
			case codexproto.UserInputTypeAudio, codexproto.UserInputTypeLocalAudio:
				parts = append(parts, "[Audio]")
			}
		}
		if text := strings.TrimSpace(strings.Join(parts, "\n")); text != "" {
			return text
		}
	}
	return strings.TrimSpace(deref(item.Text))
}

// Rollback discards the named turn and every turn after it, provider-side.
//
// Inclusive of the named turn on purpose: this is undo, and the thing a person
// wants back is the state from before they sent the message they are pointing at.
// Keeping that turn would leave the exchange they are trying to take back in the
// agent's memory, which is the one outcome that makes the button pointless.
//
// It changes what the agent remembers. AO's rows have to follow, and that is the
// caller's job — see the Chat controller.
func (c *conversation) Rollback(ctx context.Context, providerTurnID string) error {
	if strings.TrimSpace(providerTurnID) == "" {
		return errors.New("rollback needs a provider turn id")
	}

	// Serialized with turn dispatch: the count computed below is only valid for the
	// turn list it was read from, and a turn starting in between would shift every
	// position in it.
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	turns, err := c.readTurns(ctx)
	if err != nil {
		return err
	}

	discard := 0
	for i, turn := range turns {
		if turn.ID == providerTurnID {
			discard = len(turns) - i
			break
		}
	}
	if discard == 0 {
		// Either the turn was already discarded or it belongs to history this thread
		// no longer holds. Refusing beats guessing: any count AO invented here would
		// discard turns the user never named.
		return refuse("turn %q is not in the provider's history", providerTurnID)
	}

	if err := c.conn.request(ctx, "thread/rollback", map[string]any{
		"threadId": c.threadID,
		"numTurns": discard,
	}, nil); err != nil {
		return asRefusal(fmt.Errorf("thread/rollback: %w", err))
	}
	return nil
}

// readTurns is the provider's own turn list for this thread, oldest first.
//
// Read from the provider rather than counted from AO's rows because the two sets
// legitimately differ: AO records a turn whose dispatch the provider rejected, and
// a turn from before a resume may be absent here. thread/rollback counts in the
// provider's terms, so counting in AO's would discard the wrong number of turns.
func (c *conversation) readTurns(ctx context.Context) ([]providerTurn, error) {
	nativeTurns, err := c.readHistoryTurns(ctx)
	if err != nil {
		return nil, err
	}
	turns := make([]providerTurn, 0, len(nativeTurns))
	for _, turn := range nativeTurns {
		turns = append(turns, providerTurn{ID: turn.ID, Status: string(turn.Status)})
	}
	return turns, nil
}

// Fork branches this conversation into a second provider conversation, so an
// alternative approach can be tried without destroying the original.
//
// A nil anchor copies the whole history; a provider turn id copies through that
// turn inclusively. In both cases, the original thread remains unchanged.
func (c *conversation) Fork(ctx context.Context, lastProviderTurnID *string) (string, error) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	params := codexproto.ThreadForkParams{ThreadID: c.threadID}
	if lastProviderTurnID != nil {
		anchor := strings.TrimSpace(*lastProviderTurnID)
		if anchor == "" {
			return "", errors.New("fork anchor must not be blank")
		}
		params.LastTurnID = &anchor
	}
	// cwd is deliberately absent so the fork inherits the source thread's working
	// directory. A fork pointed at a different tree would remember editing files
	// that are not there.
	var resp codexproto.ThreadForkResponse
	if err := c.conn.request(ctx, codexproto.MethodThreadFork, params, &resp); err != nil {
		return "", asRefusal(fmt.Errorf("thread/fork: %w", err))
	}
	if resp.Thread.ID == "" {
		return "", errors.New("thread/fork returned no thread id")
	}
	return resp.Thread.ID, nil
}

// SetTitle names the thread provider-side.
//
// Nothing is written to AO's rows here. The provider answers a bare {} and then
// emits thread/name/updated, and that notification is the single path by which a
// title reaches AO — including when the name was set by some other client
// entirely. Writing it optimistically as well would give AO two sources for one
// fact and no way to tell which one lost.
func (c *conversation) SetTitle(ctx context.Context, title string) error {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return refuse("thread title must not be empty")
	}

	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	if err := c.conn.request(ctx, "thread/name/set", map[string]any{
		"threadId": c.threadID,
		"name":     trimmed,
	}, nil); err != nil {
		return asRefusal(fmt.Errorf("thread/name/set: %w", err))
	}
	return nil
}
