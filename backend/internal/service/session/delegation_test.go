package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestDelegateTaskSpawnsWorkerAndRefinesTitleThroughBackgroundHarness(t *testing.T) {
	tests := []struct {
		name      string
		agent     domain.AgentHarness
		model     string
		effort    string
		mode      domain.SessionMode
		wantAgent domain.AgentHarness
	}{
		{name: "project default"},
		{name: "requested agent model and mode", agent: domain.HarnessCursor, model: "  sonnet-custom  ", effort: " high ", mode: domain.SessionModeChat, wantAgent: domain.HarnessCursor},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var effort *string
			if tt.effort != "" {
				effort = &tt.effort
			}
			st := newFakeStore()
			st.projects["ao"] = domain.ProjectRecord{ID: "ao"}
			cmd := &fakeCommander{backgroundResult: "Fix renderer"}
			cmd.spawnFunc = func(cfg ports.SpawnConfig) domain.SessionRecord {
				rec := domain.SessionRecord{
					ID: "mer-9", ProjectID: cfg.ProjectID, Kind: cfg.Kind, Harness: cfg.Harness,
					DisplayName: cfg.DisplayName,
				}
				st.sessions[rec.ID] = rec
				return rec
			}
			svc := &Service{store: st, manager: cmd, runBackground: runInline}
			brief := "  Fix the renderer\nwithout changing the API.  "

			out, err := svc.DelegateTask(context.Background(), DelegateTaskInput{
				ProjectID: "ao", Brief: brief, RequestedAgent: tt.agent, Model: tt.model,
				Effort: effort, RequestedMode: tt.mode,
			})
			if err != nil {
				t.Fatalf("DelegateTask: %v", err)
			}
			if out.WorkerID != "mer-9" || out.OrchestratorID != "" {
				t.Fatalf("out = %#v, want worker mer-9 only", out)
			}
			if !cmd.spawned || cmd.spawnedCfg.ProjectID != "ao" || cmd.spawnedCfg.Kind != domain.KindWorker || cmd.spawnedCfg.Harness != tt.wantAgent || cmd.spawnedCfg.Prompt != brief || cmd.spawnedCfg.DisplayName != "Fix the renderer without changing the API." {
				t.Fatalf("spawn cfg = %#v", cmd.spawnedCfg)
			}
			if cmd.spawnedCfg.AgentConfig.Model != strings.TrimSpace(tt.model) || cmd.spawnedCfg.AgentConfig.Effort != strings.TrimSpace(tt.effort) {
				t.Fatalf("spawn tuning = %#v", cmd.spawnedCfg.AgentConfig)
			}
			if cmd.spawnedCfg.EffortOverride != (effort != nil) || cmd.spawnedCfg.RequestedMode != tt.mode {
				t.Fatalf("spawn tuning presence/mode = %#v/%q", cmd.spawnedCfg, cmd.spawnedCfg.RequestedMode)
			}
			if len(cmd.backgroundCalls) != 1 {
				t.Fatalf("background calls = %#v, want one", cmd.backgroundCalls)
			}
			call := cmd.backgroundCalls[0]
			if call.id != "mer-9" || call.prompt != brief || call.systemPrompt != delegatedTaskTitleSystemPrompt {
				t.Fatalf("background call = %#v", call)
			}
			if len(cmd.sent) != 0 || len(cmd.resumed) != 0 || cmd.spawnCalls != 1 {
				t.Fatalf("title refinement touched a session: sent=%#v resumed=%#v spawns=%d", cmd.sent, cmd.resumed, cmd.spawnCalls)
			}
			if got := st.sessions["mer-9"].DisplayName; got != "Fix renderer" {
				t.Fatalf("display name = %q, want generated title", got)
			}
		})
	}
}

func TestDelegatedTaskTitles(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{name: "empty provisional", in: " \n\t ", want: "Untitled task"},
		{name: "short provisional", in: "  tell me a joke  ", want: "tell me a joke"},
		{name: "provisional whitespace", in: "Fix the renderer\nwithout changing the API", want: "Fix the renderer without changing the API"},
		{name: "provisional unicode limit", in: strings.Repeat("一", 101), want: strings.Repeat("一", 100)},
		{name: "provisional byte truncation", in: strings.Repeat(" long", 21), want: strings.TrimSpace(strings.Repeat(" long", 21)[:100])},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := delegatedTaskDisplayName(tt.in); got != tt.want {
				t.Fatalf("delegatedTaskDisplayName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	for _, tt := range []struct {
		in   string
		want string
	}{
		{in: "## `Fix renderer.`\nExtra prose", want: "Fix renderer"},
		{in: "Upgrade to C++", want: "Upgrade to C++"},
		{in: "Move to F#", want: "Move to F#"},
		{in: "Fix\x00 renderer", want: "Fix renderer"},
		{in: strings.Repeat("界", 101), want: strings.Repeat("界", 100)},
		{in: " -- ... ", want: ""},
	} {
		if got := generatedTaskTitle(tt.in); got != tt.want {
			t.Errorf("generatedTaskTitle(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDelegateTaskStartsPromptlessWorkerWithoutGeneratingTitle(t *testing.T) {
	st := newFakeStore()
	st.projects["ao"] = domain.ProjectRecord{ID: "ao"}
	cmd := &fakeCommander{}

	out, err := (&Service{store: st, manager: cmd, runBackground: runInline}).DelegateTask(
		context.Background(), DelegateTaskInput{ProjectID: "ao", Brief: " \n\t "},
	)
	if err != nil {
		t.Fatalf("DelegateTask: %v", err)
	}
	if out.WorkerID != "mer-9" || out.OrchestratorID != "" {
		t.Fatalf("out = %#v, want promptless worker mer-9", out)
	}
	if !cmd.spawned || cmd.spawnedCfg.Prompt != "" || cmd.spawnedCfg.DisplayName != "Untitled task" {
		t.Fatalf("spawn cfg = %#v", cmd.spawnedCfg)
	}
	if len(cmd.backgroundCalls) != 0 || len(cmd.sent) != 0 || len(cmd.resumed) != 0 {
		t.Fatalf("promptless spawn ran title work: background=%#v sent=%#v resumed=%#v", cmd.backgroundCalls, cmd.sent, cmd.resumed)
	}
}

func TestGeneratedTaskTitleDoesNotOverwriteManualRename(t *testing.T) {
	st := newFakeStore()
	st.projects["ao"] = domain.ProjectRecord{ID: "ao"}
	cmd := &fakeCommander{backgroundResult: "Generated title"}
	cmd.spawnFunc = func(cfg ports.SpawnConfig) domain.SessionRecord {
		rec := domain.SessionRecord{ID: "mer-9", ProjectID: cfg.ProjectID, Kind: cfg.Kind, DisplayName: "Mine now"}
		st.sessions[rec.ID] = rec
		return rec
	}

	if _, err := (&Service{store: st, manager: cmd, runBackground: runInline}).DelegateTask(
		context.Background(), DelegateTaskInput{ProjectID: "ao", Brief: "Fix it"},
	); err != nil {
		t.Fatalf("DelegateTask: %v", err)
	}
	if got := st.sessions["mer-9"].DisplayName; got != "Mine now" {
		t.Fatalf("display name = %q, want manual rename preserved", got)
	}
}

func TestDelegateTaskKeepsSpawnSuccessWhenBackgroundTitleFails(t *testing.T) {
	st := newFakeStore()
	st.projects["ao"] = domain.ProjectRecord{ID: "ao"}
	cmd := &fakeCommander{backgroundErr: errors.New("provider unavailable")}

	out, err := (&Service{store: st, manager: cmd, runBackground: runInline}).DelegateTask(
		context.Background(), DelegateTaskInput{ProjectID: "ao", Brief: "Fix it"},
	)
	if err != nil || out.WorkerID != "mer-9" || !cmd.spawned {
		t.Fatalf("DelegateTask = %#v, %v; want successful worker", out, err)
	}
}

func TestDelegateTaskReturnsBeforeBackgroundTitleCompletes(t *testing.T) {
	st := newFakeStore()
	st.projects["ao"] = domain.ProjectRecord{ID: "ao"}
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	cmd := &fakeCommander{backgroundFunc: func(backgroundTaskCall) (string, error) {
		close(started)
		<-release
		close(finished)
		return "Fix it", nil
	}}
	svc := &Service{store: st, manager: cmd}

	out, err := svc.DelegateTask(context.Background(), DelegateTaskInput{ProjectID: "ao", Brief: "Fix it"})
	if err != nil || out.WorkerID != "mer-9" {
		t.Fatalf("DelegateTask = %#v, %v", out, err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background title request did not start")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("background title request did not finish")
	}
}

func TestDelegateTaskSkipsTitleWhenCapacityIsFull(t *testing.T) {
	st := newFakeStore()
	st.projects["ao"] = domain.ProjectRecord{ID: "ao"}
	cmd := &fakeCommander{}
	slots := make(chan struct{}, 1)
	slots <- struct{}{}

	out, err := (&Service{store: st, manager: cmd, titleRefinementSlots: slots}).DelegateTask(
		context.Background(), DelegateTaskInput{ProjectID: "ao", Brief: "Fix it"},
	)
	if err != nil || out.WorkerID != "mer-9" || len(cmd.backgroundCalls) != 0 {
		t.Fatalf("DelegateTask = %#v, %v; background=%#v", out, err, cmd.backgroundCalls)
	}
}

func TestDelegateTaskIdempotencyCoalescesAndReplaysWorker(t *testing.T) {
	st := newTaskDelegationFakeStore()
	st.projects["ao"] = domain.ProjectRecord{ID: "ao"}
	spawnStarted := make(chan struct{})
	releaseSpawn := make(chan struct{})
	var startedOnce sync.Once
	cmd := &fakeCommander{spawnFunc: func(cfg ports.SpawnConfig) domain.SessionRecord {
		startedOnce.Do(func() { close(spawnStarted) })
		<-releaseSpawn
		return domain.SessionRecord{ID: "ao-1", ProjectID: cfg.ProjectID, Kind: cfg.Kind, Harness: cfg.Harness}
	}}
	svc := &Service{store: st, manager: cmd, runBackground: func(func()) {}}
	input := DelegateTaskInput{ProjectID: "ao", Brief: "Fix the race", IdempotencyKey: "request-1"}

	type result struct {
		out DelegateTaskOutcome
		err error
	}
	results := make(chan result, 2)
	go func() {
		out, err := svc.DelegateTask(context.Background(), input)
		results <- result{out: out, err: err}
	}()
	select {
	case <-spawnStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first delegation did not reach spawn")
	}
	go func() {
		out, err := svc.DelegateTask(context.Background(), input)
		results <- result{out: out, err: err}
	}()
	close(releaseSpawn)

	for range 2 {
		got := <-results
		if got.err != nil || got.out.WorkerID != "ao-1" {
			t.Fatalf("delegation = %#v, err=%v", got.out, got.err)
		}
	}
	if cmd.spawnCalls != 1 {
		t.Fatalf("spawn calls = %d, want 1", cmd.spawnCalls)
	}

	replayed, err := svc.DelegateTask(context.Background(), input)
	if err != nil || replayed.WorkerID != "ao-1" || cmd.spawnCalls != 1 {
		t.Fatalf("replay = %#v, err=%v, spawn calls=%d", replayed, err, cmd.spawnCalls)
	}
}

func TestDelegateTaskIdempotencyRejectsDifferentPayload(t *testing.T) {
	st := newTaskDelegationFakeStore()
	st.projects["ao"] = domain.ProjectRecord{ID: "ao"}
	cmd := &fakeCommander{}
	svc := &Service{store: st, manager: cmd, runBackground: func(func()) {}}
	if _, err := svc.DelegateTask(context.Background(), DelegateTaskInput{
		ProjectID: "ao", Brief: "First task", IdempotencyKey: "request-1",
	}); err != nil {
		t.Fatal(err)
	}

	_, err := svc.DelegateTask(context.Background(), DelegateTaskInput{
		ProjectID: "ao", Brief: "Different task", IdempotencyKey: "request-1",
	})
	var apiError *apierr.Error
	if !errors.As(err, &apiError) || apiError.Kind != apierr.KindConflict || apiError.Code != "TASK_DELEGATION_IDEMPOTENCY_CONFLICT" {
		t.Fatalf("conflict error = %v", err)
	}
	if cmd.spawnCalls != 1 {
		t.Fatalf("spawn calls = %d, want 1", cmd.spawnCalls)
	}
}

func TestKillCancelsBackgroundTitle(t *testing.T) {
	st := newFakeStore()
	st.projects["ao"] = domain.ProjectRecord{ID: "ao"}
	started := make(chan struct{})
	finished := make(chan struct{})
	cmd := &fakeCommander{backgroundFunc: func(call backgroundTaskCall) (string, error) {
		close(started)
		<-call.ctx.Done()
		close(finished)
		return "", call.ctx.Err()
	}}
	svc := NewWithDeps(Deps{Manager: cmd, Store: st})

	if _, err := svc.DelegateTask(context.Background(), DelegateTaskInput{ProjectID: "ao", Brief: "Fix it"}); err != nil {
		t.Fatalf("DelegateTask: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background title request did not start")
	}
	if _, err := svc.Kill(context.Background(), "mer-9"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("background title request was not cancelled")
	}
}

func runInline(work func()) { work() }

type taskDelegationFakeStore struct {
	*fakeStore
	mu      sync.Mutex
	records map[string]domain.TaskDelegation
}

func newTaskDelegationFakeStore() *taskDelegationFakeStore {
	return &taskDelegationFakeStore{
		fakeStore: newFakeStore(),
		records:   map[string]domain.TaskDelegation{},
	}
}

func (f *taskDelegationFakeStore) ReserveTaskDelegation(_ context.Context, rec domain.TaskDelegation) (domain.TaskDelegation, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	existing, ok := f.records[rec.IdempotencyKey]
	if !ok {
		f.records[rec.IdempotencyKey] = rec
		return rec, true, nil
	}
	if existing.RequestFingerprint != rec.RequestFingerprint {
		return existing, false, domain.ErrTaskDelegationIdempotencyConflict
	}
	return existing, false, nil
}

func (f *taskDelegationFakeStore) CompleteTaskDelegation(
	_ context.Context,
	idempotencyKey string,
	fingerprint domain.TaskDelegationRequestFingerprint,
	workerID domain.SessionID,
	updatedAt time.Time,
) (domain.TaskDelegation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := f.records[idempotencyKey]
	if rec.RequestFingerprint != fingerprint {
		return rec, domain.ErrTaskDelegationIdempotencyConflict
	}
	rec.State = domain.TaskDelegationCompleted
	rec.WorkerID = workerID
	rec.UpdatedAt = updatedAt
	f.records[idempotencyKey] = rec
	return rec, nil
}
