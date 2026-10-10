package testing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// CreatePullRequestRunsInput selects a PR and a configured target recipe.
type CreatePullRequestRunsInput struct {
	ProjectID domain.ProjectID
	PRURL     string
	RecipeID  string
	Requester string
}

// PullRequestRuns identifies the two immutable revisions of a comparison.
type PullRequestRuns struct{ Base, Head domain.TestRunRecord }

// CreatePullRequestRuns resolves metadata once, before starting any worker/target.
func (s *Service) CreatePullRequestRuns(ctx context.Context, in CreatePullRequestRunsInput) (PullRequestRuns, error) {
	var empty PullRequestRuns
	if err := s.configured(); err != nil {
		return empty, err
	}
	if s.deps.PullRequests == nil {
		return empty, ProviderNotConfigured()
	}
	if strings.TrimSpace(in.Requester) == "" || strings.TrimSpace(in.PRURL) == "" {
		return empty, invalid("PR URL and requester are required")
	}
	project, found, err := s.deps.Store.GetProject(ctx, string(in.ProjectID))
	if err != nil {
		return empty, err
	}
	if !found || !project.ArchivedAt.IsZero() {
		return empty, apierr.NotFound("PROJECT_NOT_FOUND", "Unknown project")
	}
	if in.RecipeID == "" {
		in.RecipeID = "local-ao"
	}
	recipe, found := s.deps.Recipes[in.RecipeID]
	if !found {
		return empty, invalid("Unknown configured testing recipe")
	}
	snapshot, checkout, err := s.deps.PullRequests.Snapshot(ctx, project, in.PRURL)
	if err != nil {
		return empty, apierr.Invalid("TEST_PR_INTAKE_FAILED", fmt.Sprintf("Resolve PR: %s", err), nil)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return empty, err
	}
	recipe.PullRequest, recipe.CheckoutPath = &snapshot, checkout
	input := CreateRunInput{ProjectID: in.ProjectID, IssueURL: snapshot.URL, IssueSnapshot: string(data), CommitSHA: snapshot.BaseSHA, RecipeID: recipe.ID, Requester: in.Requester, recipeOverride: &recipe}
	base, err := s.CreateRun(ctx, input)
	if err != nil {
		return empty, err
	}
	input.CommitSHA, input.LinkedRunID = snapshot.HeadSHA, base.ID
	head, err := s.CreateRun(ctx, input)
	if err != nil {
		return empty, fmt.Errorf("base run %s created; create head run: %w", base.ID, err)
	}
	return PullRequestRuns{Base: base, Head: head}, nil
}
