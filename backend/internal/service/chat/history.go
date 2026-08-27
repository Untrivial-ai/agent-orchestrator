package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Conversation history operations: rollback, fork, and the thread title.
//
// The three share one property that shapes everything here: they change the
// provider's own record of the conversation, not AO's view of it. Rollback is the
// sharp one — it changes what the agent REMEMBERS — so AO's durable rows have to
// follow, or the timeline goes on showing prose the agent cannot recall.
//
// Each is optional on the driver and feature-detected, following the Models
// precedent: a provider that cannot do one of these gets a typed refusal the client
// can render as an absent affordance rather than a failed request.

// Typed outcomes for the history operations. Each one distinguishes a permanent
// answer from something worth retrying, because "this agent cannot undo" and "stop
// the agent first" ask completely different things of the person reading it.
var (
	// ErrRollbackUnsupported reports a provider that cannot discard history.
	ErrRollbackUnsupported = errors.New("chat driver cannot roll back history")
	// ErrForkUnsupported reports a provider that cannot branch a conversation.
	ErrForkUnsupported = errors.New("chat driver cannot fork a conversation")
	// ErrRenameUnsupported reports a provider whose thread carries no title.
	ErrRenameUnsupported = errors.New("chat driver cannot set a thread title")
	// ErrTurnRunning refuses a rollback while the agent is working. Retryable once
	// the turn ends, which is why it is separate from every other refusal here.
	ErrTurnRunning = errors.New("cannot roll back while a turn is running")
	// ErrTurnNotRollbackable reports a turn the provider never accepted. There is
	// no provider history to discard, so an undo would only hide AO's own rows and
	// leave the agent remembering more than the timeline shows.
	ErrTurnNotRollbackable = errors.New("turn was never dispatched to the provider")
	// ErrTurnProviderMismatch reports a durable turn owned by an earlier agent
	// provider. Its opaque provider turn id is invalid in the current controller.
	ErrTurnProviderMismatch = errors.New("conversation turn belongs to a different agent provider")
	// ErrTitleRequired refuses a blank thread title. The provider refuses one too,
	// and reporting it here keeps the error about the caller's input.
	ErrTitleRequired = errors.New("thread title is required")
	// ErrProviderRefused reports something the provider declined on its own terms
	// while the conversation stayed healthy. It is a conflict, never an internal
	// failure: the message it carries is the provider's own explanation.
	ErrProviderRefused = errors.New("the agent refused the request")
	// ErrEditTurnInvalid reports a prompt that cannot be safely reconstructed from
	// durable history, including malformed legacy structured content.
	ErrEditTurnInvalid = errors.New("conversation turn cannot be edited")
	// ErrBranchProviderMismatch refuses a historical branch that the active
	// provider binding cannot reopen.
	ErrBranchProviderMismatch = errors.New("conversation branch belongs to a different agent provider")
)

// providerRefusal is satisfied by driver errors that mean "the provider said no"
// rather than "the call did not get through".
//
// Declared here and satisfied structurally, so this package needs no import of any
// adapter and no adapter needs to know this package's vocabulary. Without the
// distinction every ordinary refusal would surface as a 500.
type providerRefusal interface {
	ChatRefusal() bool
}

// classify folds a driver error into the typed vocabulary above.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var refusal providerRefusal
	if errors.As(err, &refusal) && refusal.ChatRefusal() {
		return fmt.Errorf("%w: %w", ErrProviderRefused, err)
	}
	return err
}

// maxTitleRunes bounds a thread title, matching the automatic-semantic-task-titles
// design's contract. Longer output is treated as generation failure rather than
// truncated into something that reads like a sentence someone cut off.
const maxTitleRunes = 80

// Rollback discards a turn and everything after it, from the agent's memory and
// from AO's timeline.
//
// Refused while a turn is running, and refused BEFORE the provider is asked. The
// provider refuses this too, but relying on that alone would make the outcome a
// race: AO would have already decided to hide rows by the time it learned it could
// not. The controller holds its dispatch lock across the check and the call, so
// within AO the answer cannot change underneath.
func (s *Service) Rollback(ctx context.Context, id domain.SessionID, turnID string) (int, error) {
	if _, err := s.requireChatSession(ctx, id); err != nil {
		return 0, err
	}
	controller, err := s.Controller(id)
	if err != nil {
		return 0, err
	}
	if _, ok := controller.conv.(ports.ChatRollbacker); !ok {
		return 0, ErrRollbackUnsupported
	}
	return controller.Rollback(ctx, turnID)
}

// ForkConversation branches this session's provider conversation and returns the
// new provider handle.
//
// Deliberately not reachable over HTTP yet, and the reason is not effort. AO's
// schema allows one conversation per session, so a fork has to become a second AO
// session — and a second session gets a fresh worktree, while the provider's own
// documentation is explicit that a fork copies conversation history and does NOT
// revert or copy the file changes the agent made. The forked agent would remember
// editing files that are not in its tree, which is a worse failure than the absent
// feature: it would look like it worked.
//
// Landing a fork therefore needs the spawn path to adopt the source session's
// worktree state, which is a workspace decision and not a chat one. Until then this
// exists so the provider call is written, tested, and honest about what it returns,
// rather than half-wired into a UI that cannot be correct.
func (s *Service) ForkConversation(ctx context.Context, id domain.SessionID) (string, error) {
	if _, err := s.requireChatSession(ctx, id); err != nil {
		return "", err
	}
	controller, err := s.Controller(id)
	if err != nil {
		return "", err
	}
	forker, ok := controller.conv.(ports.ChatForker)
	if !ok {
		return "", ErrForkUnsupported
	}
	forked, err := forker.Fork(ctx, nil)
	if err != nil {
		return "", classify(fmt.Errorf("fork conversation for %s: %w", id, err))
	}
	return forked, nil
}

// EditMessage forks immediately before one durable human prompt, swaps the live
// controller to that provider branch, then sends the edited text with the exact
// structured content stored for the original prompt.
func (s *Service) EditMessage(
	ctx context.Context,
	id domain.SessionID,
	turnID string,
	msg ports.ChatUserMessage,
) (EditMessageResult, error) {
	if _, err := s.requireChatSession(ctx, id); err != nil {
		return EditMessageResult{}, err
	}
	source, err := s.Controller(id)
	if err != nil {
		return EditMessageResult{}, err
	}
	forker, canFork := source.conv.(ports.ChatForker)
	canReplay := supportsApproximateReplay(source.conv)
	anchor, err := s.store.ConversationEditAnchor(ctx, source.conversation.ID, turnID)
	if err != nil {
		return EditMessageResult{}, fmt.Errorf("%w: %w", ErrEditTurnInvalid, err)
	}
	var content []ports.ChatContent
	if anchor.OriginalDeliveryContentJSON != "" {
		if err := json.Unmarshal([]byte(anchor.OriginalDeliveryContentJSON), &content); err != nil {
			return EditMessageResult{}, fmt.Errorf("%w: decode stored prompt content: %w", ErrEditTurnInvalid, err)
		}
	}
	msg.Content = withoutInternalReplayContent(content)
	if anchor.RetryActiveBranch {
		branch, err := s.store.ConversationBranch(ctx, source.conversation.ID, anchor.SourceBranchID)
		if err != nil {
			return EditMessageResult{}, fmt.Errorf("load pending edited conversation: %w", err)
		}
		if domain.NormalizeConversationBranchStrategy(branch.Strategy) == domain.ConversationBranchStrategyApproximateContext {
			if !canReplay {
				return EditMessageResult{}, ErrForkUnsupported
			}
			replay, _, replayErr := s.approximateReplayContent(
				ctx, source.conversation.ID, anchor.ReplayFloorSequence, branch.ReplayCutoffSequence)
			if replayErr != nil {
				return EditMessageResult{}, fmt.Errorf("prepare edited conversation retry: %w", replayErr)
			}
			msg.Content = append([]ports.ChatContent{replay}, msg.Content...)
		}
		return s.sendEditedMessage(ctx, branch.ParentBranchID, branch.ID, source, msg, true)
	}
	sourceBranch, err := s.store.ConversationBranch(ctx, source.conversation.ID, anchor.SourceBranchID)
	if err != nil {
		return EditMessageResult{}, fmt.Errorf("load source conversation branch: %w", err)
	}
	canNativeFork := anchor.PreviousProviderTurnID != "" && canFork
	needsPriorContext := anchor.HasPriorContext
	needsReplay := !canNativeFork && needsPriorContext
	if needsReplay && !canReplay {
		return EditMessageResult{}, ErrForkUnsupported
	}
	if err := source.BeginIdleBranchHandoff(ctx); err != nil {
		return EditMessageResult{}, err
	}
	abortSource := true
	defer func() {
		if abortSource {
			source.AbortHandoff()
		}
	}()

	cfg, driver, err := s.branchLaunchConfig(id, source)
	if err != nil {
		return EditMessageResult{}, err
	}
	var providerConversationID string
	var provider ports.ChatConversation
	var replayContent ports.ChatContent
	var replayTruncated bool
	var providerScopeID string
	if canNativeFork {
		forkAnchor := anchor.PreviousProviderTurnID
		providerConversationID, err = forker.Fork(ctx, &forkAnchor)
		if err == nil {
			provider, err = driver.Resume(ctx, ports.ChatResumeConfig{
				SessionID: cfg.SessionID, ProviderConversationID: providerConversationID,
				DataDir: cfg.DataDir, WorkspacePath: cfg.WorkspacePath, Env: cfg.Env,
				Model: cfg.Model, Permissions: cfg.Permissions, SystemPrompt: cfg.SystemPrompt,
				ProviderScopeID:       sourceBranch.ProviderScopeID,
				AdditionalDirectories: cfg.AdditionalDirectories, MCPServers: cfg.MCPServers,
			})
		}
	} else {
		// Every Start owns a fresh namespace for provider-issued turn/item IDs,
		// even when editing the first prompt requires no replay payload.
		providerScopeID = s.newID()
		if needsReplay {
			var replayErr error
			replayContent, replayTruncated, replayErr = s.approximateReplayContent(
				ctx, source.conversation.ID, anchor.ReplayFloorSequence, anchor.ForkAfterSequence)
			if replayErr != nil {
				return EditMessageResult{}, fmt.Errorf("prepare edited conversation: %w", replayErr)
			}
		}
		provider, err = driver.Start(ctx, ports.ChatStartConfig{
			SessionID: cfg.SessionID, DataDir: cfg.DataDir, WorkspacePath: cfg.WorkspacePath,
			Env: cfg.Env, Model: cfg.Model, Permissions: cfg.Permissions,
			SystemPrompt: cfg.SystemPrompt, AdditionalDirectories: cfg.AdditionalDirectories,
			MCPServers: cfg.MCPServers, ProviderScopeID: providerScopeID,
		})
		if err == nil {
			providerConversationID = provider.ProviderConversationID()
		}
	}
	if err != nil {
		return EditMessageResult{}, classify(fmt.Errorf("prepare edited conversation: %w", err))
	}
	if provider == nil || providerConversationID == "" {
		if provider != nil {
			_ = provider.Close()
		}
		return EditMessageResult{}, errors.New("replacement provider conversation is not ready")
	}
	if replayContent.Type != "" && !supportsApproximateReplay(provider) {
		_ = provider.Close()
		return EditMessageResult{}, ErrForkUnsupported
	}

	branchID := s.newID()
	generation := s.newID()
	branch := domain.ConversationBranch{
		ID: branchID, ConversationID: source.conversation.ID, SessionID: id,
		ProviderConversationID: providerConversationID, ParentBranchID: anchor.SourceBranchID,
		ReplacedTurnID: anchor.ReplacedTurnID, ForkAfterSequence: anchor.ForkAfterSequence,
		CreatedAt: s.now(), Strategy: domain.ConversationBranchStrategyNative,
		ProviderScopeID: providerScopeID,
	}
	if replayContent.Type != "" {
		branch.Strategy = domain.ConversationBranchStrategyApproximateContext
		branch.ReplayCutoffSequence = anchor.ForkAfterSequence
		branch.ReplayTruncated = replayTruncated
	}
	conversation := source.conversation
	conversation.ActiveBranchID = branchID
	replacement := newController(id, conversation, generation, provider, s.store, s.activity, s.log, s.newID, s.now)
	if err := s.store.CreateAndActivateConversationBranch(ctx, id, branch, generation, s.now()); err != nil {
		_ = provider.Close()
		return EditMessageResult{}, fmt.Errorf("activate edited conversation: %w", err)
	}
	// Consume the replacement privately while its bootstrap prompt is recorded.
	// The source remains the registry owner with its intake fence installed, so a
	// concurrent ordinary send cannot become the first turn on the new branch.
	replacement.start()
	if replayContent.Type != "" {
		msg.Content = append([]ports.ChatContent{replayContent}, msg.Content...)
	}
	result, sendErr := s.sendEditedMessage(
		ctx, anchor.SourceBranchID, branchID, replacement, msg, false)
	if result.Turn.ID == "" || errors.Is(sendErr, ErrProviderRefused) {
		if err := s.store.ActivateConversationBranch(ctx, id, source.conversation.ID,
			anchor.SourceBranchID, source.ProviderConversationID(), source.generation, s.now()); err != nil {
			sendErr = errors.Join(sendErr, fmt.Errorf("restore source after rejected edit: %w", err))
			// The durable head still belongs to the child. Publish its controller
			// rather than reopening a source generation the store just rejected.
			if installErr := s.installStartedBranchController(
				ctx, id, source, replacement, anchor.SourceBranchID); installErr != nil {
				return result, errors.Join(sendErr, installErr)
			}
			abortSource = false
			return result, sendErr
		}
		if err := replacement.Close(ctx); err != nil {
			sendErr = errors.Join(sendErr, fmt.Errorf("close rejected edited conversation: %w", err))
		}
		return result, sendErr
	}
	if err := s.installStartedBranchController(
		ctx, id, source, replacement, anchor.SourceBranchID); err != nil {
		return result, errors.Join(sendErr, err)
	}
	abortSource = false
	return result, sendErr
}

// approximateReplayContent renders only the durable textual transcript before the
// edited prompt. Tool calls, approvals, and file changes are intentionally not
// replayed: doing so could repeat side effects. Providers that support native
// fork remain on that exact-history path instead.
const approximateReplayBudget = 24 * 1024

func (s *Service) approximateReplayContent(
	ctx context.Context,
	conversationID string,
	floor, cutoff int64,
) (ports.ChatContent, bool, error) {
	if s.reader == nil {
		return ports.ChatContent{}, false, errors.New("conversation snapshot reader is unavailable")
	}
	rows, err := s.reader.LoadConversationSnapshot(ctx, conversationID)
	if err != nil {
		return ports.ChatContent{}, false, fmt.Errorf("load conversation for replay: %w", err)
	}
	seed, truncated, err := buildApproximateReplayContext(rows.Messages, floor, cutoff)
	if err != nil {
		return ports.ChatContent{}, false, err
	}
	return ports.ChatContent{
		Type: "resource", URI: ports.ChatInternalReplayResourceURI, Name: "approximate conversation context",
		MIMEType: "application/json", Text: seed, Internal: true,
	}, truncated, nil
}

func supportsApproximateReplay(conversation ports.ChatConversation) bool {
	capabilities := conversation.Capabilities()
	return capabilities.Has(ports.ChatCapabilityPromptReplay) &&
		capabilities.Has(ports.ChatCapabilityEmbeddedContext)
}

func buildApproximateReplayContext(rows []domain.ConversationMessage, floor, cutoff int64) (string, bool, error) {
	type replayMessage struct {
		Sequence int64              `json:"sequence"`
		Role     domain.MessageRole `json:"role"`
		Text     string             `json:"text"`
	}
	type replayEnvelope struct {
		Kind      string          `json:"kind"`
		Messages  []replayMessage `json:"messages"`
		Truncated bool            `json:"truncated"`
	}
	encode := func(selected []replayMessage, truncated bool) ([]byte, error) {
		return json.Marshal(replayEnvelope{
			Kind: "approximate_conversation_context", Messages: selected, Truncated: truncated,
		})
	}
	messages := make([]replayMessage, 0, len(rows))
	for _, message := range rows {
		if message.Sequence <= floor || message.Sequence > cutoff || message.Streaming || strings.TrimSpace(message.Text) == "" {
			continue
		}
		switch message.Role {
		case domain.MessageRoleUser, domain.MessageRoleAssistant:
		default:
			continue
		}
		messages = append(messages, replayMessage{message.Sequence, message.Role, strings.TrimSpace(message.Text)})
	}
	sort.SliceStable(messages, func(i, j int) bool { return messages[i].Sequence < messages[j].Sequence })
	selected := make([]replayMessage, 0, len(messages))
	truncated := false
	for index := len(messages) - 1; index >= 0; index-- {
		candidate := make([]replayMessage, len(selected)+1)
		candidate[0] = messages[index]
		copy(candidate[1:], selected)
		encoded, marshalErr := encode(candidate, true)
		if marshalErr != nil {
			return "", false, fmt.Errorf("encode replay context: %w", marshalErr)
		}
		if len(encoded) > approximateReplayBudget {
			truncated = true
			continue
		}
		selected = candidate
	}
	encoded, err := encode(selected, truncated)
	if err != nil {
		return "", false, fmt.Errorf("encode replay context: %w", err)
	}
	// The all-messages envelope uses `false`, which is one byte larger than the
	// pessimistic `true` admission check above. Drop oldest context if that final
	// byte crosses the hard wire budget.
	for len(encoded) > approximateReplayBudget && len(selected) > 0 {
		selected = selected[1:]
		truncated = true
		encoded, err = encode(selected, true)
		if err != nil {
			return "", false, fmt.Errorf("encode replay context: %w", err)
		}
	}
	return string(encoded), truncated, nil
}

func withoutInternalReplayContent(content []ports.ChatContent) []ports.ChatContent {
	filtered := make([]ports.ChatContent, 0, len(content))
	for _, item := range content {
		if ports.IsInternalReplayContent(item) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

// sendEditedMessage always attaches the attempted replacement to its branch. A
// transport error is ambiguous: the provider may have accepted the prompt before
// the connection failed, so deleting and blindly retrying it could execute the
// work twice. Keeping the failed turn makes that uncertainty durable and leaves
// both alternatives navigable after restart. A provider-declared refusal is
// conclusive, so AO safely returns the user to the source branch.
func (s *Service) sendEditedMessage(
	ctx context.Context,
	sourceBranchID, activeBranchID string,
	controller *Controller,
	msg ports.ChatUserMessage,
	restoreConclusiveFailure bool,
) (EditMessageResult, error) {
	turn, sendErr := controller.Send(ctx, msg)
	result := EditMessageResult{
		SourceBranchID: sourceBranchID,
		ActiveBranchID: activeBranchID,
		Turn:           turn,
	}
	if turn.ID == "" {
		// Nothing durable was created, so the provider was never dispatched. This
		// includes a duplicate client-message id: treating the controller's empty
		// no-op result as a successful edit would leave an invisible active child.
		if sendErr == nil {
			sendErr = errors.New("edited message was not dispatched")
		}
		if restoreConclusiveFailure {
			if _, err := s.ActivateBranch(ctx, controller.sessionID, sourceBranchID); err != nil {
				sendErr = errors.Join(sendErr, fmt.Errorf("restore source after undispatched edit: %w", err))
			}
		}
		return result, classify(sendErr)
	}
	if sendErr != nil {
		if err := s.store.UpdateConversationBranchReplacement(ctx, activeBranchID, turn.ID); err != nil {
			sendErr = errors.Join(sendErr, fmt.Errorf("record failed edited turn: %w", err))
		}
		var refusal providerRefusal
		if restoreConclusiveFailure && errors.As(sendErr, &refusal) && refusal.ChatRefusal() {
			if _, err := s.ActivateBranch(ctx, controller.sessionID, sourceBranchID); err != nil {
				sendErr = errors.Join(sendErr, fmt.Errorf("restore source after refused edit: %w", err))
			}
		}
		return result, classify(sendErr)
	}
	if turn.ID != "" {
		if err := s.store.UpdateConversationBranchReplacement(ctx, activeBranchID, turn.ID); err != nil {
			return result, err
		}
	}
	return result, nil
}

// EditMessageResult identifies the source, selected child, and replacement turn.
type EditMessageResult struct {
	SourceBranchID string
	ActiveBranchID string
	Turn           domain.ConversationTurn
}

// ActivateBranch resumes a durable provider branch in the same worktree and
// swaps controllers without sending a new prompt.
func (s *Service) ActivateBranch(ctx context.Context, id domain.SessionID, branchID string) (string, error) {
	if _, err := s.requireChatSession(ctx, id); err != nil {
		return "", err
	}
	source, err := s.Controller(id)
	if err != nil {
		return "", err
	}
	branch, err := s.store.ConversationBranch(ctx, source.conversation.ID, branchID)
	if err != nil {
		return "", err
	}
	activeBranch, err := s.store.ConversationBranch(
		ctx, source.conversation.ID, source.conversation.ActiveBranchID)
	if err != nil {
		return "", fmt.Errorf("load active conversation branch: %w", err)
	}
	if branch.Active {
		return branch.ID, nil
	}
	cfg, driver, err := s.branchLaunchConfig(id, source)
	if err != nil {
		return "", err
	}
	if branch.ProviderBindingID != activeBranch.ProviderBindingID {
		// Provider scopes may differ between approximate siblings, but their
		// durable binding epoch must match. Crossing it could hand an opaque
		// conversation handle to a different adapter/configuration owner.
		return "", ErrBranchProviderMismatch
	}
	if err := source.BeginIdleBranchHandoff(ctx); err != nil {
		return "", err
	}
	abortSource := true
	defer func() {
		if abortSource {
			source.AbortHandoff()
		}
	}()
	provider, err := driver.Resume(ctx, ports.ChatResumeConfig{
		SessionID: cfg.SessionID, ProviderConversationID: branch.ProviderConversationID,
		DataDir: cfg.DataDir, WorkspacePath: cfg.WorkspacePath, Env: cfg.Env,
		Model: cfg.Model, Permissions: cfg.Permissions, SystemPrompt: cfg.SystemPrompt,
		ProviderScopeID:       branch.ProviderScopeID,
		AdditionalDirectories: cfg.AdditionalDirectories, MCPServers: cfg.MCPServers,
	})
	if err != nil {
		return "", fmt.Errorf("resume conversation branch %s: %w", branchID, err)
	}
	generation := s.newID()
	conversation := source.conversation
	conversation.ActiveBranchID = branch.ID
	replacement := newController(id, conversation, generation, provider, s.store, s.activity, s.log, s.newID, s.now)
	if err := s.store.ActivateConversationBranch(ctx, id, conversation.ID, branch.ID,
		branch.ProviderConversationID, generation, s.now()); err != nil {
		_ = provider.Close()
		return "", err
	}
	if err := s.installBranchController(ctx, id, source, replacement, source.conversation.ActiveBranchID); err != nil {
		return "", err
	}
	abortSource = false
	return branch.ID, nil
}

func (s *Service) branchLaunchConfig(
	id domain.SessionID,
	source *Controller,
) (StartConfig, ports.ChatDriver, error) {
	s.mu.RLock()
	cfg, ok := s.startConfigs[id]
	current := s.controllers[id]
	s.mu.RUnlock()
	if !ok || current != source {
		return StartConfig{}, nil, ErrControllerHandoff
	}
	driver, err := s.drivers.Driver(cfg.Harness)
	if err != nil {
		return StartConfig{}, nil, fmt.Errorf("chat driver for %s: %w", cfg.Harness, err)
	}
	return cloneStartConfig(cfg), driver, nil
}

func (s *Service) installBranchController(
	ctx context.Context,
	id domain.SessionID,
	source, replacement *Controller,
	sourceBranchID string,
) error {
	replacement.start()
	return s.installStartedBranchController(ctx, id, source, replacement, sourceBranchID)
}

// installStartedBranchController publishes a controller whose provider event
// stream is already being consumed. Edit bootstraps use this after recording the
// replacement prompt privately; ordinary branch activation starts and delegates
// through installBranchController above.
func (s *Service) installStartedBranchController(
	ctx context.Context,
	id domain.SessionID,
	source, replacement *Controller,
	sourceBranchID string,
) error {
	s.mu.Lock()
	if s.controllers[id] != source {
		s.mu.Unlock()
		_ = replacement.Close(ctx)
		if err := s.store.ActivateConversationBranch(ctx, id, source.conversation.ID,
			sourceBranchID, source.ProviderConversationID(), source.generation, s.now()); err != nil {
			return fmt.Errorf("restore source branch after controller swap conflict: %w", err)
		}
		return ErrControllerHandoff
	}
	s.controllers[id] = replacement
	s.mu.Unlock()

	go func() {
		replacement.Wait()
		s.mu.Lock()
		if current := s.controllers[id]; current == replacement {
			delete(s.controllers, id)
		}
		s.mu.Unlock()
	}()
	if err := source.Close(ctx); err != nil {
		s.log.Error("close source controller after branch swap", "session", id, "error", err)
	}
	return nil
}

// SetTitle names the provider's thread and returns the normalized title.
//
// Nothing is written to AO's rows here. The provider answers, then emits its own
// rename notification, and the projection applies it — so the title AO stores is
// always one the provider confirmed. Writing it optimistically as well would give
// one fact two authors and no way to tell which lost.
func (s *Service) SetTitle(ctx context.Context, id domain.SessionID, title string) (string, error) {
	if _, err := s.requireChatSession(ctx, id); err != nil {
		return "", err
	}
	normalized := NormalizeTitle(title)
	if normalized == "" {
		return "", ErrTitleRequired
	}
	controller, err := s.Controller(id)
	if err != nil {
		return "", err
	}
	renamer, ok := controller.conv.(ports.ChatRenamer)
	if !ok {
		return "", ErrRenameUnsupported
	}
	if err := renamer.SetTitle(ctx, normalized); err != nil {
		return "", classify(fmt.Errorf("set title for %s: %w", id, err))
	}
	return normalized, nil
}

// NormalizeTitle reduces a title to the one-line label AO is willing to show.
//
// The rules come from the automatic-semantic-task-titles design: one line, no
// wrapper punctuation, no trailing punctuation, and a hard length bound. They are
// applied to whatever the provider says rather than trusted, because a title is
// model output and arrives with heading markers, surrounding quotes, and full stops
// often enough that a client would otherwise render them.
//
// Over-length input is truncated at a word boundary rather than rejected: the
// provider already accepted the name, and refusing to display a title AO's own
// session is now carrying would leave the two disagreeing.
func NormalizeTitle(raw string) string {
	title := raw
	if idx := strings.IndexAny(title, "\r\n"); idx >= 0 {
		title = title[:idx]
	}
	title = strings.TrimSpace(title)

	// Markdown heading and list markers, which a model reaches for when asked for a
	// title and told nothing else.
	title = strings.TrimLeft(title, "#*->+ \t")
	// Wrapper punctuation, stripped from both ends so a quoted title is not shown
	// quoted and a backticked one is not shown as code.
	title = strings.Trim(title, "\"'`“”‘’ \t")
	title = strings.Join(strings.Fields(title), " ")
	title = strings.TrimRight(title, ".,;:!。 \t")

	if utf8.RuneCountInString(title) > maxTitleRunes {
		title = truncateAtWord(title, maxTitleRunes)
	}
	// A title made entirely of punctuation is not a title.
	if strings.IndexFunc(title, func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	}) < 0 {
		return ""
	}
	return title
}

// truncateAtWord cuts at the last space inside the limit, falling back to a hard cut
// for a language that does not put spaces between words.
func truncateAtWord(title string, limit int) string {
	runes := []rune(title)
	if len(runes) <= limit {
		return title
	}
	cut := string(runes[:limit])
	if idx := strings.LastIndex(cut, " "); idx > 0 {
		return strings.TrimRight(cut[:idx], " ")
	}
	return strings.TrimRight(cut, " ")
}
