package pr

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeActionStore struct {
	pr          domain.PullRequest
	ok          bool
	checks      []domain.PullRequestCheck
	comments    []domain.PullRequestComment
	threads     []domain.PullRequestReviewThread
	reviews     []domain.PullRequestReview
	activeCount int
}

func (f *fakeActionStore) GetPR(context.Context, string) (domain.PullRequest, bool, error) {
	return f.pr, f.ok, nil
}

func (f *fakeActionStore) GetPRByNumber(_ context.Context, number int) (domain.PullRequest, bool, error) {
	return f.pr, f.ok && f.pr.Number == number, nil
}

func (f *fakeActionStore) CountActivePRsByNumber(context.Context, int) (int, error) {
	if f.activeCount > 0 {
		return f.activeCount, nil
	}
	if f.ok {
		return 1, nil
	}
	return 0, nil
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
	writeCalls       int
	threadWriteCalls int
	resolvedThreads  []string
	threads          []domain.PullRequestReviewThread
	comments         []domain.PullRequestComment
	writeErr         error
	threadWriteErr   error
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

func (f *fakeActionWriter) MarkPRReviewThreadResolved(_ context.Context, _ string, threadID string) error {
	f.threadWriteCalls++
	if f.threadWriteErr != nil {
		return f.threadWriteErr
	}
	f.resolvedThreads = append(f.resolvedThreads, threadID)
	return nil
}

type fakeSCMAction struct {
	observation     ports.SCMObservation
	review          ports.SCMReviewObservation
	mergeErr        error
	request         ports.SCMMergeRequest
	mergeCalls      int
	resolveErr      error
	resolveErrs     []error
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
	err := f.resolveErr
	if len(f.resolveErrs) >= f.resolveCalls+1 {
		err = f.resolveErrs[f.resolveCalls]
	}
	f.resolveCalls++
	f.resolveRequests = append(f.resolveRequests, request)
	return err
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
	if result.Resolved != 2 || scm.resolveCalls != 2 || writer.threadWriteCalls != 2 {
		t.Fatalf("result=%+v resolveCalls=%d threadWriteCalls=%d", result, scm.resolveCalls, writer.threadWriteCalls)
	}
	for _, request := range scm.resolveRequests {
		if request.PR.Number != 42 || request.PR.Repo.Owner != "acme" || request.PR.Repo.Name != "widgets" {
			t.Fatalf("resolve request = %+v", request)
		}
	}
	if got := strings.Join(writer.resolvedThreads, ","); got != "thread-1,thread-3" {
		t.Fatalf("resolved thread ids = %q", got)
	}
}

func TestActionServiceResolveComments_ExplicitIDsAreDeduplicated(t *testing.T) {
	pr, scm := mergeableActionFixture()
	scm.review = ports.SCMReviewObservation{Threads: []ports.SCMReviewThreadObservation{
		{ID: "thread-1", Comments: []ports.SCMReviewCommentObservation{{ID: "comment-1"}}},
		{ID: "thread-2", Comments: []ports.SCMReviewCommentObservation{{ID: "comment-2"}}},
	}}
	store := &fakeActionStore{pr: pr, ok: true}
	writer := &fakeActionWriter{}
	svc := NewActionService(ActionDeps{Store: store, Reader: scm, Resolver: scm, Writer: writer})

	result, err := svc.ResolveComments(context.Background(), "42", []string{"thread-1", "", "thread-1", "thread-2"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Resolved != 2 || scm.resolveCalls != 2 || writer.threadWriteCalls != 2 {
		t.Fatalf("result=%+v resolveCalls=%d", result, scm.resolveCalls)
	}
}

func TestActionServiceResolveComments_MapsCommentIDsAndRejectsUnknownIDs(t *testing.T) {
	pr, scm := mergeableActionFixture()
	scm.review = ports.SCMReviewObservation{Threads: []ports.SCMReviewThreadObservation{
		{ID: "thread-1", Comments: []ports.SCMReviewCommentObservation{{ID: "comment-1"}}},
	}}
	store := &fakeActionStore{pr: pr, ok: true}
	writer := &fakeActionWriter{}
	svc := NewActionService(ActionDeps{Store: store, Reader: scm, Resolver: scm, Writer: writer})

	result, err := svc.ResolveComments(context.Background(), "42", []string{"comment-1"})
	if err != nil || result.Resolved != 1 || len(scm.resolveRequests) != 1 || scm.resolveRequests[0].ThreadID != "thread-1" {
		t.Fatalf("comment id result=%+v err=%v requests=%+v", result, err, scm.resolveRequests)
	}

	scm.resolveRequests = nil
	_, err = svc.ResolveComments(context.Background(), "42", []string{"foreign-comment"})
	if !errors.Is(err, ErrPRPreconditions) || scm.resolveCalls != 1 {
		t.Fatalf("unknown id err=%v resolveCalls=%d, want precondition without provider mutation", err, scm.resolveCalls)
	}
}

func TestActionServiceResolveComments_RejectsPartialProviderListing(t *testing.T) {
	pr, scm := mergeableActionFixture()
	scm.review = ports.SCMReviewObservation{Partial: true, Threads: []ports.SCMReviewThreadObservation{{ID: "thread-1"}}}
	store := &fakeActionStore{pr: pr, ok: true}
	writer := &fakeActionWriter{}
	svc := NewActionService(ActionDeps{Store: store, Reader: scm, Resolver: scm, Writer: writer})

	if _, err := svc.ResolveComments(context.Background(), "42", nil); !errors.Is(err, ErrPRPreconditions) {
		t.Fatalf("error = %v, want incomplete observation precondition", err)
	}
	if scm.resolveCalls != 0 || writer.threadWriteCalls != 0 {
		t.Fatalf("remote resolves=%d local writes=%d, want 0", scm.resolveCalls, writer.threadWriteCalls)
	}
}

func TestActionServiceResolveComments_AllowsKnownExplicitIDFromPartialListing(t *testing.T) {
	pr, scm := mergeableActionFixture()
	scm.review = ports.SCMReviewObservation{Partial: true, Threads: []ports.SCMReviewThreadObservation{{ID: "thread-1"}}}
	store := &fakeActionStore{pr: pr, ok: true}
	writer := &fakeActionWriter{}
	svc := NewActionService(ActionDeps{Store: store, Reader: scm, Resolver: scm, Writer: writer})

	result, err := svc.ResolveComments(context.Background(), "42", []string{"thread-1"})
	if err != nil || result.Resolved != 1 || scm.resolveCalls != 1 || writer.threadWriteCalls != 1 {
		t.Fatalf("result=%+v err=%v resolves=%d writes=%d", result, err, scm.resolveCalls, writer.threadWriteCalls)
	}

	_, err = svc.ResolveComments(context.Background(), "42", []string{"unknown-thread"})
	if !errors.Is(err, ErrPRPreconditions) || scm.resolveCalls != 1 {
		t.Fatalf("unknown id err=%v resolves=%d, want precondition without mutation", err, scm.resolveCalls)
	}
}

func TestActionServiceResolveComments_RejectsAmbiguousActivePRNumber(t *testing.T) {
	pr, scm := mergeableActionFixture()
	store := &fakeActionStore{pr: pr, ok: true, activeCount: 2}
	writer := &fakeActionWriter{}
	svc := NewActionService(ActionDeps{Store: store, Reader: scm, Resolver: scm, Writer: writer})

	if _, err := svc.ResolveComments(context.Background(), "42", nil); !errors.Is(err, ErrPRPreconditions) {
		t.Fatalf("error = %v, want ambiguous PR precondition", err)
	}
	if scm.resolveCalls != 0 || writer.threadWriteCalls != 0 {
		t.Fatalf("remote resolves=%d local writes=%d, want 0", scm.resolveCalls, writer.threadWriteCalls)
	}
}

func TestActionServiceResolveComments_PersistsSuccessfulSubsetBeforeProviderFailure(t *testing.T) {
	pr, scm := mergeableActionFixture()
	scm.review = ports.SCMReviewObservation{Threads: []ports.SCMReviewThreadObservation{{ID: "thread-1"}, {ID: "thread-2"}}}
	scm.resolveErrs = []error{nil, errors.New("provider unavailable")}
	store := &fakeActionStore{pr: pr, ok: true}
	writer := &fakeActionWriter{}
	svc := NewActionService(ActionDeps{Store: store, Reader: scm, Resolver: scm, Writer: writer})

	result, err := svc.ResolveComments(context.Background(), "42", nil)
	if err == nil || result.Resolved != 1 || writer.threadWriteCalls != 1 || len(writer.resolvedThreads) != 1 || writer.resolvedThreads[0] != "thread-1" {
		t.Fatalf("result=%+v err=%v writes=%d ids=%v", result, err, writer.threadWriteCalls, writer.resolvedThreads)
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
	if writer.threadWriteCalls != 0 {
		t.Fatalf("threadWriteCalls=%d, want 0", writer.threadWriteCalls)
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
