package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.SCMReviewRequester = (*Provider)(nil)
var _ ports.SCMReviewResolver = (*Provider)(nil)

// RequestReview asks GitLab to assign the supplied user as a merge-request
// reviewer. GitLab's update endpoint expects numeric reviewer ids, so the
// username carried by AO is resolved first and existing reviewers are preserved.
func (p *Provider) RequestReview(ctx context.Context, request ports.SCMReviewRequest) error {
	if p == nil || p.client == nil {
		return fmt.Errorf("gitlab scm: review requester is not configured")
	}
	if request.PR.Number <= 0 || strings.TrimSpace(request.PR.Repo.Owner) == "" || strings.TrimSpace(request.PR.Repo.Name) == "" {
		return fmt.Errorf("gitlab scm: invalid merge request reference")
	}
	reviewer := strings.TrimSpace(strings.TrimPrefix(request.Reviewer, "@"))
	if reviewer == "" {
		return fmt.Errorf("gitlab scm: reviewer is required")
	}

	client, err := p.clientForRepoErr(request.PR.Repo)
	if err != nil {
		return err
	}
	reviewerID, err := lookupUserID(ctx, client, reviewer)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/projects/%s/merge_requests/%d", projectPath(request.PR.Repo.Owner, request.PR.Repo.Name), request.PR.Number)
	existing, err := mergeRequestReviewerIDs(ctx, client, path)
	if err != nil {
		return err
	}
	reviewerIDs := appendReviewerID(existing, reviewerID)
	resp, err := client.doMERGE(ctx, path, nil, struct {
		ReviewerIDs []int `json:"reviewer_ids"`
	}{ReviewerIDs: reviewerIDs})
	if err != nil {
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w: %w", ports.ErrSCMNotFound, err)
		}
		return err
	}
	return nil
}

// ResolveReviewThread marks a GitLab merge-request discussion as resolved.
func (p *Provider) ResolveReviewThread(ctx context.Context, request ports.SCMReviewResolveRequest) error {
	if p == nil || p.client == nil {
		return fmt.Errorf("gitlab scm: review resolver is not configured")
	}
	if request.PR.Number <= 0 || strings.TrimSpace(request.PR.Repo.Owner) == "" || strings.TrimSpace(request.PR.Repo.Name) == "" {
		return fmt.Errorf("gitlab scm: invalid merge request reference")
	}
	threadID := strings.TrimSpace(request.ThreadID)
	if threadID == "" {
		return fmt.Errorf("gitlab scm: review discussion id is required")
	}

	client, err := p.clientForRepoErr(request.PR.Repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/discussions/%s",
		projectPath(request.PR.Repo.Owner, request.PR.Repo.Name), request.PR.Number, url.PathEscape(threadID))
	q := url.Values{}
	q.Set("resolved", "true")
	resp, err := client.doMERGE(ctx, path, q, nil)
	if err != nil {
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w: %w", ports.ErrSCMNotFound, err)
		}
		return err
	}
	return nil
}

func lookupUserID(ctx context.Context, client *Client, username string) (int, error) {
	q := url.Values{}
	q.Set("username", username)
	resp, err := client.doGET(ctx, "/users", q)
	if err != nil {
		if resp.StatusCode == http.StatusNotFound {
			return 0, fmt.Errorf("%w: %w", ports.ErrSCMNotFound, err)
		}
		return 0, err
	}
	var users []struct {
		ID       int    `json:"id"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(resp.Body, &users); err != nil {
		return 0, fmt.Errorf("gitlab scm: decode users response: %w", err)
	}
	for _, user := range users {
		if user.ID > 0 && strings.EqualFold(user.Username, username) {
			return user.ID, nil
		}
	}
	return 0, fmt.Errorf("%w: gitlab reviewer %q not found", ports.ErrSCMNotFound, username)
}

func mergeRequestReviewerIDs(ctx context.Context, client *Client, path string) ([]int, error) {
	resp, err := client.doGET(ctx, path, nil)
	if err != nil {
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %w", ports.ErrSCMNotFound, err)
		}
		return nil, err
	}
	var mr struct {
		Reviewers []struct {
			ID int `json:"id"`
		} `json:"reviewers"`
	}
	if err := json.Unmarshal(resp.Body, &mr); err != nil {
		return nil, fmt.Errorf("gitlab scm: decode merge request response: %w", err)
	}
	ids := make([]int, 0, len(mr.Reviewers))
	for _, reviewer := range mr.Reviewers {
		if reviewer.ID > 0 {
			ids = append(ids, reviewer.ID)
		}
	}
	return ids, nil
}

func appendReviewerID(existing []int, reviewerID int) []int {
	ids := make([]int, 0, len(existing)+1)
	seen := map[int]bool{}
	for _, id := range existing {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if reviewerID > 0 && !seen[reviewerID] {
		ids = append(ids, reviewerID)
	}
	return ids
}
