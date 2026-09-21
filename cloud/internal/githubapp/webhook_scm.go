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

func scmWebhookPullRequestReference(event string, payload []byte) (scmWebhookPullRequestRef, error) {
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
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return scmWebhookPullRequestRef{}, postgres.ErrInvalid
	}
	switch event {
	case "pull_request", "pull_request_review":
		if envelope.PullRequest != nil {
			return scmWebhookPullRequestRef{Number: envelope.PullRequest.Number, HeadSHA: envelope.PullRequest.Head.SHA}, nil
		}
	case "check_run":
		if envelope.CheckRun != nil {
			ref := scmWebhookPullRequestRef{HeadSHA: envelope.CheckRun.HeadSHA}
			if len(envelope.CheckRun.PullRequests) > 0 {
				ref.Number = envelope.CheckRun.PullRequests[0].Number
			}
			return ref, nil
		}
	case "check_suite":
		if envelope.CheckSuite != nil {
			ref := scmWebhookPullRequestRef{HeadSHA: envelope.CheckSuite.HeadSHA}
			if len(envelope.CheckSuite.PullRequests) > 0 {
				ref.Number = envelope.CheckSuite.PullRequests[0].Number
			}
			return ref, nil
		}
	}
	return scmWebhookPullRequestRef{}, nil
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
	ref, err := scmWebhookPullRequestReference(delivery.Event, delivery.Payload)
	if err != nil {
		return err
	}
	if (ref.Number <= 0 && ref.HeadSHA == "") || delivery.GitHubRepositoryID <= 0 {
		return nil
	}
	var pr domain.PullRequest
	if ref.Number > 0 {
		pr, err = s.store.PullRequestByGitHubReference(ctx, orgID, delivery.GitHubRepositoryID, ref.Number)
	} else {
		pr, err = s.store.PullRequestByGitHubHead(ctx, orgID, delivery.GitHubRepositoryID, ref.HeadSHA)
	}
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
