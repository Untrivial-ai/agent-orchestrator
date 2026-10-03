package httpd

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/browserruntime"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
)

func TestMobileBrowserRuntimeCannotRegisterOnUnauthenticatedLoopbackRouter(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouterWithControl(config.Config{}, log, nil, APIDeps{MobileBrowser: browserruntime.NewMobileHub()}, ControlDeps{})
	req := httptest.NewRequest(http.MethodGet, "/mobile-browser-runtime?sessionId=s1&deviceId=d1", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("loopback mobile runtime registration = %d, want 404", recorder.Code)
	}
}
