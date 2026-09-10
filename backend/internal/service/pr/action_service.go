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

type actionReader interface {
	FetchPullRequests(ctx context.Context, refs []ports.SCMPRRef) ([]ports.SCMObservation, error)
	FetchReviewThreads(ctx context.Context, ref ports.SCMPRRef) (ports.SCMReviewObservation, error)
}

// ActionDeps contains the storage and SCM boundaries used by ActionService.
type ActionDeps struct {
	Store  actionStore
	Merger ports.SCMMerger
	Reader actionReader
}

// ActionService validates current pull request state before applying mutations.
type ActionService struct {
	store  actionStore
	merger ports.SCMMerger
	reader actionReader
}

var _ ActionManager = (*ActionService)(nil)

// NewActionService builds the guarded pull request action service.
func NewActionService(deps ActionDeps) *ActionService {
	return &ActionService{store: deps.Store, merger: deps.Merger, reader: deps.Reader}
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

// ResolveComments is not implemented by the current provider action service.
func (s *ActionService) ResolveComments(_ context.Context, _ string, _ []string) (ResolveResult, error) {
	return ResolveResult{Resolved: 0}, nil
}
