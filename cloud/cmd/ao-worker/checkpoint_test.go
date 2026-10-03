package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// The checkpoint bridge is the event trigger: a POST from the Stop hook runs one
// checkpoint. There is no timer; capture fires on turn completion.
func TestCheckpointBridgeRunsOnPoke(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cp.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ran := make(chan struct{}, 8)
	go func() {
		_ = runCheckpointBridge(ctx, socket, func(context.Context) { ran <- struct{}{} }, discardLogger())
	}()

	httpClient := &http.Client{Transport: &http.Transport{
		DialContext: func(c context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(c, "unix", socket)
		},
	}}
	// Retry the first poke until the bridge has bound its socket.
	var lastErr error
	for i := 0; i < 50; i++ {
		req, _ := http.NewRequest(http.MethodPost, "http://localhost/checkpoint", nil)
		resp, err := httpClient.Do(req)
		if err == nil {
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("poke status = %d, want 202", resp.StatusCode)
			}
			_ = resp.Body.Close()
			lastErr = nil
			break
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("poke never reached the bridge: %v", lastErr)
	}

	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("checkpoint did not run on poke")
	}
}

// The coarse periodic safety net captures in-progress work even when no Stop
// hook has fired (a long turn, or a delete/restore mid-first-turn).
func TestCheckpointBridgeSafetyNetFires(t *testing.T) {
	// A short temp dir: this test's long name makes t.TempDir() overflow the
	// ~104-char unix-socket path limit, so the bind would fail spuriously.
	dir, err := os.MkdirTemp("", "ao")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ran := make(chan struct{}, 8)
	go func() {
		_ = runCheckpointBridgeWithInterval(ctx, socket, func(context.Context) { ran <- struct{}{} }, discardLogger(), 20*time.Millisecond)
	}()

	// No HTTP poke: the checkpoint must run from the periodic safety net alone.
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("safety-net checkpoint did not run without a poke")
	}
}

func TestWriteCapturedTranscriptClaude(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	body := []byte(`{"type":"user"}` + "\n")
	captured := transcriptCheckpoint{
		AgentSessionID: "sess-1",
		Harness:        "claude-code",
		Transcript:     base64.StdEncoding.EncodeToString(body),
	}
	if err := writeCapturedTranscript(captured, "/home/ao/work", ""); err != nil {
		t.Fatalf("writeCapturedTranscript: %v", err)
	}
	want := filepath.Join(configDir, "projects", "-home-ao-work", "sess-1.jsonl")
	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read written transcript: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("transcript = %q, want %q", got, body)
	}
}

func TestWriteCapturedTranscriptDoesNotClobber(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	dir := filepath.Join(configDir, "projects", "-home-ao-work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(dir, "sess-1.jsonl")
	if err := os.WriteFile(existing, []byte("agent-produced"), 0o600); err != nil {
		t.Fatal(err)
	}
	captured := transcriptCheckpoint{
		AgentSessionID: "sess-1",
		Harness:        "claude-code",
		Transcript:     base64.StdEncoding.EncodeToString([]byte("captured")),
	}
	if err := writeCapturedTranscript(captured, "/home/ao/work", ""); err != nil {
		t.Fatalf("writeCapturedTranscript: %v", err)
	}
	got, _ := os.ReadFile(existing)
	if string(got) != "agent-produced" {
		t.Fatalf("existing transcript was clobbered: %q", got)
	}
}

// TestPreserveAndApplyRoundTrip exercises the full git capture/rehydrate path: a
// worktree's uncommitted work is committed to refs/ao/preserved/<id>, pushed to
// a local origin, then fetched and applied onto a fresh clone.
func TestPreserveAndApplyRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	ctx := context.Background()
	git := worker.ExecGitRunner{}
	root := t.TempDir()

	// Bare origin.
	origin := filepath.Join(root, "origin.git")
	mustGit(t, ctx, git, root, "init", "--bare", origin)

	// Seed origin with one commit via an initial clone.
	seed := filepath.Join(root, "seed")
	mustGit(t, ctx, git, root, "clone", origin, seed)
	configIdentity(t, ctx, git, seed)
	if err := os.WriteFile(filepath.Join(seed, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, ctx, git, seed, "add", "-A")
	mustGit(t, ctx, git, seed, "commit", "-m", "base")
	mustGit(t, ctx, git, seed, "push", "origin", "HEAD:refs/heads/main")

	// Workspace: clone, then make uncommitted edits and a new file.
	workspace := filepath.Join(root, "workspace")
	mustGit(t, ctx, git, root, "clone", origin, workspace)
	configIdentity(t, ctx, git, workspace)
	mustGit(t, ctx, git, workspace, "checkout", "-B", "main", "origin/main")
	if err := os.WriteFile(filepath.Join(workspace, "tracked.txt"), []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "untracked.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cp := &checkpointer{
		git:       git,
		workspace: workspace,
		sessionID: "sess-round",
		logger:    discardLogger(),
	}
	ref, treeSHA, err := cp.preserveWork(ctx)
	if err != nil {
		t.Fatalf("preserveWork: %v", err)
	}
	if ref != preservedRefPrefix+"sess-round" || treeSHA == "" {
		t.Fatalf("preserveWork ref=%q tree=%q", ref, treeSHA)
	}

	// Fresh sandbox: a brand-new clone with none of the uncommitted work.
	restored := filepath.Join(root, "restored")
	mustGit(t, ctx, git, root, "clone", origin, restored)
	configIdentity(t, ctx, git, restored)
	mustGit(t, ctx, git, restored, "checkout", "-B", "main", "origin/main")

	if err := applyPreservedRef(ctx, git, restored, ref); err != nil {
		t.Fatalf("applyPreservedRef: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(restored, "tracked.txt")); string(got) != "edited\n" {
		t.Fatalf("tracked edit not restored: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(restored, "untracked.txt")); string(got) != "new\n" {
		t.Fatalf("untracked file not restored: %q", got)
	}
	// The restore must not create a stray commit: HEAD stays at the base commit.
	out := mustGit(t, ctx, git, restored, "rev-list", "--count", "HEAD")
	if got := trimNL(out); got != "1" {
		t.Fatalf("HEAD advanced to %s commits; expected the base commit only", got)
	}
}

// TestPreserveWorkCleanTreeYieldsNoRef verifies a clean worktree records no
// preserve ref (nothing uncommitted to carry across a destroy).
func TestPreserveWorkCleanTreeYieldsNoRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	ctx := context.Background()
	git := worker.ExecGitRunner{}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	mustGit(t, ctx, git, root, "init", workspace)
	configIdentity(t, ctx, git, workspace)
	if err := os.WriteFile(filepath.Join(workspace, "f.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, ctx, git, workspace, "add", "-A")
	mustGit(t, ctx, git, workspace, "commit", "-m", "c")

	cp := &checkpointer{git: git, workspace: workspace, sessionID: "s", logger: discardLogger()}
	ref, _, err := cp.preserveWork(ctx)
	if err != nil {
		t.Fatalf("preserveWork: %v", err)
	}
	if ref != "" {
		t.Fatalf("clean tree yielded ref %q, want empty", ref)
	}
}

func mustGit(t *testing.T, ctx context.Context, git worker.ExecGitRunner, dir string, args ...string) string {
	t.Helper()
	out, err := git.Run(ctx, dir, nil, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func configIdentity(t *testing.T, ctx context.Context, git worker.ExecGitRunner, dir string) {
	t.Helper()
	mustGit(t, ctx, git, dir, "config", "user.name", "AO Test")
	mustGit(t, ctx, git, dir, "config", "user.email", "test@ao.local")
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

// recordingGitRunner wraps a real runner and records every command so tests can
// assert which git operations (if any) a checkpoint performed.
type recordingGitRunner struct {
	inner worker.GitRunner
	calls [][]string
}

func (r *recordingGitRunner) Run(ctx context.Context, dir string, env map[string]string, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	return r.inner.Run(ctx, dir, env, args...)
}

func (r *recordingGitRunner) pushCalls() [][]string {
	var out [][]string
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == "push" {
			out = append(out, c)
		}
	}
	return out
}

// newPushFixture builds a bare origin plus a workspace clone whose default
// branch is main, and returns both paths. The workspace is left on main at the
// origin tip with no session commits.
func newPushFixture(t *testing.T, ctx context.Context) (root, origin, workspace string) {
	t.Helper()
	git := worker.ExecGitRunner{}
	root = t.TempDir()
	origin = filepath.Join(root, "origin.git")
	mustGit(t, ctx, git, root, "init", "--bare", origin)

	seed := filepath.Join(root, "seed")
	mustGit(t, ctx, git, root, "clone", origin, seed)
	configIdentity(t, ctx, git, seed)
	if err := os.WriteFile(filepath.Join(seed, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, ctx, git, seed, "add", "-A")
	mustGit(t, ctx, git, seed, "commit", "-m", "base")
	mustGit(t, ctx, git, seed, "push", "origin", "HEAD:refs/heads/main")

	workspace = filepath.Join(root, "workspace")
	mustGit(t, ctx, git, root, "clone", origin, workspace)
	configIdentity(t, ctx, git, workspace)
	return root, origin, workspace
}

// TestPushSessionBranchPublishesCommittedWork: a session branch with no
// commits past the default branch is not published, a committed branch is
// pushed to origin (reachable from the bare remote, exactly once, never
// force), and re-pushing an unchanged tip performs no network operation.
func TestPushSessionBranchPublishesCommittedWork(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	ctx := context.Background()
	git := worker.ExecGitRunner{}
	_, origin, workspace := newPushFixture(t, ctx)
	rec := &recordingGitRunner{inner: git}
	cp := &checkpointer{
		git:           rec,
		workspace:     workspace,
		sessionID:     "sess-push",
		branch:        "ao/sess-push",
		defaultBranch: "main",
		logger:        discardLogger(),
	}

	// Local branch at the default tip (no session commits): not published.
	mustGit(t, ctx, git, workspace, "checkout", "-b", "ao/sess-push")
	cp.pushSessionBranch(ctx)
	if got := rec.pushCalls(); len(got) != 0 {
		t.Fatalf("pushed with no session commits: %v", got)
	}
	if out, err := exec.Command("git", "--git-dir", origin, "rev-parse", "--verify", "refs/heads/ao/sess-push").CombinedOutput(); err == nil {
		t.Fatalf("origin already has session branch: %s", out)
	}

	// Commit on the session branch: the branch must land on origin.
	if err := os.WriteFile(filepath.Join(workspace, "feature.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, ctx, git, workspace, "add", "-A")
	mustGit(t, ctx, git, workspace, "commit", "-m", "feature")
	localTip := strings.TrimSpace(mustGit(t, ctx, git, workspace, "rev-parse", "HEAD"))

	cp.pushSessionBranch(ctx)
	pushes := rec.pushCalls()
	if len(pushes) != 1 {
		t.Fatalf("push calls = %v, want exactly one push", pushes)
	}
	for _, push := range pushes {
		for _, arg := range push {
			if strings.Contains(arg, "force") {
				t.Fatalf("push must never force: %v", push)
			}
		}
	}
	remoteTip := strings.TrimSpace(mustGit(t, ctx, git, origin, "rev-parse", "refs/heads/ao/sess-push"))
	if remoteTip != localTip {
		t.Fatalf("origin tip %s, want %s", remoteTip, localTip)
	}

	// Unchanged tip: no further push (the 25s safety tick stays local).
	pushesBefore := len(rec.pushCalls())
	callsBefore := len(rec.calls)
	cp.pushSessionBranch(ctx)
	if got := len(rec.pushCalls()); got != pushesBefore {
		t.Fatalf("re-pushed unchanged tip: %v", rec.pushCalls()[pushesBefore:])
	}
	if len(rec.calls) == callsBefore {
		t.Fatalf("expected local rev-parse calls on the idempotent path")
	}
}

// TestPushSessionBranchSkipsScratch locks that scratch repositories (no
// origin) never attempt a push.
func TestPushSessionBranchSkipsScratch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	ctx := context.Background()
	git := worker.ExecGitRunner{}
	_, _, workspace := newPushFixture(t, ctx)

	mustGit(t, ctx, git, workspace, "checkout", "-b", "ao/sess")
	if err := os.WriteFile(filepath.Join(workspace, "f.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, ctx, git, workspace, "add", "-A")
	mustGit(t, ctx, git, workspace, "commit", "-m", "c")

	scratchRec := &recordingGitRunner{inner: git}
	scratch := &checkpointer{
		git: scratchRec, workspace: workspace, sessionID: "s",
		branch: "ao/sess", defaultBranch: "main", scratch: true, logger: discardLogger(),
	}
	scratch.pushSessionBranch(ctx)
	if got := scratchRec.pushCalls(); len(got) != 0 {
		t.Fatalf("scratch pushed: %v", got)
	}
}

// TestPushSessionBranchToleratesNonFastForward: when origin's branch has
// diverged but the workspace's tracking ref is stale (does not know yet), the
// push is attempted once, rejected without force, and the tip is preserved on
// the AO-owned preserved-branch ref so the lineage stays fetchable. The failure
// latches, so an immediate retry performs no further push.
func TestPushSessionBranchToleratesNonFastForward(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	ctx := context.Background()
	git := worker.ExecGitRunner{}
	root, origin, workspace := newPushFixture(t, ctx)

	// Local commit on the session branch.
	mustGit(t, ctx, git, workspace, "checkout", "-b", "ao/div")
	if err := os.WriteFile(filepath.Join(workspace, "local.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, ctx, git, workspace, "add", "-A")
	mustGit(t, ctx, git, workspace, "commit", "-m", "local")
	localTip := strings.TrimSpace(mustGit(t, ctx, git, workspace, "rev-parse", "HEAD"))

	// Diverged remote: an unrelated commit already on origin's ao/div.
	other := filepath.Join(root, "other")
	mustGit(t, ctx, git, root, "clone", origin, other)
	configIdentity(t, ctx, git, other)
	mustGit(t, ctx, git, other, "checkout", "-b", "ao/div", "origin/main")
	if err := os.WriteFile(filepath.Join(other, "remote.txt"), []byte("remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, ctx, git, other, "add", "-A")
	mustGit(t, ctx, git, other, "commit", "-m", "remote")
	mustGit(t, ctx, git, other, "push", "origin", "ao/div")
	remoteTip := strings.TrimSpace(mustGit(t, ctx, git, origin, "rev-parse", "refs/heads/ao/div"))

	rec := &recordingGitRunner{inner: git}
	cp := &checkpointer{
		git: rec, workspace: workspace, sessionID: "s",
		branch: "ao/div", defaultBranch: "main", logger: discardLogger(),
	}
	cp.pushSessionBranch(ctx) // must not panic; failure is logged only

	pushes := rec.pushCalls()
	if len(pushes) != 2 {
		t.Fatalf("push attempts = %v, want one rejected branch push plus the preserved-branch fallback", pushes)
	}
	if !containsArg(pushes[1], "--force") ||
		!containsArg(pushes[1], localTip+":"+worker.PreservedBranchRef("s")) {
		t.Fatalf("second push = %v, want a force-push of the local tip to the preserved-branch ref", pushes[1])
	}
	if got := strings.TrimSpace(mustGit(t, ctx, git, origin, "rev-parse", "refs/heads/ao/div")); got != remoteTip {
		t.Fatalf("remote tip changed to %s, want %s (non-fast-forward must not overwrite)", got, remoteTip)
	}
	preserved := strings.TrimSpace(mustGit(t, ctx, git, origin, "rev-parse", worker.PreservedBranchRef("s")))
	if preserved != localTip {
		t.Fatalf("preserved-branch tip %s, want the local tip %s", preserved, localTip)
	}

	// Latched: an immediate retry performs no further push for this tip.
	before := len(rec.pushCalls())
	cp.pushSessionBranch(ctx)
	if got := len(rec.pushCalls()); got != before {
		t.Fatalf("latched tip retried: %v", rec.pushCalls()[before:])
	}
}

// TestPushSessionBranchDivergenceGateSkipsDoomedPush locks the bounded-failure
// behavior: once the workspace's origin tracking ref shows the remote branch
// ahead, the checkpoint must not attempt the doomed non-fast-forward push at
// all. It force-pushes the local tip to the AO-owned preserved-branch ref
// instead, publishes session.branch_backup_degraded exactly once per tip, and
// latches retries for branchPushRetryInterval - after which the attempt is
// retried but the event is still not re-sent.
func TestPushSessionBranchDivergenceGateSkipsDoomedPush(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	ctx := context.Background()
	git := worker.ExecGitRunner{}
	root, origin, workspace := newPushFixture(t, ctx)

	// Local commit on the session branch.
	mustGit(t, ctx, git, workspace, "checkout", "-b", "ao/gate")
	if err := os.WriteFile(filepath.Join(workspace, "local.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, ctx, git, workspace, "add", "-A")
	mustGit(t, ctx, git, workspace, "commit", "-m", "local")
	localTip := strings.TrimSpace(mustGit(t, ctx, git, workspace, "rev-parse", "HEAD"))

	// Diverged remote, fetched so the workspace's tracking ref reflects it -
	// the divergence gate's precondition.
	other := filepath.Join(root, "other")
	mustGit(t, ctx, git, root, "clone", origin, other)
	configIdentity(t, ctx, git, other)
	mustGit(t, ctx, git, other, "checkout", "-b", "ao/gate", "origin/main")
	if err := os.WriteFile(filepath.Join(other, "remote.txt"), []byte("remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, ctx, git, other, "add", "-A")
	mustGit(t, ctx, git, other, "commit", "-m", "remote")
	mustGit(t, ctx, git, other, "push", "origin", "ao/gate")
	remoteTip := strings.TrimSpace(mustGit(t, ctx, git, origin, "rev-parse", "refs/heads/ao/gate"))
	mustGit(t, ctx, git, workspace, "fetch", "origin")
	if got := strings.TrimSpace(mustGit(t, ctx, git, workspace, "rev-parse", "refs/remotes/origin/ao/gate")); got != remoteTip {
		t.Fatalf("tracking ref %s, want %s", got, remoteTip)
	}

	// Control-plane event sink: records every published event request.
	var mu sync.Mutex
	var events []worker.EventRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req worker.EventRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		events = append(events, req)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	clock := time.Now()
	rec := &recordingGitRunner{inner: git}
	cp := &checkpointer{
		git: rec, workspace: workspace, sessionID: "s-gate",
		branch: "ao/gate", defaultBranch: "main", logger: discardLogger(),
		client: &client{baseURL: srv.URL, http: srv.Client()},
		nowFn:  func() time.Time { return clock },
	}

	cp.pushSessionBranch(ctx)

	pushes := rec.pushCalls()
	if len(pushes) != 1 || !containsArg(pushes[0], "--force") ||
		!containsArg(pushes[0], localTip+":"+worker.PreservedBranchRef("s-gate")) {
		t.Fatalf("push calls = %v, want only the preserved-branch force-push (doomed push must be skipped)", pushes)
	}
	if got := strings.TrimSpace(mustGit(t, ctx, git, origin, "rev-parse", "refs/heads/ao/gate")); got != remoteTip {
		t.Fatalf("remote tip changed to %s, want %s", got, remoteTip)
	}
	preserved := strings.TrimSpace(mustGit(t, ctx, git, origin, "rev-parse", worker.PreservedBranchRef("s-gate")))
	if preserved != localTip {
		t.Fatalf("preserved-branch tip %s, want local tip %s", preserved, localTip)
	}
	mu.Lock()
	published := append([]worker.EventRequest(nil), events...)
	mu.Unlock()
	if len(published) != 1 || published[0].Type != "session.branch_backup_degraded" {
		t.Fatalf("published events = %+v, want exactly one session.branch_backup_degraded", published)
	}

	// Latched: an immediate retry performs no push and re-publishes nothing.
	cp.pushSessionBranch(ctx)
	if got := len(rec.pushCalls()); got != 1 {
		t.Fatalf("latched tip retried: %v", rec.pushCalls()[1:])
	}
	mu.Lock()
	count := len(events)
	mu.Unlock()
	if count != 1 {
		t.Fatalf("event re-published for latched tip: %d", count)
	}

	// After the retry interval the push is attempted again (still just the
	// preserved-branch ref), but the failure was already announced for this tip.
	clock = clock.Add(branchPushRetryInterval + time.Second)
	cp.pushSessionBranch(ctx)
	if got := len(rec.pushCalls()); got != 2 {
		t.Fatalf("push not retried after the retry interval: %v", rec.pushCalls())
	}
	mu.Lock()
	count = len(events)
	mu.Unlock()
	if count != 1 {
		t.Fatalf("published %d events for one tip, want 1", count)
	}
}
