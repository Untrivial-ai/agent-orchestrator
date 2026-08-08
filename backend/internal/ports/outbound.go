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

// StyledTerminalOutputReader is an optional runtime capability for safety
// checks that must distinguish dim placeholder text from a human-authored
// draft. Implementations return the same bounded pane excerpt as GetOutput but
// preserve ANSI style sequences. Callers must fail closed when unavailable.
type StyledTerminalOutputReader interface {
	GetStyledOutput(ctx context.Context, handle RuntimeHandle, lines int) (string, error)
}

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
// their attach CLI; conpty backs it with a loopback connection to the pty-host.
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
	// ErrWorkspaceBranchInvalid reports the requested branch name is not a valid
	// git ref (rejected by `git check-ref-format`).
	ErrWorkspaceBranchInvalid = errors.New("workspace: invalid branch name")
	// ErrWorkspaceDirty reports Destroy refused to remove a workspace because
	// it holds uncommitted changes or untracked files. Teardown is never
	// forced; callers treat the workspace as intentionally preserved.
	ErrWorkspaceDirty = errors.New("workspace: uncommitted changes present")
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
	// ErrRuntimeWorkspaceCwdMismatch reports that a runtime session's working
	// directory never settled on the wanted workspace path after Create's
	// retried verification (see the tmux adapter's verifyPaneWorkingDirectory).
	// Wrapping this sentinel lets the session service map it to a typed,
	// actionable apierr instead of letting it fall through to an opaque 500
	// with no message (issue #2775).
	ErrRuntimeWorkspaceCwdMismatch = errors.New("runtime: session working directory mismatch")
	// ErrRuntimeUnavailable reports that a liveness probe could not reach the
	// runtime infrastructure at all (e.g. tmux "no server running" or "error
	// connecting"). It says nothing about any individual session, so callers
	// must treat it as an inconclusive probe, never as per-session death
	// (issue #3475: reading a server-level outage as N session deaths archived
	// every session on the board). Adapters wrap this sentinel via fmt.Errorf
	// so callers can match it with errors.Is.
	ErrRuntimeUnavailable = errors.New("runtime: infrastructure unavailable")
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
	// BaseBranch is the per-project default branch new session branches are
	// created from. Empty falls back to the workspace adapter's own default.
	BaseBranch string
	// RepoPath optionally overrides ProjectID-based repo resolution.
	RepoPath string
	// Path optionally supplies an existing managed worktree path for restore.
	Path string
}

// WorkspaceInfo describes a created workspace — where it lives and its branch.
type WorkspaceInfo struct {
	Path      string
	Branch    string
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
	BaseBranch    string
	Repos         []WorkspaceProjectRepoConfig
}

// WorkspaceProjectRepoConfig describes one registered child repo in a
// workspace project session.
type WorkspaceProjectRepoConfig struct {
	Name         string
	RelativePath string
	RepoPath     string
	BaseBranch   string
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
	RepoName     string
	RepoPath     string
	Path         string
	Branch       string
	BaseSHA      string
	SessionID    domain.SessionID
	ProjectID    domain.ProjectID
	RelativePath string
}
