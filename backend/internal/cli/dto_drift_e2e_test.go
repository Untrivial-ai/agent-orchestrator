package cli

// dto_drift_e2e_test.go is the DTO-drift guard for the `ao spawn` and
// `ao project add` commands. The CLI defines its OWN request structs
// (spawnRequest in spawn.go, addProjectRequest in project.go) that are separate
// copies of the daemon's canonical request DTOs (controllers.SpawnSessionRequest
// and project.AddInput). Nothing else verifies the two sides agree on JSON field
// names — a renamed `json:"..."` tag on either side compiles fine but silently
// breaks at runtime.
//
// This test stands up the REAL daemon HTTP router + REAL controllers (with fakes
// only BELOW the controller, at the service layer) and drives the actual CLI
// commands through the actual postJSON client over a real loopback HTTP round
// trip. If the CLI's JSON field names diverge from what the controllers decode,
// the captured values are wrong/empty and the subtests fail.
//
// (This lives in a separate file from the build-tagged e2e_test.go so it runs in
// the normal `go test ./...` lane — it binds no extra ports beyond httptest and
// spawns no processes.)

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
	agentsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/agent"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
)

// fakeSessionService captures the ports.SpawnConfig the controller decodes from
// the CLI's request body. Every other method is a no-op so it satisfies the
// controllers.SessionService interface.
type fakeSessionService struct {
	spawned ports.SpawnConfig
}

var _ controllers.SessionService = (*fakeSessionService)(nil)

func (f *fakeSessionService) List(context.Context, sessionsvc.ListFilter) ([]domain.Session, error) {
	return nil, nil
}

func (f *fakeSessionService) Spawn(_ context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
	f.spawned = cfg
	return domain.Session{
		SessionRecord: domain.SessionRecord{ID: domain.SessionID(string(cfg.ProjectID) + "-1")},
		Status:        domain.StatusIdle,
	}, len(cfg.Prompt), 0, nil
}

func (f *fakeSessionService) SpawnOrchestrator(ctx context.Context, projectID domain.ProjectID, _ bool, _ domain.SessionMode) (domain.Session, error) {
	s, _, _, err := f.Spawn(ctx, ports.SpawnConfig{ProjectID: projectID, Kind: domain.KindOrchestrator})
	return s, err
}

func (f *fakeSessionService) DelegateTask(context.Context, sessionsvc.DelegateTaskInput) (sessionsvc.DelegateTaskOutcome, error) {
	return sessionsvc.DelegateTaskOutcome{}, nil
}

func (f *fakeSessionService) Get(context.Context, domain.SessionID) (domain.Session, error) {
	return domain.Session{}, nil
}

func (f *fakeSessionService) Restore(context.Context, domain.SessionID) (sessionsvc.RestoreOutcome, error) {
	return sessionsvc.RestoreOutcome{}, nil
}

func (f *fakeSessionService) ResumeAgent(context.Context, domain.SessionID) (sessionsvc.ResumeAgentOutcome, error) {
	return sessionsvc.ResumeAgentOutcome{}, nil
}

func (f *fakeSessionService) Kill(context.Context, domain.SessionID) (bool, error) {
	return false, nil
}

func (f *fakeSessionService) RollbackSpawn(context.Context, domain.SessionID) (sessionsvc.RollbackOutcome, error) {
	return sessionsvc.RollbackOutcome{}, nil
}

func (f *fakeSessionService) Cleanup(context.Context, domain.ProjectID) (sessionsvc.CleanupOutcome, error) {
	return sessionsvc.CleanupOutcome{}, nil
}

func (f *fakeSessionService) Rename(context.Context, domain.SessionID, string) error {
	return nil
}

func (f *fakeSessionService) SetPreview(context.Context, domain.SessionID, string) (domain.Session, error) {
	return domain.Session{}, nil
}

func (f *fakeSessionService) SetTerminateOnPRMerge(context.Context, domain.SessionID, bool) (domain.Session, error) {
	return domain.Session{}, nil
}

func (f *fakeSessionService) Pin(context.Context, domain.SessionID) (domain.Session, error) {
	return domain.Session{}, nil
}

func (f *fakeSessionService) Unpin(context.Context, domain.SessionID) (domain.Session, error) {
	return domain.Session{}, nil
}

func (f *fakeSessionService) SetReviewerHarness(context.Context, domain.SessionID, domain.ReviewerHarness) (domain.Session, error) {
	return domain.Session{}, nil
}

func (f *fakeSessionService) Send(context.Context, domain.SessionID, string) error {
	return nil
}

func (f *fakeSessionService) ListPRSummaries(context.Context, domain.SessionID) ([]sessionsvc.PRSummary, error) {
	return nil, nil
}

func (f *fakeSessionService) ClaimPR(context.Context, domain.SessionID, string, sessionsvc.ClaimPROptions) (sessionsvc.ClaimPRResult, error) {
	return sessionsvc.ClaimPRResult{}, nil
}

func (f *fakeSessionService) ListWorkspaceFiles(context.Context, domain.SessionID) (sessionsvc.WorkspaceFiles, error) {
	return sessionsvc.WorkspaceFiles{}, nil
}

func (f *fakeSessionService) WorkspaceWatchPaths(context.Context, domain.SessionID) ([]string, error) {
	return nil, nil
}

func (f *fakeSessionService) GetWorkspaceFile(context.Context, domain.SessionID, string) (sessionsvc.WorkspaceFileDetail, error) {
	return sessionsvc.WorkspaceFileDetail{}, nil
}

func (f *fakeSessionService) StageAttachments(context.Context, domain.SessionID, []ports.SpawnAttachment) ([]string, error) {
	return nil, nil
}

type fakeAgentCatalog struct{}

var _ controllers.AgentCatalog = (*fakeAgentCatalog)(nil)

func (f *fakeAgentCatalog) List(context.Context) (agentsvc.Inventory, error) {
	return authorizedCodexInventory(), nil
}

func (f *fakeAgentCatalog) Refresh(context.Context) (agentsvc.Inventory, error) {
	return authorizedCodexInventory(), nil
}

func (f *fakeAgentCatalog) Probe(_ context.Context, agentID string) (agentsvc.ProbeResult, error) {
	info := agentsvc.Info{ID: agentID, Label: agentID, AuthStatus: "authorized"}
	return agentsvc.ProbeResult{Agent: info, Supported: true, Installed: true}, nil
}

func (f *fakeAgentCatalog) Models(_ context.Context, agentID, _ string, _ bool) (ports.AgentModelCatalog, error) {
	return ports.AgentModelCatalog{
		AgentID:       agentID,
		SelectionMode: ports.ModelSelectionText,
		Models:        []ports.AgentModelInfo{},
		AllowCustom:   true,
		Source:        "test",
	}, nil
}

func (f *fakeAgentCatalog) RevalidateModels(ctx context.Context, agentID, projectID string) (ports.AgentModelCatalog, error) {
	return f.Models(ctx, agentID, projectID, false)
}

func authorizedCodexInventory() agentsvc.Inventory {
	info := agentsvc.Info{ID: "codex", Label: "Codex", AuthStatus: "authorized"}
	return agentsvc.Inventory{
		Supported:  []agentsvc.Info{info},
		Installed:  []agentsvc.Info{info},
		Authorized: []agentsvc.Info{info},
	}
}

// fakeProjectManager captures the project.AddInput the controller decodes from
// the CLI's request body. Every other method is a no-op so it satisfies the
// projectsvc.Manager interface.
type fakeProjectManager struct {
	added projectsvc.AddInput
}

var _ projectsvc.Manager = (*fakeProjectManager)(nil)

func (f *fakeProjectManager) List(context.Context) ([]projectsvc.Summary, error) {
	return nil, nil
}

func (f *fakeProjectManager) Get(_ context.Context, id domain.ProjectID) (projectsvc.GetResult, error) {
	project := projectsvc.Project{ID: id, Path: "/repo/" + string(id)}
	return projectsvc.GetResult{Status: "ok", Project: &project}, nil
}

func (f *fakeProjectManager) Add(_ context.Context, in projectsvc.AddInput) (projectsvc.Project, error) {
	f.added = in
	id := domain.ProjectID("demo")
	if in.ProjectID != nil {
		id = domain.ProjectID(*in.ProjectID)
	}
	return projectsvc.Project{ID: id, Path: in.Path}, nil
}

func (f *fakeProjectManager) InitializeRepository(_ context.Context, in projectsvc.InitializeRepositoryInput) (projectsvc.InitializeRepositoryResult, error) {
	return projectsvc.InitializeRepositoryResult(in), nil
}

func (f *fakeProjectManager) UpdateSettings(_ context.Context, id domain.ProjectID, in projectsvc.UpdateSettingsInput) (projectsvc.Project, error) {
	cfg := in.Config
	return projectsvc.Project{ID: id, Name: in.DisplayName, Config: &cfg}, nil
}

func (f *fakeProjectManager) SetConfig(_ context.Context, id domain.ProjectID, in projectsvc.SetConfigInput) (projectsvc.Project, error) {
	cfg := in.Config
	return projectsvc.Project{ID: id, Config: &cfg}, nil
}

func (f *fakeProjectManager) Remove(context.Context, domain.ProjectID) (projectsvc.RemoveResult, error) {
	return projectsvc.RemoveResult{}, nil
}

// startDriftTestDaemon stands up the real router+controllers backed by the
// supplied fakes and points the CLI's run-file at it. The CLI discovers the
// server purely via AO_RUN_FILE + the run-file port, so this is a genuine
// loopback round trip through postJSON.
func startDriftTestDaemon(t *testing.T, sessions controllers.SessionService, projects projectsvc.Manager) {
	t.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Agents:   &fakeAgentCatalog{},
		Sessions: sessions,
		Projects: projects,
	}, httpd.ControlDeps{})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	port := srv.Listener.Addr().(*net.TCPAddr).Port

	rfPath := filepath.Join(t.TempDir(), "running.json")
	t.Setenv("AO_RUN_FILE", rfPath)
	if err := runfile.Write(rfPath, runfile.Info{PID: os.Getpid(), Port: port, StartedAt: time.Now()}); err != nil {
		t.Fatalf("write run-file: %v", err)
	}
}

func TestE2E_SpawnAndProjectAddDTORoundTrip(t *testing.T) {
	t.Run("spawn", func(t *testing.T) {
		sessions := &fakeSessionService{}
		startDriftTestDaemon(t, sessions, &fakeProjectManager{})

		var out bytes.Buffer
		root := NewRootCommand(Deps{
			Out:          &out,
			Err:          &out,
			HTTPClient:   &http.Client{},
			ProcessAlive: func(int) bool { return true },
		})
		root.SetArgs([]string{
			"spawn",
			"--project", "mer",
			"--harness", "codex",
			"--branch", "feat/x",
			"--prompt", "hi",
			"--issue", "ISS-1",
			"--name", "my worker",
			"--model", "codex-cheap",
		})
		if err := root.Execute(); err != nil {
			t.Fatalf("spawn execute: %v\noutput: %s", err, out.String())
		}

		got := sessions.spawned
		if got.ProjectID != "mer" {
			t.Errorf("ProjectID = %q, want %q (CLI json:\"projectId\" vs SpawnSessionRequest)", got.ProjectID, "mer")
		}
		if got.Harness != "codex" {
			t.Errorf("Harness = %q, want %q", got.Harness, "codex")
		}
		if got.Branch != "feat/x" {
			t.Errorf("Branch = %q, want %q", got.Branch, "feat/x")
		}
		if got.Prompt != "hi" {
			t.Errorf("Prompt = %q, want %q", got.Prompt, "hi")
		}
		if got.IssueID != "ISS-1" {
			t.Errorf("IssueID = %q, want %q", got.IssueID, "ISS-1")
		}
		if got.DisplayName != "my worker" {
			t.Errorf("DisplayName = %q, want %q (CLI json:\"displayName\" vs SpawnSessionRequest)", got.DisplayName, "my worker")
		}
		if got.Model != "codex-cheap" {
			t.Errorf("Model = %q, want %q (CLI json:\"model\" vs SpawnSessionRequest)", got.Model, "codex-cheap")
		}
		if !bytes.Contains(out.Bytes(), []byte("spawned session")) {
			t.Errorf("output missing %q; got: %s", "spawned session", out.String())
		}
	})

	t.Run("project add", func(t *testing.T) {
		projects := &fakeProjectManager{}
		startDriftTestDaemon(t, &fakeSessionService{}, projects)

		var out bytes.Buffer
		root := NewRootCommand(Deps{
			Out:          &out,
			Err:          &out,
			HTTPClient:   &http.Client{},
			ProcessAlive: func(int) bool { return true },
		})
		root.SetArgs([]string{
			"project", "add",
			"--path", "/repo/mer",
			"--id", "demo",
			"--name", "Demo",
			"--worker-agent", "codex",
			"--orchestrator-agent", "claude-code",
			"--as-workspace",
		})
		if err := root.Execute(); err != nil {
			t.Fatalf("project add execute: %v\noutput: %s", err, out.String())
		}

		got := projects.added
		if got.Path != "/repo/mer" {
			t.Errorf("Path = %q, want %q", got.Path, "/repo/mer")
		}
		if got.ProjectID == nil || *got.ProjectID != "demo" {
			t.Errorf("ProjectID = %v, want %q (CLI json:\"projectId\" vs AddInput)", got.ProjectID, "demo")
		}
		if got.Name == nil || *got.Name != "Demo" {
			t.Errorf("Name = %v, want %q", got.Name, "Demo")
		}
		if got.Config == nil {
			t.Fatal("Config = nil, want role agent config")
		}
		if got.Config.Worker.Harness != domain.HarnessCodex {
			t.Errorf("Config.Worker.Harness = %q, want codex", got.Config.Worker.Harness)
		}
		if got.Config.Orchestrator.Harness != domain.HarnessClaudeCode {
			t.Errorf("Config.Orchestrator.Harness = %q, want claude-code", got.Config.Orchestrator.Harness)
		}
		if !got.AsWorkspace {
			t.Errorf("AsWorkspace = false, want true (CLI json:\"asWorkspace\" vs AddInput)")
		}
		if !bytes.Contains(out.Bytes(), []byte("registered project")) {
			t.Errorf("output missing %q; got: %s", "registered project", out.String())
		}
	})
}
