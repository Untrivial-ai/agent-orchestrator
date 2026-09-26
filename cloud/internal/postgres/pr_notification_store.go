package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

func pullRequestReadyToMerge(pr domain.PullRequest, unresolvedHumanComments bool) bool {
	return pr.State == contract.PRStateOpen && !pr.Draft && pr.CIState == contract.CIPassing &&
		pr.ReviewState != contract.ReviewChangesRequest && !unresolvedHumanComments &&
		pr.Mergeability == contract.MergeMergeable
}

func pullRequestNotificationTransition(previous, current domain.PullRequest, previousComments, currentComments bool) (create, resolve string) {
	previousReady := pullRequestReadyToMerge(previous, previousComments)
	currentReady := pullRequestReadyToMerge(current, currentComments)
	switch {
	case current.State == contract.PRStateMerged && previous.State != contract.PRStateMerged:
		return "pr_merged", "ready_to_merge"
	case current.State == contract.PRStateClosed && previous.State != contract.PRStateClosed:
		return "pr_closed_unmerged", "ready_to_merge"
	case currentReady && !previousReady:
		return "ready_to_merge", ""
	case !currentReady:
		return "", "ready_to_merge"
	default:
		return "", ""
	}
}

func recordPullRequestNotificationsTx(ctx context.Context, tx pgx.Tx, transition domain.PullRequestTransition, previousComments, currentComments bool) error {
	previous, current := transition.Previous, transition.Current
	create, resolve := pullRequestNotificationTransition(previous, current, previousComments, currentComments)
	if resolve != "" {
		if err := resolvePullRequestNotificationTx(ctx, tx, current, resolve); err != nil {
			return err
		}
	}
	if create != "" {
		if err := createPullRequestNotificationTx(ctx, tx, current, create); err != nil {
			return err
		}
	}
	previousFeedback := pullRequestHasReviewFeedback(previous, previousComments)
	currentFeedback := pullRequestHasReviewFeedback(current, currentComments)
	if previousFeedback && !currentFeedback {
		return resolvePullRequestNotificationTx(ctx, tx, current, "review_feedback")
	}
	if currentFeedback {
		exists, err := activePullRequestNotificationExistsTx(ctx, tx, current, "review_feedback")
		if err != nil {
			return err
		}
		if !exists || !previousFeedback || transitionHasNewHumanReviewFeedback(transition) {
			return createPullRequestNotificationTx(ctx, tx, current, "review_feedback")
		}
	}
	return nil
}

func pullRequestHasReviewFeedback(pr domain.PullRequest, unresolvedHumanComments bool) bool {
	return pr.ReviewState == contract.ReviewChangesRequest || unresolvedHumanComments
}

func transitionHasNewHumanReviewFeedback(transition domain.PullRequestTransition) bool {
	for _, review := range transition.NewReviews {
		if !review.IsBot && review.State == contract.ReviewChangesRequest {
			return true
		}
	}
	for _, comment := range transition.NewComments {
		if !comment.IsBot && !comment.Resolved && !comment.Outdated {
			return true
		}
	}
	return false
}

func pullRequestNotificationCopy(kind string, pr domain.PullRequest) (string, string) {
	switch kind {
	case "review_feedback":
		return "Review changes requested", fmt.Sprintf("%s#%d has unresolved review feedback.", pr.Repository, pr.Number)
	case "ready_to_merge":
		return "Pull request ready to merge", fmt.Sprintf("%s#%d is ready to merge.", pr.Repository, pr.Number)
	case "pr_merged":
		return "Pull request merged", fmt.Sprintf("%s#%d was merged.", pr.Repository, pr.Number)
	default:
		return "Pull request closed", fmt.Sprintf("%s#%d was closed without merging.", pr.Repository, pr.Number)
	}
}

func activePullRequestNotificationExistsTx(ctx context.Context, tx pgx.Tx, pr domain.PullRequest, kind string) (bool, error) {
	var recipientID string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(created_by_user_id::text,'') FROM ao_sessions WHERE org_id=$1 AND id=$2`, pr.OrgID, pr.SessionID).Scan(&recipientID); err != nil {
		return false, err
	}
	if recipientID == "" {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('ao.user_id',$1,true)`, recipientID); err != nil {
		return false, err
	}
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ao_notifications WHERE org_id=$1 AND recipient_user_id=$2 AND dedupe_key=$3 AND resolved_at IS NULL)`, pr.OrgID, recipientID, kind+":"+pr.ID).Scan(&exists)
	return exists, err
}

func createPullRequestNotificationTx(ctx context.Context, tx pgx.Tx, pr domain.PullRequest, kind string) error {
	var projectID, recipientID string
	if err := tx.QueryRow(ctx, `SELECT project_id::text,COALESCE(created_by_user_id::text,'') FROM ao_sessions WHERE org_id=$1 AND id=$2`, pr.OrgID, pr.SessionID).Scan(&projectID, &recipientID); err != nil {
		return err
	}
	if recipientID == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('ao.user_id',$1,true)`, recipientID); err != nil {
		return err
	}
	title, body := pullRequestNotificationCopy(kind, pr)
	metadata, _ := json.Marshal(map[string]any{"pullRequestId": pr.ID, "pullRequestUrl": pr.URL, "pullRequestNumber": pr.Number, "repository": pr.Repository})
	dedupe := kind + ":" + pr.ID
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO ao_notifications(org_id,recipient_user_id,project_id,session_id,pull_request_id,source,type,title,body,metadata,dedupe_key,source_event_id,status)
		VALUES($1,$2,$3,$4,$5,'cloud',$6,$7,$8,$9,$10,$10,'unread')
		ON CONFLICT(org_id,recipient_user_id,dedupe_key) WHERE resolved_at IS NULL
		DO UPDATE SET status='unread',body=EXCLUDED.body,metadata=EXCLUDED.metadata,updated_at=now()
		RETURNING id::text`, pr.OrgID, recipientID, projectID, pr.SessionID, pr.ID, kind, title, body, metadata, dedupe).Scan(&id); err != nil {
		return err
	}
	return publishPullRequestNotificationEventTx(ctx, tx, pr.OrgID, recipientID, id, "notification_created", dedupe)
}

func resolvePullRequestNotificationTx(ctx context.Context, tx pgx.Tx, pr domain.PullRequest, kind string) error {
	dedupe := kind + ":" + pr.ID
	rows, err := tx.Query(ctx, `UPDATE ao_notifications SET resolved_at=now(),updated_at=now() WHERE org_id=$1 AND pull_request_id=$2 AND dedupe_key=$3 AND resolved_at IS NULL RETURNING id::text,recipient_user_id::text`, pr.OrgID, pr.ID, dedupe)
	if err != nil {
		return err
	}
	type resolvedNotification struct{ id, recipient string }
	var resolved []resolvedNotification
	for rows.Next() {
		var notification resolvedNotification
		if err := rows.Scan(&notification.id, &notification.recipient); err != nil {
			rows.Close()
			return err
		}
		resolved = append(resolved, notification)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, notification := range resolved {
		if err := publishPullRequestNotificationEventTx(ctx, tx, pr.OrgID, notification.recipient, notification.id, "notification_resolved", "resolved:"+dedupe); err != nil {
			return err
		}
	}
	return nil
}

func publishPullRequestNotificationEventTx(ctx context.Context, tx pgx.Tx, orgID, recipientID, notificationID, kind, eventID string) error {
	var snapshot []byte
	if err := tx.QueryRow(ctx, `SELECT jsonb_build_object('id',id::text,'orgId',org_id::text,'recipientUserId',recipient_user_id::text,'projectId',project_id::text,'sessionId',session_id::text,'source',source,'type',type,'title',title,'body',body,'status',status,'eventId',source_event_id,'metadata',metadata,'resolvedAt',resolved_at,'createdAt',created_at,'updatedAt',updated_at) FROM ao_notifications WHERE org_id=$1 AND id=$2`, orgID, notificationID).Scan(&snapshot); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ao_notification_events(org_id,recipient_user_id,notification_id,kind,source_event_id,snapshot) VALUES($1,$2,$3,$4,$5,$6)`, orgID, recipientID, notificationID, kind, eventID, snapshot); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT pg_notify('ao_notification_event',$1)`, orgID)
	return err
}
