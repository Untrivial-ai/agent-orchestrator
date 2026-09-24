package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/browserstream"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
)

const (
	browserTestOrg     = "11111111-1111-1111-1111-111111111111"
	browserTestSession = "22222222-2222-2222-2222-222222222222"
)

type browserRelayStore struct{ Store }

func (browserRelayStore) RefreshBrowserInteraction(context.Context, domain.Principal, string, string, int64) error {
	return nil
}

func (browserRelayStore) OpenBrowserViewerTicket(context.Context, string) (domain.AccessTicket, error) {
	return domain.AccessTicket{
		OrgID: browserTestOrg, SessionID: browserTestSession, Purpose: "browser-view",
		WorkerEpoch: 7, Scopes: []string{"browser:view", "browser:operate", "subject:user-1"},
		ExpiresAt: time.Now().Add(time.Minute),
	}, nil
}

type browserTicketStore struct {
	Store
	issueToken  string
	issueScopes []string
	issueErr    error
	issuedFor   domain.Principal
	issuedTTL   time.Duration
	openTicket  domain.AccessTicket
	openErr     error
	oneUse      bool
	openCount   int
}

func (s *browserTicketStore) IssueBrowserViewerTicket(
	_ context.Context,
	principal domain.Principal,
	_, _ string,
	ttl time.Duration,
) (string, []string, error) {
	s.issuedFor = principal
	s.issuedTTL = ttl
	return s.issueToken, s.issueScopes, s.issueErr
}

func (s *browserTicketStore) OpenBrowserViewerTicket(context.Context, string) (domain.AccessTicket, error) {
	s.openCount++
	if s.openErr != nil {
		return domain.AccessTicket{}, s.openErr
	}
	if s.oneUse && s.openCount > 1 {
		return domain.AccessTicket{}, postgres.ErrInvalidTicket
	}
	return s.openTicket, nil
}

func TestBrowserViewerTicketIssueIsNoStoreAndBounded(t *testing.T) {
	store := &browserTicketStore{
		issueToken:  "opaque-ticket",
		issueScopes: []string{"browser:view", "browser:operate", "subject:user-1"},
	}
	server := &Server{store: store, browserViewerEnabled: true}
	router := chi.NewRouter()
	router.Post("/orgs/{orgId}/sessions/{sessionId}/browser-view-ticket", func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), principalKey, domain.Principal{UserID: "user-1"})
		server.createBrowserViewerTicket(w, r.WithContext(ctx))
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/orgs/"+browserTestOrg+"/sessions/"+browserTestSession+"/browser-view-ticket",
		nil,
	)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("ticket status = %d, body %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache control = %q", response.Header().Get("Cache-Control"))
	}
	if store.issuedFor.UserID != "user-1" || store.issuedTTL != 5*time.Minute {
		t.Fatalf("issue principal=%q ttl=%s", store.issuedFor.UserID, store.issuedTTL)
	}
	var body struct {
		Ticket          string `json:"ticket"`
		ExpiresIn       int    `json:"expiresIn"`
		ProtocolVersion int    `json:"protocolVersion"`
		CanOperate      bool   `json:"canOperate"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Ticket != "opaque-ticket" || body.ExpiresIn != 300 || body.ProtocolVersion != browserstream.Version || !body.CanOperate {
		t.Fatalf("ticket response = %+v", body)
	}
}

func TestBrowserViewerRejectsExpiredStaleAndMismatchedTickets(t *testing.T) {
	tests := []struct {
		name   string
		ticket domain.AccessTicket
		err    error
	}{
		{name: "expired or reused", err: postgres.ErrInvalidTicket},
		{name: "stale worker epoch", err: postgres.ErrStaleWorker},
		{name: "wrong organization", ticket: domain.AccessTicket{
			OrgID: "33333333-3333-3333-3333-333333333333", SessionID: browserTestSession,
			Scopes: []string{"browser:view", "subject:user-1"},
		}},
		{name: "wrong session", ticket: domain.AccessTicket{
			OrgID: browserTestOrg, SessionID: "33333333-3333-3333-3333-333333333333",
			Scopes: []string{"browser:view", "subject:user-1"},
		}},
		{name: "missing subject", ticket: domain.AccessTicket{
			OrgID: browserTestOrg, SessionID: browserTestSession, Scopes: []string{"browser:view"},
		}},
		{name: "missing view scope", ticket: domain.AccessTicket{
			OrgID: browserTestOrg, SessionID: browserTestSession, Scopes: []string{"subject:user-1"},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &browserTicketStore{openTicket: test.ticket, openErr: test.err}
			server := &Server{store: store, browserViewerEnabled: true, browserStreams: newBrowserStreams()}
			router := chi.NewRouter()
			router.Get("/orgs/{orgId}/sessions/{sessionId}/browser-view/stream", server.connectBrowserViewer)
			request := httptest.NewRequest(
				http.MethodGet,
				"/orgs/"+browserTestOrg+"/sessions/"+browserTestSession+"/browser-view/stream?ticket=opaque",
				nil,
			)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestBrowserViewerTicketCanBeRedeemedOnlyOnce(t *testing.T) {
	store := &browserTicketStore{
		oneUse: true,
		openTicket: domain.AccessTicket{
			OrgID: browserTestOrg, SessionID: browserTestSession,
			Scopes: []string{"browser:view", "browser:operate", "subject:user-1"},
		},
	}
	server := &Server{store: store, browserViewerEnabled: true, browserStreams: newBrowserStreams()}
	router := chi.NewRouter()
	router.Get("/orgs/{orgId}/sessions/{sessionId}/browser-view/stream", server.connectBrowserViewer)
	httpServer := httptest.NewServer(router)
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	streamURL := "ws" + httpServer.URL[len("http"):] + "/orgs/" + browserTestOrg + "/sessions/" + browserTestSession + "/browser-view/stream?ticket=opaque"
	connection, _, err := websocket.Dial(ctx, streamURL, nil)
	if err != nil {
		t.Fatalf("first redemption: %v", err)
	}
	connection.CloseNow()
	_, response, err := websocket.Dial(ctx, streamURL, nil)
	if err == nil {
		t.Fatal("second redemption unexpectedly succeeded")
	}
	if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("second redemption response=%v err=%v", response, err)
	}
	defer response.Body.Close()
	if store.openCount != 2 {
		t.Fatalf("ticket open count = %d", store.openCount)
	}
}

func TestBrowserStreamsReplaceSameSubjectAndFenceStaleDeregister(t *testing.T) {
	registry := newBrowserStreams()
	first := &browserViewerPeer{subject: "user-1", frames: browserstream.NewLatest(), done: make(chan struct{})}
	second := &browserViewerPeer{subject: "user-1", frames: browserstream.NewLatest(), done: make(chan struct{})}
	_, accepted, unregisterFirst := registry.registerViewer("session", first)
	if !accepted {
		t.Fatal("first viewer was rejected")
	}
	_, accepted, _ = registry.registerViewer("session", second)
	if !accepted {
		t.Fatal("same subject reconnect was rejected")
	}
	select {
	case <-first.done:
	default:
		t.Fatal("replaced viewer was not closed")
	}
	unregisterFirst()
	if registry.viewer("session") != second {
		t.Fatal("stale viewer deregister evicted its replacement")
	}
	other := &browserViewerPeer{subject: "user-2", frames: browserstream.NewLatest(), done: make(chan struct{})}
	if _, accepted, _ := registry.registerViewer("session", other); accepted {
		t.Fatal("a different subject replaced the controlling viewer")
	}
}

func TestBrowserStreamsKeepSessionsIsolatedAndBoundInputRate(t *testing.T) {
	registry := newBrowserStreams()
	first := &browserViewerPeer{subject: "user-1", frames: browserstream.NewLatest(), done: make(chan struct{})}
	second := &browserViewerPeer{subject: "user-1", frames: browserstream.NewLatest(), done: make(chan struct{})}
	defer first.close()
	defer second.close()
	if _, accepted, _ := registry.registerViewer("org/session-1", first); !accepted {
		t.Fatal("first session viewer was rejected")
	}
	if _, accepted, _ := registry.registerViewer("org/session-2", second); !accepted {
		t.Fatal("second session viewer was rejected")
	}
	if registry.viewer("org/session-1") != first || registry.viewer("org/session-2") != second {
		t.Fatal("viewer registry crossed session boundaries")
	}

	now := time.Now()
	for index := 0; index < 120; index++ {
		if !first.allow(now) {
			t.Fatalf("input %d was rejected below the rate limit", index+1)
		}
	}
	if first.allow(now) {
		t.Fatal("input above the 120 per second rate limit was accepted")
	}
	if !first.allow(now.Add(time.Second)) {
		t.Fatal("input rate did not reset after one second")
	}
}

func TestBrowserReplacementHasTerminalCloseCode(t *testing.T) {
	server := &Server{store: browserRelayStore{}, browserViewerEnabled: true, browserStreams: newBrowserStreams()}
	router := chi.NewRouter()
	router.Get("/orgs/{orgId}/sessions/{sessionId}/browser-view/stream", server.connectBrowserViewer)
	httpServer := httptest.NewServer(router)
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + httpServer.URL[len("http"):] + "/orgs/" + browserTestOrg + "/sessions/" + browserTestSession + "/browser-view/stream?ticket=test"
	first, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.CloseNow()
	second, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.CloseNow()
	for err == nil {
		_, _, err = first.Read(ctx)
	}
	if websocket.CloseStatus(err) != browserstream.ViewerReplacedCloseCode {
		t.Fatalf("replacement close: %v", err)
	}
}

type browserInteractionStore struct {
	Store
	refreshes  atomic.Int32
	refreshErr error
}

func (s *browserInteractionStore) RefreshBrowserInteraction(_ context.Context, principal domain.Principal, orgID, sessionID string, epoch int64) error {
	if principal.UserID != "user-1" || orgID != browserTestOrg || sessionID != browserTestSession || epoch != 7 {
		return postgres.ErrStaleWorker
	}
	s.refreshes.Add(1)
	return s.refreshErr
}

func TestBrowserInteractionLeaseIsAuthorizedAndThrottled(t *testing.T) {
	for _, test := range []struct {
		name     string
		operate  bool
		storeErr error
		replaced bool
	}{
		{name: "operator", operate: true},
		{name: "read only"},
		{name: "revoked", operate: true, storeErr: postgres.ErrForbidden},
		{name: "stale worker", operate: true, storeErr: postgres.ErrStaleWorker},
		{name: "replaced", operate: true, replaced: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &browserInteractionStore{refreshErr: test.storeErr}
			server := &Server{store: store, browserStreams: newBrowserStreams()}
			peer := &browserViewerPeer{subject: "user-1", operate: test.operate, done: make(chan struct{}),
				frames: browserstream.NewLatest(), controls: make(chan []byte, 16)}
			peer.streamEpoch.Store(1)
			defer peer.close()
			workerPeer := &browserWorkerPeer{claims: worker.Claims{OrgID: browserTestOrg, SessionID: browserTestSession, Epoch: 7},
				send: make(chan []byte, 16), done: make(chan struct{})}
			key := browserRelayKey(browserTestOrg, browserTestSession)
			server.browserStreams.registerViewer(key, peer)
			server.browserStreams.registerWorker(key, workerPeer)
			if test.replaced {
				replacement := &browserViewerPeer{subject: "user-1", done: make(chan struct{}), frames: browserstream.NewLatest()}
				defer replacement.close()
				server.browserStreams.registerViewer(key, replacement)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				_ = server.readBrowserViewer(ctx, conn, key, peer)
			}))
			defer httpServer.Close()
			conn, _, err := websocket.Dial(ctx, "ws"+httpServer.URL[len("http"):], nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			controls := []browserstream.Control{
				{Type: "ping"}, {Type: "viewport"}, {Type: "input", Kind: "pointerMove"},
				{Type: "input", Kind: "keyDown", StreamEpoch: 2},
				{Type: "input", Kind: "text", Text: "first"}, {Type: "input", Kind: "text", Text: "second"},
			}
			for index, control := range controls {
				control.Version, control.InputSeq = browserstream.Version, uint64(index+1)
				if control.StreamEpoch == 0 {
					control.StreamEpoch = 1
				}
				payload, _ := json.Marshal(control)
				if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
					t.Fatal(err)
				}
				if test.replaced {
					if _, _, err := conn.Read(ctx); err == nil {
						t.Fatal("replaced viewer remained connected")
					}
					break
				}
				select {
				case <-workerPeer.send:
					if index >= 4 && (!test.operate || test.storeErr != nil) {
						t.Fatal("unauthorized input forwarded")
					}
				case <-peer.controls:
					if test.operate && (test.storeErr == nil || index < 4) {
						t.Fatal("valid control rejected")
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if index < 4 && store.refreshes.Load() != 0 {
					t.Fatal("passive or stale input renewed lease")
				}
			}
			want := int32(0)
			if test.operate && !test.replaced {
				want = 1
				if test.storeErr != nil {
					want = 2
				}
			}
			if got := store.refreshes.Load(); got != want {
				t.Fatalf("refreshes=%d, want %d", got, want)
			}
		})
	}
}

func TestBrowserViewerOriginPolicy(t *testing.T) {
	server := &Server{browserViewerOrigins: []string{"https://desktop.example.com"}}
	tests := []struct {
		name   string
		origin string
		host   string
		want   bool
	}{
		{name: "missing origin", host: "cloud.example.com", want: true},
		{name: "packaged desktop", origin: "null", host: "cloud.example.com", want: true},
		{name: "file origin", origin: "file://", host: "cloud.example.com", want: true},
		{name: "same host", origin: "https://cloud.example.com", host: "cloud.example.com", want: true},
		{name: "configured origin", origin: "https://desktop.example.com", host: "cloud.example.com", want: true},
		{name: "configured trailing slash", origin: "https://desktop.example.com/", host: "cloud.example.com", want: true},
		{name: "unconfigured origin", origin: "https://attacker.example.com", host: "cloud.example.com", want: false},
		{name: "invalid origin", origin: "chrome-extension://viewer", host: "cloud.example.com", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://"+test.host+"/stream", nil)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if got := server.browserViewerOriginAllowed(request); got != test.want {
				t.Fatalf("browserViewerOriginAllowed() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestBrowserRelayCarriesFrameAndInputWithoutDurableQueue(t *testing.T) {
	server := &Server{
		store: browserRelayStore{}, browserViewerEnabled: true,
		browserStreams: newBrowserStreams(),
	}
	claims := worker.Claims{
		OrgID: browserTestOrg, SessionID: browserTestSession, WorkerID: "worker-1",
		Epoch: 7, Scopes: []string{"worker:transport"},
	}
	router := chi.NewRouter()
	router.Get("/worker/sessions/{sessionId}/browser-stream", func(w http.ResponseWriter, r *http.Request) {
		server.workerBrowserStream(w, r.WithContext(context.WithValue(r.Context(), workerContextKey{}, claims)))
	})
	router.Get("/orgs/{orgId}/sessions/{sessionId}/browser-view/stream", server.connectBrowserViewer)
	httpServer := httptest.NewServer(router)
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	workerURL := "ws" + httpServer.URL[len("http"):] + "/worker/sessions/" + browserTestSession + "/browser-stream"
	workerConn, _, err := websocket.Dial(ctx, workerURL, nil)
	if err != nil {
		t.Fatalf("dial worker browser stream: %v", err)
	}
	defer workerConn.CloseNow()
	viewerURL := "ws" + httpServer.URL[len("http"):] + "/orgs/" + browserTestOrg + "/sessions/" + browserTestSession + "/browser-view/stream?ticket=one-use"
	viewerConn, _, err := websocket.Dial(ctx, viewerURL, nil)
	if err != nil {
		t.Fatalf("dial viewer browser stream: %v", err)
	}
	defer viewerConn.CloseNow()

	kind, payload, err := workerConn.Read(ctx)
	if err != nil || kind != websocket.MessageText {
		t.Fatalf("read attach: kind=%v err=%v", kind, err)
	}
	var attach browserstream.Control
	if json.Unmarshal(payload, &attach) != nil || attach.Type != "attach" || attach.Version != browserstream.Version {
		t.Fatalf("unexpected attach %s", payload)
	}
	frame, err := browserstream.EncodeFrame(browserstream.Frame{
		StreamEpoch: 1, Sequence: 1, Width: 800, Height: 600,
		CapturedMS: 4, TargetID: "tab-1", JPEG: []byte{0xff, 0xd8, 0xff, 0xd9},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := workerConn.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("write worker frame: %v", err)
	}
	for {
		kind, payload, err = viewerConn.Read(ctx)
		if err != nil {
			t.Fatalf("read viewer frame: %v", err)
		}
		if kind == websocket.MessageBinary {
			break
		}
	}
	decoded, err := browserstream.DecodeFrame(payload)
	if err != nil || decoded.TargetID != "tab-1" || decoded.Sequence != 1 {
		t.Fatalf("decoded frame=%+v err=%v", decoded, err)
	}

	input, _ := json.Marshal(browserstream.Control{
		Type: "input", Version: browserstream.Version, InputSeq: 9,
		Kind: "pointerDown", X: 10, Y: 20, Button: "left", Buttons: 1,
	})
	if err := viewerConn.Write(ctx, websocket.MessageText, input); err != nil {
		t.Fatalf("write viewer input: %v", err)
	}
	kind, payload, err = workerConn.Read(ctx)
	if err != nil || kind != websocket.MessageText {
		t.Fatalf("read worker input: kind=%v err=%v", kind, err)
	}
	var relayed browserstream.Control
	if json.Unmarshal(payload, &relayed) != nil || relayed.Type != "input" || relayed.InputSeq != 9 {
		t.Fatalf("unexpected relayed input %s", payload)
	}
}
