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

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// preservedRefPrefix namespaces the git ref that carries a session's uncommitted
// work across a sandbox destroy. It matches desktop AO's
// refs/ao/preserved/<session-id> convention so both implementations agree.
const preservedRefPrefix = "refs/ao/preserved/"

// checkpointer captures a session's resume state to the control plane so a
// deleted (and later restored) sandbox can be rebuilt. Every method is
// best-effort: a failure is logged and never blocks the agent or crashes the
// worker.
type checkpointer struct {
	client    *client
	resolver  transcriptResolver
	git       worker.GitRunner
	workspace string
	sessionID string
	harness   string
	scratch   bool
	logger    *slog.Logger

	mu sync.Mutex
	// change detection: an identical checkpoint is neither re-pushed nor re-sent.
	lastTranscriptHash string
	lastPreservedRef   string
	lastTreeSHA        string
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
		harness:   bootstrap.Launch.Harness,
		scratch:   worker.IsScratchRepositoryURL(bootstrap.Launch.RepositoryURL),
		logger:    logger,
	}
}

// checkpoint captures the transcript and uncommitted work once. It is invoked by
// the checkpoint bridge on each turn-completion (Stop hook) event; the capture is
// change-detected, so a poke with nothing new to save is a no-op.
func (cp *checkpointer) checkpoint(ctx context.Context) {
	agentSessionID, path, ok := cp.resolver.locate()
	if !ok {
		// No transcript yet (agent still booting or first turn incomplete).
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

	hash := sha256Hex(data)
	cp.mu.Lock()
	unchanged := hash == cp.lastTranscriptHash &&
		ref == cp.lastPreservedRef &&
		treeSHA == cp.lastTreeSHA
	cp.mu.Unlock()
	if unchanged {
		return
	}

	if err := cp.client.putTranscript(ctx, transcriptCheckpoint{
		AgentSessionID:  agentSessionID,
		Harness:         cp.harness,
		Transcript:      base64.StdEncoding.EncodeToString(data),
		PreservedGitRef: ref,
	}); err != nil {
		cp.logger.Warn("checkpoint: push to control plane", "error", err)
		return
	}

	cp.mu.Lock()
	cp.lastTranscriptHash, cp.lastPreservedRef, cp.lastTreeSHA = hash, ref, treeSHA
	cp.mu.Unlock()
	cp.logger.Info("captured durable checkpoint",
		"agent_session_id", agentSessionID, "preserved_ref", ref)
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
// the harness transcript where --resume will find it. It must run after the
// repository checkout and before the agent is built.
func rehydrateSession(
	ctx context.Context,
	logger *slog.Logger,
	c *client,
	bootstrap worker.BootstrapResponse,
	workspace, dataDir string,
) (bool, error) {
	captured, ok, err := c.getTranscript(ctx)
	if err != nil {
		return false, fmt.Errorf("fetch captured checkpoint: %w", err)
	}
	if !ok {
		return false, nil
	}

	if ref := strings.TrimSpace(captured.PreservedGitRef); ref != "" {
		if err := applyPreservedRef(ctx, worker.ExecGitRunner{}, workspace, ref); err != nil {
			return false, fmt.Errorf("apply preserved work %s: %w", ref, err)
		}
		logger.Info("rehydrate: restored uncommitted work", "ref", ref)
	}

	if err := writeCapturedTranscript(captured, workspace, dataDir); err != nil {
		return false, fmt.Errorf("write captured transcript: %w", err)
	}
	logger.Info("rehydrate: restored transcript",
		"agent_session_id", captured.AgentSessionID, "harness", captured.Harness)
	return true, nil
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
