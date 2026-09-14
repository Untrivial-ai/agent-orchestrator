// Package chat owns the daemon side of a Chat-mode session: one controller per
// session, the projection of provider events into durable conversation rows, and
// the typed commands a client can issue against it.
//
// Ownership rules this package enforces:
//
//   - exactly one live controller per session, and it is the only writer to that
//     session's provider conversation;
//   - provider events are archived and projected together, so the raw record can
//     never disagree with the timeline derived from it;
//   - an event carrying a stale controller generation is dropped, so a controller
//     that is dying cannot mutate the session that replaced it;
//   - a turn left in flight when its provider ends settles as failed. Deliberate
//     daemon detach from a persistent provider is not provider termination.
package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	nativeHistorySettlePoll  = 100 * time.Millisecond
	nativeHistorySettleLimit = 45 * time.Second
	branchHandoffReportLimit = 5 * time.Second
	retryClientMessagePrefix = "retry-attempt/"
)

// Store is the durable conversation surface the controller needs. Implemented by
// the SQLite store.
type Store interface {
	CreateConversation(ctx context.Context, id string, scope domain.ConversationScope, project domain.ProjectID, session domain.SessionID, now time.Time) (domain.ConversationRecord, error)
	CreateProjectConversationWithContextReset(ctx context.Context, id string, project domain.ProjectID, session domain.SessionID, reset domain.ConversationActivity, now time.Time) (domain.ConversationRecord, error)
	ConversationForSession(ctx context.Context, session domain.SessionID) (domain.ConversationRecord, error)
	ClaimChatControllerGeneration(ctx context.Context, session domain.SessionID, generation string, now time.Time) error
	ConversationBranch(ctx context.Context, conversationID, branchID string) (domain.ConversationBranch, error)
	ConversationEditAnchor(ctx context.Context, conversationID, replacedTurnID string) (domain.ConversationEditAnchor, error)
	RepairIncompleteConversationEdit(ctx context.Context, sessionID domain.SessionID, conversationID string, now time.Time) (domain.ConversationBranch, bool, error)
	CreateAndActivateConversationBranch(ctx context.Context, sessionID domain.SessionID, branch domain.ConversationBranch, generation string, now time.Time) error
	ActivateConversationBranch(ctx context.Context, sessionID domain.SessionID, conversationID, branchID, providerConversationID, generation string, now time.Time) error
	UpdateConversationBranchReplacement(ctx context.Context, branchID, replacementTurnID string) error

	AdoptProviderTurn(ctx context.Context, conversationID string, session domain.SessionID, generation, turnID, providerTurnID string, now time.Time) error
	AppendImportedUserMessage(ctx context.Context, conversationID, providerTurnID string, msg domain.ConversationMessage, now time.Time) error

	AppendUserMessage(ctx context.Context, conversationID string, session domain.SessionID, generation string, msg domain.ConversationMessage, turnID string, now time.Time) (bool, error)
	AppendRetryUserMessage(ctx context.Context, conversationID string, session domain.SessionID, generation string, msg domain.ConversationMessage, turnID, retryOfTurnID string, now time.Time) (bool, error)
	BindTurnToProvider(ctx context.Context, turnID, providerTurnID string, now time.Time) error
	SettleTurn(ctx context.Context, conversationID, providerTurnID string, state domain.TurnState, errMessage string, now time.Time) error
	SettleTurnByID(ctx context.Context, turnID string, state domain.TurnState, errMessage string, now time.Time) error
	SettleOrphanedTurns(ctx context.Context, session domain.SessionID, now time.Time) error
	CleanupOwnedControllerWork(ctx context.Context, session domain.SessionID, conversationID, generation string, now time.Time) (bool, error)
	ListVisibleRunningTurnProviderIDs(ctx context.Context, conversationID string) ([]string, error)

	SetConversationSettings(ctx context.Context, conversationID string, settings domain.ConversationSettings, now time.Time) error

	// Usage and rate limits are current state, not timeline entries: each write
	// replaces the last. The provider reports usage after every tool call, so an
	// append-per-report is what buried the conversation the first time round.
	RecordUsage(ctx context.Context, conversationID string, usage domain.ConversationUsage) error
	RecordRateLimits(ctx context.Context, conversationID string, limits domain.ConversationRateLimits) error

	NextQueuedTurn(ctx context.Context, conversationID string) (domain.QueuedTurn, error)
	ReserveQueuedTurnForPromotion(ctx context.Context, conversationID, turnID string, now time.Time) (domain.QueuedTurn, error)
	ReleaseQueuedTurnPromotion(ctx context.Context, conversationID, turnID string) error
	CompleteQueuedTurnPromotion(ctx context.Context, conversationID, sourceTurnID, providerTurnID string, activity domain.ConversationActivity, now time.Time) error
	CancelQueuedTurns(ctx context.Context, conversationID string, cutoff, now time.Time) error
	CancelAllQueuedTurns(ctx context.Context, conversationID string, now time.Time) error
	CancelQueuedTurnByID(ctx context.Context, conversationID, turnID string, now time.Time) error
	QueuedTurnMessage(ctx context.Context, conversationID, turnID string) (domain.ConversationMessage, error)
	UpdateQueuedTurnMessage(ctx context.Context, conversationID, turnID, text, contentJSON string, revision int64, now time.Time) error
	ReorderQueuedTurns(ctx context.Context, conversationID string, turnIDs []string) error

	RetryPrompt(ctx context.Context, conversationID, turnID string) (domain.RetryPrompt, error)
	RetryTurnIDForSource(ctx context.Context, conversationID, sourceTurnID string) (string, bool, error)

	TurnByID(ctx context.Context, turnID string) (domain.ConversationTurn, error)
	RollbackTurns(ctx context.Context, conversationID, turnID string, now time.Time) (int, error)

	SetProviderTitle(ctx context.Context, conversationID, title string, now time.Time) error
	ApplyProviderTitle(ctx context.Context, conversationID string, session domain.SessionID, title string, now time.Time) (bool, error)

	AppendAssistantDelta(ctx context.Context, conversationID, providerItemID, providerTurnID, delta, messageID string, now time.Time) error
	SettleAssistantMessage(ctx context.Context, conversationID, providerItemID, providerTurnID, text, messageID string, now time.Time) error

	AppendCommandOutput(ctx context.Context, conversationID, providerItemID, delta string, now time.Time) (bool, error)
	SetTurnDiff(ctx context.Context, conversationID, providerTurnID string, diff domain.ConversationTurnDiff, now time.Time) (bool, error)

	// Streamed provider prose for an activity: reasoning summaries, terminal
	// keystrokes, tool progress. Appended many times and then replaced by the
	// settled form, which is why it is two calls rather than one upsert.
	AppendActivityStreamedText(ctx context.Context, conversationID, providerItemID, delta string, now time.Time) (bool, error)
	SettleActivityStreamedText(ctx context.Context, conversationID, providerItemID, text string, now time.Time) (bool, error)

	SetTurnPlan(ctx context.Context, conversationID, providerTurnID string, plan domain.ConversationPlan) (bool, error)

	// Latest-wins provider state that belongs to the conversation rather than to a
	// turn. Each write replaces the last.
	RecordModelReroute(ctx context.Context, conversationID string, reroute domain.ConversationModelReroute) error
	RecordAccount(ctx context.Context, conversationID string, account domain.ConversationAccount, now time.Time) error
	RecordThreadState(ctx context.Context, conversationID string, state domain.ConversationThreadState) error
	RecordMCPServers(ctx context.Context, conversationID string, servers []domain.ConversationMCPServer) error

	UpsertActivity(ctx context.Context, conversationID, providerTurnID string, activity domain.ConversationActivity, now time.Time) error
	MarkCompacted(ctx context.Context, conversationID string, at time.Time) error
	ResolveApproval(ctx context.Context, conversationID, requestID, detailJSON string, now time.Time) error
	HasPendingConversationInteractions(ctx context.Context, conversationID string) (bool, error)
	FailPendingApprovals(ctx context.Context, conversationID string, now time.Time) error
	FailPendingInputs(ctx context.Context, conversationID string, now time.Time) error

	ProjectProviderEvent(ctx context.Context, conversationID string, session domain.SessionID, generation, providerEventID, method, payloadJSON string, now time.Time, project func(context.Context) error) (bool, error)
}

// ActivityRecorder feeds derived session status.
//
// Chat reports activity through the SAME lifecycle reduction terminal sessions
// use, rather than persisting a second display status. Without it a chat session
// reads as idle while the agent is working, because the hook and terminal signals
// that normally drive activity never fire for a chat controller.
type ActivityRecorder interface {
	ApplyActivitySignal(ctx context.Context, id domain.SessionID, s ports.ActivitySignal) error
}

// IDFactory mints the identifiers AO assigns. Injected so tests get stable ids.
type IDFactory func() string

// Clock is injected so tests do not depend on wall time.
type Clock func() time.Time

// controllerHandoff is the controller's dispatch gate. Keeping the lifecycle in
// one state avoids invalid combinations such as "handoff=true, drain=false"
// meaning either an interrupt transition or an idle branch mutation.
type controllerHandoff uint8

const (
	controllerHandoffNone controllerHandoff = iota
	controllerHandoffInterfaceDrain
	controllerHandoffInterfaceInterrupt
	controllerHandoffIdleBranch
)

func interfaceHandoff(policy domain.SessionInterfaceTransitionPolicy) controllerHandoff {
	if policy == domain.SessionInterfaceTransitionDrain {
		return controllerHandoffInterfaceDrain
	}
	return controllerHandoffInterfaceInterrupt
}

// Controller drives one Chat session.
type Controller struct {
	sessionID    domain.SessionID
	conversation domain.ConversationRecord
	generation   string
	harness      domain.AgentHarness

	conv                   ports.ChatConversation
	store                  Store
	activity               ActivityRecorder
	log                    *slog.Logger
	newID                  IDFactory
	now                    Clock
	onAccountChanged       func(domain.SessionID, string, domain.AgentHarness)
	onCodexCapacityChanged func(domain.SessionID, string, ports.CodexCapacityObservation)

	// sendMu serializes command dispatch so only one operation mutates the
	// provider conversation at a time.
	sendMu sync.Mutex
	// configMu serializes live provider setting changes. Each response replaces
	// the complete option catalog and updates durable next-turn settings, so
	// concurrent writes could otherwise persist an older catalog last.
	configMu sync.Mutex

	mu sync.Mutex
	// suppressStoppedActivity marks a deliberate branch-controller retirement.
	// The replacement generation owns lifecycle state; publishing an exited fact
	// from the source would make that replacement unable to report active work.
	suppressStoppedActivity bool
	// activeTurn maps a provider turn id to AO's turn id for the turn currently
	// in flight, so a completion can be attributed without a round trip.
	pendingTurnID string
	// dispatchingTurnID is AO's durable turn row while SendTurn is in flight.
	// Eager providers can emit turn/started before SendTurn returns with the
	// provider id; that event must bind this row instead of adopting a duplicate.
	dispatchingTurnID string
	// ackedTurnID is the turn the PROVIDER has confirmed it started, which lags
	// pendingTurnID by the round trip between turn/start returning and the
	// turn-started notification arriving. Interrupt needs the distinction: a
	// provider refuses to cancel a turn it has not acknowledged yet.
	ackedTurnID string
	state       ports.ChatControllerState
	// settings are the provider choices applied to the next dispatch. Held here as
	// well as on disk so a dispatch does not need a read, and updated together with
	// the row so the two cannot drift.
	settings domain.ConversationSettings
	// cancelQueuedAt is set when the user interrupts, and is the cutoff for the
	// queue that interrupt cancels. Zero means nothing is being cancelled.
	cancelQueuedAt time.Time
	// handoff closes source-controller intake while ownership is being changed.
	// Interface drain keeps dispatching accepted rows; interface interrupt and an
	// idle branch mutation stop dispatch entirely. The target controller is not
	// started until this controller reports quiescent and is closed.
	handoff           controllerHandoff
	branchHandoffDone chan struct{}
	// preserveProviderOnStop is set before deliberate daemon detach. It prevents
	// the closing controller from projecting a false provider exit or failing work
	// that the detached host continues to run.
	preserveProviderOnStop bool

	// account, threadState and mcpServers are merged here before being written,
	// because the provider reports each of them in pieces: account/updated carries
	// auth mode and plan but never a credential demand, a thread-status report says
	// nothing about archiving, and MCP servers are announced one at a time. Writing
	// each report straight through would make every field blank the one before it.
	account     domain.ConversationAccount
	threadState domain.ConversationThreadState
	// usage is merged in memory because providers may split context occupancy and
	// cumulative accounting across separate events. Writing either half as a full
	// replacement erases the other half and turns a percentage meter into a bare
	// token count.
	usage domain.ConversationUsage
	// mcpServers is keyed by name; mcpServerOrder preserves first-seen order so the
	// list a client renders does not reshuffle on every turn.
	mcpServers     map[string]domain.ConversationMCPServer
	mcpServerOrder []string

	stopped  chan struct{}
	once     sync.Once
	closeErr error
}

// ErrNoActiveTurn reports an interrupt with nothing to cancel.
var ErrNoActiveTurn = errors.New("no active turn")

// ErrControllerHandoff reports a send attempted after a controller handoff has
// closed source intake. The client should retain the draft and retry after the
// session_updated event announces the target mode.
var ErrControllerHandoff = errors.New("chat controller is switching interfaces")

// ErrTurnNotRetryable reports a turn that cannot be retried: it is not part of
// this conversation, not failed, was rolled back, or carries no durable prompt
// the user authored. A retry is only ever offered for an eligible failed human
// turn.
var ErrTurnNotRetryable = errors.New("turn is not retryable")

// ErrRetryStaleBranch reports a durable failed turn that is no longer visible
// on the conversation's active branch. Retrying it would inject abandoned
// history into the provider thread the user is currently viewing.
var ErrRetryStaleBranch = errors.New("turn is not on the active conversation branch")

// ErrRetryDeliveryUncertain reports a failed turn whose dispatch was never
// acknowledged by the provider. Settling it failed was AO's safe guess rather
// than a fact about delivery: the provider may have accepted the work. Because
// a retry re-sends the prompt as a new turn, an uncertain source could execute
// it twice, so these turns are refused until delivery is confirmed.
var ErrRetryDeliveryUncertain = errors.New("turn was never confirmed as delivered to the provider")

// ErrRetryContentInvalid reports durable structured prompt content that can no
// longer be reconstructed safely. Retrying must fail clearly rather than drop
// an attachment or hand corrupt data to the provider.
var ErrRetryContentInvalid = errors.New("stored retry content is invalid")

// ErrRetryUnsupported reports structured prompt content the current provider
// cannot accept. This can happen after an in-place agent switch: the failed
// prompt remains visible, but the new provider may negotiate fewer capabilities.
var ErrRetryUnsupported = errors.New("current agent cannot retry this prompt content")

func newController(
	sessionID domain.SessionID,
	conversation domain.ConversationRecord,
	generation string,
	harness domain.AgentHarness,
	conv ports.ChatConversation,
	store Store,
	activity ActivityRecorder,
	log *slog.Logger,
	newID IDFactory,
	now Clock,
	onAccountChanged func(domain.SessionID, string, domain.AgentHarness),
	onCodexCapacityChanged func(domain.SessionID, string, ports.CodexCapacityObservation),
) *Controller {
	c := &Controller{
		sessionID:              sessionID,
		conversation:           conversation,
		generation:             generation,
		harness:                harness,
		conv:                   conv,
		store:                  store,
		activity:               activity,
		log:                    log,
		newID:                  newID,
		now:                    now,
		onAccountChanged:       onAccountChanged,
		onCodexCapacityChanged: onCodexCapacityChanged,
		state:                  ports.ChatControllerReady,
		settings:               conversation.Settings,
		mcpServers:             map[string]domain.ConversationMCPServer{},
		stopped:                make(chan struct{}),
	}
	// Seeded from the durable row so a reconnect merges onto what is already known
	// rather than starting from blank and reporting a conversation as having no
	// account until the provider next mentions one.
	if conversation.Account != nil {
		c.account = *conversation.Account
	}
	if conversation.ThreadState != nil {
		c.threadState = *conversation.ThreadState
	}
	if conversation.Usage != nil {
		c.usage = *conversation.Usage
	}
	for _, server := range conversation.MCPServers {
		if _, seen := c.mcpServers[server.Name]; !seen {
			c.mcpServerOrder = append(c.mcpServerOrder, server.Name)
		}
		c.mcpServers[server.Name] = server
	}
	return c
}

// restoreLiveTurnOwnership rebuilds the volatile busy gate from durable facts
// before a replacement daemon publishes a reconnected controller. The provider
// kept running while AO was detached, so forgetting this turn would let a new
// Send start a second root turn on the same native conversation.
func (c *Controller) restoreLiveTurnOwnership(turns []domain.ConversationTurn) {
	var latest *domain.ConversationTurn
	for i := range turns {
		turn := &turns[i]
		if turn.State != domain.TurnStateRunning || turn.ProviderTurnID == "" || turn.RolledBackAt != nil {
			continue
		}
		if latest == nil || turn.RequestedAt.After(latest.RequestedAt) {
			latest = turn
		}
	}
	if latest == nil {
		return
	}
	c.pendingTurnID = latest.ProviderTurnID
	c.ackedTurnID = latest.ProviderTurnID
	c.state = ports.ChatControllerBusy
}

// start begins live provider consumption after any durable native history has
// been imported. Keeping construction and consumption separate prevents a resume
// notification from racing ahead of the older turns it follows.
func (c *Controller) start() {
	go c.project()
	if c.harness != domain.HarnessCodex {
		go c.readRateLimits()
	}
}

type nativeHistoryHighWater struct {
	sequence       int64
	providerTurnID string
	providerItemID string
	kind           ports.ChatEventKind
	text           string
	activityKind   domain.ActivityKind
	activityStatus domain.ActivityStatus
	summary        string
	detail         []byte
}

// nativeHistoryCheckpoint combines the newest source-TUI facts with AO's last
// settled projection of this same provider thread. The hook facts cover work
// completed while Chat was not attached; the AO high-water mark covers an
// immediate round trip before a resumed TUI has emitted another hook.
type nativeHistoryCheckpoint struct {
	latestUserPrompt      string
	latestAssistantUpdate string
	aoHighWater           nativeHistoryHighWater
}

func (p *nativeHistoryCheckpoint) captureAOHighWater(
	sessionID domain.SessionID,
	turns []domain.ConversationTurn,
	messages []domain.ConversationMessage,
	activities []domain.ConversationActivity,
) {
	turnsByID := make(map[string]*domain.ConversationTurn, len(turns))
	for i := range turns {
		turnsByID[turns[i].ID] = &turns[i]
	}
	// An agent switch starts a new provider-native thread with an AO coordination
	// turn. Completed turns before it belong to the previous provider: their
	// opaque ids remain useful timeline facts, but the new provider cannot replay
	// them and they must not gate this native-history import.
	var providerBoundary time.Time
	for _, message := range messages {
		turn := turnsByID[message.TurnID]
		if turn == nil || message.Role != domain.MessageRoleUser ||
			!nativeHistoryCoordinationMessage(message.Text) {
			continue
		}
		if turn.RequestedAt.After(providerBoundary) {
			providerBoundary = turn.RequestedAt
		}
	}

	var latest *domain.ConversationTurn
	for i := range turns {
		turn := &turns[i]
		// Only completed turns anchor the high-water mark. A provider promises to
		// reproduce settled work during history load, but a failed or interrupted
		// turn's items carry no such promise: Claude forks its next prompt from the
		// pre-failure transcript entry, leaving the failed turn (e.g. a synthetic
		// auth-error message) on a dead branch that session/load never replays.
		// Requiring one of those items would make every future switch time out.
		if turn.HandledBySessionID != sessionID || turn.State != domain.TurnStateCompleted || turn.ProviderTurnID == "" ||
			(!providerBoundary.IsZero() && !turn.RequestedAt.After(providerBoundary)) {
			continue
		}
		if latest == nil || turn.RequestedAt.After(latest.RequestedAt) {
			latest = turn
		}
	}
	if latest == nil {
		return
	}
	p.aoHighWater.providerTurnID = latest.ProviderTurnID
	for _, message := range messages {
		if message.TurnID != latest.ID || message.Streaming || message.Sequence <= p.aoHighWater.sequence {
			continue
		}
		kind := ports.ChatEventKind("")
		switch message.Role {
		case domain.MessageRoleUser:
			kind = ports.ChatEventUserMessageCompleted
		case domain.MessageRoleAssistant:
			kind = ports.ChatEventMessageCompleted
		}
		if kind == "" {
			continue
		}
		p.aoHighWater = nativeHistoryHighWater{
			sequence: message.Sequence, providerTurnID: latest.ProviderTurnID,
			providerItemID: message.ProviderItemID, kind: kind, text: message.Text,
		}
	}
	for _, activity := range activities {
		if activity.TurnID != latest.ID || activity.ProviderItemID == "" ||
			activity.Sequence <= p.aoHighWater.sequence {
			continue
		}
		switch activity.Kind {
		case domain.ActivityKindCommand, domain.ActivityKindFileChange,
			domain.ActivityKindReasoning, domain.ActivityKindMCPTool:
		default:
			// Approval/input/system rows are AO control-plane facts, not items
			// the provider promises to reproduce during native history load.
			continue
		}
		switch activity.Status {
		case domain.ActivityStatusCompleted, domain.ActivityStatusFailed:
			p.aoHighWater = nativeHistoryHighWater{
				sequence: activity.Sequence, providerTurnID: latest.ProviderTurnID,
				providerItemID: activity.ProviderItemID, activityKind: activity.Kind,
				activityStatus: activity.Status, summary: activity.Summary,
				detail: append([]byte(nil), activity.Detail...),
			}
		}
	}
}

func (p nativeHistoryCheckpoint) reached(events []ports.ChatEvent) bool {
	completedTurns := make(map[string]bool)
	coordinationTurns := make(map[string]bool)
	for _, event := range events {
		if event.Kind == ports.ChatEventTurnCompleted && event.ProviderTurnID != "" {
			completedTurns[event.ProviderTurnID] = true
		}
		if event.Kind == ports.ChatEventUserMessageCompleted && nativeHistoryCoordinationMessage(event.Text) {
			coordinationTurns[event.ProviderTurnID] = true
		}
	}

	var latestUser, latestAssistant ports.ChatEvent
	for _, event := range events {
		if coordinationTurns[event.ProviderTurnID] || !completedTurns[event.ProviderTurnID] {
			continue
		}
		switch event.Kind {
		case ports.ChatEventUserMessageCompleted:
			latestUser = event
		case ports.ChatEventMessageCompleted:
			latestAssistant = event
		}
	}
	if p.latestUserPrompt != "" &&
		!nativeHistoryTextMatches(p.latestUserPrompt, latestUser.Text) {
		return false
	}
	if p.latestAssistantUpdate != "" &&
		!nativeHistoryTextMatches(p.latestAssistantUpdate, latestAssistant.Text) {
		return false
	}

	highWater := p.aoHighWater
	if highWater.providerTurnID == "" {
		return true
	}
	if highWater.providerItemID == "" && highWater.kind == "" {
		return completedTurns[highWater.providerTurnID]
	}
	for _, event := range events {
		if !completedTurns[event.ProviderTurnID] {
			continue
		}
		if event.ProviderItemID == highWater.providerItemID {
			return true
		}
		// ACP identifiers are opaque and can be reassigned by a conforming
		// provider during load. Exact settled content is the fallback identity
		// used by reconciliation for that same reason.
		if highWater.kind != "" && event.Kind == highWater.kind &&
			nativeHistoryTextMatches(highWater.text, event.Text) {
			return true
		}
		if highWater.kind == "" &&
			event.ActivityKind == highWater.activityKind &&
			event.ActivityStatus == highWater.activityStatus &&
			event.Summary == highWater.summary && bytes.Equal(event.Detail, highWater.detail) {
			return true
		}
	}
	return false
}

func nativeHistoryCoordinationMessage(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "<ao-handoff-request") ||
		strings.HasPrefix(text, "AO transferred the previous agent's context in hidden system instructions.")
}

func nativeHistoryTextMatches(checkpoint, replayed string) bool {
	checkpoint = strings.TrimSpace(checkpoint)
	replayed = strings.TrimSpace(replayed)
	if checkpoint == replayed {
		return true
	}
	// Hook payloads are bounded before persistence. Preserve their head+tail
	// evidence without requiring a provider replay to reproduce AO's marker.
	const marker = "\n[... truncated by AO ...]\n"
	parts := strings.Split(checkpoint, marker)
	return len(parts) == 2 && strings.HasPrefix(replayed, parts[0]) && strings.HasSuffix(replayed, parts[1])
}

// readNativeHistory loads and reconciles the settled provider thread without
// mutating AO's timeline. A pending provider boundary uses this split phase so
// the caller can project the events inside the same transaction that publishes
// the boundary and controller generation.
func (c *Controller) readNativeHistory(
	ctx context.Context,
	existingTurns []domain.ConversationTurn,
	existingMessages []domain.ConversationMessage,
	existingActivities []domain.ConversationActivity,
	required bool,
	checkpoint nativeHistoryCheckpoint,
) ([]ports.ChatEvent, error) {
	reader, ok := c.conv.(ports.ChatHistoryReader)
	if !ok {
		if required {
			return nil, fmt.Errorf("%w: provider does not implement typed history replay",
				ports.ErrChatHistoryUnavailable)
		}
		return nil, nil
	}
	historyCtx, cancel := context.WithTimeout(ctx, nativeHistorySettleLimit)
	defer cancel()
	events, err := reader.ReadHistory(historyCtx)
	refresher, refreshable := reader.(ports.ChatHistoryRefresher)
	sawUnsettled := false
	for err != nil || (required && !checkpoint.reached(events)) {
		if err == nil {
			err = ports.ErrChatHistoryUnsettled
		}
		if errors.Is(err, ports.ErrChatHistoryUnavailable) && !required {
			return nil, nil
		}
		if sawUnsettled && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return nil, fmt.Errorf("wait for settled native conversation history: %w: %w",
				ports.ErrChatHistoryUnsettled, err)
		}
		if !errors.Is(err, ports.ErrChatHistoryUnsettled) {
			return nil, fmt.Errorf("read native conversation history: %w", err)
		}
		sawUnsettled = true
		if !refreshable {
			return nil, fmt.Errorf("native conversation history snapshot is incomplete and cannot be refreshed: %w", err)
		}

		timer := time.NewTimer(nativeHistorySettlePoll)
		select {
		case <-historyCtx.Done():
			timer.Stop()
			return nil, fmt.Errorf("wait for settled native conversation history: %w: %w",
				ports.ErrChatHistoryUnsettled, historyCtx.Err())
		case <-timer.C:
		}
		events, err = refresher.RefreshHistory(historyCtx)
	}
	events = reconcileNativeHistory(
		events, existingTurns, existingMessages, existingActivities,
	)
	for _, event := range events {
		if event.ProviderEventID == "" {
			return nil, fmt.Errorf("native history event %s has no stable identity", event.Kind)
		}
	}
	return events, nil
}

// projectNativeHistory durably imports a previously reconciled snapshot.
// Re-running it is safe because history events carry stable identities and
// ProjectProviderEvent deduplicates archive+projection together.
func (c *Controller) projectNativeHistory(ctx context.Context, events []ports.ChatEvent) error {
	for _, event := range events {
		if _, _, err := c.projectEvent(ctx, event); err != nil {
			return fmt.Errorf("import native history event %s: %w", event.Kind, err)
		}
	}
	return nil
}

// nativeHistoryTurn is the durable identity AO already assigned to one native
// turn. ProviderTurnID is the value every replay event must use before it reaches
// the projector; the other fields are progressively stronger ways to recognize
// it when an ACP agent assigned a different replay turn id.
type nativeHistoryTurn struct {
	providerTurnID string
	state          domain.TurnState
	clientMessage  string
	providerItem   string
	text           string
	messages       map[string]int
	activities     map[string]int
	used           bool
}

func nativeHistoryMessageFingerprint(role domain.MessageRole, text string) string {
	return string(role) + "\x00" + text
}

func nativeHistoryEventMessageFingerprint(event ports.ChatEvent) (string, bool) {
	switch event.Kind {
	case ports.ChatEventUserMessageCompleted:
		return nativeHistoryMessageFingerprint(domain.MessageRoleUser, event.Text), true
	case ports.ChatEventMessageCompleted:
		return nativeHistoryMessageFingerprint(domain.MessageRoleAssistant, event.Text), true
	default:
		return "", false
	}
}

func nativeHistoryActivityFingerprint(
	kind domain.ActivityKind,
	status domain.ActivityStatus,
	summary string,
	detail []byte,
) string {
	return string(kind) + "\x00" + string(status) + "\x00" + summary + "\x00" + string(detail)
}

// reconcileNativeHistory maps a provider replay onto AO's existing durable
// turns before any event is projected.
//
// ACP intentionally treats message ids as opaque. Well-behaved agents may echo
// the client's id, while others persist their own user uuid. Assistant/tool item
// ids are usually replay-stable, so they are the strongest cross-restart signal;
// the original client id is next, and exact prompt text in conversation order is
// the compatibility fallback. This belongs above every driver: Codex history is
// already identity-stable, while all ACP bindings need the same reconciliation.
func reconcileNativeHistory(
	events []ports.ChatEvent,
	existingTurns []domain.ConversationTurn,
	existingMessages []domain.ConversationMessage,
	existingActivities []domain.ConversationActivity,
) []ports.ChatEvent {
	if len(events) == 0 || len(existingTurns) == 0 {
		return events
	}

	byAOTurnID := make(map[string]*nativeHistoryTurn, len(existingTurns))
	byProviderTurnID := make(map[string]*nativeHistoryTurn, len(existingTurns))
	ordered := make([]*nativeHistoryTurn, 0, len(existingTurns))
	for _, turn := range existingTurns {
		if turn.ProviderTurnID == "" || turn.RolledBackAt != nil {
			continue
		}
		candidate := &nativeHistoryTurn{
			providerTurnID: turn.ProviderTurnID,
			state:          turn.State,
			messages:       make(map[string]int),
			activities:     make(map[string]int),
		}
		byAOTurnID[turn.ID] = candidate
		byProviderTurnID[turn.ProviderTurnID] = candidate
		ordered = append(ordered, candidate)
	}
	if len(ordered) == 0 {
		return events
	}

	// A provider item is useful only when it identifies exactly one durable turn.
	// Treat collisions as ambiguous rather than guessing across turns.
	providerItems := make(map[string]*nativeHistoryTurn)
	ambiguousItems := make(map[string]bool)
	rememberProviderItem := func(itemID string, candidate *nativeHistoryTurn) {
		if itemID == "" || candidate == nil || ambiguousItems[itemID] {
			return
		}
		if previous, exists := providerItems[itemID]; exists && previous != candidate {
			delete(providerItems, itemID)
			ambiguousItems[itemID] = true
			return
		}
		providerItems[itemID] = candidate
	}
	for _, message := range existingMessages {
		candidate := byAOTurnID[message.TurnID]
		if candidate == nil {
			continue
		}
		candidate.messages[nativeHistoryMessageFingerprint(message.Role, message.Text)]++
		rememberProviderItem(message.ProviderItemID, candidate)
		if message.Role == domain.MessageRoleUser && candidate.text == "" {
			candidate.text = message.Text
			candidate.clientMessage = message.ClientMessageID
			candidate.providerItem = message.ProviderItemID
		}
	}
	for _, activity := range existingActivities {
		candidate := byAOTurnID[activity.TurnID]
		if candidate == nil {
			continue
		}
		candidate.activities[nativeHistoryActivityFingerprint(
			activity.Kind, activity.Status, activity.Summary, activity.Detail,
		)]++
		rememberProviderItem(activity.ProviderItemID, candidate)
	}

	mapped := make(map[string]*nativeHistoryTurn)
	bind := func(replayTurnID string, candidate *nativeHistoryTurn) {
		if replayTurnID == "" || candidate == nil {
			return
		}
		if previous, exists := mapped[replayTurnID]; exists && previous != candidate {
			// One replay turn claiming stable items from two durable turns is
			// malformed/ambiguous. Preserve the first strong observation rather than
			// moving already-associated events between turns.
			return
		}
		mapped[replayTurnID] = candidate
		candidate.used = true
	}
	providerItemCandidate := func(event ports.ChatEvent) *nativeHistoryTurn {
		var match *nativeHistoryTurn
		identities := make([]string, 0, 1+len(event.ProviderItemAliases))
		identities = append(identities, event.ProviderItemID)
		identities = append(identities, event.ProviderItemAliases...)
		for _, identity := range identities {
			candidate := providerItems[identity]
			if candidate == nil {
				continue
			}
			if match != nil && match != candidate {
				return nil
			}
			match = candidate
		}
		return match
	}
	providerItemAliasCandidate := func(event ports.ChatEvent) *nativeHistoryTurn {
		var match *nativeHistoryTurn
		for _, identity := range event.ProviderItemAliases {
			candidate := providerItems[identity]
			if candidate == nil {
				continue
			}
			if match != nil && match != candidate {
				return nil
			}
			match = candidate
		}
		return match
	}

	// Gather mappings from the complete replay before rewriting TurnStarted, which
	// necessarily arrives before the assistant/tool item that can identify it.
	for _, event := range events {
		if candidate := byProviderTurnID[event.ProviderTurnID]; candidate != nil {
			bind(event.ProviderTurnID, candidate)
		}
		if candidate := providerItemCandidate(event); candidate != nil {
			bind(event.ProviderTurnID, candidate)
		}
	}
	for _, event := range events {
		if event.Kind != ports.ChatEventUserMessageCompleted || event.ProviderTurnID == "" {
			continue
		}
		if mapped[event.ProviderTurnID] != nil {
			continue
		}
		var match *nativeHistoryTurn
		clientIdentities := make([]string, 0, 1+len(event.ProviderItemAliases))
		clientIdentities = append(clientIdentities, event.ClientMessageID)
		clientIdentities = append(clientIdentities, event.ProviderItemAliases...)
		if event.ClientMessageID != "" || len(event.ProviderItemAliases) > 0 {
			for _, candidate := range ordered {
				if candidate.used || candidate.clientMessage == "" {
					continue
				}
				for _, identity := range clientIdentities {
					if candidate.clientMessage == identity {
						match = candidate
						break
					}
				}
				if match != nil {
					break
				}
			}
		}
		if match == nil && event.ProviderItemID != "" {
			for _, candidate := range ordered {
				if !candidate.used && candidate.providerItem == event.ProviderItemID {
					match = candidate
					break
				}
			}
		}
		if match == nil && event.Text != "" {
			for _, candidate := range ordered {
				if !candidate.used && candidate.text == event.Text {
					match = candidate
					break
				}
			}
		}
		bind(event.ProviderTurnID, match)
	}

	// A native provider may omit persisted item ids even though its live stream
	// supplied them. Codex does this today: a live assistant message can be
	// `msg_...`, while thread/read later calls the same item `item-2`. Stable turn
	// identity still tells us which AO turn owns the replay, so suppress already
	// projected message/activity facts by semantic fingerprint. Counts preserve
	// legitimate repeated identical items within one turn.
	dropEvent := make([]bool, len(events))
	dropItem := make(map[string]bool)
	itemKey := func(event ports.ChatEvent) string {
		if event.ProviderItemID == "" {
			return ""
		}
		return event.ProviderTurnID + "\x00" + event.ProviderItemID
	}
	for i, event := range events {
		candidate := mapped[event.ProviderTurnID]
		if candidate == nil {
			continue
		}
		matched := false
		if aliased := providerItemAliasCandidate(event); aliased != nil && aliased == candidate {
			// The adapter identified the exact pre-namespacing item. Its detail can
			// legitimately differ only because nested provider ids inside that JSON
			// were namespaced too; do not re-import the same durable item under its
			// new outer id.
			matched = true
		}
		if fingerprint, ok := nativeHistoryEventMessageFingerprint(event); ok && candidate.messages[fingerprint] > 0 {
			candidate.messages[fingerprint]--
			matched = true
		}
		if event.Kind == ports.ChatEventActivityCompleted {
			fingerprint := nativeHistoryActivityFingerprint(
				event.ActivityKind, event.ActivityStatus, event.Summary, event.Detail,
			)
			if candidate.activities[fingerprint] > 0 {
				candidate.activities[fingerprint]--
				matched = true
			}
		}
		if !matched {
			continue
		}
		dropEvent[i] = true
		if key := itemKey(event); key != "" {
			dropItem[key] = true
		}
	}

	reconciled := make([]ports.ChatEvent, 0, len(events))
	for i, event := range events {
		if dropEvent[i] || dropItem[itemKey(event)] {
			continue
		}
		candidate := mapped[event.ProviderTurnID]
		if candidate == nil {
			reconciled = append(reconciled, event)
			continue
		}
		event.ProviderTurnID = candidate.providerTurnID
		// A replay may have no portable turn-outcome field. Preserve AO's stronger
		// known result only when the adapter reports recovered. Conversely, a known
		// replay outcome upgrades an older recovered observation.
		if event.Kind == ports.ChatEventTurnCompleted &&
			event.TurnState == domain.TurnStateRecovered && knownTurnOutcome(candidate.state) {
			event.TurnState = candidate.state
		}
		reconciled = append(reconciled, event)
	}
	return reconciled
}

func knownTurnOutcome(state domain.TurnState) bool {
	switch state {
	case domain.TurnStateCompleted, domain.TurnStateInterrupted, domain.TurnStateFailed:
		return true
	default:
		return false
	}
}

// rateLimitReadTimeout bounds the startup quota read. It is a local IPC call, and
// a provider that cannot answer it quickly must not hold up a conversation.
const rateLimitReadTimeout = 10 * time.Second

// readRateLimits seeds the account's quota position when the controller opens.
//
// The provider only pushes account/rateLimits/updated alongside a turn, which is
// too late for the thing this signal is for: a user wants to know they are near a
// wall BEFORE spending a turn discovering it. Reading once at startup closes that
// gap without giving clients a provider RPC to poll.
//
// Off the critical path on purpose, and failure is logged rather than surfaced: a
// conversation whose quota AO could not read is entirely usable, and refusing to
// start one over a missing readout would be a worse outcome than showing no meter.
func (c *Controller) readRateLimits() {
	reporter, ok := c.conv.(ports.ChatUsageReporter)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), rateLimitReadTimeout)
	defer cancel()

	limits, err := reporter.ReadRateLimits(ctx)
	if err != nil {
		c.log.Debug("chat rate limit read failed", "session", c.sessionID, "error", err)
		return
	}
	// Racing a pushed update is benign: both come from the same provider, and these
	// are percentages of a multi-day window that barely move between two reads
	// seconds apart.
	if err := c.store.RecordRateLimits(ctx, c.conversation.ID, domain.ConversationRateLimits{
		PrimaryUsedPercent:       limits.PrimaryUsedPercent,
		SecondaryUsedPercent:     limits.SecondaryUsedPercent,
		PrimaryResetsInSeconds:   limits.PrimaryResetsInSeconds,
		SecondaryResetsInSeconds: limits.SecondaryResetsInSeconds,
		PlanLabel:                limits.PlanLabel,
	}); err != nil {
		c.log.Debug("failed to record chat rate limits", "session", c.sessionID, "error", err)
	}
}

// ProviderConversationID is the handle to persist for resume.
func (c *Controller) ProviderConversationID() string { return c.conv.ProviderConversationID() }

// ConversationID is the durable conversation this controller writes to.
func (c *Controller) ConversationID() string { return c.conversation.ID }

// Generation fences events from a controller that has been replaced.
func (c *Controller) Generation() string { return c.generation }

// State reports controller health.
func (c *Controller) State() ports.ChatControllerState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Capabilities reports what this session's provider can do, so a client can gate
// a control before drawing it rather than discovering the answer from a refusal.
func (c *Controller) Capabilities() ports.ChatCapabilities {
	return c.conv.Capabilities()
}

// Send records a message and dispatches it, or queues it if the agent is busy.
//
// The durable record is written first: if the provider call then fails, the user
// can see their message and its delivery state rather than having it vanish. A
// retry carrying the same client message id is a no-op, so a flaky client cannot
// produce two provider turns.
//
// A message that arrives mid-turn stays queued rather than being pushed at the
// provider. Two reasons: the agent is a single conversation and a second
// concurrent turn is not a thing it can run, and a queued row is a promise AO can
// keep across a restart, which a message dropped into a busy provider is not.
func (c *Controller) Send(ctx context.Context, msg ports.ChatUserMessage) (domain.ConversationTurn, error) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.mu.Lock()
	handoff := c.handoff != controllerHandoffNone
	c.mu.Unlock()
	if handoff {
		return domain.ConversationTurn{}, ErrControllerHandoff
	}

	now := c.now()
	turnID := c.newID()
	deliveryContent := ""
	if len(msg.Content) > 0 {
		encoded, err := json.Marshal(msg.Content)
		if err != nil {
			return domain.ConversationTurn{}, fmt.Errorf("encode chat delivery content: %w", err)
		}
		deliveryContent = string(encoded)
	}
	record := domain.ConversationMessage{
		ID:                  c.newID(),
		Text:                msg.Text,
		Origin:              normalizeOrigin(msg.Origin),
		ClientMessageID:     msg.ClientMessageID,
		DeliveryContentJSON: deliveryContent,
	}

	created, err := c.store.AppendUserMessage(
		ctx, c.conversation.ID, c.sessionID, c.generation, record, turnID, now)
	if err != nil {
		return domain.ConversationTurn{}, fmt.Errorf("record user message: %w", err)
	}
	if !created {
		// Already delivered under this client message id. Returning the empty turn
		// signals "nothing new happened" without claiming a second dispatch.
		c.log.Debug("duplicate chat send ignored",
			"session", c.sessionID, "clientMessageId", msg.ClientMessageID)
		return domain.ConversationTurn{}, nil
	}

	if c.busy() {
		// AppendUserMessage wrote it as queued, which is exactly where it belongs
		// until the running turn ends. drain picks it up from there.
		return domain.ConversationTurn{
			ID:                 turnID,
			ConversationID:     c.conversation.ID,
			HandledBySessionID: c.sessionID,
			State:              domain.TurnStateQueued,
			RequestedAt:        now,
		}, nil
	}

	return c.dispatch(ctx, turnID, msg, now)
}

// RetryTurn re-dispatches a failed turn's durable prompt as a new turn.
//
// The content is loaded from AO's own rows, never from the caller, so the daemon
// owns what gets sent again. The original failed turn is never mutated or
// relabeled: the retry is a brand-new turn and both attempts stay in history.
//
// Idempotency: each retry turn stores an explicit, unique link to its source.
// A replayed request — an uncertain network round trip, a double-click, a
// client retry — looks that relation up before anything else and returns the
// turn that already exists, so caller-controlled message ids cannot counterfeit
// or consume a retry. A deliberate further attempt retries the failed child:
// the chain A -> B -> C is built from distinct sources, never by re-sending A.
// The current next-turn settings apply.
func (c *Controller) RetryTurn(ctx context.Context, turnID string) (domain.ConversationTurn, error) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.handoffActive() {
		return domain.ConversationTurn{}, ErrControllerHandoff
	}

	source, err := c.store.TurnByID(ctx, turnID)
	if err != nil {
		return domain.ConversationTurn{}, err
	}
	if source.ConversationID != c.conversation.ID {
		return domain.ConversationTurn{}, fmt.Errorf("%w: %s", domain.ErrNoConversationTurn, turnID)
	}
	if source.State != domain.TurnStateFailed || source.RolledBackAt != nil {
		return domain.ConversationTurn{}, fmt.Errorf("%w: %s", ErrTurnNotRetryable, turnID)
	}

	prompt, err := c.store.RetryPrompt(ctx, c.conversation.ID, turnID)
	if err != nil {
		return domain.ConversationTurn{}, fmt.Errorf("%w: %s", ErrTurnNotRetryable, turnID)
	}
	if !prompt.ActiveLineage {
		return domain.ConversationTurn{}, fmt.Errorf("%w: %s", ErrRetryStaleBranch, turnID)
	}
	if prompt.Origin != domain.MessageOriginHuman {
		return domain.ConversationTurn{}, fmt.Errorf("%w: %s", ErrTurnNotRetryable, turnID)
	}

	// A dispatch the provider never acknowledged may still have been accepted:
	// settling it failed was AO's safe guess, not a fact about delivery.
	// Re-dispatching such a prompt could run the work twice, which is the one
	// outcome a retry must never have. Refuse until the caller confirms.
	if source.ProviderTurnID == "" {
		return domain.ConversationTurn{}, fmt.Errorf("%w: %s", ErrRetryDeliveryUncertain, turnID)
	}

	// The replay check comes before the busy check on purpose: returning an
	// already-recorded attempt must stay true even while that attempt runs.
	if existingID, found, lookupErr := c.store.RetryTurnIDForSource(ctx, c.conversation.ID, turnID); lookupErr != nil {
		return domain.ConversationTurn{}, lookupErr
	} else if found {
		existing, existingErr := c.store.TurnByID(ctx, existingID)
		if existingErr != nil {
			return domain.ConversationTurn{}, existingErr
		}
		return existing, nil
	}

	if c.busy() {
		return domain.ConversationTurn{}, ErrTurnRunning
	}

	content, err := retryPromptContent(prompt.DeliveryContentJSON, c.Capabilities())
	if err != nil {
		return domain.ConversationTurn{}, err
	}

	now := c.now()
	newTurnID := c.newID()
	key := retryClientMessagePrefix + newTurnID
	created, err := c.store.AppendRetryUserMessage(ctx, c.conversation.ID, c.sessionID, c.generation,
		domain.ConversationMessage{
			ID:                  c.newID(),
			Text:                prompt.Text,
			Origin:              prompt.Origin,
			ClientMessageID:     key,
			DeliveryContentJSON: prompt.DeliveryContentJSON,
		}, newTurnID, turnID, now)
	if err != nil {
		return domain.ConversationTurn{}, fmt.Errorf("record retried message: %w", err)
	}
	if !created {
		// Lost a race with a concurrent identical click. Report the turn it
		// created rather than opening a second one.
		if existingID, found, lookupErr := c.store.RetryTurnIDForSource(ctx, c.conversation.ID, turnID); lookupErr == nil && found {
			if existing, existingErr := c.store.TurnByID(ctx, existingID); existingErr == nil {
				return existing, nil
			}
		}
		return domain.ConversationTurn{}, ErrTurnNotRetryable
	}

	return c.dispatch(ctx, newTurnID, ports.ChatUserMessage{
		Text:            prompt.Text,
		Content:         content,
		Origin:          prompt.Origin,
		ClientMessageID: key,
	}, now)
}

// retryPromptContent reconstructs provider-neutral durable prompt blocks and
// refuses anything that would be corrupted or silently discarded by the
// currently attached provider.
func retryPromptContent(raw string, capabilities ports.ChatCapabilities) ([]ports.ChatContent, error) {
	if raw == "" {
		return nil, nil
	}
	var content []ports.ChatContent
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		return nil, fmt.Errorf("%w: attachments are not valid JSON", ErrRetryContentInvalid)
	}
	if len(content) == 0 {
		return nil, fmt.Errorf("%w: attachment data contains no content blocks", ErrRetryContentInvalid)
	}
	for _, item := range content {
		switch item.Type {
		case "image":
			if item.Data == "" || !strings.HasPrefix(strings.ToLower(item.MIMEType), "image/") {
				return nil, fmt.Errorf("%w: image attachments require data and an image MIME type", ErrRetryContentInvalid)
			}
			if !capabilities.Has(ports.ChatCapabilityImages) {
				return nil, fmt.Errorf("%w: image attachments are unsupported", ErrRetryUnsupported)
			}
		case "resource":
			if item.URI == "" {
				return nil, fmt.Errorf("%w: embedded resources require a URI", ErrRetryContentInvalid)
			}
			if !capabilities.Has(ports.ChatCapabilityEmbeddedContext) {
				return nil, fmt.Errorf("%w: embedded resources are unsupported", ErrRetryUnsupported)
			}
		case "resource_link":
			if item.URI == "" || item.Name == "" {
				return nil, fmt.Errorf("%w: resource links require a URI and name", ErrRetryContentInvalid)
			}
			if !capabilities.Has(ports.ChatCapabilityResourceLinks) {
				return nil, fmt.Errorf("%w: resource links are unsupported", ErrRetryUnsupported)
			}
		default:
			return nil, fmt.Errorf("%w: unsupported attachment type %q", ErrRetryContentInvalid, item.Type)
		}
	}
	return content, nil
}

// Settings reports the provider choices for the next turn.
func (c *Controller) Settings() domain.ConversationSettings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.settings
}

// SetSettings records the provider choices for the next turn.
//
// The row is written first: if that fails, the in-memory copy must not move, or a
// restart would silently revert a choice the user watched take effect.
func (c *Controller) SetSettings(ctx context.Context, settings domain.ConversationSettings) error {
	if err := c.store.SetConversationSettings(ctx, c.conversation.ID, settings, c.now()); err != nil {
		return fmt.Errorf("record conversation settings: %w", err)
	}
	c.mu.Lock()
	c.settings = settings
	c.mu.Unlock()
	return nil
}

// turnSettings converts the stored choices into what a driver takes per turn.
func (c *Controller) turnSettings() ports.ChatTurnSettings {
	current := c.Settings()
	return ports.ChatTurnSettings{
		Model:    current.Model,
		Effort:   current.ReasoningEffort,
		Approval: current.ApprovalMode,
	}
}

// mergeUsage combines independently reported usage groups. Explicit flags are
// authoritative, including a meaningful zero after compaction. The inference is
// compatibility for existing drivers/tests that predate the flags and report a
// complete non-zero snapshot in one event.
func (c *Controller) mergeUsage(update ports.ChatUsage) domain.ConversationUsage {
	contextKnown := update.ContextKnown
	totalsKnown := update.TotalsKnown
	if !contextKnown && !totalsKnown {
		contextKnown = update.ContextUsed != 0 || update.ContextWindow != 0
		totalsKnown = update.InputTokens != 0 || update.OutputTokens != 0 ||
			update.CachedTokens != 0 || update.TotalTokens != 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if contextKnown {
		c.usage.ContextUsed = update.ContextUsed
		c.usage.ContextWindow = update.ContextWindow
	}
	if totalsKnown {
		c.usage.InputTokens = update.InputTokens
		c.usage.OutputTokens = update.OutputTokens
		c.usage.CachedTokens = update.CachedTokens
		c.usage.TotalTokens = update.TotalTokens
	}
	if update.Cost != nil {
		cost := *update.Cost
		c.usage.Cost = &cost
		c.usage.Currency = update.Currency
	}
	return c.usage
}

// busy reports whether a provider turn is in flight.
func (c *Controller) busy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pendingTurnID != ""
}

// dispatch hands a recorded turn to the provider. Callers must hold sendMu.
func (c *Controller) dispatch(
	ctx context.Context,
	turnID string,
	msg ports.ChatUserMessage,
	requestedAt time.Time,
) (domain.ConversationTurn, error) {
	// Every dispatch carries the conversation's choices, including one AO makes on
	// the user's behalf: a queued message draining, or a relay from `ao send`. A
	// setting that only applied when the user pressed send would silently stop
	// applying exactly when they were not watching.
	msg.Settings = c.turnSettings()

	c.mu.Lock()
	c.dispatchingTurnID = turnID
	c.mu.Unlock()
	ref, err := c.conv.SendTurn(ctx, msg)
	if err != nil {
		c.mu.Lock()
		if c.dispatchingTurnID == turnID {
			c.dispatchingTurnID = ""
		}
		c.mu.Unlock()
		// The provider may or may not have accepted it. Settle the turn as failed
		// rather than retrying: a duplicate turn would run the work twice. Settling
		// by AO's own turn id is required here — an undispatched turn has no
		// provider id, so looking one up by the empty string would hit whichever
		// undispatched turn the database returned first.
		completedAt := c.now()
		if settleErr := c.store.SettleTurnByID(
			ctx, turnID, domain.TurnStateFailed, err.Error(), completedAt); settleErr != nil {
			c.log.Error("failed to settle turn after send error", "error", settleErr)
		}
		return domain.ConversationTurn{
			ID:                 turnID,
			ConversationID:     c.conversation.ID,
			HandledBySessionID: c.sessionID,
			State:              domain.TurnStateFailed,
			ErrorMessage:       err.Error(),
			RequestedAt:        requestedAt,
			CompletedAt:        &completedAt,
		}, fmt.Errorf("send turn: %w", err)
	}

	if err := c.store.BindTurnToProvider(ctx, turnID, ref.ProviderTurnID, c.now()); err != nil {
		c.mu.Lock()
		if c.dispatchingTurnID == turnID {
			c.dispatchingTurnID = ""
		}
		c.mu.Unlock()
		if deferred, ok := c.conv.(ports.ChatDeferredTurnStarter); ok {
			deferred.DiscardDeferredTurn(ref.ProviderTurnID)
		}
		return domain.ConversationTurn{}, fmt.Errorf("bind turn: %w", err)
	}

	c.mu.Lock()
	c.pendingTurnID = ref.ProviderTurnID
	if c.dispatchingTurnID == turnID {
		c.dispatchingTurnID = ""
	}
	// Dispatched, not yet acknowledged: turn/start returning is AO's fact, and the
	// provider's own turn-started notification is the one an interrupt needs.
	c.ackedTurnID = ""
	c.mu.Unlock()

	// ACP's session/prompt request stays open until the turn finishes, so an ACP
	// driver prepares the request in SendTurn and starts it here. The durable
	// provider-id binding above must exist before the first streamed update can be
	// projected. Eager drivers do not implement this optional interface.
	if deferred, ok := c.conv.(ports.ChatDeferredTurnStarter); ok {
		if err := deferred.StartDeferredTurn(ref.ProviderTurnID); err != nil {
			c.mu.Lock()
			c.pendingTurnID = ""
			c.mu.Unlock()
			if settleErr := c.store.SettleTurnByID(
				ctx, turnID, domain.TurnStateFailed, err.Error(), c.now()); settleErr != nil {
				c.log.Error("failed to settle turn after deferred start error", "error", settleErr)
			}
			return domain.ConversationTurn{}, fmt.Errorf("start turn: %w", err)
		}
	}

	return domain.ConversationTurn{
		ID:                 turnID,
		ConversationID:     c.conversation.ID,
		HandledBySessionID: c.sessionID,
		ProviderTurnID:     ref.ProviderTurnID,
		State:              domain.TurnStateRunning,
		RequestedAt:        requestedAt,
	}, nil
}

// drain sends the next queued message now that the agent is free.
//
// Runs on the projection goroutine, so it observes turn completion in order with
// everything else the provider said. One message per call: the turn it starts
// makes the controller busy again, and the next completion drains the next.
func (c *Controller) drain(ctx context.Context) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.drainLocked(ctx)
}

// drainLocked is drain with the dispatch lock already held. Turn completion
// uses it so committing the completion, clearing primary ownership, and claiming
// the next queued request are one serialized lifecycle transition.
func (c *Controller) drainLocked(ctx context.Context) {
	c.mu.Lock()
	cutoff := c.cancelQueuedAt
	c.cancelQueuedAt = time.Time{}
	busy := c.pendingTurnID != ""
	handoff := c.handoff
	c.mu.Unlock()

	if busy {
		// Something already claimed the agent, so this drain has nothing to do.
		return
	}

	if !cutoff.IsZero() {
		// The user stopped the agent. Everything queued at that moment is
		// cancelled; anything typed afterwards is still theirs to send, and falls
		// through to the dispatch below.
		if err := c.store.CancelQueuedTurns(ctx, c.conversation.ID, cutoff, c.now()); err != nil {
			c.log.Error("failed to cancel queued turns", "session", c.sessionID, "error", err)
			return
		}
	}
	if handoff != controllerHandoffNone && handoff != controllerHandoffInterfaceDrain {
		return
	}

	queued, err := c.store.NextQueuedTurn(ctx, c.conversation.ID)
	if errors.Is(err, domain.ErrNoQueuedTurn) {
		return
	}
	if err != nil {
		c.log.Error("failed to read queued turn", "session", c.sessionID, "error", err)
		return
	}

	var content []ports.ChatContent
	if queued.DeliveryContentJSON != "" {
		if err := json.Unmarshal([]byte(queued.DeliveryContentJSON), &content); err != nil {
			_ = c.store.SettleTurnByID(ctx, queued.TurnID, domain.TurnStateFailed,
				"queued chat content is corrupt", c.now())
			c.log.Error("failed to decode queued chat content",
				"session", c.sessionID, "turn", queued.TurnID, "error", err)
			return
		}
	}
	if _, err := c.dispatch(ctx, queued.TurnID, ports.ChatUserMessage{
		Text:            queued.Text,
		Content:         content,
		Origin:          queued.Origin,
		ClientMessageID: queued.ClientMessageID,
	}, c.now()); err != nil {
		// dispatch already settled this turn as failed. Stopping here rather than
		// walking the rest of the queue: whatever broke the send is likely to break
		// the next one too, and failing them all on one bad provider state would
		// discard messages the user can otherwise still see waiting.
		c.log.Error("failed to dispatch queued turn",
			"session", c.sessionID, "turn", queued.TurnID, "error", err)
	}
}

// ArmHandoff is the linearization point for an interface transition. It closes
// source intake and queue dispatch synchronously while sendMu makes promotion
// impossible. Once this method returns, no queued turn accepted before the
// transition can cross the provider seam.
//
// This phase is deliberately reversible and performs no provider or durable
// queue mutation: target preflight can still fail. BeginHandoff settles the queue
// only after that preflight has succeeded.
func (c *Controller) ArmHandoff(
	ctx context.Context,
	policy domain.SessionInterfaceTransitionPolicy,
) error {
	if !policy.Valid() {
		return fmt.Errorf("invalid interface handoff policy %q", policy)
	}
	want := interfaceHandoff(policy)

	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	if c.handoff == want {
		c.mu.Unlock()
		return nil
	}
	if c.handoff != controllerHandoffNone {
		c.mu.Unlock()
		return ErrControllerHandoff
	}
	c.handoff = want
	c.mu.Unlock()
	return nil
}

// BeginHandoff quiesces a controller whose dispatch gate is already armed. It
// remains safe to call directly: focused callers and tests use this single deep
// interface, while Session Manager calls ArmHandoff synchronously before its
// background preflight and then reaches this method later.
func (c *Controller) BeginHandoff(
	ctx context.Context,
	policy domain.SessionInterfaceTransitionPolicy,
) error {
	if err := c.ArmHandoff(ctx, policy); err != nil {
		return err
	}

	if policy == domain.SessionInterfaceTransitionInterrupt {
		// Target preflight has succeeded. Settle every accepted queue row while
		// sendMu and the armed gate make dispatch and explicit promotion impossible.
		// Local durable state is committed before the external provider side effect.
		c.sendMu.Lock()
		c.mu.Lock()
		active := c.pendingTurnID != ""
		c.mu.Unlock()
		err := c.store.CancelAllQueuedTurns(ctx, c.conversation.ID, c.now())
		c.sendMu.Unlock()
		if err != nil {
			c.AbortHandoff()
			return fmt.Errorf("prepare interrupt handoff: cancel queued turns: %w", err)
		}
		if active {
			if err := c.interruptForHandoff(ctx); err != nil {
				c.AbortHandoff()
				return err
			}
		}
		// Provider acceptance is the source-side cancellation boundary. Some
		// adapters never report turn/completed for a killed long-running tool, so
		// waiting for that notification would make Terminal unreachable again.
		return nil
	}

	c.mu.Lock()
	active := c.pendingTurnID != ""
	c.mu.Unlock()
	if !active {
		// A queued row can exist in the narrow gap after a completion was
		// projected and before its drain ran. Claim it now so drain mode cannot
		// report quiescent while accepted work is still waiting.
		c.drain(ctx)
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		// Quiescence is a dispatch boundary, not merely "pendingTurnID is empty".
		// dispatch marks the durable row running before it installs pendingTurnID;
		// without sendMu a handoff can observe that narrow middle state as neither
		// queued nor active and start the target controller too early.
		c.sendMu.Lock()
		c.mu.Lock()
		busy := c.pendingTurnID != ""
		c.mu.Unlock()
		if !busy {
			_, err := c.store.NextQueuedTurn(ctx, c.conversation.ID)
			switch {
			case errors.Is(err, domain.ErrNoQueuedTurn):
				c.sendMu.Unlock()
				return nil
			case err != nil:
				c.sendMu.Unlock()
				c.AbortHandoff()
				return fmt.Errorf("check queued turns before handoff: %w", err)
			case policy == domain.SessionInterfaceTransitionDrain:
				c.drainLocked(ctx)
			}
		}
		c.sendMu.Unlock()
		select {
		case <-ctx.Done():
			c.AbortHandoff()
			return ctx.Err()
		case <-c.stopped:
			return nil
		case <-ticker.C:
		}
	}
}

// BeginIdleBranchHandoff installs a persistent intake fence only when the
// controller is already quiescent. Unlike an interface handoff it never drains
// or interrupts accepted work: branch changes are refused until the user stops
// the active turn and the durable queue is empty.
func (c *Controller) BeginIdleBranchHandoff(ctx context.Context) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handoff != controllerHandoffNone {
		return ErrControllerHandoff
	}
	if c.pendingTurnID != "" {
		return ErrTurnRunning
	}
	if _, err := c.store.NextQueuedTurn(ctx, c.conversation.ID); err == nil {
		return ErrTurnRunning
	} else if !errors.Is(err, domain.ErrNoQueuedTurn) {
		return fmt.Errorf("check queue before branch handoff: %w", err)
	}
	c.handoff = controllerHandoffIdleBranch
	c.branchHandoffDone = make(chan struct{})
	return nil
}

// AbortHandoff reopens source intake when a drain is cancelled before the source
// controller is stopped. Any queued work resumes through the ordinary drain.
func (c *Controller) AbortHandoff() {
	c.mu.Lock()
	resumeDispatch := c.handoff == controllerHandoffInterfaceDrain ||
		c.handoff == controllerHandoffInterfaceInterrupt
	branchHandoffDone := c.branchHandoffDone
	c.branchHandoffDone = nil
	c.handoff = controllerHandoffNone
	c.mu.Unlock()
	if branchHandoffDone != nil {
		close(branchHandoffDone)
	}
	if resumeDispatch {
		go c.drain(context.WithoutCancel(context.Background()))
	}
}

// completeBranchHandoff releases stopped-controller registry cleanup without
// reopening the obsolete source's intake. Commands may already hold a pointer
// captured before the registry swap, so the fence must remain closed forever.
func (c *Controller) completeBranchHandoff() {
	c.mu.Lock()
	branchHandoffDone := c.branchHandoffDone
	c.branchHandoffDone = nil
	c.mu.Unlock()
	if branchHandoffDone != nil {
		close(branchHandoffDone)
	}
}

// waitForBranchHandoff keeps the registry's stopped-controller cleanup from
// deleting a deliberately closed source while its forked replacement is being
// resumed and installed. Ordinary stops have no latch and return immediately.
func (c *Controller) waitForBranchHandoff() {
	c.mu.Lock()
	done := c.branchHandoffDone
	c.mu.Unlock()
	if done != nil {
		<-done
	}
}

// Resolve answers a pending approval. The provider is told first: if it rejects
// the decision, AO must not have already recorded the approval as answered.
func (c *Controller) Resolve(ctx context.Context, requestID string, decision ports.ChatDecision) error {
	if err := c.conv.ResolveRequest(ctx, requestID, decision); err != nil {
		return fmt.Errorf("resolve request %s: %w", requestID, err)
	}
	detail, _ := json.Marshal(map[string]string{"decision": decision.ID})
	if err := c.store.ResolveApproval(
		ctx, c.conversation.ID, requestID, string(detail), c.now()); err != nil {
		return fmt.Errorf("record approval %s: %w", requestID, err)
	}
	c.reportInteractionResolved(ctx, "chat.approval.resolved", c.now())
	return nil
}

// ResolveInput answers a structured form/URL request through the optional driver
// capability. The provider is told first for the same consent reason as an
// approval: AO must not record an answer that the live provider rejected.
func (c *Controller) ResolveInput(
	ctx context.Context,
	requestID string,
	response ports.ChatInputResponse,
) error {
	responder, ok := c.conv.(ports.ChatInputResponder)
	if !ok {
		return fmt.Errorf("%w: structured input", ports.ErrChatUnsupported)
	}
	if err := responder.ResolveInput(ctx, requestID, response); err != nil {
		return fmt.Errorf("resolve input %s: %w", requestID, err)
	}
	detail, _ := json.Marshal(map[string]any{
		"action":  response.Action,
		"content": response.Content,
	})
	if err := c.store.ResolveApproval(
		ctx, c.conversation.ID, requestID, string(detail), c.now()); err != nil {
		return fmt.Errorf("record input %s: %w", requestID, err)
	}
	c.reportInteractionResolved(ctx, "chat.input.resolved", c.now())
	return nil
}

// ErrCompactionUnsupported reports a driver whose provider cannot summarize
// history. Distinct from a failed compaction: "this agent has no way to reclaim
// context" is a permanent answer a client should stop offering, not something to
// retry.
var ErrCompactionUnsupported = errors.New("chat driver cannot compact history")

// ErrCompactionWhileBusy reports a compaction requested while a turn is running.
//
// Refused rather than forwarded because of what the provider does with it:
// `thread/compact/start` mid-turn silently INTERRUPTS the running turn, reports it
// as interrupted, and then compacts. Measured twice against a live app-server.
// Losing work the user is waiting on as a side effect of a housekeeping action is
// not something they should discover afterwards from the timeline, so AO makes them
// stop the turn themselves.
var ErrCompactionWhileBusy = errors.New("cannot compact while a turn is in flight")

// Compact summarizes earlier history to reclaim context.
//
// Without it a long conversation eventually cannot accept another turn at all:
// every turn re-sends the history, so context fills whether or not the user is
// doing anything unusual.
//
// It takes sendMu for the same reason a dispatch does, and the reason matters here:
// the busy check and the provider call have to be one step. Split, a message
// arriving in between would start a turn that the compaction then destroys — the
// exact outcome the check exists to prevent.
//
// The lock is released as soon as the provider accepts. The compaction itself runs
// as a provider turn for the next ten seconds or so, and holding sendMu across that
// would make the whole conversation unresponsive; the provider's own single-turn
// rule is what keeps a send from landing mid-compaction, and a send that arrives
// then queues behind the compaction turn like any other.
func (c *Controller) Compact(ctx context.Context) (ports.ChatCompactionResult, error) {
	compactor, ok := c.conv.(ports.ChatCompactor)
	if !ok {
		return ports.ChatCompactionResult{}, ErrCompactionUnsupported
	}

	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.handoffActive() {
		return ports.ChatCompactionResult{}, ErrControllerHandoff
	}

	if c.busy() {
		return ports.ChatCompactionResult{}, ErrCompactionWhileBusy
	}
	return compactor.Compact(ctx)
}

// turnAckWait bounds how long Interrupt waits for the provider to acknowledge a
// turn it has only just been handed.
//
// Stop appears the instant a message is sent, so a user can press it before the
// provider has started the turn — and a provider refuses to cancel a turn it does
// not yet consider active. Waiting out that gap is what makes the button reliable
// in the moment someone is most likely to use it: right after realizing they sent
// the wrong thing.
const turnAckWait = 3 * time.Second

// interruptForHandoff cancels only the provider turn. BeginHandoff has already
// cancelled the durable queue under the dispatch lock, so reusing Interrupt here
// would reintroduce a second, callback-ordered cancellation path.
func (c *Controller) interruptForHandoff(ctx context.Context) error {
	turn, ok := c.awaitAcknowledgedTurn(ctx)
	if !ok {
		return nil
	}
	if err := c.conv.Interrupt(ctx, turn); err != nil {
		if errors.Is(err, ports.ErrChatNoActiveTurn) {
			// Completion won the race after the dispatch gate was armed. The queue is
			// already terminal, so there is no remaining source work to release.
			return nil
		}
		return fmt.Errorf("interrupt turn %s for handoff: %w", turn, err)
	}
	return nil
}

// Interrupt cancels the in-flight turn, and with it anything queued behind it.
//
// The queue is cancelled because stop is the user's brake: a brake that releases
// the next message instead of stopping would be the opposite of what the button
// says. The cutoff is recorded before the provider call, so a completion that
// races back cannot drain the queue the interrupt was about to cancel.
// Durable state is what the UI renders, so it is also what Stop must be able to
// act on. Two recovery paths reconcile the cases where in-memory tracking and the
// durable turn row disagree:
//
//   - Memory says nothing is running but SQLite has a turn in 'running'. Either a
//     projection failure kept the completion from committing, or Stop landed in
//     the dispatch window after BindTurnToProvider wrote 'running' but before
//     pendingTurnID was set. Taking sendMu first means dispatch has finished (and
//     set pendingTurnID) before the disk is consulted — if a turn appeared while
//     we waited, the normal interrupt path runs instead.
//   - The provider answers ErrChatNoActiveTurn for a turn memory still tracks.
//     The provider is the authority that the work already ended; surfacing a bare
//     "no active turn" would leave the user staring at a Working bar they cannot
//     dismiss. Reconcile the durable row instead.
func (c *Controller) Interrupt(ctx context.Context) error {
	// Stop's linearization point is the dispatch lock. A Send that acquired the
	// lock first is existing work Stop should cancel; a Send that arrives after
	// this point waits and is therefore post-Stop work that must survive.
	c.sendMu.Lock()
	cutoff := c.now()
	c.mu.Lock()
	turn := c.pendingTurnID
	if turn != "" {
		// Publish the queue cutoff before releasing sendMu. A completion can then
		// win the provider race, but it cannot drain pre-Stop work as if Stop had
		// never happened.
		c.cancelQueuedAt = cutoff
	}
	c.mu.Unlock()

	if turn == "" {
		providerTurnIDs, err := c.store.ListVisibleRunningTurnProviderIDs(ctx, c.conversation.ID)
		if err != nil {
			c.sendMu.Unlock()
			return fmt.Errorf("check running turns: %w", err)
		}
		if len(providerTurnIDs) == 0 {
			c.sendMu.Unlock()
			return ErrNoActiveTurn
		}
		// The list uses the snapshot's visibility and ordering rules, so its first
		// row is the same running turn whose Working bar the user pressed Stop on.
		providerTurnID := providerTurnIDs[0]
		if err := c.conv.Interrupt(ctx, providerTurnID); err != nil &&
			!errors.Is(err, ports.ErrChatNoActiveTurn) {
			c.log.Warn("provider interrupt during reconciliation failed",
				"session", c.sessionID, "turn", providerTurnID, "error", err)
		}
		err = c.reconcileDurableTurnsLocked(ctx, providerTurnID, providerTurnIDs, cutoff)
		c.sendMu.Unlock()
		return err
	}
	c.sendMu.Unlock()

	// sendMu proved dispatch is complete, but the provider's turn-started event
	// may still be in transit. Wait for this exact turn: if it completes and a
	// post-Stop survivor starts, Stop must not follow ownership to that new turn.
	if !c.awaitTurnAcknowledged(ctx, turn) {
		return nil
	}

	if err := c.conv.Interrupt(ctx, turn); err != nil {
		if errors.Is(err, ports.ErrChatNoActiveTurn) {
			// Serialize durable settlement, memory cleanup, and queue promotion so
			// a message arriving after Stop cannot slip between those steps.
			c.sendMu.Lock()
			defer c.sendMu.Unlock()

			// A committed completion may have won the sendMu race after the
			// provider answered. It already consumed the cutoff and drained the
			// queue, so do not overwrite its terminal state or a successor turn.
			c.mu.Lock()
			stillPending := c.pendingTurnID == turn
			c.mu.Unlock()
			if !stillPending {
				return nil
			}
			providerTurnIDs, listErr := c.store.ListVisibleRunningTurnProviderIDs(ctx, c.conversation.ID)
			if listErr != nil {
				return fmt.Errorf("check running turns after provider refusal: %w", listErr)
			}
			return c.reconcileDurableTurnsLocked(ctx, turn, providerTurnIDs, cutoff)
		}
		// The interrupt did not happen, so the queue it was going to cancel is
		// still the user's to send.
		c.mu.Lock()
		c.cancelQueuedAt = time.Time{}
		c.mu.Unlock()
		return fmt.Errorf("interrupt turn %s: %w", turn, err)
	}
	return nil
}

// reconcileDurableTurnsLocked settles work the controller can no longer cancel
// through the normal path but durable state still shows as running. Every visible
// running row is settled because nested provider turns can coexist with the root;
// leaving any one of them running would leave the same Working bar behind. Callers
// hold sendMu so settlement, memory cleanup, queue cancellation, and survivor
// dispatch are one serialized transition. The cutoff is the original Stop time: a
// prompt submitted while provider cancellation was pending is new work and survives.
func (c *Controller) reconcileDurableTurnsLocked(
	ctx context.Context,
	targetProviderTurnID string,
	visibleProviderTurnIDs []string,
	cutoff time.Time,
) error {
	now := c.now()
	providerTurnIDs := make([]string, 0, len(visibleProviderTurnIDs)+1)
	if targetProviderTurnID != "" {
		providerTurnIDs = append(providerTurnIDs, targetProviderTurnID)
	}
	for _, providerTurnID := range visibleProviderTurnIDs {
		if providerTurnID != "" && providerTurnID != targetProviderTurnID {
			providerTurnIDs = append(providerTurnIDs, providerTurnID)
		}
	}
	for _, providerTurnID := range providerTurnIDs {
		if err := c.store.SettleTurn(ctx, c.conversation.ID, providerTurnID,
			domain.TurnStateInterrupted, "", now); err != nil {
			return fmt.Errorf("reconcile durable turn %s: %w", providerTurnID, err)
		}
	}

	c.mu.Lock()
	c.cancelQueuedAt = cutoff
	if c.pendingTurnID == targetProviderTurnID {
		c.pendingTurnID = ""
	}
	if c.ackedTurnID == targetProviderTurnID {
		c.ackedTurnID = ""
	}
	if c.pendingTurnID == "" {
		c.state = ports.ChatControllerReady
	}
	c.mu.Unlock()

	c.reportActivity(ctx, domain.ActivityIdle, "chat.interrupt.reconciled", now)
	// drainLocked consumes cancelQueuedAt, cancels only the pre-Stop queue, and
	// immediately dispatches the oldest surviving post-Stop prompt.
	c.drainLocked(ctx)
	return nil
}

// awaitAcknowledgedTurn returns the turn to interrupt once the provider has
// confirmed it started, or reports that there is nothing to cancel.
//
// It gives up immediately when no turn is in flight — that is a plain "nothing to
// stop" and must stay fast. It only waits in the narrow window where AO has
// dispatched a turn and the provider has not yet said so. On expiry it returns the
// turn anyway: the provider is the authority on whether it can be cancelled, and
// its refusal is already translated into a typed answer.
func (c *Controller) awaitAcknowledgedTurn(ctx context.Context) (string, bool) {
	c.mu.Lock()
	pending := c.pendingTurnID
	c.mu.Unlock()
	if pending == "" || !c.awaitTurnAcknowledged(ctx, pending) {
		return "", false
	}
	return pending, true
}

// awaitTurnAcknowledged waits only for the named turn. Ownership can move to a
// queued post-Stop survivor while this wait is in progress; following that move
// would interrupt new work the user submitted after pressing Stop.
func (c *Controller) awaitTurnAcknowledged(ctx context.Context, turn string) bool {
	deadline := time.Now().Add(turnAckWait)
	for {
		c.mu.Lock()
		pending, acked := c.pendingTurnID, c.ackedTurnID
		c.mu.Unlock()

		if pending != turn {
			return false
		}
		if acked == turn || time.Now().After(deadline) {
			return true
		}

		select {
		case <-ctx.Done():
			return true
		case <-c.stopped:
			return true
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Rollback discards a turn and everything after it, and reports how many turns went.
//
// Order is deliberate: the provider first, AO's rows second. If the provider
// refuses, AO must not already have hidden anything — a timeline missing turns the
// agent still remembers is the same lie in the other direction. If AO's write then
// fails, the error is returned and logged loudly: the provider call cannot be undone,
// and the raw provider-event archive plus the surviving turn rows are what make the
// disagreement repairable rather than invisible.
//
// The busy check is inside sendMu, which is also what dispatch holds, so a turn
// cannot start between the check and the provider call. That is the difference
// between refusing a rollback and racing one.
func (c *Controller) Rollback(ctx context.Context, turnID string) (int, error) {
	rollbacker, ok := c.conv.(ports.ChatRollbacker)
	if !ok {
		return 0, ErrRollbackUnsupported
	}

	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.handoffActive() {
		return 0, ErrControllerHandoff
	}

	if c.busy() {
		// Not a failure and not permanent: the agent is mid-thought, and the same
		// request works the moment it finishes.
		return 0, ErrTurnRunning
	}

	turn, err := c.store.TurnByID(ctx, turnID)
	if err != nil {
		return 0, err
	}
	if turn.ConversationID != c.conversation.ID {
		return 0, fmt.Errorf("%w: turn %s is not in this session's conversation",
			ErrTurnNotRollbackable, turnID)
	}
	if turn.BranchID != "" {
		turnBranch, branchErr := c.store.ConversationBranch(
			ctx, c.conversation.ID, turn.BranchID)
		if branchErr != nil {
			return 0, fmt.Errorf("load rollback turn branch: %w", branchErr)
		}
		activeBranch, branchErr := c.store.ConversationBranch(
			ctx, c.conversation.ID, c.conversation.ActiveBranchID)
		if branchErr != nil {
			return 0, fmt.Errorf("load active rollback branch: %w", branchErr)
		}
		if turnBranch.ProviderScopeID != activeBranch.ProviderScopeID {
			return 0, ErrTurnProviderMismatch
		}
	}
	if turn.ProviderTurnID == "" {
		// The provider never accepted this turn, so it holds no history to discard.
		// Hiding AO's rows anyway would leave the agent remembering more than the
		// timeline shows, which is the failure this whole operation exists to avoid.
		return 0, fmt.Errorf("%w: %s", ErrTurnNotRollbackable, turnID)
	}

	if err := rollbacker.Rollback(ctx, turn.ProviderTurnID); err != nil {
		return 0, classify(fmt.Errorf("roll back turn %s: %w", turnID, err))
	}

	discarded, err := c.store.RollbackTurns(ctx, c.conversation.ID, turnID, c.now())
	if err != nil {
		c.log.Error("chat rollback: provider discarded history but AO rows did not follow",
			"session", c.sessionID, "turn", turnID, "error", err)
		return 0, fmt.Errorf("record rollback of %s: %w", turnID, err)
	}
	return discarded, nil
}

// Close detaches this daemon's controller. Persistent provider hosts keep the
// native connection and any in-flight turn alive; non-persistent drivers retain
// their historical process-close behavior. The persistent host replays detached
// output and unresolved provider requests to the replacement controller.
func (c *Controller) Close(ctx context.Context) error {
	c.once.Do(func() {
		if preserver, ok := c.conv.(ports.ChatProviderPreserver); ok && preserver.PreservesProviderOnClose() {
			c.mu.Lock()
			c.preserveProviderOnStop = true
			c.mu.Unlock()
		}
		c.closeErr = c.conv.Close()
	})
	select {
	case <-c.stopped:
		return c.closeErr
	case <-ctx.Done():
		if c.closeErr != nil {
			return errors.Join(c.closeErr, ctx.Err())
		}
		return ctx.Err()
	}
}

// Terminate closes the controller and explicitly destroys a persistent provider
// host. Daemon shutdown uses Close so the host survives; user/session teardown
// and controller replacement use Terminate.
func (c *Controller) Terminate(ctx context.Context) error {
	c.once.Do(func() {
		if terminator, ok := c.conv.(ports.ChatProviderTerminator); ok {
			c.closeErr = terminator.Terminate()
		} else {
			c.closeErr = c.conv.Close()
		}
	})
	select {
	case <-c.stopped:
		return c.closeErr
	case <-ctx.Done():
		if c.closeErr != nil {
			return errors.Join(c.closeErr, ctx.Err())
		}
		return ctx.Err()
	}
}

// closeForBranchHandoff retires and terminates a source controller without
// publishing a session-level exit. Branch replacement is an in-place writer
// handoff, not the end of the session; the replacement generation continues the
// lifecycle, but the old persistent host must release exclusive ownership.
func (c *Controller) closeForBranchHandoff(ctx context.Context) error {
	c.prepareBranchHandoffStop()
	return c.Terminate(ctx)
}

func (c *Controller) prepareBranchHandoffStop() {
	c.mu.Lock()
	c.suppressStoppedActivity = true
	c.mu.Unlock()
}

func (c *Controller) reportFailedBranchHandoff(ctx context.Context) {
	c.mu.Lock()
	c.suppressStoppedActivity = false
	stopped := c.state == ports.ChatControllerStopped
	c.mu.Unlock()
	if !stopped {
		// project will publish the ordinary stop boundary when the stream ends.
		return
	}
	reportCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), branchHandoffReportLimit)
	defer cancel()
	c.reportActivity(reportCtx, domain.ActivityExited, "chat.controller.stopped", c.now())
}

// Wait blocks until the controller's event stream has ended.
func (c *Controller) Wait() { <-c.stopped }

// project consumes the driver's normalized events and writes them down. It runs
// until this controller's stream closes. That can mean provider termination or a
// deliberate detach from a provider that remains alive in a persistent host.
func (c *Controller) project() {
	defer close(c.stopped)

	// Detached from any request context: this outlives the call that started the
	// controller, and must keep persisting until the provider stream ends.
	ctx := context.WithoutCancel(context.Background())

	for event := range c.conv.Events() {
		c.mu.Lock()
		preserveProvider := c.preserveProviderOnStop
		c.mu.Unlock()
		if preserveProvider && event.Kind == ports.ChatEventControllerState && event.ControllerState == ports.ChatControllerStopped {
			continue
		}
		// A lifecycle event and a concurrent Send must agree on whether the root
		// conversation is busy. Holding the same lock Send/dispatch use closes the
		// window between the durable projection and the in-memory ownership update.
		lifecycle := event.Kind == ports.ChatEventTurnStarted || event.Kind == ports.ChatEventTurnCompleted
		if lifecycle {
			c.sendMu.Lock()
		}
		projected, primaryTurn, err := c.projectEvent(ctx, event)
		if err != nil {
			// A projection failure must not kill the provider stream. The store
			// rolls the archive back with its projection, so durable state remains
			// internally consistent and a later provider replay may retry it.
			c.log.Error("failed to project chat event",
				"session", c.sessionID, "kind", event.Kind, "error", err)
		} else if projected {
			c.afterProject(ctx, event, primaryTurn)
		}
		if lifecycle {
			c.sendMu.Unlock()
		}
	}

	c.mu.Lock()
	c.state = ports.ChatControllerStopped
	suppressStoppedActivity := c.suppressStoppedActivity
	preserveProvider := c.preserveProviderOnStop
	c.mu.Unlock()
	if preserveProvider {
		return
	}

	// The stream has ended, so nothing more can arrive for this controller. This
	// is the only place that reliably knows that — a provider process can die on
	// its own, in which case no AO code path called Close — so it is where
	// in-flight work gets settled.
	//
	// A turn the controller was running is not evidence the work finished, and an
	// approval still pending can never be answered because the provider call it
	// was blocking is gone. Both are closed out honestly rather than left looking
	// live forever.
	now := c.now()
	// The provider stream ending is the one universal stop signal. Some drivers
	// emit an explicit controller-state event first, but a process crash or lost
	// transport cannot. Report the same lifecycle boundary here so the session
	// does not remain durably active, idle, or blocked after its controller died.
	// ControllerGeneration fences this write from a replacement controller.
	if !suppressStoppedActivity {
		c.reportActivity(ctx, domain.ActivityExited, "chat.controller.stopped", now)
	}
	if _, err := c.store.CleanupOwnedControllerWork(
		ctx, c.sessionID, c.conversation.ID, c.generation, now,
	); err != nil {
		c.log.Error("failed to clean up stopped controller work", "session", c.sessionID, "error", err)
	}
}

// projectEvent archives one normalized provider event and applies its durable
// projection in the same SQLite transaction.
func (c *Controller) projectEvent(ctx context.Context, event ports.ChatEvent) (bool, bool, error) {
	record := map[string]any{
		"kind":                   event.Kind,
		"providerEventId":        event.ProviderEventID,
		"providerTurnId":         event.ProviderTurnID,
		"providerConversationId": event.ProviderConversationID,
		"providerItemId":         event.ProviderItemID,
		"providerItemAliases":    event.ProviderItemAliases,
		"clientMessageId":        event.ClientMessageID,
		"turnState":              event.TurnState,
		"delta":                  event.Delta,
		"text":                   event.Text,
		"activityKind":           event.ActivityKind,
		"activityStatus":         event.ActivityStatus,
		"summary":                event.Summary,
		"detail":                 json.RawMessage(nonEmptyJSON(event.Detail)),
		"requestId":              event.RequestID,
		"decisions":              event.Decisions,
		"controllerState":        event.ControllerState,
		"usage":                  event.Usage,
		"rateLimits":             event.RateLimits,
		"title":                  event.Title,
		"plan":                   event.Plan,
		"reroute":                event.Reroute,
		"account":                event.Account,
		"threadState":            event.ThreadState,
		"mcpServers":             event.MCPServers,
	}
	if c.harness == domain.HarnessCodex {
		// Codex account identity and subscription capacity are daemon-memory
		// account state. Conversation provider archives must not become a second
		// persistence path for email, plan, percentages, reset times, or raw
		// account payloads.
		if event.Kind == ports.ChatEventAccountChanged {
			record["account"] = nil
		}
		if event.Kind == ports.ChatEventRateLimits {
			record["rateLimits"] = nil
		}
	}
	record["diff"] = event.Diff
	if event.Input != nil {
		record["input"] = event.Input
	}
	if event.Err != nil {
		record["error"] = event.Err.Error()
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return false, false, fmt.Errorf("encode provider event archive: %w", err)
	}
	projected, err := c.store.ProjectProviderEvent(ctx, c.conversation.ID, c.sessionID,
		c.generation, event.ProviderEventID, string(event.Kind), string(payload), c.now(),
		func(txCtx context.Context) error { return c.apply(txCtx, event) })
	if err != nil || !projected {
		return projected, false, err
	}
	return true, c.applyCommittedTurnLifecycle(event), nil
}

// applyCommittedTurnLifecycle updates the controller's volatile ownership only
// after the matching durable projection commits. Codex streams events for nested
// child threads over the root connection, so only events from the conversation AO
// opened are allowed to claim or release the primary turn.
func (c *Controller) applyCommittedTurnLifecycle(event ports.ChatEvent) bool {
	if event.Kind != ports.ChatEventTurnStarted && event.Kind != ports.ChatEventTurnCompleted {
		return false
	}
	if event.ProviderConversationID != "" && event.ProviderConversationID != c.conv.ProviderConversationID() {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	switch event.Kind {
	case ports.ChatEventTurnStarted:
		// AO serializes root dispatch. A different turn id while one is pending is
		// auxiliary provider work, even for a protocol that cannot name its thread.
		if c.pendingTurnID != "" && c.pendingTurnID != event.ProviderTurnID {
			return false
		}
		c.pendingTurnID = event.ProviderTurnID
		c.ackedTurnID = event.ProviderTurnID
		c.state = ports.ChatControllerBusy
		return true
	case ports.ChatEventTurnCompleted:
		if c.pendingTurnID != event.ProviderTurnID {
			return false
		}
		c.pendingTurnID = ""
		c.dispatchingTurnID = ""
		if c.ackedTurnID == event.ProviderTurnID {
			c.ackedTurnID = ""
		}
		c.state = ports.ChatControllerReady
		return true
	default:
		return false
	}
}

func (c *Controller) apply(ctx context.Context, event ports.ChatEvent) error {
	now := c.now()

	switch event.Kind {
	case ports.ChatEventTurnStarted:
		c.mu.Lock()
		dispatchingTurnID := c.dispatchingTurnID
		c.mu.Unlock()
		if event.ProviderTurnID != "" {
			rootConversation := event.ProviderConversationID == "" || event.ProviderConversationID == c.conv.ProviderConversationID()
			if dispatchingTurnID != "" && rootConversation {
				if err := c.store.BindTurnToProvider(ctx, dispatchingTurnID, event.ProviderTurnID, now); err != nil {
					return fmt.Errorf("bind early provider-started turn %s: %w", event.ProviderTurnID, err)
				}
			} else {
				// A turn AO dispatched already has a row, bound in dispatch. This covers
				// the turn AO did NOT dispatch: a compaction, or work the provider resumed
				// from its own history. Adopting it is what keeps every item it emits
				// correlated, and without that the activities arrive with no turn and the
				// timeline quietly stops grouping them.
				if err := c.store.AdoptProviderTurn(ctx, c.conversation.ID, c.sessionID,
					c.generation, c.newID(), event.ProviderTurnID, now); err != nil {
					return fmt.Errorf("adopt provider-started turn %s: %w", event.ProviderTurnID, err)
				}
			}
		}
		return nil

	case ports.ChatEventUserMessageCompleted:
		if event.ProviderTurnID == "" || event.Text == "" {
			return nil
		}
		return c.store.AppendImportedUserMessage(ctx, c.conversation.ID, event.ProviderTurnID,
			domain.ConversationMessage{
				ID:              c.newID(),
				Role:            domain.MessageRoleUser,
				Origin:          domain.MessageOriginHuman,
				Text:            event.Text,
				ProviderItemID:  event.ProviderItemID,
				ClientMessageID: event.ClientMessageID,
			}, now)

	case ports.ChatEventTurnCompleted:
		message := ""
		if event.Err != nil {
			message = event.Err.Error()
		}
		state := event.TurnState
		if state == "" {
			// A completion with no status is not evidence of success.
			state = domain.TurnStateFailed
		}
		if err := c.store.SettleTurn(
			ctx, c.conversation.ID, event.ProviderTurnID, state, message, now); err != nil {
			return err
		}
		return nil

	case ports.ChatEventMessageDelta:
		return c.store.AppendAssistantDelta(ctx, c.conversation.ID,
			event.ProviderItemID, event.ProviderTurnID, event.Delta, c.newID(), now)

	case ports.ChatEventMessageCompleted:
		return c.store.SettleAssistantMessage(ctx, c.conversation.ID,
			event.ProviderItemID, event.ProviderTurnID, event.Text, c.newID(), now)

	case ports.ChatEventCommandOutputDelta:
		// Appended to the command's own activity row, not added to the timeline: a
		// row per delta would bury the conversation under one noisy command.
		//
		// A delta whose activity does not exist yet is dropped. The provider can
		// emit output before the item/started that creates the row, and inventing an
		// activity from a delta would mint a timeline entry with no command on it.
		found, err := c.store.AppendCommandOutput(ctx, c.conversation.ID,
			event.ProviderItemID, event.Delta, now)
		if err != nil {
			return err
		}
		if !found {
			c.log.Debug("command output delta had no activity to append to",
				"session", c.sessionID, "item", event.ProviderItemID)
		}
		return nil

	case ports.ChatEventTurnDiff:
		// Per-turn state, overwritten. The provider re-sends the whole diff on every
		// update, so appending a timeline row per notification would show the same
		// edits over and over as if they had happened repeatedly.
		if event.Diff == nil {
			return nil
		}
		diff := domain.ConversationTurnDiff{
			Truncated: event.Summary == domain.ChatDiffTruncatedSummary,
			Files:     make([]domain.ConversationDiffFile, 0, len(event.Diff.Files)),
		}
		for _, file := range event.Diff.Files {
			diff.Files = append(diff.Files, domain.ConversationDiffFile{
				Path:      file.Path,
				Additions: file.Additions,
				Deletions: file.Deletions,
				Status:    file.Status,
				OldPath:   file.OldPath,
			})
		}
		found, err := c.store.SetTurnDiff(ctx, c.conversation.ID, event.ProviderTurnID, diff, now)
		if err != nil {
			return err
		}
		if !found {
			// A turn from before this controller existed, seen after a restart.
			c.log.Debug("turn diff had no turn to attach to",
				"session", c.sessionID, "providerTurn", event.ProviderTurnID)
		}
		return nil

	case ports.ChatEventReasoningDelta, ports.ChatEventCommandInput, ports.ChatEventActivityText:
		// Provider prose for one activity, appended to that activity's own stream.
		// Deliberately not a timeline row per delta, and deliberately not the same
		// column as command output: a PTY echoes the keystrokes the agent sends, so
		// merging the two streams would show every typed line twice.
		//
		// A delta whose activity does not exist yet is dropped for the same reason a
		// command output delta is: the provider can stream before the item/started
		// that creates the row, and minting an activity from a delta would put a
		// timeline entry there with nothing on it.
		found, err := c.store.AppendActivityStreamedText(ctx, c.conversation.ID,
			event.ProviderItemID, event.Delta, now)
		if err != nil {
			return err
		}
		if !found {
			c.log.Debug("streamed activity text had no activity to append to",
				"session", c.sessionID, "kind", event.Kind, "item", event.ProviderItemID)
		}
		return nil

	case ports.ChatEventActivityStarted, ports.ChatEventActivityCompleted:
		if err := c.store.UpsertActivity(ctx, c.conversation.ID, event.ProviderTurnID,
			domain.ConversationActivity{
				ID:             c.newID(),
				Kind:           event.ActivityKind,
				Status:         event.ActivityStatus,
				Summary:        event.Summary,
				Detail:         event.Detail,
				ProviderItemID: event.ProviderItemID,
			}, now); err != nil {
			return err
		}
		// After the upsert, so the row exists to settle. Only when the provider
		// actually settled something: an empty settle would erase an accumulation the
		// user watched arrive, and a reasoning item with an empty summary array is the
		// normal shape on a build that streams no summaries at all.
		if event.Text != "" {
			if _, err := c.store.SettleActivityStreamedText(ctx, c.conversation.ID,
				event.ProviderItemID, event.Text, now); err != nil {
				return err
			}
		}
		return nil

	case ports.ChatEventPlanUpdated:
		// Per-turn state, overwritten. The provider re-sends the whole plan on every
		// change, so a row per update would read as though the agent had planned three
		// separate times in one turn.
		if event.Plan == nil {
			return nil
		}
		found, err := c.store.SetTurnPlan(ctx, c.conversation.ID, event.ProviderTurnID, *event.Plan)
		if err != nil {
			return err
		}
		if !found {
			// A turn from before this controller existed, seen after a restart.
			c.log.Debug("plan had no turn to attach to",
				"session", c.sessionID, "providerTurn", event.ProviderTurnID)
		}
		// Also a timeline row, upserted so the plan mutates in place rather than
		// stacking. The turn column answers "what is the plan now" without walking the
		// timeline; this row answers "where in the conversation did the agent plan",
		// which the column cannot. The compaction projection already sets this
		// precedent, and the two cannot disagree because both are written from one
		// event.
		return c.store.UpsertActivity(ctx, c.conversation.ID, event.ProviderTurnID,
			domain.ConversationActivity{
				ID:      c.newID(),
				Kind:    domain.ActivityKindPlan,
				Status:  planActivityStatus(*event.Plan),
				Summary: event.Summary,
				Detail:  planDetail(*event.Plan),
				// Synthetic, because turn/plan/updated carries no item id: the plan is
				// not an item. Keyed on the turn so a turn's plan is one row that
				// updates, and prefixed so it can never collide with a provider id.
				ProviderItemID: planItemID(event.ProviderTurnID),
			}, now)

	case ports.ChatEventModelRerouted:
		// A correction to a claim AO has already made. The composer names the model it
		// is sending to, so a substitution nobody recorded leaves every later reading
		// of the turn attributing the answer to a model that did not produce it.
		if event.Reroute == nil || event.Reroute.ToModel == "" {
			return nil
		}
		if err := c.store.RecordModelReroute(ctx, c.conversation.ID,
			domain.ConversationModelReroute{
				FromModel:      event.Reroute.FromModel,
				ToModel:        event.Reroute.ToModel,
				Reason:         event.Reroute.Reason,
				ProviderTurnID: event.ProviderTurnID,
				At:             now,
			}); err != nil {
			return err
		}
		// And a timeline row, because the substitution happened at a point in the
		// conversation: everything above it was answered by one model and everything
		// below by another, and conversation state alone cannot say where the line is.
		return c.store.UpsertActivity(ctx, c.conversation.ID, event.ProviderTurnID,
			domain.ConversationActivity{
				ID:     c.newID(),
				Kind:   domain.ActivityKindSystem,
				Status: domain.ActivityStatusCompleted,
				Summary: fmt.Sprintf("Provider answered with %s instead of %s",
					event.Reroute.ToModel, firstNonEmpty(event.Reroute.FromModel, "the selected model")),
				Detail:         rerouteDetail(*event.Reroute),
				ProviderItemID: rerouteItemID(event.ProviderTurnID),
			}, now)

	case ports.ChatEventAccountChanged:
		if event.Account == nil {
			return nil
		}
		if err := c.applyAccount(ctx, *event.Account, now); err != nil {
			return err
		}
		if c.onAccountChanged != nil {
			c.onAccountChanged(c.sessionID, c.generation, c.harness)
		}
		return nil

	case ports.ChatEventThreadState:
		if event.ThreadState == nil {
			return nil
		}
		return c.applyThreadState(ctx, *event.ThreadState, now)

	case ports.ChatEventMCPServers:
		if len(event.MCPServers) == 0 {
			return nil
		}
		return c.applyMCPServers(ctx, event.MCPServers)

	case ports.ChatEventCompacted:
		// A fact about the conversation, emitted from a provider-owned turn that AO
		// did not dispatch. Keep that native turn correlation even though the UI
		// renders it as a boundary between user turns: rollback needs to hide the
		// compaction when the provider forgets the turn that produced it.
		//
		// Recorded because it is the one thing the timeline cannot show without a
		// row: after a restart the reclaim figures are gone from memory, and a
		// conversation that silently lost half its history with nothing to mark where
		// reads as if the agent simply forgot.
		if err := c.store.UpsertActivity(ctx, c.conversation.ID, event.ProviderTurnID,
			domain.ConversationActivity{
				ID:     c.newID(),
				Kind:   domain.ActivityKindSystem,
				Status: domain.ActivityStatusCompleted,
				// Falls back to a plain label rather than an empty row: a driver that
				// reports a compaction without a summary still happened.
				Summary: firstNonEmpty(event.Summary, "Compacted the conversation history"),
				Detail:  compactionDetail(event),
				// The provider's own item id, so a compaction replayed across a
				// reconnect updates the existing row instead of adding a second.
				ProviderItemID: event.ProviderItemID,
			}, now); err != nil {
			return err
		}
		return c.store.MarkCompacted(ctx, c.conversation.ID, now)

	case ports.ChatEventApprovalRequested:
		detail := mergeApprovalDetail(event)
		return c.store.UpsertActivity(ctx, c.conversation.ID, event.ProviderTurnID,
			domain.ConversationActivity{
				ID:             c.newID(),
				Kind:           domain.ActivityKindApproval,
				Status:         domain.ActivityStatusPending,
				Summary:        event.Summary,
				Detail:         detail,
				RequestID:      event.RequestID,
				ProviderItemID: event.ProviderItemID,
			}, now)

	case ports.ChatEventThreadRenamed:
		return c.applyThreadTitle(ctx, event.Title, now)

	case ports.ChatEventApprovalResolved:
		// The provider resolved it, possibly through another client. Mark it so a
		// card still on screen elsewhere stops being actionable.
		detail, _ := json.Marshal(map[string]string{"resolvedBy": "provider"})
		return c.store.ResolveApproval(ctx, c.conversation.ID, event.RequestID, string(detail), now)

	case ports.ChatEventInputRequested:
		if event.Input == nil {
			return nil
		}
		detail, _ := json.Marshal(map[string]any{
			"inputMode":     event.Input.Mode,
			"message":       event.Input.Message,
			"schema":        event.Input.Schema,
			"url":           event.Input.URL,
			"elicitationId": event.Input.ElicitationID,
		})
		return c.store.UpsertActivity(ctx, c.conversation.ID, event.ProviderTurnID,
			domain.ConversationActivity{
				ID:             c.newID(),
				Kind:           domain.ActivityKindUserInput,
				Status:         domain.ActivityStatusPending,
				Summary:        firstNonEmpty(event.Input.Message, "The agent needs more information"),
				Detail:         detail,
				RequestID:      event.RequestID,
				ProviderItemID: event.ProviderItemID,
			}, now)

	case ports.ChatEventInputResolved:
		detail, _ := json.Marshal(map[string]string{"resolvedBy": "provider"})
		return c.store.ResolveApproval(ctx, c.conversation.ID, event.RequestID, string(detail), now)

	case ports.ChatEventUsage:
		if event.Usage == nil {
			return nil
		}
		// Overwrites rather than appends. Deliberately not reported as activity
		// either: token accounting arriving is not the agent doing work, and
		// treating it as such would keep a finished session looking busy.
		return c.store.RecordUsage(ctx, c.conversation.ID, c.mergeUsage(*event.Usage))

	case ports.ChatEventRateLimits:
		if event.RateLimits == nil {
			return nil
		}
		if c.harness == domain.HarnessCodex {
			if c.onCodexCapacityChanged != nil && event.RateLimits.CodexCapacity != nil {
				c.onCodexCapacityChanged(c.sessionID, c.generation, *event.RateLimits.CodexCapacity)
			}
			return nil
		}
		return c.store.RecordRateLimits(ctx, c.conversation.ID, domain.ConversationRateLimits{
			PrimaryUsedPercent:       event.RateLimits.PrimaryUsedPercent,
			SecondaryUsedPercent:     event.RateLimits.SecondaryUsedPercent,
			PrimaryResetsInSeconds:   event.RateLimits.PrimaryResetsInSeconds,
			SecondaryResetsInSeconds: event.RateLimits.SecondaryResetsInSeconds,
			PlanLabel:                event.RateLimits.PlanLabel,
		})

	case ports.ChatEventControllerState:
		if event.ControllerState == ports.ChatControllerStopped {
			_, err := c.store.CleanupOwnedControllerWork(
				ctx, c.sessionID, c.conversation.ID, c.generation, now)
			return err
		}
		return nil

	case ports.ChatEventError:
		message := "provider error"
		if event.Err != nil {
			message = event.Err.Error()
		}
		detail, _ := json.Marshal(map[string]string{"error": message})
		return c.store.UpsertActivity(ctx, c.conversation.ID, event.ProviderTurnID,
			domain.ConversationActivity{
				ID:      c.newID(),
				Kind:    domain.ActivityKindError,
				Status:  domain.ActivityStatusFailed,
				Summary: message,
				Detail:  detail,
			}, now)

	default:
		// An event kind this build does not model is archived but not projected.
		return nil
	}
}

// afterProject performs effects that intentionally live outside the SQLite
// archive/projection transaction. Lifecycle writes touch the sessions table and
// draining may call the provider, so either one inside the store transaction
// would hold the single writer connection across another subsystem.
func (c *Controller) afterProject(ctx context.Context, event ports.ChatEvent, primaryTurn bool) {
	now := c.now()
	switch event.Kind {
	case ports.ChatEventTurnStarted:
		if primaryTurn {
			c.reportActivity(ctx, domain.ActivityActive, "chat.turn.started", now)
		}
	case ports.ChatEventTurnCompleted:
		if !primaryTurn {
			return
		}
		c.reportActivity(ctx, domain.ActivityIdle, "chat.turn.completed", now)
		// The settled turn is committed before another queued turn can dispatch.
		c.drainLocked(ctx)
	case ports.ChatEventApprovalRequested:
		c.reportActivity(ctx, domain.ActivityWaitingInput, "chat.approval.requested", now)
	case ports.ChatEventApprovalResolved:
		c.reportInteractionResolved(ctx, "chat.approval.resolved", now)
	case ports.ChatEventInputRequested:
		c.reportActivity(ctx, domain.ActivityWaitingInput, "chat.input.requested", now)
	case ports.ChatEventInputResolved:
		c.reportInteractionResolved(ctx, "chat.input.resolved", now)
	case ports.ChatEventControllerState:
		// Volatile state moves only after the provider event and all of its durable
		// cleanup committed. Otherwise a rollback can say "stopped" in memory while
		// SQLite still contains live work.
		c.mu.Lock()
		c.state = event.ControllerState
		suppressStoppedActivity := c.suppressStoppedActivity
		c.mu.Unlock()
		if event.ControllerState == ports.ChatControllerStopped && !suppressStoppedActivity {
			c.reportActivity(ctx, domain.ActivityExited, "chat.controller.stopped", now)
		}
	case ports.ChatEventAccountChanged:
		if event.Account != nil && event.Account.ReauthRequired {
			c.reportActivity(ctx, domain.ActivityWaitingInput, "chat.account.reauth", now)
		}
	}
}

func (c *Controller) reportInteractionResolved(ctx context.Context, event string, now time.Time) {
	pending, err := c.store.HasPendingConversationInteractions(ctx, c.conversation.ID)
	if err != nil {
		c.log.Debug("chat pending-interaction check failed",
			"session", c.sessionID, "event", event, "error", err)
		return
	}
	if pending {
		return
	}
	c.reportActivity(ctx, domain.ActivityActive, event, now)
}

// applyThreadTitle records a title the provider reports for the thread and, when
// it may, adopts it as the session's label.
//
// The session's display name is the field that already exists for this, so a
// provider title lands there rather than in a parallel one — every surface that
// shows a session name picks it up with no change, and the sessions CDC trigger
// fans it out on the event clients already listen to.
//
// It never overwrites a name a person chose. The store's compare-and-set is what
// enforces that in one statement; a false result here means the user's label won,
// which is the correct outcome and not an error.
//
// A cleared title is recorded but never applied: the provider having no name for
// the thread is not a reason to strip the label off AO's session.
func (c *Controller) applyThreadTitle(ctx context.Context, title string, now time.Time) error {
	normalized := NormalizeTitle(title)
	if err := c.store.SetProviderTitle(ctx, c.conversation.ID, normalized, now); err != nil {
		return err
	}
	if normalized == "" {
		return nil
	}
	applied, err := c.store.ApplyProviderTitle(
		ctx, c.conversation.ID, c.sessionID, normalized, now)
	if err != nil {
		return err
	}
	if !applied {
		c.log.Debug("chat title not applied: session carries a name the user chose",
			"session", c.sessionID)
	}
	return nil
}

// applyAccount folds an account report into what AO already knows and records the
// result.
//
// A merge rather than a replace because the provider reports the account in pieces:
// account/updated carries the auth mode and plan and says nothing about credentials,
// while a credential demand carries only the demand. Writing either straight through
// would blank the other, so a session whose tokens expired would lose the plan label
// and one that changed plan would look like its credentials were fine again.
//
// A demand for credentials also gets a timeline row. It is the one account fact the
// user has to act on, and a turn that failed because the provider wanted a login it
// could not get is otherwise indistinguishable from any other failed turn.
func (c *Controller) applyAccount(
	ctx context.Context,
	update ports.ChatAccount,
	now time.Time,
) error {
	c.mu.Lock()
	if update.AuthMode != "" {
		c.account.AuthMode = update.AuthMode
	}
	if update.PlanLabel != "" {
		c.account.PlanLabel = update.PlanLabel
	}
	if update.ReauthRequired {
		at := now
		c.account.ReauthRequiredAt = &at
		c.account.ReauthReason = update.ReauthReason
	}
	account := c.account
	c.mu.Unlock()

	if err := c.store.RecordAccount(ctx, c.conversation.ID, account, now); err != nil {
		return err
	}
	if !update.ReauthRequired {
		return nil
	}
	detail, _ := json.Marshal(map[string]string{
		"event":  "auth.reauth_required",
		"reason": update.ReauthReason,
	})
	return c.store.UpsertActivity(ctx, c.conversation.ID, "",
		domain.ConversationActivity{
			ID:      c.newID(),
			Kind:    domain.ActivityKindSystem,
			Status:  domain.ActivityStatusFailed,
			Summary: "The provider needs you to sign in again",
			Detail:  detail,
			// Keyed on the reason so a provider retrying its demand updates one row
			// instead of filling the timeline with the same notice.
			ProviderItemID: "ao-reauth-" + firstNonEmpty(update.ReauthReason, "unknown"),
		}, now)
}

// applyThreadState folds a thread report into what AO already knows.
//
// Tri-state on purpose. A status report says nothing about archiving and an archive
// report says nothing about status, so each report updates only what it actually
// spoke about — otherwise an ordinary idle report would silently un-archive a
// thread.
func (c *Controller) applyThreadState(
	ctx context.Context,
	update ports.ChatThreadState,
	now time.Time,
) error {
	c.mu.Lock()
	if update.Status != "" {
		c.threadState.Status = update.Status
		// The flags belong to the status report that carried them: an active thread
		// that is no longer waiting on an approval reports active with no flags, and
		// keeping the old list would leave it looking blocked forever.
		c.threadState.WaitingOn = update.WaitingOn
	}
	if update.Archived != nil {
		if *update.Archived {
			at := now
			c.threadState.ArchivedAt = &at
		} else {
			c.threadState.ArchivedAt = nil
		}
	}
	if update.Closed && c.threadState.ClosedAt == nil {
		at := now
		c.threadState.ClosedAt = &at
	}
	c.threadState.UpdatedAt = now
	state := c.threadState
	c.mu.Unlock()

	return c.store.RecordThreadState(ctx, c.conversation.ID, state)
}

// applyMCPServers merges one server's report into the list and records it.
//
// The provider announces servers one at a time and re-announces all of them on
// every turn, so this is a merge by name. First-seen order is preserved so the list
// a client renders does not reshuffle between polls.
func (c *Controller) applyMCPServers(ctx context.Context, updates []ports.ChatMCPServer) error {
	c.mu.Lock()
	for _, update := range updates {
		if update.Name == "" {
			continue
		}
		if _, seen := c.mcpServers[update.Name]; !seen {
			c.mcpServerOrder = append(c.mcpServerOrder, update.Name)
		}
		c.mcpServers[update.Name] = domain.ConversationMCPServer{
			Name:          update.Name,
			Status:        update.Status,
			Error:         update.Error,
			FailureReason: update.FailureReason,
		}
	}
	servers := make([]domain.ConversationMCPServer, 0, len(c.mcpServerOrder))
	for _, name := range c.mcpServerOrder {
		if server, ok := c.mcpServers[name]; ok {
			servers = append(servers, server)
		}
	}
	c.mu.Unlock()

	return c.store.RecordMCPServers(ctx, c.conversation.ID, servers)
}

// ErrMCPReloadUnsupported reports a driver whose provider cannot restart its tool
// servers. Permanent rather than transient, so a client should stop offering it.
var ErrMCPReloadUnsupported = errors.New("chat driver cannot reload MCP servers")

// ReloadMCPServers restarts the provider's tool servers.
//
// It takes sendMu for the reason a compaction does: the reload tears down and
// re-establishes every tool the agent has, and doing that under a running turn would
// pull tools out from under work in flight. Refusing while busy makes the user stop
// the turn themselves rather than discovering afterwards from the timeline that
// their request failed for a reason they caused.
func (c *Controller) ReloadMCPServers(ctx context.Context) ([]domain.ConversationMCPServer, error) {
	reloader, ok := c.conv.(ports.ChatMCPReloader)
	if !ok {
		return nil, ErrMCPReloadUnsupported
	}

	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.handoffActive() {
		return nil, ErrControllerHandoff
	}

	if c.busy() {
		return nil, ErrTurnRunning
	}

	servers, err := reloader.ReloadMCPServers(ctx)
	if err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		// The reload succeeded and the provider did not enumerate. Its own startup
		// notifications are the authoritative report and are already on their way, so
		// the current list is the honest answer rather than an empty one.
		c.mu.Lock()
		current := make([]domain.ConversationMCPServer, 0, len(c.mcpServerOrder))
		for _, name := range c.mcpServerOrder {
			if server, found := c.mcpServers[name]; found {
				current = append(current, server)
			}
		}
		c.mu.Unlock()
		return current, nil
	}

	if err := c.applyMCPServers(ctx, servers); err != nil {
		return nil, err
	}

	c.mu.Lock()
	merged := make([]domain.ConversationMCPServer, 0, len(c.mcpServerOrder))
	for _, name := range c.mcpServerOrder {
		if server, found := c.mcpServers[name]; found {
			merged = append(merged, server)
		}
	}
	c.mu.Unlock()
	return merged, nil
}

func (c *Controller) handoffActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.handoff != controllerHandoffNone
}

// planItemID keys a turn's plan activity. The provider sends no item id with a plan
// update — a plan is not an item — so the key is synthesized from the turn, which
// is what makes repeated updates rewrite one row. The prefix keeps it from ever
// colliding with a provider item id.
func planItemID(providerTurnID string) string {
	if providerTurnID == "" {
		return ""
	}
	return "ao-plan-" + providerTurnID
}

// rerouteItemID keys a reroute notice the same way, for the same reason.
func rerouteItemID(providerTurnID string) string {
	if providerTurnID == "" {
		return "ao-reroute"
	}
	return "ao-reroute-" + providerTurnID
}

// planActivityStatus reports a plan as still running until every step is done.
// Status here is about the plan, not about the turn: a plan with work left is not a
// completed thing, and showing it as one would tick off steps the agent is still on.
func planActivityStatus(plan domain.ConversationPlan) domain.ActivityStatus {
	if len(plan.Steps) == 0 {
		return domain.ActivityStatusCompleted
	}
	for _, step := range plan.Steps {
		if step.Status != domain.PlanStepCompleted {
			return domain.ActivityStatusRunning
		}
	}
	return domain.ActivityStatusCompleted
}

// planDetail is the plan's typed payload. The steps go in as structure rather than
// as rendered text: a client that wants checkboxes needs the per-step status, and a
// client that wants prose can join them, but the reverse is not recoverable.
func planDetail(plan domain.ConversationPlan) []byte {
	steps := make([]map[string]any, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		steps = append(steps, map[string]any{"text": step.Text, "status": string(step.Status)})
	}
	detail := map[string]any{"event": "plan", "steps": steps}
	if plan.Explanation != "" {
		detail["explanation"] = plan.Explanation
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return nil
	}
	return encoded
}

// rerouteDetail is the reroute notice's typed payload.
func rerouteDetail(reroute ports.ChatModelReroute) []byte {
	detail := map[string]any{
		"event":   "model.rerouted",
		"toModel": reroute.ToModel,
	}
	if reroute.FromModel != "" {
		detail["fromModel"] = reroute.FromModel
	}
	if reroute.Reason != "" {
		detail["reason"] = reroute.Reason
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return nil
	}
	return encoded
}

// mergeApprovalDetail folds the provider's offered decisions into the activity
// payload, so the client renders buttons from what the provider actually allows
// rather than from a fixed set.
func mergeApprovalDetail(event ports.ChatEvent) []byte {
	merged := map[string]any{}
	if len(event.Detail) > 0 {
		_ = json.Unmarshal(event.Detail, &merged)
	}
	decisions := make([]map[string]string, 0, len(event.Decisions))
	for _, option := range event.Decisions {
		decision := map[string]string{"id": option.ID, "label": option.Label}
		if option.Kind != "" {
			decision["kind"] = string(option.Kind)
		}
		decisions = append(decisions, decision)
	}
	merged["decisions"] = decisions
	encoded, err := json.Marshal(merged)
	if err != nil {
		return event.Detail
	}
	return encoded
}

func normalizeOrigin(origin domain.MessageOrigin) domain.MessageOrigin {
	switch origin {
	case domain.MessageOriginHuman, domain.MessageOriginAutomation,
		domain.MessageOriginDaemon, domain.MessageOriginProvider:
		return origin
	default:
		return domain.MessageOriginHuman
	}
}

// compactionDetail stamps the driver's reclaim figures with what kind of system
// event this row is.
//
// `system` is a general bucket, so a reader cannot tell a compaction from whatever
// else lands there next. The discriminator is set here rather than in a driver
// because the choice of kind is this projection's, not the provider's — and a
// compaction rendered as a generic notice would lose the one thing that matters
// about it: that the history above it is no longer what the agent sees.
func compactionDetail(event ports.ChatEvent) []byte {
	merged := map[string]any{}
	if len(event.Detail) > 0 {
		_ = json.Unmarshal(event.Detail, &merged)
	}
	merged["event"] = "compaction"
	encoded, err := json.Marshal(merged)
	if err != nil {
		return event.Detail
	}
	return encoded
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func nonEmptyJSON(raw []byte) []byte {
	if len(raw) == 0 {
		return []byte("null")
	}
	return raw
}

// reportActivity feeds the lifecycle reduction that derives user-facing status.
//
// Chat uses the same pipeline terminal sessions use rather than persisting a
// second display status — AO derives status from durable facts at read time, and
// a parallel chat-only status would be a second source of truth to keep in sync.
//
// Best-effort: a rejected signal must not stop the durable projection, which is
// the record that actually matters. Lifecycle also rejects signals it considers
// stale, which is a legitimate outcome rather than an error to surface.
func (c *Controller) reportActivity(
	ctx context.Context,
	state domain.ActivityState,
	event string,
	now time.Time,
) {
	if c.activity == nil {
		return
	}
	if err := c.activity.ApplyActivitySignal(ctx, c.sessionID, ports.ActivitySignal{
		Valid:                true,
		State:                state,
		Timestamp:            now,
		Event:                event,
		ControllerGeneration: c.generation,
	}); err != nil {
		c.log.Debug("chat activity signal rejected",
			"session", c.sessionID, "event", event, "error", err)
	}
}
