package pr

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeActionStore struct {
	pr       domain.PullRequest
	ok       bool
	checks   []domain.PullRequestCheck
	comments []domain.PullRequestComment
	threads  []domain.PullRequestReviewThread
	reviews  []domain.PullRequestReview
}

func (f *fakeActionStore) GetPR(context.Context, string) (domain.PullRequest, bool, error) {
	return f.pr, f.ok, nil
}

func (f *fakeActionStore) GetPRByNumber(_ context.Context, number int) (domain.PullRequest, bool, error) {
	return f.pr, f.ok && f.pr.Number == number, nil
}

func (f *fakeActionStore) ListChecks(context.Context, string) ([]domain.PullRequestCheck, error) {
	return append([]domain.PullRequestCheck(nil), f.checks...), nil
}

func (f *fakeActionStore) ListPRComments(context.Context, string) ([]domain.PullRequestComment, error) {
	return append([]domain.PullRequestComment(nil), f.comments...), nil
}

func (f *fakeActionStore) ListPRReviewThreads(context.Context, string) ([]domain.PullRequestReviewThread, error) {
	return append([]domain.PullRequestReviewThread(nil), f.threads...), nil
}

func (f *fakeActionStore) ListPRReviews(context.Context, string) ([]domain.PullRequestReview, error) {
	return append([]domain.PullRequestReview(nil), f.reviews...), nil
}

type fakeActionWriter struct {
	writeCalls int
	threads    []domain.PullRequestReviewThread
	comments   []domain.PullRequestComment
	writeErr   error
}

func (f *fakeActionWriter) WriteSCMObservation(_ context.Context, _ domain.PullRequest, _ []domain.PullRequestCheck, _ []domain.PullRequestReview, threads []domain.PullRequestReviewThread, comments []domain.PullRequestComment, _ ports.ReviewWriteMode) error {
	f.writeCalls++
	if f.writeErr != nil {
		return f.writeErr
	}
	f.threads = append([]domain.PullRequestReviewThread(nil), threads...)
	f.comments = append([]domain.PullRequestComment(nil), comments...)
	return nil
}

type fakeSCMAction struct {
	observation     ports.SCMObservation
	review          ports.SCMReviewObservation
	mergeErr        error
	request         ports.SCMMergeRequest
	mergeCalls      int
	resolveErr      error
	resolveCalls    int
	resolveRequests []ports.SCMReviewResolveRequest
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

func (f *fakeSCMAction) ResolveReviewThread(_ context.Context, request ports.SCMReviewResolveRequest) error {
	f.resolveCalls++
	f.resolveRequests = append(f.resolveRequests, request)
	return f.resolveErr
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

func TestActionServiceMerge_MergesAPRThatNeedsNoReview(t *testing.T) {
	pr, scm := mergeableActionFixture()
	scm.review = ports.SCMReviewObservation{
		Decision: string(domain.ReviewNone),
		Reviews:  []ports.SCMReviewSummaryObservation{{ID: "r1", State: "COMMENTED"}},
	}
	svc := NewActionService(ActionDeps{Store: &fakeActionStore{pr: pr, ok: true}, Reader: scm, Merger: scm})
	if _, err := svc.Merge(context.Background(), MergeRequest{PRID: "42", PRURL: pr.URL, ExpectedHeadSHA: pr.HeadSHA}); err != nil {
		t.Fatal(err)
	}
	if scm.mergeCalls != 1 {
		t.Fatalf("merge calls = %d, want 1", scm.mergeCalls)
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

func TestActionServiceResolveComments_ResolvesRemoteThreadsBeforeWritingLocalState(t *testing.T) {
	pr, scm := mergeableActionFixture()
	scm.review = ports.SCMReviewObservation{Threads: []ports.SCMReviewThreadObservation{
		{ID: "thread-1"},
		{ID: "thread-2", Resolved: true},
		{ID: "thread-3"},
	}}
	store := &fakeActionStore{
		pr: pr, ok: true,
		threads:  []domain.PullRequestReviewThread{{ThreadID: "thread-1"}, {ThreadID: "thread-3"}},
		comments: []domain.PullRequestComment{{ID: "comment-1", ThreadID: "thread-1"}, {ID: "comment-3", ThreadID: "thread-3"}},
	}
	writer := &fakeActionWriter{}
	svc := NewActionService(ActionDeps{Store: store, Reader: scm, Resolver: scm, Writer: writer})

	result, err := svc.ResolveComments(context.Background(), "42", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Resolved != 2 || scm.resolveCalls != 2 || writer.writeCalls != 1 {
		t.Fatalf("result=%+v resolveCalls=%d writeCalls=%d", result, scm.resolveCalls, writer.writeCalls)
	}
	for _, request := range scm.resolveRequests {
		if request.PR.Number != 42 || request.PR.Repo.Owner != "acme" || request.PR.Repo.Name != "widgets" {
			t.Fatalf("resolve request = %+v", request)
		}
	}
	for _, thread := range writer.threads {
		if !thread.Resolved {
			t.Fatalf("thread %q was not marked resolved", thread.ThreadID)
		}
	}
	for _, comment := range writer.comments {
		if !comment.Resolved {
			t.Fatalf("comment %q was not marked resolved", comment.ID)
		}
	}
}

func TestActionServiceResolveComments_ExplicitIDsAreDeduplicated(t *testing.T) {
	pr, scm := mergeableActionFixture()
	store := &fakeActionStore{pr: pr, ok: true}
	writer := &fakeActionWriter{}
	svc := NewActionService(ActionDeps{Store: store, Reader: scm, Resolver: scm, Writer: writer})

	result, err := svc.ResolveComments(context.Background(), "42", []string{"thread-1", "", "thread-1", "thread-2"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Resolved != 2 || scm.resolveCalls != 2 {
		t.Fatalf("result=%+v resolveCalls=%d", result, scm.resolveCalls)
	}
}

func TestActionServiceResolveComments_RemoteFailureDoesNotWriteLocalState(t *testing.T) {
	pr, scm := mergeableActionFixture()
	scm.review = ports.SCMReviewObservation{Threads: []ports.SCMReviewThreadObservation{{ID: "thread-1"}}}
	scm.resolveErr = errors.New("provider unavailable")
	store := &fakeActionStore{pr: pr, ok: true}
	writer := &fakeActionWriter{}
	svc := NewActionService(ActionDeps{Store: store, Reader: scm, Resolver: scm, Writer: writer})

	if _, err := svc.ResolveComments(context.Background(), "42", nil); err == nil {
		t.Fatal("expected provider error")
	}
	if writer.writeCalls != 0 {
		t.Fatalf("writeCalls=%d, want 0", writer.writeCalls)
	}
}

func TestActionServiceResolveComments_NothingToResolve(t *testing.T) {
	pr, scm := mergeableActionFixture()
	store := &fakeActionStore{pr: pr, ok: true}
	writer := &fakeActionWriter{}
	svc := NewActionService(ActionDeps{Store: store, Reader: scm, Resolver: scm, Writer: writer})

	if _, err := svc.ResolveComments(context.Background(), "42", nil); !errors.Is(err, ErrNothingToResolve) {
		t.Fatalf("error=%v, want ErrNothingToResolve", err)
	}
	if writer.writeCalls != 0 || scm.resolveCalls != 0 {
		t.Fatalf("writes=%d resolves=%d, want 0", writer.writeCalls, scm.resolveCalls)
	}
}
