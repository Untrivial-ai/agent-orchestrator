package httpd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
)

// fakeMobileBridge is a no-op mobileBridge implementation. Its type is
// unexported to httpd, but the controllers.MobileController.Bridge field is
// exported and structurally typed, so any value with matching method
// signatures satisfies it from outside the package.
type fakeMobileBridge struct{}

func (fakeMobileBridge) Status() controllers.MobileStatusResponse {
	return controllers.MobileStatusResponse{}
}

func (fakeMobileBridge) Enable() (controllers.MobileStatusResponse, error) {
	return controllers.MobileStatusResponse{}, nil
}

func (fakeMobileBridge) Disable() error { return nil }

func (fakeMobileBridge) Regenerate() (controllers.MobileStatusResponse, error) {
	return controllers.MobileStatusResponse{}, nil
}

func (fakeMobileBridge) StartRemoteAccess() (controllers.MobileStatusResponse, error) {
	return controllers.MobileStatusResponse{}, nil
}

func (fakeMobileBridge) SetSecurePairing(on bool) (controllers.MobileStatusResponse, error) {
	return controllers.MobileStatusResponse{}, nil
}

// newTestRouterWithMobile builds a bare router with only the mobile control
// routes mounted, backed by a fake bridge.
func newTestRouterWithMobile(t *testing.T) chi.Router {
	t.Helper()
	r := chi.NewRouter()
	mountMobile(r, &controllers.MobileController{Bridge: fakeMobileBridge{}})
	return r
}

// The mobile control routes are served on the loopback router without a
// Host/Origin gate: the desktop renderer is a browser context that always
// sends an Origin, so a localControlRequest-style gate would (wrongly) 403 the
// very client meant to call them. The "phone cannot toggle its own access"
// invariant is enforced on the LAN listener by lanControlBlock instead — see
// TestLANManagerBlocksLoopbackOnlyControlRoutes. This test pins that the
// loopback route is reachable and NOT Host-gated.
func TestMobileStatusRouteServedOnLoopbackRouter(t *testing.T) {
	r := newTestRouterWithMobile(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mobile/status", nil)
	// A non-loopback Host used to force a 403; it must no longer matter here,
	// because Host-based gating is not how the phone is blocked.
	req.Host = "192.168.1.9:3011"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mobile status on loopback router: got %d want 200", w.Code)
	}
}
