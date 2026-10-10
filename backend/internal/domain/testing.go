package domain

import "time"

type (
	// TestRunID identifies an investigation at one revision and recipe.
	TestRunID string
	// TestAttemptID identifies one execution of a test run.
	TestAttemptID string
	// TestToolProfileID identifies a versioned, daemon-owned tool definition.
	TestToolProfileID string
)

// TestToolProfileNativeV1 selects AO's checkpoint 0 tools over stdio.
const TestToolProfileNativeV1 TestToolProfileID = "ao-native-test-v1"

// TestRunRecord holds investigation facts independently of session status.
// Snapshots contain the issue and recipe used for this run, never credentials.
type TestRunRecord struct {
	ID               TestRunID
	LinkedRunID      TestRunID
	ProjectID        ProjectID
	IssueURL         string
	IssueSnapshot    string
	CommitSHA        string
	RecipeSnapshot   string
	Requester        string
	WorkerSessionID  SessionID
	ReportEvidenceID string
	CreatedAt        time.Time
}

// TestAttemptPhase records execution progress, separate from the outcome.
type TestAttemptPhase string

// Test attempt phases. A finished attempt admits no further tool actions.
const (
	TestAttemptStarting TestAttemptPhase = "starting"
	TestAttemptActive   TestAttemptPhase = "active"
	TestAttemptFinished TestAttemptPhase = "finished"
)

// TestOutcome records the observation result. Empty means no result yet.
type TestOutcome string

// Test outcomes require evidence appropriate to the claimed result.
const (
	TestOutcomeReproduced         TestOutcome = "reproduced"
	TestOutcomeNotReproduced      TestOutcome = "not_reproduced"
	TestOutcomeNeedsInformation   TestOutcome = "needs_information"
	TestOutcomeEnvironmentBlocked TestOutcome = "environment_blocked"
	TestOutcomePartial            TestOutcome = "partial"
	TestOutcomeCancelled          TestOutcome = "cancelled"
)

// TestCleanupState records resource cleanup independently of observations.
type TestCleanupState string

// Test cleanup states retain failures without changing the behavioral result.
const (
	TestCleanupPending  TestCleanupState = "pending"
	TestCleanupRunning  TestCleanupState = "running"
	TestCleanupComplete TestCleanupState = "complete"
	TestCleanupFailed   TestCleanupState = "failed"
)

// TestTargetIdentity pins one environment launch and its desktop window.
// ID and LaunchID are opaque adapter handles. Process start times prevent PID
// reuse from transferring ownership. Desktop control fills WindowID at binding.
// These facts come from adapters, never from worker tool arguments.
type TestTargetIdentity struct {
	ID                string
	LaunchID          string
	Generation        int64
	ElectronPID       int
	ElectronStartedAt time.Time
	DaemonPID         int
	DaemonStartedAt   time.Time
	DataDir           string
	WindowID          string
}

// TestAttemptRecord is durable execution state. LeaseGeneration is reserved
// for ownership fencing; checkpoint 0 does not implement lease renewal.
// Capability tokens and worker environments must never be added to this record.
type TestAttemptRecord struct {
	ID              TestAttemptID
	RunID           TestRunID
	Number          int64
	Target          TestTargetIdentity
	LeaseGeneration int64
	Phase           TestAttemptPhase
	Deadline        time.Time
	Outcome         TestOutcome
	CleanupState    TestCleanupState
	RecordingGap    string
	CancelledAt     *time.Time
	CreatedAt       time.Time
	FinishedAt      *time.Time
}

// TestToolProfileLink is the persistent session-to-attempt tool binding.
// Restore resolves ProfileID again and mints a fresh in-memory capability;
// neither MCP environment values nor capabilities belong in this link.
type TestToolProfileLink struct {
	SessionID SessionID
	AttemptID TestAttemptID
	ProfileID TestToolProfileID
}

// TestWorkerLegs pins both revisions for one investigator. The current attempt
// remains in TestToolProfileLink and changes without replacing the worker.
type TestWorkerLegs struct {
	SessionID      SessionID
	BaseRunID      TestRunID
	HeadRunID      TestRunID
	TimeoutSeconds int64
}

// TestEvidenceReceipt identifies an artifact saved outside the target.
// RelativePath is confined to the attempt's evidence directory under ~/.ao.
type TestEvidenceReceipt struct {
	ID           string        `json:"id"`
	AttemptID    TestAttemptID `json:"attemptId"`
	Kind         string        `json:"kind"`
	RelativePath string        `json:"relativePath"`
	MIMEType     string        `json:"mimeType"`
	SizeBytes    int64         `json:"sizeBytes"`
	SHA256       string        `json:"sha256"`
	CreatedAt    time.Time     `json:"createdAt"`
}
