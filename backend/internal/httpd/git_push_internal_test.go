package httpd

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/gitservice"
)

func gitPushTestRouter() http.Handler {
	return NewRouterWithControl(config.Config{}, slog.Default(), nil, APIDeps{
		GitPush: &gitservice.Service{}, GitPushCapability: "private-capability",
	}, ControlDeps{})
}

func TestTrustedGitPushRejectsOrdinaryLocalHTTP(t *testing.T) {
	for _, tc := range []struct{ name, capability, origin string }{
		{name: "missing capability"},
		{name: "wrong capability", capability: "wrong"},
		{name: "browser origin", capability: "private-capability", origin: "app://renderer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/internal/git-push/prepare", bytes.NewBufferString(`{}`))
			if tc.capability != "" {
				req.Header.Set(gitPushCapabilityHeader, tc.capability)
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			gitPushTestRouter().ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", w.Code)
			}
		})
	}
}

func TestTrustedGitPushCapabilityReachesStrictDecoder(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://localhost/internal/git-push/prepare", bytes.NewBufferString(`{"unknown":true}`))
	req.Header.Set(gitPushCapabilityHeader, "private-capability")
	w := httptest.NewRecorder()
	gitPushTestRouter().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestTrustedGitPushRejectsRendererOriginForEveryBrokerAction(t *testing.T) {
	for _, action := range []string{"prepare", "approve-and-push", "revoke"} {
		t.Run(action, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/internal/git-push/"+action, bytes.NewBufferString(`{}`))
			req.Header.Set(gitPushCapabilityHeader, "private-capability")
			req.Header.Set("Origin", "app://renderer")
			w := httptest.NewRecorder()
			gitPushTestRouter().ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", w.Code)
			}
		})
	}
}
