package postgres

import (
	"context"
	"encoding/json"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

// ApplyPullRequestSnapshot atomically replaces the authoritative PR read model.
func (s *Store) ApplyPullRequestSnapshot(ctx context.Context, orgID, pullRequestID string, snapshot domain.PullRequestSnapshot) (domain.PullRequestTransition, error) {
	var transition domain.PullRequestTransition
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		previous, err := scanPullRequest(tx.QueryRow(ctx, `SELECT `+pullRequestColumns+` FROM ao_pull_requests WHERE org_id=$1 AND id=$2 FOR UPDATE`, orgID, pullRequestID))
		if err != nil {
			return err
		}
		transition.Previous = previous
		previousComments, err := unresolvedHumanCommentsTx(ctx, tx, orgID, pullRequestID)
		if err != nil {
			return err
		}
		var autoInjectReview bool
		if err := tx.QueryRow(ctx, `SELECT auto_inject_review FROM ao_sessions WHERE org_id=$1 AND id=$2`, orgID, previous.SessionID).Scan(&autoInjectReview); err != nil {
			return err
		}
		checks := snapshot.Observation.Checks
		if len(checks) == 0 {
			checks = json.RawMessage(`[]`)
		}
		state := snapshot.Observation.State
		if snapshot.Observation.Draft && state == "draft" {
			state = "open"
		}
		current, err := scanPullRequest(tx.QueryRow(ctx, `UPDATE ao_pull_requests SET
			author=$3, author_avatar_url=$4, url=$5, title=$6, state=$7, draft=$8,
			head_sha=$9, base_sha=$10, merge_commit_sha=$11, source_branch=$12, target_branch=$13,
			additions=$14, deletions=$15, changed_files=$16, ci_state=$17, review_state=$18,
			mergeability=$19, checks=$20, review_partial=$21, created_at_provider=$22,
			updated_at_provider=$23, merged_at_provider=$24, closed_at_provider=$25,
			observed_at=now(), updated_at=now()
			WHERE org_id=$1 AND id=$2 RETURNING `+pullRequestColumns,
			orgID, pullRequestID, snapshot.Author, snapshot.AuthorAvatarURL, snapshot.URL, snapshot.Title,
			string(state), snapshot.Observation.Draft, snapshot.Observation.HeadSHA, snapshot.BaseSHA,
			snapshot.MergeCommitSHA, snapshot.SourceBranch, snapshot.TargetBranch,
			snapshot.Observation.Additions, snapshot.Observation.Deletions, snapshot.Observation.ChangedFiles,
			string(snapshot.Observation.CIState), string(snapshot.Observation.ReviewState),
			string(snapshot.Observation.Mergeability), checks, snapshot.ReviewsPartial,
			snapshot.CreatedAtProvider, snapshot.UpdatedAtProvider, snapshot.MergedAtProvider, snapshot.ClosedAtProvider))
		if err != nil {
			return err
		}
		transition.Current = current

		existingReviews, err := stringSet(ctx, tx, `SELECT provider_review_id FROM ao_pr_reviews WHERE org_id=$1 AND pull_request_id=$2`, orgID, pullRequestID)
		if err != nil {
			return err
		}
		reviewIDs := make([]string, 0, len(snapshot.Reviews))
		for _, review := range snapshot.Reviews {
			reviewIDs = append(reviewIDs, review.ProviderID)
			if !existingReviews[review.ProviderID] {
				review.AutoInjectReview = autoInjectReview
				transition.NewReviews = append(transition.NewReviews, review)
			}
			_, err = tx.Exec(ctx, `INSERT INTO ao_pr_reviews (
				org_id,pull_request_id,provider_review_id,provider_database_id,author_login,state,body,url,target_sha,is_bot,auto_inject_review,submitted_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (pull_request_id,provider_review_id) DO UPDATE SET
				provider_database_id=EXCLUDED.provider_database_id,author_login=EXCLUDED.author_login,
				state=EXCLUDED.state,body=EXCLUDED.body,url=EXCLUDED.url,target_sha=EXCLUDED.target_sha,
				is_bot=EXCLUDED.is_bot,submitted_at=EXCLUDED.submitted_at,observed_at=now(),updated_at=now()`,
				orgID, pullRequestID, review.ProviderID, nullableInt64(review.DatabaseID), review.Author, string(review.State), review.Body, review.URL, review.TargetSHA, review.IsBot, autoInjectReview, review.SubmittedAt)
			if err != nil {
				return err
			}
		}

		threadIDs := make([]string, 0, len(snapshot.Threads))
		for _, thread := range snapshot.Threads {
			threadIDs = append(threadIDs, thread.ProviderID)
			_, err = tx.Exec(ctx, `INSERT INTO ao_pr_review_threads (
				org_id,pull_request_id,provider_thread_id,is_resolved,is_outdated,path,line,is_bot,auto_inject_review
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (pull_request_id,provider_thread_id) DO UPDATE SET
				is_resolved=EXCLUDED.is_resolved,is_outdated=EXCLUDED.is_outdated,path=EXCLUDED.path,
				line=EXCLUDED.line,is_bot=EXCLUDED.is_bot,observed_at=now(),updated_at=now()`,
				orgID, pullRequestID, thread.ProviderID, thread.Resolved, thread.Outdated, thread.Path, nullableLine(thread.Line), thread.IsBot, autoInjectReview)
			if err != nil {
				return err
			}
		}

		existingComments, err := stringSet(ctx, tx, `SELECT provider_comment_id FROM ao_pr_review_comments WHERE org_id=$1 AND pull_request_id=$2`, orgID, pullRequestID)
		if err != nil {
			return err
		}
		commentIDs := make([]string, 0, len(snapshot.Comments))
		for _, comment := range snapshot.Comments {
			commentIDs = append(commentIDs, comment.ProviderID)
			if !existingComments[comment.ProviderID] {
				comment.AutoInjectReview = autoInjectReview
				transition.NewComments = append(transition.NewComments, comment)
			}
			_, err = tx.Exec(ctx, `INSERT INTO ao_pr_review_comments (
				org_id,pull_request_id,provider_comment_id,provider_database_id,provider_thread_id,
				provider_review_id,author_login,body,url,path,line,is_resolved,is_outdated,is_bot,auto_inject_review
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			ON CONFLICT (pull_request_id,provider_comment_id) DO UPDATE SET
				provider_database_id=EXCLUDED.provider_database_id,provider_thread_id=EXCLUDED.provider_thread_id,
				provider_review_id=EXCLUDED.provider_review_id,author_login=EXCLUDED.author_login,
				body=EXCLUDED.body,url=EXCLUDED.url,path=EXCLUDED.path,line=EXCLUDED.line,
				is_resolved=EXCLUDED.is_resolved,is_outdated=EXCLUDED.is_outdated,is_bot=EXCLUDED.is_bot,
				observed_at=now(),updated_at=now()`,
				orgID, pullRequestID, comment.ProviderID, nullableInt64(comment.DatabaseID), comment.ThreadProviderID,
				comment.ReviewProviderID, comment.Author, comment.Body, comment.URL, comment.Path, nullableLine(comment.Line),
				comment.Resolved, comment.Outdated, comment.IsBot, autoInjectReview)
			if err != nil {
				return err
			}
		}
		if !snapshot.ReviewsPartial {
			if _, err := tx.Exec(ctx, `DELETE FROM ao_pr_review_comments WHERE org_id=$1 AND pull_request_id=$2 AND NOT (provider_comment_id=ANY($3))`, orgID, pullRequestID, commentIDs); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM ao_pr_review_threads WHERE org_id=$1 AND pull_request_id=$2 AND NOT (provider_thread_id=ANY($3))`, orgID, pullRequestID, threadIDs); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM ao_pr_reviews WHERE org_id=$1 AND pull_request_id=$2 AND NOT (provider_review_id=ANY($3))`, orgID, pullRequestID, reviewIDs); err != nil {
				return err
			}
		}
		currentComments, err := unresolvedHumanCommentsTx(ctx, tx, orgID, pullRequestID)
		if err != nil {
			return err
		}
		if _, err := recordPullRequestTransitionTx(ctx, tx, previous, current); err != nil {
			return err
		}
		if err := recordPullRequestNotificationsTx(ctx, tx, transition, previousComments, currentComments); err != nil {
			return err
		}
		if err := recordSCMFeedbackTx(ctx, tx, transition); err != nil {
			return err
		}
		return appendTypedEvent(ctx, tx, orgID, current.SessionID, "scm.updated", map[string]any{
			"pullRequestId": pullRequestID,
			"number":        current.Number,
			"repository":    current.Repository,
		})
	})
	return transition, normalizeConstraintError(err)
}

func unresolvedHumanCommentsTx(ctx context.Context, tx pgx.Tx, orgID, pullRequestID string) (bool, error) {
	var found bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ao_pr_review_comments WHERE org_id=$1 AND pull_request_id=$2 AND is_resolved=false AND is_outdated=false AND is_bot=false AND path<>'' AND line IS NOT NULL)`, orgID, pullRequestID).Scan(&found)
	return found, err
}

func (s *Store) PullRequestSnapshot(ctx context.Context, orgID, pullRequestID string) (domain.PullRequestSnapshot, error) {
	var out domain.PullRequestSnapshot
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		pr, err := scanPullRequest(tx.QueryRow(ctx, `SELECT `+pullRequestColumns+` FROM ao_pull_requests WHERE org_id=$1 AND id=$2`, orgID, pullRequestID))
		if err != nil {
			return err
		}
		out = domain.PullRequestSnapshot{Author: pr.Author, AuthorAvatarURL: pr.AuthorAvatarURL, Title: pr.Title, URL: pr.URL, SourceBranch: pr.SourceBranch, TargetBranch: pr.TargetBranch, BaseSHA: pr.BaseSHA, MergeCommitSHA: pr.MergeCommitSHA, CreatedAtProvider: pr.CreatedAtProvider, UpdatedAtProvider: pr.UpdatedAtProvider, MergedAtProvider: pr.MergedAtProvider, ClosedAtProvider: pr.ClosedAtProvider, ReviewsPartial: pr.ReviewPartial}
		out.Observation = domain.PullRequestObservation{State: pr.State, Draft: pr.Draft, HeadSHA: pr.HeadSHA, Additions: pr.Additions, Deletions: pr.Deletions, ChangedFiles: pr.ChangedFiles, CIState: pr.CIState, ReviewState: pr.ReviewState, Mergeability: pr.Mergeability, Checks: pr.Checks}
		if err := loadReviews(ctx, tx, orgID, pullRequestID, &out); err != nil {
			return err
		}
		return nil
	})
	return out, err
}

func loadReviews(ctx context.Context, tx pgx.Tx, orgID, pullRequestID string, out *domain.PullRequestSnapshot) error {
	rows, err := tx.Query(ctx, `SELECT provider_review_id,COALESCE(provider_database_id,0),author_login,state,body,url,target_sha,is_bot,auto_inject_review,submitted_at FROM ao_pr_reviews WHERE org_id=$1 AND pull_request_id=$2 ORDER BY submitted_at NULLS LAST,provider_review_id`, orgID, pullRequestID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var r domain.PullRequestReview
		var state string
		if err := rows.Scan(&r.ProviderID, &r.DatabaseID, &r.Author, &state, &r.Body, &r.URL, &r.TargetSHA, &r.IsBot, &r.AutoInjectReview, &r.SubmittedAt); err != nil {
			rows.Close()
			return err
		}
		r.State = contractReview(state)
		out.Reviews = append(out.Reviews, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	tr, err := tx.Query(ctx, `SELECT provider_thread_id,path,COALESCE(line,0),is_resolved,is_outdated,is_bot FROM ao_pr_review_threads WHERE org_id=$1 AND pull_request_id=$2 ORDER BY provider_thread_id`, orgID, pullRequestID)
	if err != nil {
		return err
	}
	for tr.Next() {
		var v domain.PullRequestReviewThread
		if err := tr.Scan(&v.ProviderID, &v.Path, &v.Line, &v.Resolved, &v.Outdated, &v.IsBot); err != nil {
			tr.Close()
			return err
		}
		out.Threads = append(out.Threads, v)
	}
	tr.Close()
	if err := tr.Err(); err != nil {
		return err
	}
	cr, err := tx.Query(ctx, `SELECT provider_comment_id,COALESCE(provider_database_id,0),provider_thread_id,provider_review_id,author_login,body,url,path,COALESCE(line,0),is_resolved,is_outdated,is_bot,auto_inject_review FROM ao_pr_review_comments WHERE org_id=$1 AND pull_request_id=$2 ORDER BY provider_comment_id`, orgID, pullRequestID)
	if err != nil {
		return err
	}
	for cr.Next() {
		var v domain.PullRequestReviewComment
		if err := cr.Scan(&v.ProviderID, &v.DatabaseID, &v.ThreadProviderID, &v.ReviewProviderID, &v.Author, &v.Body, &v.URL, &v.Path, &v.Line, &v.Resolved, &v.Outdated, &v.IsBot, &v.AutoInjectReview); err != nil {
			cr.Close()
			return err
		}
		out.Comments = append(out.Comments, v)
	}
	cr.Close()
	return cr.Err()
}

func stringSet(ctx context.Context, tx pgx.Tx, query string, args ...any) (map[string]bool, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}
func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
func nullableLine(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}
func contractReview(v string) contract.ReviewDecision { return contract.ReviewDecision(v) }
