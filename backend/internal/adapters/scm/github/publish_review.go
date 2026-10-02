package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.SCMReviewPublisher = (*Provider)(nil)
var _ ports.SCMReviewPublicationFinder = (*Provider)(nil)

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
		// A transport failure (status unset) or a 5xx leaves the review's
		// existence unknown: GitHub may have processed the request before the
		// failure. 4xx rejections are definitive — no review was created. The
		// service maps the unknown case to an uncertain publication instead of
		// a failed one, so a rerun cannot post a duplicate review.
		if resp.StatusCode == 0 || resp.StatusCode >= 500 {
			return ports.SCMReviewPublishResult{}, fmt.Errorf("%w: %w", ports.ErrSCMPublishOutcomeUnknown, err)
		}
		return ports.SCMReviewPublishResult{}, err
	}
	var created struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	// A 2xx response means GitHub accepted the review; only the handle is
	// unreadable. Like a transport outage, the review's existence is known and
	// its identity is not, so the service must record uncertainty instead of a
	// definitive failure a rerun would turn into a duplicate review.
	if err := json.Unmarshal(resp.Body, &created); err != nil {
		return ports.SCMReviewPublishResult{}, fmt.Errorf("%w: github scm: decode created review: %w", ports.ErrSCMPublishOutcomeUnknown, err)
	}
	if created.ID == 0 {
		return ports.SCMReviewPublishResult{}, fmt.Errorf("%w: github scm: created review returned no id", ports.ErrSCMPublishOutcomeUnknown)
	}
	return ports.SCMReviewPublishResult{ReviewID: strconv.FormatInt(created.ID, 10), HTMLURL: created.HTMLURL}, nil
}

// FindPublishedReview locates a review this daemon previously published by
// scanning the pull request's reviews for one whose body carries bodyMarker —
// the review-run marker the service embeds at publication time. It is the
// recovery path for runs whose publication outcome was lost before it could be
// recorded: a found review proves the publication happened, so the run can be
// marked published instead of guessing or reposting a duplicate. Only the most
// recent page of reviews is scanned; AO publications are recent by construction.
func (p *Provider) FindPublishedReview(ctx context.Context, ref ports.SCMPRRef, bodyMarker string) (ports.SCMReviewPublishResult, bool, error) {
	if p == nil || p.client == nil {
		return ports.SCMReviewPublishResult{}, false, fmt.Errorf("github scm: review publisher is not configured")
	}
	if ref.Number <= 0 || strings.TrimSpace(ref.Repo.Owner) == "" || strings.TrimSpace(ref.Repo.Name) == "" || bodyMarker == "" {
		return ports.SCMReviewPublishResult{}, false, fmt.Errorf("github scm: invalid review lookup reference")
	}
	q := url.Values{"per_page": []string{"100"}}
	resp, err := p.client.doREST(ctx, http.MethodGet,
		repoPath(ref.Repo.Owner, ref.Repo.Name, "pulls", strconv.Itoa(ref.Number), "reviews"),
		q, nil)
	if err != nil {
		// Mirror PublishReview: a transport failure or 5xx leaves the lookup
		// unresolved, never "not found" — the caller must keep treating the
		// run's outcome as unknown rather than as evidence of absence.
		if resp.StatusCode == 0 || resp.StatusCode >= 500 {
			return ports.SCMReviewPublishResult{}, false, fmt.Errorf("%w: %w", ports.ErrSCMPublishOutcomeUnknown, err)
		}
		return ports.SCMReviewPublishResult{}, false, err
	}
	var reviews []struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
		Body    string `json:"body"`
	}
	if err := json.Unmarshal(resp.Body, &reviews); err != nil {
		return ports.SCMReviewPublishResult{}, false, fmt.Errorf("github scm: decode review list: %w", err)
	}
	for _, review := range reviews {
		if strings.Contains(review.Body, bodyMarker) {
			return ports.SCMReviewPublishResult{ReviewID: strconv.FormatInt(review.ID, 10), HTMLURL: review.HTMLURL}, true, nil
		}
	}
	return ports.SCMReviewPublishResult{}, false, nil
}
