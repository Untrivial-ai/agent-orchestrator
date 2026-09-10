package githubapp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/google/uuid"
)

const githubUserRefreshSkew = 5 * time.Minute

func (s *Service) StartUserAuthorization(
	ctx context.Context,
	principal domain.Principal,
) (string, time.Time, error) {
	if principal.UserID == "" {
		return "", time.Time{}, postgres.ErrForbidden
	}
	state, stateHash, err := NewState()
	if err != nil {
		return "", time.Time{}, err
	}
	verifier, challenge, err := NewPKCE()
	if err != nil {
		return "", time.Time{}, err
	}
	ciphertext, nonce, err := Encrypt(
		s.credentialKey,
		[]byte(verifier),
		githubUserOAuthAssociatedData(principal.UserID, stateHash),
	)
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().UTC().Add(s.installTTL)
	if _, err := s.store.CreateGitHubUserAuthAttempt(
		ctx,
		principal.UserID,
		stateHash,
		ciphertext,
		nonce,
		expiresAt,
	); err != nil {
		return "", time.Time{}, err
	}
	return s.client.UserAuthorizationURL(state, challenge), expiresAt, nil
}

func (s *Service) CompleteUserAuthorization(
	ctx context.Context,
	state, code string,
) (domain.GitHubUserConnection, error) {
	if strings.TrimSpace(state) == "" || strings.TrimSpace(code) == "" {
		return domain.GitHubUserConnection{}, postgres.ErrInvalid
	}
	stateHash := HashState(state)
	attempt, err := s.store.GitHubUserAuthAttempt(ctx, stateHash)
	if err != nil {
		return domain.GitHubUserConnection{}, err
	}
	verifier, err := Decrypt(
		s.credentialKey,
		attempt.CodeVerifierCiphertext,
		attempt.CodeVerifierNonce,
		githubUserOAuthAssociatedData(attempt.UserID, stateHash),
	)
	if err != nil {
		return domain.GitHubUserConnection{}, err
	}
	token, err := s.client.ExchangeUserCode(ctx, code, string(verifier))
	if err != nil {
		return domain.GitHubUserConnection{}, err
	}
	user, err := s.client.GetUser(ctx, token.Token())
	if err != nil {
		return domain.GitHubUserConnection{}, err
	}
	input, err := s.encryptGitHubUserConnection(attempt.UserID, user, token)
	if err != nil {
		return domain.GitHubUserConnection{}, err
	}
	return s.store.CompleteGitHubUserAuthorization(ctx, stateHash, input)
}

func (s *Service) UserConnection(
	ctx context.Context,
	principal domain.Principal,
) (domain.GitHubUserConnection, []domain.GitHubUserInstallation, error) {
	connection, token, err := s.githubUserAccessToken(ctx, principal.UserID)
	if err != nil {
		return domain.GitHubUserConnection{}, nil, err
	}
	installations, err := s.client.ListUserInstallations(ctx, token)
	if err != nil {
		var httpError *HTTPError
		if errors.As(err, &httpError) &&
			httpError.StatusCode == http.StatusUnauthorized {
			_ = s.store.DeleteGitHubUserConnection(ctx, principal.UserID)
			return domain.GitHubUserConnection{}, nil, postgres.ErrNotFound
		}
		return domain.GitHubUserConnection{}, nil, err
	}
	owners := make([]domain.GitHubUserInstallation, 0, len(installations))
	for _, installation := range installations {
		eligible, reason := scratchInstallationEligibility(installation)
		owners = append(owners, domain.GitHubUserInstallation{
			GitHubInstallationID: installation.ID,
			AccountLogin:         installation.Account.Login,
			AccountType:          installation.Account.Type,
			RepositorySelection:  installation.RepositorySelection,
			CanCreateRepository:  eligible,
			UnavailableReason:    reason,
		})
	}
	return connection, owners, nil
}

func (s *Service) DisconnectUser(
	ctx context.Context,
	principal domain.Principal,
) error {
	if revokeErr := s.store.RevokeGitHubRepositoryCapabilitiesForUser(
		ctx,
		principal.UserID,
		"github_user_disconnected",
	); revokeErr != nil {
		return revokeErr
	}
	_, token, err := s.githubUserAccessToken(ctx, principal.UserID)
	if err == nil {
		if revokeErr := s.client.RevokeUserAuthorization(ctx, token); revokeErr != nil {
			s.logger.Warn(
				"revoke GitHub user authorization",
				"error",
				revokeErr,
				"user_id",
				principal.UserID,
			)
		}
	}
	deleteErr := s.store.DeleteGitHubUserConnection(ctx, principal.UserID)
	if errors.Is(deleteErr, postgres.ErrNotFound) {
		return nil
	}
	return deleteErr
}

// ClaimUserInstallation attaches an existing GitHub App installation after
// proving that the connected GitHub user can administer it. GitHub does not
// invoke the App setup callback when an existing installation is configured.
func (s *Service) ClaimUserInstallation(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	githubInstallationID int64,
) (domain.GitHubInstallation, error) {
	if principal.UserID == "" || githubInstallationID <= 0 {
		return domain.GitHubInstallation{}, postgres.ErrInvalid
	}
	_, token, err := s.githubUserAccessToken(ctx, principal.UserID)
	if err != nil {
		return domain.GitHubInstallation{}, err
	}
	visible, err := s.client.UserHasInstallation(ctx, token, githubInstallationID)
	if err != nil {
		return domain.GitHubInstallation{}, err
	}
	if !visible {
		return domain.GitHubInstallation{}, postgres.ErrForbidden
	}
	providerInstallation, err := s.client.GetInstallation(ctx, githubInstallationID)
	if err != nil {
		return domain.GitHubInstallation{}, err
	}
	if !InstallationSupportsAuthorityProof(providerInstallation) {
		return domain.GitHubInstallation{}, postgres.ErrForbidden
	}
	authorized, err := s.client.UserCanAdministerInstallation(
		ctx,
		token,
		providerInstallation,
	)
	if err != nil {
		return domain.GitHubInstallation{}, err
	}
	if !authorized {
		return domain.GitHubInstallation{}, postgres.ErrForbidden
	}
	installation, err := s.store.BindGitHubInstallation(
		ctx,
		principal,
		orgID,
		toDomainInstallation(providerInstallation),
	)
	if err != nil {
		return domain.GitHubInstallation{}, err
	}
	if err := s.sync(ctx, installation); err != nil {
		return domain.GitHubInstallation{}, err
	}
	installation.SyncStatus = "ready"
	now := time.Now().UTC()
	installation.LastSyncedAt = &now
	installation.LastError = ""
	return installation, nil
}

func (s *Service) RevokeUserByGitHubID(
	ctx context.Context,
	githubUserID int64,
) error {
	// The store resolves the GitHub route and revokes every capability before
	// deleting the user token, so no worker can redeem stale authority.
	return s.store.DeleteGitHubUserConnectionByGitHubID(ctx, githubUserID)
}

func (s *Service) PrepareScratchRepository(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	githubInstallationID int64,
	displayName string,
	private bool,
) (domain.GitHubRepository, func(context.Context), error) {
	if githubInstallationID <= 0 {
		return domain.GitHubRepository{}, nil, postgres.ErrInvalid
	}
	_, token, err := s.githubUserAccessToken(ctx, principal.UserID)
	if err != nil {
		return domain.GitHubRepository{}, nil, err
	}
	visible, err := s.client.ListUserInstallations(ctx, token)
	if err != nil {
		return domain.GitHubRepository{}, nil, err
	}
	var selected Installation
	for _, installation := range visible {
		if installation.ID == githubInstallationID {
			selected = installation
			break
		}
	}
	if selected.ID == 0 {
		return domain.GitHubRepository{}, nil, postgres.ErrForbidden
	}
	providerInstallation, err := s.client.GetInstallation(ctx, githubInstallationID)
	if err != nil {
		return domain.GitHubRepository{}, nil, err
	}
	if providerInstallation.Account.ID != selected.Account.ID ||
		!strings.EqualFold(providerInstallation.Account.Login, selected.Account.Login) ||
		!strings.EqualFold(providerInstallation.Account.Type, selected.Account.Type) {
		return domain.GitHubRepository{}, nil, postgres.ErrForbidden
	}
	if eligible, _ := scratchInstallationEligibility(providerInstallation); !eligible {
		return domain.GitHubRepository{}, nil, postgres.ErrForbidden
	}
	bound, err := s.store.BindGitHubInstallation(
		ctx,
		principal,
		orgID,
		toDomainInstallation(providerInstallation),
	)
	if err != nil {
		return domain.GitHubRepository{}, nil, err
	}
	if err := s.sync(ctx, bound); err != nil {
		return domain.GitHubRepository{}, nil, err
	}
	repository, err := s.createScratchRepository(
		ctx,
		token,
		providerInstallation,
		displayName,
		private,
	)
	if err != nil {
		return domain.GitHubRepository{}, nil, err
	}
	cleanup := func(cleanupCtx context.Context) {
		if err := s.client.DeleteRepositoryAsUser(
			cleanupCtx,
			token,
			repository.Owner.Login,
			repository.Name,
		); err != nil {
			s.logger.Warn(
				"roll back GitHub scratch repository",
				"error",
				err,
				"repository",
				repository.FullName,
			)
			return
		}
		_ = s.sync(cleanupCtx, bound)
	}
	if err := s.sync(ctx, bound); err != nil {
		cleanup(context.WithoutCancel(ctx))
		return domain.GitHubRepository{}, nil, err
	}
	converted := providerRepository(repository)
	return converted, cleanup, nil
}

func (s *Service) createScratchRepository(
	ctx context.Context,
	token string,
	installation Installation,
	displayName string,
	private bool,
) (Repository, error) {
	base := scratchRepositoryName(displayName)
	if base == "" {
		base = "ao-project"
	}
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		name := base
		if attempt > 0 {
			name += "-" + strings.ToLower(uuid.NewString()[:4])
		}
		repository, err := s.client.CreateRepositoryAsUser(
			ctx,
			token,
			installation.Account.Login,
			installation.Account.Type,
			name,
			private,
		)
		if err == nil {
			return repository, nil
		}
		lastErr = err
		var httpError *HTTPError
		if !errors.As(err, &httpError) ||
			httpError.StatusCode != http.StatusUnprocessableEntity {
			return Repository{}, err
		}
	}
	return Repository{}, lastErr
}

func (s *Service) githubUserAccessToken(
	ctx context.Context,
	userID string,
) (domain.GitHubUserConnection, string, error) {
	s.userTokenMu.Lock()
	defer s.userTokenMu.Unlock()

	connection, err := s.store.GitHubUserConnection(ctx, userID)
	if err != nil {
		return domain.GitHubUserConnection{}, "", err
	}
	accessToken, err := Decrypt(
		s.credentialKey,
		connection.AccessTokenCiphertext,
		connection.AccessTokenNonce,
		githubUserTokenAssociatedData(userID, connection.GitHubUserID, "access"),
	)
	if err != nil {
		return domain.GitHubUserConnection{}, "", err
	}
	now := time.Now().UTC()
	if connection.AccessTokenExpiresAt == nil ||
		connection.AccessTokenExpiresAt.After(now.Add(githubUserRefreshSkew)) {
		return connection, string(accessToken), nil
	}
	if len(connection.RefreshTokenCiphertext) == 0 ||
		connection.RefreshTokenExpiresAt == nil ||
		!connection.RefreshTokenExpiresAt.After(now) {
		_ = s.store.DeleteGitHubUserConnection(ctx, userID)
		return domain.GitHubUserConnection{}, "", postgres.ErrNotFound
	}
	refreshToken, err := Decrypt(
		s.credentialKey,
		connection.RefreshTokenCiphertext,
		connection.RefreshTokenNonce,
		githubUserTokenAssociatedData(userID, connection.GitHubUserID, "refresh"),
	)
	if err != nil {
		return domain.GitHubUserConnection{}, "", err
	}
	rotated, err := s.client.RefreshUserAccessToken(ctx, string(refreshToken))
	if err != nil {
		_ = s.store.DeleteGitHubUserConnection(ctx, userID)
		return domain.GitHubUserConnection{}, "", postgres.ErrNotFound
	}
	user, err := s.client.GetUser(ctx, rotated.Token())
	if err != nil || user.ID != connection.GitHubUserID {
		_ = s.store.DeleteGitHubUserConnection(ctx, userID)
		return domain.GitHubUserConnection{}, "", postgres.ErrNotFound
	}
	input, err := s.encryptGitHubUserConnection(userID, user, rotated)
	if err != nil {
		return domain.GitHubUserConnection{}, "", err
	}
	input.ExpectedUpdatedAt = connection.UpdatedAt
	updated, err := s.store.UpdateGitHubUserConnection(ctx, userID, input)
	if err != nil {
		return domain.GitHubUserConnection{}, "", err
	}
	return updated, rotated.Token(), nil
}

func (s *Service) encryptGitHubUserConnection(
	userID string,
	user User,
	token UserAccessToken,
) (postgres.GitHubUserConnectionInput, error) {
	accessCiphertext, accessNonce, err := Encrypt(
		s.credentialKey,
		[]byte(token.Token()),
		githubUserTokenAssociatedData(userID, user.ID, "access"),
	)
	if err != nil {
		return postgres.GitHubUserConnectionInput{}, err
	}
	input := postgres.GitHubUserConnectionInput{
		GitHubUserID:          user.ID,
		GitHubLogin:           strings.TrimSpace(user.Login),
		GitHubAvatarURL:       strings.TrimSpace(user.AvatarURL),
		AccessTokenCiphertext: accessCiphertext,
		AccessTokenNonce:      accessNonce,
		AccessTokenExpiresAt:  token.ExpiresAt,
	}
	if refreshToken := strings.TrimSpace(token.RefreshToken()); refreshToken != "" {
		ciphertext, nonce, err := Encrypt(
			s.credentialKey,
			[]byte(refreshToken),
			githubUserTokenAssociatedData(userID, user.ID, "refresh"),
		)
		if err != nil {
			return postgres.GitHubUserConnectionInput{}, err
		}
		input.RefreshTokenCiphertext = ciphertext
		input.RefreshTokenNonce = nonce
		input.RefreshTokenExpiresAt = token.RefreshExpiresAt
	}
	return input, nil
}

func scratchInstallationEligibility(installation Installation) (bool, string) {
	if !strings.EqualFold(installation.RepositorySelection, "all") {
		return false, "Configure the GitHub App for all repositories first."
	}
	if !strings.EqualFold(installation.Permissions["administration"], "write") {
		return false, "Repository administration write access is required."
	}
	contents := installation.Permissions["contents"]
	if !strings.EqualFold(contents, "read") &&
		!strings.EqualFold(contents, "write") {
		return false, "Approve the GitHub App's Repository contents permission for this installation."
	}
	return true, ""
}

func scratchRepositoryName(displayName string) string {
	value := strings.ToLower(strings.TrimSpace(displayName))
	var builder strings.Builder
	lastDash := false
	for _, char := range value {
		valid := char >= 'a' && char <= 'z' || char >= '0' && char <= '9'
		switch {
		case valid:
			builder.WriteRune(char)
			lastDash = false
		case !lastDash:
			builder.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(builder.String(), "-")
	if len(result) > 80 {
		result = strings.Trim(result[:80], "-")
	}
	return result
}

func providerRepository(repository Repository) domain.GitHubRepository {
	updatedAt := repository.UpdatedAt
	return domain.GitHubRepository{
		GitHubRepositoryID: repository.ID,
		GitHubOwnerID:      repository.Owner.ID,
		Name:               repository.Name,
		FullName:           repository.FullName,
		HTMLURL:            repository.HTMLURL,
		CloneURL:           repository.CloneURL,
		SSHURL:             repository.SSHURL,
		DefaultBranch:      repository.DefaultBranch,
		Visibility:         repository.Visibility,
		IsPrivate:          repository.Private,
		IsArchived:         repository.Archived,
		IsDisabled:         repository.Disabled,
		GitHubUpdatedAt:    &updatedAt,
	}
}

func githubUserOAuthAssociatedData(userID string, stateHash []byte) []byte {
	return []byte(
		"github-user-oauth:" + userID + ":" +
			base64.RawURLEncoding.EncodeToString(stateHash),
	)
}

func githubUserTokenAssociatedData(
	userID string,
	githubUserID int64,
	kind string,
) []byte {
	return []byte(
		"github-user-token:" + userID + ":" +
			strconv.FormatInt(githubUserID, 10) + ":" + kind,
	)
}

var _ fmt.Stringer = UserAccessToken{}
