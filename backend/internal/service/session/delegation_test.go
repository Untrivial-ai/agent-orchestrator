package session

import (
	"context"
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
			now := time.Now().UTC()
			st.sessions["orch-old"] = domain.SessionRecord{ID: "orch-old", ProjectID: "ao", Kind: domain.KindOrchestrator, CreatedAt: now.Add(-time.Minute)}
			st.sessions["orch-exited"] = domain.SessionRecord{ID: "orch-exited", ProjectID: "ao", Kind: domain.KindOrchestrator, Activity: domain.Activity{State: domain.ActivityExited}, CreatedAt: now}
			cmd := &fakeCommander{}
			brief := "  Fix the renderer\nwithout changing the API.  "

			out, err := (&Service{store: st, manager: cmd}).DelegateTask(context.Background(), DelegateTaskInput{
				ProjectID: "ao", Brief: brief, RequestedAgent: tt.agent, Model: tt.model,
				Effort: effort, RequestedMode: tt.mode, TaskPreparation: "prep-token",
			})
			if err != nil {
				t.Fatalf("DelegateTask: %v", err)
			}
			if out.WorkerID != "mer-9" || out.OrchestratorID != "" {
				t.Fatalf("out = %#v, want worker mer-9 only", out)
			}
			cfg := cmd.spawnedCfg
			if !cmd.spawned || cfg.ProjectID != "ao" || cfg.Kind != domain.KindWorker || cfg.Harness != tt.wantAgent || cfg.Prompt != brief || cfg.DisplayName != "Fix the renderer without changing the API." {
				t.Fatalf("spawn cfg = %#v", cfg)
			}
			if cfg.AgentConfig.Model != strings.TrimSpace(tt.model) || cfg.AgentConfig.Effort != strings.TrimSpace(tt.effort) || cfg.EffortOverride != (effort != nil) || cfg.RequestedMode != tt.mode {
				t.Fatalf("spawn tuning = %#v", cfg)
			}
			if cfg.StartupSystemPrompt != sessionmanager.DelegatedTaskTitleStartupPrompt || cfg.TaskPreparation != "prep-token" || !cfg.Async {
				t.Fatalf("spawn startup/preparation = %#v", cfg)
			}
			if cmd.spawnCalls != 1 || len(cmd.resumed) != 0 || len(cmd.ready) != 0 || len(cmd.sent) != 0 || len(cmd.backgroundCalls) != 0 {
				t.Fatalf("delegation started extra title work: spawns=%d resumed=%#v ready=%#v sent=%#v background=%#v", cmd.spawnCalls, cmd.resumed, cmd.ready, cmd.sent, cmd.backgroundCalls)
			}
		})
	}
}

func TestDelegatedTaskDisplayName(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: " \n\t ", want: "Untitled task"},
		{name: "short", in: "  tell me a joke  ", want: "tell me a joke"},
		{name: "whitespace", in: "Fix the renderer\nwithout changing the API", want: "Fix the renderer without changing the API"},
		{name: "unicode rune limit", in: strings.Repeat("一", 101), want: strings.Repeat("一", 100)},
		{name: "truncates past the rune limit", in: strings.Repeat(" long", 21), want: strings.TrimSpace(strings.Repeat(" long", 21)[:100])},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := delegatedTaskDisplayName(tt.in); got != tt.want {
				t.Fatalf("delegatedTaskDisplayName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDelegateTaskStartsPromptlessWorkerWithoutRequestingTitle(t *testing.T) {
	st := newFakeStore()
	st.projects["ao"] = domain.ProjectRecord{ID: "ao"}
	cmd := &fakeCommander{}
	out, err := (&Service{store: st, manager: cmd}).DelegateTask(context.Background(), DelegateTaskInput{ProjectID: "ao", Brief: " \n\t "})
	if err != nil || out.WorkerID != "mer-9" || out.OrchestratorID != "" {
		t.Fatalf("DelegateTask = %#v, %v", out, err)
	}
	if cfg := cmd.spawnedCfg; !cmd.spawned || cfg.Prompt != "" || cfg.DisplayName != "Untitled task" || cfg.StartupSystemPrompt != "" {
		t.Fatalf("spawn cfg = %#v", cfg)
	}
	if len(cmd.backgroundCalls) != 0 || len(cmd.ready) != 0 || len(cmd.sent) != 0 || len(cmd.resumed) != 0 {
		t.Fatalf("promptless spawn started title work: background=%#v ready=%#v sent=%#v resumed=%#v", cmd.backgroundCalls, cmd.ready, cmd.sent, cmd.resumed)
	}
}

func TestDelegateTaskDoesNotSpawnOrResumeOrchestratorForTitle(t *testing.T) {
	st := newFakeStore()
	st.projects["ao"] = domain.ProjectRecord{ID: "ao"}
	st.sessions["orch"] = domain.SessionRecord{ID: "orch", ProjectID: "ao", Kind: domain.KindOrchestrator, Activity: domain.Activity{State: domain.ActivityExited}}
	cmd := &fakeCommander{spawnFunc: func(cfg ports.SpawnConfig) domain.SessionRecord {
		if cfg.Kind == domain.KindOrchestrator {
			t.Fatal("orchestrator spawned for title")
		}
		return domain.SessionRecord{ID: "worker-new", ProjectID: cfg.ProjectID, Kind: cfg.Kind}
	}}
	out, err := (&Service{store: st, manager: cmd}).DelegateTask(context.Background(), DelegateTaskInput{ProjectID: "ao", Brief: "Fix it"})
	if err != nil || out.WorkerID != "worker-new" || out.OrchestratorID != "" {
		t.Fatalf("DelegateTask = %#v, %v", out, err)
	}
	if cmd.spawnCalls != 1 || len(cmd.resumed) != 0 || len(cmd.ready) != 0 || len(cmd.sent) != 0 || len(cmd.backgroundCalls) != 0 {
		t.Fatalf("orchestrator contacted for title: spawns=%d resumed=%#v ready=%#v sent=%#v background=%#v", cmd.spawnCalls, cmd.resumed, cmd.ready, cmd.sent, cmd.backgroundCalls)
	}
}
