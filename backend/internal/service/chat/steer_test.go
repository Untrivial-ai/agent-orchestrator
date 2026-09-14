package chat_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// Steering scenarios.
//
// The promise under test is not "the provider was called". It is that guidance goes
// to the turn the user is actually watching, that it shows up on the timeline so
// they can see it landed, and that every refusal is a typed answer rather than a
// failure — because the moment someone steers is the moment a turn is ending
// underneath them.

/* ---- a provider double that can be steered ----------------------------- */

type steerCall struct {
	turnID string
	msg    ports.ChatUserMessage
}

type steerRecorder struct {
	*fakeConversation

	mu     sync.Mutex
	calls  []steerCall
	err    error
	landed string
}

type cancelAfterSteerRecorder struct {
	*steerRecorder
	cancel context.CancelFunc
}

// blockingSteerRecorder holds a successful provider admission open so a test can
// race project ownership replacement against the whole promotion outcome.
type blockingSteerRecorder struct {
	*steerRecorder
	started     chan struct{}
	release     chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
}

func newBlockingSteerRecorder() *blockingSteerRecorder {
	return &blockingSteerRecorder{
		steerRecorder: newSteerRecorder(),
		started:       make(chan struct{}),
		release:       make(chan struct{}),
	}
}

func (s *blockingSteerRecorder) Steer(
	ctx context.Context,
	providerTurnID string,
	msg ports.ChatUserMessage,
) (ports.ChatTurnRef, error) {
	s.startOnce.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return s.steerRecorder.Steer(ctx, providerTurnID, msg)
	case <-ctx.Done():
		return ports.ChatTurnRef{}, ctx.Err()
	}
}

func (s *blockingSteerRecorder) releaseSteer() {
	s.releaseOnce.Do(func() { close(s.release) })
}

type cancelAfterInterruptRecorder struct {
	*steerRecorder
	cancel context.CancelFunc
	err    error
}

// blockingInterruptSteerRecorder keeps Stop inside the provider boundary so a
// competing queue command can exercise the interval after scope confirmation
// but before the interrupt outcome is known.
type blockingInterruptSteerRecorder struct {
	*steerRecorder
	interruptStarted chan struct{}
	releaseInterrupt chan struct{}
	interruptErr     error
	startOnce        sync.Once
	commandMu        sync.Mutex
	providerCommands []string
}

func newBlockingInterruptSteerRecorder(interruptErr error) *blockingInterruptSteerRecorder {
	return &blockingInterruptSteerRecorder{
		steerRecorder:    newSteerRecorder(),
		interruptStarted: make(chan struct{}),
		releaseInterrupt: make(chan struct{}),
		interruptErr:     interruptErr,
	}
}

func (s *blockingInterruptSteerRecorder) Interrupt(ctx context.Context, _ string) error {
	s.startOnce.Do(func() { close(s.interruptStarted) })
	select {
	case <-s.releaseInterrupt:
		return s.interruptErr
	case <-ctx.Done():
		return errors.Join(ports.ErrChatInterruptDeliveryUncertain, ctx.Err())
	}
}

func (s *cancelAfterInterruptRecorder) Interrupt(context.Context, string) error {
	s.cancel()
	return s.err
}

func (s *blockingInterruptSteerRecorder) Compact(context.Context) (ports.ChatCompactionResult, error) {
	s.recordProviderCommand("compact")
	return ports.ChatCompactionResult{}, nil
}

func (s *blockingInterruptSteerRecorder) Rollback(context.Context, string) error {
	s.recordProviderCommand("rollback")
	return nil
}

func (s *blockingInterruptSteerRecorder) ReloadMCPServers(context.Context) ([]ports.ChatMCPServer, error) {
	s.recordProviderCommand("mcp reload")
	return nil, nil
}

func (s *blockingInterruptSteerRecorder) ListConfigOptions(context.Context) ([]ports.ChatConfigOption, error) {
	return nil, nil
}

func (s *blockingInterruptSteerRecorder) SetConfigOption(
	context.Context,
	string,
	ports.ChatConfigOptionValue,
) ([]ports.ChatConfigOption, error) {
	s.recordProviderCommand("config")
	return nil, nil
}

func (s *blockingInterruptSteerRecorder) SetTitle(context.Context, string) error {
	s.recordProviderCommand("title")
	return nil
}

func (s *blockingInterruptSteerRecorder) ResolveRequest(
	context.Context,
	string,
	ports.ChatDecision,
) error {
	s.recordProviderCommand("approval")
	return nil
}

func (s *blockingInterruptSteerRecorder) ResolveInput(
	context.Context,
	string,
	ports.ChatInputResponse,
) error {
	s.recordProviderCommand("input")
	return nil
}

func (s *blockingInterruptSteerRecorder) recordProviderCommand(command string) {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	s.providerCommands = append(s.providerCommands, command)
}

func (s *blockingInterruptSteerRecorder) providerCommandSnapshot() []string {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	return append([]string(nil), s.providerCommands...)
}

func (s *cancelAfterSteerRecorder) Steer(
	ctx context.Context,
	providerTurnID string,
	msg ports.ChatUserMessage,
) (ports.ChatTurnRef, error) {
	ref, err := s.steerRecorder.Steer(ctx, providerTurnID, msg)
	s.cancel()
	return ref, err
}

func newSteerRecorder() *steerRecorder {
	return &steerRecorder{fakeConversation: newFakeConversation()}
}

func (s *steerRecorder) Steer(
	_ context.Context,
	providerTurnID string,
	msg ports.ChatUserMessage,
) (ports.ChatTurnRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, steerCall{turnID: providerTurnID, msg: msg})
	if s.err != nil {
		return ports.ChatTurnRef{}, s.err
	}
	landed := s.landed
	if landed == "" {
		landed = providerTurnID
	}
	return ports.ChatTurnRef{ProviderTurnID: landed}, nil
}

func (s *steerRecorder) steers() []steerCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]steerCall(nil), s.calls...)
}

func (s *steerRecorder) failWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// steerHarness starts a session whose provider can be steered, and puts a turn in
// flight — the only state steering is meaningful in.
func steerHarness(t *testing.T) (*harness, *steerRecorder) {
	t.Helper()
	return steerHarnessWithStore(t, func(st *store.Store) chatsvc.Store { return st })
}

func steerHarnessWithStore(
	t *testing.T,
	wrapStore func(*store.Store) chatsvc.Store,
) (*harness, *steerRecorder) {
	t.Helper()
	provider := newSteerRecorder()
	h := newHarnessWithConversationAndStore(t, provider, wrapStore)

	if _, err := h.svc.Send(context.Background(), testSession, ports.ChatUserMessage{
		Text:            "do the long thing",
		ClientMessageID: "turn-1",
		Origin:          domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// The provider's own acknowledgement. Steering is refused for a turn the provider
	// has not announced, so nothing can be steered before this arrives.
	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1",
	})
	return h, provider
}

func restartSteerService(
	t *testing.T,
	h *harness,
	provider *steerRecorder,
) *chatsvc.Service {
	t.Helper()
	if err := h.svc.Stop(context.Background(), testSession); err != nil {
		t.Fatalf("stop original service: %v", err)
	}
	var (
		idMu sync.Mutex
		id   int
	)
	svc := chatsvc.New(chatsvc.Options{
		Store: h.st, Sessions: h.st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: provider}},
		Log:     slog.New(slog.DiscardHandler),
		NewID: func() string {
			idMu.Lock()
			defer idMu.Unlock()
			id++
			return fmt.Sprintf("restart-steer-%d", id)
		},
		Now: h.now,
	})
	if _, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1",
	}); err != nil {
		t.Fatalf("restart service: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
	return svc
}

type failSteerCompletionStore struct {
	chatsvc.Store
}

func (s *failSteerCompletionStore) CompleteSteerDelivery(
	context.Context,
	string,
	string,
	string,
	domain.ConversationActivity,
	time.Time,
) error {
	return errors.New("injected steer completion failure")
}

// steerMarkers reads the steer entries out of a timeline the way a renderer must: by
// the discriminator in the detail payload. `system` is a general bucket, so the
// activity kind alone does not identify one.
func steerMarkers(s store.ConversationSnapshot) []struct {
	activity domain.ConversationActivity
	detail   struct {
		Event           string `json:"event"`
		Text            string `json:"text"`
		Origin          string `json:"origin"`
		ClientMessageID string `json:"clientMessageId"`
	}
} {
	type marker = struct {
		activity domain.ConversationActivity
		detail   struct {
			Event           string `json:"event"`
			Text            string `json:"text"`
			Origin          string `json:"origin"`
			ClientMessageID string `json:"clientMessageId"`
		}
	}
	var found []marker
	for _, a := range s.Activities {
		if a.Kind != domain.ActivityKindSystem || len(a.Detail) == 0 {
			continue
		}
		var m marker
		if err := json.Unmarshal(a.Detail, &m.detail); err != nil {
			continue
		}
		if m.detail.Event != "steer" {
			continue
		}
		m.activity = a
		found = append(found, m)
	}
	return found
}

/* ---- tests ------------------------------------------------------------- */

// The whole feature: guidance reaches the running turn and the timeline says so.
func TestSteerReachesTheRunningTurnAndLandsOnTheTimeline(t *testing.T) {
	h, provider := steerHarness(t)
	ctx := context.Background()

	result, err := h.svc.Steer(ctx, testSession, ports.ChatUserMessage{
		Text:            "actually, just summarize what you have",
		ClientMessageID: "steer-1",
		Origin:          domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if result.ProviderTurnID != "provider-turn-1" {
		t.Errorf("steered turn = %q, want provider-turn-1", result.ProviderTurnID)
	}
	if result.ActivityID == "" {
		t.Error("no activity id reported; a client cannot reconcile its own bubble")
	}

	calls := provider.steers()
	if len(calls) != 1 {
		t.Fatalf("provider saw %d steers, want 1", len(calls))
	}
	// The turn is named as a precondition rather than left to the provider to guess,
	// which is what stops a correction landing on work the user was not watching.
	if calls[0].turnID != "provider-turn-1" {
		t.Errorf("steered turn id = %q, want provider-turn-1", calls[0].turnID)
	}
	if calls[0].msg.Text != "actually, just summarize what you have" {
		t.Errorf("steer text = %q", calls[0].msg.Text)
	}
	if calls[0].msg.ClientMessageID != "steer-1" {
		t.Errorf("idempotency handle = %q, want steer-1", calls[0].msg.ClientMessageID)
	}

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(steerMarkers(s)) == 1
	})
	markers := steerMarkers(snapshot)
	if markers[0].detail.Text != "actually, just summarize what you have" {
		t.Errorf("recorded text = %q", markers[0].detail.Text)
	}
	if markers[0].detail.Origin != string(domain.MessageOriginHuman) {
		t.Errorf("recorded origin = %q, want human", markers[0].detail.Origin)
	}
	if markers[0].detail.ClientMessageID != "steer-1" {
		t.Errorf("recorded client message id = %q", markers[0].detail.ClientMessageID)
	}
	if markers[0].activity.Summary == "" {
		t.Error("the row has no summary; a collapsed timeline would show an empty entry")
	}

	// Bound to the turn it steered, not floating: an unattached row would leave the
	// guidance rendering outside the conversation it changed.
	var running string
	for _, turn := range snapshot.Turns {
		if turn.ProviderTurnID == "provider-turn-1" {
			running = turn.ID
		}
	}
	if running == "" {
		t.Fatalf("no turn row for provider-turn-1:\n%+v", snapshot.Turns)
	}
	if markers[0].activity.TurnID != running {
		t.Errorf("steer recorded on turn %q, want the running turn %q",
			markers[0].activity.TurnID, running)
	}

	// And it must not have opened a turn of its own. A second turn row would be
	// dispatched by the drain loop later, sending the correction twice.
	if len(snapshot.Turns) != 1 {
		t.Errorf("steering produced %d turns, want 1:\n%+v", len(snapshot.Turns), snapshot.Turns)
	}
}

// A retry with the same handle is the same guidance, not a second piece of it.
func TestSteerIsIdempotentOnTheClientHandle(t *testing.T) {
	h, provider := steerHarness(t)
	ctx := context.Background()

	msg := ports.ChatUserMessage{Text: "narrow the search", ClientMessageID: "steer-retry"}
	first, err := h.svc.Steer(ctx, testSession, msg)
	if err != nil {
		t.Fatalf("first Steer: %v", err)
	}
	replayed, err := h.svc.Steer(ctx, testSession, msg)
	if err != nil {
		t.Fatalf("retried Steer: %v", err)
	}
	if replayed != first {
		t.Fatalf("replayed result = %+v, want original %+v", replayed, first)
	}
	if calls := provider.steers(); len(calls) != 1 {
		t.Fatalf("provider received %d steer attempts, want one", len(calls))
	}

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(steerMarkers(s)) >= 1
	})
	if got := len(steerMarkers(snapshot)); got != 1 {
		t.Errorf("a retried steer produced %d timeline entries, want 1", got)
	}
}

func TestAcceptedSteerReplaysAfterControllerRestartWithoutProviderRedispatch(t *testing.T) {
	h, provider := steerHarness(t)
	msg := ports.ChatUserMessage{Text: "narrow the search", ClientMessageID: "steer-restart"}

	first, err := h.svc.Steer(context.Background(), testSession, msg)
	if err != nil {
		t.Fatalf("first Steer: %v", err)
	}
	if calls := provider.steers(); len(calls) != 1 {
		t.Fatalf("original provider received %d steer attempts, want one", len(calls))
	}

	restartedProvider := newSteerRecorder()
	restarted := restartSteerService(t, h, restartedProvider)
	replayed, err := restarted.Steer(context.Background(), testSession, msg)
	if err != nil {
		t.Fatalf("Steer after restart: %v", err)
	}
	if replayed != first {
		t.Fatalf("replayed result = %+v, want original %+v", replayed, first)
	}
	if calls := restartedProvider.steers(); len(calls) != 0 {
		t.Fatalf("restarted provider received %d steer attempts, want none", len(calls))
	}
}

func TestReservedSteerStaysUncertainAcrossRetryAndRestart(t *testing.T) {
	var flaky *failSteerCompletionStore
	h, provider := steerHarnessWithStore(t, func(st *store.Store) chatsvc.Store {
		flaky = &failSteerCompletionStore{Store: st}
		return flaky
	})
	msg := ports.ChatUserMessage{Text: "narrow the search", ClientMessageID: "steer-unknown"}

	_, err := h.svc.Steer(context.Background(), testSession, msg)
	if !errors.Is(err, chatsvc.ErrSteerDeliveryUncertain) {
		t.Fatalf("first Steer error = %v, want ErrSteerDeliveryUncertain", err)
	}
	_, err = h.svc.Steer(context.Background(), testSession, msg)
	if !errors.Is(err, chatsvc.ErrSteerDeliveryUncertain) {
		t.Fatalf("same-process retry error = %v, want ErrSteerDeliveryUncertain", err)
	}
	if calls := provider.steers(); len(calls) != 1 {
		t.Fatalf("provider received %d steer attempts after retry, want one", len(calls))
	}

	restartedProvider := newSteerRecorder()
	restarted := restartSteerService(t, h, restartedProvider)
	_, err = restarted.Steer(context.Background(), testSession, msg)
	if !errors.Is(err, chatsvc.ErrSteerDeliveryUncertain) {
		t.Fatalf("restart retry error = %v, want ErrSteerDeliveryUncertain", err)
	}
	if calls := restartedProvider.steers(); len(calls) != 0 {
		t.Fatalf("restarted provider received %d steer attempts, want none", len(calls))
	}
}

func TestSteerClientHandleCannotBeReusedForDifferentGuidance(t *testing.T) {
	h, provider := steerHarness(t)
	if _, err := h.svc.Steer(context.Background(), testSession, ports.ChatUserMessage{
		Text: "narrow the search", ClientMessageID: "steer-collision",
	}); err != nil {
		t.Fatalf("first Steer: %v", err)
	}
	_, err := h.svc.Steer(context.Background(), testSession, ports.ChatUserMessage{
		Text: "search everything", ClientMessageID: "steer-collision",
	})
	if !errors.Is(err, chatsvc.ErrSteerIdempotencyConflict) {
		t.Fatalf("changed retry error = %v, want ErrSteerIdempotencyConflict", err)
	}
	if calls := provider.steers(); len(calls) != 1 {
		t.Fatalf("provider received %d steer attempts, want one", len(calls))
	}
}

// A handoff refusal belongs to the original delivery handle. If the 409 response
// is lost, retrying after the source controller reopens or a new controller starts
// must replay that refusal rather than steering whichever turn happens to be live.
func TestSteerDuringInterfaceTransitionDurablyReplaysWithoutProviderDispatch(t *testing.T) {
	h, provider := steerHarness(t)
	msg := ports.ChatUserMessage{
		Text: "guidance typed during the switch", ClientMessageID: "steer-handoff",
		Origin: domain.MessageOriginHuman,
	}
	if err := h.ctrl.ArmHandoff(
		context.Background(), domain.SessionInterfaceTransitionDrain); err != nil {
		t.Fatalf("ArmHandoff: %v", err)
	}

	_, err := h.svc.Steer(context.Background(), testSession, msg)
	if !errors.Is(err, chatsvc.ErrControllerHandoff) {
		t.Fatalf("Steer during handoff error = %v, want ErrControllerHandoff", err)
	}
	if calls := provider.steers(); len(calls) != 0 {
		t.Fatalf("provider received %d steers during handoff, want none", len(calls))
	}

	// Model a lost 409: the caller did not observe it and retries only after the
	// transition was abandoned. The durable result still wins over current state.
	h.svc.AbortChatHandoff(testSession)
	_, err = h.svc.Steer(context.Background(), testSession, msg)
	if !errors.Is(err, chatsvc.ErrControllerHandoff) {
		t.Fatalf("same-controller replay error = %v, want durable ErrControllerHandoff", err)
	}
	if calls := provider.steers(); len(calls) != 0 {
		t.Fatalf("provider received %d steers after handoff reopened, want none", len(calls))
	}

	restartedProvider := newSteerRecorder()
	restarted := restartSteerService(t, h, restartedProvider)
	_, err = restarted.Steer(context.Background(), testSession, msg)
	if !errors.Is(err, chatsvc.ErrControllerHandoff) {
		t.Fatalf("new-controller replay error = %v, want durable ErrControllerHandoff", err)
	}
	if calls := restartedProvider.steers(); len(calls) != 0 {
		t.Fatalf("new provider received %d steers for prior handoff refusal, want none", len(calls))
	}
}

// Nothing in flight is an ordinary outcome — the turn finished while the user was
// typing — and the provider must not be asked.
func TestSteerWithNothingInFlightIsTypedAndNeverReachesTheProvider(t *testing.T) {
	provider := newSteerRecorder()
	h := newHarnessWithConversation(t, provider)
	msg := ports.ChatUserMessage{Text: "too late", ClientMessageID: "steer-no-active"}

	_, err := h.svc.Steer(context.Background(), testSession, msg)
	if !errors.Is(err, chatsvc.ErrNoActiveTurn) {
		t.Fatalf("err = %v, want ErrNoActiveTurn", err)
	}
	if len(provider.steers()) != 0 {
		t.Error("asked the provider to steer with no turn in flight")
	}

	// Even if a different turn starts before recovery, the original handle owns the
	// durable refusal. A lost 409 must not turn into guidance for later work.
	if _, err := h.svc.Send(context.Background(), testSession, ports.ChatUserMessage{
		Text: "later work", ClientMessageID: "later-turn",
	}); err != nil {
		t.Fatalf("start later turn: %v", err)
	}
	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1",
	})
	_, err = h.svc.Steer(context.Background(), testSession, msg)
	if !errors.Is(err, chatsvc.ErrNoActiveTurn) {
		t.Fatalf("retry after later turn error = %v, want durable ErrNoActiveTurn", err)
	}
	if len(provider.steers()) != 0 {
		t.Error("a recovered refusal was delivered into a later turn")
	}
}

// The provider is the authority on whether its turn is still steerable, and losing
// that race must read as "nothing to steer", not as a failure.
func TestSteerRaceLostToTheProviderIsReportedAsNoActiveTurn(t *testing.T) {
	h, provider := steerHarness(t)
	provider.failWith(ports.ErrChatNoSteerableTurn)
	msg := ports.ChatUserMessage{Text: "guidance", ClientMessageID: "steer-refused"}

	_, err := h.svc.Steer(context.Background(), testSession, msg)
	if !errors.Is(err, chatsvc.ErrNoActiveTurn) {
		t.Fatalf("err = %v, want ErrNoActiveTurn", err)
	}
	_, err = h.svc.Steer(context.Background(), testSession, msg)
	if !errors.Is(err, chatsvc.ErrNoActiveTurn) {
		t.Fatalf("retried err = %v, want ErrNoActiveTurn", err)
	}
	if calls := provider.steers(); len(calls) != 1 {
		t.Fatalf("provider received %d refused steer attempts, want one", len(calls))
	}

	// Nothing recorded: a timeline claiming guidance the agent never received would
	// have the user waiting for an answer to something it never heard.
	snapshot, loadErr := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
	if loadErr != nil {
		t.Fatalf("load snapshot: %v", loadErr)
	}
	if got := len(steerMarkers(snapshot)); got != 0 {
		t.Errorf("recorded %d steers for a refused one", got)
	}

	restartedProvider := newSteerRecorder()
	restarted := restartSteerService(t, h, restartedProvider)
	_, err = restarted.Steer(context.Background(), testSession, msg)
	if !errors.Is(err, chatsvc.ErrNoActiveTurn) {
		t.Fatalf("restart retry error = %v, want ErrNoActiveTurn", err)
	}
	if calls := restartedProvider.steers(); len(calls) != 0 {
		t.Fatalf("restarted provider received %d refused steer attempts, want none", len(calls))
	}
}

// A turn that is running but cannot take guidance (a compaction, a review) is a
// different answer: retryable once it ends, so it keeps its own sentinel.
func TestSteerOfAnUnsteerableTurnKeepsItsOwnOutcome(t *testing.T) {
	h, provider := steerHarness(t)
	provider.failWith(ports.ErrChatTurnNotSteerable)

	_, err := h.svc.Steer(context.Background(), testSession,
		ports.ChatUserMessage{Text: "guidance"})
	if !errors.Is(err, chatsvc.ErrTurnNotSteerable) {
		t.Fatalf("err = %v, want ErrTurnNotSteerable", err)
	}
	if errors.Is(err, chatsvc.ErrNoActiveTurn) {
		t.Error("an unsteerable running turn was reported as no turn at all")
	}
}

// A provider with no steering at all: a permanent answer, so a client hides the
// control instead of retrying.
func TestSteerIsRefusedWhenTheDriverCannotDoIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	msg := ports.ChatUserMessage{Text: "guidance", ClientMessageID: "steer-unsupported"}

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "do the long thing", ClientMessageID: "turn-1",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})

	_, err := h.svc.Steer(ctx, testSession, msg)
	if !errors.Is(err, chatsvc.ErrSteerUnsupported) {
		t.Fatalf("err = %v, want ErrSteerUnsupported", err)
	}

	restartedProvider := newSteerRecorder()
	restarted := restartSteerService(t, h, restartedProvider)
	_, err = restarted.Steer(ctx, testSession, msg)
	if !errors.Is(err, chatsvc.ErrSteerUnsupported) {
		t.Fatalf("restart retry error = %v, want durable ErrSteerUnsupported", err)
	}
	if calls := restartedProvider.steers(); len(calls) != 0 {
		t.Fatalf("restarted capable provider received %d attempts for a prior refusal, want none", len(calls))
	}
}

func TestSteerRejectsEmptyText(t *testing.T) {
	h, provider := steerHarness(t)

	_, err := h.svc.Steer(context.Background(), testSession,
		ports.ChatUserMessage{Text: "  \n "})
	if !errors.Is(err, chatsvc.ErrSteerTextRequired) {
		t.Fatalf("err = %v, want ErrSteerTextRequired", err)
	}
	if len(provider.steers()) != 0 {
		t.Error("sent an empty steer to the provider")
	}
}

// The trap this waits for is real: the provider refuses a steer for a turn it has
// accepted but not yet announced, and steering is most useful in exactly that
// window. So a steer that arrives between dispatch and acknowledgement must WAIT for
// the acknowledgement rather than being refused or fired early.
func TestSteerWaitsForTheProviderToAcknowledgeTheTurn(t *testing.T) {
	provider := newSteerRecorder()
	h := newHarnessWithConversation(t, provider)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "do the long thing", ClientMessageID: "turn-1",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Dispatched but NOT acknowledged: no turn/started has arrived.

	done := make(chan error, 1)
	go func() {
		_, err := h.svc.Steer(ctx, testSession, ports.ChatUserMessage{Text: "guidance"})
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("steer resolved before the provider acknowledged the turn (err=%v); "+
			"the provider would have refused it", err)
	case <-time.After(150 * time.Millisecond):
	}

	provider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Steer after acknowledgement: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("steer never completed after the turn was acknowledged")
	}

	calls := provider.steers()
	if len(calls) != 1 || calls[0].turnID != "provider-turn-1" {
		t.Fatalf("provider saw %+v, want one steer for provider-turn-1", calls)
	}
}

// The provider names the turn its guidance joined, and AO attributes the row to
// that turn rather than to the one it asked about. Same id in practice; asserted so
// a provider that answered differently could not be silently misfiled.
func TestSteerRecordsTheTurnTheProviderNames(t *testing.T) {
	h, provider := steerHarness(t)
	provider.mu.Lock()
	provider.landed = "provider-turn-1"
	provider.mu.Unlock()

	result, err := h.svc.Steer(context.Background(), testSession,
		ports.ChatUserMessage{Text: "guidance"})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if result.ProviderTurnID != "provider-turn-1" {
		t.Errorf("reported turn = %q, want the one the provider named", result.ProviderTurnID)
	}
}

// Promoting a selected queued turn must use AO's durable content, attach it to
// the running provider turn, and remove only that source turn from the visible
// queue. If this regresses to queue-head-only behavior, the second message below
// is never the one the provider receives.
func TestPromoteSelectedQueuedTurnIntoTheRunningTurn(t *testing.T) {
	h, provider := steerHarness(t)
	ctx := context.Background()

	first, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "first queued", ClientMessageID: "queued-1", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("queue first: %v", err)
	}
	selected, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "second queued", ClientMessageID: "queued-2", Origin: domain.MessageOriginHuman,
		Content: []ports.ChatContent{{Type: "image", Data: "aGVsbG8=", MIMEType: "image/png"}},
	})
	if err != nil {
		t.Fatalf("queue selected: %v", err)
	}

	result, err := h.svc.PromoteQueuedTurn(ctx, testSession, selected.ID)
	if err != nil {
		t.Fatalf("PromoteQueuedTurn: %v", err)
	}
	if result.SourceTurnID != selected.ID || result.ProviderTurnID != "provider-turn-1" || result.ActivityID == "" {
		t.Fatalf("promotion result = %+v", result)
	}
	calls := provider.steers()
	if len(calls) != 1 {
		t.Fatalf("provider steers = %+v, want one", calls)
	}
	if calls[0].msg.Text != "second queued" || calls[0].msg.ClientMessageID != "queued-2" {
		t.Fatalf("provider message = %+v, want selected durable message", calls[0].msg)
	}
	if len(calls[0].msg.Content) != 1 || calls[0].msg.Content[0].MIMEType != "image/png" {
		t.Fatalf("provider content = %+v, want stored image", calls[0].msg.Content)
	}

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(steerMarkers(s)) == 1
	})
	for _, turn := range snapshot.Turns {
		if turn.ID == selected.ID {
			t.Fatalf("promoted source turn remains visible: %+v", turn)
		}
	}
	next, err := h.st.NextQueuedTurn(ctx, h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("remaining queue: %v", err)
	}
	if next.TurnID != first.ID {
		t.Fatalf("remaining queue head = %q, want %q", next.TurnID, first.ID)
	}
}

// A project rebind is an ownership transfer, so it cannot commit while the old
// controller has a reserved queue item crossing the provider boundary. The
// promotion must either finish under owner A or observe owner B before provider
// contact; completing the rebind between provider admission and AO's durable
// outcome would make the timeline disagree with which agent received the work.
func TestPromoteQueuedTurnFencesProjectRebindThroughProviderOutcome(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	provider := newBlockingSteerRecorder()

	var nextID atomic.Int32
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: provider}},
		Log:     slog.New(slog.DiscardHandler),
		NewID: func() string {
			return fmt.Sprintf("promotion-rebind-%d", nextID.Add(1))
		},
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
	// Release provider admission before Stop waits for the controller command lock.
	// Cleanups run in reverse registration order.
	t.Cleanup(provider.releaseSteer)
	controller, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Kind: domain.KindOrchestrator,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start project controller: %v", err)
	}

	if _, err := svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "running", ClientMessageID: "promotion-rebind-running",
		Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send running turn: %v", err)
	}
	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1",
	})
	queued, err := svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "promote before replacement", ClientMessageID: "promotion-rebind-queued",
		Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send queued turn: %v", err)
	}

	promotionDone := make(chan struct {
		result chatsvc.PromoteQueuedTurnResult
		err    error
	}, 1)
	go func() {
		result, promoteErr := svc.PromoteQueuedTurn(ctx, testSession, queued.ID)
		promotionDone <- struct {
			result chatsvc.PromoteQueuedTurnResult
			err    error
		}{result: result, err: promoteErr}
	}()
	select {
	case <-provider.started:
	case <-time.After(4 * time.Second):
		t.Fatal("queued promotion did not reach provider admission")
	}

	replacement, err := st.CreateSession(ctx, domain.SessionRecord{
		ProjectID: testProject, Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeChat, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create replacement orchestrator: %v", err)
	}
	rebindDone := make(chan error, 1)
	go func() {
		_, rebindErr := st.CreateConversation(
			ctx, "unused-promotion-replacement", domain.ConversationScopeProject,
			testProject, replacement.ID, time.Now().UTC(),
		)
		rebindDone <- rebindErr
	}()
	select {
	case rebindErr := <-rebindDone:
		t.Fatalf("project rebind completed before provider outcome: %v", rebindErr)
	case <-time.After(150 * time.Millisecond):
	}

	provider.releaseSteer()
	var promotion struct {
		result chatsvc.PromoteQueuedTurnResult
		err    error
	}
	select {
	case promotion = <-promotionDone:
	case <-time.After(4 * time.Second):
		t.Fatal("queued promotion did not complete after provider release")
	}
	if promotion.err != nil {
		t.Fatalf("PromoteQueuedTurn: %v", promotion.err)
	}
	if promotion.result.SourceTurnID != queued.ID || promotion.result.ActivityID == "" {
		t.Fatalf("promotion result = %+v, want durable source and activity", promotion.result)
	}
	select {
	case rebindErr := <-rebindDone:
		if rebindErr != nil {
			t.Fatalf("rebind after promotion outcome: %v", rebindErr)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("project rebind remained blocked after promotion completed")
	}

	if calls := provider.steers(); len(calls) != 1 || calls[0].msg.Text != "promote before replacement" {
		t.Fatalf("provider promotions = %+v, want one admitted under the original owner", calls)
	}
	snapshot, err := st.LoadConversationSnapshot(ctx, controller.ConversationID())
	if err != nil {
		t.Fatalf("LoadConversationSnapshot: %v", err)
	}
	if got := len(steerMarkers(snapshot)); got != 1 {
		t.Fatalf("durable promotion markers = %d, want one before ownership transfer", got)
	}
}

func TestRetiredProjectControllerCannotPromoteReplacementQueue(t *testing.T) {
	provider := newSteerRecorder()
	h := newProjectHarnessWithConversation(t, provider)
	ctx := context.Background()
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "old owner running", ClientMessageID: "old-owner-running",
		Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("start old owner turn: %v", err)
	}
	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1",
	})
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		return turnStateByText(t, snapshot)["old owner running"] == domain.TurnStateRunning
	})

	const replacementTurnID = "replacement-queued-promotion"
	rebindProjectConversationAndQueue(t, h, replacementTurnID, "replacement-owned work")
	_, err := h.svc.PromoteQueuedTurn(ctx, testSession, replacementTurnID)
	if !errors.Is(err, chatsvc.ErrControllerHandoff) {
		t.Fatalf("retired PromoteQueuedTurn = %v, want ErrControllerHandoff", err)
	}
	if calls := provider.steers(); len(calls) != 0 {
		t.Fatalf("retired provider received replacement promotion: %+v", calls)
	}
	turn, err := h.st.TurnByID(ctx, replacementTurnID)
	if err != nil {
		t.Fatalf("load replacement turn: %v", err)
	}
	if turn.State != domain.TurnStateQueued {
		t.Fatalf("replacement turn state = %q, want queued", turn.State)
	}
}

// A provider refusal has not delivered anything, so the exact selected message
// must return to its original queue position instead of being lost or failed.
func TestPromoteQueuedTurnRefusalRestoresItsQueuePosition(t *testing.T) {
	h, provider := steerHarness(t)
	ctx := context.Background()
	queued, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "keep me queued", ClientMessageID: "queued-refused", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	provider.failWith(ports.ErrChatTurnNotSteerable)

	_, err = h.svc.PromoteQueuedTurn(ctx, testSession, queued.ID)
	if !errors.Is(err, chatsvc.ErrTurnNotSteerable) {
		t.Fatalf("promotion error = %v, want ErrTurnNotSteerable", err)
	}
	next, err := h.st.NextQueuedTurn(ctx, h.ctrl.ConversationID())
	if err != nil || next.TurnID != queued.ID {
		t.Fatalf("restored queue head = %+v, %v; want %s", next, err, queued.ID)
	}
}

// Only human-originated queue items are eligible for mid-turn guidance. The
// service must enforce that boundary even when a caller bypasses the frontend,
// without consuming or reordering the automation item.
func TestPromoteQueuedTurnRejectsNonHumanSourceWithoutContactingProvider(t *testing.T) {
	h, provider := steerHarness(t)
	ctx := context.Background()
	queued, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "automation follow-up", ClientMessageID: "queued-automation", Origin: domain.MessageOriginAutomation,
	})
	if err != nil {
		t.Fatalf("queue automation turn: %v", err)
	}

	_, err = h.svc.PromoteQueuedTurn(ctx, testSession, queued.ID)
	if !errors.Is(err, chatsvc.ErrTurnNotQueued) {
		t.Fatalf("promotion error = %v, want ErrTurnNotQueued", err)
	}
	if calls := provider.steers(); len(calls) != 0 {
		t.Fatalf("provider received %d steer attempts, want none", len(calls))
	}
	next, err := h.st.NextQueuedTurn(ctx, h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("load queue after rejection: %v", err)
	}
	if next.TurnID != queued.ID || next.Origin != domain.MessageOriginAutomation {
		t.Fatalf("queue head after rejection = %+v, want unchanged automation turn %s", next, queued.ID)
	}
}

// A transport failure after the request leaves delivery unknowable. Returning the
// source to the queue would let drain send guidance the provider may already have
// accepted, so it must settle failed and require an explicit user decision.
func TestPromoteQueuedTurnAmbiguousProviderFailureSettlesUncertainWithoutRedelivery(t *testing.T) {
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	t.Cleanup(cancelRequest)
	provider := &cancelAfterSteerRecorder{steerRecorder: newSteerRecorder(), cancel: cancelRequest}
	h := newHarnessWithConversation(t, provider)
	storeCtx := context.Background()
	if _, err := h.svc.Send(storeCtx, testSession, ports.ChatUserMessage{
		Text: "do the long thing", ClientMessageID: "turn-1", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("start running turn: %v", err)
	}
	provider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	queued, err := h.svc.Send(storeCtx, testSession, ports.ChatUserMessage{
		Text: "deliver me at most once", ClientMessageID: "queued-uncertain", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	transportErr := errors.New("connection lost after request write")
	provider.failWith(transportErr)

	_, err = h.svc.PromoteQueuedTurn(requestCtx, testSession, queued.ID)
	if !errors.Is(err, chatsvc.ErrPromotionUncertain) {
		t.Fatalf("promotion error = %v, want ErrPromotionUncertain", err)
	}
	if !errors.Is(err, transportErr) {
		t.Fatalf("promotion error = %v, want transport cause", err)
	}

	snapshot, err := h.st.LoadConversationSnapshot(storeCtx, h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	var source *domain.ConversationTurn
	for index := range snapshot.Turns {
		if snapshot.Turns[index].ID == queued.ID {
			source = &snapshot.Turns[index]
			break
		}
	}
	if source == nil {
		t.Fatalf("uncertain source turn %s is not visible", queued.ID)
	}
	if source.State != domain.TurnStateFailed || source.ErrorMessage != chatsvc.ErrPromotionUncertain.Error() {
		t.Fatalf("uncertain source = %+v, want failed with promotion-uncertain error", *source)
	}
	if _, err := h.st.NextQueuedTurn(storeCtx, h.ctrl.ConversationID()); !errors.Is(err, domain.ErrNoQueuedTurn) {
		t.Fatalf("uncertain source remained drainable: %v", err)
	}

	_, retryErr := h.svc.PromoteQueuedTurn(storeCtx, testSession, queued.ID)
	if !errors.Is(retryErr, chatsvc.ErrTurnNotQueued) {
		t.Fatalf("retry error = %v, want ErrTurnNotQueued", retryErr)
	}
	if calls := provider.steers(); len(calls) != 1 {
		t.Fatalf("provider received %d steer attempts, want one", len(calls))
	}
}

func TestInterruptFencesConfirmedQueueFromConcurrentPromotion(t *testing.T) {
	provider := newBlockingInterruptSteerRecorder(nil)
	t.Cleanup(func() {
		select {
		case <-provider.releaseInterrupt:
		default:
			close(provider.releaseInterrupt)
		}
	})
	h := newHarnessWithConversation(t, provider)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "running", ClientMessageID: "running", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send running: %v", err)
	}
	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1",
	})
	queued, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "confirmed for Stop", ClientMessageID: "queued", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send queued: %v", err)
	}

	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- h.svc.Interrupt(ctx, testSession, []string{queued.ID})
	}()
	select {
	case <-provider.interruptStarted:
	case <-time.After(4 * time.Second):
		t.Fatal("Stop did not reach provider")
	}

	_, promoteErr := h.svc.PromoteQueuedTurn(ctx, testSession, queued.ID)
	if !errors.Is(promoteErr, chatsvc.ErrTurnNotQueued) {
		t.Fatalf("concurrent promotion error = %v, want ErrTurnNotQueued", promoteErr)
	}
	if calls := provider.steers(); len(calls) != 0 {
		t.Fatalf("provider received confirmed Stop work through promotion: %+v", calls)
	}

	close(provider.releaseInterrupt)
	if err := <-interruptDone; err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["confirmed for Stop"].Terminal()
	})
	if got := turnStateByText(t, snapshot)["confirmed for Stop"]; got != domain.TurnStateInterrupted {
		t.Fatalf("confirmed queue state = %q, want interrupted", got)
	}
}

func TestPendingInterruptReservationBlocksConflictingProviderCommands(t *testing.T) {
	provider := newBlockingInterruptSteerRecorder(nil)
	caps := productionCaps()
	caps[ports.ChatCapabilityCompaction] = true
	provider.setCapabilities(caps)
	h := newHarnessWithConversation(t, provider)
	t.Cleanup(func() {
		select {
		case <-provider.releaseInterrupt:
		default:
			close(provider.releaseInterrupt)
		}
	})
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "running", ClientMessageID: "running", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send running: %v", err)
	}
	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1",
	})
	queued, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "confirmed for Stop", ClientMessageID: "queued", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send queued: %v", err)
	}

	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- h.svc.Interrupt(ctx, testSession, []string{queued.ID})
	}()
	select {
	case <-provider.interruptStarted:
	case <-time.After(4 * time.Second):
		t.Fatal("Stop did not reach provider")
	}

	if _, err := h.svc.Steer(ctx, testSession, ports.ChatUserMessage{
		Text: "change course", Origin: domain.MessageOriginHuman,
	}); !errors.Is(err, chatsvc.ErrInterruptPending) {
		t.Fatalf("Steer during Stop = %v, want ErrInterruptPending", err)
	}
	if _, err := h.svc.RetryTurn(ctx, testSession, "missing-retry"); !errors.Is(err, chatsvc.ErrInterruptPending) {
		t.Fatalf("Retry during Stop = %v, want ErrInterruptPending", err)
	}
	if _, err := h.svc.Rollback(ctx, testSession, "missing-rollback"); !errors.Is(err, chatsvc.ErrInterruptPending) {
		t.Fatalf("Rollback during Stop = %v, want ErrInterruptPending", err)
	}
	if _, err := h.svc.Compact(ctx, testSession); !errors.Is(err, chatsvc.ErrInterruptPending) {
		t.Fatalf("Compact during Stop = %v, want ErrInterruptPending", err)
	}
	if _, err := h.svc.ReloadMCPServers(ctx, testSession); !errors.Is(err, chatsvc.ErrInterruptPending) {
		t.Fatalf("MCP reload during Stop = %v, want ErrInterruptPending", err)
	}
	if _, err := h.svc.SetConfigOption(
		ctx, testSession, "model", ports.ChatConfigOptionValue{Select: "other"},
	); !errors.Is(err, chatsvc.ErrInterruptPending) {
		t.Fatalf("provider config during Stop = %v, want ErrInterruptPending", err)
	}
	if _, err := h.svc.SetTitle(ctx, testSession, "A safer title"); !errors.Is(err, chatsvc.ErrInterruptPending) {
		t.Fatalf("title mutation during Stop = %v, want ErrInterruptPending", err)
	}
	if err := h.svc.Resolve(
		ctx, testSession, "pending-approval", ports.ChatDecision{ID: "accept"},
	); !errors.Is(err, chatsvc.ErrInterruptPending) {
		t.Fatalf("approval during Stop = %v, want ErrInterruptPending", err)
	}
	if err := h.svc.ResolveInput(ctx, testSession, "pending-input", ports.ChatInputResponse{
		Action: ports.ChatInputActionAccept,
	}); !errors.Is(err, chatsvc.ErrInterruptPending) {
		t.Fatalf("input response during Stop = %v, want ErrInterruptPending", err)
	}
	if err := h.svc.PrepareChatHandoff(
		ctx, testSession, domain.SessionInterfaceTransitionInterrupt,
	); !errors.Is(err, chatsvc.ErrInterruptPending) {
		t.Fatalf("interface handoff during Stop = %v, want ErrInterruptPending", err)
	}

	if calls := provider.steers(); len(calls) != 0 {
		t.Fatalf("provider received steer during Stop: %+v", calls)
	}
	if calls := provider.providerCommandSnapshot(); len(calls) != 0 {
		t.Fatalf("provider received conflicting commands during Stop: %v", calls)
	}
	if got := provider.sentTexts(); len(got) != 1 {
		t.Fatalf("provider received %v while Stop outcome was pending", got)
	}

	close(provider.releaseInterrupt)
	if err := <-interruptDone; err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
}

func TestArmedInterfaceHandoffRejectsStopAndTitleBeforeProviderBoundary(t *testing.T) {
	provider := newBlockingInterruptSteerRecorder(nil)
	h := newHarnessWithConversation(t, provider)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "running", ClientMessageID: "running", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send running: %v", err)
	}
	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1",
	})
	queued, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "queued", ClientMessageID: "queued", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send queued: %v", err)
	}
	if err := h.svc.ArmChatHandoff(
		ctx, testSession, domain.SessionInterfaceTransitionDrain,
	); err != nil {
		t.Fatalf("ArmChatHandoff: %v", err)
	}
	if _, err := h.svc.SetTitle(ctx, testSession, "must not cross handoff"); !errors.Is(err, chatsvc.ErrControllerHandoff) {
		t.Fatalf("SetTitle during armed handoff = %v, want ErrControllerHandoff", err)
	}

	stopCtx, stopCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stopCancel()
	if err := h.svc.Interrupt(stopCtx, testSession, []string{queued.ID}); !errors.Is(err, chatsvc.ErrControllerHandoff) {
		t.Fatalf("Stop during armed handoff = %v, want ErrControllerHandoff", err)
	}
	select {
	case <-provider.interruptStarted:
		t.Fatal("Stop crossed provider boundary during an armed interface handoff")
	default:
	}
	h.svc.AbortChatHandoff(testSession)
}

func TestInterruptCompletionBeforeSuccessCancelsExactScopeAndReleasesPostStopWork(t *testing.T) {
	provider := newBlockingInterruptSteerRecorder(nil)
	h := newHarnessWithConversation(t, provider)
	t.Cleanup(func() {
		select {
		case <-provider.releaseInterrupt:
		default:
			close(provider.releaseInterrupt)
		}
	})
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "running", ClientMessageID: "running", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send running: %v", err)
	}
	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1",
	})
	confirmed, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "confirmed for Stop", ClientMessageID: "confirmed", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send confirmed queue: %v", err)
	}

	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- h.svc.Interrupt(ctx, testSession, []string{confirmed.ID})
	}()
	select {
	case <-provider.interruptStarted:
	case <-time.After(4 * time.Second):
		t.Fatal("Stop did not reach provider")
	}

	postStop, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "after Stop", ClientMessageID: "after", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send after Stop: %v", err)
	}
	if postStop.State != domain.TurnStateQueued {
		t.Fatalf("post-Stop send state = %q, want queued behind pending outcome", postStop.State)
	}
	if _, err := h.svc.PromoteQueuedTurn(ctx, testSession, postStop.ID); !errors.Is(err, chatsvc.ErrTurnNotQueued) {
		t.Fatalf("promote post-Stop work error = %v, want ErrTurnNotQueued", err)
	}
	if err := h.svc.CancelQueuedTurn(ctx, testSession, confirmed.ID); !errors.Is(err, chatsvc.ErrInterruptPending) {
		t.Fatalf("cancel confirmed work error = %v, want ErrInterruptPending", err)
	}
	if err := h.svc.Interrupt(ctx, testSession, []string{confirmed.ID, postStop.ID}); !errors.Is(err, chatsvc.ErrQueueScopeChanged) {
		t.Fatalf("second Stop error = %v, want ErrQueueScopeChanged", err)
	}

	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateCompleted,
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		states := turnStateByText(t, s)
		return states["running"] == domain.TurnStateCompleted &&
			states["confirmed for Stop"] == domain.TurnStateQueued &&
			states["after Stop"] == domain.TurnStateQueued
	})
	if got := provider.sentTexts(); len(got) != 1 {
		t.Fatalf("provider received %v while Stop outcome was pending", got)
	}

	close(provider.releaseInterrupt)
	if err := <-interruptDone; err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		states := turnStateByText(t, s)
		return states["confirmed for Stop"] == domain.TurnStateInterrupted &&
			states["after Stop"] == domain.TurnStateRunning
	})
	states := turnStateByText(t, snapshot)
	if states["confirmed for Stop"] != domain.TurnStateInterrupted {
		t.Fatalf("confirmed queue state = %q, want interrupted", states["confirmed for Stop"])
	}
	if got := provider.sentTexts(); len(got) != 2 || got[1] != "after Stop" {
		t.Fatalf("provider received %v, want only post-Stop work after success", got)
	}
}

func TestInterruptFailureReleasesFenceAndResumesOriginalQueue(t *testing.T) {
	provider := newBlockingInterruptSteerRecorder(errors.New("provider unavailable"))
	t.Cleanup(func() {
		select {
		case <-provider.releaseInterrupt:
		default:
			close(provider.releaseInterrupt)
		}
	})
	h := newHarnessWithConversation(t, provider)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "running", ClientMessageID: "running", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send running: %v", err)
	}
	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1",
	})
	queued, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "original queue", ClientMessageID: "queued", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send queued: %v", err)
	}

	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- h.svc.Interrupt(ctx, testSession, []string{queued.ID})
	}()
	select {
	case <-provider.interruptStarted:
	case <-time.After(4 * time.Second):
		t.Fatal("Stop did not reach provider")
	}
	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateCompleted,
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		states := turnStateByText(t, s)
		return states["running"] == domain.TurnStateCompleted &&
			states["original queue"] == domain.TurnStateQueued
	})
	if got := provider.sentTexts(); len(got) != 1 {
		t.Fatalf("provider received %v before failed Stop released its fence", got)
	}

	close(provider.releaseInterrupt)
	if err := <-interruptDone; err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("Interrupt error = %v, want provider failure", err)
	}
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["original queue"] == domain.TurnStateRunning
	})
	if got := provider.sentTexts(); len(got) != 2 || got[1] != "original queue" {
		t.Fatalf("provider received %v, want original queue resumed after failure", got)
	}
}

// A cancelled HTTP request does not make a provider response ambiguous when the
// response already crossed the RPC correlation boundary. A definitive rejection
// releases Stop's queue fence: the confirmed prompt stays queued, then resumes
// normally when the active turn completes.
func TestInterruptProviderRejectionAfterCancellationPreservesAndReleasesQueue(t *testing.T) {
	providerFailure := errors.New("provider rejected interrupt")
	ctx := context.Background()
	interruptCtx, cancel := context.WithCancel(ctx)
	provider := &cancelAfterInterruptRecorder{
		steerRecorder: newSteerRecorder(), cancel: cancel, err: providerFailure,
	}
	h := newHarnessWithConversation(t, provider)

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "running", ClientMessageID: "provider-rejection-running", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send running: %v", err)
	}
	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1",
	})
	queued, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "preserved queue", ClientMessageID: "provider-rejection-queued", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send queued: %v", err)
	}

	interruptErr := h.svc.Interrupt(interruptCtx, testSession, []string{queued.ID})
	if !errors.Is(interruptErr, providerFailure) {
		t.Fatalf("Interrupt error = %v, want definitive provider rejection", interruptErr)
	}
	if errors.Is(interruptErr, ports.ErrChatInterruptDeliveryUncertain) {
		t.Fatalf("Interrupt error = %v, did not want delivery uncertainty", interruptErr)
	}
	snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if got := turnStateByText(t, snapshot)["preserved queue"]; got != domain.TurnStateQueued {
		t.Fatalf("confirmed queue = %q, want preserved queued state", got)
	}

	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateCompleted,
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["preserved queue"] == domain.TurnStateRunning
	})
	if got := provider.sentTexts(); len(got) != 2 || got[1] != "preserved queue" {
		t.Fatalf("provider received %v, want preserved queue resumed after rejection", got)
	}
}

func TestInterruptCancellationAfterProviderDispatchNeverDeliversConfirmedQueue(t *testing.T) {
	provider := newBlockingInterruptSteerRecorder(nil)
	t.Cleanup(func() {
		select {
		case <-provider.releaseInterrupt:
		default:
			close(provider.releaseInterrupt)
		}
	})
	h := newHarnessWithConversation(t, provider)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "running", ClientMessageID: "running", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send running: %v", err)
	}
	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1",
	})
	confirmed, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "confirmed for Stop", ClientMessageID: "confirmed", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send confirmed queue: %v", err)
	}

	interruptCtx, cancel := context.WithCancel(ctx)
	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- h.svc.Interrupt(interruptCtx, testSession, []string{confirmed.ID})
	}()
	select {
	case <-provider.interruptStarted:
	case <-time.After(4 * time.Second):
		t.Fatal("Stop did not cross the provider dispatch boundary")
	}
	cancel()
	interruptErr := <-interruptDone
	if !errors.Is(interruptErr, context.Canceled) {
		t.Fatalf("Interrupt error = %v, want context.Canceled", interruptErr)
	}
	if !errors.Is(interruptErr, ports.ErrChatInterruptDeliveryUncertain) {
		t.Fatalf("Interrupt error = %v, want delivery-uncertain classification", interruptErr)
	}

	// The provider may have accepted the dispatched Stop even though its response
	// lost the race with cancellation. Its eventual completion must not release the
	// exact queue the user confirmed into a new provider turn.
	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateCompleted,
	})
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		states := turnStateByText(t, s)
		return states["running"] == domain.TurnStateCompleted &&
			states["confirmed for Stop"] != domain.TurnStateQueued
	})
	states := turnStateByText(t, snapshot)
	if states["confirmed for Stop"] != domain.TurnStateInterrupted {
		t.Fatalf("confirmed Stop queue = %q, want interrupted after uncertain delivery", states["confirmed for Stop"])
	}
	if got := provider.sentTexts(); len(got) != 1 {
		t.Fatalf("provider received %v, want zero confirmed-queue deliveries", got)
	}

	after, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "after uncertain Stop", ClientMessageID: "after", Origin: domain.MessageOriginHuman,
	})
	if err != nil || after.State != domain.TurnStateRunning {
		t.Fatalf("Send after durable reconciliation = %+v, %v; want running", after, err)
	}
	if got := provider.sentTexts(); len(got) != 2 || got[1] != "after uncertain Stop" {
		t.Fatalf("provider received %v, want only new post-reconciliation work", got)
	}
}

func TestRecoverImageSteerWithoutControllerNeverRedispatches(t *testing.T) {
	for _, tc := range []struct {
		name          string
		providerError error
		wantError     error
	}{
		{name: "accepted"},
		{name: "rejected", providerError: ports.ErrChatNoSteerableTurn, wantError: chatsvc.ErrNoActiveTurn},
		{name: "uncertain", providerError: errors.New("response lost"), wantError: chatsvc.ErrSteerDeliveryUncertain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, provider := steerHarness(t)
			provider.failWith(tc.providerError)
			ctx := context.Background()
			original, err := h.svc.Steer(ctx, testSession, ports.ChatUserMessage{
				Text: "use this image", ClientMessageID: "image-steer",
				Content: []ports.ChatContent{{Type: "image", MIMEType: "image/png", Data: "aW1hZ2U="}},
			})
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("steer error = %v, want %v", err, tc.wantError)
			}
			if err := h.svc.Stop(ctx, testSession); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				recovered, err := h.svc.RecoverSteer(ctx, testSession, "image-steer")
				if !errors.Is(err, tc.wantError) || recovered != original {
					t.Fatalf("recovery = %+v, %v; want %+v, %v", recovered, err, original, tc.wantError)
				}
			}
			for _, id := range []string{"", "never-reserved"} {
				if _, err := h.svc.RecoverSteer(ctx, testSession, id); !errors.Is(err, chatsvc.ErrSteerDeliveryUncertain) {
					t.Fatalf("missing receipt: %v", err)
				}
			}
			if len(provider.steers()) != 1 {
				t.Fatal("recovery redispatched guidance")
			}
		})
	}
}
