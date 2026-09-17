package httpd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	agentswitchobs "github.com/aoagents/agent-orchestrator/backend/internal/observe/agentswitch"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestShutdownGuard verifies that POST /shutdown only fires for a trusted local
// caller: a loopback Host with no Origin header. A cross-site Origin or a
// non-loopback (DNS-rebinding) Host must be rejected without triggering the
// shutdown side effect.
func TestShutdownGuard(t *testing.T) {
	cases := []struct {
		name       string
		host       string
		origin     string
		wantStatus int
		wantFired  bool
	}{
		{name: "loopback no origin", host: "127.0.0.1:3001", wantStatus: http.StatusAccepted, wantFired: true},
		{name: "localhost no origin", host: "localhost:3001", wantStatus: http.StatusAccepted, wantFired: true},
		{name: "cross-site origin", host: "127.0.0.1:3001", origin: "https://evil.example", wantStatus: http.StatusForbidden, wantFired: false},
		{name: "rebinding host", host: "evil.example", wantStatus: http.StatusForbidden, wantFired: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fired := false
			r := NewRouterWithControl(config.Config{}, discardLogger(), nil, APIDeps{}, ControlDeps{
				RequestShutdown: func() { fired = true },
			})

			req := httptest.NewRequest(http.MethodPost, "http://"+tc.host+"/shutdown", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if fired != tc.wantFired {
				t.Fatalf("shutdown fired = %v, want %v", fired, tc.wantFired)
			}
		})
	}
}

func TestAgentSwitchPolicyControlRoutesAreTypedAndLoopbackOnly(t *testing.T) {
	policy := &policyControlFake{
		prepareAck: ports.AgentSwitchFailurePolicyAcknowledgement{
			Authorization: domain.AgentSwitchReportingAuthorization{ConsentGeneration: "generation-on"},
			GateDrained:   true,
		},
		applyAck: ports.AgentSwitchFailurePolicyAcknowledgement{
			Authorization: domain.AgentSwitchReportingAuthorization{ConsentGeneration: "generation-off"},
			GateDrained:   true, PurgeConfirmed: true,
		},
	}
	r := NewRouterWithControl(config.Config{}, discardLogger(), nil, APIDeps{}, ControlDeps{AgentSwitchPolicy: policy})
	prepare := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/internal/agent-switch-observability/prepare-disable", nil)
	prepare.Host = "127.0.0.1"
	prepared := httptest.NewRecorder()
	r.ServeHTTP(prepared, prepare)
	if prepared.Code != http.StatusOK || policy.prepared != 1 {
		t.Fatalf("prepare status=%d calls=%d", prepared.Code, policy.prepared)
	}
	var preparedBody map[string]any
	if err := json.Unmarshal(prepared.Body.Bytes(), &preparedBody); err != nil {
		t.Fatalf("decode prepare acknowledgement: %v", err)
	}
	if preparedBody["status"] != "applied" || preparedBody["consentGeneration"] != "generation-on" || preparedBody["eventsEnabled"] != false || preparedBody["gateDrained"] != true || preparedBody["purgeConfirmed"] != false {
		t.Fatalf("prepare acknowledgement = %#v", preparedBody)
	}
	body, _ := json.Marshal(map[string]any{"consentGeneration": "generation-off", "eventsEnabled": false})
	apply := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/internal/agent-switch-observability/apply-policy", bytes.NewReader(body))
	apply.Host = "127.0.0.1"
	applied := httptest.NewRecorder()
	r.ServeHTTP(applied, apply)
	if applied.Code != http.StatusOK || policy.appliedGeneration != "generation-off" || policy.appliedEnabled {
		t.Fatalf("apply status=%d fake=%+v", applied.Code, policy)
	}
	var appliedBody map[string]any
	if err := json.Unmarshal(applied.Body.Bytes(), &appliedBody); err != nil {
		t.Fatalf("decode apply acknowledgement: %v", err)
	}
	if appliedBody["status"] != "applied" || appliedBody["consentGeneration"] != "generation-off" || appliedBody["eventsEnabled"] != false || appliedBody["gateDrained"] != true || appliedBody["purgeConfirmed"] != true {
		t.Fatalf("apply acknowledgement = %#v", appliedBody)
	}
	remote := httptest.NewRequest(http.MethodPost, "http://evil.example/internal/agent-switch-observability/prepare-disable", nil)
	remote.Host = "evil.example"
	denied := httptest.NewRecorder()
	r.ServeHTTP(denied, remote)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("remote status=%d", denied.Code)
	}
}

func TestAgentSwitchPolicyControlNeverAcknowledgesFailedCleanup(t *testing.T) {
	policy := &policyControlFake{applyErr: errors.New("atomic purge failed")}
	r := NewRouterWithControl(config.Config{}, discardLogger(), nil, APIDeps{}, ControlDeps{AgentSwitchPolicy: policy})
	body, _ := json.Marshal(map[string]any{"consentGeneration": "generation-off", "eventsEnabled": false})
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/internal/agent-switch-observability/apply-policy", bytes.NewReader(body))
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code < http.StatusBadRequest {
		t.Fatalf("failed cleanup status = %d, want non-success", rec.Code)
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode failed cleanup response: %v", err)
	}
	if response["status"] == "applied" || response["purgeConfirmed"] == true {
		t.Fatalf("failed cleanup response fabricated success: %#v", response)
	}
}

func TestAgentSwitchPolicyControlReportsCleanupPendingWithoutProof(t *testing.T) {
	policy := &policyControlFake{applyErr: agentswitchobs.ErrPolicyCleanupPending}
	r := NewRouterWithControl(config.Config{}, discardLogger(), nil, APIDeps{}, ControlDeps{AgentSwitchPolicy: policy})
	body, _ := json.Marshal(map[string]any{"consentGeneration": "generation-on", "eventsEnabled": true})
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/internal/agent-switch-observability/apply-policy", bytes.NewReader(body))
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("cleanup-pending status = %d, want %d", rec.Code, http.StatusConflict)
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode cleanup-pending response: %v", err)
	}
	if response["status"] != "cleanup_pending" || response["purgeConfirmed"] == true {
		t.Fatalf("cleanup-pending response = %#v", response)
	}
}

type policyControlFake struct {
	prepared          int
	appliedGeneration string
	appliedEnabled    bool
	prepareAck        ports.AgentSwitchFailurePolicyAcknowledgement
	applyAck          ports.AgentSwitchFailurePolicyAcknowledgement
	prepareErr        error
	applyErr          error
}

func (f *policyControlFake) PrepareDisable(context.Context) (ports.AgentSwitchFailurePolicyAcknowledgement, error) {
	f.prepared++
	return f.prepareAck, f.prepareErr
}
func (f *policyControlFake) ApplyPolicy(_ context.Context, generation string, enabled bool) (ports.AgentSwitchFailurePolicyAcknowledgement, error) {
	f.appliedGeneration = generation
	f.appliedEnabled = enabled
	return f.applyAck, f.applyErr
}
