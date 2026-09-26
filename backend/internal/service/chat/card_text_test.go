package chat

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type cardTextTestRegistry struct{ driver ports.ChatDriver }

func (r cardTextTestRegistry) Driver(domain.AgentHarness) (ports.ChatDriver, error) {
	return r.driver, nil
}
func (r cardTextTestRegistry) SupportsChat(domain.AgentHarness) bool { return true }

type cardTextTestDriver struct {
	start func(ports.ChatStartConfig) (ports.ChatConversation, error)
}

func (d cardTextTestDriver) Harness() domain.AgentHarness { return domain.HarnessCodex }
func (d cardTextTestDriver) Probe(context.Context) (ports.ChatCapabilities, error) {
	return ports.ChatCapabilities{}, nil
}
func (d cardTextTestDriver) Start(_ context.Context, cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
	return d.start(cfg)
}
func (d cardTextTestDriver) Resume(context.Context, ports.ChatResumeConfig) (ports.ChatConversation, error) {
	return nil, nil
}

type cardTextTestConversation struct {
	events     chan ports.ChatEvent
	terminated atomic.Bool
}

func newCardTextTestConversation() *cardTextTestConversation {
	return &cardTextTestConversation{events: make(chan ports.ChatEvent, 2)}
}
func (c *cardTextTestConversation) ProviderConversationID() string { return "card-text-thread" }
func (c *cardTextTestConversation) Capabilities() ports.ChatCapabilities {
	return ports.ChatCapabilities{}
}
func (c *cardTextTestConversation) SendTurn(_ context.Context, _ ports.ChatUserMessage) (ports.ChatTurnRef, error) {
	turn := ports.ChatTurnRef{ProviderTurnID: "turn-1"}
	c.events <- ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: turn.ProviderTurnID, Text: `{"summary":"Inspecting the navigation flow"}`}
	return turn, nil
}
func (c *cardTextTestConversation) Interrupt(context.Context, string) error { return nil }
func (c *cardTextTestConversation) ResolveRequest(context.Context, string, ports.ChatDecision) error {
	return nil
}
func (c *cardTextTestConversation) Events() <-chan ports.ChatEvent { return c.events }
func (c *cardTextTestConversation) Close() error                   { return nil }
func (c *cardTextTestConversation) Terminate() error {
	c.terminated.Store(true)
	return nil
}

func TestCardTextRequestsUseIsolatedProviderHosts(t *testing.T) {
	var mu sync.Mutex
	var starts []ports.ChatStartConfig
	var conversations []*cardTextTestConversation
	driver := cardTextTestDriver{start: func(cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
		conv := newCardTextTestConversation()
		mu.Lock()
		starts = append(starts, cfg)
		conversations = append(conversations, conv)
		mu.Unlock()
		return conv, nil
	}}
	service := New(Options{Drivers: cardTextTestRegistry{driver: driver}})
	id := domain.SessionID("worker-1")
	service.startConfigs[domain.SessionConversationOwner(id)] = StartConfig{
		SessionID: id, Harness: domain.HarnessCodex, DataDir: t.TempDir(), WorkspacePath: t.TempDir(),
	}

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := service.GenerateCardSummary(context.Background(), id, "Audit dashboard navigation", "Inspecting dashboard routes"); err != nil {
				t.Errorf("GenerateCardSummary: %v", err)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 2 {
		t.Fatalf("starts = %d, want 2", len(starts))
	}
	if starts[0].SessionID == starts[1].SessionID || starts[0].ProviderScopeID == starts[1].ProviderScopeID {
		t.Fatalf("card editors shared ownership: %#v %#v", starts[0], starts[1])
	}
	for i, conv := range conversations {
		if !conv.terminated.Load() {
			t.Errorf("conversation %d was detached instead of terminated", i)
		}
	}
}

func TestSafeCardSummaryRejectsCombinedPromptExamples(t *testing.T) {
	bad := "Inspecting the authentication flow, editing the topbar component, validating the wishlist implementation"
	if safeCardSummary(bad) {
		t.Fatalf("accepted copied multi-action summary %q", bad)
	}
	if !safeCardSummary("Inspecting authentication callback behavior") {
		t.Fatal("rejected a clean single-action summary")
	}
	for _, generic := range []string{"Working on the task", "Implementing the requested changes", "Investigating an implementation issue"} {
		if safeCardSummary(generic) {
			t.Errorf("accepted generic summary %q", generic)
		}
	}
	for _, unsuitable := range []string{
		"Reconsidering to install dependencies for type-check and lint in a read-only worktree",
		"Planning the next implementation steps",
	} {
		if safeCardSummary(unsuitable) {
			t.Errorf("accepted verbose deliberation %q", unsuitable)
		}
	}
}

func TestCardRefreshUsesFixedWindowsAndBatchesEvidence(t *testing.T) {
	oldFirst, oldRefresh := cardFirstRefreshDelay, cardRefreshDelay
	cardFirstRefreshDelay, cardRefreshDelay = 20*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { cardFirstRefreshDelay, cardRefreshDelay = oldFirst, oldRefresh })

	result := make(chan string, 1)
	service := New(Options{OnAssistantMessage: func(_ context.Context, _ domain.SessionID, evidence string) {
		result <- evidence
	}})
	id := domain.SessionID("worker-1")
	service.scheduleCardRefresh(context.Background(), id, "Exploring dashboard routes")
	time.Sleep(10 * time.Millisecond)
	service.scheduleCardRefresh(context.Background(), id, "Read sidebar component")

	select {
	case evidence := <-result:
		if !strings.Contains(evidence, "Exploring dashboard routes") || !strings.Contains(evidence, "Read sidebar component") {
			t.Fatalf("batch = %q", evidence)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("continuous activity reset the fixed summary window")
	}
}

func TestCardRefreshKeepsLatestEvidenceFreshDuringQuietWork(t *testing.T) {
	oldFirst, oldRefresh := cardFirstRefreshDelay, cardRefreshDelay
	oldHeartbeat, oldWindow := cardHeartbeatRefreshDelay, cardSummaryFreshWindow
	cardFirstRefreshDelay, cardRefreshDelay = 10*time.Millisecond, 10*time.Millisecond
	cardHeartbeatRefreshDelay, cardSummaryFreshWindow = 20*time.Millisecond, 70*time.Millisecond
	t.Cleanup(func() {
		cardFirstRefreshDelay, cardRefreshDelay = oldFirst, oldRefresh
		cardHeartbeatRefreshDelay, cardSummaryFreshWindow = oldHeartbeat, oldWindow
	})

	result := make(chan string, 4)
	service := New(Options{
		KeepCardSummaryFresh: true,
		OnAssistantMessage: func(_ context.Context, _ domain.SessionID, evidence string) {
			result <- evidence
		},
	})
	service.scheduleCardRefresh(context.Background(), domain.SessionID("worker-1"), "Running the verification suite")

	for range 2 {
		select {
		case evidence := <-result:
			if evidence != "Running the verification suite" {
				t.Fatalf("heartbeat evidence = %q", evidence)
			}
		case <-time.After(150 * time.Millisecond):
			t.Fatal("quiet active work did not receive another card refresh")
		}
	}
}
