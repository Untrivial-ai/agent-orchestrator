package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workerexec"
	"github.com/google/uuid"
)

func TestProjectSettingsStorePreservesOmissionsAndValidates(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	principal := domain.Principal{UserID: fixture.userID, Provider: "local"}
	if _, err := admin.Exec(ctx, `UPDATE ao_projects SET config = $2 WHERE id = $1`, fixture.projectID,
		`{"workerAgent":"codex","orchestratorAgent":"claude-code","worker":{"agentConfig":{"model":"worker-model","permissions":"auto"}},"coder":{"templateId":"keep"},"agentRules":"keep rules"}`); err != nil {
		t.Fatal(err)
	}
	patch, err := domain.ParseProjectSettingsPatch(json.RawMessage(`{"config":{"worker":{"agentConfig":{"effort":"max"}},"autoReview":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.UpdateProjectSettings(ctx, principal, fixture.orgID, fixture.projectID, patch)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := domain.DecodeProjectSettings(project.Config)
	if err != nil || settings.Worker.AgentConfig.Model != "worker-model" || settings.Worker.AgentConfig.Effort != "max" || settings.Worker.AgentConfig.Permissions != "auto" || settings.Orchestrator.Agent != "claude-code" || *settings.AutoReview {
		t.Fatalf("settings=%+v config=%s err=%v", settings, project.Config, err)
	}
	if project.DisplayName != "Project" || project.DefaultBranch != "main" || strings.Contains(string(project.Config), "workerAgent") || !strings.Contains(string(project.Config), `"templateId":"keep"`) {
		t.Fatalf("omitted values changed: %+v", project)
	}
	invalid := domain.ProjectSettingsPatch{Config: json.RawMessage(`{"worker":{"agentConfig":{"permissions":"invalid"}}}`)}
	if _, err := store.UpdateProjectSettings(ctx, principal, fixture.orgID, fixture.projectID, invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid config error=%v", err)
	}
	unchanged, err := store.GetProject(ctx, principal, fixture.orgID, fixture.projectID)
	if err != nil || !jsonEqual(unchanged.Config, project.Config) {
		t.Fatalf("failed write changed project: %s, %v", unchanged.Config, err)
	}
	if _, err := store.UpdateProjectSettings(ctx, principal, uuid.NewString(), fixture.projectID, domain.ProjectSettingsPatch{}); err == nil {
		t.Fatal("cross-organization settings write succeeded")
	}
}

func TestProjectSettingsStoreClearsRoleDefaults(t *testing.T) {
	for _, role := range []string{"worker", "orchestrator"} {
		t.Run(role, func(t *testing.T) {
			store, admin, fixture := openNotificationTestStore(t)
			ctx := context.Background()
			principal := domain.Principal{UserID: fixture.userID, Provider: "local"}
			if _, err := admin.Exec(ctx, `UPDATE ao_projects SET config = $2 WHERE id = $1`, fixture.projectID,
				`{"workerAgent":"codex","orchestratorAgent":"claude-code","worker":{"agentConfig":{"model":"worker-model","effort":"max","permissions":"auto"}},"orchestrator":{"agentConfig":{"model":"orchestrator-model","effort":"high"}},"coder":{"templateId":"keep"}}`); err != nil {
				t.Fatal(err)
			}
			patch, err := domain.ParseProjectSettingsPatch(json.RawMessage(`{"config":{"` + role + `":null}}`))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.UpdateProjectSettings(ctx, principal, fixture.orgID, fixture.projectID, patch); err != nil {
				t.Fatal(err)
			}
			project, err := store.GetProject(ctx, principal, fixture.orgID, fixture.projectID)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(project.Config, &fields); err != nil {
				t.Fatal(err)
			}
			if _, exists := fields[role]; exists || strings.Contains(string(project.Config), role+"Agent") || string(fields["coder"]) != `{"templateId":"keep"}` {
				t.Fatalf("role reset not persisted or omitted config lost: %s", project.Config)
			}
			session, err := store.CreateSession(ctx, principal, fixture.orgID, uuid.NewString(), 10, domain.CreateSession{ProjectID: fixture.projectID, Kind: role, Harness: "opencode", DisplayName: role, Mode: "trusted"})
			if err != nil {
				t.Fatal(err)
			}
			launch, err := store.WorkerLaunchSpec(ctx, fixture.orgID, session.ID)
			if err != nil || launch.Harness != "opencode" || launch.Model != "" || !jsonEqual(launch.AgentConfig, json.RawMessage(`{}`)) {
				t.Fatalf("cleared defaults reached new session: %+v, %v", launch, err)
			}
		})
	}
}

func TestConcurrentPartialProjectSettingsDoNotLoseValues(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	if _, err := admin.Exec(ctx, `UPDATE ao_projects SET config = '{"worker":{"agent":"codex"}}' WHERE id = $1`, fixture.projectID); err != nil {
		t.Fatal(err)
	}
	principal := domain.Principal{UserID: fixture.userID, Provider: "local"}
	var wait sync.WaitGroup
	errors := make(chan error, 2)
	for _, raw := range []string{`{"worker":{"agentConfig":{"model":"concurrent-model"}}}`, `{"worker":{"agentConfig":{"effort":"max"}}}`} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.UpdateProjectSettings(ctx, principal, fixture.orgID, fixture.projectID, domain.ProjectSettingsPatch{Config: json.RawMessage(raw)})
			errors <- err
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	project, err := store.GetProject(ctx, principal, fixture.orgID, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := domain.DecodeProjectSettings(project.Config)
	if err != nil || settings.Worker.AgentConfig.Model != "concurrent-model" || settings.Worker.AgentConfig.Effort != "max" {
		t.Fatalf("lost concurrent update: %s, %v", project.Config, err)
	}
}

func TestProjectRoleSettingsAreStampedAtSessionCreation(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	principal := domain.Principal{UserID: fixture.userID, Provider: "local"}
	if _, err := admin.Exec(ctx, `UPDATE ao_projects SET config = $2 WHERE id = $1`, fixture.projectID,
		`{"worker":{"agent":"codex","agentConfig":{"model":"worker-model","effort":"max","permissions":"accept-edits"}},"orchestrator":{"agent":"claude-code","agentConfig":{"model":"orchestrator-model","effort":"high","permissions":"auto"}}}`); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"worker", "orchestrator"} {
		harness, model := "codex", "worker-model"
		if kind == "orchestrator" {
			harness, model = "claude-code", "orchestrator-model"
		}
		session, err := store.CreateSession(ctx, principal, fixture.orgID, uuid.NewString(), 10, domain.CreateSession{ProjectID: fixture.projectID, Kind: kind, Harness: harness, DisplayName: kind, Mode: "trusted"})
		if err != nil {
			t.Fatal(err)
		}
		launch, err := store.WorkerLaunchSpec(ctx, fixture.orgID, session.ID)
		if err != nil || launch.Model != model {
			t.Fatalf("launch model=%s err=%v", launch.Model, err)
		}
		var config domain.ProjectAgentConfig
		if err := json.Unmarshal(launch.AgentConfig, &config); err != nil || config.Effort == "" || config.Permissions == "" {
			t.Fatalf("launch config=%s err=%v", launch.AgentConfig, err)
		}
	}
}

func TestReviewsSnapshotIndependentReviewerAndSeparateAutoReviewFromInjection(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	if _, err := admin.Exec(ctx, `UPDATE ao_projects SET config = $2 WHERE id = $1`, fixture.projectID,
		`{"reviewers":[{"harness":"claude-code","agentConfig":{"model":"reviewer-model","effort":"high","permissions":"auto"}}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE ao_sessions SET model = 'worker-model', auto_inject_review = false WHERE id = $1`, fixture.sessionID); err != nil {
		t.Fatal(err)
	}
	pr, err := store.CreatePullRequestRecord(ctx, fixture.orgID, fixture.sessionID, "github", "owner/repo", "author", 1, "https://github.test/owner/repo/pull/1", "feature", "main", "sha-1", "Review test", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	run, created, err := store.CreateReviewRun(ctx, fixture.orgID, pr.ID, fixture.sessionID, "sha-1")
	if err != nil || !created {
		t.Fatalf("autoReview default with autoInjectReview=false: created=%v err=%v", created, err)
	}
	// Changing project settings must not change the already-created run.
	if _, err := admin.Exec(ctx, `UPDATE ao_projects SET config = '{"autoReview":false,"reviewers":[{"harness":"codex"}]}' WHERE id = $1`, fixture.projectID); err != nil {
		t.Fatal(err)
	}
	if err := store.OpenReviewTerminal(ctx, fixture.orgID, fixture.sessionID, run.ID, "Review prompt"); err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	if err := admin.QueryRow(ctx, `SELECT payload FROM ao_worker_requests WHERE session_id = $1 AND kind = 'terminal.open'`, fixture.sessionID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var command worker.TerminalCommand
	if err := json.Unmarshal(raw, &command); err != nil || command.Kind != "reviewer" || command.ReviewRunID != run.ID || command.Reviewer == nil || command.Reviewer.Harness != "claude-code" || command.Reviewer.AgentConfig.Model != "reviewer-model" || command.Reviewer.AgentConfig.Effort != "high" || command.Reviewer.AgentConfig.Permissions != "auto" {
		t.Fatalf("review transport=%s err=%v", raw, err)
	}
	principal := domain.Principal{UserID: fixture.userID, Provider: "local"}
	if _, err := store.UpsertProviderConnection(ctx, principal, fixture.orgID, "claude-code", "default", []byte("ciphertext"), []byte("nonce"), json.RawMessage(`{"credentialType":"api_key"}`)); err != nil {
		t.Fatal(err)
	}
	credential, err := store.WorkerReviewCredential(ctx, fixture.orgID, fixture.sessionID, fixture.workerID, fixture.epoch, run.ID)
	if err != nil || credential.Provider != "claude-code" {
		t.Fatalf("review credential provider=%s err=%v", credential.Provider, err)
	}
	if _, err := store.WorkerReviewCredential(ctx, fixture.orgID, fixture.sessionID, fixture.workerID, fixture.epoch, uuid.NewString()); err == nil {
		t.Fatal("credential lookup accepted an unrelated review")
	}
	if _, created, err := store.CreateReviewRun(ctx, fixture.orgID, pr.ID, fixture.sessionID, "sha-2"); err != nil || created {
		t.Fatalf("autoReview=false created=%v err=%v", created, err)
	}
}

func TestReviewWithoutSettingsUsesSessionAgent(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	if _, err := admin.Exec(ctx, `UPDATE ao_sessions SET model = 'worker-model', mode = 'trusted', agent_config = '{"effort":"max"}' WHERE id = $1`, fixture.sessionID); err != nil {
		t.Fatal(err)
	}
	pr, err := store.CreatePullRequestRecord(ctx, fixture.orgID, fixture.sessionID, "github", "owner/repo", "author", 2, "https://github.test/owner/repo/pull/2", "feature", "main", "sha-1", "Review test", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	run, created, err := store.CreateReviewRun(ctx, fixture.orgID, pr.ID, fixture.sessionID, "sha-1")
	if err != nil || !created {
		t.Fatalf("review=%+v created=%v err=%v", run, created, err)
	}
	var reviewer domain.ProjectReviewer
	var raw json.RawMessage
	if err := admin.QueryRow(ctx, `SELECT reviewer_config FROM ao_review_runs WHERE id = $1`, run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &reviewer); err != nil || reviewer.Harness != "codex" || reviewer.AgentConfig.Model != "worker-model" || reviewer.AgentConfig.Effort != "max" || reviewer.AgentConfig.Permissions != "bypass-permissions" {
		t.Fatalf("fallback=%s err=%v", raw, err)
	}
	_, created, err = store.CreateReviewRun(ctx, fixture.orgID, pr.ID, fixture.sessionID, "sha-1")
	if err != nil || created {
		t.Fatalf("duplicate review created=%v err=%v", created, err)
	}
}

func TestLegacyReviewSnapshotAndFailedReviewerLaunch(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	pr, err := store.CreatePullRequestRecord(ctx, fixture.orgID, fixture.sessionID, "github", "owner/repo", "author", 3, "https://github.test/owner/repo/pull/3", "feature", "main", "sha-1", "Legacy review", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.CreateReviewRun(ctx, fixture.orgID, pr.ID, fixture.sessionID, "sha-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE ao_review_runs SET reviewer_config = '{}' WHERE id = $1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE ao_projects SET config = '{"reviewers":[{"harness":"claude-code"}]}' WHERE id = $1`, fixture.projectID); err != nil {
		t.Fatal(err)
	}
	if err := store.OpenReviewTerminal(ctx, fixture.orgID, fixture.sessionID, run.ID, "Review prompt"); err != nil {
		t.Fatal(err)
	}
	request, found, err := store.ClaimWorkerRequest(ctx, fixture.orgID, fixture.sessionID, fixture.workerID, fixture.epoch, time.Minute)
	if err != nil || !found || request.Kind != "terminal.open" {
		t.Fatalf("open request=%+v found=%v err=%v", request, found, err)
	}
	var command worker.TerminalCommand
	if err := json.Unmarshal(request.Payload, &command); err != nil || command.Reviewer.Harness != "codex" {
		t.Fatalf("legacy reviewer should keep session agent: %s, %v", request.Payload, err)
	}
	if err := store.FailWorkerRequest(ctx, fixture.orgID, fixture.sessionID, fixture.workerID, request.ID, fixture.epoch, request.Attempt, "WORKER_ERROR", "Reviewer credential unavailable"); err != nil {
		t.Fatal(err)
	}
	var status, lastError, reviewState string
	if err := admin.QueryRow(ctx, `SELECT run.status, run.last_error, pr.ao_review_state FROM ao_review_runs run JOIN ao_pull_requests pr ON pr.id = run.pull_request_id WHERE run.id = $1`, run.ID).Scan(&status, &lastError, &reviewState); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || lastError != "Reviewer credential unavailable" || reviewState != "needs_review" {
		t.Fatalf("review failure not surfaced: %s, %s, %s", status, lastError, reviewState)
	}
}

// Exercise the settings write, durable launch and provider process boundaries
// together. The executable records argv so this never invokes a paid model.
func TestSavedProjectSettingsReachWorkerOrchestratorAndReviewerProcesses(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	principal := domain.Principal{UserID: fixture.userID, Provider: "local"}
	patch, err := domain.ParseProjectSettingsPatch(json.RawMessage(`{"config":{"worker":{"agent":"codex","agentConfig":{"model":"gpt-6-luna","effort":"medium","permissions":"auto"}},"orchestrator":{"agent":"codex","agentConfig":{"model":"gpt-6.1-sol","effort":"low","permissions":"auto"}},"reviewers":[{"harness":"claude-code","agentConfig":{"model":"sonnet","effort":"high","permissions":"auto"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateProjectSettings(ctx, principal, fixture.orgID, fixture.projectID, patch); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	recorder := filepath.Join(t.TempDir(), "record-agent-args")
	if err := os.WriteFile(recorder, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	checkLaunch := func(kind, harness, model, effort string, launch worker.LaunchContext) {
		t.Helper()
		if launch.Harness != harness || launch.Model != model || launch.AgentConfig.Effort != effort {
			t.Fatalf("%s launch differs from saved settings: %+v", kind, launch)
		}
		credential := worker.CredentialResponse{Provider: harness, CredentialType: "auth_json", Secret: `{"tokens":{"access_token":"fixture"}}`}
		if harness == "claude-code" {
			credential.CredentialType, credential.Secret = "api_key", "fixture"
		}
		command, err := (workerexec.HarnessBuilder{DataDir: t.TempDir(), ConfigRoot: t.TempDir(), Binaries: map[string]string{harness: recorder}}).BuildInteractive(launch, credential, workspace)
		if err != nil {
			t.Fatal(err)
		}
		if command.Cleanup != nil {
			defer command.Cleanup()
		}
		process := exec.CommandContext(ctx, command.Path, command.Args...)
		process.Dir = command.Dir
		output, err := process.Output()
		if err != nil {
			t.Fatalf("%s process: %v", kind, err)
		}
		args := strings.Split(strings.TrimSpace(string(output)), "\n")
		modelIndex := slices.Index(args, "--model")
		if modelIndex < 0 || modelIndex+1 >= len(args) || args[modelIndex+1] != model {
			t.Fatalf("%s launched wrong model: %q", kind, args)
		}
		effortFlag := `model_reasoning_effort="` + effort + `"`
		if harness == "claude-code" {
			effortIndex := slices.Index(args, "--effort")
			if effortIndex < 0 || effortIndex+1 >= len(args) || args[effortIndex+1] != effort {
				t.Fatalf("%s launched wrong effort: %q", kind, args)
			}
		} else if !slices.Contains(args, effortFlag) {
			t.Fatalf("%s launched wrong effort: %q", kind, args)
		}
		t.Logf("%s process received harness=%s model=%s effort=%s", kind, harness, model, effort)
	}
	for _, role := range []struct{ kind, model, effort string }{{"worker", "gpt-6-luna", "medium"}, {"orchestrator", "gpt-6.1-sol", "low"}} {
		session, err := store.CreateSession(ctx, principal, fixture.orgID, uuid.NewString(), 10, domain.CreateSession{ProjectID: fixture.projectID, Kind: role.kind, DisplayName: role.kind, Mode: "standard"})
		if err != nil {
			t.Fatal(err)
		}
		spec, err := store.WorkerLaunchSpec(ctx, fixture.orgID, session.ID)
		if err != nil {
			t.Fatal(err)
		}
		var config domain.ProjectAgentConfig
		if err := json.Unmarshal(spec.AgentConfig, &config); err != nil {
			t.Fatal(err)
		}
		checkLaunch(role.kind, "codex", role.model, role.effort, worker.LaunchContext{SessionID: session.ID, Kind: role.kind, Harness: spec.Harness, Mode: spec.Mode, Model: spec.Model, AgentConfig: config})
	}
	pr, err := store.CreatePullRequestRecord(ctx, fixture.orgID, fixture.sessionID, "github", "owner/repo", "author", 1, "https://github.test/owner/repo/pull/1", "feature", "main", "settings-sha", "Settings launch", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	run, created, err := store.CreateReviewRun(ctx, fixture.orgID, pr.ID, fixture.sessionID, "settings-sha")
	if err != nil || !created {
		t.Fatalf("review creation: created=%v err=%v", created, err)
	}
	if err := store.OpenReviewTerminal(ctx, fixture.orgID, fixture.sessionID, run.ID, "Review"); err != nil {
		t.Fatal(err)
	}
	var payload json.RawMessage
	if err := admin.QueryRow(ctx, `SELECT payload FROM ao_worker_requests WHERE session_id = $1 AND kind = 'terminal.open'`, fixture.sessionID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var terminal worker.TerminalCommand
	if err := json.Unmarshal(payload, &terminal); err != nil || terminal.Reviewer == nil {
		t.Fatalf("review transport: %s, %v", payload, err)
	}
	checkLaunch("reviewer", "claude-code", "sonnet", "high", worker.LaunchContext{SessionID: run.ID, Kind: "reviewer", Harness: terminal.Reviewer.Harness, Mode: "standard", Model: terminal.Reviewer.AgentConfig.Model, AgentConfig: terminal.Reviewer.AgentConfig})
}
