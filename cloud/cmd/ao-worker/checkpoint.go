package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// preservedRefPrefix namespaces the git ref that carries a session's uncommitted
// work across a sandbox destroy. It matches desktop AO's
// refs/ao/preserved/<session-id> convention so both implementations agree.
const preservedRefPrefix = "refs/ao/preserved/"

const (
	// branchPushTimeout bounds one checkpoint push so a stalled remote cannot
	// hold the single checkpoint consumer (and thus the agent's turn pipeline)
	// indefinitely; git's own low-speed abort only covers transfers in flight.
	branchPushTimeout = 45 * time.Second
	// branchPushRetryInterval suppresses retries of a branch tip already known
	// to be rejected (non-fast-forward or failed). The checkpoint fires on every
	// turn completion, so without the latch a permanently blocked branch would
	// burn a credential-brokered network push — and a doomed Warn — thousands of
	// times a day. The tip moving clears the suppression naturally.
	branchPushRetryInterval = 5 * time.Minute
)

// checkpointer captures a session's resume state to the control plane so a
// deleted (and later restored) sandbox can be rebuilt. Every method is
// best-effort: a failure is logged and never blocks the agent or crashes the
// worker.
type checkpointer struct {
	client        *client
	resolver      transcriptResolver
	git           worker.GitRunner
	workspace     string
	sessionID     string
	branch        string
	defaultBranch string
	harness       string
	scratch       bool
	logger        *slog.Logger

	mu sync.Mutex
	// change detection: an identical checkpoint is neither re-pushed nor re-sent.
	lastTranscriptHash string
	lastPreservedRef   string
	lastTreeSHA        string
	lastBranchTip      string
	// push failure latch: a tip already known to be rejected is not retried
	// (and not re-warned) until branchPushRetryInterval elapses. nowFn is a
	// test seam.
	failedTip string
	failedAt  time.Time
	warnedTip string
	nowFn     func() time.Time
}

func newCheckpointer(
	c *client,
	bootstrap worker.BootstrapResponse,
	workspace, dataDir string,
	logger *slog.Logger,
) *checkpointer {
	return &checkpointer{
		client: c,
		resolver: transcriptResolver{
			harness:              bootstrap.Launch.Harness,
			dataDir:              dataDir,
			workspace:            workspace,
			launchAgentSessionID: bootstrap.Launch.AgentSessionID,
			aoSessionID:          bootstrap.SessionID,
		},
		git:       worker.ExecGitRunner{},
		workspace: workspace,
		sessionID: bootstrap.SessionID,
		branch:    strings.TrimSpace(bootstrap.Launch.Branch),
		// defaultBranch lets pushSessionBranch skip a branch that has not moved
		// past the freshly cloned default (no commits yet), so no empty ao/* branch
		// is published for sessions that never committed.
		defaultBranch: strings.TrimSpace(bootstrap.Launch.DefaultBranch),
		harness:       bootstrap.Launch.Harness,
		scratch:       worker.IsScratchRepositoryURL(bootstrap.Launch.RepositoryURL),
		logger:        logger,
		nowFn:         time.Now,
	}
}

// checkpoint captures the transcript and uncommitted work once. It is invoked by
// the checkpoint bridge on each turn-completion (Stop hook) event; the capture is
// change-detected, so a poke with nothing new to save is a no-op.
//
// Ordering matters: the transcript is captured and durably PUT before the branch
// push runs, so a slow or failing push (network stall, non-fast-forward
// rejection) can never delay or prevent transcript capture — the agent's
// --resume path depends on it, and the push is independently recoverable.
func (cp *checkpointer) checkpoint(ctx context.Context) {
	agentSessionID, path, ok := cp.resolver.locate()
	if !ok {
		// No transcript yet (agent still booting or first turn incomplete).
		// Committed work still has no other backup (the preserve ref only
		// carries uncommitted changes), so publish it now — there is nothing
		// captured to delay.
		cp.pushSessionBranch(ctx)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		cp.logger.Warn("checkpoint: read transcript", "error", err)
		return
	}

	// Preserve uncommitted work. A failure here still lets the transcript be
	// captured so --resume works even when the working tree could not be saved.
	ref, treeSHA, err := cp.preserveWork(ctx)
	if err != nil {
		cp.logger.Warn("checkpoint: preserve uncommitted work", "error", err)
		ref, treeSHA = "", ""
	}

	// The session branch tip rides the transcript capture so the control plane
	// can attest what boot-time branch adoption may restore. Empty when there is
	// nothing to attest (scratch, no branch, or no commits); an empty tip never
	// clears a previously recorded one.
	branchTip := cp.currentBranchTip(ctx)

	hash := sha256Hex(data)
	cp.mu.Lock()
	unchanged := hash == cp.lastTranscriptHash &&
		ref == cp.lastPreservedRef &&
		treeSHA == cp.lastTreeSHA &&
		branchTip == cp.lastBranchTip
	cp.mu.Unlock()
	if unchanged {
		// Nothing new to capture; the push below still runs so a previously
		// failed push retries (subject to its failure latch).
		cp.pushSessionBranch(ctx)
		return
	}

	if err := cp.client.putTranscript(ctx, transcriptCheckpoint{
		AgentSessionID:   agentSessionID,
		Harness:          cp.harness,
		Transcript:       base64.StdEncoding.EncodeToString(data),
		PreservedGitRef:  ref,
		SessionBranchTip: branchTip,
	}); err != nil {
		cp.logger.Warn("checkpoint: push to control plane", "error", err)
		// Still push the branch: the transcript PUT failing must not stop
		// committed work from being published.
		cp.pushSessionBranch(ctx)
		return
	}

	cp.mu.Lock()
	cp.lastTranscriptHash, cp.lastPreservedRef, cp.lastTreeSHA, cp.lastBranchTip =
		hash, ref, treeSHA, branchTip
	cp.mu.Unlock()
	cp.logger.Info("captured durable checkpoint",
		"agent_session_id", agentSessionID, "preserved_ref", ref)

	cp.pushSessionBranch(ctx)
}

// currentBranchTip resolves the session branch's local tip for the checkpoint's
// attestation record. Empty for scratch repositories, sessions without a
// branch, or a branch that does not exist locally yet.
func (cp *checkpointer) currentBranchTip(ctx context.Context) string {
	if cp.scratch || cp.branch == "" {
		return ""
	}
	tip, ok := cp.revParse(ctx, "refs/heads/"+cp.branch)
	if !ok {
		return ""
	}
	return tip
}

// pushSessionBranch publishes the session's branch (refs/heads/<branch>) to
// origin so committed work survives a sandbox destroy and the branch is visible
// on GitHub even when the agent never opened a pull request. It never
// force-pushes: a non-fast-forward update (someone else advanced the remote
// branch) fails loudly instead of discarding remote commits. The decision is a
// local comparison of the branch tip against the origin remote-tracking ref (or
// the default branch when the remote does not have the branch yet), so a tick
// with nothing new to publish costs no network, and it stays correct across
// worker restarts and after the agent pushes manually. Scratch repositories
// have no origin, so this is skipped entirely.
//
// Failure handling is deliberately loud but bounded:
//   - a tip already known to be rejected is suppressed for
//     branchPushRetryInterval, so a permanently blocked branch retries every
//     5 minutes instead of on every turn completion;
//   - each new failing tip is warned and announced on the session stream
//     exactly once (session.branch_backup_degraded);
//   - when the visible branch rejects the tip, the tip is force-pushed to the
//     AO-owned preserved-branch ref so boot-time restore still has an
//     attestable fetch location for the session's own lineage;
//   - the push itself runs under branchPushTimeout.
//
// Best-effort like every checkpoint step: failures are logged and never block
// the agent.
func (cp *checkpointer) pushSessionBranch(ctx context.Context) {
	if cp.scratch || cp.branch == "" {
		return
	}
	ref := "refs/heads/" + cp.branch
	out, err := cp.git.Run(ctx, cp.workspace, nil, "rev-parse", "--verify", ref)
	if err != nil {
		// The branch does not exist locally yet — nothing committed.
		return
	}
	tip := strings.TrimSpace(out)

	remoteTip, remoteExists := cp.revParse(ctx, "refs/remotes/origin/"+cp.branch)
	if remoteExists {
		if remoteTip == tip {
			// The remote already has this exact commit (our last push, or the
			// agent's own git push — which updates the remote-tracking ref).
			cp.noteBranchPushSuccess()
			return
		}
	} else if cp.defaultBranch != "" {
		// The remote does not have the branch yet. Publish only once the branch
		// has moved past the freshly cloned default, so a session that never
		// committed does not litter the repository with an empty ao/* branch.
		if defaultTip, ok := cp.revParse(ctx, "refs/remotes/origin/"+cp.defaultBranch); ok && defaultTip == tip {
			return
		}
	}
	if cp.branchPushSuppressed(tip) {
		return
	}
	if remoteExists && !cp.isAncestor(ctx, remoteTip, tip) {
		// The remote has commits this workspace lacks; the push would be
		// rejected as non-fast-forward. Skip the doomed network attempt, keep
		// the tip fetchable via the preserved-branch ref, and announce once.
		cp.logger.Warn("checkpoint: session branch diverged from origin; skipping push",
			"branch", cp.branch, "sha", tip, "remote_sha", remoteTip)
		cp.recordBranchPushFailure(ctx, tip, "non-fast-forward")
		cp.preserveBlockedTip(ctx, tip)
		return
	}

	// GIT_TERMINAL_PROMPT=0 makes a missing credential helper fail fast instead
	// of hanging the single checkpoint consumer; the repo-local helper written by
	// ConfigureWorkerGit brokers a fresh write-scoped token on demand.
	pushCtx, cancel := context.WithTimeout(ctx, branchPushTimeout)
	defer cancel()
	if _, err := cp.git.Run(pushCtx, cp.workspace,
		map[string]string{"GIT_TERMINAL_PROMPT": "0"},
		"push", "--", "origin", ref+":"+ref); err != nil {
		cp.recordBranchPushFailure(ctx, tip, "push-failed")
		// Fresh budget: pushCtx may already be spent after a stall.
		cp.preserveBlockedTip(ctx, tip)
		return
	}
	cp.noteBranchPushSuccess()
	cp.logger.Info("checkpoint: pushed session branch", "branch", cp.branch, "sha", tip)
}

// now returns the checkpointer's clock, defaulting to time.Now for
// zero-value fixtures.
func (cp *checkpointer) now() time.Time {
	if cp.nowFn != nil {
		return cp.nowFn()
	}
	return time.Now()
}

// branchPushSuppressed reports whether this exact tip already failed within
// branchPushRetryInterval and must not be retried (or re-announced) yet.
func (cp *checkpointer) branchPushSuppressed(tip string) bool {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return cp.failedTip == tip && cp.now().Before(cp.failedAt.Add(branchPushRetryInterval))
}

// recordBranchPushFailure latches the tip for branchPushRetryInterval and, the
// first time this tip fails, warns and publishes session.branch_backup_degraded
// so the user learns their committed work is not backing up instead of
// watching every push fail silently in the worker log.
func (cp *checkpointer) recordBranchPushFailure(ctx context.Context, tip, reason string) {
	cp.mu.Lock()
	cp.failedTip, cp.failedAt = tip, cp.now()
	announce := cp.warnedTip != tip
	cp.warnedTip = tip
	cp.mu.Unlock()
	if !announce {
		return
	}
	cp.logger.Warn("checkpoint: session branch backup degraded",
		"branch", cp.branch, "sha", tip, "reason", reason)
	if cp.client == nil {
		return
	}
	if err := cp.client.publishEvent(ctx, "session.branch_backup_degraded", map[string]any{
		"branch": cp.branch,
		"sha":    tip,
		"reason": reason,
	}); err != nil {
		cp.logger.Warn("checkpoint: publish branch backup degraded event", "error", err)
	}
}

// noteBranchPushSuccess clears the failure latch: the tip is on origin (our
// push or the agent's), so a later failure for a different tip must warn again.
func (cp *checkpointer) noteBranchPushSuccess() {
	cp.mu.Lock()
	cp.failedTip, cp.failedAt, cp.warnedTip = "", time.Time{}, ""
	cp.mu.Unlock()
}

// preserveBlockedTip force-pushes the session's tip to the AO-owned
// refs/ao/preserved-branch/<session-id> ref when origin/<branch> itself
// rejected it (or the divergence gate predicted the rejection). The ref has no
// remote counterpart to conflict with, so the tip stays fetchable for
// boot-time restore even while the visible branch is blocked. Best-effort:
// failure only logs — the latch already bounds retries for this tip.
func (cp *checkpointer) preserveBlockedTip(ctx context.Context, tip string) {
	pushCtx, cancel := context.WithTimeout(ctx, branchPushTimeout)
	defer cancel()
	if _, err := cp.git.Run(pushCtx, cp.workspace,
		map[string]string{"GIT_TERMINAL_PROMPT": "0"},
		"push", "--force", "origin", tip+":"+worker.PreservedBranchRef(cp.sessionID),
	); err != nil {
		cp.logger.Warn("checkpoint: preserve blocked session branch tip",
			"branch", cp.branch, "sha", tip, "error", err)
	}
}

// isAncestor reports whether ancestor is an ancestor of — or equals — descendant.
func (cp *checkpointer) isAncestor(ctx context.Context, ancestor, descendant string) bool {
	_, err := cp.git.Run(ctx, cp.workspace, nil, "merge-base", "--is-ancestor", ancestor, descendant)
	return err == nil
}

// revParse resolves a ref to its commit SHA; ok is false when the ref is absent.
func (cp *checkpointer) revParse(ctx context.Context, ref string) (string, bool) {
	out, err := cp.git.Run(ctx, cp.workspace, nil, "rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

// preserveWork commits any uncommitted work in the checkout to
// refs/ao/preserved/<session-id> and pushes that ref to origin so it survives a
// sandbox destroy. It builds the commit through a temporary index so the agent's
// real index and working tree are never touched, and skips the push when the
// tree has not changed since the last checkpoint. A clean tree yields an empty
// ref (nothing to preserve). Scratch repositories have no origin, so preserve is
// skipped entirely.
func (cp *checkpointer) preserveWork(ctx context.Context) (ref, treeSHA string, err error) {
	if cp.scratch {
		return "", "", nil
	}
	ref = preservedRefPrefix + cp.sessionID
	treeSHA, headSHA, clean, err := cp.buildPreserveTree(ctx)
	if err != nil {
		return "", "", err
	}
	if clean {
		// Only ignored/committed content differs: nothing uncommitted to preserve.
		return "", treeSHA, nil
	}

	cp.mu.Lock()
	unchanged := treeSHA == cp.lastTreeSHA && cp.lastPreservedRef != ""
	lastRef := cp.lastPreservedRef
	cp.mu.Unlock()
	if unchanged {
		// The working tree is identical to the last pushed checkpoint; reuse it
		// rather than pushing an identical ref again.
		return lastRef, treeSHA, nil
	}

	commitSHA, err := cp.commitPreserveTree(ctx, treeSHA, headSHA)
	if err != nil {
		return "", treeSHA, err
	}
	if _, err := cp.git.Run(ctx, cp.workspace, nil, "update-ref", ref, commitSHA); err != nil {
		return "", treeSHA, fmt.Errorf("update preserve ref %q: %w", ref, err)
	}
	// Force-push the AO-owned ref so a checkpoint whose parent lineage differs
	// from a prior one still lands. The worker's repo-local credential helper
	// (ConfigureWorkerGit) authenticates the push automatically.
	if _, err := cp.git.Run(
		ctx, cp.workspace, nil, "push", "--force", "origin", commitSHA+":"+ref,
	); err != nil {
		return "", treeSHA, fmt.Errorf("push preserve ref %q: %w", ref, err)
	}
	return ref, treeSHA, nil
}

// buildPreserveTree stages every tracked and non-ignored untracked change into a
// temporary index and writes it to a tree object, without mutating the real
// index or working tree. clean is true when the tree equals HEAD's tree (no
// uncommitted work). headSHA is empty for an unborn HEAD.
func (cp *checkpointer) buildPreserveTree(ctx context.Context) (treeSHA, headSHA string, clean bool, err error) {
	// git requires GIT_INDEX_FILE to be either absent (it creates it) or a valid
	// index, so reserve a unique name and remove it before invoking git.
	tmp, err := os.CreateTemp("", "ao-preserve-idx-*")
	if err != nil {
		return "", "", false, fmt.Errorf("reserve temp index: %w", err)
	}
	tmpIdx := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpIdx)
	defer func() { _ = os.Remove(tmpIdx) }()
	env := map[string]string{"GIT_INDEX_FILE": tmpIdx}

	// Seed the temp index from HEAD so `add -A` records deletions too. An unborn
	// HEAD (no commits yet) simply starts from an empty index.
	if out, herr := cp.git.Run(ctx, cp.workspace, nil, "rev-parse", "--verify", "HEAD"); herr == nil {
		headSHA = strings.TrimSpace(out)
		if _, rerr := cp.git.Run(ctx, cp.workspace, env, "read-tree", headSHA); rerr != nil {
			return "", "", false, fmt.Errorf("seed preserve index from HEAD: %w", rerr)
		}
	}
	if _, aerr := cp.git.Run(ctx, cp.workspace, env, "add", "-A"); aerr != nil {
		return "", "", false, fmt.Errorf("stage preserve tree: %w", aerr)
	}
	treeOut, terr := cp.git.Run(ctx, cp.workspace, env, "write-tree")
	if terr != nil {
		return "", "", false, fmt.Errorf("write preserve tree: %w", terr)
	}
	treeSHA = strings.TrimSpace(treeOut)

	if headSHA != "" {
		if htOut, herr := cp.git.Run(ctx, cp.workspace, nil, "rev-parse", headSHA+"^{tree}"); herr == nil {
			clean = strings.TrimSpace(htOut) == treeSHA
		}
	}
	return treeSHA, headSHA, clean, nil
}

func (cp *checkpointer) commitPreserveTree(ctx context.Context, treeSHA, headSHA string) (string, error) {
	args := []string{"commit-tree", treeSHA, "-m", "ao preserved " + cp.sessionID}
	if headSHA != "" {
		args = []string{"commit-tree", treeSHA, "-p", headSHA, "-m", "ao preserved " + cp.sessionID}
	}
	out, err := cp.git.Run(ctx, cp.workspace, nil, args...)
	if err != nil {
		return "", fmt.Errorf("commit preserve tree: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// rehydrateSession restores a destroyed sandbox's state on boot: it fetches and
// applies the preserved uncommitted work onto the fresh checkout, then writes
// the harness transcript where --resume will find it. Best-effort: any failure
// is logged and falls through to a normal fresh launch. It must run after the
// repository checkout and before the agent is built.
func rehydrateSession(
	ctx context.Context,
	logger *slog.Logger,
	c *client,
	bootstrap worker.BootstrapResponse,
	workspace, dataDir string,
) {
	captured, ok, err := c.getTranscript(ctx)
	if err != nil {
		logger.Warn("rehydrate: fetch captured checkpoint", "error", err)
		return
	}
	if !ok {
		// Nothing captured — the normal case for a session that was never deleted.
		return
	}

	if ref := strings.TrimSpace(captured.PreservedGitRef); ref != "" {
		if err := applyPreservedRef(ctx, worker.ExecGitRunner{}, workspace, ref); err != nil {
			logger.Warn("rehydrate: apply preserved work", "ref", ref, "error", err)
		} else {
			logger.Info("rehydrate: restored uncommitted work", "ref", ref)
		}
	}

	if err := writeCapturedTranscript(captured, workspace, dataDir); err != nil {
		logger.Warn("rehydrate: write transcript", "error", err)
		return
	}
	logger.Info("rehydrate: restored transcript",
		"agent_session_id", captured.AgentSessionID, "harness", captured.Harness)
}

// applyPreservedRef fetches the preserved commit from origin and replays its
// uncommitted diff onto the working tree via a three-way merge, without
// committing or moving HEAD, so the file state returns without a stray commit.
func applyPreservedRef(ctx context.Context, git worker.GitRunner, workspace, ref string) error {
	if _, err := git.Run(ctx, workspace, nil, "fetch", "origin", ref+":"+ref); err != nil {
		return fmt.Errorf("fetch preserved ref: %w", err)
	}
	out, err := git.Run(ctx, workspace, nil, "rev-parse", "--verify", ref)
	if err != nil {
		return fmt.Errorf("resolve preserved ref: %w", err)
	}
	commitSHA := strings.TrimSpace(out)
	// cherry-pick --no-commit diffs the preserve commit against its parent (HEAD
	// at capture time) and 3-way-merges it onto the current working tree. On
	// conflict it leaves markers and exits non-zero without committing.
	if _, err := git.Run(ctx, workspace, nil, "cherry-pick", "--no-commit", commitSHA); err != nil {
		return fmt.Errorf("apply preserved ref: %w", err)
	}
	return nil
}

// writeCapturedTranscript decodes the captured transcript and writes it to the
// harness's expected resume path, creating parent directories. It never clobbers
// a transcript the agent already produced this boot.
func writeCapturedTranscript(captured transcriptCheckpoint, workspace, dataDir string) error {
	data, err := base64.StdEncoding.DecodeString(captured.Transcript)
	if err != nil {
		return fmt.Errorf("decode transcript: %w", err)
	}
	resolver := transcriptResolver{
		harness:   captured.Harness,
		dataDir:   dataDir,
		workspace: workspace,
	}
	path, err := resolver.rehydratePath(captured.AgentSessionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create transcript directory: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		// The agent already wrote a transcript at this path; leave it untouched.
		return nil
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write transcript: %w", err)
	}
	return nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
