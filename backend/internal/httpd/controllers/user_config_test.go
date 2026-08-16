package controllers_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	userconfigsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/userconfig"
)

// stubUserConfigManager lets a test drive the GET/PUT handlers with a
// deterministic Manager implementation, without a store.
type stubUserConfigManager struct {
	userconfigsvc.Manager
	get func(context.Context) (domain.AgentConfig, error)
	set func(context.Context, userconfigsvc.SetUserConfigInput) (domain.AgentConfig, error)
}

func (m stubUserConfigManager) Get(ctx context.Context) (domain.AgentConfig, error) {
	if m.get == nil {
		return domain.AgentConfig{}, nil
	}
	return m.get(ctx)
}

func (m stubUserConfigManager) Set(ctx context.Context, in userconfigsvc.SetUserConfigInput) (domain.AgentConfig, error) {
	if m.set == nil {
		return in.AgentConfig, nil
	}
	return m.set(ctx, in)
}

func userConfigServer(t *testing.T, mgr userconfigsvc.Manager) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		UserConfig: mgr,
	}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return srv
}

// nilManagerRoutesReturn501 locks the OpenAPI-backed 501 contract: with no
// Manager wired, the routes stay mounted and respond with a parseable 501 rather
// than a bare 404. Requires the operation to be registered in the spec (otherwise
// apispec.NotImplemented panics), so this test also guards the specgen wiring.
func TestUserConfigRoutes_NilManagerIs501(t *testing.T) {
	srv := userConfigServer(t, nil)
	body, status, headers := doRequest(t, srv, "GET", "/api/v1/user-config", "")
	assertJSON(t, headers)
	assertErrorCode(t, body, status, 501, "NOT_IMPLEMENTED")

	body, status, headers = doRequest(t, srv, "PUT", "/api/v1/user-config", `{"agentConfig":{"model":"m"}}`)
	assertJSON(t, headers)
	assertErrorCode(t, body, status, 501, "NOT_IMPLEMENTED")
}

func TestUserConfigAPI_GetUnsetReturnsEmpty(t *testing.T) {
	srv := userConfigServer(t, stubUserConfigManager{
		get: func(context.Context) (domain.AgentConfig, error) { return domain.AgentConfig{}, nil },
	})
	body, status, _ := doRequest(t, srv, "GET", "/api/v1/user-config", "")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	var resp struct {
		AgentConfig domain.AgentConfig `json:"agentConfig"`
	}
	mustJSON(t, body, &resp)
	if !resp.AgentConfig.IsZero() {
		t.Fatalf("agentConfig = %+v, want zero", resp.AgentConfig)
	}
}

func TestUserConfigAPI_PutReturnsStored(t *testing.T) {
	srv := userConfigServer(t, stubUserConfigManager{})
	body, status, _ := doRequest(t, srv, "PUT", "/api/v1/user-config", `{"agentConfig":{"model":"claude-opus-4-8","permissions":"auto"}}`)
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	var resp struct {
		AgentConfig domain.AgentConfig `json:"agentConfig"`
	}
	mustJSON(t, body, &resp)
	if resp.AgentConfig.Model != "claude-opus-4-8" || resp.AgentConfig.Permissions != domain.PermissionModeAuto {
		t.Fatalf("agentConfig = %+v", resp.AgentConfig)
	}
}

func TestUserConfigAPI_PutRejectsUnknownField(t *testing.T) {
	// decodeJSONStrict rejects unknown keys so an unexpected field surfaces as 400.
	srv := userConfigServer(t, stubUserConfigManager{})
	body, status, _ := doRequest(t, srv, "PUT", "/api/v1/user-config", `{"agentConfig":{"bogus":"x"}}`)
	assertErrorCode(t, body, status, 400, "INVALID_JSON")
}
