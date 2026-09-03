package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const providerColumns = `p.id, p.display_name, p.api_protocol, p.base_url, p.secret_ref, p.enabled,
p.created_at, p.updated_at, EXISTS(SELECT 1 FROM provider_secrets s WHERE s.secret_ref = p.secret_ref)`

func scanProvider(row rowScanner) (domain.Provider, error) {
	var p domain.Provider
	if err := row.Scan(&p.ID, &p.DisplayName, &p.APIProtocol, &p.BaseURL, &p.SecretRef,
		&p.Enabled, &p.CreatedAt, &p.UpdatedAt, &p.SecretConfigured); err != nil {
		return domain.Provider{}, err
	}
	return p, nil
}

func (s *Store) ListProviders(ctx context.Context) ([]domain.Provider, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT `+providerColumns+` FROM providers p ORDER BY p.display_name, p.id`)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	defer rows.Close()
	out := make([]domain.Provider, 0)
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProvider(ctx context.Context, id domain.ProviderID) (domain.Provider, bool, error) {
	p, err := scanProvider(s.readDB.QueryRowContext(ctx, `SELECT `+providerColumns+` FROM providers p WHERE p.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Provider{}, false, nil
	}
	return p, err == nil, err
}

func (s *Store) PutProvider(ctx context.Context, p domain.Provider) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx, `INSERT INTO providers
(id,display_name,api_protocol,base_url,secret_ref,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET display_name=excluded.display_name, api_protocol=excluded.api_protocol,
base_url=excluded.base_url, enabled=excluded.enabled, updated_at=excluded.updated_at`,
		p.ID, p.DisplayName, p.APIProtocol, p.BaseURL, p.SecretRef, p.Enabled, p.CreatedAt, p.UpdatedAt)
	return err
}

func (s *Store) PutProviderSecret(ctx context.Context, ref string, ciphertext []byte, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx, `INSERT INTO provider_secrets(secret_ref,ciphertext,updated_at) VALUES(?,?,?)
ON CONFLICT(secret_ref) DO UPDATE SET ciphertext=excluded.ciphertext, updated_at=excluded.updated_at`, ref, ciphertext, at)
	return err
}

func (s *Store) GetProviderSecret(ctx context.Context, ref string) ([]byte, bool, error) {
	var b []byte
	err := s.readDB.QueryRowContext(ctx, `SELECT ciphertext FROM provider_secrets WHERE secret_ref=?`, ref).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return b, err == nil, err
}

func (s *Store) DeleteProviderSecret(ctx context.Context, ref string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx, `DELETE FROM provider_secrets WHERE secret_ref=?`, ref)
	return err
}

const providerModelColumns = `id,provider_id,display_name,model_name,enabled,sort_order,created_at,updated_at`

func scanProviderModel(row rowScanner) (domain.ProviderModel, error) {
	var m domain.ProviderModel
	err := row.Scan(&m.ID, &m.ProviderID, &m.DisplayName, &m.ModelName, &m.Enabled, &m.SortOrder, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

func (s *Store) ListProviderModels(ctx context.Context, providerID domain.ProviderID) ([]domain.ProviderModel, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT `+providerModelColumns+` FROM provider_models WHERE provider_id=? ORDER BY sort_order,display_name,id`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.ProviderModel, 0)
	for rows.Next() {
		m, e := scanProviderModel(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) GetProviderModel(ctx context.Context, id domain.ProviderModelID) (domain.ProviderModel, bool, error) {
	m, e := scanProviderModel(s.readDB.QueryRowContext(ctx, `SELECT `+providerModelColumns+` FROM provider_models WHERE id=?`, id))
	if errors.Is(e, sql.ErrNoRows) {
		return domain.ProviderModel{}, false, nil
	}
	return m, e == nil, e
}
func (s *Store) PutProviderModel(ctx context.Context, m domain.ProviderModel) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, e := s.writeDB.ExecContext(ctx, `INSERT INTO provider_models(id,provider_id,display_name,model_name,enabled,sort_order,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET display_name=excluded.display_name,model_name=excluded.model_name,enabled=excluded.enabled,sort_order=excluded.sort_order,updated_at=excluded.updated_at`, m.ID, m.ProviderID, m.DisplayName, m.ModelName, m.Enabled, m.SortOrder, m.CreatedAt, m.UpdatedAt)
	return e
}
func (s *Store) CreateProviderAudit(ctx context.Context, a domain.ProviderAudit) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, e := s.writeDB.ExecContext(ctx, `INSERT INTO provider_audits(id,provider_id,action,detail,created_at) VALUES(?,?,?,?,?)`, a.ID, a.ProviderID, a.Action, a.Detail, a.CreatedAt)
	return e
}
