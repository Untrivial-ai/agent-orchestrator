package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ---- AgentRole CRUD tests (Phase 2.4) ----

func TestCreateAgentRole_Success(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	role, err := svc.CreateAgentRole(ctx, CreateAgentRoleInput{
		Name:         "coder",
		DisplayName:  "Coder",
		Description:  "Writes code",
		SystemPrompt: "You are a coder.",
	})
	if err != nil {
		t.Fatalf("CreateAgentRole: %v", err)
	}
	if role.Name != "coder" {
		t.Errorf("expected name 'coder', got %q", role.Name)
	}
	if role.SystemPrompt != "You are a coder." {
		t.Errorf("expected system prompt, got %q", role.SystemPrompt)
	}
	if !role.Enabled {
		t.Error("expected enabled=true")
	}
}

func TestCreateAgentRole_EmptyName_ErrInvalidInput(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateAgentRole(ctx, CreateAgentRoleInput{Name: ""})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
}

func TestCreateAgentRole_WhitespaceOnlyName_ErrInvalidInput(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateAgentRole(ctx, CreateAgentRoleInput{Name: "   "})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
}

func TestCreateAgentRole_PartialProviderPair_ErrInvalidInput(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateAgentRole(ctx, CreateAgentRoleInput{
		Name:              "partial",
		DefaultProviderID: "openai",
		// DefaultProviderModelID empty → partial pair → fail
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput for partial pair, got %v", err)
	}
}

func TestCreateAgentRole_PartialModelOnly_ErrInvalidInput(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateAgentRole(ctx, CreateAgentRoleInput{
		Name:                   "partial-model",
		DefaultProviderModelID: "gpt-4o",
		// DefaultProviderID empty → partial pair → fail
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput for partial pair, got %v", err)
	}
}

func TestCreateAgentRole_EmptyPair_Unconfigured(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	role, err := svc.CreateAgentRole(ctx, CreateAgentRoleInput{
		Name: "no-provider",
	})
	if err != nil {
		t.Fatalf("CreateAgentRole with empty pair: %v", err)
	}
	if role.DefaultProviderID != "" || role.DefaultProviderModelID != "" {
		t.Error("expected empty provider pair for unconfigured role")
	}
}

func TestGetAgentRole_Success(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	created, _ := svc.CreateAgentRole(ctx, CreateAgentRoleInput{Name: "fetcher"})
	got, err := svc.GetAgentRole(ctx, domain.AgentRoleID(created.ID))
	if err != nil {
		t.Fatalf("GetAgentRole: %v", err)
	}
	if got.Name != "fetcher" {
		t.Errorf("expected 'fetcher', got %q", got.Name)
	}
}

func TestGetAgentRole_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.GetAgentRole(ctx, "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestListAgentRoles(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	svc.CreateAgentRole(ctx, CreateAgentRoleInput{Name: "role-a"})
	svc.CreateAgentRole(ctx, CreateAgentRoleInput{Name: "role-b"})

	roles, err := svc.ListAgentRoles(ctx)
	if err != nil {
		t.Fatalf("ListAgentRoles: %v", err)
	}
	if len(roles) != 2 {
		t.Errorf("expected 2 roles, got %d", len(roles))
	}
}

func TestListAgentRoles_Empty(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	roles, err := svc.ListAgentRoles(ctx)
	if err != nil {
		t.Fatalf("ListAgentRoles: %v", err)
	}
	if len(roles) != 0 {
		t.Errorf("expected 0 roles, got %d", len(roles))
	}
}

func TestUpdateAgentRole_PATCHMerge(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	created, _ := svc.CreateAgentRole(ctx, CreateAgentRoleInput{
		Name:         "updater",
		DisplayName:  "Original Display",
		Description:  "Original Desc",
		SystemPrompt: "Original prompt",
	})

	newDisplay := "New Display"
	updated, err := svc.UpdateAgentRole(ctx, domain.AgentRoleID(created.ID), UpdateAgentRoleInput{
		DisplayName: &newDisplay,
	})
	if err != nil {
		t.Fatalf("UpdateAgentRole: %v", err)
	}
	if updated.DisplayName != "New Display" {
		t.Errorf("expected 'New Display', got %q", updated.DisplayName)
	}
	// Unchanged fields preserved
	if updated.Description != "Original Desc" {
		t.Errorf("expected 'Original Desc', got %q", updated.Description)
	}
	if updated.SystemPrompt != "Original prompt" {
		t.Errorf("expected 'Original prompt', got %q", updated.SystemPrompt)
	}
}

func TestUpdateAgentRole_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	disp := "x"
	_, err := svc.UpdateAgentRole(ctx, "nonexistent", UpdateAgentRoleInput{DisplayName: &disp})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateAgentRole_MergedPartialPair_ErrInvalidInput(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	// Create with no provider pair
	created, _ := svc.CreateAgentRole(ctx, CreateAgentRoleInput{Name: "merge-pair"})

	// Patch only provider (not model) → merged pair is partial → ErrInvalidInput
	pid := domain.ProviderID("openai")
	_, err := svc.UpdateAgentRole(ctx, domain.AgentRoleID(created.ID), UpdateAgentRoleInput{
		DefaultProviderID: &pid,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput for merged partial pair, got %v", err)
	}
}

func TestUpdateAgentRole_ClearProvider(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	created, _ := svc.CreateAgentRole(ctx, CreateAgentRoleInput{Name: "clear-test"})

	emptyPID := domain.ProviderID("")
	updated, err := svc.UpdateAgentRole(ctx, domain.AgentRoleID(created.ID), UpdateAgentRoleInput{
		DefaultProviderID: &emptyPID,
	})
	if err != nil {
		t.Fatalf("UpdateAgentRole: %v", err)
	}
	if updated.DefaultProviderID != "" {
		t.Errorf("expected empty provider, got %q", updated.DefaultProviderID)
	}
}

func TestSetAgentRoleEnabled_DisableAndReEnable(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	created, _ := svc.CreateAgentRole(ctx, CreateAgentRoleInput{Name: "toggle"})

	if err := svc.SetAgentRoleEnabled(ctx, domain.AgentRoleID(created.ID), false); err != nil {
		t.Fatalf("SetAgentRoleEnabled(false): %v", err)
	}
	role, _ := svc.GetAgentRole(ctx, domain.AgentRoleID(created.ID))
	if role.Enabled {
		t.Error("expected disabled after SetAgentRoleEnabled(false)")
	}

	if err := svc.SetAgentRoleEnabled(ctx, domain.AgentRoleID(created.ID), true); err != nil {
		t.Fatalf("SetAgentRoleEnabled(true): %v", err)
	}
	role, _ = svc.GetAgentRole(ctx, domain.AgentRoleID(created.ID))
	if !role.Enabled {
		t.Error("expected re-enabled after SetAgentRoleEnabled(true)")
	}
}

func TestSetAgentRoleEnabled_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	err := svc.SetAgentRoleEnabled(ctx, "nonexistent", true)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ---- Resolution tests (Phase 2.4) ----

func TestResolveRole_NoRoleID_ReturnsNil(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	task := domain.DevelopmentTask{AgentRoleID: ""}
	role, err := svc.resolveRole(ctx, task)
	if err != nil {
		t.Fatalf("resolveRole: %v", err)
	}
	if role != nil {
		t.Error("expected nil role when AgentRoleID is empty")
	}
}

func TestResolveRole_RoleNotFound_ErrNotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	task := domain.DevelopmentTask{AgentRoleID: "ghost"}
	_, err := svc.resolveRole(ctx, task)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestResolveRole_DisabledRole_ErrInvalidTransition(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	created, _ := svc.CreateAgentRole(ctx, CreateAgentRoleInput{Name: "disabled-role"})
	svc.SetAgentRoleEnabled(ctx, domain.AgentRoleID(created.ID), false)

	task := domain.DevelopmentTask{AgentRoleID: domain.AgentRoleID(created.ID)}
	_, err := svc.resolveRole(ctx, task)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestResolveRole_EnabledRole_ReturnsRole(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	created, _ := svc.CreateAgentRole(ctx, CreateAgentRoleInput{
		Name:         "active",
		SystemPrompt: "hello",
	})

	task := domain.DevelopmentTask{AgentRoleID: domain.AgentRoleID(created.ID)}
	role, err := svc.resolveRole(ctx, task)
	if err != nil {
		t.Fatalf("resolveRole: %v", err)
	}
	if role == nil {
		t.Fatal("expected non-nil role")
	}
	if role.Name != "active" {
		t.Errorf("expected 'active', got %q", role.Name)
	}
}

func TestResolveProvider_TaskExplicit(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	task := domain.DevelopmentTask{
		ProviderID:      "prov-1",
		ProviderModelID: "model-1",
	}
	// Provider validation will fail (not in store), but this tests that
	// task-level pair is tried first (not empty = not system default).
	_, _, err := svc.resolveProvider(ctx, task, nil)
	// Expect error from validateProviderPair (store doesn't have prov-1)
	if err == nil {
		t.Fatal("expected validation error for non-existent provider")
	}
}

func TestResolveProvider_EmptyAll_SystemDefault(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	task := domain.DevelopmentTask{} // no task-level provider
	pid, mid, err := svc.resolveProvider(ctx, task, nil)
	if err != nil {
		t.Fatalf("resolveProvider system default: %v", err)
	}
	if pid != "" || mid != "" {
		t.Errorf("expected empty system default, got %q/%q", pid, mid)
	}
}

func TestResolveProvider_PartialTaskPair_ErrInvalidInput(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	task := domain.DevelopmentTask{
		ProviderID: "openai",
		// ModelID empty → partial
	}
	_, _, err := svc.resolveProvider(ctx, task, nil)
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
}

// ---- pairOrEmpty helper tests ----

func TestPairOrEmpty_BothEmpty(t *testing.T) {
	pp := pairOrEmpty("", "")
	if !pp.empty {
		t.Error("expected empty=true")
	}
}

func TestPairOrEmpty_BothSet(t *testing.T) {
	pp := pairOrEmpty("p", "m")
	if !pp.complete {
		t.Error("expected complete=true")
	}
}

func TestPairOrEmpty_PartialProviderOnly(t *testing.T) {
	pp := pairOrEmpty("p", "")
	if !pp.partial {
		t.Error("expected partial=true")
	}
}

func TestPairOrEmpty_PartialModelOnly(t *testing.T) {
	pp := pairOrEmpty("", "m")
	if !pp.partial {
		t.Error("expected partial=true")
	}
}

// ---- AssignTask role validation ----

func TestAssignTask_RoleNotFound(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	_, err := svc.AssignTask(ctx, task.ID, "ghost-role", "", "")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestAssignTask_RoleExists_Success(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	role, _ := svc.CreateAgentRole(ctx, CreateAgentRoleInput{Name: "assignee"})

	updated, err := svc.AssignTask(ctx, task.ID, domain.AgentRoleID(role.ID), "", "")
	if err != nil {
		t.Fatalf("AssignTask: %v", err)
	}
	if updated.AgentRoleID != domain.AgentRoleID(role.ID) {
		t.Errorf("expected role %q, got %q", role.ID, updated.AgentRoleID)
	}
}

func TestAssignTask_PartialProviderPair_ErrInvalidInput(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	_, err := svc.AssignTask(ctx, task.ID, "", "openai", "")
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
}

func TestAssignTask_EmptyAssignment_Success(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	updated, err := svc.AssignTask(ctx, task.ID, "", "", "")
	if err != nil {
		t.Fatalf("AssignTask empty: %v", err)
	}
	if updated.AgentRoleID != "" || updated.ProviderID != "" || updated.ProviderModelID != "" {
		t.Error("expected empty assignment fields")
	}
}
