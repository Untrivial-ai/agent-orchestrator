package session

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestSpawnForwardsTheChosenAccountAndStillRequiresTheAgentBinary(t *testing.T) {
	for _, installed := range []domain.AgentInstallationState{domain.AgentInstallationInstalled, domain.AgentInstallationNotInstalled} {
		for _, selection := range []string{"", "account-a"} {
			store := newFakeStore()
			store.projects["project"] = domain.ProjectRecord{ID: "project"}
			manager := &fakeCommander{}
			readiness := &fakeAgentReadiness{snapshot: domain.AgentReadinessSnapshot{ID: "codex", Installation: domain.AgentInstallationObservation{State: installed, Freshness: domain.AgentReadinessFresh},
				Authentication: domain.AgentAuthenticationObservation{State: domain.AgentAuthenticationAuthorized, Freshness: domain.AgentReadinessFresh}}}
			service := NewWithDeps(Deps{Manager: manager, Store: store, AgentReadiness: readiness})
			_, _, _, err := service.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "project", Kind: domain.KindWorker, Harness: domain.HarnessCodex, AccountID: selection, DisplayName: "worker"})
			if installed == domain.AgentInstallationNotInstalled {
				var apiError *apierr.Error
				if !errors.As(err, &apiError) || apiError.Code != "AGENT_BINARY_NOT_FOUND" || manager.spawnCalls != 0 {
					t.Fatalf("missing binary: err=%v spawns=%d", err, manager.spawnCalls)
				}
			} else if err != nil || manager.spawnCalls != 1 || manager.spawnedCfg.AccountID != selection || readiness.purpose != domain.AgentReadinessPurposeLaunch {
				t.Fatalf("selection %q: err=%v cfg=%+v", selection, err, manager.spawnedCfg)
			}
		}
	}
}

func TestAccountErrorsFromALaunchKeepTheirAPICode(t *testing.T) {
	for _, failure := range []*apierr.Error{ports.ErrProviderLoginRequired, ports.ErrProviderAccountUnknown, ports.ErrProviderAccountIncompatible, ports.ErrProviderAccountBusy} {
		store := newFakeStore()
		store.projects["project"] = domain.ProjectRecord{ID: "project"}
		manager := &fakeCommander{spawnErr: fmt.Errorf("account admission: %w", failure)}
		service := NewWithDeps(Deps{Manager: manager, Store: store})
		_, _, _, spawnErr := service.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "project", Harness: domain.HarnessCodex, Kind: domain.KindWorker, AccountID: "selected", DisplayName: "worker"})
		outcome, delegateErr := service.DelegateTask(context.Background(), DelegateTaskInput{ProjectID: "project", RequestedAgent: domain.HarnessCodex, AccountID: "selected", Brief: "Fix a bug"})
		for _, err := range []error{spawnErr, delegateErr} {
			var apiError *apierr.Error
			if !errors.As(err, &apiError) || apiError.Code != failure.Code || apiError.Kind != failure.Kind {
				t.Fatalf("%s: err=%v", failure.Code, err)
			}
		}
		if !reflect.DeepEqual(outcome, DelegateTaskOutcome{}) || len(store.sessions) != 0 || len(manager.backgroundCalls) != 0 {
			t.Fatalf("%s: a refused launch left a worker behind", failure.Code)
		}
	}
}

func TestDelegateForwardsTheChosenAccountWithoutChangingTaskSettings(t *testing.T) {
	for _, selection := range []string{"", "account-a"} {
		store := newFakeStore()
		store.projects["project"] = domain.ProjectRecord{ID: "project"}
		manager := &fakeCommander{}
		service := NewWithDeps(Deps{Manager: manager, Store: store})
		effort := "high"
		outcome, err := service.DelegateTask(context.Background(), DelegateTaskInput{ProjectID: "project", RequestedAgent: domain.HarnessClaudeCode, AccountID: selection, RequestedMode: domain.SessionModeChat, TaskPreparation: "prepared-worktree", Model: "model-probe", Effort: &effort, ApprovalMode: domain.PermissionMode("auto")})
		if err != nil || outcome.WorkerID == "" {
			t.Fatalf("outcome=%+v err=%v", outcome, err)
		}
		cfg := manager.spawnedCfg
		if cfg.AccountID != selection || cfg.Harness != domain.HarnessClaudeCode || cfg.ProjectID != "project" || cfg.Kind != domain.KindWorker ||
			cfg.AgentConfig.Model != "model-probe" || cfg.AgentConfig.Effort != "high" || cfg.RequestedMode != domain.SessionModeChat || cfg.TaskPreparation != "prepared-worktree" || !cfg.Async {
			t.Fatalf("delegated launch=%+v", cfg)
		}
	}
}
