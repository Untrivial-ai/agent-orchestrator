package sessionimportsvc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimport"
)

// fakeAgent stands in for a user's CLI. It records the prompt so a test can
// assert what left the machine, and can refuse or misbehave on demand.
type fakeAgent struct {
	ports.Agent
	reply    string
	err      error
	auth     ports.AgentAuthStatus
	prompts  []string
	oneShot  bool
	authable bool
}

func (f *fakeAgent) RunOneShot(_ context.Context, _, prompt string) (string, error) {
	f.prompts = append(f.prompts, prompt)
	return f.reply, f.err
}

func (f *fakeAgent) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return f.auth, nil
}

type fakeRegistry struct {
	agents map[domain.AgentHarness]*fakeAgent
}

func (r fakeRegistry) Agent(h domain.AgentHarness) (ports.Agent, bool) {
	agent, ok := r.agents[h]
	if !ok {
		return nil, false
	}
	// A CLI with no print mode must be indistinguishable from one that has it
	// but is not authorized: both leave the conversation ambiguous.
	if !agent.oneShot {
		return struct{ ports.Agent }{}, true
	}
	if !agent.authable {
		return &oneShotOnly{inner: agent}, true
	}
	return agent, true
}

// oneShotOnly exposes RunOneShot without the auth probe, modelling an adapter
// that cannot report authorization. Embedding the interface satisfies
// ports.Agent without implementing the optional auth capability.
type oneShotOnly struct {
	ports.Agent
	inner *fakeAgent
}

func (o *oneShotOnly) RunOneShot(ctx context.Context, dir, prompt string) (string, error) {
	return o.inner.RunOneShot(ctx, dir, prompt)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func ambiguous(id, title, prompt string) sessionimport.ImportableSession {
	return sessionimport.ImportableSession{
		Provider:        domain.HarnessClaudeCode,
		NativeSessionID: id,
		CWD:             "/Users/dev/code",
		Title:           title,
		FirstPrompt:     prompt,
		Meaning:         sessionimport.MeaningAmbiguous,
		LastActivity:    time.Unix(1700000000, 0),
	}
}

func newClassifier(t *testing.T, reg fakeRegistry) *classifier {
	t.Helper()
	dir := t.TempDir()
	return &classifier{agents: reg, cache: newVerdictCache(dir), workDir: dir, logger: quietLogger()}
}

func TestClassifySettlesAmbiguousConversations(t *testing.T) {
	agent := &fakeAgent{
		oneShot: true, authable: true, auth: ports.AgentAuthStatusAuthorized,
		reply: `Here you go: [{"id":"keep","verdict":"meaningful"},{"id":"junk","verdict":"trivial"}]`,
	}
	c := newClassifier(t, fakeRegistry{agents: map[domain.AgentHarness]*fakeAgent{domain.HarnessClaudeCode: agent}})

	batch := []sessionimport.ImportableSession{
		ambiguous("keep", "Rework the cache", "how should we key the cache"),
		ambiguous("junk", "hey", "hey"),
	}

	// The first listing is served immediately, without waiting on a model, so
	// both conversations are still shown.
	if got := c.resolve(batch); len(got) != 2 {
		t.Fatalf("a listing must not wait on classification, got %+v", got)
	}
	c.waitForBackground()

	got := c.resolve(batch)
	if len(got) != 1 || got[0].NativeSessionID != "keep" {
		t.Fatalf("want only the meaningful conversation once judged, got %+v", got)
	}
	if len(agent.prompts) != 1 {
		t.Fatalf("want one batched call for both conversations, got %d", len(agent.prompts))
	}
	// Only a title and a short excerpt may leave the machine.
	if !strings.Contains(agent.prompts[0], "how should we key the cache") {
		t.Error("the prompt should carry the excerpt the verdict is based on")
	}
}

// Every failure mode must leave the conversation visible. Classification
// refines the list; it must never be the reason real work disappears.
func TestClassifyKeepsAmbiguousOnEveryFailure(t *testing.T) {
	cases := []struct {
		name  string
		agent *fakeAgent
	}{
		{"agent refuses", &fakeAgent{oneShot: true, authable: true, auth: ports.AgentAuthStatusAuthorized, err: errors.New("boom")}},
		{"not logged in", &fakeAgent{oneShot: true, authable: true, auth: ports.AgentAuthStatusUnauthorized, reply: `[{"id":"x","verdict":"trivial"}]`}},
		{"no print mode", &fakeAgent{oneShot: false}},
		{"reply is prose", &fakeAgent{oneShot: true, authable: true, auth: ports.AgentAuthStatusAuthorized, reply: "I could not decide."}},
		{"reply is empty", &fakeAgent{oneShot: true, authable: true, auth: ports.AgentAuthStatusAuthorized, reply: ""}},
		{"verdict is unrecognized", &fakeAgent{oneShot: true, authable: true, auth: ports.AgentAuthStatusAuthorized, reply: `[{"id":"x","verdict":"maybe"}]`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClassifier(t, fakeRegistry{agents: map[domain.AgentHarness]*fakeAgent{domain.HarnessClaudeCode: tc.agent}})
			batch := []sessionimport.ImportableSession{ambiguous("x", "Something", "something real")}
			c.resolve(batch)
			c.waitForBackground()

			got := c.resolve(batch)
			if len(got) != 1 {
				t.Fatalf("an unusable answer must not discard the conversation, got %+v", got)
			}
			if got[0].Meaning != sessionimport.MeaningAmbiguous {
				t.Errorf("verdict should stay ambiguous, got %q", got[0].Meaning)
			}
		})
	}
}

// An unknown auth status is advisory, not a refusal, so it is still attempted.
func TestClassifyAttemptsWhenAuthIsUnknown(t *testing.T) {
	agent := &fakeAgent{
		oneShot: true, authable: true, auth: ports.AgentAuthStatusUnknown,
		reply: `[{"id":"x","verdict":"trivial"}]`,
	}
	c := newClassifier(t, fakeRegistry{agents: map[domain.AgentHarness]*fakeAgent{domain.HarnessClaudeCode: agent}})

	batch := []sessionimport.ImportableSession{ambiguous("x", "hey", "hey")}
	c.resolve(batch)
	c.waitForBackground()

	if got := c.resolve(batch); len(got) != 0 {
		t.Fatalf("an unknown probe should not block classification, got %+v", got)
	}
}

func TestClassifyLeavesSettledConversationsAlone(t *testing.T) {
	agent := &fakeAgent{oneShot: true, authable: true, auth: ports.AgentAuthStatusAuthorized, reply: `[{"id":"a","verdict":"trivial"}]`}
	c := newClassifier(t, fakeRegistry{agents: map[domain.AgentHarness]*fakeAgent{domain.HarnessClaudeCode: agent}})

	settled := ambiguous("a", "Real work", "real work")
	settled.Meaning = sessionimport.MeaningMeaningful

	got := c.resolve([]sessionimport.ImportableSession{settled})
	c.waitForBackground()
	if len(got) != 1 {
		t.Fatal("a locally settled conversation must survive")
	}
	if len(agent.prompts) != 0 {
		t.Error("no model call should be made when nothing is ambiguous")
	}
}

// Re-opening the list must not spend the user's quota again.
func TestClassifyCachesVerdicts(t *testing.T) {
	agent := &fakeAgent{oneShot: true, authable: true, auth: ports.AgentAuthStatusAuthorized, reply: `[{"id":"x","verdict":"meaningful"}]`}
	reg := fakeRegistry{agents: map[domain.AgentHarness]*fakeAgent{domain.HarnessClaudeCode: agent}}
	c := newClassifier(t, reg)

	for i := 0; i < 3; i++ {
		got := c.resolve([]sessionimport.ImportableSession{ambiguous("x", "Real", "a real question about sharding")})
		c.waitForBackground()
		if len(got) != 1 {
			t.Fatalf("run %d: conversation lost", i)
		}
	}
	if len(agent.prompts) != 1 {
		t.Fatalf("want one call across three list opens, got %d", len(agent.prompts))
	}

	// A conversation that has since been continued is judged afresh.
	moved := ambiguous("x", "Real", "a real question about sharding")
	moved.LastActivity = moved.LastActivity.Add(time.Hour)
	c.resolve([]sessionimport.ImportableSession{moved})
	c.waitForBackground()
	if len(agent.prompts) != 2 {
		t.Fatalf("a continued conversation should be re-judged, got %d calls", len(agent.prompts))
	}
}

func TestClassifyBatchesPerProvider(t *testing.T) {
	claude := &fakeAgent{oneShot: true, authable: true, auth: ports.AgentAuthStatusAuthorized, reply: `[{"id":"c1","verdict":"meaningful"},{"id":"c2","verdict":"meaningful"}]`}
	codex := &fakeAgent{oneShot: true, authable: true, auth: ports.AgentAuthStatusAuthorized, reply: `[{"id":"x1","verdict":"meaningful"}]`}
	c := newClassifier(t, fakeRegistry{agents: map[domain.AgentHarness]*fakeAgent{
		domain.HarnessClaudeCode: claude,
		domain.HarnessCodex:      codex,
	}})

	codexSession := ambiguous("x1", "Codex thread", "a codex question")
	codexSession.Provider = domain.HarnessCodex

	got := c.resolve([]sessionimport.ImportableSession{
		ambiguous("c1", "One", "first question"),
		ambiguous("c2", "Two", "second question"),
		codexSession,
	})
	c.waitForBackground()
	if len(got) != 3 {
		t.Fatalf("want all three kept, got %d", len(got))
	}
	// Each provider judges its own conversations, in one call each.
	if len(claude.prompts) != 1 || len(codex.prompts) != 1 {
		t.Fatalf("want one call per provider, got claude=%d codex=%d", len(claude.prompts), len(codex.prompts))
	}
	if strings.Contains(claude.prompts[0], "x1") {
		t.Error("a Codex conversation was sent to Claude Code")
	}
	if strings.Contains(codex.prompts[0], "c1") {
		t.Error("a Claude Code conversation was sent to Codex")
	}
}

func TestExcerptBoundsWhatLeavesTheMachine(t *testing.T) {
	long := strings.Repeat("x", maxExcerptRunes*2)
	got := excerpt(long)
	if len([]rune(got)) > maxExcerptRunes+1 {
		t.Errorf("excerpt not bounded: %d runes", len([]rune(got)))
	}
	if excerpt("  a\n\n  b  ") != "a b" {
		t.Errorf("excerpt should collapse whitespace, got %q", excerpt("  a\n\n  b  "))
	}
}
