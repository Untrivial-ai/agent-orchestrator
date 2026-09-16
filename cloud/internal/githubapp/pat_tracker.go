package githubapp

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

// PATTrackerStore is the durable surface the PAT pull request tracker needs.
// The concrete *postgres.Store satisfies it.
type PATTrackerStore interface {
	PullRequestTrackingSessions(ctx context.Context) ([]domain.PullRequestTrackingSession, error)
	SessionGitHubPAT(ctx context.Context, orgID, sessionID string) (domain.WorkerGitHubPAT, error)
	OpenPullRequestRefsForSession(ctx context.Context, orgID, sessionID string) ([]domain.PullRequestRef, error)
	PullRequestTracked(ctx context.Context, orgID, provider, repository string, number int) (bool, error)
	ClaimPullRequestRecord(ctx context.Context, orgID, sessionID string, input domain.PullRequest) (domain.PullRequest, error)
	UpdatePullRequestObservation(
		ctx context.Context,
		orgID, pullRequestID string,
		observation domain.PullRequestObservation,
	) (domain.PullRequest, error)
}

// PATDecrypter turns a session's stored PAT credential into the plaintext
// token. Decryption stays with the caller that owns the secret cipher and its
// associated-data scheme.
type PATDecrypter func(domain.WorkerGitHubPAT) (string, error)

// PATPullRequestTracker gives a deployment without a GitHub App the pull
// request tracking the local daemon has: it discovers PRs opened from each live
// session's branch, however the agent opened them, and keeps every tracked open
// PR's CI, review, and mergeability current. It authenticates with the session
// creator's PAT, the same credential worker checkout and push already use.
type PATPullRequestTracker struct {
	client  *Client
	store   PATTrackerStore
	decrypt PATDecrypter
	log     *slog.Logger
}

// NewPATPullRequestTracker wires a REST-only GitHub client to the store. Pass a
// client built with NewRESTClient.
func NewPATPullRequestTracker(
	client *Client,
	store PATTrackerStore,
	decrypt PATDecrypter,
	logger *slog.Logger,
) *PATPullRequestTracker {
	if logger == nil {
		logger = slog.Default()
	}
	return &PATPullRequestTracker{client: client, store: store, decrypt: decrypt, log: logger}
}

// ScanOnce discovers and refreshes pull requests for every live session once.
// A failure for one session is logged and does not stop the others.
func (t *PATPullRequestTracker) ScanOnce(ctx context.Context) error {
	sessions, err := t.store.PullRequestTrackingSessions(ctx)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if ctx.Err() != nil {
			return nil
		}
		if err := t.trackSession(ctx, session); err != nil {
			t.log.Warn("pat pull request tracking failed",
				"org_id", session.OrgID, "session_id", session.SessionID, "err", err)
		}
	}
	return nil
}

func (t *PATPullRequestTracker) trackSession(ctx context.Context, session domain.PullRequestTrackingSession) error {
	credential, err := t.store.SessionGitHubPAT(ctx, session.OrgID, session.SessionID)
	if errors.Is(err, postgres.ErrNotFound) || errors.Is(err, postgres.ErrForbidden) {
		// No PAT configured: nothing this tracker can authenticate with.
		return nil
	}
	if err != nil {
		return err
	}
	owner, repo, ok := githubPATRepository(credential.CloneURL)
	if !ok {
		return nil
	}
	token, err := t.decrypt(credential)
	if err != nil {
		return err
	}
	if token == "" {
		return nil
	}
	// Only a running sandbox can open a new PR, so paused sessions skip the
	// discovery call and only refresh what they already track.
	if session.Active {
		if err := t.discover(ctx, session, token, owner, repo); err != nil {
			return err
		}
	}
	refs, err := t.store.OpenPullRequestRefsForSession(ctx, session.OrgID, session.SessionID)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if !strings.EqualFold(ref.Repository, owner+"/"+repo) {
			continue
		}
		observation, err := observePullRequest(ctx, t.client, token, owner, repo, ref.Number)
		if err != nil {
			return err
		}
		if _, err := t.store.UpdatePullRequestObservation(ctx, ref.OrgID, ref.ID, observation); err != nil {
			return err
		}
	}
	return nil
}

// discover claims PRs whose head is the session's branch and that no session
// tracks yet. A PR another session already claimed is never reassigned.
func (t *PATPullRequestTracker) discover(
	ctx context.Context,
	session domain.PullRequestTrackingSession,
	token, owner, repo string,
) error {
	pullRequests, err := t.client.ListPullRequestsByHead(ctx, token, owner, repo, session.Branch)
	if err != nil {
		return err
	}
	fullName := owner + "/" + repo
	for _, pr := range pullRequests {
		if pr.Number <= 0 || pr.HTMLURL == "" || pr.Head.Ref != session.Branch {
			continue
		}
		tracked, err := t.store.PullRequestTracked(ctx, session.OrgID, "github", fullName, pr.Number)
		if err != nil {
			return err
		}
		if tracked {
			continue
		}
		if _, err := t.store.ClaimPullRequestRecord(ctx, session.OrgID, session.SessionID, domain.PullRequest{
			Provider:     "github",
			Repository:   fullName,
			Author:       pr.User.Login,
			Number:       pr.Number,
			URL:          pr.HTMLURL,
			Title:        pr.Title,
			State:        listedPullRequestState(pr),
			Draft:        pr.Draft,
			HeadSHA:      pr.Head.SHA,
			SourceBranch: pr.Head.Ref,
			TargetBranch: pr.Base.Ref,
		}); err != nil {
			return err
		}
		t.log.Info("pat pull request tracker claimed pull request",
			"org_id", session.OrgID, "session_id", session.SessionID, "number", pr.Number)
	}
	return nil
}

func listedPullRequestState(pr PullRequestResponse) contract.PRState {
	switch {
	case pr.MergedAt != nil:
		return contract.PRStateMerged
	case pr.State == "closed":
		return contract.PRStateClosed
	default:
		return contract.PRStateOpen
	}
}

// githubPATRepository accepts only the clone URLs a PAT may be used against:
// bare https github.com URLs, as the worker PAT grant requires.
func githubPATRepository(cloneURL string) (string, string, bool) {
	parsed, err := url.Parse(cloneURL)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.User != nil {
		return "", "", false
	}
	owner, repo, err := ownerRepoFromCloneURL(cloneURL)
	if err != nil {
		return "", "", false
	}
	return owner, repo, true
}
