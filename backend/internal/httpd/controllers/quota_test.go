package controllers_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeQuotaService struct {
	snapshot   domain.QuotaSnapshot
	points     []domain.QuotaHistoryPoint
	alerts     []domain.QuotaAlert
	refreshErr error
}

func (f *fakeQuotaService) Alerts(context.Context, time.Time, int64) ([]domain.QuotaAlert, error) {
	return f.alerts, nil
}

func (f *fakeQuotaService) List(context.Context) ([]domain.QuotaSnapshot, error) {
	return []domain.QuotaSnapshot{f.snapshot}, nil
}
func (f *fakeQuotaService) Get(context.Context, domain.QuotaProviderID, domain.QuotaAccountID) (domain.QuotaSnapshot, bool, error) {
	return f.snapshot, f.snapshot.Provider != "", nil
}
func (f *fakeQuotaService) History(context.Context, domain.QuotaProviderID, domain.QuotaAccountID, time.Time, int64) ([]domain.QuotaHistoryPoint, error) {
	return f.points, nil
}
func (f *fakeQuotaService) Refresh(context.Context, domain.QuotaProviderID, domain.QuotaAccountID) (domain.QuotaSnapshot, error) {
	return f.snapshot, f.refreshErr
}
func (f *fakeQuotaService) RefreshAll(context.Context) ([]domain.QuotaSnapshot, error) {
	return []domain.QuotaSnapshot{f.snapshot}, f.refreshErr
}

func newQuotaTestServer(t *testing.T, svc *fakeQuotaService) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Quota: svc}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return srv
}

func TestQuotaAPIListsProviderNeutralSnapshots(t *testing.T) {
	used := 71.0
	reset := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	svc := &fakeQuotaService{snapshot: domain.QuotaSnapshot{
		Provider: "codex", AccountID: "default", PlanType: "pro", Completeness: domain.QuotaComplete,
		ObservedAt: time.Now().UTC(), Capabilities: domain.QuotaCapabilities{SupportsRead: true},
		Limits: []domain.QuotaLimit{{ID: "codex", Category: domain.QuotaRateLimit, Scope: domain.QuotaAccountScope, WindowType: "primary", UsedPercent: &used, ResetsAt: &reset}},
	}}
	srv := newQuotaTestServer(t, svc)
	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/usage/plans", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d; body=%s", status, body)
	}
	var got struct {
		Providers []struct {
			Provider string `json:"provider"`
			Plan     string `json:"planType"`
			Limits   []struct {
				Remaining float64 `json:"remainingPercent"`
			} `json:"limits"`
		} `json:"providers"`
	}
	mustJSON(t, body, &got)
	if len(got.Providers) != 1 || got.Providers[0].Provider != "codex" || got.Providers[0].Plan != "pro" || got.Providers[0].Limits[0].Remaining != 29 {
		t.Fatalf("response = %+v", got)
	}
}

func TestQuotaAPIReportsUnsupportedRefresh(t *testing.T) {
	srv := newQuotaTestServer(t, &fakeQuotaService{refreshErr: ports.ErrQuotaRefreshUnsupported})
	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/usage/plans/claude/accounts/default/refresh", "")
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", status, body)
	}
}

func TestQuotaAPIRefreshesAllKnownProviders(t *testing.T) {
	svc := &fakeQuotaService{snapshot: domain.QuotaSnapshot{
		Provider: "claude", AccountID: "default", Completeness: domain.QuotaPartial, ObservedAt: time.Now().UTC(),
	}}
	srv := newQuotaTestServer(t, svc)
	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/usage/plans/refresh", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"provider":"claude"`) {
		t.Fatalf("status = %d; body=%s", status, body)
	}
}

func TestQuotaAPIListsTransitionAlerts(t *testing.T) {
	svc := &fakeQuotaService{alerts: []domain.QuotaAlert{{
		ID: "quota_1", Provider: "claude", AccountID: "default", LimitID: "five_hour",
		Kind: "threshold", Severity: "critical", Title: "Claude usage is critical", CreatedAt: time.Now().UTC(),
	}}}
	srv := newQuotaTestServer(t, svc)
	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/usage/plans/alerts", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"kind":"threshold"`) {
		t.Fatalf("status = %d; body=%s", status, body)
	}
}
