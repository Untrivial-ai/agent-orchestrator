package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

const pullRequestSnapshotQuery = `query($owner:String!,$repo:String!,$number:Int!){
 repository(owner:$owner,name:$repo){ pullRequest(number:$number){
  number id url state isDraft merged closed title additions deletions changedFiles
  mergeable mergeStateStatus reviewDecision headRefName headRefOid baseRefName baseRefOid
  createdAt updatedAt mergedAt closedAt author{login avatarUrl} mergeCommit{oid}
  commits(last:1){nodes{commit{statusCheckRollup{state contexts(first:100){nodes{
   __typename ... on CheckRun{name status conclusion detailsUrl databaseId}
   ... on StatusContext{context state targetUrl}
  } pageInfo{hasNextPage}}}}}}
  reviews(last:100,states:[APPROVED,CHANGES_REQUESTED,COMMENTED,DISMISSED]){nodes{
   id databaseId state url body submittedAt commit{oid} author{login __typename}
  } pageInfo{hasNextPage}}
  reviewThreads(last:100){nodes{id isResolved isOutdated path line comments(first:100){nodes{
   id databaseId body url author{login __typename} pullRequestReview{databaseId}
  }}} pageInfo{hasNextPage}}
 }}
}`

type githubActor struct {
	Login string `json:"login"`
	Type  string `json:"__typename"`
}

type githubPageInfo struct {
	HasNextPage bool `json:"hasNextPage"`
}

type githubPullRequestSnapshotResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				Number                                           int `json:"number"`
				ID, URL, State, Title                            string
				IsDraft, Merged, Closed                          bool
				Additions, Deletions, ChangedFiles               int
				Mergeable, MergeStateStatus, ReviewDecision      string
				HeadRefName, HeadRefOid, BaseRefName, BaseRefOid string
				CreatedAt, UpdatedAt, MergedAt, ClosedAt         *time.Time
				Author                                           struct{ Login, AvatarURL string } `json:"author"`
				MergeCommit                                      struct {
					OID string `json:"oid"`
				} `json:"mergeCommit"`
				Commits struct {
					Nodes []struct {
						Commit struct {
							StatusCheckRollup *struct {
								State    string
								Contexts struct {
									Nodes []struct {
										Type                                 string `json:"__typename"`
										Name, Status, Conclusion, DetailsURL string
										DatabaseID                           int64
										Context, State, TargetURL            string
									} `json:"nodes"`
									PageInfo githubPageInfo `json:"pageInfo"`
								} `json:"contexts"`
							} `json:"statusCheckRollup"`
						} `json:"commit"`
					} `json:"nodes"`
				} `json:"commits"`
				Reviews struct {
					Nodes []struct {
						ID               string
						DatabaseID       int64
						State, URL, Body string
						SubmittedAt      *time.Time
						Commit           struct {
							OID string `json:"oid"`
						}
						Author githubActor
					} `json:"nodes"`
					PageInfo githubPageInfo `json:"pageInfo"`
				} `json:"reviews"`
				ReviewThreads struct {
					Nodes []struct {
						ID                     string
						IsResolved, IsOutdated bool
						Path                   string
						Line                   int
						Comments               struct {
							Nodes []struct {
								ID                string
								DatabaseID        int64
								Body, URL         string
								Author            githubActor
								PullRequestReview struct{ DatabaseID int64 } `json:"pullRequestReview"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
					PageInfo githubPageInfo `json:"pageInfo"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (s *Service) FetchPullRequestSnapshot(ctx context.Context, ref domain.PullRequestRef) (domain.PullRequestSnapshot, error) {
	owner, repo, ok := strings.Cut(ref.Repository, "/")
	if !ok || owner == "" || repo == "" || ref.Number <= 0 {
		return domain.PullRequestSnapshot{}, postgres.ErrInvalid
	}
	installationID, repositoryID, err := s.store.GitHubInstallationForRepository(ctx, ref.OrgID, ref.Repository)
	if err != nil {
		return domain.PullRequestSnapshot{}, err
	}
	access, err := s.client.statusReadToken(ctx, installationID, repositoryID)
	if err != nil {
		return domain.PullRequestSnapshot{}, err
	}
	var response githubPullRequestSnapshotResponse
	if err := s.client.graphQL(ctx, access.Token, pullRequestSnapshotQuery,
		map[string]any{"owner": owner, "repo": repo, "number": ref.Number}, &response); err != nil {
		return domain.PullRequestSnapshot{}, err
	}
	if len(response.Errors) > 0 {
		return domain.PullRequestSnapshot{}, errors.New("GitHub GraphQL pull request snapshot failed")
	}
	return normalizePullRequestSnapshot(response)
}

func normalizePullRequestSnapshot(response githubPullRequestSnapshotResponse) (domain.PullRequestSnapshot, error) {
	pr := response.Data.Repository.PullRequest
	if pr.Number <= 0 || strings.TrimSpace(pr.URL) == "" {
		return domain.PullRequestSnapshot{}, errors.New("GitHub returned an incomplete pull request snapshot")
	}
	state := contract.PRStateOpen
	switch {
	case pr.Merged:
		state = contract.PRStateMerged
	case pr.Closed:
		state = contract.PRStateClosed
	case pr.IsDraft:
		state = contract.PRStateDraft
	}
	snapshot := domain.PullRequestSnapshot{
		Author: pr.Author.Login, AuthorAvatarURL: pr.Author.AvatarURL, Title: pr.Title, URL: pr.URL,
		SourceBranch: pr.HeadRefName, TargetBranch: pr.BaseRefName, BaseSHA: pr.BaseRefOid,
		MergeCommitSHA: pr.MergeCommit.OID, CreatedAtProvider: pr.CreatedAt, UpdatedAtProvider: pr.UpdatedAt,
		MergedAtProvider: pr.MergedAt, ClosedAtProvider: pr.ClosedAt,
		ReviewsPartial: pr.Reviews.PageInfo.HasNextPage || pr.ReviewThreads.PageInfo.HasNextPage,
	}
	snapshot.Observation = domain.PullRequestObservation{
		State: state, Draft: pr.IsDraft, HeadSHA: pr.HeadRefOid, Additions: pr.Additions,
		Deletions: pr.Deletions, ChangedFiles: pr.ChangedFiles,
		ReviewState:  mapSnapshotReviewDecision(pr.ReviewDecision),
		Mergeability: mapSnapshotMergeability(pr.Mergeable, pr.MergeStateStatus),
	}
	if len(pr.Commits.Nodes) > 0 && pr.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
		rollup := pr.Commits.Nodes[0].Commit.StatusCheckRollup
		for _, node := range rollup.Contexts.Nodes {
			check := domain.PullRequestCheck{HeadSHA: pr.HeadRefOid}
			switch node.Type {
			case "CheckRun":
				check.ProviderID = strconv.FormatInt(node.DatabaseID, 10)
				check.Name = node.Name
				check.Status = strings.ToLower(node.Status)
				check.Conclusion = strings.ToLower(node.Conclusion)
				check.URL = node.DetailsURL
			case "StatusContext":
				check.Name = node.Context
				check.Status = "completed"
				check.Conclusion = strings.ToLower(node.State)
				check.URL = node.TargetURL
			default:
				continue
			}
			snapshot.Checks = append(snapshot.Checks, check)
		}
		snapshot.Observation.CIState = mapRollupCIState(rollup.State, snapshot.Checks)
	} else {
		snapshot.Observation.CIState = contract.CIPassing
	}
	checks := snapshot.Checks
	if checks == nil {
		checks = []domain.PullRequestCheck{}
	}
	checkJSON, err := json.Marshal(checks)
	if err != nil {
		return domain.PullRequestSnapshot{}, err
	}
	snapshot.Observation.Checks = checkJSON
	for _, review := range pr.Reviews.Nodes {
		snapshot.Reviews = append(snapshot.Reviews, domain.PullRequestReview{
			ProviderID: review.ID, DatabaseID: review.DatabaseID, Author: review.Author.Login,
			State: mapSnapshotReviewDecision(review.State), Body: review.Body, URL: review.URL,
			TargetSHA: review.Commit.OID, IsBot: actorIsBot(review.Author), SubmittedAt: review.SubmittedAt,
		})
	}
	for _, thread := range pr.ReviewThreads.Nodes {
		allBot := len(thread.Comments.Nodes) > 0
		for _, comment := range thread.Comments.Nodes {
			if !actorIsBot(comment.Author) {
				allBot = false
			}
		}
		snapshot.Threads = append(snapshot.Threads, domain.PullRequestReviewThread{
			ProviderID: thread.ID, Path: thread.Path, Line: thread.Line, Resolved: thread.IsResolved,
			Outdated: thread.IsOutdated, IsBot: allBot,
		})
		for _, comment := range thread.Comments.Nodes {
			snapshot.Comments = append(snapshot.Comments, domain.PullRequestReviewComment{
				ProviderID: comment.ID, DatabaseID: comment.DatabaseID, ThreadProviderID: thread.ID,
				ReviewProviderID: strconv.FormatInt(comment.PullRequestReview.DatabaseID, 10),
				Author:           comment.Author.Login, Body: comment.Body, URL: comment.URL, Path: thread.Path,
				Line: thread.Line, Resolved: thread.IsResolved, Outdated: thread.IsOutdated, IsBot: actorIsBot(comment.Author),
			})
		}
	}
	return snapshot, nil
}

func actorIsBot(actor githubActor) bool {
	return actor.Type == "Bot" || strings.HasSuffix(strings.ToLower(actor.Login), "[bot]")
}

func mapSnapshotReviewDecision(value string) contract.ReviewDecision {
	switch strings.ToUpper(value) {
	case "APPROVED":
		return contract.ReviewApproved
	case "CHANGES_REQUESTED":
		return contract.ReviewChangesRequest
	case "REVIEW_REQUIRED":
		return contract.ReviewRequired
	default:
		return contract.ReviewNone
	}
}

func mapSnapshotMergeability(mergeable, state string) contract.Mergeability {
	switch strings.ToUpper(state) {
	case "DIRTY":
		return contract.MergeConflicting
	case "BLOCKED", "BEHIND":
		return contract.MergeBlocked
	case "UNSTABLE":
		return contract.MergeUnstable
	case "CLEAN", "HAS_HOOKS":
		if strings.ToUpper(mergeable) == "MERGEABLE" {
			return contract.MergeMergeable
		}
	}
	return contract.MergeUnknown
}

func mapRollupCIState(state string, checks []domain.PullRequestCheck) contract.CIState {
	for _, check := range checks {
		switch check.Conclusion {
		case "failure", "error", "timed_out", "action_required", "startup_failure", "cancelled":
			return contract.CIFailing
		}
	}
	switch strings.ToUpper(state) {
	case "FAILURE", "ERROR":
		return contract.CIFailing
	case "PENDING", "EXPECTED":
		return contract.CIPending
	case "SUCCESS", "NEUTRAL":
		return contract.CIPassing
	}
	if len(checks) == 0 {
		return contract.CIPassing
	}
	return contract.CIPending
}
