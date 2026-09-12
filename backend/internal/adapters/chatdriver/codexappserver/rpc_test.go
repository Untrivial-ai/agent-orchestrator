package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

const notificationReplayBurst = 4096

// fakeAppServer stands in for the `codex app-server` process over in-memory
// pipes, so the transport is tested without spawning anything.
type fakeAppServer struct {
	t *testing.T
	// requests receives every client->server request the fake saw.
	requests chan frame
	// toClient is written by the test to push frames at the client.
	toClient io.WriteCloser
}

func newFakeAppServer(t *testing.T, onReq serverRequestHandler) (*conn, *fakeAppServer) {
	t.Helper()

	clientReads, serverWrites := io.Pipe()
	serverReads, clientWrites := io.Pipe()

	fake := &fakeAppServer{
		t:        t,
		requests: make(chan frame, 32),
		toClient: serverWrites,
	}

	// Drain what the client sends so writes never block, recording each frame.
	go func() {
		br := bufio.NewReader(serverReads)
		for {
			line, err := readFrame(br)
			if err != nil {
				close(fake.requests)
				return
			}
			if len(line) == 0 {
				continue
			}
			var f frame
			if err := json.Unmarshal(line, &f); err != nil {
				continue
			}
			fake.requests <- f
		}
	}()

	log := slog.New(slog.DiscardHandler)
	c := newConn(clientWrites, clientReads, log, onReq)
	t.Cleanup(func() {
		_ = serverWrites.Close()
		_ = c.closeWrite()
	})
	return c, fake
}

// push writes a raw frame to the client.
func (f *fakeAppServer) push(raw string) {
	f.t.Helper()
	if _, err := io.WriteString(f.toClient, raw+"\n"); err != nil {
		f.t.Fatalf("push frame: %v", err)
	}
}

// nextRequest returns the next client->server request, failing on timeout.
func (f *fakeAppServer) nextRequest() frame {
	f.t.Helper()
	select {
	case got, ok := <-f.requests:
		if !ok {
			f.t.Fatal("fake app-server stream closed while awaiting a request")
		}
		return got
	case <-time.After(2 * time.Second):
		f.t.Fatal("timed out waiting for a client request")
		return frame{}
	}
}

func TestRequestReturnsDecodedResult(t *testing.T) {
	c, fake := newFakeAppServer(t, rejectAllServerRequests)

	type initResult struct {
		UserAgent string `json:"userAgent"`
		CodexHome string `json:"codexHome"`
	}

	done := make(chan error, 1)
	var got initResult
	go func() {
		done <- c.request(context.Background(), "initialize", map[string]any{"x": 1}, &got)
	}()

	req := fake.nextRequest()
	if req.Method != "initialize" {
		t.Fatalf("method = %q, want initialize", req.Method)
	}
	if req.ID == nil {
		t.Fatal("request carried no id")
	}
	fake.push(`{"id":` + string(*req.ID) + `,"result":{"userAgent":"ao/1","codexHome":"/tmp/.codex"}}`)

	if err := <-done; err != nil {
		t.Fatalf("request returned %v", err)
	}
	if got.UserAgent != "ao/1" || got.CodexHome != "/tmp/.codex" {
		t.Fatalf("decoded result = %+v", got)
	}
}

func TestRequestSurfacesServerError(t *testing.T) {
	c, fake := newFakeAppServer(t, rejectAllServerRequests)

	done := make(chan error, 1)
	go func() { done <- c.request(context.Background(), "thread/resume", nil, nil) }()

	req := fake.nextRequest()
	fake.push(`{"id":` + string(*req.ID) + `,"error":{"code":-32602,"message":"unknown thread"}}`)

	err := <-done
	if err == nil {
		t.Fatal("expected an error for a failed response")
	}
	if !strings.Contains(err.Error(), "unknown thread") {
		t.Fatalf("error %q does not carry the provider message", err)
	}
}

// A response that never arrives must not pin the caller forever, and the late
// response must not corrupt anything.
func TestRequestHonorsContextAndToleratesLateResponse(t *testing.T) {
	c, fake := newFakeAppServer(t, rejectAllServerRequests)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := c.request(ctx, "turn/start", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}

	// The request was still sent; answering it now must be discarded quietly.
	req := fake.nextRequest()
	fake.push(`{"id":` + string(*req.ID) + `,"result":{}}`)

	// A subsequent request still works, proving the pending map is clean.
	done := make(chan error, 1)
	go func() { done <- c.request(context.Background(), "thread/read", nil, nil) }()
	next := fake.nextRequest()
	fake.push(`{"id":` + string(*next.ID) + `,"result":{}}`)
	if err := <-done; err != nil {
		t.Fatalf("follow-up request failed: %v", err)
	}
}

func TestNotificationsAreFannedOut(t *testing.T) {
	c, fake := newFakeAppServer(t, rejectAllServerRequests)

	fake.push(`{"method":"turn/started","params":{"threadId":"t1","turnId":"u1"}}`)
	fake.push(`{"method":"item/agentMessage/delta","params":{"itemId":"m1","delta":"hi"}}`)

	for _, want := range []string{"turn/started", "item/agentMessage/delta"} {
		select {
		case n := <-c.notifs():
			if n.Method != want {
				t.Fatalf("notification method = %q, want %q", n.Method, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

func TestNotificationReplayBurstRetainsTerminalFrame(t *testing.T) {
	c, fake := newFakeAppServer(t, rejectAllServerRequests)
	for i := 0; i < notificationReplayBurst; i++ {
		fake.push(`{"method":"item/agentMessage/delta","params":{"delta":"x"}}`)
	}
	fake.push(`{"method":"turn/completed","params":{"turn":{"status":"completed"}}}`)

	foundTerminal := false
	for i := 0; i < notificationReplayBurst+1; i++ {
		if n := <-c.notifs(); n.Method == "turn/completed" {
			foundTerminal = true
		}
	}
	if !foundTerminal {
		t.Fatal("turn/completed was dropped when replay exceeded the notification buffer")
	}
}

func TestNotificationRelayAppliesBackpressureAtByteLimit(t *testing.T) {
	c, fake := newFakeAppServer(t, rejectAllServerRequests)
	const oversizedReplayFrames = 40
	largeFrame := `{"method":"item/agentMessage/delta","params":{"delta":"` +
		strings.Repeat("x", 1<<20) + `"}}`
	producerDone := make(chan error, 1)
	go func() {
		for i := 0; i < oversizedReplayFrames; i++ {
			if _, err := io.WriteString(fake.toClient, largeFrame+"\n"); err != nil {
				producerDone <- err
				return
			}
		}
		producerDone <- nil
	}()

	select {
	case err := <-producerDone:
		if err != nil {
			t.Fatalf("provider write failed before backpressure: %v", err)
		}
		t.Fatal("notification relay accepted more than its byte budget without backpressure")
	case <-time.After(3 * time.Second):
	}

	for i := 0; i < oversizedReplayFrames; i++ {
		select {
		case <-c.notifs():
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out draining notification %d", i)
		}
	}
	if err := <-producerDone; err != nil {
		t.Fatalf("provider did not resume after notifications drained: %v", err)
	}
}

// A frame this build cannot parse must be skipped, not fatal: the provider is
// free to add shapes we do not model yet.
func TestUnparseableFrameDoesNotKillConnection(t *testing.T) {
	c, fake := newFakeAppServer(t, rejectAllServerRequests)

	fake.push(`{"method":"broken",`)
	fake.push(`{"method":"turn/completed","params":{"turn":{"status":"completed"}}}`)

	select {
	case n := <-c.notifs():
		if n.Method != "turn/completed" {
			t.Fatalf("method = %q, want turn/completed", n.Method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("connection stopped delivering after an unparseable frame")
	}
}

// Approvals arrive as server->client requests and the provider blocks on them, so
// the handler's answer must get written back.
func TestServerRequestIsAnswered(t *testing.T) {
	handled := make(chan serverRequest, 1)
	c, fake := newFakeAppServer(t, func(_ context.Context, req serverRequest) (any, error) {
		handled <- req
		return map[string]any{"decision": "accept"}, nil
	})

	fake.push(`{"id":7,"method":"item/commandExecution/requestApproval","params":{"requestId":"r1","command":"date -u"}}`)

	select {
	case req := <-handled:
		if req.Method != "item/commandExecution/requestApproval" {
			t.Fatalf("handler saw %q", req.Method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler was never invoked")
	}

	reply := fake.nextRequest()
	if reply.ID == nil || string(*reply.ID) != "7" {
		t.Fatalf("reply id = %v, want 7", reply.ID)
	}
	var payload struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(reply.Result, &payload); err != nil {
		t.Fatalf("reply result not decodable: %v (raw %s)", err, reply.Result)
	}
	if payload.Decision != "accept" {
		t.Fatalf("decision = %q, want accept", payload.Decision)
	}
	_ = c
}

// A handler that refuses must send a JSON-RPC error rather than a fabricated
// decision — the provider must never read a refusal as an approval.
func TestServerRequestHandlerErrorBecomesRPCError(t *testing.T) {
	c, fake := newFakeAppServer(t, func(context.Context, serverRequest) (any, error) {
		return nil, errors.New("no controller for session")
	})

	fake.push(`{"id":9,"method":"item/fileChange/requestApproval","params":{}}`)

	reply := fake.nextRequest()
	if reply.Error == nil {
		t.Fatalf("reply carried no error object: %+v", reply)
	}
	if !strings.Contains(reply.Error.Message, "no controller") {
		t.Fatalf("error message = %q", reply.Error.Message)
	}
	_ = c
}

// When the process dies, an in-flight request must fail fast instead of hanging.
func TestProcessExitUnblocksPendingRequest(t *testing.T) {
	c, fake := newFakeAppServer(t, rejectAllServerRequests)

	done := make(chan error, 1)
	go func() { done <- c.request(context.Background(), "turn/start", nil, nil) }()
	fake.nextRequest()

	if err := fake.toClient.Close(); err != nil {
		t.Fatalf("close server side: %v", err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, ErrConnClosed) {
			t.Fatalf("err = %v, want ErrConnClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending request did not unblock when the process exited")
	}

	select {
	case <-c.wait():
	case <-time.After(2 * time.Second):
		t.Fatal("connection never reported done")
	}
}

func rejectAllServerRequests(context.Context, serverRequest) (any, error) {
	return nil, errors.New("no server requests expected in this test")
}
