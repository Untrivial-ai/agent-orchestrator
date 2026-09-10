package ports

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// PRWriter records the PR facts a PR observation carries. The pr table's own DB
// triggers emit the CDC; this just writes the rows.
type PRWriter interface {
	// WritePR persists a full PR observation — scalar facts, check runs, and the
	// replacement comment set — in one transaction, so the rows and the CDC
	// events they emit are all-or-nothing.
	WritePR(ctx context.Context, pr domain.PullRequest, checks []domain.PullRequestCheck, comments []domain.PullRequestComment) error
}

// ReviewWriteMode describes how an SCM observation should update normalized
// review-thread/comment rows.
type ReviewWriteMode int

const (
	// ReviewWritePreserve leaves stored review rows untouched. Metadata/CI-only
	// refreshes and failed review fetches use this mode.
	ReviewWritePreserve ReviewWriteMode = iota
	// ReviewWriteReplace treats the fetched review rows as a complete snapshot
	// and replaces all stored review rows for the PR.
	ReviewWriteReplace
	// ReviewWriteMerge treats the fetched review rows as a partial window:
	// fetched threads/comments are updated while older unseen rows are preserved.
	ReviewWriteMerge
)

// SCMWriter records provider-neutral SCM observations. reviewMode decides
// whether review facts are preserved, replaced with a complete snapshot, or
// merged as a bounded partial window.
type SCMWriter interface {
	WriteSCMObservation(ctx context.Context, pr domain.PullRequest, checks []domain.PullRequestCheck, reviews []domain.PullRequestReview, threads []domain.PullRequestReviewThread, comments []domain.PullRequestComment, reviewMode ReviewWriteMode) error
}

// PRClaimer atomically moves (or creates) a PR row for a target session and
// persists the live SCM facts observed for that PR in the same transaction.
type PRClaimer interface {
	ClaimPR(ctx context.Context, pr domain.PullRequest, checks []domain.PullRequestCheck, reviews []domain.PullRequestReview, threads []domain.PullRequestReviewThread, comments []domain.PullRequestComment, reviewMode ReviewWriteMode, allowActiveTakeover bool) (ClaimOutcome, error)
}

// ErrPRClaimedByActiveSession is returned by PRClaimer.ClaimPR when takeover is
// explicitly disallowed and the existing owner is still alive.
var ErrPRClaimedByActiveSession = errors.New("pr claimed by active session")

// PRClaimedByActiveSessionError carries the active owner that blocked a claim.
type PRClaimedByActiveSessionError struct {
	Owner domain.SessionID
}

func (e PRClaimedByActiveSessionError) Error() string {
	return fmt.Sprintf("%s: %s", ErrPRClaimedByActiveSession, e.Owner)
}

func (e PRClaimedByActiveSessionError) Unwrap() error { return ErrPRClaimedByActiveSession }

// ClaimOutcome describes what owner, if any, a successful claim replaced.
type ClaimOutcome struct {
	PreviousOwner   domain.SessionID
	OwnerTerminated bool
}

// AgentMessenger injects a message into a running agent. An empty message
// sends only the submit keystroke (Enter) — callers use it to nudge a pasted
// prompt that was not submitted; every runtime must honor this contract.
type AgentMessenger interface {
	Send(ctx context.Context, id domain.SessionID, message string) error
}

// ---- runtime / agent / workspace plugin ports ----

// Runtime is the full runtime adapter contract: session creation/teardown plus
// liveness probing for reapers and terminal attachment.
type Runtime interface {
	Create(ctx context.Context, cfg RuntimeConfig) (RuntimeHandle, error)
	Destroy(ctx context.Context, handle RuntimeHandle) error
	GetOutput(ctx context.Context, handle RuntimeHandle, lines int) (string, error)
	IsAlive(ctx context.Context, handle RuntimeHandle) (bool, error)
}

// FencedLiveness is exact ownership evidence for one AO runtime generation.
// Unknown is deliberately distinct from dead: callers must retain ownership
// gates when an adapter cannot prove an exact match or exact absence.
type FencedLiveness string

// FencedAlive and the related constants enumerate exact-generation liveness
// conclusions.
const (
	FencedAlive   FencedLiveness = "alive"
	FencedDead    FencedLiveness = "dead"
	FencedUnknown FencedLiveness = "unknown"
)

// FencedProbeReason explains the evidence behind a fenced liveness result.
type FencedProbeReason string

// FencedReasonExactMatch and the related constants explain the evidence behind
// a fenced liveness conclusion.
const (
	FencedReasonExactMatch         FencedProbeReason = "exact_match"
	FencedReasonExactAbsent        FencedProbeReason = "exact_absent"
	FencedReasonIdentityMissing    FencedProbeReason = "identity_missing"
	FencedReasonRegistryUnreadable FencedProbeReason = "registry_unreadable"
	FencedReasonRegistryMalformed  FencedProbeReason = "registry_malformed"
	FencedReasonProbeFailed        FencedProbeReason = "probe_failed"
	FencedReasonGenerationMismatch FencedProbeReason = "generation_mismatch"
)

// FencedRuntimeRef identifies the exact AO-owned runtime generation whose
// ownership must be proven. NativeIdentity is populated when the caller has a
// provider-native identity that the adapter can inspect.
type FencedRuntimeRef struct {
	Handle         RuntimeHandle
	SessionID      domain.SessionID
	Generation     string
	NativeIdentity string
}

// FencedProbeResult pairs an exact-generation liveness conclusion with its
// evidence category.
type FencedProbeResult struct {
	Liveness FencedLiveness
	Reason   FencedProbeReason
}

// FencedRuntimeProber determines liveness for an exact AO-owned runtime
// generation without treating uncertainty as death.
type FencedRuntimeProber interface {
	ProbeFencedRuntime(context.Context, FencedRuntimeRef) FencedProbeResult
}

// RuntimeEffectOutcome describes whether a failed runtime operation may have
// applied an external side effect.
type RuntimeEffectOutcome string

// RuntimeEffectNone and the related constants classify whether a failed
// runtime operation may have applied an external side effect.
const (
	RuntimeEffectNone     RuntimeEffectOutcome = "none"
	RuntimeEffectPossible RuntimeEffectOutcome = "possible"
	RuntimeEffectApplied  RuntimeEffectOutcome = "applied"
)

// RuntimeCleanupOutcome describes cleanup performed after a failed runtime
// operation that may have applied an external side effect.
type RuntimeCleanupOutcome string

// RuntimeCleanupNotAttempted and the related constants classify cleanup after
// a failed runtime operation.
const (
	RuntimeCleanupNotAttempted RuntimeCleanupOutcome = "not_attempted"
	RuntimeCleanupSucceeded    RuntimeCleanupOutcome = "succeeded"
	RuntimeCleanupFailed       RuntimeCleanupOutcome = "failed"
)

// RuntimeEffectError preserves ownership evidence from a failed operation.
// Callers must not replace PossibleHandle with a different persisted handle.
type RuntimeEffectError interface {
	error
	PossibleHandle() RuntimeHandle
	EffectOutcome() RuntimeEffectOutcome
	CleanupOutcome() RuntimeCleanupOutcome
}

// StyledTerminalOutputReader is an optional runtime capability for safety
// checks that must distinguish dim placeholder text from a human-authored
// draft. Implementations return a bounded excerpt of the rendered current
// viewport with ANSI cell styles preserved; raw terminal history is not valid
// evidence for this contract. Callers must fail closed when unavailable.
type StyledTerminalOutputReader interface {
	GetStyledOutput(ctx context.Context, handle RuntimeHandle, lines int) (string, error)
}

// ErrStyledTerminalOutputUnavailable reports that a runtime implementation can
// provide styled current-screen output in general, but not for this particular
// handle. This occurs when a detached runtime host survives an AO upgrade from
// a protocol version that predates rendered-surface support. Callers may use
// their conservative no-surface fallback; every other read error remains an
// inconclusive probe and must fail closed.
var ErrStyledTerminalOutputUnavailable = errors.New("runtime: styled terminal output unavailable for handle")

// ErrRuntimeProcessExited is returned by an attached runtime stream when the
// hosted command has definitively exited while its detached terminal host
// remains available for scrollback. Terminal transport treats this as a clean
// exit instead of attempting to reattach to the dead command.
var ErrRuntimeProcessExited = errors.New("runtime: process exited")

// RuntimeRestarter is an optional runtime capability for replacing the process
// inside an existing terminal session. Implementations should preserve the
// handle when possible so attached clients do not need a new terminal identity.
type RuntimeRestarter interface {
	Restart(ctx context.Context, handle RuntimeHandle, cfg RuntimeConfig) (RuntimeHandle, error)
}

// RuntimeConfig is the spec for launching a session's process in a Runtime.
// Argv is the agent's launch command as discrete arguments; each Runtime
// shell-quotes it for its own shell, so the command survives args with spaces
// (e.g. a prompt) without the caller guessing the target shell's quoting.
type RuntimeConfig struct {
	SessionID     domain.SessionID
	WorkspacePath string
	Argv          []string
	Env           map[string]string
	// ExitOnCommandCompletion is reserved for short-lived, backend-owned
	// command terminals. Interactive agent and shell runtimes deliberately keep
	// their terminal alive after the launched command exits so scrollback and
	// manual recovery remain available.
	ExitOnCommandCompletion bool
}

// RuntimeHandle identifies a live runtime instance. Its ID is opaque outside
// the concrete runtime adapter.
type RuntimeHandle struct {
	ID string
}

// SupervisedProcessRef identifies the AO-owned supervisor belonging to one
// managed agent launch. LaunchID fences process observations from older
// spawn/restore generations of the same session.
type SupervisedProcessRef struct {
	SessionID domain.SessionID
	LaunchID  string
}

// SupervisedProcessInspector is an optional runtime capability used by the
// reaper for agents without native exit hooks. Implementations may also detect
// a workload relaunched from a preserved runtime shell. A false result is
// definitive only when err is nil; inspection errors must never be interpreted
// as exit.
type SupervisedProcessInspector interface {
	IsSupervisedProcessAlive(ctx context.Context, handle RuntimeHandle, ref SupervisedProcessRef) (bool, error)
}

// ExactSupervisedProcessInspector is the strict launch-generation probe used
// at agent-switch ownership boundaries. Unlike SupervisedProcessInspector it
// must never treat an arbitrary child of a preserved shell as the requested
// AO supervisor. A true result proves the exact session/launch pair and the
// supervisor's managed agent child are both alive.
type ExactSupervisedProcessInspector interface {
	IsExactSupervisedProcessAlive(ctx context.Context, handle RuntimeHandle, ref SupervisedProcessRef) (bool, error)
}

// ContainerReaper removes Docker containers a worker session owns, identified
// by the ao.session=<id> label convention (see EnvSessionID). It is an
// optional capability: nil wiring means container reaping is a no-op, not an
// error. Implementations MUST treat a container's ao.spare=true label as an
// unconditional skip, and MUST bias toward sparing on any ambiguity (e.g. a
// docker CLI probe failure reaps nothing rather than guessing) -- a wrongly
// reaped container can cost a live worker its database.
type ContainerReaper interface {
	// ReapSessionContainers force-removes every non-spared container labeled
	// for session id. removed is the count actually removed; err is non-nil
	// only for a genuine adapter failure, never for "docker not installed" or
	// "nothing found" (both return removed=0, err=nil).
	ReapSessionContainers(ctx context.Context, id domain.SessionID) (removed int, err error)
}

// Stream is one live terminal attach: PTY-like bytes plus resize. Returned
// already-open by a Runtime's Attach. tmux backs it with a local PTY around
// its attach CLI; detached hosts use a loopback connection to the pty-host.
type Stream interface {
	io.ReadWriteCloser
	Resize(rows, cols uint16) error
}

// Attacher opens a fresh attach Stream for a session handle, sized rows x cols from
// birth (0 means size not yet known). ctx cancellation must terminate the stream.
type Attacher interface {
	Attach(ctx context.Context, handle RuntimeHandle, rows, cols uint16) (Stream, error)
}

// The Agent port and its supporting types live in agent.go.

// WorkspaceReclaim distinguishes a teardown that actually released disk from
// one that found nothing to release. Destroy returning a nil error proves only
// that the workspace is absent afterwards, not that this call is what removed
// it: a directory that was already gone leaves nothing for the adapter to fail
// on. Callers that report teardown counts need the two kept apart.
type WorkspaceReclaim string

// Workspace reclaim outcomes.
const (
	// WorkspaceReclaimRemoved means the workspace was present and this call
	// released it.
	WorkspaceReclaimRemoved WorkspaceReclaim = "removed"
	// WorkspaceReclaimAlreadyAbsent means there was nothing on disk to release.
	// Any stale registration is still reconciled, so the post-state matches
	// WorkspaceReclaimRemoved; only the accounting differs.
	WorkspaceReclaimAlreadyAbsent WorkspaceReclaim = "already_absent"
)

// WorkspaceReclaimer is the optional half of Workspace that reports which of
// the two reclaim outcomes a teardown reached. Implementing it is optional:
// callers fall back to Destroy and treat a nil error as
// WorkspaceReclaimRemoved, which is the pre-existing behaviour.
type WorkspaceReclaimer interface {
	DestroyReclaim(ctx context.Context, info WorkspaceInfo) (WorkspaceReclaim, error)
}

// Workspace is the isolated checkout an agent works in (a git worktree or clone).
type Workspace interface {
	Create(ctx context.Context, cfg WorkspaceConfig) (WorkspaceInfo, error)
	Destroy(ctx context.Context, info WorkspaceInfo) error
	Restore(ctx context.Context, cfg WorkspaceConfig) (WorkspaceInfo, error)
	// ForceDestroy removes the worktree unconditionally, bypassing the
	// dirty-worktree refusal that Destroy enforces. It is only safe to call
	// AFTER the session's uncommitted work has been captured via StashUncommitted.
	// Never call it from interactive teardown paths.
	ForceDestroy(ctx context.Context, info WorkspaceInfo) error
	// StashUncommitted captures all uncommitted work in the worktree as a git
	// commit object stored at refs/ao/preserved/<session-id>, WITHOUT mutating
	// the working tree or the global stash stack. Tracked edits and new
	// non-ignored files are captured; .gitignore-d files are skipped (the count
	// of skipped ignored paths is logged). Returns the ref name on success, or
	// an empty string if the worktree is clean (nothing to preserve).
	StashUncommitted(ctx context.Context, info WorkspaceInfo) (ref string, err error)
	// ApplyPreserved replays a capture created by StashUncommitted onto the
	// worktree identified by info. On clean success the preserve ref is deleted.
	// On conflict, the ref is kept, conflict markers are left in the working
	// tree, and ErrPreservedConflict (wrapped) is returned. The ref must never
	// be deleted on a failed or conflicted apply.
	ApplyPreserved(ctx context.Context, info WorkspaceInfo, ref string) error
	// AddExclude appends the given git ignore patterns to the worktree's local
	// info/exclude so daemon-generated files (e.g. pasted task attachments) never
	// surface as untracked changes or get committed. Idempotent: patterns already
	// present are skipped. Owning this here keeps git/process execution inside the
	// workspace adapter rather than leaking into callers.
	AddExclude(ctx context.Context, info WorkspaceInfo, patterns ...string) error
}

// WorkspaceDefaultBranchRefresher is an optional capability for Git-backed
// workspaces. Resolution is local-only so callers can retain the canonical ref
// even when the subsequent best-effort network refresh fails.
type WorkspaceDefaultBranchRefresher interface {
	ResolveDefaultBranch(ctx context.Context, repoPath, configuredBranch string) (WorkspaceDefaultBranch, error)
	FetchDefaultBranch(ctx context.Context, repoPath string, target WorkspaceDefaultBranch) error
}

// WorkspaceDefaultBranch is a locally resolved default-branch target. BaseRef
// is canonical (for example refs/remotes/upstream/main), so materialization
// never has to reconstruct the remote decision from an ambiguous slash.
type WorkspaceDefaultBranch struct {
	Remote  string
	Branch  string
	BaseRef string
}

// WorkspaceObserver is an optional read-only capability implemented by
// workspace adapters that can describe the durable state an agent handoff
// must treat as authoritative. The session manager consumes it before and
// after replacing an agent process; it never infers Git state from terminal
// prose supplied by a model.
type WorkspaceObserver interface {
	ObserveWorkspace(ctx context.Context, info WorkspaceInfo) (WorkspaceObservation, error)
}

// WorkspaceObservation is a bounded, provider-neutral snapshot of one
// materialized workspace. Git-backed adapters populate repository facts;
// scratch adapters return the path with Git fields empty.
type WorkspaceObservation struct {
	Path      string
	Branch    string
	HeadSHA   string
	Dirty     bool
	Staged    bool
	Untracked bool
	Changes   []WorkspaceChange
	Commits   []WorkspaceCommit
}

// WorkspaceChange is one changed path reported by the workspace adapter.
// Status is Git's two-column porcelain status (for example " M", "A ", or
// "??") so callers retain staged/worktree provenance without reparsing prose.
type WorkspaceChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// WorkspaceCommit is one recent commit reachable from the workspace HEAD.
type WorkspaceCommit struct {
	SHA        string `json:"sha"`
	Subject    string `json:"subject"`
	AuthoredAt string `json:"authoredAt"`
}

// WorkspaceProject is an optional extension for projects composed from a
// root-as-repo parent plus child repositories. It materialises the parent
// worktree at the session root and each child repo at its registered relative
// path inside that root.
type WorkspaceProject interface {
	CreateWorkspaceProject(ctx context.Context, cfg WorkspaceProjectConfig) (WorkspaceProjectInfo, error)
	DestroyWorkspaceProject(ctx context.Context, info WorkspaceProjectInfo) error
}

// Workspace-level sentinels surfaced through Create/Restore/Destroy so callers
// can map them to typed errors rather than collapsing every adapter failure
// into an opaque 500. Adapters wrap these via fmt.Errorf("...: %w", sentinel).
var (
	// ErrWorkspaceBranchCheckedOutElsewhere reports the requested branch is
	// already checked out in another worktree of the same repo.
	ErrWorkspaceBranchCheckedOutElsewhere = errors.New("workspace: branch is already checked out in another worktree")
	// ErrWorkspaceBranchNotFetched reports the requested branch exists nowhere
	// reachable (no local head, no remote-tracking branch, no tag).
	ErrWorkspaceBranchNotFetched = errors.New("workspace: branch is not fetched")
	// ErrWorkspaceDefaultBranchUnresolved reports that automatic branch
	// selection found no authoritative repository default. Callers should ask
	// the user to configure a branch instead of guessing from the checkout.
	ErrWorkspaceDefaultBranchUnresolved = errors.New("workspace: default branch is unresolved")
	// ErrWorkspaceBranchInvalid reports the requested branch name is not a valid
	// git ref (rejected by `git check-ref-format`).
	ErrWorkspaceBranchInvalid = errors.New("workspace: invalid branch name")
	// ErrWorkspaceDirty reports Destroy refused to remove a workspace because
	// it holds uncommitted changes or untracked files. Teardown is never
	// forced; callers treat the workspace as intentionally preserved.
	ErrWorkspaceDirty = errors.New("workspace: uncommitted changes present")
	// ErrWorkspaceRepoUnavailable reports that the project's source repository
	// is no longer reachable on disk, so git cannot be asked to remove the
	// session's worktree. Teardown treats this like ErrWorkspaceDirty: the
	// directory is left alone and the session still terminates, because a
	// session the user deleted must leave the sidebar whether or not its
	// worktree could be reclaimed.
	ErrWorkspaceRepoUnavailable = errors.New("workspace: project repository is unavailable")
	// ErrWorkspaceStale reports an AO-managed workspace path no longer points
	// at a registered git worktree. Replacement paths may skip preservation for
	// this state after path-safety checks, while real preserve failures remain
	// fatal.
	ErrWorkspaceStale = errors.New("workspace: stale managed worktree")
	// ErrWorkspaceLocked reports a registered git worktree whose directory is
	// missing but whose registration is locked (`git worktree lock`). `git
	// worktree prune` deliberately leaves a locked registration in place even
	// when its directory is gone, and `git worktree add`/`remove` at the same
	// path then fail with an opaque git error. Callers must not treat this as
	// recoverable on their own; the operator has to unlock or remove the
	// registration first.
	ErrWorkspaceLocked = errors.New("workspace: registered worktree is locked")
	// ErrPreservedConflict is returned by ApplyPreserved when replaying a
	// preserved ref onto the worktree produces merge conflicts. The ref is
	// kept intact (never deleted on conflict); the working tree is left with
	// conflict markers for manual resolution. Adapters wrap this sentinel via
	// fmt.Errorf so callers can match it with errors.Is.
	ErrPreservedConflict = errors.New("workspace: preserved apply produced conflicts")
	// ErrRuntimePrerequisite reports a missing host prerequisite for the selected
	// runtime before a session can be created.
	ErrRuntimePrerequisite = errors.New("runtime: prerequisite missing")
	// ErrRuntimeCommandLineTooLong reports that the fully escaped command line
	// exceeds the host operating system's process-creation limit.
	ErrRuntimeCommandLineTooLong = errors.New("runtime: command line too long")
	// ErrRuntimeWorkspaceCwdMismatch reports that a runtime session's working
	// directory never settled on the wanted workspace path after Create's
	// retried verification (see the tmux adapter's verifyPaneWorkingDirectory).
	// Wrapping this sentinel lets the session service map it to a typed,
	// actionable apierr instead of letting it fall through to an opaque 500
	// with no message (issue #2775).
	ErrRuntimeWorkspaceCwdMismatch = errors.New("runtime: session working directory mismatch")
	// ErrRuntimeUnavailable reports that the runtime infrastructure is
	// conclusively absent (for example tmux reports "no server running"). Some
	// recovery callers intentionally use that evidence to recreate a runtime.
	ErrRuntimeUnavailable = errors.New("runtime: infrastructure unavailable")
	// ErrRuntimeProbeInconclusive reports that a liveness probe could not inspect
	// a possibly-live runtime (for example a transient socket error, missing
	// compatible client, or client/server protocol mismatch). Callers must
	// preserve the existing controller and must not recreate, destroy, archive,
	// or otherwise treat the session as dead. Adapters wrap this sentinel via
	// fmt.Errorf so callers can match it with errors.Is.
	ErrRuntimeProbeInconclusive = errors.New("runtime: liveness probe inconclusive")
)

// WorkspaceConfig is the spec for creating or restoring a session's workspace.
type WorkspaceConfig struct {
	ProjectID domain.ProjectID
	SessionID domain.SessionID
	Kind      domain.SessionKind
	// SessionPrefix is the human-readable project prefix used to name the
	// orchestrator worktree. Defaults to a truncation of ProjectID when empty.
	SessionPrefix string
	Branch        string
	// BaseBranch is the explicitly configured branch new session branches are
	// created from. Empty asks the workspace adapter to resolve an authoritative
	// repository default; it must never infer from the checked-out branch.
	BaseBranch string
	// BaseRef is the exact canonical ref selected before any best-effort fetch.
	// Restore carries it forward without re-resolving repository defaults.
	BaseRef string
	// RepoPath optionally overrides ProjectID-based repo resolution.
	RepoPath string
	// Path optionally supplies an existing managed worktree path for restore.
	Path string
}

// WorkspaceInfo describes a created workspace — where it lives and its branch.
type WorkspaceInfo struct {
	Path   string
	Branch string
	// BaseRef is the repository-default ref selected for session comparisons.
	// It can differ from the remote session ref used to seed the worktree.
	BaseRef   string
	SessionID domain.SessionID
	ProjectID domain.ProjectID
	// RepoPath optionally overrides ProjectID-based repo resolution. It is used
	// when the normal workspace lifecycle primitives operate on one child repo
	// inside a workspace project.
	RepoPath string
}

// WorkspaceProjectConfig describes a multi-repo workspace session. RootRepoPath
// and child RepoPath values are absolute paths to the canonical repositories.
type WorkspaceProjectConfig struct {
	ProjectID     domain.ProjectID
	SessionID     domain.SessionID
	Kind          domain.SessionKind
	SessionPrefix string
	Branch        string
	RootRepoPath  string
	// BaseBranch applies only to RootRepoPath. Empty asks the workspace adapter
	// to resolve that repository's default independently from every child.
	BaseBranch string
	BaseRef    string
	Repos      []WorkspaceProjectRepoConfig
	Assets     []WorkspaceProjectAssetConfig
}

// WorkspaceProjectAssetConfig describes a non-repository child directory that
// is copied from the canonical workspace into each session workspace.
type WorkspaceProjectAssetConfig struct {
	RelativePath string
	SourcePath   string
}

// WorkspaceProjectRepoConfig describes one registered child repo in a
// workspace project session.
type WorkspaceProjectRepoConfig struct {
	Name         string
	RelativePath string
	RepoPath     string
	// BaseBranch applies only to RepoPath. Empty asks the workspace adapter to
	// resolve this repository's default independently from the workspace root.
	BaseBranch string
	BaseRef    string
}

// WorkspaceProjectInfo returns the root worktree plus every child worktree.
// Worktrees are ordered root first, then children in creation order.
type WorkspaceProjectInfo struct {
	Root      WorkspaceInfo
	Worktrees []WorkspaceRepoInfo
}

// WorkspaceRepoInfo describes one materialized repo worktree in a workspace
// project session.
type WorkspaceRepoInfo struct {
	RepoName string
	RepoPath string
	Path     string
	Branch   string
	BaseSHA  string
	// BaseRef is the repository-default ref persisted with BaseSHA so comparisons
	// can recompute a merge base after that default advances or the session is
	// rebased. It can differ from the remote session ref used to seed the worktree.
	BaseRef      string
	SessionID    domain.SessionID
	ProjectID    domain.ProjectID
	RelativePath string
}
