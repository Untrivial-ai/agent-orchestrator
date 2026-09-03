package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/providersecret"
	"github.com/google/uuid"
)

var ErrNotFound = errors.New("provider or model not found")
var ErrDisabled = errors.New("provider or model is disabled")
var ErrSecretMissing = errors.New("provider credential is not configured")

type Store interface {
	ListProviders(context.Context) ([]domain.Provider, error)
	GetProvider(context.Context, domain.ProviderID) (domain.Provider, bool, error)
	PutProvider(context.Context, domain.Provider) error
	PutProviderSecret(context.Context, string, []byte, time.Time) error
	GetProviderSecret(context.Context, string) ([]byte, bool, error)
	DeleteProviderSecret(context.Context, string) error
	ListProviderModels(context.Context, domain.ProviderID) ([]domain.ProviderModel, error)
	GetProviderModel(context.Context, domain.ProviderModelID) (domain.ProviderModel, bool, error)
	PutProviderModel(context.Context, domain.ProviderModel) error
	CreateProviderAudit(context.Context, domain.ProviderAudit) error
}

type Service struct {
	store     Store
	protector providersecret.Protector
	client    *http.Client
	now       func() time.Time
	newID     func() string
}

func New(store Store, protector providersecret.Protector, client *http.Client) *Service {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &Service{store: store, protector: protector, client: client, now: func() time.Time { return time.Now().UTC() }, newID: uuid.NewString}
}

type ProviderInput struct {
	DisplayName  string
	APIProtocol  domain.APIProtocol
	BaseURL      string
	Enabled      bool
	APIKey       *string
	DeleteAPIKey bool
}
type ModelInput struct {
	DisplayName, ModelName string
	Enabled                bool
	SortOrder              int
}

func validateProvider(in ProviderInput) error {
	if strings.TrimSpace(in.DisplayName) == "" {
		return errors.New("display name is required")
	}
	if in.APIProtocol != domain.APIProtocolAnthropicCompatible && in.APIProtocol != domain.APIProtocolOpenAICompatible {
		return errors.New("unsupported API protocol")
	}
	u, e := url.Parse(strings.TrimSpace(in.BaseURL))
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("base URL must be an HTTPS URL without credentials")
	}
	return nil
}
func (s *Service) List(ctx context.Context) ([]domain.Provider, error) {
	return s.store.ListProviders(ctx)
}
func (s *Service) Get(ctx context.Context, id domain.ProviderID) (domain.Provider, []domain.ProviderModel, error) {
	p, ok, e := s.store.GetProvider(ctx, id)
	if e != nil {
		return p, nil, e
	}
	if !ok {
		return p, nil, ErrNotFound
	}
	m, e := s.store.ListProviderModels(ctx, id)
	return p, m, e
}
func (s *Service) Create(ctx context.Context, in ProviderInput) (domain.Provider, error) {
	if e := validateProvider(in); e != nil {
		return domain.Provider{}, e
	}
	now := s.now()
	id := domain.ProviderID(s.newID())
	p := domain.Provider{ID: id, DisplayName: strings.TrimSpace(in.DisplayName), APIProtocol: in.APIProtocol, BaseURL: strings.TrimRight(strings.TrimSpace(in.BaseURL), "/"), SecretRef: "provider:" + string(id), Enabled: in.Enabled, CreatedAt: now, UpdatedAt: now}
	if e := s.store.PutProvider(ctx, p); e != nil {
		return domain.Provider{}, e
	}
	if e := s.applySecret(ctx, p, in.APIKey, in.DeleteAPIKey); e != nil {
		return domain.Provider{}, e
	}
	s.audit(ctx, id, "PROVIDER_CREATED", "")
	p, _, e := s.store.GetProvider(ctx, id)
	return p, e
}
func (s *Service) Update(ctx context.Context, id domain.ProviderID, in ProviderInput) (domain.Provider, error) {
	old, ok, e := s.store.GetProvider(ctx, id)
	if e != nil {
		return old, e
	}
	if !ok {
		return old, ErrNotFound
	}
	if e = validateProvider(in); e != nil {
		return old, e
	}
	wasEnabled := old.Enabled
	old.DisplayName = strings.TrimSpace(in.DisplayName)
	old.APIProtocol = in.APIProtocol
	old.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	old.Enabled = in.Enabled
	old.UpdatedAt = s.now()
	if e = s.store.PutProvider(ctx, old); e != nil {
		return old, e
	}
	if e = s.applySecret(ctx, old, in.APIKey, in.DeleteAPIKey); e != nil {
		return old, e
	}
	action := "PROVIDER_UPDATED"
	if wasEnabled != old.Enabled {
		if old.Enabled {
			action = "PROVIDER_ENABLED"
		} else {
			action = "PROVIDER_DISABLED"
		}
	}
	s.audit(ctx, id, action, "")
	old, _, e = s.store.GetProvider(ctx, id)
	return old, e
}

func (s *Service) Disable(ctx context.Context, id domain.ProviderID) (domain.Provider, error) {
	p, ok, err := s.store.GetProvider(ctx, id)
	if err != nil {
		return p, err
	}
	if !ok {
		return p, ErrNotFound
	}
	return s.Update(ctx, id, ProviderInput{DisplayName: p.DisplayName, APIProtocol: p.APIProtocol, BaseURL: p.BaseURL, Enabled: false})
}
func (s *Service) applySecret(ctx context.Context, p domain.Provider, key *string, del bool) error {
	if del {
		if err := s.store.DeleteProviderSecret(ctx, p.SecretRef); err != nil {
			return err
		}
		s.audit(ctx, p.ID, "SECRET_DELETED", "")
		return nil
	}
	if key == nil {
		return nil
	}
	v := strings.TrimSpace(*key)
	if v == "" || v == "********" {
		return nil
	}
	cipher, e := s.protector.Protect([]byte(v))
	if e != nil {
		return e
	}
	if err := s.store.PutProviderSecret(ctx, p.SecretRef, cipher, s.now()); err != nil {
		return err
	}
	action := "SECRET_SET"
	if p.SecretConfigured {
		action = "SECRET_REPLACED"
	}
	s.audit(ctx, p.ID, action, "")
	return nil
}
func (s *Service) PutModel(ctx context.Context, providerID domain.ProviderID, id domain.ProviderModelID, in ModelInput) (domain.ProviderModel, error) {
	if strings.TrimSpace(in.DisplayName) == "" || strings.TrimSpace(in.ModelName) == "" {
		return domain.ProviderModel{}, errors.New("model display name and model name are required")
	}
	if _, ok, e := s.store.GetProvider(ctx, providerID); e != nil || !ok {
		if e != nil {
			return domain.ProviderModel{}, e
		}
		return domain.ProviderModel{}, ErrNotFound
	}
	now := s.now()
	m := domain.ProviderModel{ID: id, ProviderID: providerID, DisplayName: strings.TrimSpace(in.DisplayName), ModelName: strings.TrimSpace(in.ModelName), Enabled: in.Enabled, SortOrder: in.SortOrder, CreatedAt: now, UpdatedAt: now}
	if id == "" {
		m.ID = domain.ProviderModelID(s.newID())
	} else {
		old, ok, getErr := s.store.GetProviderModel(ctx, id)
		if getErr != nil {
			return domain.ProviderModel{}, getErr
		}
		if !ok || old.ProviderID != providerID {
			return domain.ProviderModel{}, ErrNotFound
		}
		m.CreatedAt = old.CreatedAt
	}
	if e := s.store.PutProviderModel(ctx, m); e != nil {
		return m, e
	}
	action := "MODEL_CREATED"
	if id != "" {
		action = "MODEL_UPDATED"
	}
	if !m.Enabled {
		action = "MODEL_DISABLED"
	}
	s.audit(ctx, providerID, action, string(m.ID))
	return m, nil
}

func (s *Service) DisableModel(ctx context.Context, providerID domain.ProviderID, id domain.ProviderModelID) (domain.ProviderModel, error) {
	m, ok, err := s.store.GetProviderModel(ctx, id)
	if err != nil {
		return m, err
	}
	if !ok || m.ProviderID != providerID {
		return m, ErrNotFound
	}
	return s.PutModel(ctx, providerID, id, ModelInput{DisplayName: m.DisplayName, ModelName: m.ModelName, Enabled: false, SortOrder: m.SortOrder})
}

type RuntimeConfig struct {
	ProviderID                                       domain.ProviderID
	ProviderModelID                                  domain.ProviderModelID
	ProviderDisplayName, ModelDisplayName, ModelName string
	APIProtocol                                      domain.APIProtocol
	BaseURL                                          string
	APIKey                                           string
}

func (s *Service) ResolveRuntimeProvider(ctx context.Context, pid domain.ProviderID, mid domain.ProviderModelID) (RuntimeConfig, error) {
	return s.resolveRuntimeProvider(ctx, pid, mid, true)
}

// ResolveRuntimeProviderForResume permits a historical session to continue
// after its Provider or model is disabled for new selection. Removing the
// credential remains an explicit hard stop.
func (s *Service) ResolveRuntimeProviderForResume(ctx context.Context, pid domain.ProviderID, mid domain.ProviderModelID) (RuntimeConfig, error) {
	return s.resolveRuntimeProvider(ctx, pid, mid, false)
}

func (s *Service) resolveRuntimeProvider(ctx context.Context, pid domain.ProviderID, mid domain.ProviderModelID, requireEnabled bool) (RuntimeConfig, error) {
	p, ok, e := s.store.GetProvider(ctx, pid)
	if e != nil {
		return RuntimeConfig{}, e
	}
	if !ok {
		return RuntimeConfig{}, ErrNotFound
	}
	m, mok, e := s.store.GetProviderModel(ctx, mid)
	if e != nil {
		return RuntimeConfig{}, e
	}
	if !mok || m.ProviderID != pid {
		return RuntimeConfig{}, ErrNotFound
	}
	if requireEnabled && (!p.Enabled || !m.Enabled) {
		return RuntimeConfig{}, ErrDisabled
	}
	cipher, ok, e := s.store.GetProviderSecret(ctx, p.SecretRef)
	if e != nil {
		return RuntimeConfig{}, e
	}
	if !ok {
		return RuntimeConfig{}, ErrSecretMissing
	}
	plain, e := s.protector.Unprotect(cipher)
	if e != nil {
		return RuntimeConfig{}, e
	}
	return RuntimeConfig{ProviderID: p.ID, ProviderModelID: m.ID, ProviderDisplayName: p.DisplayName, ModelDisplayName: m.DisplayName, ModelName: m.ModelName, APIProtocol: p.APIProtocol, BaseURL: p.BaseURL, APIKey: string(plain)}, nil
}
func (r RuntimeConfig) ClaudeEnvironment() (map[string]string, error) {
	if r.APIProtocol != domain.APIProtocolAnthropicCompatible {
		return nil, errors.New("Claude Code requires an anthropic-compatible provider")
	}
	return map[string]string{
		"ANTHROPIC_BASE_URL": r.BaseURL, "ANTHROPIC_AUTH_TOKEN": r.APIKey, "ANTHROPIC_API_KEY": "", "ANTHROPIC_MODEL": r.ModelName,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": r.ModelName, "ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME": r.ModelName,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": r.ModelName, "ANTHROPIC_DEFAULT_SONNET_MODEL_NAME": r.ModelName,
		"ANTHROPIC_DEFAULT_OPUS_MODEL": r.ModelName, "ANTHROPIC_DEFAULT_OPUS_MODEL_NAME": r.ModelName,
		"ANTHROPIC_DEFAULT_FABLE_MODEL": r.ModelName, "ANTHROPIC_DEFAULT_FABLE_MODEL_NAME": r.ModelName,
		"CLAUDE_CODE_SUBAGENT_MODEL": r.ModelName, "CLAUDE_CODE_EFFORT_LEVEL": "high",
	}, nil
}

type TestResult struct {
	OK        bool   `json:"ok"`
	Category  string `json:"category"`
	Message   string `json:"message"`
	LatencyMS int64  `json:"latencyMs"`
}

func (s *Service) TestConnection(ctx context.Context, pid domain.ProviderID, mid domain.ProviderModelID) (TestResult, error) {
	start := time.Now()
	r, e := s.ResolveRuntimeProvider(ctx, pid, mid)
	if e != nil {
		s.audit(ctx, pid, "CONNECTION_TEST_FAILED", string(mid))
		return TestResult{}, e
	}
	if r.APIProtocol != domain.APIProtocolAnthropicCompatible {
		result := TestResult{Category: "unsupported", Message: "Connection testing for this protocol is not implemented"}
		s.audit(ctx, pid, "CONNECTION_TEST_FAILED", string(mid))
		return result, nil
	}
	body, _ := json.Marshal(map[string]any{"model": r.ModelName, "max_tokens": 1, "messages": []map[string]string{{"role": "user", "content": "ping"}}})
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, r.BaseURL+"/v1/messages", bytes.NewReader(body))
	if e != nil {
		return TestResult{}, e
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("x-api-key", r.APIKey)
	resp, e := s.client.Do(req)
	lat := time.Since(start).Milliseconds()
	if e != nil {
		result := TestResult{Category: "network", Message: "Provider connection failed", LatencyMS: lat}
		s.audit(ctx, pid, "CONNECTION_TEST_FAILED", string(mid))
		return result, nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		result := TestResult{OK: true, Category: "ok", Message: "Connection succeeded", LatencyMS: lat}
		s.audit(ctx, pid, "CONNECTION_TEST_SUCCEEDED", string(mid))
		return result, nil
	}
	cat := "provider"
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		cat = "authentication"
	}
	result := TestResult{Category: cat, Message: fmt.Sprintf("Provider returned HTTP %d", resp.StatusCode), LatencyMS: lat}
	s.audit(ctx, pid, "CONNECTION_TEST_FAILED", string(mid))
	return result, nil
}
func (s *Service) audit(ctx context.Context, pid domain.ProviderID, action, detail string) {
	_ = s.store.CreateProviderAudit(ctx, domain.ProviderAudit{ID: s.newID(), ProviderID: pid, Action: action, Detail: detail, CreatedAt: s.now()})
}
