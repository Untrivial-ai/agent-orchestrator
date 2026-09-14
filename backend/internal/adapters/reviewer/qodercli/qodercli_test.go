package qodercli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	workeragent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/qodercli"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// fakeBinary puts an executable named qodercli on PATH so binary resolution
// succeeds without a real install.
func fakeBinary(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim layout differs on Windows")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "qodercli"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func flagValue(argv []string, flag string) (string, bool) {
	index := slices.Index(argv, flag)
	if index < 0 || index+1 >= len(argv) {
		return "", false
	}
	return argv[index+1], true
}

func countFlag(argv []string, flag string) int {
	count := 0
	for _, arg := range argv {
		if arg == flag {
			count++
		}
	}
	return count
}

func TestReviewCommandIsReadOnlyAndNeverAsks(t *testing.T) {
	fakeBinary(t)

	spec, err := New().ReviewCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:    "reviewer-1",
		WorkspacePath: t.TempDir(),
		DataDir:       t.TempDir(),
		Prompt:        "review PR 1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// dont_ask is the whole reason this adapter composes its own argv: an
	// unattended pane must never stall on a dialog, and `auto` would hand the
	// decision to a classifier model that may not exist.
	if mode, ok := flagValue(spec.Argv, "--permission-mode"); !ok || mode != "dont_ask" {
		t.Fatalf("permission mode = %q, want dont_ask: %#v", mode, spec.Argv)
	}

	// --tools is the containment: everything outside this set is denied
	// outright rather than merely left unapproved.
	toolsAt := slices.Index(spec.Argv, "--tools")
	if toolsAt < 0 {
		t.Fatalf("no --tools restriction: %#v", spec.Argv)
	}
	gotTools := spec.Argv[toolsAt+1 : toolsAt+1+len(reviewerCoreTools)]
	if !slices.Equal(gotTools, reviewerCoreTools) {
		t.Fatalf("core tools = %#v, want %#v", gotTools, reviewerCoreTools)
	}
	for _, writeTool := range []string{"Edit", "Write", "NotebookEdit"} {
		if slices.Contains(gotTools, writeTool) {
			t.Fatalf("%s must not exist in a reviewer process", writeTool)
		}
	}

	if got := countFlag(spec.Argv, "--allowed-tools"); got != len(reviewerAllowedTools) {
		t.Fatalf("allowed-tools occurrences = %d, want %d", got, len(reviewerAllowedTools))
	}
	if got := countFlag(spec.Argv, "--disallowed-tools"); got != len(reviewerDisallowedTools) {
		t.Fatalf("disallowed-tools occurrences = %d, want %d", got, len(reviewerDisallowedTools))
	}

	if prompt, ok := flagValue(spec.Argv, "-i"); !ok || prompt != "review PR 1" {
		t.Fatalf("task = %q, want the review prompt", prompt)
	}
	if spec.AgentSessionID != workeragent.SessionUUID("reviewer-1") {
		t.Fatalf("agent session id = %q, want the derived reviewer id", spec.AgentSessionID)
	}
}

func TestReviewRestoreKeepsToolPolicyAndDefersTheTask(t *testing.T) {
	fakeBinary(t)

	configRoot := t.TempDir()
	t.Setenv("QODER_CONFIG_DIR", configRoot)

	workspace := "/work/repo-a"
	agentSessionID := workeragent.SessionUUID("reviewer-1")
	bucket := filepath.Join(configRoot, "projects", "-work-repo-a")
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bucket, agentSessionID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	spec, ok, err := New().ReviewRestoreCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:     "reviewer-1",
		RunID:          "run-1",
		AgentSessionID: agentSessionID,
		WorkspacePath:  workspace,
		DataDir:        t.TempDir(),
		Prompt:         "review PR 2",
	})
	if err != nil || !ok {
		t.Fatalf("restore ok=%v err=%v", ok, err)
	}

	// Qoder CLI rebuilds permission rules from flags on resume, so a restore
	// that dropped them would come back in dont_ask with nothing allowed and
	// every git/gh call would be refused in silence.
	if mode, _ := flagValue(spec.Argv, "--permission-mode"); mode != "dont_ask" {
		t.Fatalf("restore lost the permission mode: %#v", spec.Argv)
	}
	if !slices.Contains(spec.Argv, "--tools") {
		t.Fatalf("restore lost the tool restriction: %#v", spec.Argv)
	}
	if got := countFlag(spec.Argv, "--allowed-tools"); got != len(reviewerAllowedTools) {
		t.Fatalf("restore allowed-tools occurrences = %d, want %d", got, len(reviewerAllowedTools))
	}
	if resume, ok := flagValue(spec.Argv, "--resume"); !ok || resume != agentSessionID {
		t.Fatalf("restore should resume %q: %#v", agentSessionID, spec.Argv)
	}

	// The task is injected after startup: --resume resolves asynchronously in
	// the TUI, so a command-line turn could be submitted into a session that
	// has not finished loading.
	if slices.Contains(spec.Argv, "-i") {
		t.Fatalf("restore must not carry a command-line turn: %#v", spec.Argv)
	}
	if spec.InitialMessage != "review PR 2" {
		t.Fatalf("initial message = %q, want the review prompt", spec.InitialMessage)
	}
	if !spec.NativeResumed {
		t.Fatal("restore should report a native resume")
	}
}

func TestReviewRestoreRelaunchesWhenNoTranscriptExists(t *testing.T) {
	fakeBinary(t)
	t.Setenv("QODER_CONFIG_DIR", t.TempDir())

	_, ok, err := New().ReviewRestoreCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:    "reviewer-1",
		WorkspacePath: "/work/repo-a",
		Prompt:        "review PR 1",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Resuming an id Qoder CLI cannot resolve opens its session-picker modal,
	// which would strand the pane; a fresh launch is the safe answer.
	if ok {
		t.Fatal("restore must report false when the reviewer has no transcript")
	}
}
