package githubapp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

// repositoryVisibilityAttempts and repositoryVisibilityDelay bound how long
// PrepareScratchCapability waits for a just-created repository to appear in
// GitHub's own installation-repository listing before giving up. 5 attempts
// at 500ms is a ~2s worst case — enough to absorb GitHub's typical
// read-after-write lag without making a normal (already-consistent) request
// noticeably slower, since the common case resolves on the first attempt.
// repositoryVisibilityDelay is a var, not a const, so tests can shrink it and
// exercise the retry loop without actually sleeping through it.
const repositoryVisibilityAttempts = 5

var repositoryVisibilityDelay = 500 * time.Millisecond

// waitForRepositoryVisible polls the installation's repository listing until
// repositoryID appears in it, or the retry budget is spent.
func (s *Service) waitForRepositoryVisible(
	ctx context.Context,
	installationID, repositoryID int64,
) error {
	var lastErr error
	for attempt := 0; attempt < repositoryVisibilityAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(repositoryVisibilityDelay):
			}
		}
		repositories, err := s.client.ListRepositories(ctx, installationID)
		if err != nil {
			lastErr = err
			continue
		}
		for _, repository := range repositories {
			if repository.ID == repositoryID {
				return nil
			}
		}
		lastErr = fmt.Errorf(
			"repository %d not yet visible on installation %d",
			repositoryID, installationID,
		)
	}
	return lastErr
}

type ScratchCapabilityGrant struct {
	Capability string
	Authority  domain.GitHubRepositoryCapability
}

func (s *Service) PrepareScratchCapability(
	ctx context.Context,
	principal domain.Principal,
	orgID, idempotencyKey, targetEnvironment string,
	githubInstallationID int64,
	displayName string,
	private bool,
	requestBinding []byte,
) (ScratchCapabilityGrant, error) {
	if !validCapabilityEnvironment(targetEnvironment) ||
		githubInstallationID <= 0 ||
		strings.TrimSpace(idempotencyKey) == "" {
		return ScratchCapabilityGrant{}, postgres.ErrInvalid
	}
	requestPayload, err := json.Marshal(struct {
		InstallationID int64    `json:"installationId"`
		DisplayName    string   `json:"displayName"`
		Private        bool     `json:"private"`
		Target         string   `json:"target"`
		BindingHash    [32]byte `json:"bindingHash"`
	}{
		InstallationID: githubInstallationID,
		DisplayName:    strings.TrimSpace(displayName),
		Private:        private,
		Target:         targetEnvironment,
		BindingHash:    sha256.Sum256(requestBinding),
	})
	if err != nil {
		return ScratchCapabilityGrant{}, err
	}
	requestHash := sha256.Sum256(requestPayload)
	reservation, _, err := s.store.ReserveGitHubRepositoryCapability(
		ctx,
		principal,
		orgID,
		targetEnvironment,
		idempotencyKey,
		requestHash[:],
		githubInstallationID,
	)
	if err != nil {
		return ScratchCapabilityGrant{}, err
	}
	if reservation.Status == "active" {
		plaintext, err := Decrypt(
			s.credentialKey,
			reservation.CapabilityCiphertext,
			reservation.CapabilityNonce,
			productionCapabilityAssociatedData(reservation),
		)
		if err != nil {
			return ScratchCapabilityGrant{}, err
		}
		defer clear(plaintext)
		return ScratchCapabilityGrant{
			Capability: string(plaintext),
			Authority:  reservation,
		}, nil
	}

	_, token, err := s.githubUserAccessToken(ctx, principal.UserID)
	if err != nil {
		return ScratchCapabilityGrant{}, err
	}
	visible, err := s.client.ListUserInstallations(ctx, token)
	if err != nil {
		return ScratchCapabilityGrant{}, err
	}
	var selected Installation
	for _, installation := range visible {
		if installation.ID == githubInstallationID {
			selected = installation
			break
		}
	}
	if selected.ID == 0 {
		return ScratchCapabilityGrant{}, postgres.ErrForbidden
	}
	providerInstallation, err := s.client.GetInstallation(ctx, githubInstallationID)
	if err != nil {
		return ScratchCapabilityGrant{}, err
	}
	if providerInstallation.Account.ID != selected.Account.ID ||
		!strings.EqualFold(providerInstallation.Account.Login, selected.Account.Login) ||
		!strings.EqualFold(providerInstallation.Account.Type, selected.Account.Type) {
		return ScratchCapabilityGrant{}, postgres.ErrForbidden
	}
	if eligible, _ := scratchInstallationEligibility(providerInstallation); !eligible {
		return ScratchCapabilityGrant{}, postgres.ErrForbidden
	}
	bound, err := s.store.BindGitHubInstallation(
		ctx,
		principal,
		orgID,
		toDomainInstallation(providerInstallation),
	)
	if err != nil {
		return ScratchCapabilityGrant{}, err
	}
	if err := s.sync(ctx, bound); err != nil {
		return ScratchCapabilityGrant{}, err
	}

	name := scratchRepositoryName(displayName)
	if name == "" {
		name = "ao-project"
	}
	name = strings.Trim(name+"-"+strings.ToLower(reservation.ID[:8]), "-")
	repository, err := s.client.GetRepositoryAsUser(
		ctx,
		token,
		providerInstallation.Account.Login,
		name,
	)
	if err != nil {
		var httpError *HTTPError
		if !errors.As(err, &httpError) || httpError.StatusCode != http.StatusNotFound {
			return ScratchCapabilityGrant{}, err
		}
		repository, err = s.client.CreateRepositoryAsUser(
			ctx,
			token,
			providerInstallation.Account.Login,
			providerInstallation.Account.Type,
			name,
			private,
		)
		if err != nil {
			// A concurrent retry may have won the create race.
			if errors.As(err, &httpError) &&
				httpError.StatusCode == http.StatusUnprocessableEntity {
				repository, err = s.client.GetRepositoryAsUser(
					ctx,
					token,
					providerInstallation.Account.Login,
					name,
				)
			}
			if err != nil {
				return ScratchCapabilityGrant{}, err
			}
		}
	}
	if repository.Owner.ID != providerInstallation.Account.ID ||
		!strings.EqualFold(repository.Owner.Login, providerInstallation.Account.Login) ||
		repository.Name != name {
		return ScratchCapabilityGrant{}, postgres.ErrForbidden
	}
	// GitHub's repository-creation and installation-listing endpoints are not
	// strictly read-after-write consistent: a repository just created via
	// CreateRepositoryAsUser can take a moment before it appears in
	// ListRepositories for the installation. sync() alone can race that gap
	// and silently reconcile a repository set that omits the one just
	// created, leaving no grant row for a repository that verifiably exists.
	if err := s.waitForRepositoryVisible(
		ctx, providerInstallation.ID, repository.ID,
	); err != nil {
		return ScratchCapabilityGrant{}, fmt.Errorf(
			"repository created but not yet visible to sync: %w", err,
		)
	}
	if err := s.sync(ctx, bound); err != nil {
		return ScratchCapabilityGrant{}, err
	}

	capability, capabilityHash, err := NewState()
	if err != nil {
		return ScratchCapabilityGrant{}, err
	}
	converted := providerRepository(repository)
	owner, _, ok := strings.Cut(converted.FullName, "/")
	if !ok || owner == "" {
		return ScratchCapabilityGrant{}, postgres.ErrInvalid
	}
	reservation.GitHubRepositoryID = converted.GitHubRepositoryID
	reservation.RepositoryOwner = owner
	reservation.RepositoryName = converted.Name
	reservation.CapabilityHash = capabilityHash
	ciphertext, nonce, err := Encrypt(
		s.credentialKey,
		[]byte(capability),
		productionCapabilityAssociatedData(reservation),
	)
	if err != nil {
		return ScratchCapabilityGrant{}, err
	}
	activated, err := s.store.ActivateGitHubRepositoryCapability(
		ctx,
		principal,
		orgID,
		reservation.ID,
		converted,
		capabilityHash,
		ciphertext,
		nonce,
	)
	if errors.Is(err, postgres.ErrConflict) {
		winner, _, reloadErr := s.store.ReserveGitHubRepositoryCapability(
			ctx,
			principal,
			orgID,
			targetEnvironment,
			idempotencyKey,
			requestHash[:],
			githubInstallationID,
		)
		if reloadErr != nil {
			return ScratchCapabilityGrant{}, reloadErr
		}
		if winner.Status != "active" {
			return ScratchCapabilityGrant{}, err
		}
		plaintext, decryptErr := Decrypt(
			s.credentialKey,
			winner.CapabilityCiphertext,
			winner.CapabilityNonce,
			productionCapabilityAssociatedData(winner),
		)
		if decryptErr != nil {
			return ScratchCapabilityGrant{}, decryptErr
		}
		defer clear(plaintext)
		return ScratchCapabilityGrant{
			Capability: string(plaintext),
			Authority:  winner,
		}, nil
	}
	if err != nil {
		return ScratchCapabilityGrant{}, err
	}
	return ScratchCapabilityGrant{Capability: capability, Authority: activated}, nil
}

func (s *Service) ValidateRepositoryCapability(
	ctx context.Context,
	capability, targetEnvironment string,
	githubInstallationID, githubRepositoryID int64,
	userExternalID string,
) (domain.GitHubRepositoryCapability, error) {
	if strings.TrimSpace(capability) == "" ||
		!validCapabilityEnvironment(targetEnvironment) {
		return domain.GitHubRepositoryCapability{}, postgres.ErrForbidden
	}
	authority, err := s.store.GitHubRepositoryCapability(
		ctx,
		HashState(capability),
		targetEnvironment,
	)
	if err != nil {
		return domain.GitHubRepositoryCapability{}, err
	}
	if authority.GitHubInstallationID != githubInstallationID ||
		authority.GitHubRepositoryID != githubRepositoryID ||
		authority.UserExternalID != userExternalID ||
		authority.TargetEnvironment != targetEnvironment ||
		authority.GitHubUserID <= 0 ||
		!validGitHubCloneIdentity(
			authority.Repository.CloneURL,
			authority.Repository.FullName,
		) {
		return domain.GitHubRepositoryCapability{}, postgres.ErrForbidden
	}
	return authority, nil
}

func (s *Service) RedeemRepositoryCapability(
	ctx context.Context,
	capability, targetEnvironment string,
	githubInstallationID, githubRepositoryID int64,
	userExternalID string,
) (CheckoutGrant, error) {
	authority, err := s.ValidateRepositoryCapability(
		ctx,
		capability,
		targetEnvironment,
		githubInstallationID,
		githubRepositoryID,
		userExternalID,
	)
	if err != nil {
		return CheckoutGrant{}, err
	}
	access, err := s.client.repositoryToken(
		ctx,
		authority.GitHubInstallationID,
		authority.GitHubRepositoryID,
	)
	if err != nil {
		return CheckoutGrant{}, err
	}
	if !access.ExpiresAt.After(time.Now().UTC()) ||
		access.ExpiresAt.After(time.Now().UTC().Add(2*time.Hour)) {
		return CheckoutGrant{}, errors.New("GitHub returned an invalid installation token lifetime")
	}
	return CheckoutGrant{
		CloneURL:  authority.Repository.CloneURL,
		Token:     access.Token,
		ExpiresAt: access.ExpiresAt,
	}, nil
}

func (s *Service) RevokeScratchCapability(
	ctx context.Context,
	principal domain.Principal,
	orgID, capability string,
) error {
	hash := HashState(capability)
	revoked, err := s.store.RevokeGitHubRepositoryCapability(
		ctx,
		principal,
		orgID,
		hash,
		"compensated",
	)
	if errors.Is(err, postgres.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	_, token, tokenErr := s.githubUserAccessToken(ctx, principal.UserID)
	if tokenErr != nil {
		return tokenErr
	}
	if err := s.client.DeleteRepositoryAsUser(
		ctx,
		token,
		revoked.RepositoryOwner,
		revoked.RepositoryName,
	); err != nil {
		return err
	}
	return nil
}

func productionCapabilityAssociatedData(
	capability domain.GitHubRepositoryCapability,
) []byte {
	return []byte(strings.Join([]string{
		"github-production-capability",
		capability.ID,
		capability.OrgID,
		capability.UserID,
		strconv.FormatInt(capability.GitHubUserID, 10),
		strconv.FormatInt(capability.GitHubInstallationID, 10),
		strconv.FormatInt(capability.GitHubRepositoryID, 10),
		capability.TargetEnvironment,
	}, ":"))
}
