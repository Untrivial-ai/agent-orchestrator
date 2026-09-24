package session

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type researchCommander struct {
	commander
	run     func(context.Context, domain.ResearcherConfig, func(context.Context, ports.ChatEvent) (ports.ChatDecision, error)) (string, error)
	stopped []string
}

func (*researchCommander) SupportsResearch(domain.AgentHarness) bool { return true }
func (r *researchCommander) RunResearch(ctx context.Context, _ domain.SessionID, _, _ string, cfg domain.ResearcherConfig, approval func(context.Context, ports.ChatEvent) (ports.ChatDecision, error)) (string, error) {
	return r.run(ctx, cfg, approval)
}
func (r *researchCommander) StopResearchOrphan(_ context.Context, run domain.ResearchRun) error {
	r.stopped = append(r.stopped, run.ID)
	return nil
}

func TestResearchLifecycle(t *testing.T) {
	ctx := context.Background()
	st := sqlitetest.MustOpen(t)
	project := domain.ProjectRecord{
		ID: "mer", Path: t.TempDir(), RegisteredAt: time.Now(),
		Config: domain.ProjectConfig{
			AgentConfig: domain.AgentConfig{Permissions: domain.PermissionModeAcceptEdits},
			Researcher: domain.ResearcherConfig{Enabled: true, Harness: domain.HarnessCodex,
				AgentConfig: domain.AgentConfig{Model: "chosen-model", Effort: "high"}},
		},
	}
	if err := st.UpsertProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	parent, err := st.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "mer", Kind: domain.KindOrchestrator, Harness: domain.HarnessClaudeCode,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &researchCommander{}
	s := NewWithDeps(Deps{Store: st, Manager: runner})
	var work func()
	s.runBackground = func(fn func()) { work = fn }
	runner.run = func(_ context.Context, cfg domain.ResearcherConfig, _ func(context.Context, ports.ChatEvent) (ports.ChatDecision, error)) (string, error) {
		if cfg.AgentConfig.Model != "chosen-model" || cfg.AgentConfig.Effort != "high" || cfg.AgentConfig.Permissions != domain.PermissionModeAcceptEdits {
			t.Fatalf("settings were not snapshotted: %+v", cfg)
		}
		return strings.Repeat("€", maxResearchResult/3+1), nil
	}
	run, err := s.StartResearch(ctx, parent.ID, "Find the flow")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartResearch(ctx, parent.ID, "Duplicate"); err == nil {
		t.Fatal("allowed concurrent research")
	}
	if _, err := s.GetResearch(ctx, "another-parent", run.ID); err == nil {
		t.Fatal("exposed another parent's research")
	}
	project.Config.Researcher.AgentConfig.Model = "next-model"
	if err := st.UpsertProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	work()
	got, err := s.GetResearch(ctx, parent.ID, run.ID)
	if err != nil || got.Status != "completed" || got.FinishedAt == nil || !utf8.ValidString(got.Result) || !strings.Contains(got.Result, "truncated") {
		t.Fatalf("completed research: status=%s, validUTF8=%v, err=%v", got.Status, utf8.ValidString(got.Result), err)
	}
	// Cancelling queued work must not start a paid provider call.
	runner.run = func(context.Context, domain.ResearcherConfig, func(context.Context, ports.ChatEvent) (ports.ChatDecision, error)) (string, error) {
		t.Fatal("cancelled/recovered research reached provider")
		return "", nil
	}
	run, err = s.StartResearch(ctx, parent.ID, "Cancel this")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelResearch(ctx, parent.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	work()
	got, err = s.GetResearch(ctx, parent.ID, run.ID)
	if err != nil || got.Status != "cancelled" {
		t.Fatalf("cancelled research = %+v, %v", got, err)
	}
	run, err = s.StartResearch(ctx, parent.ID, "Interrupted")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverResearch(ctx); err != nil {
		t.Fatal(err)
	}
	work()
	got, err = s.GetResearch(ctx, parent.ID, run.ID)
	if err != nil || got.Status != "interrupted" || len(runner.stopped) != 1 || runner.stopped[0] != run.ID {
		t.Fatalf("recovered research = %+v, %v; stopped=%v", got, err, runner.stopped)
	}
	project.Config.Researcher.Enabled = false
	if err := st.UpsertProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartResearch(ctx, parent.ID, "Disabled"); err == nil {
		t.Fatal("disabled research started")
	}
}

func TestResearchDeadlinePersistsFailure(t *testing.T) {
	ctx := context.Background()
	st := sqlitetest.MustOpen(t)
	project := domain.ProjectRecord{
		ID: "mer", Path: t.TempDir(), RegisteredAt: time.Now(),
		Config: domain.ProjectConfig{Researcher: domain.ResearcherConfig{Enabled: true, Harness: domain.HarnessCodex}},
	}
	if err := st.UpsertProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	parent, err := st.CreateSession(ctx, domain.SessionRecord{
		ProjectID: domain.ProjectID(project.ID), Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if researchTimeout != 30*time.Minute {
		t.Fatalf("research timeout = %s, want 30m", researchTimeout)
	}

	background, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := make(chan struct{})
	runner := &researchCommander{run: func(ctx context.Context, _ domain.ResearcherConfig, _ func(context.Context, ports.ChatEvent) (ports.ChatDecision, error)) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	s := NewWithDeps(Deps{Store: st, Manager: runner, BackgroundContext: background})
	var work func()
	s.runBackground = func(fn func()) { work = fn }
	run, err := s.StartResearch(ctx, parent.ID, "Wait until the deadline")
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	go func() {
		work()
		close(finished)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("research runner did not start")
	}
	running, err := s.GetResearch(ctx, parent.ID, run.ID)
	if err != nil || running.Status != "running" {
		t.Fatalf("active research = status %q, err %v", running.Status, err)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("research did not stop at its deadline")
	}

	got, err := s.GetResearch(ctx, parent.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || got.Error != "Research exceeded the 30-minute limit" || got.Result != "" {
		t.Fatalf("deadline result = status %q, error %q, result length %d", got.Status, got.Error, len(got.Result))
	}
}

func TestResearchApprovalUsesOnlyOfferedProviderChoices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st := sqlitetest.MustOpen(t)
	seedKanbanTestProject(t, st, "mer")
	parent, err := st.CreateSession(ctx, domain.SessionRecord{ProjectID: "mer", Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateResearchRun(ctx, domain.ResearchRun{ID: "run-1", ParentSessionID: parent.ID, ProjectID: "mer", Prompt: "question", Harness: domain.HarnessCodex, CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	s := NewWithDeps(Deps{Store: st, Manager: &researchCommander{}})
	result := make(chan ports.ChatDecision, 1)
	errors := make(chan error, 1)
	go func() {
		decision, err := s.awaitResearchApproval(ctx, "run-1", ports.ChatEvent{
			RequestID: "approval-1", Summary: "Allow command?",
			Decisions: []ports.ChatDecisionOption{{ID: "allow", Label: "Allow once", Raw: []byte(`{"approved":true}`)}},
		})
		result <- decision
		errors <- err
	}()
	for {
		run, err := s.GetResearch(ctx, parent.ID, "run-1")
		if err != nil {
			t.Fatal(err)
		}
		if run.Approval != nil {
			if _, err := s.ResolveResearchApproval(ctx, "wrong-parent", "run-1", "approval-1", "allow"); err == nil {
				t.Fatal("accepted another parent's approval")
			}
			if _, err := s.ResolveResearchApproval(ctx, parent.ID, "run-1", "approval-1", "invented"); err == nil {
				t.Fatal("accepted an unknown choice")
			}
			if _, err := s.ResolveResearchApproval(ctx, parent.ID, "run-1", "approval-1", "allow"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ResolveResearchApproval(ctx, parent.ID, "run-1", "approval-1", "allow"); err == nil {
				t.Fatal("accepted duplicate approval")
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("approval was never exposed")
		case <-time.After(time.Millisecond):
		}
	}
	if got := <-result; got.ID != "allow" || string(got.Raw) != `{"approved":true}` {
		t.Fatalf("decision = %+v", got)
	}
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
	s.researchMu.Lock()
	defer s.researchMu.Unlock()
	if len(s.researchApprovals) != 0 {
		t.Fatal("approval was not cleared")
	}
}
