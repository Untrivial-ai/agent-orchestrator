package workerexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestPullRequestObserverClaimsCreatedPRFromCommandOutput(t *testing.T) {
	var claimed []string
	observer := pullRequestObserver{
		lookupHead: func(context.Context, string) (pullRequestHead, error) {
			return pullRequestHead{SHA: "abc123", Branch: "feature"}, nil
		},
		hasCommit: func(context.Context, string, pullRequestHead) bool { return true },
		claim:     func(_ context.Context, url string) error { claimed = append(claimed, url); return nil },
	}
	observer.observe(Output{Activity: &worker.ChatActivity{Kind: "command", Status: "completed", Detail: map[string]any{
		"command": "curl -X POST https://api.github.com/repos/acme/widgets/pulls",
		"output":  `{"html_url":"https://github.com/acme/widgets/pull/7"}`,
	}}})
	observer.claimObserved(context.Background(), "/tmp/repo")
	if len(claimed) != 1 || claimed[0] != "https://github.com/acme/widgets/pull/7" {
		t.Fatalf("claimed = %v, want created PR URL", claimed)
	}
}

func TestCheckoutHasCommitRequiresMatchingLocalBranch(t *testing.T) {
	workspace := t.TempDir()
	ctx := context.Background()
	run := func(args ...string) string {
		t.Helper()
		output, err := exec.CommandContext(ctx, "git", append([]string{"-C", workspace}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	run("init", "--quiet")
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--quiet", "-m", "test")
	run("branch", "feature")
	sha := run("rev-parse", "HEAD")
	if !checkoutHasCommit(ctx, workspace, pullRequestHead{SHA: sha, Branch: "feature"}) {
		t.Fatal("matching branch was rejected")
	}
	if checkoutHasCommit(ctx, workspace, pullRequestHead{SHA: sha, Branch: "unrelated"}) {
		t.Fatal("missing branch was accepted")
	}
}

func TestPullRequestObserverDoesNotClaimForeignCommit(t *testing.T) {
	claimed := false
	observer := pullRequestObserver{
		lookupHead: func(context.Context, string) (pullRequestHead, error) {
			return pullRequestHead{SHA: "foreign", Branch: "other"}, nil
		},
		hasCommit: func(context.Context, string, pullRequestHead) bool { return false },
		claim:     func(context.Context, string) error { claimed = true; return nil },
	}
	observer.observe(Output{Activity: &worker.ChatActivity{Kind: "command", Status: "completed", Detail: map[string]any{
		"output": "https://github.com/acme/widgets/pull/7",
	}}})
	observer.claimObserved(context.Background(), "/tmp/repo")
	if claimed {
		t.Fatal("foreign PR was claimed")
	}
}

func TestPullRequestObserverFindsURLAcrossAssistantChunks(t *testing.T) {
	var claimed string
	observer := pullRequestObserver{
		lookupHead: func(context.Context, string) (pullRequestHead, error) {
			return pullRequestHead{SHA: "abc123", Branch: "feature"}, nil
		},
		hasCommit: func(context.Context, string, pullRequestHead) bool { return true },
		claim:     func(_ context.Context, url string) error { claimed = url; return nil },
	}
	observer.observe(Output{Stream: "stdout", Text: "Created https://github.com/acme/widgets/pu"})
	observer.observe(Output{Stream: "stdout", Text: "ll/9 for review."})
	if err := observer.claimObserved(context.Background(), "/tmp/repo"); err != nil {
		t.Fatal(err)
	}
	if claimed != "https://github.com/acme/widgets/pull/9" {
		t.Fatalf("claimed %q", claimed)
	}
}
