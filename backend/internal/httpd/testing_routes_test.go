package httpd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	testingsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/testing"
)

type testingOriginService struct {
	controllers.TestingService
	calls int
}

func (s *testingOriginService) CreateRun(context.Context, testingsvc.CreateRunInput) (domain.TestRunRecord, error) {
	s.calls++
	return domain.TestRunRecord{ID: "run"}, nil
}

func TestTestingOriginBoundary(t *testing.T) {
	svc := &testingOriginService{}
	router := NewRouterWithControl(config.Config{AllowedOrigins: []string{"app://renderer", "http://localhost:5173"}}, nil, nil, APIDeps{Testing: svc}, ControlDeps{})
	paths := []string{"/api/v1/testing", "/api/v1/testing/runs", "/api/v1/testing/runs/run/attempts", "/api/v1/testing/attempts/attempt/cancel", "/api/v1/testing/attempts/attempt/evidence"}
	paths = append(paths, "/api/v1/testing/comparisons", "/api/v1/testing/sessions/worker/legs/base/start")
	for _, name := range testingsvc.ToolNames {
		paths = append(paths, "/api/v1/testing/attempts/attempt/tools/"+name, "/api/v1/testing/sessions/worker/tools/"+name)
	}
	for _, path := range paths {
		for _, origin := range []string{"https://hostile.example", "http://ao-preview.hostile.localhost:5181", "null"} {
			for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodOptions} {
				req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
				req.Header.Set("Origin", origin)
				req.Header.Set("Content-Type", "text/plain")
				if method == http.MethodOptions {
					req.Header.Set("Access-Control-Request-Method", http.MethodPost)
				}
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "ORIGIN_FORBIDDEN") || svc.calls != 0 {
					t.Fatalf("%s %s origin %s: %d %s, calls %d", method, path, origin, w.Code, w.Body.String(), svc.calls)
				}
			}
		}
	}
	for _, origin := range []string{"", "app://renderer", "http://localhost:5173"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/testing/runs", strings.NewReader(`{}`))
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		before := svc.calls
		router.ServeHTTP(w, req)
		if w.Code != http.StatusCreated || svc.calls != before+1 {
			t.Fatalf("allowed origin %q: %d %s, calls %d", origin, w.Code, w.Body.String(), svc.calls)
		}
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != origin {
			t.Fatalf("allowed origin %q: CORS header %q", origin, got)
		}
	}
}

func TestTestingRoutesOnLoopbackButBlockedOnLAN(t *testing.T) {
	router := NewRouterWithControl(config.Config{}, nil, nil, APIDeps{}, ControlDeps{})
	paths := []string{"/api/v1/testing/runs", "/api/v1/testing/runs/run/attempts", "/api/v1/testing/attempts/attempt/cancel", "/api/v1/testing/attempts/attempt/evidence"}
	paths = append(paths, "/api/v1/testing/comparisons", "/api/v1/testing/sessions/worker/legs/base/start")
	for _, name := range testingsvc.ToolNames {
		paths = append(paths, "/api/v1/testing/attempts/attempt/tools/"+name, "/api/v1/testing/sessions/worker/tools/"+name)
	}
	for _, path := range paths {
		method := http.MethodPost
		if strings.HasSuffix(path, "/evidence") {
			method = http.MethodGet
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		if w.Code != 503 || !strings.Contains(w.Body.String(), "TESTING_PROVIDER_NOT_CONFIGURED") {
			t.Fatal("loopback route missing", path, w.Code, w.Body.String())
		}
		w = httptest.NewRecorder()
		lanControlBlock(router).ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(`{}`)))
		if w.Code != http.StatusNotFound {
			t.Fatal("testing exposed on LAN", path, w.Code)
		}
	}
}
