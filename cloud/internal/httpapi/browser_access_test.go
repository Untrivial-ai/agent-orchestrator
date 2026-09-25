package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/browserstream"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
)

type revocableBrowserStore struct {
	browserRelayStore
	operate bool
	denied  atomic.Bool
	checks  atomic.Int32
	err     error
}

func (s *revocableBrowserStore) OpenBrowserViewerTicket(ctx context.Context, token string) (domain.AccessTicket, error) {
	ticket, err := s.browserRelayStore.OpenBrowserViewerTicket(ctx, token)
	if !s.operate {
		ticket.Scopes = []string{"browser:view", "subject:user-1"}
	}
	return ticket, err
}

func (s *revocableBrowserStore) CheckBrowserViewerAccess(_ context.Context, principal domain.Principal, orgID, sessionID string, operate bool) error {
	s.checks.Add(1)
	if principal.UserID != "user-1" || orgID != browserTestOrg || sessionID != browserTestSession || operate != s.operate {
		return errors.New("incorrect viewer identity")
	}
	if s.denied.Load() {
		return s.err
	}
	return nil
}

func TestBrowserAccessRevocationDisconnectsPassiveViewer(t *testing.T) {
	for _, test := range []struct {
		name    string
		operate bool
		err     error
		status  websocket.StatusCode
	}{
		{"read-only", false, postgres.ErrForbidden, websocket.StatusPolicyViolation},
		{"idle operator", true, postgres.ErrForbidden, websocket.StatusPolicyViolation},
		{"temporary outage", false, errors.New("store unavailable"), websocket.StatusTryAgainLater},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &revocableBrowserStore{operate: test.operate, err: test.err}
			server := &Server{store: store, browserViewerEnabled: true, browserStreams: newBrowserStreams(), browserAccessInterval: time.Millisecond}
			router := chi.NewRouter()
			router.Get("/orgs/{orgId}/sessions/{sessionId}/browser-view/stream", server.connectBrowserViewer)
			httpServer := httptest.NewServer(router)
			defer httpServer.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			url := "ws" + httpServer.URL[len("http"):] + "/orgs/" + browserTestOrg + "/sessions/" + browserTestSession + "/browser-view/stream?ticket=test"
			conn, _, err := websocket.Dial(ctx, url, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			key := browserRelayKey(browserTestOrg, browserTestSession)
			peer := server.browserStreams.viewer(key)
			frame, err := browserstream.EncodeFrame(browserstream.Frame{
				StreamEpoch: 1, Sequence: 1, Width: 800, Height: 600, TargetID: "target", JPEG: []byte{0xff, 0xd8, 0xff, 0xd9},
			})
			if err != nil {
				t.Fatal(err)
			}
			peer.frames.Put(frame)
			for {
				kind, _, err := conn.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if kind == websocket.MessageBinary {
					break
				}
			}
			store.denied.Store(true)
			for err == nil {
				_, _, err = conn.Read(ctx)
			}
			if websocket.CloseStatus(err) != test.status {
				t.Fatalf("close=%v, want %v", err, test.status)
			}
			select {
			case <-peer.done:
			case <-ctx.Done():
				t.Fatal("revoked viewer was not cleaned up")
			}
			if server.browserStreams.viewer(key) != nil || store.checks.Load() == 0 {
				t.Fatal("viewer authorization was not revoked")
			}
			peer.frames.Put(frame)
			if _, _, err := conn.Read(ctx); err == nil {
				t.Fatal("revoked viewer received another frame")
			}
		})
	}
}
