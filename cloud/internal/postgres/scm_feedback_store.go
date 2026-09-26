package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

type scmFeedbackCandidate struct {
	Kind           string
	ApplicationKey string
	Message        string
}

func scmFeedbackCandidates(transition domain.PullRequestTransition) []scmFeedbackCandidate {
	pr := transition.Current
	var out []scmFeedbackCandidate
	for _, review := range transition.NewReviews {
		if review.State != contract.ReviewChangesRequest || review.IsBot || !review.AutoInjectReview {
			continue
		}
		body := sanitizeFeedback(review.Body)
		message := fmt.Sprintf("A changes-requested review from @%s is on %s#%d.",
			fallback(sanitizeFeedback(review.Author), "unknown reviewer"), sanitizeFeedback(pr.Repository), pr.Number)
		if strings.TrimSpace(body) != "" {
			message += "\n\nReview body:\n" + body
		}
		if review.URL != "" {
			message += "\n\nReview URL: " + sanitizeFeedback(review.URL)
		}
		out = append(out, scmFeedbackCandidate{
			Kind: "review", ApplicationKey: "review:" + pr.ID + ":" + review.ProviderID + ":" + feedbackHash(review.Body),
			Message: message + "\n\nAddress the requested changes and push.",
		})
	}
	for _, comment := range transition.NewComments {
		if comment.Resolved || comment.Outdated || comment.IsBot || !comment.AutoInjectReview || comment.Path == "" || comment.Line <= 0 {
			continue
		}
		message := fmt.Sprintf("An unresolved review comment is on %s#%d.\n\n%s:%d (@%s):\n%s",
			sanitizeFeedback(pr.Repository), pr.Number, sanitizeFeedback(comment.Path), comment.Line,
			fallback(sanitizeFeedback(comment.Author), "unknown reviewer"), sanitizeFeedback(comment.Body))
		if comment.URL != "" {
			message += "\n\nComment URL: " + sanitizeFeedback(comment.URL)
		}
		out = append(out, scmFeedbackCandidate{
			Kind: "comment", ApplicationKey: "comment:" + pr.ID + ":" + comment.ProviderID + ":" + feedbackHash(comment.Body),
			Message: message + "\n\nAddress the feedback and push.",
		})
	}
	if pr.Mergeability == contract.MergeConflicting &&
		(transition.Previous.Mergeability != contract.MergeConflicting || transition.Previous.HeadSHA != pr.HeadSHA || transition.Previous.BaseSHA != pr.BaseSHA) {
		out = append(out, scmFeedbackCandidate{
			Kind: "conflict", ApplicationKey: "conflict:" + pr.ID + ":" + pr.HeadSHA + ":" + pr.BaseSHA,
			Message: fmt.Sprintf("There are merge conflicts on %s#%d. Rebase onto the base branch and resolve them.\nPR: %s",
				sanitizeFeedback(pr.Repository), pr.Number, sanitizeFeedback(pr.URL)),
		})
	}
	return out
}

func recordSCMFeedbackTx(ctx context.Context, tx pgx.Tx, transition domain.PullRequestTransition) error {
	pr := transition.Current
	for _, candidate := range scmFeedbackCandidates(transition) {
		payload, err := json.Marshal(map[string]any{
			"kind": candidate.Kind, "pullRequestId": pr.ID, "pullRequestUrl": pr.URL,
			"pullRequestNumber": pr.Number, "headSha": pr.HeadSHA, "repository": pr.Repository,
			"message": candidate.Message,
		})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ao_ci_feedback_outbox(
			application_key,org_id,session_id,pull_request_id,payload
		) VALUES($1,$2,$3,$4,$5) ON CONFLICT(application_key) DO NOTHING`,
			candidate.ApplicationKey, pr.OrgID, pr.SessionID, pr.ID, payload); err != nil {
			return err
		}
	}
	return nil
}

func sanitizeFeedback(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
}

func fallback(value, other string) string {
	if strings.TrimSpace(value) == "" {
		return other
	}
	return value
}

func feedbackHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:6])
}
