package domain

import (
	"errors"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

// ErrDuplicateReviewRun is returned by InsertReviewRun when a run already exists
// for the same worker session and target commit (the partial unique index from
// migration 0013). It lets the review engine fall back to the recorded run
// instead of surfacing a raw storage error after a reviewer may have launched.
var ErrDuplicateReviewRun = errors.New("domain: review run already exists for session and target sha")

// Review is the per-worker, per-reviewer-harness code-review record. A repeat
// trigger for the same harness reuses this row; the per-pass facts live on
// ReviewRun.
type Review struct {
	ID        string          `json:"id"`
	SessionID SessionID       `json:"sessionId"`
	ProjectID ProjectID       `json:"projectId"`
	Harness   ReviewerHarness `json:"harness"`
	PRURL     string          `json:"prUrl"`
	// ReviewerHandleID is the runtime handle of the live reviewer pane, reused
	// across passes and exposed so the UI can attach its terminal.
	ReviewerHandleID string `json:"reviewerHandleId"`
	AgentSessionID   string `json:"agentSessionId"`
	// ReviewerLaunchID is the AO runtime generation that owns the live reviewer
	// pane on this row. It fences delayed hooks from an older replaced reviewer.
	ReviewerLaunchID string `json:"-"`
	// ReviewerActivityState is the latest activity hook reported by the review
	// pane itself. It is separate from ReviewRun.Status so the UI can distinguish
	// "review pass exists" from "reviewer is actively working right now".
	ReviewerActivityState ActivityState `json:"reviewerActivityState,omitempty"`
	// InterfaceMode selects the durable reviewer surface. Chat reviewers own a
	// native conversation; TUI reviewers continue to use their terminal handle.
	InterfaceMode          ReviewerInterfaceMode `json:"interfaceMode"`
	ProviderConversationID string                `json:"providerConversationId"`
	ControllerGeneration   string                `json:"controllerGeneration"`
	ControllerError        string                `json:"controllerError"`
	CreatedAt              time.Time             `json:"createdAt"`
	UpdatedAt              time.Time             `json:"updatedAt"`
}

// ReviewerInterfaceMode selects the durable UI surface for a reviewer.
type ReviewerInterfaceMode string

const (
	// ReviewerInterfaceTUI uses the reviewer's terminal handle.
	ReviewerInterfaceTUI ReviewerInterfaceMode = "tui"
	// ReviewerInterfaceChat uses a reviewer-owned native chat.
	ReviewerInterfaceChat ReviewerInterfaceMode = "chat"
)

// ReviewerSurface gives clients one stable identifier for either reviewer UI.
type ReviewerSurface struct {
	Mode            ReviewerInterfaceMode `json:"mode"`
	ReviewID        string                `json:"reviewId"`
	Harness         ReviewerHarness       `json:"harness"`
	HandleID        string                `json:"handleId,omitempty"`
	ControllerError string                `json:"controllerError,omitempty"`
}

// ReviewRun is one review pass against a worker's PR.
type ReviewRun struct {
	ID        string    `json:"id"`
	ReviewID  string    `json:"reviewId"`
	SessionID SessionID `json:"sessionId"`
	// BatchID groups review runs created by one trigger so worker feedback can
	// be delivered once after the whole trigger batch is terminal. Empty marks
	// legacy/single-run delivery.
	BatchID string          `json:"batchId"`
	Harness ReviewerHarness `json:"harness"`
	// TriggerSource records whether this pass was requested by a user or by the
	// daemon auto-review coordinator.
	TriggerSource ReviewTriggerSource `json:"triggerSource" enum:"manual,auto"`
	PRURL         string              `json:"prUrl"`
	// TargetSHA is the PR head commit this pass reviewed.
	TargetSHA string          `json:"targetSha"`
	Status    ReviewRunStatus `json:"status"`
	Verdict   ReviewVerdict   `json:"verdict"`
	// Body is the review text the reviewer submitted. It is recorded for AO's
	// own tracking; the reviewer also posts the review to the PR itself.
	Body string `json:"body"`
	// GithubReviewID is the id of the GitHub PR review the daemon published for
	// this pass (the POST .../pulls/{n}/reviews object id). It is an output of
	// publication, never a caller-supplied input. When the pass requests
	// changes, AO includes it in the message to the worker so the worker knows
	// exactly which review to address and reply to.
	GithubReviewID string `json:"githubReviewId"`
	// Findings are the run's inline review comments, recorded at submit time
	// and published to the provider by the daemon.
	Findings []ReviewFinding `json:"findings,omitempty"`
	// PublishState tracks the daemon-side GitHub publication attempt:
	// pending → publishing → published | failed, with uncertain marking an
	// attempt whose outcome could not be confirmed (for example a daemon
	// restart mid-publish). It exists so retries and restarts can neither lose
	// a recorded result nor silently duplicate a published review.
	PublishState ReviewRunPublishState `json:"publishState"`
	// PublishError carries the last publication failure, for the CLI and UI.
	PublishError string     `json:"publishError,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	DeliveredAt  *time.Time `json:"deliveredAt,omitempty"`
	// AutoInjectReview snapshots the session policy when this result is first
	// recorded. Later toggle changes must not rewrite or deliver this run.
	AutoInjectReview bool `json:"autoInjectReview"`
}

// ReviewFinding is one inline review comment: a file, the (diff) line in that
// file, and a single-line finding body.
type ReviewFinding struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
}

// ReviewRunPublishState is the state of a run's daemon-side provider publication.
type ReviewRunPublishState string

// Review run publication states.
const (
	// ReviewPublishPending means publication has not been attempted yet.
	ReviewPublishPending ReviewRunPublishState = "pending"
	// ReviewPublishPublishing marks a persisted publication attempt. It is
	// written before the provider call so an interrupted attempt is visible.
	ReviewPublishPublishing ReviewRunPublishState = "publishing"
	// ReviewPublishPublished means the provider accepted the review and its id
	// was recorded.
	ReviewPublishPublished ReviewRunPublishState = "published"
	// ReviewPublishFailed means the provider definitively rejected the
	// publication. A repeated submission retries it.
	ReviewPublishFailed ReviewRunPublishState = "failed"
	// ReviewPublishUncertain means an attempt's outcome is unknown: it must be
	// reported, never blindly reposted, because the provider may already hold
	// the review.
	ReviewPublishUncertain ReviewRunPublishState = "uncertain"
)

// ReviewTriggerSource identifies who initiated a review pass.
type ReviewTriggerSource string

const (
	// ReviewTriggerManual marks a user-initiated review pass.
	ReviewTriggerManual ReviewTriggerSource = "manual"
	// ReviewTriggerAuto marks a daemon-initiated review pass.
	ReviewTriggerAuto ReviewTriggerSource = "auto"
)

// ReviewRunStatus is the lifecycle state of a single review pass.
type ReviewRunStatus = contract.AOReviewRunStatus

// Review run statuses.
const (
	ReviewRunRunning   = contract.AOReviewRunRunning
	ReviewRunComplete  = contract.AOReviewRunComplete
	ReviewRunDelivered = contract.AOReviewRunDelivered
	ReviewRunFailed    = contract.AOReviewRunFailed
	ReviewRunCancelled = contract.AOReviewRunCancelled
)

// ReviewVerdict is the outcome a reviewer reports. The empty verdict marks a
// run that has not produced an outcome yet.
type ReviewVerdict = contract.AOReviewVerdict

// Review verdicts.
const (
	VerdictNone             = contract.AOReviewVerdictNone
	VerdictApproved         = contract.AOReviewVerdictApproved
	VerdictChangesRequested = contract.AOReviewVerdictChangesRequested
)

// CurrentHeadReviewRun is one AO review pass recorded against a PR's current
// head commit, reduced to the fields a derived read model needs.
type CurrentHeadReviewRun struct {
	SessionID SessionID
	Harness   ReviewerHarness
	PRURL     string
	Status    ReviewRunStatus
	Verdict   ReviewVerdict
	ID        string
	CreatedAt time.Time
}
