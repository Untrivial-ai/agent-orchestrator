package controllers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	quotasvc "github.com/aoagents/agent-orchestrator/backend/internal/service/quota"
)

// QuotaService is the controller boundary for subscription quota operations.
type QuotaService interface {
	List(context.Context) ([]domain.QuotaSnapshot, error)
	Get(context.Context, domain.QuotaProviderID, domain.QuotaAccountID) (domain.QuotaSnapshot, bool, error)
	History(context.Context, domain.QuotaProviderID, domain.QuotaAccountID, time.Time, int64) ([]domain.QuotaHistoryPoint, error)
	Refresh(context.Context, domain.QuotaProviderID, domain.QuotaAccountID) (domain.QuotaSnapshot, error)
	RefreshAll(context.Context) ([]domain.QuotaSnapshot, error)
	Alerts(context.Context, time.Time, int64) ([]domain.QuotaAlert, error)
}

// QuotaController serves provider-neutral subscription quota endpoints.
type QuotaController struct{ Svc QuotaService }

// Register mounts the subscription quota endpoints on r.
func (c *QuotaController) Register(r chi.Router) {
	r.Get("/usage/plans", c.list)
	r.Post("/usage/plans/refresh", c.refreshAll)
	r.Get("/usage/plans/alerts", c.alerts)
	r.Get("/usage/plans/{provider}/accounts/{accountId}", c.get)
	r.Get("/usage/plans/{provider}/accounts/{accountId}/history", c.history)
	r.Post("/usage/plans/{provider}/accounts/{accountId}/refresh", c.refresh)
}

func (c *QuotaController) refreshAll(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/usage/plans/refresh")
		return
	}
	snapshots, err := c.Svc.RefreshAll(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	writeQuotaList(w, snapshots)
}

func (c *QuotaController) alerts(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/usage/plans/alerts")
		return
	}
	minutes, limit := int64(10), int64(100)
	if value := r.URL.Query().Get("minutes"); value != "" {
		parsed, err := parsePositiveInt64(value, 10080)
		if err != nil {
			envelope.WriteError(w, r, err)
			return
		}
		minutes = parsed
	}
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := parsePositiveInt64(value, 500)
		if err != nil {
			envelope.WriteError(w, r, err)
			return
		}
		limit = parsed
	}
	alerts, err := c.Svc.Alerts(r.Context(), time.Now().UTC().Add(-time.Duration(minutes)*time.Minute), limit)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	out := make([]QuotaAlertResponse, 0, len(alerts))
	for _, alert := range alerts {
		out = append(out, QuotaAlertResponse{ID: alert.ID, Provider: string(alert.Provider), AccountID: string(alert.AccountID), LimitID: string(alert.LimitID), Kind: alert.Kind, Severity: alert.Severity, Title: alert.Title, Body: alert.Body, CreatedAt: alert.CreatedAt})
	}
	envelope.WriteJSON(w, http.StatusOK, ListQuotaAlertsResponse{Alerts: out})
}

func (c *QuotaController) refresh(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/usage/plans/{provider}/accounts/{accountId}/refresh")
		return
	}
	provider, accountID := quotaPath(r)
	snapshot, err := c.Svc.Refresh(r.Context(), provider, accountID)
	if errors.Is(err, ports.ErrQuotaRefreshUnsupported) {
		err = apierr.Conflict("QUOTA_REFRESH_UNSUPPORTED", "This provider cannot refresh quota on demand", nil)
	}
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, quotaResponse(snapshot, time.Now().UTC()))
}

func (c *QuotaController) list(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/usage/plans")
		return
	}
	snapshots, err := c.Svc.List(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	writeQuotaList(w, snapshots)
}

func writeQuotaList(w http.ResponseWriter, snapshots []domain.QuotaSnapshot) {
	out := make([]ProviderQuotaResponse, 0, len(snapshots))
	for _, snapshot := range snapshots {
		out = append(out, quotaResponse(snapshot, time.Now().UTC()))
	}
	envelope.WriteJSON(w, http.StatusOK, ListProviderQuotaResponse{Providers: out})
}

func (c *QuotaController) get(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/usage/plans/{provider}/accounts/{accountId}")
		return
	}
	provider, accountID := quotaPath(r)
	snapshot, ok, err := c.Svc.Get(r.Context(), provider, accountID)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	if !ok {
		envelope.WriteError(w, r, apierr.NotFound("QUOTA_ACCOUNT_NOT_FOUND", "Unknown provider quota account"))
		return
	}
	envelope.WriteJSON(w, http.StatusOK, quotaResponse(snapshot, time.Now().UTC()))
}

func (c *QuotaController) history(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/usage/plans/{provider}/accounts/{accountId}/history")
		return
	}
	hours, limit := int64(168), int64(500)
	if value := r.URL.Query().Get("hours"); value != "" {
		if parsed, err := parsePositiveInt64(value, 2160); err == nil {
			hours = parsed
		} else {
			envelope.WriteError(w, r, err)
			return
		}
	}
	if value := r.URL.Query().Get("limit"); value != "" {
		if parsed, err := parsePositiveInt64(value, 2000); err == nil {
			limit = parsed
		} else {
			envelope.WriteError(w, r, err)
			return
		}
	}
	provider, accountID := quotaPath(r)
	points, err := c.Svc.History(r.Context(), provider, accountID, time.Now().UTC().Add(-time.Duration(hours)*time.Hour), limit)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	out := make([]QuotaHistoryPointResponse, 0, len(points))
	for _, point := range points {
		out = append(out, QuotaHistoryPointResponse{LimitID: string(point.LimitID), WindowType: point.WindowType, Scope: string(point.Scope), ScopeID: point.ScopeID, UsedPercent: point.UsedPercent, ResetsAt: point.ResetsAt, Reached: point.Reached, ObservedAt: point.ObservedAt})
	}
	envelope.WriteJSON(w, http.StatusOK, ListQuotaHistoryResponse{Points: out})
}

func quotaPath(r *http.Request) (domain.QuotaProviderID, domain.QuotaAccountID) {
	return domain.QuotaProviderID(chi.URLParam(r, "provider")), domain.QuotaAccountID(chi.URLParam(r, "accountId"))
}

func parsePositiveInt64(value string, maximum int64) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || parsed > maximum {
		return 0, apierr.Invalid("INVALID_QUOTA_QUERY", "Quota history query is outside the supported range", nil)
	}
	return parsed, nil
}

func quotaResponse(snapshot domain.QuotaSnapshot, now time.Time) ProviderQuotaResponse {
	response := ProviderQuotaResponse{
		Provider: string(snapshot.Provider), AccountID: string(snapshot.AccountID), AccountLabel: snapshot.AccountLabel,
		PlanType: snapshot.PlanType, AuthMode: snapshot.AuthMode, Completeness: string(snapshot.Completeness),
		Freshness: quotasvc.Freshness(snapshot.ObservedAt, now), Severity: string(quotasvc.SnapshotSeverity(snapshot)),
		ObservedAt: snapshot.ObservedAt, Limits: make([]QuotaLimitResponse, 0, len(snapshot.Limits)), Balances: make([]QuotaBalanceResponse, 0, len(snapshot.Balances)),
		RefreshError: snapshot.RefreshError,
		Capabilities: QuotaCapabilitiesResponse{
			SupportsRead: snapshot.Capabilities.SupportsRead, SupportsSubscribe: snapshot.Capabilities.SupportsSubscribe,
			SupportsHistory: snapshot.Capabilities.SupportsHistory, SupportsCredits: snapshot.Capabilities.SupportsCredits,
			SupportsSpendLimits: snapshot.Capabilities.SupportsSpendLimits,
		},
	}
	for _, limit := range snapshot.Limits {
		var seconds *int64
		if limit.WindowDuration != nil {
			value := int64(*limit.WindowDuration / time.Second)
			seconds = &value
		}
		response.Limits = append(response.Limits, QuotaLimitResponse{
			ID: string(limit.ID), Name: limit.Name, Category: string(limit.Category), Scope: string(limit.Scope), ScopeID: limit.ScopeID,
			WindowType: limit.WindowType, WindowDurationSeconds: seconds, UsedPercent: limit.UsedPercent,
			RemainingPercent: limit.RemainingPercent(), RemainingValue: limit.RemainingValue, TotalValue: limit.TotalValue,
			Unit: limit.Unit, ResetsAt: limit.ResetsAt, Reached: limit.Reached, ReachedReason: limit.ReachedReason,
			Severity: string(quotasvc.LimitSeverity(limit)),
		})
	}
	for _, balance := range snapshot.Balances {
		response.Balances = append(response.Balances, QuotaBalanceResponse{ID: balance.ID, Name: balance.Name, Value: balance.Value, Currency: balance.Currency, Unlimited: balance.Unlimited})
	}
	return response
}
