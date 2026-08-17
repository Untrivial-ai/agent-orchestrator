package pr

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeActionStore struct {
	pr domain.PullRequest
	ok bool
}

func (f *fakeActionStore) GetPR(context.Context, string) (domain.PullRequest, bool, error) {
	return f.pr, f.ok, nil
}

type fakeSCMAction struct {
	observation ports.SCMObservation
	review      ports.SCMReviewObservation
	mergeErr    error
	request     ports.SCMMergeRequest
	mergeCalls  int
}

func (f *fakeSCMAction) FetchPullRequests(context.Context, []ports.SCMPRRef) ([]ports.SCMObservation, error) {
	return []ports.SCMObservation{f.observation}, nil
}

func (f *fakeSCMAction) FetchReviewThreads(context.Context, ports.SCMPRRef) (ports.SCMReviewObservation, error) {
	return f.review, nil
}

func (f *fakeSCMAction) MergePullRequest(_ context.Context, request ports.SCMMergeRequest) (ports.SCMMergeResult, error) {
	f.mergeCalls++
	f.request = request
	return ports.SCMMergeResult{MergeCommitSHA: "merge-sha"}, f.mergeErr
}

func mergeableActionFixture() (domain.PullRequest, *fakeSCMAction) {
	pr := domain.PullRequest{
		URL:          "https://github.com/acme/widgets/pull/42",
		Number:       42,
		Provider:     "github",
		Host:         "github.com",
		Repo:         "acme/widgets",
		HeadSHA:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Mergeability: domain.MergeMergeable,
	}
	scm := &fakeSCMAction{observation: ports.SCMObservation{
		Fetched:      true,
		PR:           ports.SCMPRObservation{URL: pr.URL, Number: pr.Number, HeadSHA: pr.HeadSHA},
		CI:           ports.SCMCIObservation{Summary: string(domain.CIPassing), HeadSHA: pr.HeadSHA},
		Mergeability: ports.SCMMergeabilityObservation{State: string(domain.MergeMergeable), Mergeable: true},
	}}
	return pr, scm
}

func TestActionServiceMerge_GuardsAndSquashMergesExactHead(t *testing.T) {
	pr, scm := mergeableActionFixture()
	svc := NewActionService(ActionDeps{Store: &fakeActionStore{pr: pr, ok: true}, Reader: scm, Merger: scm})
	result, err := svc.Merge(context.Background(), MergeRequest{PRID: "42", PRURL: pr.URL, ExpectedHeadSHA: pr.HeadSHA})
	if err != nil {
		t.Fatal(err)
	}
	if result.PRNumber != 42 || result.Method != "squash" || result.MergeCommitSHA != "merge-sha" {
		t.Fatalf("result = %#v", result)
	}
	if scm.mergeCalls != 1 || scm.request.ExpectedHeadSHA != pr.HeadSHA || scm.request.Method != ports.SCMMergeSquash {
		t.Fatalf("request = %#v, calls = %d", scm.request, scm.mergeCalls)
	}
}

func TestActionServiceMerge_FailsClosedForStaleHeadOrReadiness(t *testing.T) {
	pr, scm := mergeableActionFixture()
	svc := NewActionService(ActionDeps{Store: &fakeActionStore{pr: pr, ok: true}, Reader: scm, Merger: scm})
	_, err := svc.Merge(context.Background(), MergeRequest{PRID: "42", PRURL: pr.URL, ExpectedHeadSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"})
	if !errors.Is(err, ErrPRHeadChanged) || scm.mergeCalls != 0 {
		t.Fatalf("stale head error = %v, calls = %d", err, scm.mergeCalls)
	}

	pr, scm = mergeableActionFixture()
	scm.observation.CI.Summary = string(domain.CIPending)
	svc = NewActionService(ActionDeps{Store: &fakeActionStore{pr: pr, ok: true}, Reader: scm, Merger: scm})
	_, err = svc.Merge(context.Background(), MergeRequest{PRID: "42", PRURL: pr.URL, ExpectedHeadSHA: pr.HeadSHA})
	if !errors.Is(err, ErrPRPreconditions) || scm.mergeCalls != 0 {
		t.Fatalf("pending CI error = %v, calls = %d", err, scm.mergeCalls)
	}
}

func TestScmRepoForPR_NestedNamespace(t *testing.T) {
	tests := []struct {
		name      string
		repo      string
		wantOK    bool
		wantOwner string
		wantName  string
	}{
		{
			name:      "nested GitLab namespace group/subgroup/project",
			repo:      "group/subgroup/project",
			wantOK:    true,
			wantOwner: "group/subgroup",
			wantName:  "project",
		},
		{
			name:      "standard owner/repo",
			repo:      "owner/repo",
			wantOK:    true,
			wantOwner: "owner",
			wantName:  "repo",
		},
		{
			name:   "single segment rejected",
			repo:   "single",
			wantOK: false,
		},
		{
			name:   "empty string rejected",
			repo:   "",
			wantOK: false,
		},
		{
			name:      "deeply nested group/a/b/c/project",
			repo:      "group/a/b/c/project",
			wantOK:    true,
			wantOwner: "group/a/b/c",
			wantName:  "project",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, ok := scmRepoForPR(domain.PullRequest{Repo: tt.repo, Provider: "gitlab", Host: "gitlab.com"})
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if repo.Owner != tt.wantOwner {
				t.Errorf("Owner = %q, want %q", repo.Owner, tt.wantOwner)
			}
			if repo.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", repo.Name, tt.wantName)
			}
		})
	}
}

func TestActionServiceMerge_MapsProviderConflict(t *testing.T) {
	pr, scm := mergeableActionFixture()
	scm.mergeErr = ports.ErrSCMHeadChanged
	svc := NewActionService(ActionDeps{Store: &fakeActionStore{pr: pr, ok: true}, Reader: scm, Merger: scm})
	_, err := svc.Merge(context.Background(), MergeRequest{PRID: "42", PRURL: pr.URL, ExpectedHeadSHA: pr.HeadSHA})
	if !errors.Is(err, ErrPRHeadChanged) {
		t.Fatalf("error = %v", err)
	}
}
