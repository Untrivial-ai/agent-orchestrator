package pr

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var (
	prNumberPattern = regexp.MustCompile(`^[1-9]\d*$`)
	gitSHAPattern   = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)
)

type actionStore interface {
	GetPR(ctx context.Context, url string) (domain.PullRequest, bool, error)
}

type resolveStore interface {
	actionStore
	GetPRByNumber(ctx context.Context, number int) (domain.PullRequest, bool, error)
	ListChecks(ctx context.Context, prURL string) ([]domain.PullRequestCheck, error)
	ListPRComments(ctx context.Context, prURL string) ([]domain.PullRequestComment, error)
	ListPRReviewThreads(ctx context.Context, prURL string) ([]domain.PullRequestReviewThread, error)
	ListPRReviews(ctx context.Context, prURL string) ([]domain.PullRequestReview, error)
}

type actionReader interface {
	FetchPullRequests(ctx context.Context, refs []ports.SCMPRRef) ([]ports.SCMObservation, error)
	FetchReviewThreads(ctx context.Context, ref ports.SCMPRRef) (ports.SCMReviewObservation, error)
}

// ActionDeps contains the storage and SCM boundaries used by ActionService.
type ActionDeps struct {
	Store        actionStore
	ResolveStore resolveStore
	Merger       ports.SCMMerger
	Reader       actionReader
	Resolver     ports.SCMReviewResolver
	Writer       ports.SCMWriter
}

// ActionService validates current pull request state before applying mutations.
type ActionService struct {
	store    actionStore
	resolve  resolveStore
	merger   ports.SCMMerger
	reader   actionReader
	resolver ports.SCMReviewResolver
	writer   ports.SCMWriter
}

var _ ActionManager = (*ActionService)(nil)

// NewActionService builds the guarded pull request action service.
func NewActionService(deps ActionDeps) *ActionService {
	resolve := deps.ResolveStore
	if resolve == nil {
		resolve, _ = deps.Store.(resolveStore)
	}
	return &ActionService{
		store:    deps.Store,
		resolve:  resolve,
		merger:   deps.Merger,
		reader:   deps.Reader,
		resolver: deps.Resolver,
		writer:   deps.Writer,
	}
}

// Merge re-fetches authoritative SCM state and then squash-merges only the
// exact head the user saw. The provider repeats the SHA guard atomically.
func (s *ActionService) Merge(ctx context.Context, request MergeRequest) (MergeResult, error) {
	prNumber, err := parsePRNumber(request.PRID)
	if err != nil || strings.TrimSpace(request.PRURL) == "" {
		return MergeResult{}, fmt.Errorf("%w: invalid pull request identity", ErrInvalidPR)
	}
	if s.store == nil || s.merger == nil || s.reader == nil {
		return MergeResult{}, errors.New("pr: merge action is not configured")
	}
	expectedHead := strings.ToLower(strings.TrimSpace(request.ExpectedHeadSHA))
	if !gitSHAPattern.MatchString(expectedHead) {
		return MergeResult{}, fmt.Errorf("%w: invalid expected head", ErrInvalidPR)
	}

	tracked, ok, err := s.store.GetPR(ctx, request.PRURL)
	if err != nil {
		return MergeResult{}, fmt.Errorf("load pull request: %w", err)
	}
	if !ok || tracked.Number != prNumber {
		return MergeResult{}, ErrPRNotFound
	}
	if tracked.Draft || tracked.Merged || tracked.Closed {
		return MergeResult{}, ErrPRNotMergeable
	}
	if !gitSHAPattern.MatchString(strings.TrimSpace(tracked.HeadSHA)) {
		return MergeResult{}, fmt.Errorf("%w: pull request head is unknown", ErrPRPreconditions)
	}
	if !strings.EqualFold(expectedHead, tracked.HeadSHA) {
		return MergeResult{}, ErrPRHeadChanged
	}

	repo, ok := scmRepoForPR(tracked)
	if !ok {
		return MergeResult{}, fmt.Errorf("%w: pull request repository is unknown", ErrPRPreconditions)
	}
	ref := ports.SCMPRRef{Repo: repo, Number: tracked.Number, URL: tracked.URL}
	fresh, review, err := s.fetchMergeReadiness(ctx, ref)
	if err != nil {
		return MergeResult{}, err
	}
	if !strings.EqualFold(fresh.PR.HeadSHA, expectedHead) {
		return MergeResult{}, ErrPRHeadChanged
	}
	if !readyToMerge(fresh, review) {
		return MergeResult{}, ErrPRPreconditions
	}

	out, err := s.merger.MergePullRequest(ctx, ports.SCMMergeRequest{PR: ref, ExpectedHeadSHA: expectedHead, Method: ports.SCMMergeSquash})
	if err != nil {
		switch {
		case errors.Is(err, ports.ErrSCMNotFound):
			return MergeResult{}, fmt.Errorf("%w: %w", ErrPRNotFound, err)
		case errors.Is(err, ports.ErrSCMHeadChanged):
			return MergeResult{}, fmt.Errorf("%w: %w", ErrPRHeadChanged, err)
		case errors.Is(err, ports.ErrSCMNotMergeable):
			return MergeResult{}, fmt.Errorf("%w: %w", ErrPRNotMergeable, err)
		default:
			return MergeResult{}, fmt.Errorf("merge pull request: %w", err)
		}
	}
	return MergeResult{PRNumber: tracked.Number, Method: string(ports.SCMMergeSquash), MergeCommitSHA: out.MergeCommitSHA}, nil
}

func (s *ActionService) fetchMergeReadiness(ctx context.Context, ref ports.SCMPRRef) (ports.SCMObservation, ports.SCMReviewObservation, error) {
	observations, err := s.reader.FetchPullRequests(ctx, []ports.SCMPRRef{ref})
	if err != nil {
		if errors.Is(err, ports.ErrSCMNotFound) {
			return ports.SCMObservation{}, ports.SCMReviewObservation{}, fmt.Errorf("%w: %w", ErrPRNotFound, err)
		}
		return ports.SCMObservation{}, ports.SCMReviewObservation{}, fmt.Errorf("refresh pull request before merge: %w", err)
	}
	if len(observations) != 1 || !observations[0].Fetched || observations[0].PR.Number != ref.Number {
		return ports.SCMObservation{}, ports.SCMReviewObservation{}, ErrPRNotFound
	}
	review, err := s.reader.FetchReviewThreads(ctx, ref)
	if err != nil {
		if errors.Is(err, ports.ErrSCMNotFound) {
			return ports.SCMObservation{}, ports.SCMReviewObservation{}, fmt.Errorf("%w: %w", ErrPRNotFound, err)
		}
		return ports.SCMObservation{}, ports.SCMReviewObservation{}, fmt.Errorf("refresh pull request reviews before merge: %w", err)
	}
	return observations[0], review, nil
}

func readyToMerge(o ports.SCMObservation, review ports.SCMReviewObservation) bool {
	if o.PR.HeadSHA == "" || o.CI.HeadSHA != o.PR.HeadSHA || review.Partial {
		return false
	}
	return domain.MergeReadiness{
		Draft:              o.PR.Draft,
		Merged:             o.PR.Merged,
		Closed:             o.PR.Closed,
		CI:                 domain.CIState(o.CI.Summary),
		Review:             domain.ReviewDecision(review.Decision),
		Mergeability:       domain.Mergeability(o.Mergeability.State),
		UnresolvedComments: hasUnresolvedHumanComments(review.Threads),
	}.ReadyToMerge()
}

func hasUnresolvedHumanComments(threads []ports.SCMReviewThreadObservation) bool {
	for _, thread := range threads {
		if thread.Resolved {
			continue
		}
		for _, comment := range thread.Comments {
			if !comment.IsBot {
				return true
			}
		}
	}
	return false
}

func parsePRNumber(value string) (int, error) {
	if !prNumberPattern.MatchString(value) {
		return 0, ErrInvalidPR
	}
	n, err := strconv.ParseInt(value, 10, 32)
	if err != nil || n <= 0 {
		return 0, ErrInvalidPR
	}
	return int(n), nil
}

func scmRepoForPR(pr domain.PullRequest) (ports.SCMRepo, bool) {
	parts := strings.Split(pr.Repo, "/")
	if len(parts) < 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[len(parts)-1]) == "" {
		return ports.SCMRepo{}, false
	}
	provider := strings.ToLower(strings.TrimSpace(pr.Provider))
	if provider == "" {
		provider = "github"
	}
	host := strings.ToLower(strings.TrimSpace(pr.Host))
	if host == "" && provider == "github" {
		host = "github.com"
	}
	return ports.SCMRepo{
		Provider: provider,
		Host:     host,
		Owner:    strings.Join(parts[:len(parts)-1], "/"),
		Name:     parts[len(parts)-1],
		Repo:     pr.Repo,
	}, true
}

// ResolveComments resolves provider review threads first and then updates the
// local observation. Empty commentIDs resolves every unresolved thread returned
// by the provider; supplied IDs are deduplicated and passed through as thread
// IDs. A failed provider call never causes a local write for that thread.
func (s *ActionService) ResolveComments(ctx context.Context, prID string, commentIDs []string) (ResolveResult, error) {
	if s.resolve == nil || s.reader == nil || s.resolver == nil || s.writer == nil {
		return ResolveResult{}, errors.New("pr: resolve-comments action is not configured")
	}
	pr, err := s.lookupResolvePR(ctx, prID)
	if err != nil {
		return ResolveResult{}, err
	}

	repo, ok := scmRepoForPR(pr)
	if !ok {
		return ResolveResult{}, fmt.Errorf("%w: pull request repository is unknown", ErrPRPreconditions)
	}
	ref := ports.SCMPRRef{Repo: repo, Number: pr.Number, URL: pr.URL}

	// Refresh the provider view before mutating anything. This both supplies the
	// resolve-all set and confirms that the tracked number still exists remotely.
	review, err := s.reader.FetchReviewThreads(ctx, ref)
	if err != nil {
		if errors.Is(err, ports.ErrSCMNotFound) {
			return ResolveResult{}, fmt.Errorf("%w: %w", ErrPRNotFound, err)
		}
		return ResolveResult{}, fmt.Errorf("refresh pull request reviews before resolving comments: %w", err)
	}
	if len(commentIDs) == 0 && review.Partial {
		return ResolveResult{}, fmt.Errorf("%w: review thread listing is incomplete", ErrPRPreconditions)
	}

	threadIDs := normalizeThreadIDs(commentIDs)
	if len(commentIDs) == 0 {
		threadIDs = make([]string, 0, len(review.Threads))
		for _, thread := range review.Threads {
			if thread.Resolved {
				continue
			}
			if id := strings.TrimSpace(thread.ID); id != "" {
				threadIDs = append(threadIDs, id)
			}
		}
		threadIDs = normalizeThreadIDs(threadIDs)
	}
	if len(threadIDs) == 0 {
		return ResolveResult{}, ErrNothingToResolve
	}

	checks, err := s.resolve.ListChecks(ctx, pr.URL)
	if err != nil {
		return ResolveResult{}, fmt.Errorf("list checks before resolving comments: %w", err)
	}
	reviews, err := s.resolve.ListPRReviews(ctx, pr.URL)
	if err != nil {
		return ResolveResult{}, fmt.Errorf("list reviews before resolving comments: %w", err)
	}
	threads, err := s.resolve.ListPRReviewThreads(ctx, pr.URL)
	if err != nil {
		return ResolveResult{}, fmt.Errorf("list review threads before resolving comments: %w", err)
	}
	comments, err := s.resolve.ListPRComments(ctx, pr.URL)
	if err != nil {
		return ResolveResult{}, fmt.Errorf("list comments before resolving comments: %w", err)
	}

	resolvedIDs := make([]string, 0, len(threadIDs))
	for _, id := range threadIDs {
		if err := s.resolver.ResolveReviewThread(ctx, ports.SCMReviewResolveRequest{PR: ref, ThreadID: id}); err != nil {
			mapped := mapResolveError(err)
			if len(resolvedIDs) == 0 {
				return ResolveResult{}, mapped
			}
			markResolvedIDs(resolvedIDs, threads, comments)
			if persistErr := s.writer.WriteSCMObservation(ctx, pr, checks, reviews, threads, comments, ports.ReviewWriteMerge); persistErr != nil {
				return ResolveResult{}, fmt.Errorf("persist partial resolved review threads: %w; remote resolve: %w", persistErr, mapped)
			}
			return ResolveResult{Resolved: len(resolvedIDs)}, mapped
		}
		resolvedIDs = append(resolvedIDs, id)
	}
	markResolvedIDs(resolvedIDs, threads, comments)
	if err := s.writer.WriteSCMObservation(ctx, pr, checks, reviews, threads, comments, ports.ReviewWriteMerge); err != nil {
		return ResolveResult{}, fmt.Errorf("persist resolved review threads: %w", err)
	}
	return ResolveResult{Resolved: len(resolvedIDs)}, nil
}

func (s *ActionService) lookupResolvePR(ctx context.Context, prID string) (domain.PullRequest, error) {
	number, err := parsePRNumber(strings.TrimSpace(prID))
	if err != nil {
		return domain.PullRequest{}, fmt.Errorf("%w: invalid pull request identity", ErrInvalidPR)
	}
	pr, ok, err := s.resolve.GetPRByNumber(ctx, number)
	if err != nil {
		return domain.PullRequest{}, fmt.Errorf("load pull request: %w", err)
	}
	if !ok {
		return domain.PullRequest{}, ErrPRNotFound
	}
	return pr, nil
}

func normalizeThreadIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func markResolvedIDs(threadIDs []string, threads []domain.PullRequestReviewThread, comments []domain.PullRequestComment) {
	resolved := make(map[string]struct{}, len(threadIDs))
	for _, threadID := range threadIDs {
		resolved[threadID] = struct{}{}
	}
	for i := range threads {
		if _, ok := resolved[threads[i].ThreadID]; ok {
			threads[i].Resolved = true
		}
	}
	for i := range comments {
		if _, ok := resolved[comments[i].ThreadID]; ok {
			comments[i].Resolved = true
			continue
		}
		if _, ok := resolved[comments[i].ID]; ok {
			comments[i].Resolved = true
		}
	}
}

func mapResolveError(err error) error {
	if errors.Is(err, ports.ErrSCMNotFound) {
		return fmt.Errorf("%w: %w", ErrPRNotFound, err)
	}
	return fmt.Errorf("resolve review thread: %w", err)
}
