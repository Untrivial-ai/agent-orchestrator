package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	providersvc "github.com/aoagents/agent-orchestrator/backend/internal/service/provider"
)

type ProviderService interface {
	List(context.Context) ([]domain.Provider, error)
	Get(context.Context, domain.ProviderID) (domain.Provider, []domain.ProviderModel, error)
	Create(context.Context, providersvc.ProviderInput) (domain.Provider, error)
	Update(context.Context, domain.ProviderID, providersvc.ProviderInput) (domain.Provider, error)
	Disable(context.Context, domain.ProviderID) (domain.Provider, error)
	PutModel(context.Context, domain.ProviderID, domain.ProviderModelID, providersvc.ModelInput) (domain.ProviderModel, error)
	DisableModel(context.Context, domain.ProviderID, domain.ProviderModelID) (domain.ProviderModel, error)
	TestConnection(context.Context, domain.ProviderID, domain.ProviderModelID) (providersvc.TestResult, error)
}

type ProvidersController struct{ Svc ProviderService }
type ProviderRequest struct {
	DisplayName  string             `json:"displayName"`
	APIProtocol  domain.APIProtocol `json:"apiProtocol"`
	BaseURL      string             `json:"baseUrl"`
	Enabled      bool               `json:"enabled"`
	APIKey       *string            `json:"apiKey,omitempty"`
	DeleteAPIKey bool               `json:"deleteApiKey,omitempty"`
}
type ProviderModelRequest struct {
	DisplayName string `json:"displayName"`
	ModelName   string `json:"modelName"`
	Enabled     bool   `json:"enabled"`
	SortOrder   int    `json:"sortOrder"`
}
type TestProviderRequest struct {
	ProviderModelID domain.ProviderModelID `json:"providerModelId"`
}

func (c *ProvidersController) Register(r chi.Router) {
	r.Get("/providers", c.list)
	r.Post("/providers", c.create)
	r.Get("/providers/{providerId}", c.get)
	r.Patch("/providers/{providerId}", c.update)
	r.Delete("/providers/{providerId}", c.disable)
	r.Post("/providers/{providerId}/test", c.test)
	r.Post("/providers/{providerId}/models", c.createModel)
	r.Patch("/providers/{providerId}/models/{modelId}", c.updateModel)
	r.Delete("/providers/{providerId}/models/{modelId}", c.disableModel)
}

type ProviderIDParam struct {
	ProviderID domain.ProviderID `path:"providerId"`
}
type ProviderModelIDParam struct {
	ModelID domain.ProviderModelID `path:"modelId"`
}
type ProvidersResponse struct {
	Providers []domain.Provider `json:"providers"`
}
type ProviderResponse struct {
	Provider domain.Provider `json:"provider"`
}
type ProviderDetailResponse struct {
	Provider domain.Provider        `json:"provider"`
	Models   []domain.ProviderModel `json:"models"`
}
type ProviderModelResponse struct {
	Model domain.ProviderModel `json:"model"`
}

func providerInput(in ProviderRequest) providersvc.ProviderInput {
	return providersvc.ProviderInput{DisplayName: in.DisplayName, APIProtocol: in.APIProtocol, BaseURL: in.BaseURL, Enabled: in.Enabled, APIKey: in.APIKey, DeleteAPIKey: in.DeleteAPIKey}
}
func modelInput(in ProviderModelRequest) providersvc.ModelInput {
	return providersvc.ModelInput{DisplayName: in.DisplayName, ModelName: in.ModelName, Enabled: in.Enabled, SortOrder: in.SortOrder}
}
func (c *ProvidersController) list(w http.ResponseWriter, r *http.Request) {
	v, e := c.Svc.List(r.Context())
	if e != nil {
		envelope.WriteError(w, r, e)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, map[string]any{"providers": v})
}
func (c *ProvidersController) get(w http.ResponseWriter, r *http.Request) {
	p, m, e := c.Svc.Get(r.Context(), domain.ProviderID(chi.URLParam(r, "providerId")))
	if e != nil {
		c.writeErr(w, r, e)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, map[string]any{"provider": p, "models": m})
}
func (c *ProvidersController) create(w http.ResponseWriter, r *http.Request) {
	var in ProviderRequest
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		envelope.WriteAPIError(w, r, 400, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	p, e := c.Svc.Create(r.Context(), providerInput(in))
	if e != nil {
		c.writeErr(w, r, e)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, map[string]any{"provider": p})
}
func (c *ProvidersController) update(w http.ResponseWriter, r *http.Request) {
	var in ProviderRequest
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		envelope.WriteAPIError(w, r, 400, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	p, e := c.Svc.Update(r.Context(), domain.ProviderID(chi.URLParam(r, "providerId")), providerInput(in))
	if e != nil {
		c.writeErr(w, r, e)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, map[string]any{"provider": p})
}
func (c *ProvidersController) disable(w http.ResponseWriter, r *http.Request) {
	p, e := c.Svc.Disable(r.Context(), domain.ProviderID(chi.URLParam(r, "providerId")))
	if e != nil {
		c.writeErr(w, r, e)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ProviderResponse{Provider: p})
}
func (c *ProvidersController) createModel(w http.ResponseWriter, r *http.Request) {
	c.putModel(w, r, "")
}
func (c *ProvidersController) updateModel(w http.ResponseWriter, r *http.Request) {
	c.putModel(w, r, domain.ProviderModelID(chi.URLParam(r, "modelId")))
}
func (c *ProvidersController) disableModel(w http.ResponseWriter, r *http.Request) {
	m, e := c.Svc.DisableModel(r.Context(), domain.ProviderID(chi.URLParam(r, "providerId")), domain.ProviderModelID(chi.URLParam(r, "modelId")))
	if e != nil {
		c.writeErr(w, r, e)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ProviderModelResponse{Model: m})
}
func (c *ProvidersController) putModel(w http.ResponseWriter, r *http.Request, id domain.ProviderModelID) {
	var in ProviderModelRequest
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		envelope.WriteAPIError(w, r, 400, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	m, e := c.Svc.PutModel(r.Context(), domain.ProviderID(chi.URLParam(r, "providerId")), id, modelInput(in))
	if e != nil {
		c.writeErr(w, r, e)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, map[string]any{"model": m})
}
func (c *ProvidersController) test(w http.ResponseWriter, r *http.Request) {
	var in TestProviderRequest
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		envelope.WriteAPIError(w, r, 400, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	v, e := c.Svc.TestConnection(r.Context(), domain.ProviderID(chi.URLParam(r, "providerId")), in.ProviderModelID)
	if e != nil {
		c.writeErr(w, r, e)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, v)
}
func (c *ProvidersController) writeErr(w http.ResponseWriter, r *http.Request, e error) {
	if errors.Is(e, providersvc.ErrNotFound) {
		envelope.WriteAPIError(w, r, 404, "not_found", "PROVIDER_NOT_FOUND", "Provider or model not found", nil)
		return
	}
	if errors.Is(e, providersvc.ErrDisabled) || errors.Is(e, providersvc.ErrSecretMissing) {
		envelope.WriteAPIError(w, r, 409, "conflict", "PROVIDER_UNAVAILABLE", e.Error(), nil)
		return
	}
	envelope.WriteAPIError(w, r, 400, "validation", "PROVIDER_INVALID", e.Error(), nil)
}
