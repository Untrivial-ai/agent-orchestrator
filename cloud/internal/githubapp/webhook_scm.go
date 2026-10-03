package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

// ReconcileWorkerGitRefs covers the race where a custom-branch PR webhook
// arrives before the worker has reported that branch. It only replays verified
// webhook payloads; it never scans GitHub for PRs or invents status updates.
func (s *Service) ReconcileWorkerGitRefs(ctx context.Context, orgID string, repositoryID int64, refs []domain.WorkerGitRef) error {
	if len(refs) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		known[ref.Branch+"\x00"+ref.SHA] = struct{}{}
	}
	deliveries, err := s.store.RecentOpenedPullRequestWebhooks(ctx, repositoryID, time.Now().Add(-7*24*time.Hour), 1000)
	if err != nil {
		return err
	}
	var failures error
	for _, delivery := range deliveries {
		var event struct {
			PullRequest struct {
				Head struct {
					Ref string `json:"ref"`
					SHA string `json:"sha"`
				} `json:"head"`
			} `json:"pull_request"`
		}
		if json.Unmarshal(delivery.Payload, &event) != nil {
			continue
		}
		if _, ok := known[event.PullRequest.Head.Ref+"\x00"+event.PullRequest.Head.SHA]; !ok {
			continue
		}
		if err := s.processSCMWebhook(ctx, orgID, delivery); err != nil {
			failures = errors.Join(failures, err)
		}
	}
	return failures
}

type scmWebhookTargetSet struct {
	PullRequestNumber int
	HeadSHA           string
	RepositoryWide    bool
}

func scmWebhookTargets(event string, payload []byte) (scmWebhookTargetSet, error) {
	var envelope struct {
		PullRequest *struct {
			Number int `json:"number"`
			Head   struct {
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
		CheckRun *struct {
			HeadSHA      string `json:"head_sha"`
			PullRequests []struct {
				Number int `json:"number"`
			} `json:"pull_requests"`
		} `json:"check_run"`
		CheckSuite *struct {
			HeadSHA      string `json:"head_sha"`
			PullRequests []struct {
				Number int `json:"number"`
			} `json:"pull_requests"`
		} `json:"check_suite"`
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return scmWebhookTargetSet{}, postgres.ErrInvalid
	}
	switch event {
	case "pull_request", "pull_request_review", "pull_request_review_comment", "pull_request_review_thread":
		if envelope.PullRequest != nil {
			return scmWebhookTargetSet{
				PullRequestNumber: envelope.PullRequest.Number,
				HeadSHA:           envelope.PullRequest.Head.SHA,
			}, nil
		}
	case "check_run":
		if envelope.CheckRun != nil {
			target := scmWebhookTargetSet{HeadSHA: envelope.CheckRun.HeadSHA}
			if len(envelope.CheckRun.PullRequests) > 0 {
				target.PullRequestNumber = envelope.CheckRun.PullRequests[0].Number
			}
			return target, nil
		}
	case "check_suite":
		if envelope.CheckSuite != nil {
			target := scmWebhookTargetSet{HeadSHA: envelope.CheckSuite.HeadSHA}
			if len(envelope.CheckSuite.PullRequests) > 0 {
				target.PullRequestNumber = envelope.CheckSuite.PullRequests[0].Number
			}
			return target, nil
		}
	case "status":
		return scmWebhookTargetSet{HeadSHA: envelope.SHA}, nil
	case "push":
		return scmWebhookTargetSet{RepositoryWide: true}, nil
	}
	return scmWebhookTargetSet{}, nil
}

func (s *Service) processSCMWebhook(
	ctx context.Context,
	orgID string,
	delivery domain.GitHubWebhookDelivery,
) error {
	target, err := scmWebhookTargets(delivery.Event, delivery.Payload)
	if err != nil {
		return err
	}
	if delivery.GitHubRepositoryID <= 0 {
		return nil
	}
	var pullRequests []domain.PullRequest
	switch {
	case target.PullRequestNumber > 0:
		var pr domain.PullRequest
		pr, err = s.store.PullRequestByGitHubReference(ctx, orgID, delivery.GitHubRepositoryID, target.PullRequestNumber)
		if errors.Is(err, postgres.ErrNotFound) && delivery.Event == "pull_request" &&
			(delivery.Action == "opened" || delivery.Action == "reopened") {
			pr, err = s.claimOpenedWebhookPullRequest(ctx, orgID, delivery)
		}
		if err == nil {
			pullRequests = []domain.PullRequest{pr}
		}
	case target.HeadSHA != "":
		var pr domain.PullRequest
		pr, err = s.store.PullRequestByGitHubHead(ctx, orgID, delivery.GitHubRepositoryID, target.HeadSHA)
		if err == nil {
			pullRequests = []domain.PullRequest{pr}
		}
	case target.RepositoryWide:
		pullRequests, err = s.store.PullRequestsByGitHubRepository(ctx, orgID, delivery.GitHubRepositoryID)
	default:
		return nil
	}
	if errors.Is(err, postgres.ErrNotFound) {
		return nil
	}
	if errors.Is(err, postgres.ErrConflict) && delivery.Event == "pull_request" {
		// More than one session claims this branch. A worker-side claim can
		// identify the owner; retrying this webhook cannot resolve ambiguity.
		return nil
	}
	if err != nil {
		return err
	}
	if delivery.Event == "pull_request" && delivery.Action == "opened" {
		for _, pr := range pullRequests {
			if err := s.store.RecordPullRequestOpened(ctx, orgID, pr, delivery.DeliveryID); err != nil {
				return err
			}
		}
	}
	logger := s.logger
	if logger == nil {
		logger = slog.Default()
	}
	var processErr error
	for _, pr := range pullRequests {
		refresh := s.RefreshPullRequestStatus
		if s.refreshPullRequestStatus != nil {
			refresh = s.refreshPullRequestStatus
		}
		if _, err := refresh(ctx, domain.PullRequestRef{
			ID: pr.ID, OrgID: pr.OrgID, Provider: pr.Provider,
			Repository: pr.Repository, Number: pr.Number,
		}, domain.PullRequestRefreshContext{Source: domain.PullRequestRefreshWebhook}); err != nil {
			logger.Warn("webhook refresh failed; delivery will retry",
				"delivery_id", delivery.DeliveryID,
				"org_id", pr.OrgID,
				"pull_request_id", pr.ID,
				"repository", pr.Repository,
				"number", pr.Number,
				"error", err,
			)
			processErr = errors.Join(processErr, err)
			continue
		}
		logger.Info("webhook refresh succeeded",
			"delivery_id", delivery.DeliveryID,
			"org_id", pr.OrgID,
			"pull_request_id", pr.ID,
			"repository", pr.Repository,
			"number", pr.Number,
		)
	}
	return processErr
}

// A verified opened event contains the PR identity. Associate it only when
// exactly one live worker session owns its source branch in this repository.
// Unknown or ambiguous branches remain unclaimed rather than showing another
// worker's PR in the wrong session.
func (s *Service) claimOpenedWebhookPullRequest(ctx context.Context, orgID string, delivery domain.GitHubWebhookDelivery) (domain.PullRequest, error) {
	var event struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		PullRequest struct {
			Number  int    `json:"number"`
			HTMLURL string `json:"html_url"`
			Title   string `json:"title"`
			State   string `json:"state"`
			Draft   bool   `json:"draft"`
			Head    struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
			User struct {
				Login string `json:"login"`
			} `json:"user"`
			Additions    int `json:"additions"`
			Deletions    int `json:"deletions"`
			ChangedFiles int `json:"changed_files"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(delivery.Payload, &event); err != nil {
		return domain.PullRequest{}, postgres.ErrInvalid
	}
	pr := event.PullRequest
	if pr.Number <= 0 || pr.HTMLURL == "" || pr.Title == "" || pr.Head.Ref == "" || pr.Head.SHA == "" || pr.Base.Ref == "" ||
		!strings.EqualFold(pr.State, "open") || event.Repository.FullName == "" {
		return domain.PullRequest{}, postgres.ErrInvalid
	}
	sessionID, err := s.store.SessionForGitHubPullRequestHead(ctx, orgID, delivery.GitHubRepositoryID, pr.Head.Ref, pr.Head.SHA)
	if err != nil {
		return domain.PullRequest{}, err
	}
	record, err := s.store.ClaimPullRequestRecord(ctx, orgID, sessionID, domain.PullRequest{
		Provider: "github", Repository: event.Repository.FullName, Author: pr.User.Login,
		Number: pr.Number, URL: pr.HTMLURL, Title: pr.Title, State: contract.PRStateOpen,
		Draft: pr.Draft, HeadSHA: pr.Head.SHA, SourceBranch: pr.Head.Ref,
		TargetBranch: pr.Base.Ref, Additions: pr.Additions, Deletions: pr.Deletions,
		ChangedFiles: pr.ChangedFiles,
	})
	if err != nil {
		return domain.PullRequest{}, err
	}
	s.triggerReview(ctx, orgID, sessionID, record)
	return record, nil
}
