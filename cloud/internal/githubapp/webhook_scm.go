package githubapp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

type scmWebhookPullRequestRef struct {
	Number  int
	HeadSHA string
}

type scmWebhookTargetSet struct {
	PullRequestNumber int
	HeadSHA           string
	BeforeSHA         string
	AfterSHA          string
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
		SHA    string `json:"sha"`
		Before string `json:"before"`
		After  string `json:"after"`
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
		return scmWebhookTargetSet{
			BeforeSHA: envelope.Before, AfterSHA: envelope.After, RepositoryWide: true,
		}, nil
	}
	return scmWebhookTargetSet{}, nil
}

func scmWebhookPullRequestReference(event string, payload []byte) (scmWebhookPullRequestRef, error) {
	target, err := scmWebhookTargets(event, payload)
	return scmWebhookPullRequestRef{Number: target.PullRequestNumber, HeadSHA: target.HeadSHA}, err
}

func scmWebhookPullRequestNumber(event string, payload []byte) (int, error) {
	ref, err := scmWebhookPullRequestReference(event, payload)
	return ref.Number, err
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
	for _, pr := range pullRequests {
		if _, err := s.RefreshPullRequestStatus(ctx, domain.PullRequestRef{
			ID: pr.ID, OrgID: pr.OrgID, Provider: pr.Provider,
			Repository: pr.Repository, Number: pr.Number,
		}); err != nil {
			return err
		}
	}
	return nil
}
