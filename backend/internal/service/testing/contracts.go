// Package testing owns investigation attempts and target-bound tool dispatch.
package testing

import (
	"context"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// CapabilityHeader carries a secret in-memory worker capability, never a URL.
const CapabilityHeader = "X-AO-Test-Capability"

// Store persists facts only. BindTestTools runs before a worker starts.
type Store interface {
	GetProject(context.Context, string) (domain.ProjectRecord, bool, error)
	CreateTestRun(context.Context, domain.TestRunRecord) error
	GetTestRun(context.Context, domain.TestRunID) (domain.TestRunRecord, bool, error)
	SetTestRunWorker(context.Context, domain.TestRunID, domain.SessionID) error
	SetTestRunReport(context.Context, domain.TestRunID, string) error
	CreateTestAttempt(context.Context, domain.TestAttemptRecord) (domain.TestAttemptRecord, error)
	GetTestAttempt(context.Context, domain.TestAttemptID) (domain.TestAttemptRecord, bool, error)
	UpdateTestAttempt(context.Context, domain.TestAttemptRecord) error
	BindTestTools(context.Context, domain.TestToolProfileLink) error
	GetTestToolBinding(context.Context, domain.SessionID) (domain.TestToolProfileLink, bool, error)
	CreateTestWorkerLegs(context.Context, domain.TestWorkerLegs) error
	GetTestWorkerLegs(context.Context, domain.SessionID) (domain.TestWorkerLegs, bool, error)
}

// WorkerLauncher uses the ordinary session manager. After creating the session
// row and BEFORE starting its chat worker, it must call request.Prepare with
// that session ID. Prepare persists the binding and issues a fresh capability.
// Spawn and restore resolve IssueCapability again immediately before ChatStart.
type WorkerLauncher interface {
	LaunchTestingWorker(context.Context, WorkerLaunchRequest) (domain.SessionID, error)
}

// WorkerLaunchRequest asks the session manager to create an ordinary investigator.
type WorkerLaunchRequest struct {
	ProjectID   domain.ProjectID
	Harness     domain.AgentHarness
	Model       string
	Effort      string
	RunID       domain.TestRunID
	AttemptID   domain.TestAttemptID
	Prompt      string
	IssueJSON   string // quoted issue data, not worker instructions
	IssueURL    string
	CommitSHA   string
	RecipeID    string
	EvidenceDir string
	Timeout     time.Duration
	Context     ports.TestingWorkerContext
	Prepare     func(context.Context, domain.SessionID) (WorkerBinding, error)
	Comparison  bool
}

// WorkerBinding is launch-only data. Never persist or log Capability.
type WorkerBinding struct {
	Link       domain.TestToolProfileLink
	Attempt    domain.TestAttemptRecord
	Capability string `json:"-"`
	Context    ports.TestingWorkerContext
}

// Clock makes deadline enforcement testable without sleeps or agent turns.
type Clock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) Timer
}

// Timer stops a scheduled deadline callback.
type Timer interface{ Stop() bool }

// Recipe is server-configured target setup, snapshotted at run creation.
type Recipe struct {
	PullRequest   *domain.TestPullRequestSnapshot `json:"pullRequest,omitempty"`
	ID            string                          `json:"id"`
	CheckoutPath  string                          `json:"checkoutPath"`
	Snapshot      string                          `json:"snapshot"`
	DeliveryMode  string                          `json:"deliveryMode,omitempty"`
	VisualMarker  bool                            `json:"visualMarker,omitempty"`
	RealProviders bool                            `json:"realProviders,omitempty"`
}

// Deps supplies persistence, providers and target recipes.
type Deps struct {
	PullRequests    ports.TestingPullRequestIntake
	Store           Store
	Target          ports.TestingTargetEnvironment
	Desktop         ports.TestingDesktopControl
	Evidence        ports.TestingEvidenceStore
	Workers         WorkerLauncher
	Clock           Clock
	Recipes         map[string]Recipe
	TargetStateRoot string
	EvidenceRoot    string
	Log             *slog.Logger
	CloseDesktop    func(context.Context) error
	// PostActionCaptureTimeout bounds the complete settling loop, not one frame.
	PostActionCaptureTimeout time.Duration
}

// CreateRunInput selects an issue, revision and configured recipe.
type CreateRunInput struct {
	recipeOverride *Recipe
	LinkedRunID    domain.TestRunID
	ProjectID      domain.ProjectID
	IssueURL       string
	IssueSnapshot  string
	CommitSHA      string
	RecipeID       string
	Requester      string
}

// StartAttemptInput selects the worker and attempt deadline.
type StartAttemptInput struct {
	Harness      domain.AgentHarness
	Model        string
	Effort       string
	WorkerPrompt string
	Timeout      time.Duration
}

// StartAttemptResult identifies the new attempt and visible investigator.
type StartAttemptResult struct {
	RunID           domain.TestRunID     `json:"runId"`
	AttemptID       domain.TestAttemptID `json:"attemptId"`
	WorkerSessionID domain.SessionID     `json:"workerSessionId"`
}

// StartLegResult supplies the new target's checked paths to the same worker.
type StartLegResult struct {
	StartAttemptResult
	Leg           string                     `json:"leg"`
	CommitSHA     string                     `json:"commitSha"`
	EvidenceDir   string                     `json:"evidenceDir"`
	TargetContext ports.TestingWorkerContext `json:"targetContext"`
}

// ToolResult retains input delivery even if its post-action observation fails.
type ToolResult struct {
	Screenshot        *domain.TestScreenshot         `json:"screenshot,omitempty"`
	Action            *domain.TestActionResult       `json:"action,omitempty"`
	ObservationStatus string                         `json:"observationStatus,omitempty" enum:"captured,settled,unsettled,failed"`
	ObservationError  string                         `json:"observationError,omitempty"`
	Logs              *domain.TestLogResult          `json:"logs,omitempty"`
	Query             *domain.TestDaemonQueryResult  `json:"query,omitempty"`
	Report            *domain.TestSubmitReportResult `json:"report,omitempty"`
	Evidence          []domain.TestEvidenceReceipt   `json:"evidence"`
}
