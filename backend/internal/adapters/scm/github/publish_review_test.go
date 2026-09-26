package github

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestPublishReviewPostsCommentReviewWithInlineComments(t *testing.T) {
	f := newFakeGH(t)
	f.on(http.MethodPost, "/repos/octo/hello/pulls/42/reviews", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event    string `json:"event"`
			CommitID string `json:"commit_id"`
			Body     string `json:"body"`
			Comments []struct {
				Path string `json:"path"`
				Line int    `json:"line"`
				Body string `json:"body"`
			} `json:"comments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload.Event != "COMMENT" {
			t.Errorf("event = %q, want COMMENT", payload.Event)
		}
		if payload.CommitID != "abc123" {
			t.Errorf("commit_id = %q, want abc123", payload.CommitID)
		}
		if !strings.Contains(payload.Body, "quotes \"here\"") {
			t.Errorf("body lost quoting: %q", payload.Body)
		}
		if len(payload.Comments) != 2 {
			t.Fatalf("comments = %#v, want 2 entries", payload.Comments)
		}
		if payload.Comments[0].Path != "main.go" || payload.Comments[0].Line != 12 || payload.Comments[0].Body != "off-by-one" {
			t.Errorf("comment[0] = %#v", payload.Comments[0])
		}
		if payload.Comments[1].Path != "util.go" || payload.Comments[1].Line != 3 || payload.Comments[1].Body != "unused var" {
			t.Errorf("comment[1] = %#v", payload.Comments[1])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 9001, "html_url": "https://github.com/octo/hello/pull/42#pullrequestreview-9001"})
	})
	p := newProviderForTest(t, f)

	result, err := p.PublishReview(ctx(), ports.SCMReviewPublishRequest{
		PR:        ports.SCMPRRef{Number: 42, Repo: ports.SCMRepo{Owner: "octo", Name: "hello"}},
		CommitSHA: "abc123",
		Body:      `Summary with quotes "here" and a code block.`,
		Comments: []ports.SCMReviewComment{
			{Path: "main.go", Line: 12, Body: "off-by-one"},
			{Path: "util.go", Line: 3, Body: "unused var"},
		},
	})
	if err != nil {
		t.Fatalf("PublishReview: %v", err)
	}
	if result.ReviewID != "9001" || result.HTMLURL != "https://github.com/octo/hello/pull/42#pullrequestreview-9001" {
		t.Fatalf("result = %#v", result)
	}
}

func TestPublishReviewOmitsCommitAndCommentsWhenAbsent(t *testing.T) {
	f := newFakeGH(t)
	f.on(http.MethodPost, "/repos/octo/hello/pulls/7/reviews", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if _, ok := body["commit_id"]; ok {
			t.Errorf("empty commit id must be omitted: %v", body)
		}
		if _, ok := body["comments"]; ok {
			t.Errorf("empty comments must be omitted: %v", body)
		}
		if body["event"] != "COMMENT" {
			t.Errorf("event = %v, want COMMENT", body["event"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 7})
	})
	p := newProviderForTest(t, f)

	if _, err := p.PublishReview(ctx(), ports.SCMReviewPublishRequest{
		PR:   ports.SCMPRRef{Number: 7, Repo: ports.SCMRepo{Owner: "octo", Name: "hello"}},
		Body: "ship it",
	}); err != nil {
		t.Fatalf("PublishReview: %v", err)
	}
}

func TestPublishReviewRejectsInvalidInputs(t *testing.T) {
	p := &Provider{}
	if _, err := p.PublishReview(ctx(), ports.SCMReviewPublishRequest{
		PR: ports.SCMPRRef{Number: 0, Repo: ports.SCMRepo{Owner: "octo", Name: "hello"}},
	}); err == nil {
		t.Fatal("zero PR number accepted")
	}
}

func TestPublishReviewRejectsResponseWithoutID(t *testing.T) {
	f := newFakeGH(t)
	f.on(http.MethodPost, "/repos/octo/hello/pulls/42/reviews", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"html_url": "https://github.com/octo/hello/pull/42"})
	})
	p := newProviderForTest(t, f)

	if _, err := p.PublishReview(ctx(), ports.SCMReviewPublishRequest{
		PR:   ports.SCMPRRef{Number: 42, Repo: ports.SCMRepo{Owner: "octo", Name: "hello"}},
		Body: "body",
	}); err == nil || !strings.Contains(err.Error(), "no id") {
		t.Fatalf("err = %v, want missing-id error", err)
	}
}
