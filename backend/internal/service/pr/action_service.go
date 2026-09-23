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

type resolveNumberCounter interface {
	CountActivePRsByNumber(context.Context, int) (int, error)
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
	// ThreadWriter updates one resolved thread and its comments without
	// replacing a potentially newer SCM observation.
	ThreadWriter interface {
		MarkPRReviewThreadResolved(context.Context, string, string) error
	}
}

// ActionService validates current pull request state before applying mutations.
type ActionService struct {
	store        actionStore
	resolve      resolveStore
	merger       ports.SCMMerger
	reader       actionReader
	resolver     ports.SCMReviewResolver
	threadWriter interface {
		MarkPRReviewThreadResolved(context.Context, string, string) error
	}
}

var _ ActionManager = (*ActionService)(nil)

// NewActionService builds the guarded pull request action service.
func NewActionService(deps ActionDeps) *ActionService {
	resolve := deps.ResolveStore
	if resolve == nil {
		resolve, _ = deps.Store.(resolveStore)
	}
	threadWriter := deps.ThreadWriter
	if threadWriter == nil {
		threadWriter, _ = deps.Writer.(interface {
			MarkPRReviewThreadResolved(context.Context, string, string) error
		})
	}
	return &ActionService{
		store:        deps.Store,
		resolve:      resolve,
		merger:       deps.Merger,
		reader:       deps.Reader,
		resolver:     deps.Resolver,
		threadWriter: threadWriter,
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

// ResolveComments resolves provider review threads first and then marks each
// successful mutation locally. Empty commentIDs resolves every unresolved
// thread returned by the provider. Supplied IDs may be either provider thread
// IDs or comment IDs; comment IDs are mapped to their owning thread before the
// mutation. Resolve-all requires a complete provider listing; an explicit ID
// may still be used when it is present in a partial listing, while unknown IDs
// are rejected because the response cannot prove their membership.
func (s *ActionService) ResolveComments(ctx context.Context, prID string, commentIDs []string) (ResolveResult, error) {
	if s.resolve == nil || s.reader == nil || s.resolver == nil || s.threadWriter == nil {
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
	if review.Partial && len(commentIDs) == 0 {
		return ResolveResult{}, fmt.Errorf("%w: review thread listing is incomplete; provide an explicit thread or comment id", ErrPRPreconditions)
	}

	threadIDs, err := requestedThreadIDs(review, commentIDs)
	if err != nil {
		return ResolveResult{}, err
	}
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

	resolved := 0
	for _, id := range threadIDs {
		if err := s.resolver.ResolveReviewThread(ctx, ports.SCMReviewResolveRequest{PR: ref, ThreadID: id}); err != nil {
			mapped := mapResolveError(err)
			return ResolveResult{Resolved: resolved}, mapped
		}
		if err := s.threadWriter.MarkPRReviewThreadResolved(ctx, pr.URL, id); err != nil {
			return ResolveResult{Resolved: resolved}, fmt.Errorf("persist resolved review thread %q: %w", id, err)
		}
		resolved++
	}
	return ResolveResult{Resolved: resolved}, nil
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
	if counter, ok := s.resolve.(resolveNumberCounter); ok {
		active, err := counter.CountActivePRsByNumber(ctx, number)
		if err != nil {
			return domain.PullRequest{}, fmt.Errorf("check pull request identity: %w", err)
		}
		if active > 1 {
			return domain.PullRequest{}, fmt.Errorf("%w: pull request number %d is tracked in multiple active repositories", ErrPRPreconditions, number)
		}
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

// requestedThreadIDs validates explicit IDs against the provider observation
// and maps documented comment IDs to their owning thread IDs.
func requestedThreadIDs(review ports.SCMReviewObservation, commentIDs []string) ([]string, error) {
	if len(commentIDs) == 0 {
		return nil, nil
	}
	threadIDs := make(map[string]struct{}, len(review.Threads))
	commentToThread := make(map[string]string)
	for _, thread := range review.Threads {
		threadID := strings.TrimSpace(thread.ID)
		if threadID == "" {
			continue
		}
		threadIDs[threadID] = struct{}{}
		for _, comment := range thread.Comments {
			if commentID := strings.TrimSpace(comment.ID); commentID != "" {
				commentToThread[commentID] = threadID
			}
		}
	}

	requested := normalizeThreadIDs(commentIDs)
	out := make([]string, 0, len(requested))
	for _, id := range requested {
		if _, ok := threadIDs[id]; ok {
			out = append(out, id)
			continue
		}
		if threadID, ok := commentToThread[id]; ok {
			out = append(out, threadID)
			continue
		}
		return nil, fmt.Errorf("%w: review thread or comment %q is not part of pull request", ErrPRPreconditions, id)
	}
	return normalizeThreadIDs(out), nil
}

func mapResolveError(err error) error {
	if errors.Is(err, ports.ErrSCMNotFound) {
		return fmt.Errorf("%w: %w", ErrPRNotFound, err)
	}
	return fmt.Errorf("resolve review thread: %w", err)
}
