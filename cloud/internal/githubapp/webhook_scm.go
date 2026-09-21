package githubapp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

func scmWebhookPullRequestNumber(event string, payload []byte) (int, error) {
	var envelope struct {
		PullRequest *struct {
			Number int `json:"number"`
		} `json:"pull_request"`
		CheckRun *struct {
			PullRequests []struct {
				Number int `json:"number"`
			} `json:"pull_requests"`
		} `json:"check_run"`
		CheckSuite *struct {
			PullRequests []struct {
				Number int `json:"number"`
			} `json:"pull_requests"`
		} `json:"check_suite"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return 0, postgres.ErrInvalid
	}
	switch event {
	case "pull_request", "pull_request_review":
		if envelope.PullRequest != nil {
			return envelope.PullRequest.Number, nil
		}
	case "check_run":
		if envelope.CheckRun != nil && len(envelope.CheckRun.PullRequests) > 0 {
			return envelope.CheckRun.PullRequests[0].Number, nil
		}
	case "check_suite":
		if envelope.CheckSuite != nil && len(envelope.CheckSuite.PullRequests) > 0 {
			return envelope.CheckSuite.PullRequests[0].Number, nil
		}
	}
	return 0, nil
}

func (s *Service) processSCMWebhook(
	ctx context.Context,
	orgID string,
	delivery domain.GitHubWebhookDelivery,
) error {
	number, err := scmWebhookPullRequestNumber(delivery.Event, delivery.Payload)
	if err != nil {
		return err
	}
	if number <= 0 || delivery.GitHubRepositoryID <= 0 {
		return nil
	}
	pr, err := s.store.PullRequestByGitHubReference(
		ctx, orgID, delivery.GitHubRepositoryID, number,
	)
	if errors.Is(err, postgres.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.RefreshPullRequestStatus(ctx, domain.PullRequestRef{
		ID: pr.ID, OrgID: pr.OrgID, Provider: pr.Provider,
		Repository: pr.Repository, Number: pr.Number,
	})
	return err
}
