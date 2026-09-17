package postgres

import (
	"context"
	"encoding/json"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

// ListUserProviderConnections lists the calling principal's own personal
// coding-agent connections — the ones usable in every org they belong to,
// not just one.
func (s *Store) ListUserProviderConnections(
	ctx context.Context,
	principal domain.Principal,
) ([]domain.UserProviderConnection, error) {
	connections := make([]domain.UserProviderConnection, 0)
	err := s.withUser(ctx, principal.UserID, func(tx pgx.Tx) error {
		rows, err := tx.Query(
			ctx,
			`SELECT id, user_id, provider, label, config, validation_state,
				validated_at, created_at, updated_at
			FROM ao_user_provider_connections
			WHERE user_id = $1
			ORDER BY provider, label`,
			principal.UserID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var connection domain.UserProviderConnection
			if err := rows.Scan(
				&connection.ID,
				&connection.UserID,
				&connection.Provider,
				&connection.Label,
				&connection.Config,
				&connection.ValidationState,
				&connection.ValidatedAt,
				&connection.CreatedAt,
				&connection.UpdatedAt,
			); err != nil {
				return err
			}
			connections = append(connections, connection)
		}
		return rows.Err()
	})
	return connections, err
}

// UpsertUserProviderConnection creates or replaces the calling principal's
// personal connection for one provider. Unlike the org-level equivalent,
// there is no admin gate — it's the user's own credential, they manage it
// themselves.
func (s *Store) UpsertUserProviderConnection(
	ctx context.Context,
	principal domain.Principal,
	provider, label string,
	encryptedSecret, nonce []byte,
	config json.RawMessage,
) (domain.UserProviderConnection, error) {
	var connection domain.UserProviderConnection
	err := s.withUser(ctx, principal.UserID, func(tx pgx.Tx) error {
		return tx.QueryRow(
			ctx,
			`INSERT INTO ao_user_provider_connections (
				user_id, provider, label, encrypted_secret, secret_nonce, config,
				validation_state, validated_at
			) VALUES ($1, $2, $3, $4, $5, $6, 'valid', now())
			ON CONFLICT (user_id, provider, label) DO UPDATE
			SET encrypted_secret = EXCLUDED.encrypted_secret,
				secret_nonce = EXCLUDED.secret_nonce,
				config = EXCLUDED.config,
				validation_state = 'valid',
				validated_at = now(),
				updated_at = now()
			RETURNING id, user_id, provider, label, config, validation_state,
				validated_at, created_at, updated_at`,
			principal.UserID,
			provider,
			label,
			encryptedSecret,
			nonce,
			config,
		).Scan(
			&connection.ID,
			&connection.UserID,
			&connection.Provider,
			&connection.Label,
			&connection.Config,
			&connection.ValidationState,
			&connection.ValidatedAt,
			&connection.CreatedAt,
			&connection.UpdatedAt,
		)
	})
	return connection, err
}

// DeleteUserProviderConnection removes the calling principal's own personal
// connection for one provider.
func (s *Store) DeleteUserProviderConnection(
	ctx context.Context,
	principal domain.Principal,
	provider, label string,
) error {
	return s.withUser(ctx, principal.UserID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(
			ctx,
			`DELETE FROM ao_user_provider_connections
			WHERE user_id = $1 AND provider = $2 AND label = $3`,
			principal.UserID,
			provider,
			label,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// UserAgentCredentialAvailable reports whether the given principal has a
// valid personal connection for a provider — the fallback createSession
// checks when the org itself has none, so connecting a credential once
// doesn't have to be repeated in every org.
func (s *Store) UserAgentCredentialAvailable(
	ctx context.Context,
	userID, provider string,
) (bool, error) {
	var available bool
	err := s.withUser(ctx, userID, func(tx pgx.Tx) error {
		return tx.QueryRow(
			ctx,
			`SELECT EXISTS (
				SELECT 1
				FROM ao_user_provider_connections
				WHERE user_id = $1
				  AND provider = $2
				  AND label = 'default'
				  AND validation_state = 'valid'
			)`,
			userID, provider,
		).Scan(&available)
	})
	return available, err
}
