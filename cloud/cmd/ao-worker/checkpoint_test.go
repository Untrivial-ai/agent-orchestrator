package main

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	bridgeDone := make(chan struct{})
	go func() {
		defer close(bridgeDone)
		_ = runCheckpointBridge(ctx, socket, func(context.Context) { ran <- struct{}{} }, discardLogger())
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-bridgeDone:
		case <-time.After(2 * time.Second):
			t.Fatal("checkpoint bridge did not stop after cancel")
		}
	})

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
	bridgeDone := make(chan struct{})
	go func() {
		defer close(bridgeDone)
		_ = runCheckpointBridgeWithInterval(ctx, socket, func(context.Context) { ran <- struct{}{} }, discardLogger(), 20*time.Millisecond)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-bridgeDone:
		case <-time.After(2 * time.Second):
			t.Fatal("checkpoint bridge did not stop after cancel")
		}
	})

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
