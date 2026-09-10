package multi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeMerger struct {
	key        string
	mergeErr   error
	mergeCount int
	lastReq    ports.SCMMergeRequest
}

func (f *fakeMerger) MergePullRequest(_ context.Context, request ports.SCMMergeRequest) (ports.SCMMergeResult, error) {
	f.mergeCount++
	f.lastReq = request
	if f.mergeErr != nil {
		return ports.SCMMergeResult{}, f.mergeErr
	}
	return ports.SCMMergeResult{MergeCommitSHA: "sha-" + f.key}, nil
}

func TestMerger_RoutesByProvider(t *testing.T) {
	gh := &fakeMerger{key: "github"}
	gl := &fakeMerger{key: "gitlab"}
	m := NewMerger(
		NamedMerger{Key: "github", Merger: gh},
		NamedMerger{Key: "gitlab", Merger: gl},
	)

	req := ports.SCMMergeRequest{
		PR:              ports.SCMPRRef{Repo: ports.SCMRepo{Provider: "gitlab"}, Number: 42},
		ExpectedHeadSHA: "abc123",
		Method:          ports.SCMMergeSquash,
	}
	res, err := m.MergePullRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.MergeCommitSHA != "sha-gitlab" {
		t.Errorf("MergeCommitSHA = %q, want %q (routed to wrong merger)", res.MergeCommitSHA, "sha-gitlab")
	}
	if gh.mergeCount != 0 {
		t.Errorf("github merger called %d times, want 0", gh.mergeCount)
	}
	if gl.mergeCount != 1 {
		t.Errorf("gitlab merger called %d times, want 1", gl.mergeCount)
	}
	if gl.lastReq.PR.Number != 42 {
		t.Errorf("gitlab merger received PR.Number = %d, want 42", gl.lastReq.PR.Number)
	}
}

func TestMerger_RoutesBothProviders(t *testing.T) {
	gh := &fakeMerger{key: "github"}
	gl := &fakeMerger{key: "gitlab"}
	m := NewMerger(
		NamedMerger{Key: "github", Merger: gh},
		NamedMerger{Key: "gitlab", Merger: gl},
	)

	ghReq := ports.SCMMergeRequest{PR: ports.SCMPRRef{Repo: ports.SCMRepo{Provider: "github"}, Number: 1}}
	glReq := ports.SCMMergeRequest{PR: ports.SCMPRRef{Repo: ports.SCMRepo{Provider: "gitlab"}, Number: 2}}

	if _, err := m.MergePullRequest(context.Background(), ghReq); err != nil {
		t.Fatal(err)
	}
	if _, err := m.MergePullRequest(context.Background(), glReq); err != nil {
		t.Fatal(err)
	}

	if gh.mergeCount != 1 || gl.mergeCount != 1 {
		t.Errorf("merge counts: github=%d gitlab=%d, want 1/1", gh.mergeCount, gl.mergeCount)
	}
}

func TestMerger_UnknownProviderReturnsError(t *testing.T) {
	gh := &fakeMerger{key: "github"}
	m := NewMerger(NamedMerger{Key: "github", Merger: gh})

	req := ports.SCMMergeRequest{
		PR: ports.SCMPRRef{Repo: ports.SCMRepo{Provider: "bitbucket"}, Number: 1},
	}
	_, err := m.MergePullRequest(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	}
	if !strings.Contains(err.Error(), "bitbucket") {
		t.Errorf("error should mention unknown key 'bitbucket', got: %s", err.Error())
	}
	if gh.mergeCount != 0 {
		t.Errorf("github merger called %d times, want 0 (unknown provider must not dispatch)", gh.mergeCount)
	}
}

func TestMerger_SingleProviderFallback(t *testing.T) {
	// Only gitlab is registered — github was unavailable at construction time.
	// The merger must still serve gitlab merge requests.
	gl := &fakeMerger{key: "gitlab"}
	m := NewMerger(NamedMerger{Key: "gitlab", Merger: gl})

	req := ports.SCMMergeRequest{
		PR:              ports.SCMPRRef{Repo: ports.SCMRepo{Provider: "gitlab"}, Number: 7},
		ExpectedHeadSHA: "deadbeef",
		Method:          ports.SCMMergeSquash,
	}
	res, err := m.MergePullRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.MergeCommitSHA != "sha-gitlab" {
		t.Errorf("MergeCommitSHA = %q, want %q", res.MergeCommitSHA, "sha-gitlab")
	}

	// A request for the missing provider must still return a clear error, not
	// a nil deref.
	ghReq := ports.SCMMergeRequest{
		PR: ports.SCMPRRef{Repo: ports.SCMRepo{Provider: "github"}, Number: 1},
	}
	_, err = m.MergePullRequest(context.Background(), ghReq)
	if err == nil {
		t.Fatal("expected error for missing github provider, got nil")
	}
}

func TestMerger_PropagatesProviderError(t *testing.T) {
	mergeErr := errors.New("gitlab 503")
	gl := &fakeMerger{key: "gitlab", mergeErr: mergeErr}
	m := NewMerger(NamedMerger{Key: "gitlab", Merger: gl})

	req := ports.SCMMergeRequest{
		PR: ports.SCMPRRef{Repo: ports.SCMRepo{Provider: "gitlab"}, Number: 1},
	}
	_, err := m.MergePullRequest(context.Background(), req)
	if !errors.Is(err, mergeErr) {
		t.Errorf("err = %v, want %v", err, mergeErr)
	}
}
