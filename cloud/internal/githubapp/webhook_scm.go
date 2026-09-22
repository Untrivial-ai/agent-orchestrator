package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

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
	if err != nil {
		return err
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
			scheduleCtx, cancelSchedule := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			scheduleErr := s.store.SchedulePullRequestRefresh(
				scheduleCtx,
				pr.OrgID,
				pr.ID,
				domain.PullRequestRefreshWebhookFailed,
				time.Now().UTC(),
				err.Error(),
			)
			cancelSchedule()
			if scheduleErr != nil {
				processErr = errors.Join(processErr, err, scheduleErr)
				continue
			}
			logger.Warn("webhook fallback scheduled",
				"delivery_id", delivery.DeliveryID,
				"org_id", pr.OrgID,
				"pull_request_id", pr.ID,
				"repository", pr.Repository,
				"number", pr.Number,
				"reason", domain.PullRequestRefreshWebhookFailed,
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
