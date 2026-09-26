package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.SCMReviewPublisher = (*Provider)(nil)

// PublishReview creates one COMMENT review on a GitHub pull request: the
// summary body plus optional inline comments, anchored at the reviewed commit.
// The daemon owns review publication since #5701; reviewers submit to AO.
func (p *Provider) PublishReview(ctx context.Context, request ports.SCMReviewPublishRequest) (ports.SCMReviewPublishResult, error) {
	if p == nil || p.client == nil {
		return ports.SCMReviewPublishResult{}, fmt.Errorf("github scm: review publisher is not configured")
	}
	if request.PR.Number <= 0 || strings.TrimSpace(request.PR.Repo.Owner) == "" || strings.TrimSpace(request.PR.Repo.Name) == "" {
		return ports.SCMReviewPublishResult{}, fmt.Errorf("github scm: invalid pull request reference")
	}
	// Reviews are always COMMENT: AO reviews are posted from the PR author's
	// own account, and GitHub rejects APPROVE and REQUEST_CHANGES there.
	payload := struct {
		Event    string                   `json:"event"`
		CommitID string                   `json:"commit_id,omitempty"`
		Body     string                   `json:"body"`
		Comments []map[string]interface{} `json:"comments,omitempty"`
	}{Event: "COMMENT", CommitID: strings.TrimSpace(request.CommitSHA), Body: request.Body}
	for _, comment := range request.Comments {
		payload.Comments = append(payload.Comments, map[string]interface{}{
			"path": comment.Path,
			"line": comment.Line,
			"body": comment.Body,
		})
	}
	resp, err := p.client.doREST(ctx, http.MethodPost,
		repoPath(request.PR.Repo.Owner, request.PR.Repo.Name, "pulls", strconv.Itoa(request.PR.Number), "reviews"),
		nil, payload)
	if err != nil {
		return ports.SCMReviewPublishResult{}, err
	}
	var created struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(resp.Body, &created); err != nil {
		return ports.SCMReviewPublishResult{}, fmt.Errorf("github scm: decode created review: %w", err)
	}
	if created.ID == 0 {
		return ports.SCMReviewPublishResult{}, fmt.Errorf("github scm: created review returned no id")
	}
	return ports.SCMReviewPublishResult{ReviewID: strconv.FormatInt(created.ID, 10), HTMLURL: created.HTMLURL}, nil
}
