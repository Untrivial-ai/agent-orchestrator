package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

func TestDelegateTaskSpawnsWorkerWithoutSecondMessage(t *testing.T) {
	tests := []struct {
		name      string
		agent     domain.AgentHarness
		model     string
		mode      domain.SessionMode
		wantAgent domain.AgentHarness
	}{
		{name: "project default"},
		{name: "requested agent model and mode", agent: domain.HarnessCursor, model: "  sonnet-custom  ", mode: domain.SessionModeChat, wantAgent: domain.HarnessCursor},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newFakeStore()
			st.projects["ao"] = domain.ProjectRecord{ID: "ao"}
			now := time.Now().UTC()
			st.sessions["orch-old"] = domain.SessionRecord{ID: "orch-old", ProjectID: "ao", Kind: domain.KindOrchestrator, CreatedAt: now.Add(-time.Minute)}
			st.sessions["orch-new"] = domain.SessionRecord{ID: "orch-new", ProjectID: "ao", Kind: domain.KindOrchestrator, CreatedAt: now}
			st.sessions["orch-exited"] = domain.SessionRecord{ID: "orch-exited", ProjectID: "ao", Kind: domain.KindOrchestrator, Activity: domain.Activity{State: domain.ActivityExited}, CreatedAt: now.Add(time.Minute)}
			st.sessions["orch-dead"] = domain.SessionRecord{ID: "orch-dead", ProjectID: "ao", Kind: domain.KindOrchestrator, IsTerminated: true, CreatedAt: now.Add(2 * time.Minute)}
			st.sessions["worker"] = domain.SessionRecord{ID: "worker", ProjectID: "ao", Kind: domain.KindWorker, CreatedAt: now.Add(3 * time.Minute)}
			cmd := &fakeCommander{}
			svc := &Service{store: st, manager: cmd}

			brief := "  Fix the renderer\nwithout changing the API.  "
			out, err := svc.DelegateTask(context.Background(), DelegateTaskInput{
				ProjectID: "ao", Brief: brief, RequestedAgent: tt.agent, Model: tt.model, RequestedMode: tt.mode,
			})
			if err != nil {
				t.Fatalf("DelegateTask: %v", err)
			}
			if out.WorkerID != "mer-9" || out.OrchestratorID != "" {
				t.Fatalf("out = %#v, want worker mer-9", out)
			}
			if !cmd.spawned || cmd.spawnedCfg.ProjectID != "ao" || cmd.spawnedCfg.Kind != domain.KindWorker || cmd.spawnedCfg.Harness != tt.wantAgent || cmd.spawnedCfg.Prompt != brief || cmd.spawnedCfg.DisplayName != "Fix the renderer wit" {
				t.Fatalf("spawn cfg = %#v", cmd.spawnedCfg)
			}
			if cmd.spawnedCfg.AgentConfig.Model != strings.TrimSpace(tt.model) {
				t.Fatalf("spawn model = %q, want %q", cmd.spawnedCfg.AgentConfig.Model, strings.TrimSpace(tt.model))
			}
			if cmd.spawnedCfg.RequestedMode != tt.mode {
				t.Fatalf("spawn mode = %q, want %q", cmd.spawnedCfg.RequestedMode, tt.mode)
			}
			if cmd.spawnedCfg.StartupSystemPrompt != sessionmanager.DelegatedTaskTitleStartupPrompt {
				t.Fatalf("startup system prompt = %q, want delegated title instruction", cmd.spawnedCfg.StartupSystemPrompt)
			}
			if cmd.spawnCalls != 1 {
				t.Fatalf("spawn calls = %d, want one worker spawn", cmd.spawnCalls)
			}
			if len(cmd.resumed) != 0 || len(cmd.ready) != 0 || len(cmd.sent) != 0 {
				t.Fatalf("delegation contacted an orchestrator or sent a second message: resumed=%#v ready=%#v sent=%#v", cmd.resumed, cmd.ready, cmd.sent)
			}
		})
	}
}

func TestDelegatedTaskDisplayName(t *testing.T) {
	for _, tt := range []struct {
		name  string
		brief string
		want  string
	}{
		{name: "empty", brief: " \n\t ", want: "Untitled task"},
		{name: "short", brief: "  tell me a joke  ", want: "tell me a joke"},
		{name: "whitespace", brief: "Fix the renderer\nwithout changing the API", want: "Fix the renderer wit"},
		{name: "unicode rune limit", brief: "一二三四五六七八九十一二三四五六七八九十一", want: "一二三四五六七八九十一二三四五六七八九十"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := delegatedTaskDisplayName(tt.brief); got != tt.want {
				t.Fatalf("delegatedTaskDisplayName(%q) = %q, want %q", tt.brief, got, tt.want)
			}
		})
	}
}

func TestDelegateTaskStartsPromptlessWorkerWithoutRequestingTitle(t *testing.T) {
	st := newFakeStore()
	st.projects["ao"] = domain.ProjectRecord{ID: "ao"}
	st.sessions["orch"] = domain.SessionRecord{ID: "orch", ProjectID: "ao", Kind: domain.KindOrchestrator}
	cmd := &fakeCommander{}

	out, err := (&Service{store: st, manager: cmd}).DelegateTask(
		context.Background(),
		DelegateTaskInput{ProjectID: "ao", Brief: " \n\t "},
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
	if cmd.spawnedCfg.StartupSystemPrompt != "" {
		t.Fatalf("promptless startup system prompt = %q, want empty", cmd.spawnedCfg.StartupSystemPrompt)
	}
	if len(cmd.ready) != 0 || len(cmd.sent) != 0 || len(cmd.resumed) != 0 {
		t.Fatalf("promptless spawn contacted orchestrator: ready=%#v sent=%#v resumed=%#v", cmd.ready, cmd.sent, cmd.resumed)
	}
}

func TestDelegateTaskDoesNotResumeOrchestratorForTitle(t *testing.T) {
	st := newFakeStore()
	st.projects["ao"] = domain.ProjectRecord{ID: "ao"}
	now := time.Now().UTC()
	st.sessions["orch-old"] = domain.SessionRecord{ID: "orch-old", ProjectID: "ao", Kind: domain.KindOrchestrator, Activity: domain.Activity{State: domain.ActivityExited}, CreatedAt: now.Add(-time.Minute)}
	st.sessions["orch-new"] = domain.SessionRecord{ID: "orch-new", ProjectID: "ao", Kind: domain.KindOrchestrator, Activity: domain.Activity{State: domain.ActivityExited}, CreatedAt: now}
	cmd := &fakeCommander{}

	out, err := (&Service{store: st, manager: cmd}).DelegateTask(context.Background(), DelegateTaskInput{ProjectID: "ao", Brief: "Fix it"})
	if err != nil {
		t.Fatalf("DelegateTask: %v", err)
	}
	if out.WorkerID != "mer-9" || out.OrchestratorID != "" {
		t.Fatalf("out = %#v, want worker mer-9", out)
	}
	if cmd.spawnCalls != 1 || cmd.spawnedCfg.Kind != domain.KindWorker {
		t.Fatalf("spawn calls/config = %d/%#v, want one worker spawn", cmd.spawnCalls, cmd.spawnedCfg)
	}
	if len(cmd.resumed) != 0 || len(cmd.ready) != 0 || len(cmd.sent) != 0 {
		t.Fatalf("delegation contacted an exited orchestrator: resumed=%#v ready=%#v sent=%#v", cmd.resumed, cmd.ready, cmd.sent)
	}
}

func TestDelegateTaskDoesNotSpawnOrchestratorForTitle(t *testing.T) {
	st := newFakeStore()
	st.projects["ao"] = domain.ProjectRecord{ID: "ao"}
	st.sessions["orch-dead"] = domain.SessionRecord{ID: "orch-dead", ProjectID: "ao", Kind: domain.KindOrchestrator, IsTerminated: true}
	cmd := &fakeCommander{spawnFunc: func(cfg ports.SpawnConfig) domain.SessionRecord {
		if cfg.Kind == domain.KindOrchestrator {
			return domain.SessionRecord{ID: "orch-new", ProjectID: cfg.ProjectID, Kind: cfg.Kind}
		}
		return domain.SessionRecord{ID: "worker-new", ProjectID: cfg.ProjectID, Kind: cfg.Kind}
	}}

	out, err := (&Service{store: st, manager: cmd}).DelegateTask(context.Background(), DelegateTaskInput{ProjectID: "ao", Brief: "Fix it"})
	if err != nil {
		t.Fatalf("DelegateTask: %v", err)
	}
	if out.WorkerID != "worker-new" || out.OrchestratorID != "" {
		t.Fatalf("out = %#v, want worker-new", out)
	}
	if cmd.spawnCalls != 1 || cmd.spawnedCfg.Kind != domain.KindWorker {
		t.Fatalf("spawn calls/config = %d/%#v, want one worker spawn", cmd.spawnCalls, cmd.spawnedCfg)
	}
	if len(cmd.resumed) != 0 || len(cmd.ready) != 0 || len(cmd.sent) != 0 {
		t.Fatalf("delegation contacted or spawned an orchestrator: resumed=%#v ready=%#v sent=%#v", cmd.resumed, cmd.ready, cmd.sent)
	}
}

func TestDelegateTaskKeepsProvisionalDisplayNameWithoutTitleReadiness(t *testing.T) {
	st := newFakeStore()
	st.projects["ao"] = domain.ProjectRecord{ID: "ao"}
	st.sessions["orch"] = domain.SessionRecord{ID: "orch", ProjectID: "ao", Kind: domain.KindOrchestrator}
	cmd := &fakeCommander{readyErr: errors.New("readiness timed out")}

	out, err := (&Service{store: st, manager: cmd}).DelegateTask(context.Background(), DelegateTaskInput{ProjectID: "ao", Brief: "Fix it"})
	if err != nil {
		t.Fatalf("DelegateTask: %v", err)
	}
	if out.WorkerID != "mer-9" || out.OrchestratorID != "" {
		t.Fatalf("out = %#v, want spawned worker", out)
	}
	if cmd.spawnedCfg.DisplayName != "Fix it" {
		t.Fatalf("display name = %q, want provisional title", cmd.spawnedCfg.DisplayName)
	}
	if len(cmd.resumed) != 0 || len(cmd.ready) != 0 || len(cmd.sent) != 0 {
		t.Fatalf("delegation attempted title delivery: resumed=%#v ready=%#v sent=%#v", cmd.resumed, cmd.ready, cmd.sent)
	}
}
