package githubapp

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

// PATWriteStore is the durable-record surface the PAT write path needs. The
// concrete *postgres.Store satisfies it.
type PATWriteStore interface {
	CreatePullRequestRecord(
		ctx context.Context,
		orgID, sessionID string,
		provider, repository, author string,
		number int,
		url, sourceBranch, targetBranch, headSHA, title string,
		additions, deletions, changedFiles int,
	) (domain.PullRequest, error)
	ClaimPullRequestRecord(
		ctx context.Context,
		orgID, sessionID string,
		input domain.PullRequest,
	) (domain.PullRequest, error)
}

// PATWriteService performs GitHub write operations with a user's personal
// access token instead of a GitHub App installation token. It exists so an
// environment without a local GitHub App — which reaches GitHub read-only
// through the remote capability broker — can still open and claim pull requests
// on behalf of a user who configured a PAT. Decrypting the PAT is the caller's
// job (it needs the worker identity the CheckoutBroker interface lacks); this
// type is handed the ready token and clone URL and mirrors the App path minus
// the installation auth and the best-effort review trigger.
type PATWriteService struct {
	client *Client
	store  PATWriteStore
}

// NewPATWriteService wires a REST-only GitHub client to the record store. Pass a
// client built with NewRESTClient — no App credentials are required.
func NewPATWriteService(client *Client, store PATWriteStore) *PATWriteService {
	return &PATWriteService{client: client, store: store}
}

// RaisePullRequest opens a PR with the PAT and durably records it. When no base
// branch is given it resolves the repository's default branch through the same
// PAT, matching Service.RaisePullRequest's behavior.
func (p *PATWriteService) RaisePullRequest(
	ctx context.Context,
	orgID, sessionID, cloneURL, token string,
	input domain.RaisePullRequest,
) (domain.PullRequest, error) {
	title := strings.TrimSpace(input.Title)
	head := strings.TrimSpace(input.HeadBranch)
	if title == "" || head == "" {
		return domain.PullRequest{}, postgres.ErrInvalid
	}
	owner, repo, err := ownerRepoFromCloneURL(cloneURL)
	if err != nil {
		return domain.PullRequest{}, err
	}
	base := strings.TrimSpace(input.BaseBranch)
	if base == "" {
		repository, err := p.client.GetRepositoryAsUser(ctx, token, owner, repo)
		if err != nil {
			return domain.PullRequest{}, err
		}
		base = strings.TrimSpace(repository.DefaultBranch)
	}
	if base == "" {
		return domain.PullRequest{}, fmt.Errorf(
			"%w: no base branch given and the repository has none on record",
			postgres.ErrInvalid,
		)
	}
	pr, err := p.client.CreatePullRequest(ctx, token, owner, repo, CreatePullRequestInput{
		Title: title,
		Body:  input.Body,
		Head:  head,
		Base:  base,
	})
	if err != nil {
		return domain.PullRequest{}, err
	}
	return p.store.CreatePullRequestRecord(
		ctx,
		orgID, sessionID,
		"github", owner+"/"+repo, pr.User.Login,
		pr.Number, pr.HTMLURL, head, base, pr.Head.SHA, title,
		pr.Additions, pr.Deletions, pr.ChangedFiles,
	)
}

// ClaimPullRequest records a PR that worker tooling already opened, fetched with
// the PAT. It mirrors Service.ClaimPullRequest minus the installation auth.
func (p *PATWriteService) ClaimPullRequest(
	ctx context.Context,
	orgID, sessionID, cloneURL, token, reference string,
) (domain.PullRequest, error) {
	owner, repo, err := ownerRepoFromCloneURL(cloneURL)
	if err != nil {
		return domain.PullRequest{}, err
	}
	fullName := owner + "/" + repo
	number, err := parsePullRequestReference(reference, fullName)
	if err != nil {
		return domain.PullRequest{}, err
	}
	pr, err := p.client.GetPullRequestRecord(ctx, token, owner, repo, number)
	if err != nil {
		return domain.PullRequest{}, err
	}
	state := contract.PRState(strings.ToLower(strings.TrimSpace(pr.State)))
	switch state {
	case contract.PRStateOpen, contract.PRStateClosed:
	default:
		return domain.PullRequest{}, postgres.ErrInvalid
	}
	return p.store.ClaimPullRequestRecord(ctx, orgID, sessionID, domain.PullRequest{
		Provider:     "github",
		Repository:   fullName,
		Author:       pr.User.Login,
		Number:       pr.Number,
		URL:          pr.HTMLURL,
		Title:        pr.Title,
		State:        state,
		Draft:        pr.Draft,
		HeadSHA:      pr.Head.SHA,
		SourceBranch: pr.Head.Ref,
		TargetBranch: pr.Base.Ref,
		Additions:    pr.Additions,
		Deletions:    pr.Deletions,
		ChangedFiles: pr.ChangedFiles,
	})
}

// ownerRepoFromCloneURL parses "https://github.com/owner/repo(.git)" into owner
// and repo. The clone URL comes from the session's project record and has
// already been validated (bare https, no userinfo) before it reaches here.
func ownerRepoFromCloneURL(cloneURL string) (string, string, error) {
	trimmed := strings.TrimSuffix(strings.TrimSpace(cloneURL), ".git")
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", "", fmt.Errorf("%w: invalid repository clone url", postgres.ErrInvalid)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("%w: clone url is not owner/repo", postgres.ErrInvalid)
	}
	return parts[0], parts[1], nil
}
