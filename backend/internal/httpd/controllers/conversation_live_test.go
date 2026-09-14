package controllers_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type liveHTTPProvider struct {
	events    chan ports.ChatEvent
	closeOnce sync.Once
}

func (p *liveHTTPProvider) ProviderConversationID() string { return "live-thread" }
func (p *liveHTTPProvider) Capabilities() ports.ChatCapabilities {
	return ports.ChatCapabilities{
		ports.ChatCapabilityStreaming: true, ports.ChatCapabilityApprovals: true,
		ports.ChatCapabilityInterrupt: true, ports.ChatCapabilityResume: true,
	}
}
func (p *liveHTTPProvider) Events() <-chan ports.ChatEvent { return p.events }
func (p *liveHTTPProvider) SendTurn(context.Context, ports.ChatUserMessage) (ports.ChatTurnRef, error) {
	return ports.ChatTurnRef{ProviderTurnID: "live-turn"}, nil
}
func (p *liveHTTPProvider) Interrupt(context.Context, string) error { return nil }
func (p *liveHTTPProvider) ResolveRequest(context.Context, string, ports.ChatDecision) error {
	return nil
}
func (p *liveHTTPProvider) Close() error {
	p.closeOnce.Do(func() { close(p.events) })
	return nil
}

type liveHTTPDriver struct{ provider *liveHTTPProvider }

func (d liveHTTPDriver) Harness() domain.AgentHarness { return domain.HarnessCodex }
func (d liveHTTPDriver) Probe(context.Context) (ports.ChatCapabilities, error) {
	return d.provider.Capabilities(), nil
}
func (d liveHTTPDriver) Start(context.Context, ports.ChatStartConfig) (ports.ChatConversation, error) {
	return d.provider, nil
}
func (d liveHTTPDriver) Resume(context.Context, ports.ChatResumeConfig) (ports.ChatConversation, error) {
	return d.provider, nil
}
func (d liveHTTPDriver) Driver(domain.AgentHarness) (ports.ChatDriver, error) { return d, nil }
func (d liveHTTPDriver) SupportsChat(domain.AgentHarness) bool                { return true }

type liveHTTPSubscription struct {
	ctx context.Context
	sub *chatsvc.LiveSubscription
}

type liveHTTPService struct {
	*chatsvc.Service
	subscribed chan liveHTTPSubscription
}

func (s *liveHTTPService) SubscribeLive(ctx context.Context, id domain.SessionID) (*chatsvc.LiveSubscription, error) {
	sub, err := s.Service.SubscribeLive(ctx, id)
	if err == nil {
		s.subscribed <- liveHTTPSubscription{ctx: ctx, sub: sub}
	}
	return sub, err
}

// Block the persistence boundary without replacing its transaction or projection.
type liveHTTPBlockedStore struct {
	chatsvc.Store
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *liveHTTPBlockedStore) ProjectProviderEvent(ctx context.Context, conversationID string, session domain.SessionID, generation, providerEventID, method, payload string, now time.Time, project func(context.Context) error) (bool, error) {
	if method == string(ports.ChatEventMessageDelta) {
		s.once.Do(func() { close(s.started) })
		<-s.release
	}
	return s.Store.ProjectProviderEvent(ctx, conversationID, session, generation, providerEventID, method, payload, now, project)
}

func liveHTTPTestServer(t *testing.T) (*httptest.Server, *liveHTTPService, *liveHTTPProvider, *liveHTTPBlockedStore) {
	t.Helper()
	dir := t.TempDir()
	st := sqlitetest.MustOpenAt(t, dir)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := st.UpsertProject(ctx, domain.ProjectRecord{ID: "live", Path: dir, RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []domain.SessionMode{domain.SessionModeChat, domain.SessionModeTUI, domain.SessionModeChat} {
		if _, err := st.CreateSession(ctx, domain.SessionRecord{ProjectID: "live", Kind: domain.KindWorker, Harness: domain.HarnessCodex, Mode: mode, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	provider := &liveHTTPProvider{events: make(chan ports.ChatEvent, 8)}
	blocked := &liveHTTPBlockedStore{Store: st, started: make(chan struct{}), release: make(chan struct{})}
	var ids atomic.Int64
	svc := &liveHTTPService{Service: chatsvc.New(chatsvc.Options{
		Store: blocked, Sessions: st, Drivers: liveHTTPDriver{provider: provider},
		NewID: func() string { return fmt.Sprintf("live-id-%d", ids.Add(1)) },
		Reader: chatsvc.SnapshotReaderFunc(func(ctx context.Context, id string) (chatsvc.ConversationRows, error) {
			rows, err := st.LoadConversationSnapshot(ctx, id)
			return chatsvc.ConversationRows{Conversation: rows.Conversation, ActiveBranch: rows.ActiveBranch, Turns: rows.Turns, Messages: rows.Messages, Activities: rows.Activities}, err
		}),
	}), subscribed: make(chan liveHTTPSubscription, 1)}
	if _, err := svc.Start(ctx, chatsvc.StartConfig{SessionID: "live-1", ProjectID: "live", Harness: domain.HarnessCodex, WorkspacePath: dir}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-blocked.release:
		default:
			close(blocked.release)
		}
		_ = svc.Stop(context.Background(), "live-1")
	})
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{RequestTimeout: time.Second}, slog.New(slog.DiscardHandler), nil, httpd.APIDeps{
		Sessions: newFakeSessionService(), Conversations: svc,
	}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return srv, svc, provider, blocked
}

func readConversationLiveFrame(t *testing.T, reader *bufio.Reader) controllers.ConversationLiveResponse {
	t.Helper()
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read event name: %v", err)
	}
	if line != "event: conversation_text\n" {
		t.Fatalf("event name = %q", line)
	}
	line, err = reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read event data: %v", err)
	}
	var frame controllers.ConversationLiveResponse
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("data frame = %q", line)
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
		t.Fatal(err)
	}
	if line, err = reader.ReadString('\n'); err != nil || line != "\n" {
		t.Fatalf("frame delimiter = %q, %v", line, err)
	}
	return frame
}

func TestConversationLiveStreamPrecedesPersistenceAndReconcilesSnapshot(t *testing.T) {
	srv, svc, provider, blocked := liveHTTPTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/sessions/live-1/conversation/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("stream response: %d %v", resp.StatusCode, resp.Header)
	}
	call := <-svc.subscribed
	if deadline, ok := call.ctx.Deadline(); ok {
		t.Fatalf("live stream inherited REST timeout: %v", deadline)
	}
	reader := bufio.NewReader(resp.Body)
	initial := readConversationLiveFrame(t, reader)
	if initial.Generation == "" || initial.ConversationID == "" || initial.BranchID == "" || initial.Sequence != 0 || initial.AfterSequence != 0 || initial.Events == nil || len(initial.Events) != 0 {
		t.Fatalf("initial frame = %+v", initial)
	}
	turn, err := svc.Send(ctx, "live-1", ports.ChatUserMessage{Text: "reply", ClientMessageID: "live-request", Origin: domain.MessageOriginHuman})
	if err != nil {
		t.Fatal(err)
	}
	provider.events <- ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: turn.ProviderTurnID}
	provider.events <- ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "live-message", Delta: "hello "}
	select {
	case <-blocked.started:
	case <-ctx.Done():
		t.Fatal("delta never reached persistence")
	}
	provider.events <- ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "live-message", Delta: "world"}
	var text string
	var last controllers.ConversationLiveResponse
	for text != "hello world" {
		last = readConversationLiveFrame(t, reader)
		if last.Generation != initial.Generation || last.ConversationID != initial.ConversationID || last.BranchID != initial.BranchID {
			t.Fatalf("stream identity changed: %+v", last)
		}
		for _, event := range last.Events {
			if event.Kind != "message.delta" || event.ProviderItemID != "live-message" || event.ProviderTurnID != turn.ProviderTurnID || event.Sequence <= last.AfterSequence {
				t.Fatalf("delta frame = %+v", last)
			}
			if _, err := time.Parse(time.RFC3339Nano, event.CreatedAt); err != nil {
				t.Fatalf("createdAt = %q", event.CreatedAt)
			}
			text += event.Delta
		}
		if !strings.HasPrefix("hello world", text) {
			t.Fatalf("live text = %q", text)
		}
	}
	close(blocked.release)
	provider.events <- ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: turn.ProviderTurnID, TurnState: domain.TurnStateCompleted}
	for {
		snapshotResponse, err := srv.Client().Get(srv.URL + "/api/v1/sessions/live-1/conversation")
		if err != nil {
			t.Fatal(err)
		}
		var snapshot controllers.ConversationSnapshotResponse
		err = json.NewDecoder(snapshotResponse.Body).Decode(&snapshot)
		_ = snapshotResponse.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if snapshotResponse.StatusCode != http.StatusOK {
			t.Fatalf("snapshot status = %d", snapshotResponse.StatusCode)
		}
		if len(snapshot.Messages) == 2 && snapshot.Messages[1].Text == text && !snapshot.Messages[1].Streaming {
			if snapshot.Messages[1].ProviderItemID != "live-message" || snapshot.LiveGeneration != initial.Generation || snapshot.LiveSequence < last.Sequence {
				t.Fatalf("snapshot reconciliation = %+v", snapshot)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("persisted reply did not converge: %+v", snapshot)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	done := make(chan struct{})
	go func() {
		for open := true; open; {
			_, open = <-call.sub.Changed()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("client cancellation left the subscription open")
	}
}

func TestConversationLiveStreamEnforcesSessionModeAndControllerGuards(t *testing.T) {
	srv, _, _, _ := liveHTTPTestServer(t)
	for _, test := range []struct {
		session string
		status  int
		code    string
	}{
		{"missing", http.StatusNotFound, "SESSION_NOT_FOUND"},
		{"live-2", http.StatusConflict, "SESSION_MODE_MISMATCH"},
		{"live-3", http.StatusConflict, "CHAT_CONTROLLER_NOT_READY"},
	} {
		t.Run(test.session, func(t *testing.T) {
			resp, err := srv.Client().Get(srv.URL + "/api/v1/sessions/" + test.session + "/conversation/events")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != test.status || !strings.Contains(string(body), `"code":"`+test.code+`"`) || strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
				t.Fatalf("guard response = %d %s", resp.StatusCode, body)
			}
		})
	}
}
