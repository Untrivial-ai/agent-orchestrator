package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type GitHubUserConnectionInput struct {
	GitHubUserID           int64
	GitHubLogin            string
	GitHubAvatarURL        string
	AccessTokenCiphertext  []byte
	AccessTokenNonce       []byte
	AccessTokenExpiresAt   *time.Time
	RefreshTokenCiphertext []byte
	RefreshTokenNonce      []byte
	RefreshTokenExpiresAt  *time.Time
	ExpectedUpdatedAt      time.Time
}

func (s *Store) CreateGitHubUserAuthAttempt(
	ctx context.Context,
	userID string,
	stateHash, verifierCiphertext, verifierNonce []byte,
	expiresAt time.Time,
) (domain.GitHubUserAuthAttempt, error) {
	if userID == "" || len(stateHash) != 32 || len(verifierCiphertext) == 0 ||
		len(verifierNonce) != 12 || !expiresAt.After(time.Now()) {
		return domain.GitHubUserAuthAttempt{}, ErrInvalid
	}
	var attempt domain.GitHubUserAuthAttempt
	err := s.withUser(ctx, userID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(
			ctx,
			`DELETE FROM ao_github_user_auth_routes WHERE expires_at <= now()`,
		); err != nil {
			return err
		}
		if err := tx.QueryRow(
			ctx,
			`INSERT INTO ao_github_user_auth_attempts (
				user_id, state_hash, code_verifier_ciphertext,
				code_verifier_nonce, expires_at
			) VALUES ($1, $2, $3, $4, $5)
			RETURNING id, user_id, state_hash, code_verifier_ciphertext,
				code_verifier_nonce, expires_at, consumed_at, created_at`,
			userID,
			stateHash,
			verifierCiphertext,
			verifierNonce,
			expiresAt,
		).Scan(githubUserAuthAttemptTargets(&attempt)...); err != nil {
			return err
		}
		_, err := tx.Exec(
			ctx,
			`INSERT INTO ao_github_user_auth_routes (
				state_hash, attempt_id, user_id, expires_at
			) VALUES ($1, $2, $3, $4)`,
			stateHash,
			attempt.ID,
			userID,
			expiresAt,
		)
		return err
	})
	return attempt, err
}

func (s *Store) GitHubUserAuthAttempt(
	ctx context.Context,
	stateHash []byte,
) (domain.GitHubUserAuthAttempt, error) {
	route, err := s.githubUserAuthRoute(ctx, stateHash)
	if err != nil {
		return domain.GitHubUserAuthAttempt{}, err
	}
	var attempt domain.GitHubUserAuthAttempt
	err = s.withUser(ctx, route.userID, func(tx pgx.Tx) error {
		return tx.QueryRow(
			ctx,
			`SELECT id, user_id, state_hash, code_verifier_ciphertext,
				code_verifier_nonce, expires_at, consumed_at, created_at
			FROM ao_github_user_auth_attempts
			WHERE id = $1 AND user_id = $2
			  AND consumed_at IS NULL AND expires_at > now()`,
			route.attemptID,
			route.userID,
		).Scan(githubUserAuthAttemptTargets(&attempt)...)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GitHubUserAuthAttempt{}, ErrNotFound
	}
	return attempt, err
}

func (s *Store) CompleteGitHubUserAuthorization(
	ctx context.Context,
	stateHash []byte,
	input GitHubUserConnectionInput,
) (domain.GitHubUserConnection, error) {
	if len(stateHash) != 32 || !validGitHubUserConnectionInput(input) {
		return domain.GitHubUserConnection{}, ErrInvalid
	}
	route, err := s.githubUserAuthRoute(ctx, stateHash)
	if err != nil {
		return domain.GitHubUserConnection{}, err
	}
	var connection domain.GitHubUserConnection
	err = s.withUser(ctx, route.userID, func(tx pgx.Tx) error {
		var attemptUserID string
		if err := tx.QueryRow(
			ctx,
			`SELECT user_id FROM ao_github_user_auth_attempts
			WHERE id = $1 AND user_id = $2
			  AND consumed_at IS NULL AND expires_at > now()
			FOR UPDATE`,
			route.attemptID,
			route.userID,
		).Scan(&attemptUserID); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		err := tx.QueryRow(
			ctx,
			`INSERT INTO ao_github_user_connections (
				user_id, github_user_id, github_login, github_avatar_url,
				access_token_ciphertext, access_token_nonce,
				access_token_expires_at, refresh_token_ciphertext,
				refresh_token_nonce, refresh_token_expires_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (user_id) DO UPDATE
			SET github_user_id = EXCLUDED.github_user_id,
			    github_login = EXCLUDED.github_login,
			    github_avatar_url = EXCLUDED.github_avatar_url,
			    access_token_ciphertext = EXCLUDED.access_token_ciphertext,
			    access_token_nonce = EXCLUDED.access_token_nonce,
			    access_token_expires_at = EXCLUDED.access_token_expires_at,
			    refresh_token_ciphertext = EXCLUDED.refresh_token_ciphertext,
			    refresh_token_nonce = EXCLUDED.refresh_token_nonce,
			    refresh_token_expires_at = EXCLUDED.refresh_token_expires_at,
			    last_synced_at = now(), updated_at = now()
			RETURNING user_id, github_user_id, github_login, github_avatar_url,
				access_token_ciphertext, access_token_nonce,
				access_token_expires_at, refresh_token_ciphertext,
				refresh_token_nonce, refresh_token_expires_at,
				last_synced_at, created_at, updated_at`,
			route.userID,
			input.GitHubUserID,
			input.GitHubLogin,
			input.GitHubAvatarURL,
			input.AccessTokenCiphertext,
			input.AccessTokenNonce,
			input.AccessTokenExpiresAt,
			nullableBytes(input.RefreshTokenCiphertext),
			nullableBytes(input.RefreshTokenNonce),
			input.RefreshTokenExpiresAt,
		).Scan(githubUserConnectionTargets(&connection)...)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return ErrConflict
			}
			return err
		}
		// One AO user can replace their connected GitHub identity. Remove the
		// previous non-secret revocation route before installing the new one so
		// a later webhook for the old identity cannot delete the replacement.
		if _, err := tx.Exec(
			ctx,
			`DELETE FROM ao_github_user_connection_routes WHERE user_id = $1`,
			route.userID,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO ao_github_user_connection_routes (
				github_user_id, user_id
			) VALUES ($1, $2)
			ON CONFLICT (github_user_id) DO UPDATE
			SET user_id = EXCLUDED.user_id
			WHERE ao_github_user_connection_routes.user_id = EXCLUDED.user_id`,
			connection.GitHubUserID,
			route.userID,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_github_user_auth_attempts
			SET consumed_at = now()
			WHERE id = $1 AND user_id = $2`,
			route.attemptID,
			route.userID,
		); err != nil {
			return err
		}
		_, err = tx.Exec(
			ctx,
			`DELETE FROM ao_github_user_auth_routes WHERE state_hash = $1`,
			stateHash,
		)
		return err
	})
	return connection, err
}

func (s *Store) GitHubUserConnection(
	ctx context.Context,
	userID string,
) (domain.GitHubUserConnection, error) {
	var connection domain.GitHubUserConnection
	err := s.withUser(ctx, userID, func(tx pgx.Tx) error {
		return tx.QueryRow(
			ctx,
			`SELECT user_id, github_user_id, github_login, github_avatar_url,
				access_token_ciphertext, access_token_nonce,
				access_token_expires_at, refresh_token_ciphertext,
				refresh_token_nonce, refresh_token_expires_at,
				last_synced_at, created_at, updated_at
			FROM ao_github_user_connections WHERE user_id = $1`,
			userID,
		).Scan(githubUserConnectionTargets(&connection)...)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GitHubUserConnection{}, ErrNotFound
	}
	return connection, err
}

func (s *Store) UpdateGitHubUserConnection(
	ctx context.Context,
	userID string,
	input GitHubUserConnectionInput,
) (domain.GitHubUserConnection, error) {
	if userID == "" || input.ExpectedUpdatedAt.IsZero() ||
		!validGitHubUserConnectionInput(input) {
		return domain.GitHubUserConnection{}, ErrInvalid
	}
	var connection domain.GitHubUserConnection
	err := s.withUser(ctx, userID, func(tx pgx.Tx) error {
		return tx.QueryRow(
			ctx,
			`UPDATE ao_github_user_connections
			SET github_login = $2, github_avatar_url = $3,
			    access_token_ciphertext = $4, access_token_nonce = $5,
			    access_token_expires_at = $6,
			    refresh_token_ciphertext = $7, refresh_token_nonce = $8,
			    refresh_token_expires_at = $9,
			    last_synced_at = now(), updated_at = now()
			WHERE user_id = $1 AND github_user_id = $10 AND updated_at = $11
			RETURNING user_id, github_user_id, github_login, github_avatar_url,
				access_token_ciphertext, access_token_nonce,
				access_token_expires_at, refresh_token_ciphertext,
				refresh_token_nonce, refresh_token_expires_at,
				last_synced_at, created_at, updated_at`,
			userID,
			input.GitHubLogin,
			input.GitHubAvatarURL,
			input.AccessTokenCiphertext,
			input.AccessTokenNonce,
			input.AccessTokenExpiresAt,
			nullableBytes(input.RefreshTokenCiphertext),
			nullableBytes(input.RefreshTokenNonce),
			input.RefreshTokenExpiresAt,
			input.GitHubUserID,
			input.ExpectedUpdatedAt,
		).Scan(githubUserConnectionTargets(&connection)...)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GitHubUserConnection{}, ErrConflict
	}
	return connection, err
}

func (s *Store) DeleteGitHubUserConnection(ctx context.Context, userID string) error {
	var deleted bool
	err := s.withUser(ctx, userID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(
			ctx,
			`DELETE FROM ao_github_user_connections WHERE user_id = $1`,
			userID,
		)
		deleted = err == nil && tag.RowsAffected() == 1
		return err
	})
	if err != nil {
		return err
	}
	if !deleted {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteGitHubUserConnectionByGitHubID(
	ctx context.Context,
	githubUserID int64,
) error {
	if githubUserID <= 0 {
		return ErrInvalid
	}
	var userID string
	err := s.pool.QueryRow(
		ctx,
		`SELECT user_id FROM ao_github_user_connection_routes
		WHERE github_user_id = $1`,
		githubUserID,
	).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.RevokeGitHubRepositoryCapabilitiesForUser(
		ctx,
		userID,
		"github_user_authorization_revoked",
	); err != nil {
		return err
	}
	err = s.DeleteGitHubUserConnection(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

type githubUserAuthRoute struct {
	attemptID string
	userID    string
}

func (s *Store) githubUserAuthRoute(
	ctx context.Context,
	stateHash []byte,
) (githubUserAuthRoute, error) {
	if len(stateHash) != 32 {
		return githubUserAuthRoute{}, ErrInvalid
	}
	var route githubUserAuthRoute
	err := s.pool.QueryRow(
		ctx,
		`SELECT attempt_id, user_id FROM ao_github_user_auth_routes
		WHERE state_hash = $1 AND expires_at > now()`,
		stateHash,
	).Scan(&route.attemptID, &route.userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return githubUserAuthRoute{}, ErrNotFound
	}
	return route, err
}

func githubUserAuthAttemptTargets(attempt *domain.GitHubUserAuthAttempt) []any {
	return []any{
		&attempt.ID,
		&attempt.UserID,
		&attempt.StateHash,
		&attempt.CodeVerifierCiphertext,
		&attempt.CodeVerifierNonce,
		&attempt.ExpiresAt,
		&attempt.ConsumedAt,
		&attempt.CreatedAt,
	}
}

func githubUserConnectionTargets(connection *domain.GitHubUserConnection) []any {
	return []any{
		&connection.UserID,
		&connection.GitHubUserID,
		&connection.GitHubLogin,
		&connection.GitHubAvatarURL,
		&connection.AccessTokenCiphertext,
		&connection.AccessTokenNonce,
		&connection.AccessTokenExpiresAt,
		&connection.RefreshTokenCiphertext,
		&connection.RefreshTokenNonce,
		&connection.RefreshTokenExpiresAt,
		&connection.LastSyncedAt,
		&connection.CreatedAt,
		&connection.UpdatedAt,
	}
}

func validGitHubUserConnectionInput(input GitHubUserConnectionInput) bool {
	if input.GitHubUserID <= 0 || input.GitHubLogin == "" ||
		len(input.AccessTokenCiphertext) == 0 || len(input.AccessTokenNonce) != 12 {
		return false
	}
	hasRefresh := len(input.RefreshTokenCiphertext) > 0
	return hasRefresh == (len(input.RefreshTokenNonce) == 12) &&
		hasRefresh == (input.RefreshTokenExpiresAt != nil)
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
