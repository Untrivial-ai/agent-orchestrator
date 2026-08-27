package controllers_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/attachmentstore"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	previewutil "github.com/aoagents/agent-orchestrator/backend/internal/preview"
	"github.com/aoagents/agent-orchestrator/backend/internal/previewserver"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
)

type fakeSessionService struct {
	sessions             map[domain.SessionID]domain.Session
	sent                 string
	sentAttachment       *ports.SpawnAttachment
	delegationInput      sessionsvc.DelegateTaskInput
	delegationErr        error
	cleanupProjects      []domain.ProjectID
	cleanupResult        []domain.SessionID
	cleanupSkipped       []sessionsvc.CleanupSkipped
	workspaceFiles       sessionsvc.WorkspaceFiles
	workspaceFile        sessionsvc.WorkspaceFileDetail
	workspaceFileSection sessionsvc.WorkspaceFileSection
	workspaceBlob        sessionsvc.WorkspaceFileBlob
	workspaceTree        sessionsvc.WorkspaceTree
	workspaceTreePath    string
	workspacePaths       []string
	spawnErr             error
	lastSpawn            ports.SpawnConfig
	orchestratorMode     domain.SessionMode
	claimErr             error
	listPRErr            error
	workspaceErr         error
	staged               []ports.SpawnAttachment
	stagedPaths          []string
	stageErr             error
	agentSwitches        map[domain.AgentSwitchID]domain.AgentSwitch
	switchConfig         sessionsvc.SwitchAgentInput
	switchErr            error
	recoveredSwitch      domain.AgentSwitchID
	handoff              json.RawMessage
	handoffSource        domain.AgentGenerationID
	autoInjectCISession  domain.SessionID
	autoInjectCIEnabled  bool
}

type fakeInterfaceTransitionSessionService struct {
	*fakeSessionService
	transition             domain.SessionInterfaceTransition
	acknowledgedSessionID  domain.SessionID
	acknowledgedTransition string
}

func (f *fakeInterfaceTransitionSessionService) InterfaceTransitionStatus(
	context.Context,
	domain.SessionID,
) (sessionsvc.InterfaceTransitionStatus, error) {
	return sessionsvc.InterfaceTransitionStatus{Supported: true, Transition: &f.transition}, nil
}

func (f *fakeInterfaceTransitionSessionService) StartInterfaceTransition(
	context.Context,
	domain.SessionID,
	domain.SessionMode,
	domain.SessionInterfaceTransitionPolicy,
) (domain.SessionInterfaceTransition, error) {
	return f.transition, nil
}

func (f *fakeInterfaceTransitionSessionService) CancelInterfaceTransition(
	context.Context,
	domain.SessionID,
) error {
	return nil
}

func (f *fakeInterfaceTransitionSessionService) AcknowledgeInterfaceTransitionNotice(
	_ context.Context,
	sessionID domain.SessionID,
	transitionID string,
) (domain.SessionInterfaceTransition, error) {
	f.acknowledgedSessionID = sessionID
	f.acknowledgedTransition = transitionID
	return f.transition, nil
}

type fakeManagedPreviewServer struct {
	status         previewserver.Status
	startErr       error
	startName      string
	startWorkspace string
	stopCalls      int
	onStop         func()
}

type allowSessionCapability struct{}

func (allowSessionCapability) Valid(domain.SessionID, string, string) bool { return true }

type denySessionCapability struct{}

func (denySessionCapability) Valid(domain.SessionID, string, string) bool { return false }

func (f *fakeManagedPreviewServer) Start(
	_ context.Context,
	sessionID domain.SessionID,
	workspacePath string,
	configurationName string,
) (previewserver.Status, error) {
	f.startName = configurationName
	f.startWorkspace = workspacePath
	if f.startErr != nil {
		return previewserver.Status{}, f.startErr
	}
	f.status.SessionID = sessionID
	return f.status, nil
}

func (f *fakeManagedPreviewServer) Stop(
	_ context.Context,
	sessionID domain.SessionID,
) (previewserver.Status, error) {
	f.stopCalls++
	if f.onStop != nil {
		f.onStop()
	}
	f.status.SessionID = sessionID
	f.status.State = previewserver.StateStopped
	return f.status, nil
}

func (f *fakeManagedPreviewServer) Status(sessionID domain.SessionID) previewserver.Status {
	status := f.status
	status.SessionID = sessionID
	if status.State == "" {
		status.State = previewserver.StateStopped
	}
	if status.Logs == nil {
		status.Logs = []string{}
	}
	return status
}

func newFakeSessionService() *fakeSessionService {
	now := time.Now().UTC()
	s := domain.Session{SessionRecord: domain.SessionRecord{ID: "ao-1", ProjectID: "ao", Kind: domain.KindWorker, Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}, AutoInjectReview: true, AutoInjectCI: true, CreatedAt: now, UpdatedAt: now}, Status: domain.StatusIdle, TerminalHandleID: "ao-1/terminal_0"}
	return &fakeSessionService{
		sessions:      map[domain.SessionID]domain.Session{s.ID: s},
		agentSwitches: map[domain.AgentSwitchID]domain.AgentSwitch{},
	}
}

func (f *fakeSessionService) List(_ context.Context, filter sessionsvc.ListFilter) ([]domain.Session, error) {
	var out []domain.Session
	for _, s := range f.sessions {
		if filter.ProjectID != "" && s.ProjectID != filter.ProjectID {
			continue
		}
		if filter.Active != nil && s.IsTerminated == *filter.Active {
			continue
		}
		if filter.OrchestratorOnly && s.Kind != domain.KindOrchestrator {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeSessionService) Spawn(_ context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
	f.lastSpawn = cfg
	if f.spawnErr != nil {
		return domain.Session{}, 0, 0, f.spawnErr
	}
	now := time.Now().UTC()
	s := domain.Session{SessionRecord: domain.SessionRecord{ID: domain.SessionID(string(cfg.ProjectID) + "-2"), ProjectID: cfg.ProjectID, IssueID: cfg.IssueID, Kind: cfg.Kind, Harness: cfg.Harness, DisplayName: cfg.DisplayName, Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}, AutoInjectReview: true, AutoInjectCI: true, CreatedAt: now, UpdatedAt: now}, Status: domain.StatusIdle}
	f.sessions[s.ID] = s
	return s, len(cfg.Prompt), 0, nil
}

func (f *fakeSessionService) SpawnOrchestrator(ctx context.Context, projectID domain.ProjectID, clean bool, requestedMode domain.SessionMode) (domain.Session, error) {
	f.orchestratorMode = requestedMode
	if clean {
		active := true
		existing, err := f.List(ctx, sessionsvc.ListFilter{ProjectID: projectID, Active: &active, OrchestratorOnly: true})
		if err != nil {
			return domain.Session{}, err
		}
		for _, o := range existing {
			if _, err := f.Kill(ctx, o.ID); err != nil {
				return domain.Session{}, err
			}
		}
	}
	s, _, _, err := f.Spawn(ctx, ports.SpawnConfig{ProjectID: projectID, Kind: domain.KindOrchestrator, RequestedMode: requestedMode})
	return s, err
}

func (f *fakeSessionService) Get(_ context.Context, id domain.SessionID) (domain.Session, error) {
	s, ok := f.sessions[id]
	if !ok {
		return domain.Session{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	return s, nil
}

func (f *fakeSessionService) SetPreview(_ context.Context, id domain.SessionID, previewURL string) (domain.Session, error) {
	s, ok := f.sessions[id]
	if !ok {
		return domain.Session{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	s.Metadata.PreviewURL = previewURL
	// Mirror the store: every set bumps the revision, even when the URL is
	// unchanged, so the controller's refresh contract can be exercised here.
	s.Metadata.PreviewRevision++
	f.sessions[id] = s
	return s, nil
}

func (f *fakeSessionService) SetTerminateOnPRMerge(_ context.Context, id domain.SessionID, terminate bool) (domain.Session, error) {
	s, ok := f.sessions[id]
	if !ok {
		return domain.Session{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	s.TerminateOnPRMerge = terminate
	f.sessions[id] = s
	return s, nil
}

func (f *fakeSessionService) SetAutoInjectReview(_ context.Context, id domain.SessionID, autoInject bool) (domain.Session, error) {
	s, ok := f.sessions[id]
	if !ok {
		return domain.Session{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	s.AutoInjectReview = autoInject
	f.sessions[id] = s
	return s, nil
}

func (f *fakeSessionService) SetAutoInjectCI(_ context.Context, id domain.SessionID, autoInject bool) (domain.Session, error) {
	s, ok := f.sessions[id]
	if !ok {
		return domain.Session{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	f.autoInjectCISession = id
	f.autoInjectCIEnabled = autoInject
	s.AutoInjectCI = autoInject
	f.sessions[id] = s
	return s, nil
}

func (f *fakeSessionService) Pin(_ context.Context, id domain.SessionID) (domain.Session, error) {
	s, ok := f.sessions[id]
	if !ok {
		return domain.Session{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	s.IsPinned = true
	now := time.Now().UTC()
	s.PinnedAt = &now
	f.sessions[id] = s
	return s, nil
}

func (f *fakeSessionService) Unpin(_ context.Context, id domain.SessionID) (domain.Session, error) {
	s, ok := f.sessions[id]
	if !ok {
		return domain.Session{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	s.IsPinned = false
	s.PinnedAt = nil
	f.sessions[id] = s
	return s, nil
}

func (f *fakeSessionService) SetReviewerHarness(_ context.Context, id domain.SessionID, harness domain.ReviewerHarness) (domain.Session, error) {
	s, ok := f.sessions[id]
	if !ok {
		return domain.Session{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	s.ReviewerHarness = harness
	f.sessions[id] = s
	return s, nil
}

func (f *fakeSessionService) SetAutoReview(_ context.Context, id domain.SessionID, enabled bool) (domain.Session, error) {
	s, ok := f.sessions[id]
	if !ok {
		return domain.Session{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	s.AutoReviewEnabled = enabled
	f.sessions[id] = s
	return s, nil
}

func (f *fakeSessionService) Restore(_ context.Context, id domain.SessionID) (sessionsvc.RestoreOutcome, error) {
	s := f.sessions[id]
	s.IsTerminated = false
	s.Status = domain.StatusIdle
	f.sessions[id] = s
	return sessionsvc.RestoreOutcome{Session: s, Mode: sessionsvc.RestoreModeView("native")}, nil
}

func (f *fakeSessionService) ResumeAgent(_ context.Context, id domain.SessionID) (sessionsvc.ResumeAgentOutcome, error) {
	s := f.sessions[id]
	s.Activity.State = domain.ActivityIdle
	s.Status = domain.StatusIdle
	f.sessions[id] = s
	return sessionsvc.ResumeAgentOutcome{Session: s, Mode: sessionsvc.RestoreModeViewNative}, nil
}

func (f *fakeSessionService) SwitchAgent(_ context.Context, id domain.SessionID, cfg sessionsvc.SwitchAgentInput) (domain.AgentSwitch, error) {
	if f.switchErr != nil {
		return domain.AgentSwitch{}, f.switchErr
	}
	if _, ok := f.sessions[id]; !ok {
		return domain.AgentSwitch{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	f.switchConfig = cfg
	now := time.Now().UTC()
	targetRef := domain.AgentNativeSessionID("native-target")
	record := domain.AgentSwitch{
		ID: "switch-1", SessionID: id, IdempotencyKey: "private-retry-key", RequestFingerprint: "v1:private-fingerprint",
		FromHarness: domain.HarnessClaudeCode, TargetHarness: cfg.TargetHarness,
		TargetNativeSessionRef: &targetRef,
		TargetStartMode:        domain.AgentSwitchTargetStartFresh, State: domain.AgentSwitchPreparingHandoff,
		AgentHandoffStatus:      domain.AgentHandoffReceived,
		SemanticHandoffIncluded: true,
		AgentHandoffPath:        "/private/ao/handoff.json", AgentHandoffHash: "private-hash",
		FinalHandoffPath: "/private/ao/handoff.json", FinalHandoffHash: "private-hash",
		SourceGenerationID: "private-source-generation", TargetGenerationID: "private-target-generation",
		TargetRuntimeHandleID:  "private-target-runtime-handle",
		TargetAcknowledgedAt:   &now,
		ErrorCode:              domain.AgentSwitchErrorTargetStartUnconfirmed,
		SourceTranscriptStatus: domain.AgentSwitchSourceTranscriptUnavailable,
		RequestedAt:            now, UpdatedAt: now,
	}
	f.agentSwitches[record.ID] = record
	return record, nil
}

func (f *fakeSessionService) ListAgentSwitches(_ context.Context, id domain.SessionID) ([]domain.AgentSwitch, error) {
	if _, ok := f.sessions[id]; !ok {
		return nil, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	out := make([]domain.AgentSwitch, 0, len(f.agentSwitches))
	for _, record := range f.agentSwitches {
		if record.SessionID == id {
			out = append(out, record)
		}
	}
	return out, nil
}

func (f *fakeSessionService) RecoverAgentSwitch(_ context.Context, id domain.SessionID, switchID domain.AgentSwitchID) (domain.AgentSwitch, error) {
	record, ok := f.agentSwitches[switchID]
	if !ok || record.SessionID != id {
		return domain.AgentSwitch{}, apierr.NotFound("AGENT_SWITCH_NOT_FOUND", "Unknown agent switch")
	}
	f.recoveredSwitch = switchID
	return record, nil
}

func (f *fakeSessionService) SubmitAgentHandoff(
	_ context.Context,
	id domain.SessionID,
	switchID domain.AgentSwitchID,
	sourceGenerationID domain.AgentGenerationID,
	handoff json.RawMessage,
) (domain.AgentSwitch, error) {
	record, ok := f.agentSwitches[switchID]
	if !ok || record.SessionID != id {
		return domain.AgentSwitch{}, apierr.NotFound("AGENT_SWITCH_NOT_FOUND", "Unknown agent switch")
	}
	f.handoffSource = sourceGenerationID
	f.handoff = append(json.RawMessage(nil), handoff...)
	record.SourceGenerationID = sourceGenerationID
	record.AgentHandoffStatus = domain.AgentHandoffReceived
	record.AgentHandoffPath = "/private/ao/agent-handoff.json"
	record.AgentHandoffHash = "private-hash"
	f.agentSwitches[switchID] = record
	return record, nil
}

func (f *fakeSessionService) Kill(_ context.Context, id domain.SessionID) (bool, error) {
	s := f.sessions[id]
	s.IsTerminated = true
	s.Status = domain.StatusTerminated
	f.sessions[id] = s
	return true, nil
}

func (f *fakeSessionService) RollbackSpawn(_ context.Context, id domain.SessionID) (sessionsvc.RollbackOutcome, error) {
	if _, ok := f.sessions[id]; ok {
		delete(f.sessions, id)
		return sessionsvc.RollbackOutcome{Deleted: true}, nil
	}
	return sessionsvc.RollbackOutcome{}, nil
}

func (f *fakeSessionService) Cleanup(_ context.Context, project domain.ProjectID) (sessionsvc.CleanupOutcome, error) {
	f.cleanupProjects = append(f.cleanupProjects, project)
	cleaned := f.cleanupResult
	if cleaned == nil {
		cleaned = []domain.SessionID{"ao-1"}
	}
	return sessionsvc.CleanupOutcome{Cleaned: cleaned, Skipped: f.cleanupSkipped}, nil
}

func (f *fakeSessionService) Rename(_ context.Context, id domain.SessionID, displayName string) error {
	s, ok := f.sessions[id]
	if !ok {
		return apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	s.DisplayName = displayName
	f.sessions[id] = s
	return nil
}

func (f *fakeSessionService) Send(_ context.Context, _ domain.SessionID, message string, attachment *ports.SpawnAttachment) error {
	f.sent = message
	f.sentAttachment = attachment
	return nil
}

func (f *fakeSessionService) DelegateTask(_ context.Context, in sessionsvc.DelegateTaskInput) (sessionsvc.DelegateTaskOutcome, error) {
	f.delegationInput = in
	if f.delegationErr != nil {
		return sessionsvc.DelegateTaskOutcome{}, f.delegationErr
	}
	return sessionsvc.DelegateTaskOutcome{WorkerID: "ao-worker", OrchestratorID: "ao-orch"}, nil
}

func (f *fakeSessionService) ListPRs(_ context.Context, id domain.SessionID) ([]domain.PRFacts, error) {
	if f.listPRErr != nil {
		return nil, f.listPRErr
	}
	if _, ok := f.sessions[id]; !ok {
		return nil, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	return []domain.PRFacts{{URL: "https://github.com/aoagents/agent-orchestrator/pull/142", Number: 142, CI: domain.CIPassing, Review: domain.ReviewRequired, Mergeability: domain.MergeMergeable, UpdatedAt: time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)}}, nil
}

func (f *fakeSessionService) ListPRSummaries(_ context.Context, id domain.SessionID) ([]sessionsvc.PRSummary, error) {
	if f.listPRErr != nil {
		return nil, f.listPRErr
	}
	if _, ok := f.sessions[id]; !ok {
		return nil, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	return []sessionsvc.PRSummary{{
		URL:          "https://github.com/aoagents/agent-orchestrator/pull/142",
		HTMLURL:      "https://github.com/aoagents/agent-orchestrator/pull/142",
		Number:       142,
		Title:        "Wire SCM summaries",
		State:        domain.PRStateOpen,
		Provider:     "github",
		Repo:         "aoagents/agent-orchestrator",
		Author:       "ada",
		SourceBranch: "codex/scm-observer-v1",
		TargetBranch: "main",
		HeadSHA:      "abc123",
		CI: sessionsvc.PRCISummary{State: domain.CIFailing, FailingChecks: []sessionsvc.PRFailingCheck{{
			Name:       "unit",
			Status:     domain.PRCheckFailed,
			Conclusion: "failure",
			URL:        "https://github.com/aoagents/agent-orchestrator/actions/runs/1",
		}}},
		Review: sessionsvc.PRReviewSummary{
			Decision:                   domain.ReviewChangesRequest,
			HasUnresolvedHumanComments: true,
			UnresolvedBy: []sessionsvc.PRUnresolvedReviewer{{
				ReviewerID: "reviewer-a",
				Count:      1,
				ReviewURL:  "https://github.com/aoagents/agent-orchestrator/pull/142#pullrequestreview-1",
				Links:      []sessionsvc.PRReviewCommentLink{{URL: "https://github.com/aoagents/agent-orchestrator/pull/142#discussion_r1", File: "main.go", Line: 12}},
			}},
		},
		Mergeability: sessionsvc.PRMergeabilitySummary{
			State:   domain.MergeConflicting,
			Reasons: []string{"conflicts"},
			PRURL:   "https://github.com/aoagents/agent-orchestrator/pull/142",
		},
		StateChangedAt: time.Date(2026, 6, 4, 11, 30, 0, 0, time.UTC),
		CreatedAt:      time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC),
	}}, nil
}

func (f *fakeSessionService) ClaimPR(_ context.Context, id domain.SessionID, ref string, opts sessionsvc.ClaimPROptions) (sessionsvc.ClaimPRResult, error) {
	if f.claimErr != nil {
		return sessionsvc.ClaimPRResult{}, f.claimErr
	}
	if _, ok := f.sessions[id]; !ok {
		return sessionsvc.ClaimPRResult{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	prs, _ := f.ListPRs(context.Background(), id)
	return sessionsvc.ClaimPRResult{PRs: prs, TakenOverFrom: []domain.SessionID{}, BranchChanged: true}, nil
}

func (f *fakeSessionService) StageAttachments(
	_ context.Context,
	id domain.SessionID,
	attachments []ports.SpawnAttachment,
) ([]string, error) {
	if f.stageErr != nil {
		return nil, f.stageErr
	}
	if _, ok := f.sessions[id]; !ok {
		return nil, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	f.staged = attachments
	return f.stagedPaths, nil
}

func (f *fakeSessionService) ListWorkspaceFiles(_ context.Context, id domain.SessionID) (sessionsvc.WorkspaceFiles, error) {
	if f.workspaceErr != nil {
		return sessionsvc.WorkspaceFiles{}, f.workspaceErr
	}
	if _, ok := f.sessions[id]; !ok {
		return sessionsvc.WorkspaceFiles{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	if f.workspaceFiles.SessionID != "" {
		return f.workspaceFiles, nil
	}
	return sessionsvc.WorkspaceFiles{SessionID: id}, nil
}

func (f *fakeSessionService) WorkspaceWatchPaths(_ context.Context, id domain.SessionID) ([]string, error) {
	if f.workspaceErr != nil {
		return nil, f.workspaceErr
	}
	session, ok := f.sessions[id]
	if !ok {
		return nil, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	if len(f.workspacePaths) > 0 {
		return f.workspacePaths, nil
	}
	return []string{session.Metadata.WorkspacePath}, nil
}

func (f *fakeSessionService) GetWorkspaceFile(_ context.Context, id domain.SessionID, path string, section sessionsvc.WorkspaceFileSection) (sessionsvc.WorkspaceFileDetail, error) {
	f.workspaceFileSection = section
	if f.workspaceErr != nil {
		return sessionsvc.WorkspaceFileDetail{}, f.workspaceErr
	}
	if _, ok := f.sessions[id]; !ok {
		return sessionsvc.WorkspaceFileDetail{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	if f.workspaceFile.SessionID != "" {
		return f.workspaceFile, nil
	}
	return sessionsvc.WorkspaceFileDetail{SessionID: id, Path: path}, nil
}

func (f *fakeSessionService) GetWorkspaceFileBlob(_ context.Context, id domain.SessionID, path string, side sessionsvc.WorkspaceFileBlobSide) (sessionsvc.WorkspaceFileBlob, error) {
	if f.workspaceErr != nil {
		return sessionsvc.WorkspaceFileBlob{}, f.workspaceErr
	}
	if _, ok := f.sessions[id]; !ok {
		return sessionsvc.WorkspaceFileBlob{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	if f.workspaceBlob.MediaType != "" {
		blob := f.workspaceBlob
		blob.Side = side
		return blob, nil
	}
	return sessionsvc.WorkspaceFileBlob{Path: path, Side: side, MediaType: "image/png"}, nil
}

func (f *fakeSessionService) ListWorkspaceTree(_ context.Context, id domain.SessionID, path string) (sessionsvc.WorkspaceTree, error) {
	f.workspaceTreePath = path
	if f.workspaceErr != nil {
		return sessionsvc.WorkspaceTree{}, f.workspaceErr
	}
	if _, ok := f.sessions[id]; !ok {
		return sessionsvc.WorkspaceTree{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	if f.workspaceTree.SessionID != "" {
		return f.workspaceTree, nil
	}
	return sessionsvc.WorkspaceTree{SessionID: id, Path: path}, nil
}

func TestSessionsAPI_ListWorkspaceTree(t *testing.T) {
	t.Run("lists the requested directory", func(t *testing.T) {
		svc := newFakeSessionService()
		svc.workspaceTree = sessionsvc.WorkspaceTree{
			SessionID: "ao-1",
			Path:      "src",
			Entries: []sessionsvc.WorkspaceTreeEntry{{
				Name: "main.go", Path: "src/main.go", Type: sessionsvc.WorkspaceTreeFile,
			}},
		}
		srv := newSessionTestServer(t, svc)
		body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/sessions/ao-1/workspace/tree?path=src", "")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", status, body)
		}
		if svc.workspaceTreePath != "src" {
			t.Fatalf("service path = %q, want src", svc.workspaceTreePath)
		}
		var got controllers.ListWorkspaceTreeResponse
		mustJSON(t, body, &got)
		if got.Path != "src" || len(got.Entries) != 1 || got.Entries[0].Path != "src/main.go" {
			t.Fatalf("response = %+v", got)
		}
	})

	t.Run("returns not found for an unknown session", func(t *testing.T) {
		srv := newSessionTestServer(t, newFakeSessionService())
		body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/sessions/missing/workspace/tree", "")
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body=%s", status, body)
		}
	})

	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{name: "invalid path", err: apierr.Invalid("INVALID_WORKSPACE_PATH", "invalid workspace path", nil), want: http.StatusBadRequest},
		{name: "service failure", err: errors.New("tree unavailable"), want: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newFakeSessionService()
			svc.workspaceErr = tc.err
			srv := newSessionTestServer(t, svc)
			body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/sessions/ao-1/workspace/tree?path=src", "")
			if status != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", status, tc.want, body)
			}
		})
	}
}

func TestSessionsAPI_AgentSwitchLifecycle(t *testing.T) {
	svc := newFakeSessionService()
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/sessions/ao-1/switch-agent", `{
		"targetHarness":"codex",
		"model":" gpt-\u0000-5.4 ",
		"idempotencyKey":"retry-1"
	}`)
	if status != http.StatusAccepted {
		t.Fatalf("switch agent = %d, want 202; body=%s", status, body)
	}
	assertAgentSwitchResponseRedacted(t, body)
	var switched controllers.AgentSwitchResponse
	mustJSON(t, body, &switched)
	if switched.Switch.ID != "switch-1" || switched.Switch.TargetHarness != domain.HarnessCodex {
		t.Fatalf("switch response = %+v", switched.Switch)
	}
	if switched.Switch.ErrorCode != domain.AgentSwitchErrorTargetStartUnconfirmed {
		t.Fatalf("safe error code = %q", switched.Switch.ErrorCode)
	}
	if switched.Switch.SourceTranscriptStatus != domain.AgentSwitchSourceTranscriptUnavailable {
		t.Fatalf("source transcript status = %q", switched.Switch.SourceTranscriptStatus)
	}
	if !switched.Switch.SemanticHandoffIncluded {
		t.Fatal("semantic handoff inclusion fact was not projected")
	}
	if svc.switchConfig.Model != "gpt--5.4" || svc.switchConfig.IdempotencyKey != "retry-1" {
		t.Fatalf("switch config = %+v", svc.switchConfig)
	}

	body, status, _ = doRequest(t, srv, http.MethodGet, "/api/v1/sessions/ao-1/agent-switches", "")
	if status != http.StatusOK {
		t.Fatalf("list switches = %d, want 200; body=%s", status, body)
	}
	assertAgentSwitchResponseRedacted(t, body)
	var listed controllers.ListAgentSwitchesResponse
	mustJSON(t, body, &listed)
	if len(listed.Switches) != 1 || listed.Switches[0].ID != "switch-1" {
		t.Fatalf("listed switches = %+v", listed.Switches)
	}

	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/sessions/ao-1/agent-switches/switch-1/handoff", `{
		"sourceGenerationId":"generation-7",
		"handoff":{"summary":"tests pass","nextSteps":["review diff"]}
	}`)
	if status != http.StatusOK {
		t.Fatalf("submit handoff = %d, want 200; body=%s", status, body)
	}
	assertAgentSwitchResponseRedacted(t, body)
	if svc.handoffSource != "generation-7" {
		t.Fatalf("source generation = %q", svc.handoffSource)
	}
	var handoff map[string]any
	if err := json.Unmarshal(svc.handoff, &handoff); err != nil {
		t.Fatalf("decode recorded handoff: %v", err)
	}
	if handoff["summary"] != "tests pass" {
		t.Fatalf("recorded handoff = %#v", handoff)
	}
	recovery := svc.agentSwitches["switch-1"]
	recovery.State = domain.AgentSwitchSourceStopped
	recovery.ErrorCode = domain.AgentSwitchErrorSourceRestoreUnconfirmed
	svc.agentSwitches[recovery.ID] = recovery
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/sessions/ao-1/agent-switches/switch-1/recover", "")
	if status != http.StatusAccepted {
		t.Fatalf("recover switch = %d, want 202; body=%s", status, body)
	}
	assertAgentSwitchResponseRedacted(t, body)
	if svc.recoveredSwitch != "switch-1" {
		t.Fatalf("recovered switch = %q, want switch-1", svc.recoveredSwitch)
	}

}

func TestSessionsAPIActiveSwitchProjectionRedactsPrivateFacts(t *testing.T) {
	svc := newFakeSessionService()
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	targetRef := domain.AgentNativeSessionID("native-target")
	session := svc.sessions["ao-1"]
	session.ActiveAgentSwitch = &domain.AgentSwitch{
		ID: "switch-active", SessionID: "ao-1", IdempotencyKey: "private-key",
		RequestFingerprint: "v1:private-fingerprint", FromHarness: domain.HarnessClaudeCode,
		TargetHarness: domain.HarnessCodex, TargetNativeSessionRef: &targetRef,
		TargetStartMode: domain.AgentSwitchTargetStartFresh, State: domain.AgentSwitchStartingTarget,
		AgentHandoffStatus: domain.AgentHandoffReceived, SemanticHandoffIncluded: true,
		SourceTranscriptStatus: domain.AgentSwitchSourceTranscriptAvailable,
		AgentHandoffPath:       "/private/agent-handoff.json", AgentHandoffHash: "private-agent-hash",
		FinalHandoffPath: "/private/final-handoff.json", FinalHandoffHash: "private-final-hash",
		SourceGenerationID: "private-source-generation", TargetGenerationID: "private-target-generation",
		TargetRuntimeHandleID: "private-runtime-handle", TargetAcknowledgedAt: &now,
		RequestedAt: now, UpdatedAt: now,
	}
	svc.sessions["ao-1"] = session
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/sessions/ao-1", "")
	if status != http.StatusOK {
		t.Fatalf("get session = %d, want 200; body=%s", status, body)
	}
	assertAgentSwitchResponseRedacted(t, body)
	var response struct {
		Session struct {
			ActiveAgentSwitch *controllers.AgentSwitchView `json:"activeAgentSwitch"`
		} `json:"session"`
	}
	mustJSON(t, body, &response)
	if response.Session.ActiveAgentSwitch == nil || response.Session.ActiveAgentSwitch.ID != "switch-active" || response.Session.ActiveAgentSwitch.State != domain.AgentSwitchStartingTarget {
		t.Fatalf("active switch projection = %+v", response.Session.ActiveAgentSwitch)
	}
}

func assertAgentSwitchResponseRedacted(t *testing.T, body []byte) {
	t.Helper()
	privateFields := []string{
		"idempotencyKey",
		"requestFingerprint",
		"targetNativeSessionRef",
		"agentHandoffPath",
		"agentHandoffHash",
		"finalHandoffPath",
		"finalHandoffHash",
		"sourceGenerationId",
		"targetGenerationId",
		"targetRuntimeHandleId",
		"targetAcknowledgedAt",
	}
	for _, field := range privateFields {
		if strings.Contains(string(body), `"`+field+`"`) {
			t.Errorf("public agent-switch response leaked %s: %s", field, body)
		}
	}
}

func TestSessionsAPI_SubmitAgentHandoffPreservesRawObject(t *testing.T) {
	svc := newFakeSessionService()
	svc.agentSwitches["switch-1"] = domain.AgentSwitch{ID: "switch-1", SessionID: "ao-1"}
	srv := newSessionTestServer(t, svc)

	const handoff = `{"schemaVersion":1,"goal":"first","goal":"second","progressSummary":"ready"}`
	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/sessions/ao-1/agent-switches/switch-1/handoff", `{
		"sourceGenerationId":"generation-7",
		"handoff":`+handoff+`
	}`)
	if status != http.StatusOK {
		t.Fatalf("submit handoff = %d, want 200; body=%s", status, body)
	}
	if got := string(svc.handoff); got != handoff {
		t.Fatalf("handoff raw JSON = %s, want exact input %s", got, handoff)
	}
	assertAgentSwitchResponseRedacted(t, body)
}

func TestSessionsAPI_AgentSwitchValidationAndErrors(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		wantCode string
	}{
		{name: "target required", method: http.MethodPost, path: "/api/v1/sessions/ao-1/switch-agent", body: `{}`, wantCode: "TARGET_HARNESS_REQUIRED"},
		{name: "retired note rejected", method: http.MethodPost, path: "/api/v1/sessions/ao-1/switch-agent", body: `{"targetHarness":"codex","note":"old context"}`, wantCode: "INVALID_JSON"},
		{name: "model bounded", method: http.MethodPost, path: "/api/v1/sessions/ao-1/switch-agent", body: `{"targetHarness":"codex","model":"` + strings.Repeat("x", 257) + `"}`, wantCode: "MODEL_TOO_LONG"},
		{name: "source generation required", method: http.MethodPost, path: "/api/v1/sessions/ao-1/agent-switches/switch-1/handoff", body: `{"handoff":{}}`, wantCode: "SOURCE_GENERATION_REQUIRED"},
		{name: "handoff required", method: http.MethodPost, path: "/api/v1/sessions/ao-1/agent-switches/switch-1/handoff", body: `{"sourceGenerationId":"generation-7"}`, wantCode: "HANDOFF_REQUIRED"},
		{name: "handoff bounded", method: http.MethodPost, path: "/api/v1/sessions/ao-1/agent-switches/switch-1/handoff", body: `{"sourceGenerationId":"generation-7","handoff":{"summary":"` + strings.Repeat("x", 65537) + `"}}`, wantCode: "HANDOFF_TOO_LARGE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := newFakeSessionService()
			svc.agentSwitches["switch-1"] = domain.AgentSwitch{ID: "switch-1", SessionID: "ao-1"}
			srv := newSessionTestServer(t, svc)
			body, status, _ := doRequest(t, srv, tc.method, tc.path, tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", status, body)
			}
			var apiErr struct {
				Code string `json:"code"`
			}
			mustJSON(t, body, &apiErr)
			if apiErr.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q; body=%s", apiErr.Code, tc.wantCode, body)
			}
		})
	}

	svc := newFakeSessionService()
	svc.switchErr = apierr.Conflict("AGENT_SWITCH_IN_PROGRESS", "switch in progress", nil)
	srv := newSessionTestServer(t, svc)
	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/sessions/ao-1/switch-agent", `{"targetHarness":"codex"}`)
	if status != http.StatusConflict || !strings.Contains(string(body), "AGENT_SWITCH_IN_PROGRESS") {
		t.Fatalf("typed conflict = %d body=%s", status, body)
	}
}

func (f *fakeSessionService) InvalidateWorkspaceCache(_ domain.SessionID) {}

func newSessionTestServer(t *testing.T, svc *fakeSessionService) *httptest.Server {
	return newSessionTestServerWithPreview(t, svc, nil)
}

func newSessionTestServerWithPreview(
	t *testing.T,
	svc *fakeSessionService,
	managed *fakeManagedPreviewServer,
) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	deps := httpd.APIDeps{Sessions: svc}
	if managed != nil {
		deps.PreviewServer = managed
		deps.SessionCapabilities = allowSessionCapability{}
	}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, deps, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return srv
}

func doPreviewOriginRequest(t *testing.T, srv *httptest.Server, previewURL, requestPath string) ([]byte, int, http.Header) {
	return doPreviewOriginMethod(t, srv, http.MethodGet, previewURL, requestPath)
}

func doPreviewOriginMethod(t *testing.T, srv *httptest.Server, method, previewURL, requestPath string) ([]byte, int, http.Header) {
	t.Helper()
	preview, err := url.Parse(previewURL)
	if err != nil {
		t.Fatalf("parse preview URL: %v", err)
	}
	req, err := http.NewRequest(method, srv.URL+requestPath, nil)
	if err != nil {
		t.Fatalf("new preview request: %v", err)
	}
	req.Host = preview.Host
	req.Header.Set("Origin", preview.Scheme+"://"+preview.Host)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do preview request: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read preview response: %v", err)
	}
	return body, resp.StatusCode, resp.Header
}

func TestSessionsRoutes_DefaultToStubsWithoutService(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)

	body, status, headers := doRequest(t, srv, "GET", "/api/v1/sessions", "")
	assertJSON(t, headers)
	assertErrorCode(t, body, status, http.StatusNotImplemented, "NOT_IMPLEMENTED")
}

func TestSessionsAPI_AcknowledgeInterfaceTransitionNotice(t *testing.T) {
	acknowledgedAt := time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)
	svc := &fakeInterfaceTransitionSessionService{
		fakeSessionService: newFakeSessionService(),
		transition: domain.SessionInterfaceTransition{
			ID: "transition-1", SessionID: "ao-1",
			SourceMode: domain.SessionModeChat, TargetMode: domain.SessionModeTUI,
			Policy:    domain.SessionInterfaceTransitionDrain,
			Phase:     domain.SessionInterfaceTransitionRecovery,
			CreatedAt: acknowledgedAt.Add(-time.Hour), UpdatedAt: acknowledgedAt.Add(-time.Minute),
			CompletedAt: acknowledgedAt.Add(-time.Minute), NoticeAcknowledgedAt: acknowledgedAt,
		},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(
		config.Config{}, log, nil, httpd.APIDeps{Sessions: svc}, httpd.ControlDeps{},
	))
	t.Cleanup(srv.Close)

	body, status, _ := doRequest(t, srv, http.MethodPut,
		"/api/v1/sessions/ao-1/interface-transition/transition-1/notice-acknowledgement", "")
	if status != http.StatusOK {
		t.Fatalf("acknowledge notice = %d, want 200; body=%s", status, body)
	}
	if svc.acknowledgedSessionID != "ao-1" || svc.acknowledgedTransition != "transition-1" {
		t.Fatalf("acknowledgement target = %s/%s", svc.acknowledgedSessionID, svc.acknowledgedTransition)
	}
	var response controllers.InterfaceTransitionNoticeAckResponse
	mustJSON(t, body, &response)
	if !response.OK || response.Transition.NoticeAcknowledgedAt == nil ||
		!response.Transition.NoticeAcknowledgedAt.Equal(acknowledgedAt) {
		t.Fatalf("acknowledgement response = %+v", response)
	}
}

func TestSessionsAPI_ListSpawnGetAndActions(t *testing.T) {
	svc := newFakeSessionService()
	s := svc.sessions["ao-1"]
	s.Metadata = domain.SessionMetadata{Branch: "qa/modal-worker", WorkspacePath: "/tmp/private-worktree", RuntimeHandleID: "runtime-1", Prompt: "private prompt"}
	s.SCMStatus = domain.StatusReviewPending
	svc.sessions["ao-1"] = s
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "GET", "/api/v1/sessions?project=ao", "")
	if status != http.StatusOK {
		t.Fatalf("GET sessions = %d, want 200; body=%s", status, body)
	}
	var list struct {
		Sessions []sessionBody `json:"sessions"`
	}
	mustJSON(t, body, &list)
	if len(list.Sessions) != 1 || list.Sessions[0].ID != "ao-1" || list.Sessions[0].Status != string(domain.StatusIdle) || list.Sessions[0].SCMStatus != string(domain.StatusReviewPending) || list.Sessions[0].TerminalHandleID != "ao-1/terminal_0" {
		t.Fatalf("list = %#v", list)
	}
	if list.Sessions[0].Branch != "qa/modal-worker" {
		t.Fatalf("branch = %q, want qa/modal-worker", list.Sessions[0].Branch)
	}
	var rawList struct {
		Sessions []map[string]any `json:"sessions"`
	}
	mustJSON(t, body, &rawList)
	if _, ok := rawList.Sessions[0]["metadata"]; ok {
		t.Fatalf("list leaked metadata: %s", body)
	}
	if _, ok := rawList.Sessions[0]["workspacePath"]; ok {
		t.Fatalf("list leaked workspacePath: %s", body)
	}
	if _, ok := rawList.Sessions[0]["prompt"]; ok {
		t.Fatalf("list leaked prompt: %s", body)
	}

	body, status, _ = doRequest(t, srv, "POST", "/api/v1/sessions", `{"projectId":"ao","issueId":"ISS-1","kind":"worker","harness":"codex","prompt":"fix","displayName":"my worker"}`)
	if status != http.StatusCreated {
		t.Fatalf("POST session = %d, want 201; body=%s", status, body)
	}
	var spawned struct {
		Session           sessionBody `json:"session"`
		PromptBytes       *int        `json:"promptBytes"`
		SystemPromptBytes *int        `json:"systemPromptBytes"`
	}
	mustJSON(t, body, &spawned)
	if spawned.Session.ID != "ao-2" || spawned.Session.IssueID != "ISS-1" || spawned.Session.Harness != "codex" {
		t.Fatalf("spawned = %#v", spawned)
	}
	if spawned.Session.DisplayName != "my worker" {
		t.Fatalf("spawned displayName = %q, want %q", spawned.Session.DisplayName, "my worker")
	}
	if spawned.PromptBytes == nil || *spawned.PromptBytes != len("fix") {
		t.Fatalf("spawned promptBytes = %v, want %d", spawned.PromptBytes, len("fix"))
	}
	if spawned.SystemPromptBytes == nil || *spawned.SystemPromptBytes != 0 {
		t.Fatalf("spawned systemPromptBytes = %v, want present zero", spawned.SystemPromptBytes)
	}

	body, status, _ = doRequest(t, srv, "GET", "/api/v1/sessions/ao-2", "")
	if status != http.StatusOK {
		t.Fatalf("GET session = %d, want 200; body=%s", status, body)
	}

	body, status, _ = doRequest(t, srv, "POST", "/api/v1/sessions/ao-2/send", "{\"message\":\"con\\u0000tinue\"}")
	if status != http.StatusOK || svc.sent != "continue" {
		t.Fatalf("send status=%d sent=%q body=%s", status, svc.sent, body)
	}

	body, status, _ = doRequest(t, srv, "POST", "/api/v1/sessions/ao-2/kill", "")
	if status != http.StatusOK {
		t.Fatalf("kill = %d, want 200; body=%s", status, body)
	}
	var killed struct {
		SessionID string `json:"sessionId"`
		Freed     bool   `json:"freed"`
	}
	mustJSON(t, body, &killed)
	if killed.SessionID != "ao-2" || !killed.Freed {
		t.Fatalf("kill response = %#v", killed)
	}

	body, status, _ = doRequest(t, srv, "POST", "/api/v1/sessions/ao-2/restore", "")
	if status != http.StatusOK {
		t.Fatalf("restore = %d, want 200; body=%s", status, body)
	}
	var restored struct {
		SessionID   string `json:"sessionId"`
		RestoreMode string `json:"restoreMode"`
	}
	mustJSON(t, body, &restored)
	if restored.SessionID != "ao-2" || restored.RestoreMode != "native" {
		t.Fatalf("restore response = %#v", restored)
	}

	body, status, _ = doRequest(t, srv, "POST", "/api/v1/sessions/ao-2/resume-agent", "")
	if status != http.StatusOK {
		t.Fatalf("resume agent = %d, want 200; body=%s", status, body)
	}
	var resumed struct {
		SessionID  string `json:"sessionId"`
		ResumeMode string `json:"resumeMode"`
	}
	mustJSON(t, body, &resumed)
	if resumed.SessionID != "ao-2" || resumed.ResumeMode != "native" {
		t.Fatalf("resume response = %#v", resumed)
	}

	body, status, _ = doRequest(t, srv, "PATCH", "/api/v1/sessions/ao-2", `{"displayName":"Renamed"}`)
	if status != http.StatusOK {
		t.Fatalf("rename = %d, want 200; body=%s", status, body)
	}
	var renamed struct {
		OK          bool   `json:"ok"`
		SessionID   string `json:"sessionId"`
		DisplayName string `json:"displayName"`
	}
	mustJSON(t, body, &renamed)
	if !renamed.OK || renamed.SessionID != "ao-2" || renamed.DisplayName != "Renamed" {
		t.Fatalf("rename response = %#v", renamed)
	}
	if svc.sessions["ao-2"].DisplayName != "Renamed" {
		t.Fatalf("session displayName not updated: %+v", svc.sessions["ao-2"])
	}

	body, status, _ = doRequest(t, srv, "PATCH", "/api/v1/sessions/ao-2/merge-policy", `{"terminateOnPrMerge":true}`)
	if status != http.StatusOK {
		t.Fatalf("merge policy = %d, want 200; body=%s", status, body)
	}
	var policy struct {
		OK                 bool   `json:"ok"`
		SessionID          string `json:"sessionId"`
		TerminateOnPRMerge bool   `json:"terminateOnPrMerge"`
	}
	mustJSON(t, body, &policy)
	if !policy.OK || policy.SessionID != "ao-2" || !policy.TerminateOnPRMerge {
		t.Fatalf("merge policy response = %#v", policy)
	}
	if !svc.sessions["ao-2"].TerminateOnPRMerge {
		t.Fatalf("session merge policy not updated: %+v", svc.sessions["ao-2"])
	}

	body, status, _ = doRequest(t, srv, "PUT", "/api/v1/sessions/ao-2/auto-review", `{"enabled":true}`)
	if status != http.StatusOK {
		t.Fatalf("auto review = %d, want 200; body=%s", status, body)
	}
	var autoReview struct {
		Session domain.Session `json:"session"`
	}
	mustJSON(t, body, &autoReview)
	if !autoReview.Session.AutoReviewEnabled || !svc.sessions["ao-2"].AutoReviewEnabled {
		t.Fatalf("auto review response=%+v stored=%+v", autoReview, svc.sessions["ao-2"])
	}

	body, status, _ = doRequest(t, srv, "PUT", "/api/v1/sessions/ao-2/auto-review", `{}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "AUTO_REVIEW_ENABLED_REQUIRED") {
		t.Fatalf("missing auto review enabled = %d, want 400; body=%s", status, body)
	}
	if !svc.sessions["ao-2"].AutoReviewEnabled {
		t.Fatal("malformed auto review request changed persisted state")
	}

	body, status, _ = doRequest(t, srv, "PATCH", "/api/v1/sessions/ao-2/auto-inject-review", `{"autoInjectReview":false}`)
	if status != http.StatusOK {
		t.Fatalf("auto-inject review policy = %d, want 200; body=%s", status, body)
	}
	var autoInjectPolicy struct {
		OK               bool   `json:"ok"`
		SessionID        string `json:"sessionId"`
		AutoInjectReview bool   `json:"autoInjectReview"`
	}
	mustJSON(t, body, &autoInjectPolicy)
	if !autoInjectPolicy.OK || autoInjectPolicy.SessionID != "ao-2" || autoInjectPolicy.AutoInjectReview {
		t.Fatalf("auto-inject review policy response = %#v", autoInjectPolicy)
	}
	if svc.sessions["ao-2"].AutoInjectReview {
		t.Fatalf("session auto-inject review policy not updated: %+v", svc.sessions["ao-2"])
	}

	body, status, _ = doRequest(t, srv, "PATCH", "/api/v1/sessions/ao-2/auto-inject-ci", `{"autoInjectCI":false}`)
	if status != http.StatusOK {
		t.Fatalf("auto-inject CI policy = %d, want 200; body=%s", status, body)
	}
	var ciPolicy struct {
		OK           bool   `json:"ok"`
		SessionID    string `json:"sessionId"`
		AutoInjectCI bool   `json:"autoInjectCI"`
	}
	mustJSON(t, body, &ciPolicy)
	if !ciPolicy.OK || ciPolicy.SessionID != "ao-2" || ciPolicy.AutoInjectCI {
		t.Fatalf("auto-inject CI policy response = %#v", ciPolicy)
	}
	if svc.autoInjectCISession != "ao-2" || svc.autoInjectCIEnabled || svc.sessions["ao-2"].AutoInjectCI {
		t.Fatalf("auto-inject CI service input = session:%q enabled:%v", svc.autoInjectCISession, svc.autoInjectCIEnabled)
	}

	body, status, _ = doRequest(t, srv, "POST", "/api/v1/sessions/ao-2/pin", "")
	if status != http.StatusOK {
		t.Fatalf("pin = %d, want 200; body=%s", status, body)
	}
	var pinned struct {
		Session struct {
			IsPinned bool `json:"isPinned"`
		} `json:"session"`
	}
	mustJSON(t, body, &pinned)
	if !pinned.Session.IsPinned {
		t.Fatalf("pin response = %#v", pinned)
	}
	if !svc.sessions["ao-2"].IsPinned {
		t.Fatalf("session pin not updated: %+v", svc.sessions["ao-2"])
	}

	body, status, _ = doRequest(t, srv, "DELETE", "/api/v1/sessions/ao-2/pin", "")
	if status != http.StatusOK {
		t.Fatalf("unpin = %d, want 200; body=%s", status, body)
	}
	var unpinned struct {
		Session struct {
			IsPinned bool `json:"isPinned"`
		} `json:"session"`
	}
	mustJSON(t, body, &unpinned)
	if unpinned.Session.IsPinned {
		t.Fatalf("unpin response = %#v", unpinned)
	}
	if svc.sessions["ao-2"].IsPinned {
		t.Fatalf("session unpin not updated: %+v", svc.sessions["ao-2"])
	}

	_, status, _ = doRequest(t, srv, "POST", "/api/v1/sessions/ghost-1/pin", "")
	if status != http.StatusNotFound {
		t.Fatalf("pin unknown = %d, want 404", status)
	}

	_, status, _ = doRequest(t, srv, "DELETE", "/api/v1/sessions/ghost-1/pin", "")
	if status != http.StatusNotFound {
		t.Fatalf("unpin unknown = %d, want 404", status)
	}

	body, status, _ = doRequest(t, srv, "POST", "/api/v1/orchestrators", `{"projectId":"ao"}`)
	if status != http.StatusCreated {
		t.Fatalf("orchestrator = %d, want 201; body=%s", status, body)
	}
}

func TestSessionsAPI_SetAutoInjectCIValidatesBody(t *testing.T) {
	for _, tt := range []struct {
		name     string
		body     string
		wantCode string
	}{
		{name: "malformed JSON", body: `{`, wantCode: "INVALID_JSON"},
		{name: "missing autoInjectCI", body: `{}`, wantCode: "AUTO_INJECT_CI_REQUIRED"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := newFakeSessionService()
			srv := newSessionTestServer(t, svc)
			body, status, _ := doRequest(t, srv, "PATCH", "/api/v1/sessions/ao-1/auto-inject-ci", tt.body)
			assertErrorCode(t, body, status, http.StatusBadRequest, tt.wantCode)
			if svc.autoInjectCISession != "" {
				t.Fatalf("SetAutoInjectCI called for invalid body with session %q", svc.autoInjectCISession)
			}
		})
	}
}

func TestSessionsAPI_SpawnRejectsOversizedBody(t *testing.T) {
	svc := newFakeSessionService()
	srv := newSessionTestServer(t, svc)

	// A body past the ~35 MiB maxSpawnBodyBytes cap is rejected while decoding
	// (MaxBytesReader), before the attachment size caps and without materializing
	// the whole body. The oversized bytes live in an *attachment* payload (not the
	// prompt, which has its own much smaller PROMPT_TOO_LONG cap), so this pins the
	// body cap specifically: MaxBytesReader makes the read/decode fail with
	// INVALID_JSON. If that line were removed the body would decode fully and be
	// rejected later with an attachment-specific code (ATTACHMENT_TOO_LARGE),
	// failing this test. 40 MiB of base64 comfortably exceeds the ~35 MiB cap.
	oversized := `{"projectId":"ao","attachments":[{"mimeType":"image/png","data":"` +
		strings.Repeat("A", 40<<20) + `"}]}`
	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions", oversized)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
}

func TestSessionsAPI_SpawnRejectsUnknownExplicitMode(t *testing.T) {
	svc := newFakeSessionService()
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions",
		`{"projectId":"ao","kind":"worker","harness":"codex","prompt":"fix","mode":"tuii"}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "SESSION_MODE_INVALID")
	if len(svc.sessions) != 1 {
		t.Fatalf("invalid mode created a session: %#v", svc.sessions)
	}
}

func TestSessionsAPI_SpawnsOMPChat(t *testing.T) {
	svc := newFakeSessionService()
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/sessions",
		`{"projectId":"ao","harness":"omp","mode":"chat","prompt":"fix"}`)
	if status != http.StatusCreated {
		t.Fatalf("spawn OMP Chat = %d, want 201; body=%s", status, body)
	}
	if svc.lastSpawn.Harness != domain.HarnessOMP || svc.lastSpawn.RequestedMode != domain.SessionModeChat {
		t.Fatalf("spawn config = %#v, want OMP Chat", svc.lastSpawn)
	}
}

func TestSessionsAPI_SpawnPassesModelToService(t *testing.T) {
	svc := newFakeSessionService()
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions",
		`{"projectId":"ao","kind":"worker","harness":"codex","prompt":"fix","displayName":"my worker","model":"sonnet"}`)
	if status != http.StatusCreated {
		t.Fatalf("POST session = %d, want 201; body=%s", status, body)
	}
	if svc.lastSpawn.AgentConfig.Model != "sonnet" {
		t.Fatalf("service AgentConfig.Model = %q, want sonnet", svc.lastSpawn.AgentConfig.Model)
	}
}

func TestSessionsAPI_OrchestratorAcceptsExplicitChatMode(t *testing.T) {
	svc := newFakeSessionService()
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/orchestrators",
		`{"projectId":"ao","mode":"chat"}`)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", status, body)
	}
	if svc.orchestratorMode != domain.SessionModeChat {
		t.Fatalf("requested mode = %q, want chat", svc.orchestratorMode)
	}
}

func TestSessionsAPI_OrchestratorRejectsUnknownExplicitMode(t *testing.T) {
	svc := newFakeSessionService()
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/orchestrators",
		`{"projectId":"ao","mode":"chatt"}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "SESSION_MODE_INVALID")
}

func TestSessionsAPI_PreviewDiscoversAndServesStaticIndex(t *testing.T) {
	svc := newFakeSessionService()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "index.html"), []byte(`<link rel="stylesheet" href="styles.css"><script src="app.js"></script>`), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "styles.css"), []byte(`body { color: red; }`), 0o644); err != nil {
		t.Fatalf("write css: %v", err)
	}
	s := svc.sessions["ao-1"]
	s.Metadata = domain.SessionMetadata{WorkspacePath: workspace}
	svc.sessions["ao-1"] = s
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "GET", "/api/v1/sessions/ao-1/preview", "")
	if status != http.StatusOK {
		t.Fatalf("preview = %d, want 200; body=%s", status, body)
	}
	var preview struct {
		SessionID  string `json:"sessionId"`
		PreviewURL string `json:"previewUrl"`
		Entry      string `json:"entry"`
	}
	mustJSON(t, body, &preview)
	if preview.SessionID != "ao-1" || preview.Entry != "index.html" || preview.PreviewURL == "" {
		t.Fatalf("preview response = %#v", preview)
	}
	if strings.Contains(preview.PreviewURL, workspace) {
		t.Fatalf("preview leaked workspace path: %s", preview.PreviewURL)
	}
	parsed, err := url.Parse(preview.PreviewURL)
	if err != nil {
		t.Fatalf("parse preview URL: %v", err)
	}
	if !strings.HasSuffix(parsed.Hostname(), ".localhost") || parsed.Path != "/index.html" {
		t.Fatalf("preview URL = %q, want isolated localhost origin ending in /index.html", preview.PreviewURL)
	}
	body, status, headers := doPreviewOriginRequest(t, srv, preview.PreviewURL, "/")
	if status != http.StatusOK {
		t.Fatalf("preview file = %d, want 200; body=%s", status, body)
	}
	if !strings.Contains(headers.Get("Content-Type"), "text/html") {
		t.Fatalf("content type = %q, want text/html", headers.Get("Content-Type"))
	}
	if !strings.Contains(string(body), "styles.css") {
		t.Fatalf("preview body did not serve index: %s", body)
	}
}

func TestSessionsAPI_PreviewRejectsSessionIDTooLongForHostname(t *testing.T) {
	svc := newFakeSessionService()
	id := domain.SessionID(strings.Repeat("x", 143))
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "index.html"), []byte("preview"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	svc.sessions[id] = domain.Session{SessionRecord: domain.SessionRecord{ID: id, Kind: domain.KindWorker, Metadata: domain.SessionMetadata{WorkspacePath: workspace}}}
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/sessions/"+string(id)+"/preview", "")
	assertErrorCode(t, body, status, http.StatusUnprocessableEntity, "PREVIEW_SESSION_ID_UNSUPPORTED")
}

func TestSessionsAPI_SetPreviewExplicitURLPersists(t *testing.T) {
	svc := newFakeSessionService()
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/preview", `{"url":"http://localhost:5173/"}`)
	if status != http.StatusOK {
		t.Fatalf("set preview = %d, want 200; body=%s", status, body)
	}
	var resp struct {
		Session struct {
			PreviewURL string `json:"previewUrl"`
		} `json:"session"`
	}
	mustJSON(t, body, &resp)
	if resp.Session.PreviewURL != "http://localhost:5173/" {
		t.Fatalf("response previewUrl = %q, want explicit url", resp.Session.PreviewURL)
	}
	if got := svc.sessions["ao-1"].Metadata.PreviewURL; got != "http://localhost:5173/" {
		t.Fatalf("persisted previewUrl = %q, want explicit url", got)
	}
}

func TestSessionsAPI_SetPreviewEmptyURLAutodetectsIndex(t *testing.T) {
	svc := newFakeSessionService()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "index.html"), []byte(`<html></html>`), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	s := svc.sessions["ao-1"]
	s.Metadata = domain.SessionMetadata{WorkspacePath: workspace}
	svc.sessions["ao-1"] = s
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/preview", `{}`)
	if status != http.StatusOK {
		t.Fatalf("set preview = %d, want 200; body=%s", status, body)
	}
	var resp struct {
		Session struct {
			PreviewURL string `json:"previewUrl"`
		} `json:"session"`
	}
	mustJSON(t, body, &resp)
	if !strings.Contains(resp.Session.PreviewURL, "/index.html") {
		t.Fatalf("response previewUrl = %q, want autodetected index.html URL", resp.Session.PreviewURL)
	}
	if strings.Contains(resp.Session.PreviewURL, workspace) {
		t.Fatalf("preview leaked workspace path: %s", resp.Session.PreviewURL)
	}
}

func TestSessionsAPI_SetPreviewEmptyURLPrefersWorkspaceEntryOverExistingTarget(t *testing.T) {
	svc := newFakeSessionService()
	workspace := t.TempDir()
	// An index.html exists, so bare `ao preview` returns to the workspace entry
	// instead of sticking to the last explicit target.
	if err := os.WriteFile(filepath.Join(workspace, "index.html"), []byte(`<html></html>`), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	s := svc.sessions["ao-1"]
	s.Metadata = domain.SessionMetadata{WorkspacePath: workspace, PreviewURL: "http://localhost:4321/docs"}
	svc.sessions["ao-1"] = s
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/preview", `{}`)
	if status != http.StatusOK {
		t.Fatalf("set preview = %d, want 200; body=%s", status, body)
	}
	var resp struct {
		Session struct {
			PreviewURL string `json:"previewUrl"`
		} `json:"session"`
	}
	mustJSON(t, body, &resp)
	if !strings.HasSuffix(resp.Session.PreviewURL, "/index.html") {
		t.Fatalf("response previewUrl = %q, want workspace index preview URL", resp.Session.PreviewURL)
	}
}

func TestSessionsAPI_SetPreviewEmptyURLNormalizesExistingRelativeTarget(t *testing.T) {
	svc := newFakeSessionService()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "index.html"), []byte(`<html></html>`), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	s := svc.sessions["ao-1"]
	s.Metadata = domain.SessionMetadata{WorkspacePath: workspace, PreviewURL: "index.html"}
	svc.sessions["ao-1"] = s
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/preview", `{}`)
	if status != http.StatusOK {
		t.Fatalf("set preview = %d, want 200; body=%s", status, body)
	}
	var resp struct {
		Session struct {
			PreviewURL string `json:"previewUrl"`
		} `json:"session"`
	}
	mustJSON(t, body, &resp)
	if !strings.HasSuffix(resp.Session.PreviewURL, "/index.html") {
		t.Fatalf("response previewUrl = %q, want index.html preview URL", resp.Session.PreviewURL)
	}
	if got := svc.sessions["ao-1"].Metadata.PreviewURL; got != resp.Session.PreviewURL {
		t.Fatalf("persisted previewUrl = %q, want normalized response URL %q", got, resp.Session.PreviewURL)
	}
}

func TestSessionsAPI_SetPreviewEmptyURLReusesExistingTargetWhenNoEntryExists(t *testing.T) {
	svc := newFakeSessionService()
	s := svc.sessions["ao-1"]
	s.Metadata = domain.SessionMetadata{WorkspacePath: t.TempDir(), PreviewURL: "http://localhost:4321/docs"}
	svc.sessions["ao-1"] = s
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/preview", `{}`)
	if status != http.StatusOK {
		t.Fatalf("set preview = %d, want 200; body=%s", status, body)
	}
	var resp struct {
		Session struct {
			PreviewURL string `json:"previewUrl"`
		} `json:"session"`
	}
	mustJSON(t, body, &resp)
	if resp.Session.PreviewURL != "http://localhost:4321/docs" {
		t.Fatalf("response previewUrl = %q, want reused existing target", resp.Session.PreviewURL)
	}
}

func TestSessionsAPI_SetPreviewLocalRelativePathResolvesToPreviewOrigin(t *testing.T) {
	svc := newFakeSessionService()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "dist"), 0o755); err != nil {
		t.Fatalf("mkdir dist: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dist", "index.html"), []byte(`<html></html>`), 0o644); err != nil {
		t.Fatalf("write dist index: %v", err)
	}
	s := svc.sessions["ao-1"]
	s.Metadata = domain.SessionMetadata{WorkspacePath: workspace}
	svc.sessions["ao-1"] = s
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/preview", `{"url":"./dist/index.html"}`)
	if status != http.StatusOK {
		t.Fatalf("set preview = %d, want 200; body=%s", status, body)
	}
	var resp struct {
		Session struct {
			PreviewURL string `json:"previewUrl"`
		} `json:"session"`
	}
	mustJSON(t, body, &resp)
	if !strings.HasSuffix(resp.Session.PreviewURL, "/dist/index.html") {
		t.Fatalf("response previewUrl = %q, want dist/index.html preview URL", resp.Session.PreviewURL)
	}
	if strings.Contains(resp.Session.PreviewURL, workspace) {
		t.Fatalf("preview leaked workspace path: %s", resp.Session.PreviewURL)
	}
	// The resolved preview origin actually serves the local file.
	fileBody, fileStatus, _ := doPreviewOriginRequest(t, srv, resp.Session.PreviewURL, "/")
	if fileStatus != http.StatusOK {
		t.Fatalf("serve local file = %d, want 200; body=%s", fileStatus, fileBody)
	}
}

func TestSessionsAPI_SetPreviewServesBrowserDisplayableArtifacts(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		contents    []byte
		contentType string
	}{
		{name: "PDF", path: "artifacts/report.pdf", contents: []byte("%PDF-1.4\n%%EOF\n"), contentType: "application/pdf"},
		{name: "PNG", path: "artifacts/mockup.png", contents: []byte("\x89PNG\r\n\x1a\n"), contentType: "image/png"},
		{name: "SVG", path: "artifacts/diagram.svg", contents: []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), contentType: "image/svg+xml"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := newFakeSessionService()
			workspace := t.TempDir()
			artifact := filepath.Join(workspace, filepath.FromSlash(tc.path))
			if err := os.MkdirAll(filepath.Dir(artifact), 0o755); err != nil {
				t.Fatalf("mkdir artifact dir: %v", err)
			}
			if err := os.WriteFile(artifact, tc.contents, 0o644); err != nil {
				t.Fatalf("write artifact: %v", err)
			}
			session := svc.sessions["ao-1"]
			session.Metadata = domain.SessionMetadata{WorkspacePath: workspace}
			svc.sessions["ao-1"] = session
			srv := newSessionTestServer(t, svc)

			request := `{"url":` + strconv.Quote(tc.path) + `}`
			body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/sessions/ao-1/preview", request)
			if status != http.StatusOK {
				t.Fatalf("set artifact preview = %d body=%s", status, body)
			}
			var response struct {
				Session struct {
					PreviewURL string `json:"previewUrl"`
				} `json:"session"`
			}
			mustJSON(t, body, &response)
			if !strings.HasSuffix(response.Session.PreviewURL, "/"+tc.path) {
				t.Fatalf("preview URL = %q, want suffix /%s", response.Session.PreviewURL, tc.path)
			}

			served, servedStatus, headers := doPreviewOriginRequest(t, srv, response.Session.PreviewURL, "/")
			if servedStatus != http.StatusOK {
				t.Fatalf("serve artifact = %d body=%q", servedStatus, served)
			}
			if got := headers.Get("Content-Type"); !strings.HasPrefix(got, tc.contentType) {
				t.Fatalf("Content-Type = %q, want %q", got, tc.contentType)
			}
		})
	}
}

func TestSessionsAPI_PreviewOriginResolvesRootRelativeAssetsFromEntryDirectory(t *testing.T) {
	svc := newFakeSessionService()
	workspace := t.TempDir()
	for _, dir := range []string{
		filepath.Join(workspace, "dist", "assets"),
		filepath.Join(workspace, "dist", "fonts"),
		filepath.Join(workspace, "assets"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	files := map[string]string{
		filepath.Join(workspace, "dist", "index.html"):         `<link rel="stylesheet" href="/assets/app.css"><script type="module" src="/assets/app.js"></script>`,
		filepath.Join(workspace, "dist", "assets", "app.css"):  `@font-face { src: url("/fonts/app.woff2") }`,
		filepath.Join(workspace, "dist", "assets", "app.js"):   `document.body.dataset.loaded = "yes"`,
		filepath.Join(workspace, "dist", "fonts", "app.woff2"): "preview-font",
		// A conflicting workspace-root asset proves /assets is mounted relative
		// to the selected deployment directory, not the workspace root.
		filepath.Join(workspace, "assets", "app.css"): "wrong-root",
	}
	for file, contents := range files {
		if err := os.WriteFile(file, []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
	}
	s := svc.sessions["ao-1"]
	s.Metadata = domain.SessionMetadata{WorkspacePath: workspace}
	svc.sessions["ao-1"] = s
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/sessions/ao-1/preview", `{"url":"./dist/index.html"}`)
	if status != http.StatusOK {
		t.Fatalf("set preview = %d, want 200; body=%s", status, body)
	}
	var resp struct {
		Session struct {
			PreviewURL string `json:"previewUrl"`
		} `json:"session"`
	}
	mustJSON(t, body, &resp)

	for requestPath, want := range map[string]string{
		"/":                   `/assets/app.css`,
		"/assets/app.css":     `/fonts/app.woff2`,
		"/dist/assets/app.js": `dataset.loaded`,
		"/fonts/app.woff2":    `preview-font`,
	} {
		assetBody, assetStatus, _ := doPreviewOriginRequest(t, srv, resp.Session.PreviewURL, requestPath)
		if assetStatus != http.StatusOK {
			t.Errorf("GET %s = %d, want 200; body=%s", requestPath, assetStatus, assetBody)
			continue
		}
		if !strings.Contains(string(assetBody), want) {
			t.Errorf("GET %s body = %q, want content containing %q", requestPath, assetBody, want)
		}
	}

	// Retain the old API route as a compatibility surface for stored URLs and
	// external callers while new previews use the isolated origin.
	legacyBody, legacyStatus, _ := doRequest(t, srv, http.MethodGet, "/api/v1/sessions/ao-1/preview/files/dist/assets/app.css", "")
	if legacyStatus != http.StatusOK || !strings.Contains(string(legacyBody), "/fonts/app.woff2") {
		t.Fatalf("legacy preview route = %d, body=%q; want existing file response", legacyStatus, legacyBody)
	}
}

func TestSessionsAPI_PreviewFileServesCanonicalAttachmentWithoutWorkspace(t *testing.T) {
	dataDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "ao-1")
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		t.Fatal(err)
	}
	want := []byte("durable-image-bytes")
	store := attachmentstore.New(dataDir)
	if err := store.Put(context.Background(), "ao-1", workspace, "attachment-durable.png", want); err != nil {
		t.Fatalf("Put attachment: %v", err)
	}
	if err := os.RemoveAll(workspace); err != nil {
		t.Fatal(err)
	}

	svc := newFakeSessionService()
	session := svc.sessions["ao-1"]
	session.Metadata = domain.SessionMetadata{WorkspacePath: workspace}
	svc.sessions["ao-1"] = session
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(
		config.Config{DataDir: dataDir}, log, nil, httpd.APIDeps{Sessions: svc}, httpd.ControlDeps{},
	))
	t.Cleanup(srv.Close)

	body, status, _ := doRequest(t, srv, http.MethodGet,
		"/api/v1/sessions/ao-1/preview/files/.ao/attachments/attachment-durable.png?cache-bust=1", "")
	if status != http.StatusOK {
		t.Fatalf("canonical attachment response = %d, body=%s", status, body)
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("canonical attachment body = %q, want %q", body, want)
	}
}

func TestSessionsAPI_PreviewFileDoesNotCrossSessionThroughCanonicalSymlink(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	name := "attachment-private.png"
	store := attachmentstore.New(dataDir)
	if err := store.Put(context.Background(), "ao-1", workspace, name, []byte("session-one-bytes")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ao-1", filepath.Join(dataDir, "attachments", "ao-2")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	svc := newFakeSessionService()
	second := svc.sessions["ao-1"]
	second.ID = "ao-2"
	second.Metadata.WorkspacePath = t.TempDir()
	svc.sessions[second.ID] = second
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(
		config.Config{DataDir: dataDir}, log, nil, httpd.APIDeps{Sessions: svc}, httpd.ControlDeps{},
	))
	t.Cleanup(srv.Close)

	body, status, _ := doRequest(t, srv, http.MethodGet,
		"/api/v1/sessions/ao-2/preview/files/.ao/attachments/"+name, "")
	if status != http.StatusNotFound || !bytes.Contains(body, []byte(`"code":"PREVIEW_FILE_NOT_FOUND"`)) {
		t.Fatalf("cross-session preview = %d, %s; want PREVIEW_FILE_NOT_FOUND", status, body)
	}
}

func TestSessionsAPI_PreviewOriginErrorContract(t *testing.T) {
	svc := newFakeSessionService()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "index.html"), []byte("preview"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	s := svc.sessions["ao-1"]
	s.Metadata = domain.SessionMetadata{WorkspacePath: workspace}
	svc.sessions["ao-1"] = s
	srv := newSessionTestServer(t, svc)
	validURL := mustPreviewFileURL(t, srv, "ao-1", "index.html")

	tests := []struct {
		name       string
		method     string
		previewURL string
		path       string
		wantStatus int
		wantCode   string
		wantAllow  string
	}{
		{name: "method", method: http.MethodPost, previewURL: validURL, path: "/", wantStatus: http.StatusMethodNotAllowed, wantCode: "METHOD_NOT_ALLOWED", wantAllow: "GET, HEAD"},
		{name: "unknown session", method: http.MethodGet, previewURL: mustPreviewFileURL(t, srv, "ao-missing", "index.html"), path: "/", wantStatus: http.StatusNotFound, wantCode: "SESSION_NOT_FOUND"},
		{name: "missing asset", method: http.MethodGet, previewURL: validURL, path: "/missing.css", wantStatus: http.StatusNotFound, wantCode: "PREVIEW_FILE_NOT_FOUND"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, status, headers := doPreviewOriginMethod(t, srv, tc.method, tc.previewURL, tc.path)
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", status, tc.wantStatus, body)
			}
			if got := headers.Get("Allow"); got != tc.wantAllow {
				t.Fatalf("Allow = %q, want %q", got, tc.wantAllow)
			}
			var got struct {
				Code      string `json:"code"`
				RequestID string `json:"requestId"`
			}
			mustJSON(t, body, &got)
			if got.Code != tc.wantCode || got.RequestID == "" {
				t.Fatalf("error = %#v, want code %q and requestId", got, tc.wantCode)
			}
		})
	}

	empty := t.TempDir()
	s = svc.sessions["ao-1"]
	s.Metadata = domain.SessionMetadata{WorkspacePath: empty}
	svc.sessions["ao-1"] = s
	body, status, _ := doPreviewOriginRequest(t, srv, validURL, "/")
	assertErrorCode(t, body, status, http.StatusNotFound, "NO_PREVIEW_ENTRY")
}

func TestSessionsAPI_PreviewRoutesRejectSymlinkOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.css")
	if err := os.WriteFile(filepath.Join(workspace, "index.html"), []byte("preview"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(outside, []byte("must-not-leak"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	link := filepath.Join(workspace, "escape.css")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	svc := newFakeSessionService()
	s := svc.sessions["ao-1"]
	s.Metadata = domain.SessionMetadata{WorkspacePath: workspace}
	svc.sessions["ao-1"] = s
	srv := newSessionTestServer(t, svc)
	previewURL := mustPreviewFileURL(t, srv, "ao-1", "index.html")

	for _, tc := range []struct {
		name string
		do   func() ([]byte, int, http.Header)
	}{
		{name: "isolated origin", do: func() ([]byte, int, http.Header) {
			return doPreviewOriginRequest(t, srv, previewURL, "/escape.css")
		}},
		{name: "legacy route", do: func() ([]byte, int, http.Header) {
			return doRequest(t, srv, http.MethodGet, "/api/v1/sessions/ao-1/preview/files/escape.css", "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, status, _ := tc.do()
			assertErrorCode(t, body, status, http.StatusNotFound, "PREVIEW_FILE_NOT_FOUND")
			if strings.Contains(string(body), "must-not-leak") {
				t.Fatalf("response leaked file outside workspace: %s", body)
			}
		})
	}
}

func mustPreviewFileURL(t *testing.T, srv *httptest.Server, id domain.SessionID, entry string) string {
	t.Helper()
	raw, err := previewutil.FileURL(srv.URL, id, entry)
	if err != nil {
		t.Fatalf("FileURL: %v", err)
	}
	return raw
}

func TestSessionsAPI_PreviewOriginsIsolateConcurrentSessionsAndSurviveRouterRestart(t *testing.T) {
	svc := newFakeSessionService()
	previewURLs := make(map[domain.SessionID]string)
	for _, tc := range []struct {
		id      domain.SessionID
		content string
	}{{id: "ao-1", content: "session-one"}, {id: "ao-2", content: "session-two"}} {
		workspace := t.TempDir()
		if err := os.WriteFile(filepath.Join(workspace, "index.html"), []byte(`<link rel="stylesheet" href="/theme.css">`), 0o644); err != nil {
			t.Fatalf("write index: %v", err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "theme.css"), []byte(tc.content), 0o644); err != nil {
			t.Fatalf("write theme: %v", err)
		}
		s := domain.Session{SessionRecord: domain.SessionRecord{ID: tc.id, Kind: domain.KindWorker}}
		s.Metadata = domain.SessionMetadata{WorkspacePath: workspace}
		svc.sessions[tc.id] = s
	}

	srv := newSessionTestServer(t, svc)
	for id := range svc.sessions {
		body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/sessions/"+string(id)+"/preview", `{}`)
		if status != http.StatusOK {
			t.Fatalf("set preview %s = %d, want 200; body=%s", id, status, body)
		}
		var resp struct {
			Session struct {
				PreviewURL string `json:"previewUrl"`
			} `json:"session"`
		}
		mustJSON(t, body, &resp)
		previewURLs[id] = resp.Session.PreviewURL
	}

	for id, want := range map[domain.SessionID]string{"ao-1": "session-one", "ao-2": "session-two"} {
		body, status, _ := doPreviewOriginRequest(t, srv, previewURLs[id], "/theme.css")
		if status != http.StatusOK || string(body) != want {
			t.Fatalf("session %s asset = %d, %q; want 200, %q", id, status, body, want)
		}
	}

	// A new router has no in-memory preview registry. The persisted URL and
	// session workspace are sufficient to reconstruct the same virtual root.
	restarted := newSessionTestServer(t, svc)
	body, status, _ := doPreviewOriginRequest(t, restarted, previewURLs["ao-1"], "/theme.css")
	if status != http.StatusOK || string(body) != "session-one" {
		t.Fatalf("asset after router restart = %d, %q; want 200, session-one", status, body)
	}
}

func TestSessionsAPI_SetPreviewAbsoluteWorkspaceFileUsesConfinedOrigin(t *testing.T) {
	svc := newFakeSessionService()
	workspace := t.TempDir()
	file := filepath.Join(workspace, "implementation_plan.html")
	if err := os.WriteFile(file, []byte(`<html>workspace preview</html>`), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	s := svc.sessions["ao-1"]
	s.Metadata.WorkspacePath = workspace
	svc.sessions["ao-1"] = s
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/preview", `{"url":`+strconv.Quote(file)+`}`)
	if status != http.StatusOK {
		t.Fatalf("set preview = %d, want 200; body=%s", status, body)
	}
	var resp struct {
		Session struct {
			PreviewURL string `json:"previewUrl"`
		} `json:"session"`
	}
	mustJSON(t, body, &resp)
	parsed, err := url.Parse(resp.Session.PreviewURL)
	if err != nil {
		t.Fatalf("parse preview url: %v", err)
	}
	if parsed.Scheme != "http" || !strings.HasPrefix(parsed.Hostname(), "ao-preview.") {
		t.Fatalf("previewUrl = %q, want confined preview origin", resp.Session.PreviewURL)
	}
	previewBody, previewStatus, _ := doPreviewOriginRequest(t, srv, resp.Session.PreviewURL, "/")
	if previewStatus != http.StatusOK || string(previewBody) != `<html>workspace preview</html>` {
		t.Fatalf("workspace preview = %d, %q; want 200 and workspace file", previewStatus, previewBody)
	}
}

func TestSessionsAPI_SetPreviewRejectsAbsoluteFilesOutsideWorkspace(t *testing.T) {
	svc := newFakeSessionService()
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.html")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	s := svc.sessions["ao-1"]
	s.Metadata.WorkspacePath = workspace
	s.Metadata.PreviewURL = "http://localhost:4321/docs"
	svc.sessions["ao-1"] = s
	srv := newSessionTestServer(t, svc)

	fileURLPath := filepath.ToSlash(outside)
	if filepath.VolumeName(outside) != "" {
		fileURLPath = "/" + fileURLPath
	}
	fileURL := (&url.URL{Scheme: "file", Path: fileURLPath}).String()
	for _, target := range []string{outside, fileURL} {
		body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/sessions/ao-1/preview", `{"url":`+strconv.Quote(target)+`}`)
		if status != http.StatusForbidden || !bytes.Contains(body, []byte(`"code":"PREVIEW_FILE_OUTSIDE_WORKSPACE"`)) {
			t.Fatalf("set outside preview %q = %d, body=%s; want 403 workspace error", target, status, body)
		}
	}
	if got := svc.sessions["ao-1"].Metadata.PreviewURL; got != "http://localhost:4321/docs" {
		t.Fatalf("persisted previewUrl = %q, want existing target preserved", got)
	}
}

func TestSessionsAPI_SetPreviewRejectsRelativeParentTraversal(t *testing.T) {
	svc := newFakeSessionService()
	s := svc.sessions["ao-1"]
	s.Metadata.WorkspacePath = t.TempDir()
	s.Metadata.PreviewURL = "http://localhost:4321/docs"
	svc.sessions["ao-1"] = s
	srv := newSessionTestServer(t, svc)

	for _, target := range []string{"../README.md", "docs/../../README.md", `..\README.md`} {
		body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/sessions/ao-1/preview", `{"url":`+strconv.Quote(target)+`}`)
		if status != http.StatusForbidden || !bytes.Contains(body, []byte(`"code":"PREVIEW_FILE_OUTSIDE_WORKSPACE"`)) {
			t.Fatalf("set parent traversal preview %q = %d, body=%s; want 403 workspace error", target, status, body)
		}
	}
	if got := svc.sessions["ao-1"].Metadata.PreviewURL; got != "http://localhost:4321/docs" {
		t.Fatalf("persisted previewUrl = %q, want existing target preserved", got)
	}
}

func TestSessionsAPI_SetPreviewRejectsWorkspaceSymlinkEscape(t *testing.T) {
	svc := newFakeSessionService()
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.html")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	escape := filepath.Join(workspace, "escape.html")
	if err := os.Symlink(outside, escape); err != nil {
		if runtime.GOOS == "windows" || errors.Is(err, os.ErrPermission) {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatalf("create symlink escape: %v", err)
	}
	s := svc.sessions["ao-1"]
	s.Metadata.WorkspacePath = workspace
	svc.sessions["ao-1"] = s
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/sessions/ao-1/preview", `{"url":`+strconv.Quote(escape)+`}`)
	if status != http.StatusForbidden || !bytes.Contains(body, []byte(`"code":"PREVIEW_FILE_OUTSIDE_WORKSPACE"`)) {
		t.Fatalf("set symlink escape = %d, body=%s; want 403 workspace error", status, body)
	}
}

func TestSessionsAPI_SetPreviewMissingOrMalformedFileFailsWithoutOverwriting(t *testing.T) {
	svc := newFakeSessionService()
	workspace := t.TempDir()
	missing := filepath.Join(workspace, "implmentation_plan.html")
	s := svc.sessions["ao-1"]
	s.Metadata = domain.SessionMetadata{WorkspacePath: workspace, PreviewURL: "http://localhost:4321/docs"}
	svc.sessions["ao-1"] = s
	srv := newSessionTestServer(t, svc)

	for _, target := range []string{missing, "file:///%"} {
		body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/preview", `{"url":`+strconv.Quote(target)+`}`)
		if status != http.StatusNotFound || !bytes.Contains(body, []byte(`"code":"PREVIEW_FILE_NOT_FOUND"`)) {
			t.Fatalf("set unavailable file preview %q = %d, want 404; body=%s", target, status, body)
		}
	}
	if got := svc.sessions["ao-1"].Metadata.PreviewURL; got != "http://localhost:4321/docs" {
		t.Fatalf("persisted previewUrl = %q, want existing target preserved", got)
	}
}

func TestSessionsAPI_SetPreviewBumpsRevisionOnSameURL(t *testing.T) {
	svc := newFakeSessionService()
	srv := newSessionTestServer(t, svc)

	readRevision := func() int64 {
		body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/preview", `{"url":"http://localhost:5173/"}`)
		if status != http.StatusOK {
			t.Fatalf("set preview = %d, want 200; body=%s", status, body)
		}
		var resp struct {
			Session struct {
				PreviewRevision int64 `json:"previewRevision"`
			} `json:"session"`
		}
		mustJSON(t, body, &resp)
		return resp.Session.PreviewRevision
	}
	first := readRevision()
	second := readRevision()
	if second <= first {
		t.Fatalf("revision did not advance on same-URL re-run: first=%d second=%d", first, second)
	}
}

func TestSessionsAPI_ClearPreviewResetsURL(t *testing.T) {
	svc := newFakeSessionService()
	s := svc.sessions["ao-1"]
	s.Metadata = domain.SessionMetadata{PreviewURL: "http://localhost:5173/"}
	svc.sessions["ao-1"] = s
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "DELETE", "/api/v1/sessions/ao-1/preview", "")
	if status != http.StatusOK {
		t.Fatalf("clear preview = %d, want 200; body=%s", status, body)
	}
	var resp struct {
		Session struct {
			PreviewURL string `json:"previewUrl"`
		} `json:"session"`
	}
	mustJSON(t, body, &resp)
	if resp.Session.PreviewURL != "" {
		t.Fatalf("response previewUrl = %q, want empty after clear", resp.Session.PreviewURL)
	}
	if got := svc.sessions["ao-1"].Metadata.PreviewURL; got != "" {
		t.Fatalf("persisted previewUrl = %q, want empty after clear", got)
	}
}

func TestSessionsAPI_ClearPreviewNotFound(t *testing.T) {
	srv := newSessionTestServer(t, newFakeSessionService())

	body, status, _ := doRequest(t, srv, "DELETE", "/api/v1/sessions/missing-1/preview", "")
	assertErrorCode(t, body, status, http.StatusNotFound, "SESSION_NOT_FOUND")
}

func TestSessionsAPI_ManagedPreviewStartsExactApplicationAndPersistsTarget(t *testing.T) {
	svc := newFakeSessionService()
	session := svc.sessions["ao-1"]
	session.Metadata.WorkspacePath = t.TempDir()
	svc.sessions["ao-1"] = session
	managed := &fakeManagedPreviewServer{status: previewserver.Status{
		State:         previewserver.StateReady,
		Configuration: "web",
		TargetKind:    previewserver.TargetApp,
		URL:           "http://127.0.0.1:43123/",
		Port:          43123,
		Logs:          []string{"ready"},
	}}
	srv := newSessionTestServerWithPreview(t, svc, managed)

	body, status, _ := doRequest(
		t,
		srv,
		http.MethodPost,
		"/api/v1/sessions/ao-1/preview/server",
		`{"configuration":"web"}`,
	)
	if status != http.StatusOK || !containsAll(body, `"state":"ready"`, `"configuration":"web"`, `"targetKind":"app"`) {
		t.Fatalf("start managed preview = %d body=%s", status, body)
	}
	if managed.startName != "web" || managed.startWorkspace != session.Metadata.WorkspacePath {
		t.Fatalf("start args = name %q workspace %q", managed.startName, managed.startWorkspace)
	}
	if got := svc.sessions["ao-1"].Metadata.PreviewURL; got != managed.status.URL {
		t.Fatalf("persisted preview URL = %q, want %q", got, managed.status.URL)
	}
}

func TestSessionsAPI_ManagedPreviewRequiresOwningCapability(t *testing.T) {
	svc := newFakeSessionService()
	managed := &fakeManagedPreviewServer{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	deps := httpd.APIDeps{
		Sessions:            svc,
		PreviewServer:       managed,
		SessionCapabilities: denySessionCapability{},
	}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, deps, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/sessions/ao-1/preview/server", `{}`)
	assertErrorCode(t, body, status, http.StatusForbidden, "PREVIEW_CAPABILITY_INVALID")
	if managed.startName != "" {
		t.Fatal("preview process started without a valid capability")
	}
}

func TestSessionsAPI_APIManagedPreviewDoesNotTakeOverBrowser(t *testing.T) {
	svc := newFakeSessionService()
	session := svc.sessions["ao-1"]
	session.Metadata.WorkspacePath = t.TempDir()
	session.Metadata.PreviewURL = "http://127.0.0.1:4173/"
	svc.sessions["ao-1"] = session
	managed := &fakeManagedPreviewServer{status: previewserver.Status{
		State:         previewserver.StateReady,
		Configuration: "api",
		TargetKind:    previewserver.TargetAPI,
		URL:           "http://127.0.0.1:8080/health",
		Port:          8080,
		Logs:          []string{},
	}}
	srv := newSessionTestServerWithPreview(t, svc, managed)

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/sessions/ao-1/preview/server", `{}`)
	if status != http.StatusOK {
		t.Fatalf("start API preview = %d body=%s", status, body)
	}
	if got := svc.sessions["ao-1"].Metadata.PreviewURL; got != "http://127.0.0.1:4173/" {
		t.Fatalf("API server replaced browser target with %q", got)
	}
}

func TestSessionsAPI_StopManagedPreviewPreservesExplicitFileTarget(t *testing.T) {
	svc := newFakeSessionService()
	session := svc.sessions["ao-1"]
	session.Metadata.PreviewURL = "http://ao-1.preview.localhost:3001/README.md"
	svc.sessions["ao-1"] = session
	managed := &fakeManagedPreviewServer{status: previewserver.Status{
		State:      previewserver.StateReady,
		TargetKind: previewserver.TargetApp,
		URL:        "http://127.0.0.1:4173/",
		Logs:       []string{},
	}}
	srv := newSessionTestServerWithPreview(t, svc, managed)

	body, status, _ := doRequest(t, srv, http.MethodDelete, "/api/v1/sessions/ao-1/preview/server", "")
	if status != http.StatusOK || !containsAll(body, `"state":"stopped"`) {
		t.Fatalf("stop managed preview = %d body=%s", status, body)
	}
	if got := svc.sessions["ao-1"].Metadata.PreviewURL; got != session.Metadata.PreviewURL {
		t.Fatalf("explicit file target was cleared: %q", got)
	}
}

func TestSessionsAPI_StopManagedPreviewPreservesTargetChangedDuringStop(t *testing.T) {
	svc := newFakeSessionService()
	session := svc.sessions["ao-1"]
	session.Metadata.PreviewURL = "http://127.0.0.1:4173/"
	svc.sessions["ao-1"] = session
	managed := &fakeManagedPreviewServer{status: previewserver.Status{
		State:      previewserver.StateReady,
		TargetKind: previewserver.TargetApp,
		URL:        session.Metadata.PreviewURL,
		Logs:       []string{},
	}}
	managed.onStop = func() {
		current := svc.sessions["ao-1"]
		current.Metadata.PreviewURL = "http://127.0.0.1:5173/"
		svc.sessions["ao-1"] = current
	}
	srv := newSessionTestServerWithPreview(t, svc, managed)

	body, status, _ := doRequest(t, srv, http.MethodDelete, "/api/v1/sessions/ao-1/preview/server", "")
	if status != http.StatusOK || !containsAll(body, `"state":"stopped"`) {
		t.Fatalf("stop managed preview = %d body=%s", status, body)
	}
	if got := svc.sessions["ao-1"].Metadata.PreviewURL; got != "http://127.0.0.1:5173/" {
		t.Fatalf("target changed during stop was cleared: %q", got)
	}
}

func TestSessionsAPI_KillLeavesPreviewTeardownToSessionLifecycle(t *testing.T) {
	svc := newFakeSessionService()
	managed := &fakeManagedPreviewServer{}
	srv := newSessionTestServerWithPreview(t, svc, managed)

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/sessions/ao-1/kill", "")
	if status != http.StatusOK {
		t.Fatalf("kill = %d body=%s", status, body)
	}
	if managed.stopCalls != 0 {
		t.Fatalf("controller duplicated managed preview teardown: stop calls = %d", managed.stopCalls)
	}
}

func TestSessionsAPI_ListWorkspaceFiles(t *testing.T) {
	svc := newFakeSessionService()
	svc.workspaceFiles = sessionsvc.WorkspaceFiles{
		SessionID:      "ao-1",
		CompareBaseSHA: "base-sha",
		CompareBaseRef: "main",
		CompareMode:    sessionsvc.WorkspaceCompareBase,
		Files: []sessionsvc.WorkspaceFileSummary{
			{Path: "README.md", Status: sessionsvc.WorkspaceFileModified, Additions: 2, Deletions: 1, Size: 48},
			{Path: "notes.txt", PreviousPath: "old-notes.txt", Status: sessionsvc.WorkspaceFileRenamed, Additions: 1, Size: 11},
		},
	}
	srv := newSessionTestServer(t, svc)

	body, status, headers := doRequest(t, srv, "GET", "/api/v1/sessions/ao-1/workspace/files", "")
	assertJSON(t, headers)
	if status != http.StatusOK {
		t.Fatalf("GET workspace files = %d, want 200; body=%s", status, body)
	}
	var got struct {
		SessionID      string `json:"sessionId"`
		CompareBaseSHA string `json:"compareBaseSha"`
		CompareBaseRef string `json:"compareBaseRef"`
		CompareMode    string `json:"compareMode"`
		Files          []struct {
			Path         string `json:"path"`
			PreviousPath string `json:"previousPath"`
			Status       string `json:"status"`
			Additions    int    `json:"additions"`
			Deletions    int    `json:"deletions"`
			Size         int64  `json:"size"`
		} `json:"files"`
	}
	mustJSON(t, body, &got)
	if got.SessionID != "ao-1" || len(got.Files) != 2 {
		t.Fatalf("response = %#v", got)
	}
	if got.CompareMode != "base" || got.CompareBaseSHA != "base-sha" || got.CompareBaseRef != "main" {
		t.Fatalf("compare metadata = mode:%q sha:%q ref:%q", got.CompareMode, got.CompareBaseSHA, got.CompareBaseRef)
	}
	if got.Files[0].Path != "README.md" || got.Files[0].Status != "modified" || got.Files[0].Additions != 2 || got.Files[0].Deletions != 1 {
		t.Fatalf("first file = %#v", got.Files[0])
	}
	if got.Files[1].Path != "notes.txt" || got.Files[1].PreviousPath != "old-notes.txt" || got.Files[1].Status != "renamed" {
		t.Fatalf("second file = %#v", got.Files[1])
	}
}

func TestSessionsAPI_GetWorkspaceFile(t *testing.T) {
	svc := newFakeSessionService()
	svc.workspaceFile = sessionsvc.WorkspaceFileDetail{
		SessionID:      "ao-1",
		Path:           "README.md",
		PreviousPath:   "README.old.md",
		Status:         sessionsvc.WorkspaceFileModified,
		Additions:      1,
		Deletions:      1,
		Size:           14,
		Content:        "hello\nupdated\n",
		Diff:           "@@ -1 +1 @@\n-hello\n+updated\n",
		CompareBaseSHA: "base-sha",
		CompareBaseRef: "main",
		CompareMode:    sessionsvc.WorkspaceCompareBase,
	}
	srv := newSessionTestServer(t, svc)

	body, status, headers := doRequest(t, srv, "GET", "/api/v1/sessions/ao-1/workspace/file?path="+url.QueryEscape("README.md"), "")
	assertJSON(t, headers)
	if status != http.StatusOK {
		t.Fatalf("GET workspace file = %d, want 200; body=%s", status, body)
	}
	var got struct {
		SessionID      string `json:"sessionId"`
		Path           string `json:"path"`
		PreviousPath   string `json:"previousPath"`
		Content        string `json:"content"`
		Diff           string `json:"diff"`
		CompareBaseSHA string `json:"compareBaseSha"`
		CompareBaseRef string `json:"compareBaseRef"`
		CompareMode    string `json:"compareMode"`
	}
	mustJSON(t, body, &got)
	if got.SessionID != "ao-1" || got.Path != "README.md" || got.Content == "" || got.Diff == "" {
		t.Fatalf("response = %#v", got)
	}
	if got.PreviousPath != "README.old.md" || got.CompareMode != "base" || got.CompareBaseSHA != "base-sha" || got.CompareBaseRef != "main" {
		t.Fatalf("workspace file metadata = %#v", got)
	}
}

// TestSessionsAPI_GetWorkspaceFileSection verifies the section query param
// reaches the service unchanged, so a staged-section request and an
// unstaged-section request for the same path resolve to different diffs
// instead of colliding on one combined base..worktree diff.
func TestSessionsAPI_GetWorkspaceFileSection(t *testing.T) {
	svc := newFakeSessionService()
	srv := newSessionTestServer(t, svc)

	for _, section := range []sessionsvc.WorkspaceFileSection{sessionsvc.WorkspaceFileSectionStaged, sessionsvc.WorkspaceFileSectionUnstaged} {
		body, status, _ := doRequest(t, srv, "GET",
			"/api/v1/sessions/ao-1/workspace/file?path="+url.QueryEscape("README.md")+"&section="+string(section), "")
		if status != http.StatusOK {
			t.Fatalf("section=%s: GET workspace file = %d, want 200; body=%s", section, status, body)
		}
		if svc.workspaceFileSection != section {
			t.Fatalf("section=%s: service received section %q", section, svc.workspaceFileSection)
		}
	}
}

func TestSessionsAPI_GetWorkspaceFileRequiresPath(t *testing.T) {
	srv := newSessionTestServer(t, newFakeSessionService())

	body, status, headers := doRequest(t, srv, "GET", "/api/v1/sessions/ao-1/workspace/file", "")
	assertJSON(t, headers)
	assertErrorCode(t, body, status, http.StatusBadRequest, "WORKSPACE_PATH_REQUIRED")
}

func TestSessionsAPI_GetWorkspaceFileBlob(t *testing.T) {
	svc := newFakeSessionService()
	svc.workspaceBlob = sessionsvc.WorkspaceFileBlob{
		Path:      "docs/logo.png",
		MediaType: "image/png",
		Data:      []byte{0x89, 'P', 'N', 'G'},
	}
	srv := newSessionTestServer(t, svc)

	body, status, headers := doRequest(t, srv, "GET", "/api/v1/sessions/ao-1/workspace/file/blob?side=before&path="+url.QueryEscape("docs/logo.png"), "")
	if status != http.StatusOK {
		t.Fatalf("GET workspace blob = %d, want 200; body=%s", status, body)
	}
	if contentType := headers.Get("Content-Type"); contentType != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", contentType)
	}
	if cache := headers.Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cache)
	}
	if !bytes.Equal(body, []byte{0x89, 'P', 'N', 'G'}) {
		t.Fatalf("body = %q, want raw png bytes", body)
	}
	if svc.workspaceBlob.Side != "" {
		t.Fatalf("fake blob mutated: %#v", svc.workspaceBlob)
	}
}

func TestSessionsAPI_GetWorkspaceFileBlobRequiresPath(t *testing.T) {
	srv := newSessionTestServer(t, newFakeSessionService())

	body, status, headers := doRequest(t, srv, "GET", "/api/v1/sessions/ao-1/workspace/file/blob?side=after", "")
	assertJSON(t, headers)
	assertErrorCode(t, body, status, http.StatusBadRequest, "WORKSPACE_PATH_REQUIRED")
}

func TestSessionsAPI_StreamWorkspaceChanges(t *testing.T) {
	workspace := t.TempDir()
	svc := newFakeSessionService()
	session := svc.sessions["ao-1"]
	session.Metadata.WorkspacePath = workspace
	svc.sessions["ao-1"] = session
	srv := newSessionTestServer(t, svc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/sessions/ao-1/workspace/events", nil)
	if err != nil {
		t.Fatalf("new workspace stream request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET workspace stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET workspace stream = %d body=%s", resp.StatusCode, body)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", contentType)
	}

	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	event := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "event:") {
				event <- scanner.Text()
				return
			}
		}
	}()
	select {
	case got := <-event:
		if got != "event: workspace_changed" {
			t.Fatalf("event = %q, want workspace_changed", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for workspace change event")
	}
}

func TestSessionsAPI_SetPreviewEmptyURLNoEntry(t *testing.T) {
	svc := newFakeSessionService()
	s := svc.sessions["ao-1"]
	s.Metadata = domain.SessionMetadata{WorkspacePath: t.TempDir()}
	svc.sessions["ao-1"] = s
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/preview", `{}`)
	assertErrorCode(t, body, status, http.StatusNotFound, "NO_PREVIEW_ENTRY")
}

func TestSessionsAPI_SetPreviewNotFound(t *testing.T) {
	srv := newSessionTestServer(t, newFakeSessionService())

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/missing-1/preview", `{"url":"http://x"}`)
	assertErrorCode(t, body, status, http.StatusNotFound, "SESSION_NOT_FOUND")
}

func TestSessionsAPI_SpawnBranchNotFetchedReturnsTypedError(t *testing.T) {
	svc := newFakeSessionService()
	svc.spawnErr = apierr.Invalid("BRANCH_NOT_FETCHED", `workspace: branch is not fetched: "feature/missing"`, nil)
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions", `{"projectId":"ao","kind":"worker","branch":"feature/missing","prompt":"fix"}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "BRANCH_NOT_FETCHED")
}

// TestSessionsAPI_SpawnRejectsOverlongDisplayName asserts the spawn endpoint
// caps displayName at 20 characters even though the field itself is optional
// (the desktop new-task dialog omits it). `ao spawn` enforces the same limit
// CLI-side before the request is sent.
func TestSessionsAPI_SpawnRejectsOverlongDisplayName(t *testing.T) {
	srv := newSessionTestServer(t, newFakeSessionService())

	overlong := strings.Repeat("x", 21)
	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions", `{"projectId":"ao","harness":"codex","displayName":"`+overlong+`"}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "DISPLAY_NAME_TOO_LONG")
}

func TestSessionsAPI_RenameNotFound(t *testing.T) {
	srv := newSessionTestServer(t, newFakeSessionService())

	body, status, _ := doRequest(t, srv, "PATCH", "/api/v1/sessions/missing-1", `{"displayName":"Renamed"}`)
	assertErrorCode(t, body, status, http.StatusNotFound, "SESSION_NOT_FOUND")
}

func TestSessionsAPI_RenameValidation(t *testing.T) {
	srv := newSessionTestServer(t, newFakeSessionService())

	body, status, _ := doRequest(t, srv, "PATCH", "/api/v1/sessions/ao-1", `{"displayName":"  "}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "DISPLAY_NAME_REQUIRED")

	body, status, _ = doRequest(t, srv, "PATCH", "/api/v1/sessions/ao-1", `{`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
}

func TestSessionsAPI_ListOrchestratorsOnly(t *testing.T) {
	svc := newFakeSessionService()
	now := time.Now().UTC()
	svc.sessions["ao-orch"] = domain.Session{
		SessionRecord: domain.SessionRecord{
			ID:        "ao-orch",
			ProjectID: "ao",
			Kind:      domain.KindOrchestrator,
			Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
			CreatedAt: now,
			UpdatedAt: now,
		},
		Status: domain.StatusIdle,
	}
	svc.sessions["other-orch"] = domain.Session{
		SessionRecord: domain.SessionRecord{
			ID:        "other-orch",
			ProjectID: "other",
			Kind:      domain.KindOrchestrator,
			Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
			CreatedAt: now,
			UpdatedAt: now,
		},
		Status: domain.StatusIdle,
	}
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "GET", "/api/v1/orchestrators", "")
	if status != http.StatusOK {
		t.Fatalf("GET orchestrators = %d, want 200; body=%s", status, body)
	}
	var list struct {
		Sessions []sessionBody `json:"sessions"`
	}
	mustJSON(t, body, &list)
	if len(list.Sessions) != 2 {
		t.Fatalf("len(orchestrators) = %d, want 2; body=%s", len(list.Sessions), body)
	}
	got := map[string]string{}
	for _, sess := range list.Sessions {
		got[sess.ID] = sess.Kind
	}
	if got["ao-orch"] != string(domain.KindOrchestrator) || got["other-orch"] != string(domain.KindOrchestrator) {
		t.Fatalf("missing orchestrators: %#v", got)
	}
	if _, ok := got["ao-1"]; ok {
		t.Fatalf("worker session leaked into orchestrator list: %#v", got)
	}
}

func TestSessionsAPI_SendValidation(t *testing.T) {
	srv := newSessionTestServer(t, newFakeSessionService())

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/send", `{"message":""}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "MESSAGE_REQUIRED")
}

func TestSessionsAPI_DelegateTask(t *testing.T) {
	svc := newFakeSessionService()
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/orchestrators/delegate", `{"projectId":"ao","brief":"Fix\u0000 it","agent":"cursor","model":" sonnet-custom ","mode":"chat","approvalMode":"bypass-permissions","attachments":[{"mimeType":"image/png","data":"AQID"}]}`)
	if status != http.StatusAccepted {
		t.Fatalf("delegate = %d, want 202; body=%s", status, body)
	}
	var got struct {
		OK             bool   `json:"ok"`
		WorkerID       string `json:"workerId"`
		OrchestratorID string `json:"orchestratorId"`
	}
	mustJSON(t, body, &got)
	if !got.OK || got.WorkerID != "ao-worker" || got.OrchestratorID != "ao-orch" {
		t.Fatalf("response = %#v", got)
	}
	if svc.delegationInput.ProjectID != "ao" || svc.delegationInput.Brief != "Fix it" || svc.delegationInput.RequestedAgent != domain.HarnessCursor || svc.delegationInput.Model != "sonnet-custom" || svc.delegationInput.RequestedMode != domain.SessionModeChat || svc.delegationInput.ApprovalMode != domain.PermissionModeBypassPermissions {
		t.Fatalf("delegation input = %#v", svc.delegationInput)
	}
	if len(svc.delegationInput.Attachments) != 1 {
		t.Fatalf("attachments = %#v, want one", svc.delegationInput.Attachments)
	}
	if got := svc.delegationInput.Attachments[0]; got.Ext != ".png" || string(got.Data) != "\x01\x02\x03" {
		t.Fatalf("attachment = %#v, want decoded png", got)
	}
}

func TestSessionsAPI_DelegatesOMPChat(t *testing.T) {
	svc := newFakeSessionService()
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/orchestrators/delegate",
		`{"projectId":"ao","brief":"Fix it","agent":"omp","mode":"chat"}`)
	if status != http.StatusAccepted {
		t.Fatalf("delegate OMP Chat = %d, want 202; body=%s", status, body)
	}
	if svc.delegationInput.RequestedAgent != domain.HarnessOMP || svc.delegationInput.RequestedMode != domain.SessionModeChat {
		t.Fatalf("delegation input = %#v, want OMP Chat", svc.delegationInput)
	}
}

func TestSessionsAPI_DelegateTaskValidationAndServiceError(t *testing.T) {
	svc := newFakeSessionService()
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/orchestrators/delegate", `{"projectId":"ao","brief":""}`)
	if status != http.StatusAccepted {
		t.Fatalf("promptless delegate = %d, want 202; body=%s", status, body)
	}
	if svc.delegationInput.ProjectID != "ao" || svc.delegationInput.Brief != "" {
		t.Fatalf("promptless delegation input = %#v", svc.delegationInput)
	}

	svc.delegationErr = apierr.Invalid("UNKNOWN_HARNESS", "Unknown requested agent", nil)
	body, status, _ = doRequest(t, srv, "POST", "/api/v1/orchestrators/delegate", `{"projectId":"ao","brief":"Fix it"}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "UNKNOWN_HARNESS")

	svc.delegationErr = nil
	body, status, _ = doRequest(t, srv, "POST", "/api/v1/orchestrators/delegate", `{"projectId":"ao","brief":"Fix it","mode":"tuii"}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_SESSION_MODE")

	body, status, _ = doRequest(t, srv, "POST", "/api/v1/orchestrators/delegate", `{"projectId":"ao","brief":"Fix it","approvalMode":"sometimes"}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_APPROVAL_MODE")
	var approvalError struct {
		Error   string `json:"error"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	mustJSON(t, body, &approvalError)
	if approvalError.Error != "bad_request" || approvalError.Code != "INVALID_APPROVAL_MODE" || approvalError.Message != "approvalMode is invalid" {
		t.Fatalf("invalid approval envelope = %#v", approvalError)
	}
}

func TestSessionsAPI_DelegateTaskRejectsInvalidAttachments(t *testing.T) {
	tests := []struct {
		name string
		body string
		code string
	}{
		{
			name: "bad base64",
			body: `{"projectId":"ao","brief":"Fix it","attachments":[{"mimeType":"image/png","data":"!!!"}]}`,
			code: "INVALID_ATTACHMENT_DATA",
		},
		{
			name: "empty base64",
			body: `{"projectId":"ao","brief":"Fix it","attachments":[{"mimeType":"image/png","data":""}]}`,
			code: "INVALID_ATTACHMENT_DATA",
		},
		{
			name: "svg",
			body: `{"projectId":"ao","brief":"Fix it","attachments":[{"mimeType":"image/svg+xml","data":"PHN2Zy8+"}]}`,
			code: "UNSUPPORTED_ATTACHMENT_TYPE",
		},
		{
			name: "too large",
			body: `{"projectId":"ao","brief":"Fix it","attachments":[{"mimeType":"image/png","data":"` +
				base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", (10<<20)+1))) + `"}]}`,
			code: "ATTACHMENT_TOO_LARGE",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := newFakeSessionService()
			srv := newSessionTestServer(t, svc)
			body, status, _ := doRequest(t, srv, "POST", "/api/v1/orchestrators/delegate", tc.body)
			assertErrorCode(t, body, status, http.StatusBadRequest, tc.code)
		})
	}
}

func TestSessionsAPI_DelegateTaskRejectsOversizedBody(t *testing.T) {
	svc := newFakeSessionService()
	srv := newSessionTestServer(t, svc)

	// A body past the spawn attachment cap is rejected while decoding
	// (MaxBytesReader), before attachment size validation and without
	// materializing the whole body.
	oversized := `{"projectId":"ao","brief":"Fix it","attachments":[{"mimeType":"image/png","data":"` +
		strings.Repeat("A", 40<<20) + `"}]}`
	body, status, _ := doRequest(t, srv, "POST", "/api/v1/orchestrators/delegate", oversized)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
	if svc.delegationInput.ProjectID != "" {
		t.Fatalf("delegate service called with oversized body: %#v", svc.delegationInput)
	}
}

func TestSessionsAPI_SendWithAttachment(t *testing.T) {
	svc := newFakeSessionService()
	srv := newSessionTestServer(t, svc)

	reqBody := `{"message":"Make the button blue.","attachment":{"mimeType":"image/png","data":"` + base64.StdEncoding.EncodeToString([]byte("snapshot")) + `"}}`
	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/send", reqBody)
	if status != http.StatusOK {
		t.Fatalf("send = %d, want 200; body=%s", status, body)
	}
	if svc.sent != "Make the button blue." {
		t.Fatalf("sent message = %q, want unchanged (attachment referencing happens in the manager)", svc.sent)
	}
	if svc.sentAttachment == nil {
		t.Fatal("sentAttachment is nil, want the decoded attachment")
	}
	if svc.sentAttachment.Ext != ".png" || string(svc.sentAttachment.Data) != "snapshot" {
		t.Fatalf("sentAttachment = %+v, want Ext=.png Data=snapshot", svc.sentAttachment)
	}
}

func TestSessionsAPI_SendRejectsUnsupportedAttachmentType(t *testing.T) {
	srv := newSessionTestServer(t, newFakeSessionService())

	reqBody := `{"message":"Make the button blue.","attachment":{"mimeType":"image/svg+xml","data":"` + base64.StdEncoding.EncodeToString([]byte("<svg/>")) + `"}}`
	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/send", reqBody)
	assertErrorCode(t, body, status, http.StatusBadRequest, "UNSUPPORTED_ATTACHMENT_TYPE")
}

func TestSessionsAPI_SendRejectsOversizedBody(t *testing.T) {
	svc := newFakeSessionService()
	srv := newSessionTestServer(t, svc)

	// A body past the send attachment cap is rejected while decoding
	// (MaxBytesReader), before attachment size validation and without
	// materializing the whole body.
	oversized := `{"message":"Make the button blue.","attachment":{"mimeType":"image/png","data":"` +
		strings.Repeat("A", 20<<20) + `"}}`
	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/send", oversized)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
	if svc.sent != "" {
		t.Fatalf("send service called with oversized body: %q", svc.sent)
	}
}

func TestSessionsAPI_CleanupWithProjectFilter(t *testing.T) {
	svc := newFakeSessionService()
	svc.cleanupResult = []domain.SessionID{"ao-1"}
	svc.cleanupSkipped = []sessionsvc.CleanupSkipped{{SessionID: "ao-2", Reason: "workspace has uncommitted changes"}}
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/cleanup?project=ao", "")
	if status != http.StatusOK {
		t.Fatalf("cleanup = %d, want 200; body=%s", status, body)
	}
	var got struct {
		OK      bool     `json:"ok"`
		Cleaned []string `json:"cleaned"`
		Skipped []struct {
			SessionID string `json:"sessionId"`
			Reason    string `json:"reason"`
		} `json:"skipped"`
	}
	mustJSON(t, body, &got)
	if !got.OK || len(got.Cleaned) != 1 || got.Cleaned[0] != "ao-1" {
		t.Fatalf("cleanup response = %#v", got)
	}
	if len(got.Skipped) != 1 || got.Skipped[0].SessionID != "ao-2" || got.Skipped[0].Reason != "workspace has uncommitted changes" {
		t.Fatalf("cleanup skipped = %#v, want preserved workspace with reason", got.Skipped)
	}
	if len(svc.cleanupProjects) != 1 || svc.cleanupProjects[0] != "ao" {
		t.Fatalf("cleanupProjects = %#v, want [ao]", svc.cleanupProjects)
	}
}

func TestSessionsAPI_CleanupWithoutProjectFilter(t *testing.T) {
	svc := newFakeSessionService()
	svc.cleanupResult = []domain.SessionID{"ao-1", "other-1"}
	srv := newSessionTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/cleanup", "")
	if status != http.StatusOK {
		t.Fatalf("cleanup = %d, want 200; body=%s", status, body)
	}
	var got struct {
		Cleaned []string `json:"cleaned"`
	}
	mustJSON(t, body, &got)
	if len(got.Cleaned) != 2 || got.Cleaned[0] != "ao-1" || got.Cleaned[1] != "other-1" {
		t.Fatalf("cleanup response = %#v", got)
	}
	if len(svc.cleanupProjects) != 1 || svc.cleanupProjects[0] != "" {
		t.Fatalf("cleanupProjects = %#v, want empty project filter", svc.cleanupProjects)
	}
}

type sessionBody struct {
	ID               string `json:"id"`
	ProjectID        string `json:"projectId"`
	IssueID          string `json:"issueId"`
	Kind             string `json:"kind"`
	Harness          string `json:"harness"`
	DisplayName      string `json:"displayName"`
	Branch           string `json:"branch"`
	Status           string `json:"status"`
	SCMStatus        string `json:"scmStatus"`
	TerminalHandleID string `json:"terminalHandleId"`
}

func TestSessionsAPI_PRRoutes(t *testing.T) {
	srv := newSessionTestServer(t, newFakeSessionService())

	body, status, _ := doRequest(t, srv, "GET", "/api/v1/sessions/ao-1/pr", "")
	if status != http.StatusOK {
		t.Fatalf("GET PRs = %d body=%s", status, body)
	}
	var listed struct {
		SessionID string `json:"sessionId"`
		PRs       []struct {
			URL            string `json:"url"`
			Number         int    `json:"number"`
			Title          string `json:"title"`
			State          string `json:"state"`
			StateChangedAt string `json:"stateChangedAt"`
			CreatedAt      string `json:"createdAt"`
			CI             struct {
				State         string `json:"state"`
				FailingChecks []struct {
					Name       string `json:"name"`
					Status     string `json:"status"`
					Conclusion string `json:"conclusion"`
					URL        string `json:"url"`
					LogTail    string `json:"logTail"`
				} `json:"failingChecks"`
			} `json:"ci"`
			Review struct {
				Decision     string `json:"decision"`
				UnresolvedBy []struct {
					ReviewerID string `json:"reviewerId"`
					Count      int    `json:"count"`
					ReviewURL  string `json:"reviewUrl"`
					Links      []struct {
						URL  string `json:"url"`
						File string `json:"file"`
						Line int    `json:"line"`
						Body string `json:"body"`
					} `json:"links"`
				} `json:"unresolvedBy"`
			} `json:"review"`
			Mergeability struct {
				State         string   `json:"state"`
				Reasons       []string `json:"reasons"`
				PRURL         string   `json:"prUrl"`
				ConflictFiles []struct {
					Path string `json:"path"`
				} `json:"conflictFiles"`
			} `json:"mergeability"`
		} `json:"prs"`
	}
	mustJSON(t, body, &listed)
	if listed.SessionID != "ao-1" || len(listed.PRs) != 1 || listed.PRs[0].State != "open" || listed.PRs[0].Title == "" {
		t.Fatalf("GET shape = %#v", listed)
	}
	if listed.PRs[0].StateChangedAt != "2026-06-04T11:30:00Z" {
		t.Fatalf("stateChangedAt = %q, want backend-selected PR state time", listed.PRs[0].StateChangedAt)
	}
	if listed.PRs[0].CreatedAt != "2026-06-04T09:00:00Z" {
		t.Fatalf("createdAt = %q, want provider PR creation time", listed.PRs[0].CreatedAt)
	}
	if checks := listed.PRs[0].CI.FailingChecks; len(checks) != 1 || checks[0].Name != "unit" || checks[0].LogTail != "" {
		t.Fatalf("failing checks = %#v", checks)
	}
	if reviewers := listed.PRs[0].Review.UnresolvedBy; len(reviewers) != 1 || reviewers[0].ReviewerID != "reviewer-a" || reviewers[0].ReviewURL == "" || reviewers[0].Links[0].Body != "" {
		t.Fatalf("reviewers = %#v", reviewers)
	}
	if merge := listed.PRs[0].Mergeability; merge.State != "conflicting" || len(merge.ConflictFiles) != 0 || merge.PRURL == "" {
		t.Fatalf("mergeability = %#v", merge)
	}

	body, status, _ = doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/pr/claim", `{"pr":"142"}`)
	if status != http.StatusOK {
		t.Fatalf("claim = %d body=%s", status, body)
	}
	var claimed struct {
		OK            bool     `json:"ok"`
		SessionID     string   `json:"sessionId"`
		PRs           []any    `json:"prs"`
		BranchChanged bool     `json:"branchChanged"`
		TakenOverFrom []string `json:"takenOverFrom"`
	}
	mustJSON(t, body, &claimed)
	if !claimed.OK || claimed.SessionID != "ao-1" || len(claimed.PRs) != 1 || !claimed.BranchChanged || len(claimed.TakenOverFrom) != 0 {
		t.Fatalf("claim shape = %#v", claimed)
	}
}

func TestSessionPRSummaryOmitsUnavailableLifecycleTimes(t *testing.T) {
	payload, err := json.Marshal(controllers.NewSessionPRSummary(sessionsvc.PRSummary{}))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["createdAt"]; ok {
		t.Fatalf("createdAt must be omitted when provider creation time is unavailable: %s", payload)
	}
	if _, ok := got["stateChangedAt"]; ok {
		t.Fatalf("stateChangedAt must be omitted when lifecycle time is unavailable: %s", payload)
	}
}

func TestSessionsAPI_ClaimPRErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		err  error
		code int
		want string
	}{
		{"bad json", `{`, nil, http.StatusBadRequest, "INVALID_JSON"},
		{"missing pr", `{}`, nil, http.StatusBadRequest, "PR_REQUIRED"},
		{"invalid ref", `{"pr":"x"}`, sessionsvc.ErrInvalidPRRef, http.StatusBadRequest, "INVALID_PR_REF"},
		{"session missing", `{"pr":"142"}`, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session"), http.StatusNotFound, "SESSION_NOT_FOUND"},
		{"pr missing", `{"pr":"142"}`, sessionsvc.ErrPRNotFound, http.StatusNotFound, "PR_NOT_FOUND"},
		{"not open", `{"pr":"142"}`, sessionsvc.ErrPRNotOpen, http.StatusConflict, "PR_NOT_OPEN"},
		{"claimed", `{"pr":"142","allowTakeover":false}`, ports.PRClaimedByActiveSessionError{Owner: "ao-2"}, http.StatusConflict, "PR_CLAIMED_BY_ACTIVE_SESSION"},
		{"not claimable", `{"pr":"142"}`, sessionsvc.ErrSessionNotClaimable, http.StatusUnprocessableEntity, "SESSION_NOT_CLAIMABLE"},
		{"mismatch", `{"pr":"142"}`, sessionsvc.ErrProjectMismatch, http.StatusUnprocessableEntity, "PR_PROJECT_MISMATCH"},
		{"scm", `{"pr":"142"}`, sessionsvc.ErrSCMUnavailable, http.StatusServiceUnavailable, "SCM_UNAVAILABLE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newFakeSessionService()
			svc.claimErr = tc.err
			srv := newSessionTestServer(t, svc)
			body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/pr/claim", tc.body)
			assertErrorCode(t, body, status, tc.code, tc.want)
		})
	}
}
