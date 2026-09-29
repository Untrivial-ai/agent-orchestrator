package chat

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

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
	c.events <- ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: turn.ProviderTurnID, Text: `{"title":"Navigation Flow Audit"}`}
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
	base := ports.ChatStartConfig{SessionID: domain.SessionID("worker-1"), DataDir: t.TempDir(), WorkspacePath: t.TempDir()}

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := service.GenerateCardTitleWithConfig(context.Background(), domain.HarnessCodex, base, "Audit dashboard navigation"); err != nil {
				t.Errorf("GenerateCardTitleWithConfig: %v", err)
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

func TestCardRefreshBoundsToolOutputEvidence(t *testing.T) {
	service := New(Options{OnAssistantMessage: func(context.Context, domain.SessionID, string) {}})
	id := domain.SessionID("worker-1")
	for range 10 {
		service.scheduleCardRefresh(context.Background(), id, strings.Repeat("é", 5000))
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	defer service.assistantTimers[id].Stop()
	batch := strings.Join(service.assistantEvidence[id], "\n")
	if len(batch) > maxCardEvidenceBatchBytes+len(service.assistantEvidence[id])-1 || !utf8.ValidString(batch) {
		t.Fatalf("invalid or oversized card evidence: %d bytes", len(batch))
	}
}

func TestCardRefreshDoesNotRepeatQuietEvidence(t *testing.T) {
	oldFirst, oldRefresh := cardFirstRefreshDelay, cardRefreshDelay
	cardFirstRefreshDelay, cardRefreshDelay = 10*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() {
		cardFirstRefreshDelay, cardRefreshDelay = oldFirst, oldRefresh
	})

	result := make(chan string, 2)
	service := New(Options{
		OnAssistantMessage: func(_ context.Context, _ domain.SessionID, evidence string) {
			result <- evidence
		},
	})
	service.scheduleCardRefresh(context.Background(), domain.SessionID("worker-1"), "Running the verification suite")

	select {
	case evidence := <-result:
		if evidence != "Running the verification suite" {
			t.Fatalf("card evidence = %q", evidence)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("initial activity did not refresh the card")
	}
	select {
	case evidence := <-result:
		t.Fatalf("quiet worker produced duplicate card evidence %q", evidence)
	case <-time.After(80 * time.Millisecond):
	}
}
