package github

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.SCMReviewRequester = (*Provider)(nil)

// RequestReview asks GitHub to request another review from the supplied user.
func (p *Provider) RequestReview(ctx context.Context, request ports.SCMReviewRequest) error {
	if p == nil || p.client == nil {
		return fmt.Errorf("github scm: review requester is not configured")
	}
	if request.PR.Number <= 0 || strings.TrimSpace(request.PR.Repo.Owner) == "" || strings.TrimSpace(request.PR.Repo.Name) == "" {
		return fmt.Errorf("github scm: invalid pull request reference")
	}
	reviewer := strings.TrimSpace(strings.TrimPrefix(request.Reviewer, "@"))
	if reviewer == "" {
		return fmt.Errorf("github scm: reviewer is required")
	}

	payload := struct {
		Reviewers []string `json:"reviewers"`
	}{Reviewers: []string{reviewer}}
	resp, err := p.client.doREST(ctx, http.MethodPost,
		repoPath(request.PR.Repo.Owner, request.PR.Repo.Name, "pulls", strconv.Itoa(request.PR.Number), "requested_reviewers"),
		nil, payload)
	if err != nil {
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w: %w", ports.ErrSCMNotFound, err)
		}
		return err
	}
	return nil
}
