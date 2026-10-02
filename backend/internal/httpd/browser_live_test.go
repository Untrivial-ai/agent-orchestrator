package httpd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/requestscope"
)

type browserLiveSessionStub struct{ calls int }

func (s *browserLiveSessionStub) Get(context.Context, domain.SessionID) (domain.Session, error) {
	s.calls++
	return domain.Session{}, nil
}

func TestBrowserLiveIsLANOnly(t *testing.T) {
	sessions := &browserLiveSessionStub{}
	hub := NewBrowserLiveHub(nil, sessions, func() bool { return true }, nil)
	r := chi.NewRouter()
	mountBrowserLive(r, hub)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/s1/browser/live", nil)
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("loopback status = %d, want 404", res.Code)
	}
	if sessions.calls != 0 {
		t.Fatalf("session lookup calls = %d, want 0", sessions.calls)
	}
}

func TestBrowserLiveRequiresDesktopOptIn(t *testing.T) {
	sessions := &browserLiveSessionStub{}
	hub := NewBrowserLiveHub(nil, sessions, func() bool { return false }, nil)
	r := chi.NewRouter()
	mountBrowserLive(r, hub)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/s1/browser/live", nil)
	req = req.WithContext(requestscope.WithLAN(req.Context()))
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("disabled status = %d, want 403", res.Code)
	}
	if sessions.calls != 0 {
		t.Fatalf("session lookup calls = %d, want 0", sessions.calls)
	}
}

func TestBrowserLiveLeaseIsExclusiveAndRevocable(t *testing.T) {
	hub := NewBrowserLiveHub(nil, nil, nil, nil)
	ctx, release, ok := hub.reserve("s1")
	if !ok {
		t.Fatal("first lease was rejected")
	}
	if _, _, ok := hub.reserve("s1"); ok {
		t.Fatal("second lease was accepted")
	}
	hub.CloseAll()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("CloseAll did not revoke lease")
	}
	release()
	_, releaseAgain, ok := hub.reserve("s1")
	if !ok {
		t.Fatal("lease was not reusable after release")
	}
	releaseAgain()
}
