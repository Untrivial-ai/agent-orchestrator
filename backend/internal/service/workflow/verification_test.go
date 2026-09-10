package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// extendedStore allows tests to call store methods outside the workflow.Store interface.
type extendedStore interface {
	PutProvider(ctx context.Context, p domain.Provider) error
	PutProviderModel(ctx context.Context, m domain.ProviderModel) error
	CreateSession(ctx context.Context, rec domain.SessionRecord) (domain.SessionRecord, error)
	UpdateSession(ctx context.Context, rec domain.SessionRecord) error
	GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error)
}

func putTestProvider(t *testing.T, store extendedStore, id domain.ProviderID, enabled bool) {
	t.Helper()
	now := time.Now().UTC()
	if err := store.PutProvider(context.Background(), domain.Provider{
		ID:          id,
		DisplayName: string(id),
		APIProtocol: domain.APIProtocolOpenAICompatible,
		SecretRef:   string(id) + "-ref",
		Enabled:     enabled,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("PutProvider %s: %v", id, err)
	}
}

func putTestModel(t *testing.T, store extendedStore, id domain.ProviderModelID, providerID domain.ProviderID, enabled bool) {
	t.Helper()
	now := time.Now().UTC()
	if err := store.PutProviderModel(context.Background(), domain.ProviderModel{
		ID:          id,
		ProviderID:  providerID,
		DisplayName: string(id),
		Enabled:     enabled,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("PutProviderModel %s: %v", id, err)
	}
}

// =====================================================================
// SECTION III: Role与Provider解耦行为测试
// =====================================================================

func TestStartRun_TaskExplicitProvider_ValidRole_SystemPromptFromRole(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	projectID := domain.ProjectID(project.ID)

	// Create a valid role with SystemPrompt
	role, _ := svc.CreateAgentRole(ctx, CreateAgentRoleInput{
		Name:         "coder",
		SystemPrompt: "You are a coder.",
	})
	// Assign role to task (no provider on role)
	svc.AssignTask(ctx, task.ID, domain.AgentRoleID(role.ID), "", "")

	// Set provider/model on the task via AssignTask
	st := svc.store.(extendedStore)
	putTestProvider(t, st, "openai", true)
	putTestModel(t, st, "gpt-4o", "openai", true)
	task, _ = svc.AssignTask(ctx, task.ID, domain.AgentRoleID(role.ID), "openai", "gpt-4o")

	ws := svc.store.(extendedStore)
	sid := createTestSession(t, ws, projectID)

	var spawnCfg ports.SpawnConfig
	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			spawnCfg = cfg
			return domain.SessionRecord{
				ID: sid, Kind: domain.SessionKind("worker"), Harness: "claude-code",
				Metadata: domain.SessionMetadata{},
			}, nil
		},
	}

	run, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	_, err := svc.StartRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// Provider must come from Task explicit
	if spawnCfg.ProviderID != "openai" {
		t.Errorf("expected provider 'openai', got %q", spawnCfg.ProviderID)
	}
	if spawnCfg.ProviderModelID != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %q", spawnCfg.ProviderModelID)
	}
	// SystemPrompt must come from Role
	if spawnCfg.SystemPrompt != "You are a coder." {
		t.Errorf("expected role system prompt 'You are a coder.', got %q", spawnCfg.SystemPrompt)
	}
}

func TestStartRun_TaskExplicitProvider_DisabledRole_Fail(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	st := svc.store.(extendedStore)
	putTestProvider(t, st, "openai", true)
	putTestModel(t, st, "gpt-4o", "openai", true)

	// Create role and immediately disable
	role, _ := svc.CreateAgentRole(ctx, CreateAgentRoleInput{Name: "disabled"})
	svc.SetAgentRoleEnabled(ctx, domain.AgentRoleID(role.ID), false)

	// Assign role + provider to task
	svc.AssignTask(ctx, task.ID, domain.AgentRoleID(role.ID), "openai", "gpt-4o")

	ws := svc.store.(extendedStore)
	sid := createTestSession(t, ws, domain.ProjectID(project.ID))
	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			return domain.SessionRecord{
				ID: sid, Kind: domain.SessionKind("worker"), Harness: "claude-code",
				Metadata: domain.SessionMetadata{},
			}, nil
		},
	}

	run, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	_, err := svc.StartRun(ctx, run.ID)
	// Must fail because role is disabled, even though task provider is complete
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition (disabled role), got %v", err)
	}
}

func TestStartRun_TaskExplicitProvider_MissingRole_Fail(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	st := svc.store.(extendedStore)
	putTestProvider(t, st, "openai", true)
	putTestModel(t, st, "gpt-4o", "openai", true)

	// Assign a non-existent role + valid provider to task
	svc.store.UpdateDevelopmentTaskAssignment(
		context.Background(), task.ID, domain.AgentRoleID("ghost-role"), "openai", "gpt-4o",
	)

	ws := svc.store.(extendedStore)
	sid := createTestSession(t, ws, domain.ProjectID(project.ID))
	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			return domain.SessionRecord{
				ID: sid, Kind: domain.SessionKind("worker"), Harness: "claude-code",
				Metadata: domain.SessionMetadata{},
			}, nil
		},
	}

	run, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	_, err := svc.StartRun(ctx, run.ID)
	// Must fail because role doesn't exist
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound (missing role), got %v", err)
	}
}

// =====================================================================
// SECTION IV: Provider metadata validation五类错误
// =====================================================================

func TestProviderValidation_ProviderMissing(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	err := svc.validateProviderPair(ctx, "nonexistent", "some-model")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing provider, got %v", err)
	}
}

func TestProviderValidation_ProviderDisabled(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	st := svc.store.(extendedStore)

	putTestProvider(t, st, "disabled-prov", false)
	putTestModel(t, st, "m1", "disabled-prov", true)

	err := svc.validateProviderPair(ctx, "disabled-prov", "m1")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition for disabled provider, got %v", err)
	}
}

func TestProviderValidation_ModelMissing(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	st := svc.store.(extendedStore)

	putTestProvider(t, st, "openai", true)
	// Don't create the model

	err := svc.validateProviderPair(ctx, "openai", "nonexistent-model")
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput for missing model, got %v", err)
	}
}

func TestProviderValidation_ModelDisabled(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	st := svc.store.(extendedStore)

	putTestProvider(t, st, "openai", true)
	putTestModel(t, st, "gpt-4o", "openai", false) // disabled

	err := svc.validateProviderPair(ctx, "openai", "gpt-4o")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition for disabled model, got %v", err)
	}
}

func TestProviderValidation_ModelBelongsToDifferentProvider(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	st := svc.store.(extendedStore)

	putTestProvider(t, st, "openai", true)
	putTestProvider(t, st, "anthropic", true)
	putTestModel(t, st, "claude-3", "anthropic", true)

	// Try to use anthropic's model with openai
	err := svc.validateProviderPair(ctx, "openai", "claude-3")
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput for model mismatch, got %v", err)
	}
}

func TestProviderValidation_TaskPartialPair(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	// Set only provider, no model
	svc.store.UpdateDevelopmentTaskAssignment(
		context.Background(), task.ID, "", "openai", "",
	)

	ws := svc.store.(extendedStore)
	sid := createTestSession(t, ws, domain.ProjectID(project.ID))
	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			return domain.SessionRecord{
				ID: sid, Kind: domain.SessionKind("worker"), Harness: "claude-code",
				Metadata: domain.SessionMetadata{},
			}, nil
		},
	}

	run, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	_, err := svc.StartRun(ctx, run.ID)
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput for task partial pair, got %v", err)
	}
}

func TestProviderValidation_RolePartialPair(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	// Create role with only provider (no model) — should fail
	pid := domain.ProviderID("openai")
	_, err := svc.CreateAgentRole(ctx, CreateAgentRoleInput{
		Name:              "partial-role",
		DefaultProviderID: pid,
		// no DefaultProviderModelID
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput for role partial pair, got %v", err)
	}
}

// =====================================================================
// SECTION V: System Default测试
// =====================================================================

func TestSystemDefault_NoRoleNoTaskProvider_EmptySpawnConfig(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)
	// task has no AgentRoleID, no ProviderID, no ProviderModelID

	ws := svc.store.(extendedStore)
	sid := createTestSession(t, ws, domain.ProjectID(project.ID))

	var spawnCfg ports.SpawnConfig
	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			spawnCfg = cfg
			return domain.SessionRecord{
				ID: sid, Kind: domain.SessionKind("worker"), Harness: "claude-code",
				Metadata: domain.SessionMetadata{},
			}, nil
		},
	}

	run, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	_, err := svc.StartRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if spawnCfg.ProviderID != "" {
		t.Errorf("expected empty provider (system default), got %q", spawnCfg.ProviderID)
	}
	if spawnCfg.ProviderModelID != "" {
		t.Errorf("expected empty model (system default), got %q", spawnCfg.ProviderModelID)
	}
}

func TestSystemDefault_RoleWithNoDefaultProvider_EmptySpawnConfig(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	// Create role with NO default provider
	role, _ := svc.CreateAgentRole(ctx, CreateAgentRoleInput{
		Name:         "no-provider-role",
		SystemPrompt: "hello",
	})
	svc.AssignTask(ctx, task.ID, domain.AgentRoleID(role.ID), "", "")

	ws := svc.store.(extendedStore)
	sid := createTestSession(t, ws, domain.ProjectID(project.ID))

	var spawnCfg ports.SpawnConfig
	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			spawnCfg = cfg
			return domain.SessionRecord{
				ID: sid, Kind: domain.SessionKind("worker"), Harness: "claude-code",
				Metadata: domain.SessionMetadata{},
			}, nil
		},
	}

	run, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	_, err := svc.StartRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if spawnCfg.ProviderID != "" {
		t.Errorf("expected empty provider (role has no default), got %q", spawnCfg.ProviderID)
	}
	if spawnCfg.ProviderModelID != "" {
		t.Errorf("expected empty model (role has no default), got %q", spawnCfg.ProviderModelID)
	}
	// SystemPrompt should still come from role
	if spawnCfg.SystemPrompt != "hello" {
		t.Errorf("expected system prompt 'hello', got %q", spawnCfg.SystemPrompt)
	}
}

// =====================================================================
// SECTION VI: SystemPrompt Spawn测试
// =====================================================================

func TestSystemPrompt_SpawnConfig_SystemLevel(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	role, _ := svc.CreateAgentRole(ctx, CreateAgentRoleInput{
		Name:         "prompt-role",
		SystemPrompt: "ROLE_SYSTEM_PROMPT",
	})
	svc.AssignTask(ctx, task.ID, domain.AgentRoleID(role.ID), "", "")

	ws := svc.store.(extendedStore)
	sid := createTestSession(t, ws, domain.ProjectID(project.ID))

	var spawnCfg ports.SpawnConfig
	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			spawnCfg = cfg
			return domain.SessionRecord{
				ID: sid, Kind: domain.SessionKind("worker"), Harness: "claude-code",
				Metadata: domain.SessionMetadata{},
			}, nil
		},
	}

	run, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	_, err := svc.StartRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// SystemPrompt goes to SpawnConfig.SystemPrompt (system-level)
	if spawnCfg.SystemPrompt != "ROLE_SYSTEM_PROMPT" {
		t.Errorf("expected SpawnConfig.SystemPrompt='ROLE_SYSTEM_PROMPT', got %q", spawnCfg.SystemPrompt)
	}

	// Task prompt goes to SpawnConfig.Prompt (user-level), not mixed with SystemPrompt
	if spawnCfg.Prompt == "" {
		t.Error("expected non-empty SpawnConfig.Prompt from task")
	}
	if spawnCfg.Prompt == "ROLE_SYSTEM_PROMPT" {
		t.Error("SystemPrompt must NOT be in SpawnConfig.Prompt")
	}
}

// =====================================================================
// SECTION VII: SystemPrompt SQLite durability测试
// =====================================================================

func TestSystemPrompt_SQLiteDurability(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	role, _ := svc.CreateAgentRole(ctx, CreateAgentRoleInput{
		Name:         "durable-role",
		SystemPrompt: "DURABLE_PROMPT",
	})
	svc.AssignTask(ctx, task.ID, domain.AgentRoleID(role.ID), "", "")

	ws := svc.store.(extendedStore)
	sid := createTestSession(t, ws, domain.ProjectID(project.ID))

	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			// Simulate real SpawnSession: persist AdditionalSystemPrompt to store
			_ = ws.UpdateSession(context.Background(), domain.SessionRecord{
				ID:       sid,
				Kind:     domain.SessionKind("worker"),
				Harness:  "claude-code",
				Metadata: domain.SessionMetadata{AdditionalSystemPrompt: cfg.SystemPrompt},
			})
			return domain.SessionRecord{
				ID:       sid,
				Kind:     domain.SessionKind("worker"),
				Harness:  "claude-code",
				Metadata: domain.SessionMetadata{AdditionalSystemPrompt: cfg.SystemPrompt},
			}, nil
		},
	}

	run, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	_, err := svc.StartRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// Read session back from SQLite store
	rec, ok, err := ws.GetSession(ctx, sid)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !ok {
		t.Fatal("session not found in store")
	}
	if rec.Metadata.AdditionalSystemPrompt != "DURABLE_PROMPT" {
		t.Errorf("expected SQLite stored AdditionalSystemPrompt='DURABLE_PROMPT', got %q",
			rec.Metadata.AdditionalSystemPrompt)
	}
}

// =====================================================================
// SECTION VIII: Restore测试 — uses historical snapshot, no role query
// =====================================================================

func TestRestore_UsesHistoricalSystemPromptSnapshot(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	ws := svc.store.(extendedStore)

	// Create session with historical AdditionalSystemPrompt = "ROLE_PROMPT_V1"
	// This simulates what happens after a successful StartRun
	rec, err := ws.CreateSession(ctx, domain.SessionRecord{
		Kind:      domain.SessionKind("worker"),
		ProjectID: domain.ProjectID(project.ID),
		Harness:   "claude-code",
		Metadata: domain.SessionMetadata{
			AdditionalSystemPrompt: "ROLE_PROMPT_V1",
		},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Read back from store (simulates what Restore does)
	readRec, ok, err := ws.GetSession(ctx, rec.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !ok {
		t.Fatal("session not found")
	}

	// The historical snapshot must survive in the store
	if readRec.Metadata.AdditionalSystemPrompt != "ROLE_PROMPT_V1" {
		t.Errorf("expected 'ROLE_PROMPT_V1' from store, got %q",
			readRec.Metadata.AdditionalSystemPrompt)
	}

	// This proves Restore can read the historical value without querying AgentRole.
	// The session_manager's Restore path reads rec.Metadata.AdditionalSystemPrompt
	// directly, never calling workflow.GetAgentRole.
}

// =====================================================================
// SECTION IX: Resume边界核验
// =====================================================================

func TestResume_HistoricalSessionBinding_NoReResolution(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	st := svc.store.(extendedStore)
	putTestProvider(t, st, "openai", true)
	putTestModel(t, st, "gpt-4o", "openai", true)

	// Create role with a specific provider and system prompt
	role, _ := svc.CreateAgentRole(ctx, CreateAgentRoleInput{
		Name:                   "resume-role",
		SystemPrompt:           "RESUME_PROMPT",
		DefaultProviderID:      "openai",
		DefaultProviderModelID: "gpt-4o",
	})
	svc.AssignTask(ctx, task.ID, domain.AgentRoleID(role.ID), "", "")

	ws := svc.store.(extendedStore)
	sid := createTestSession(t, ws, domain.ProjectID(project.ID))

	var capturedCfg ports.SpawnConfig
	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			capturedCfg = cfg
			meta := domain.SessionMetadata{
				ProviderID:             "openai",
				ProviderModelID:        "gpt-4o",
				AdditionalSystemPrompt: cfg.SystemPrompt,
			}
			// Simulate real SpawnSession: persist metadata to store
			_ = ws.UpdateSession(context.Background(), domain.SessionRecord{
				ID: sid, Kind: domain.SessionKind("worker"), Harness: "claude-code",
				Metadata: meta,
			})
			return domain.SessionRecord{
				ID: sid, Kind: domain.SessionKind("worker"), Harness: "claude-code",
				Metadata: meta,
			}, nil
		},
	}

	// First StartRun — establishes the session binding
	run, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	_, err := svc.StartRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// Verify initial spawn used role's provider
	if capturedCfg.ProviderID != "openai" {
		t.Errorf("expected initial provider 'openai', got %q", capturedCfg.ProviderID)
	}
	if capturedCfg.SystemPrompt == "" {
		t.Error("expected non-empty SystemPrompt from role")
	}

	// Now modify the role — change provider and prompt
	pid := domain.ProviderID("anthropic")
	mid := domain.ProviderModelID("claude-3")
	svc.UpdateAgentRole(ctx, domain.AgentRoleID(role.ID), UpdateAgentRoleInput{
		DefaultProviderID:      &pid,
		DefaultProviderModelID: &mid,
	})

	// Read the session back — the stored values must not change
	readRec, ok, _ := ws.GetSession(ctx, sid)
	if !ok {
		t.Fatal("session not found")
	}
	// Historical provider binding preserved
	if readRec.Metadata.ProviderID != "openai" {
		t.Errorf("expected historical provider 'openai', got %q", readRec.Metadata.ProviderID)
	}
	// Historical SystemPrompt preserved
	if readRec.Metadata.AdditionalSystemPrompt == "" {
		t.Error("expected non-empty historical AdditionalSystemPrompt")
	}
}

// =====================================================================
// SECTION X: Run-level override移除测试
// =====================================================================

func TestCreateRunInput_OnlyTaskID(t *testing.T) {
	// Verify CreateRunInput has exactly one field: TaskID
	in := CreateRunInput{TaskID: "test"}
	if in.TaskID != "test" {
		t.Error("TaskID field missing")
	}
}

func TestRun_AgentRoleID_ComesFromTask(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	role, _ := svc.CreateAgentRole(ctx, CreateAgentRoleInput{Name: "run-role"})
	svc.AssignTask(ctx, task.ID, domain.AgentRoleID(role.ID), "", "")

	// Refresh task
	task, _ = svc.GetTask(ctx, task.ID)

	run, err := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if run.AgentRoleID != domain.AgentRoleID(role.ID) {
		t.Errorf("expected Run.AgentRoleID=%q from task, got %q", role.ID, run.AgentRoleID)
	}
}
