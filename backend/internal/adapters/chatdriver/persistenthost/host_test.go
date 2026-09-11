package persistenthost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) >= 6 && os.Args[1] == "chat-host" {
		protocol := ProtocolRaw
		fingerprint := ""
		separator := 5
		if len(os.Args) > 5 && os.Args[5] == string(ProtocolACP) {
			protocol = ProtocolACP
			if len(os.Args) > 6 {
				fingerprint = os.Args[6]
			}
			separator = 7
		}
		if len(os.Args) <= separator || os.Args[separator] != "--" {
			os.Exit(2)
		}
		err := Run(context.Background(), Config{
			SessionID: os.Args[2], DataDir: os.Args[3], Workdir: os.Args[4],
			Env: os.Environ(), Argv: os.Args[separator+1:], Protocol: protocol,
			OwnershipFingerprint: fingerprint,
		})
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestProviderHelper(t *testing.T) {
	if os.Getenv("AO_CHAT_HOST_PROVIDER_HELPER") != "1" {
		return
	}
	if os.Getenv("AO_CHAT_HOST_ACP_HELPER") == "1" {
		runACPProviderHelper()
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var frame struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			continue
		}
		if frame.Method == "emit-later" {
			time.Sleep(50 * time.Millisecond)
			_, _ = fmt.Fprintln(os.Stdout, `{"method":"turn/completed","params":{"turn":{"id":"survived"}}}`)
			continue
		}
		if frame.Method == "request-approval" {
			_, _ = fmt.Fprintln(os.Stdout, `{"id":700,"method":"item/commandExecution/requestApproval","params":{"turnId":"turn-live"}}`)
			continue
		}
		if frame.Method == "" && frame.ID == 700 {
			_, _ = fmt.Fprintln(os.Stdout, `{"method":"approval/received","params":{"turnId":"turn-live"}}`)
			continue
		}
		_, _ = fmt.Fprintf(os.Stdout, `{"id":%d,"result":{"pid":%d}}`+"\n", frame.ID, os.Getpid())
	}
	os.Exit(0)
}

func runACPProviderHelper() {
	scanner := bufio.NewScanner(os.Stdin)
	var parkedPromptID json.RawMessage
	for scanner.Scan() {
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			continue
		}
		if frame.Method == "" && string(frame.ID) == `"permission-ledger"` && len(parkedPromptID) > 0 {
			_, _ = fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"acp-session-live","update":{"agent_message_chunk":{"content":{"type":"text","text":"approved"}}}}}`)
			_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", parkedPromptID)
			parkedPromptID = nil
			continue
		}
		switch frame.Method {
		case "initialize":
			_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"resume":{}}},"authMethods":[]}}`+"\n", frame.ID)
		case "session/new":
			_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"acp-session-live"}}`+"\n", frame.ID)
		case "session/prompt":
			_, _ = fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"acp-session-live","update":{"agent_message_chunk":{"content":{"type":"text","text":"before restart"}}}}}`)
			if os.Getenv("AO_CHAT_HOST_ACP_PERMISSION") == "1" {
				parkedPromptID = append(json.RawMessage(nil), frame.ID...)
				_, _ = fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","id":"permission-ledger","method":"session/request_permission","params":{"sessionId":"acp-session-live","options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}]}}`)
				continue
			}
			time.Sleep(100 * time.Millisecond)
			_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn","providerPid":%d}}`+"\n", frame.ID, os.Getpid())
		case "pid":
			_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"pid":%d}}`+"\n", frame.ID, os.Getpid())
		}
	}
}

func TestACPHostReplaysAcceptedInteractionAfterAttachmentDies(t *testing.T) {
	dataDir := t.TempDir()
	cfg := Config{
		SessionID: "acp-command-ledger", DataDir: dataDir, Workdir: t.TempDir(), Protocol: ProtocolACP,
		Env: append(os.Environ(),
			"AO_CHAT_HOST_PROVIDER_HELPER=1",
			"AO_CHAT_HOST_ACP_HELPER=1",
			"AO_CHAT_HOST_ACP_PERMISSION=1",
		),
		Argv: []string{os.Args[0], "-test.run=TestProviderHelper"},
	}
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), cfg) }()
	d := awaitDescriptor(t, dataDir, cfg.SessionID)
	t.Cleanup(func() {
		_ = Shutdown(context.Background(), dataDir, cfg.SessionID)
		<-done
	})

	first := awaitAttach(t, d)
	reader := bufio.NewReader(first.Stdout)
	sendFrame(t, first, `{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"acp-session-live","prompt":[]}}`)
	_ = readFrame(t, reader) // prompt update
	permission := readFrame(t, reader)
	requestID := frameMetaString(t, permission, ACPRequestIDMetaKey)
	if requestID == "" {
		t.Fatalf("permission has no stable request id: %s", permission)
	}

	conn := first.Stdin.(net.Conn)
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	sendFrame(t, first, fmt.Sprintf(
		`{"jsonrpc":"2.0","id":99,"method":"_ao/persistent_interaction_command","params":{"requestId":%q,"kind":"approval","decision":{"id":"allow"}}}`,
		requestID,
	))
	accepted := readFrame(t, reader)
	if !bytes.Contains(accepted, []byte(`"id":99`)) || !bytes.Contains(accepted, []byte(`"eventId"`)) {
		t.Fatalf("command acceptance = %s", accepted)
	}
	_ = first.Stdin.Close() // daemon dies before its SDK answers the provider request

	second := awaitAttach(t, d)
	secondReader := bufio.NewReader(second.Stdout)
	replayedUpdate := readFrame(t, secondReader)
	if !bytes.Contains(replayedUpdate, []byte(`"session/update"`)) {
		t.Fatalf("first replay = %s, want prompt update", replayedUpdate)
	}
	replayedPermission := readFrame(t, secondReader)
	if !bytes.Contains(replayedPermission, []byte(`"session/request_permission"`)) {
		t.Fatalf("second replay = %s, want pending permission", replayedPermission)
	}
	replayedCommand := readFrame(t, secondReader)
	if !bytes.Contains(replayedCommand, []byte(`"_ao/persistent_interaction_command"`)) ||
		!bytes.Contains(replayedCommand, []byte(requestID)) {
		t.Fatalf("third replay = %s, want accepted command", replayedCommand)
	}
	_ = second.Stdin.Close()
}

func TestACPHostPreservesPromptCorrelationAndJournal(t *testing.T) {
	dataDir := t.TempDir()
	cfg := Config{
		SessionID: "acp-live", DataDir: dataDir, Workdir: t.TempDir(), Protocol: ProtocolACP,
		Env:  append(os.Environ(), "AO_CHAT_HOST_PROVIDER_HELPER=1", "AO_CHAT_HOST_ACP_HELPER=1"),
		Argv: []string{os.Args[0], "-test.run=TestProviderHelper"},
	}
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), cfg) }()
	d := awaitDescriptor(t, dataDir, cfg.SessionID)
	if d.Protocol != ProtocolACP {
		t.Fatalf("descriptor protocol = %q", d.Protocol)
	}
	first, err := attach(context.Background(), d, false)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(first.Stdout)
	sendFrame(t, first, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	_ = readFrame(t, reader)
	sendFrame(t, first, `{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp","mcpServers":[]}}`)
	_ = readFrame(t, reader)
	pid := requestProviderPID(t, first, 3, "pid")
	sendFrame(t, first, `{"jsonrpc":"2.0","id":4,"method":"session/prompt","params":{"sessionId":"acp-session-live","prompt":[]}}`)
	update := readFrame(t, reader)
	if !bytes.Contains(update, []byte(`"session/update"`)) || !bytes.Contains(update, []byte(ACPEventIDMetaKey)) {
		t.Fatalf("live update = %s", update)
	}
	_ = first.Stdin.Close()

	second := awaitAttach(t, d)
	if second.ACPState == nil || second.ACPState.SessionID != "acp-session-live" ||
		len(second.ACPState.InitializeResult) == 0 ||
		(!second.ACPState.ActivePrompt && second.ACPState.PendingResultEventID == "") {
		t.Fatalf("reconnect state = %+v", second.ACPState)
	}
	secondReader := bufio.NewReader(second.Stdout)
	replayedUpdate := readFrame(t, secondReader)
	if !bytes.Contains(replayedUpdate, []byte(`"session/update"`)) {
		t.Fatalf("replayed update = %s", replayedUpdate)
	}
	completion := readFrame(t, secondReader)
	if !bytes.Contains(completion, []byte(ACPPromptResultMethod)) ||
		!bytes.Contains(completion, []byte(fmt.Sprintf(`"providerPid":%d`, pid))) {
		t.Fatalf("replayed completion = %s", completion)
	}
	var completed struct {
		Params struct {
			EventID string `json:"eventId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(completion, &completed); err != nil || completed.Params.EventID == "" {
		t.Fatalf("completion event identity: %q: %v", completion, err)
	}
	sendFrame(t, second, fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"_ao/persistent_prompt_ack","params":{"eventId":%q}}`,
		completed.Params.EventID,
	))
	if got := requestProviderPID(t, second, 1, "pid"); got != pid {
		t.Fatalf("provider pid changed across daemon attachment: %d -> %d", pid, got)
	}
	_ = second.Stdin.Close()

	third := awaitAttach(t, d)
	if third.ACPState == nil || third.ACPState.PendingResultEventID != "" || third.ACPState.ActivePrompt {
		t.Fatalf("acknowledged state = %+v", third.ACPState)
	}
	if got := requestProviderPID(t, third, 1, "pid"); got != pid {
		t.Fatalf("provider pid changed after acknowledged replay: %d -> %d", pid, got)
	}
	_ = third.Stdin.Close()
	if err := Shutdown(context.Background(), dataDir, cfg.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func sendFrame(t *testing.T, transport *Transport, frame string) {
	t.Helper()
	if _, err := fmt.Fprintln(transport.Stdin, frame); err != nil {
		t.Fatalf("send frame: %v", err)
	}
}

func readFrame(t *testing.T, reader *bufio.Reader) []byte {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return line
}

func TestHostReconnectsSameProviderAndReplaysDetachedOutput(t *testing.T) {
	dataDir := t.TempDir()
	workdir := t.TempDir()
	cfg := Config{
		SessionID: "project-7",
		DataDir:   dataDir,
		Workdir:   workdir,
		Env:       append(os.Environ(), "AO_CHAT_HOST_PROVIDER_HELPER=1"),
		Argv:      []string{os.Args[0], "-test.run=TestProviderHelper"},
	}
	hostDone := make(chan error, 1)
	go func() { hostDone <- Run(context.Background(), cfg) }()

	d := awaitDescriptor(t, dataDir, cfg.SessionID)
	first, err := attach(context.Background(), d, false)
	if err != nil {
		t.Fatalf("first attach: %v", err)
	}
	if _, err := attach(context.Background(), d, true); !errors.Is(err, ErrAttached) {
		t.Fatalf("concurrent attach error = %v, want ErrAttached", err)
	}
	pid := requestProviderPID(t, first, 7, "pid")
	if _, err := fmt.Fprintln(first.Stdin, `{"id":8,"method":"emit-later"}`); err != nil {
		t.Fatalf("send delayed frame: %v", err)
	}
	if err := first.Stdin.Close(); err != nil {
		t.Fatalf("detach: %v", err)
	}

	var second *Transport
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		second, err = attach(context.Background(), d, true)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrAttached) {
			t.Fatalf("reattach: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if second == nil {
		t.Fatal("host never released first controller")
	}
	if !second.Reconnected || second.NextRequestID != 8 {
		t.Fatalf("reattach metadata = reconnected:%v next:%d", second.Reconnected, second.NextRequestID)
	}

	_ = second.Stdin.(interface{ SetReadDeadline(time.Time) error }).SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(second.Stdout).ReadBytes('\n')
	if err != nil || !json.Valid(line) {
		t.Fatalf("replayed output = %q err=%v", line, err)
	}
	var replay struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(line, &replay)
	if replay.Method != "turn/completed" {
		t.Fatalf("replayed method = %q", replay.Method)
	}
	if got := requestProviderPID(t, second, 9, "pid-again"); got != pid {
		t.Fatalf("provider pid changed across daemon detach: %d -> %d", pid, got)
	}
	_ = second.Stdin.Close()
	if err := Shutdown(context.Background(), dataDir, cfg.SessionID); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case err := <-hostDone:
		if err != nil {
			t.Fatalf("host exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("host did not exit after explicit shutdown")
	}
}

func TestConnectOrStartLaunchesDetachedHost(t *testing.T) {
	dataDir := t.TempDir()
	cfg := Config{
		SessionID: "detached", DataDir: dataDir, Workdir: t.TempDir(),
		Env:  append(os.Environ(), "AO_CHAT_HOST_PROVIDER_HELPER=1"),
		Argv: []string{os.Args[0], "-test.run=TestProviderHelper"},
	}
	transport, err := ConnectOrStart(context.Background(), cfg)
	if err != nil {
		t.Fatalf("ConnectOrStart: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background(), dataDir, cfg.SessionID) })
	if transport.Reconnected {
		t.Fatal("new detached host reported reconnect")
	}
	d, err := readDescriptor(dataDir, cfg.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if d.PID == os.Getpid() {
		t.Fatalf("host pid = test pid %d; process was not detached", d.PID)
	}
	if pid := requestProviderPID(t, transport, 1, "pid"); pid == os.Getpid() || pid == d.PID {
		t.Fatalf("provider pid %d was not a distinct child of detached host %d", pid, d.PID)
	}
	_ = transport.Stdin.Close()
	if err := Shutdown(context.Background(), dataDir, cfg.SessionID); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	path, _ := descriptorPath(dataDir, cfg.SessionID)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("detached host did not remove its descriptor after shutdown")
}

func TestHostReplaysUnansweredServerRequestAfterDetach(t *testing.T) {
	dataDir := t.TempDir()
	cfg := Config{SessionID: "approval", DataDir: dataDir, Workdir: t.TempDir(),
		Env:  append(os.Environ(), "AO_CHAT_HOST_PROVIDER_HELPER=1"),
		Argv: []string{os.Args[0], "-test.run=TestProviderHelper"}}
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), cfg) }()
	d := awaitDescriptor(t, dataDir, cfg.SessionID)
	first, err := attach(context.Background(), d, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(first.Stdin, `{"id":5,"method":"request-approval"}`); err != nil {
		t.Fatal(err)
	}
	firstReader := bufio.NewReader(first.Stdout)
	line, err := firstReader.ReadBytes('\n')
	if err != nil || !bytes.Contains(line, []byte(`"id":700`)) {
		t.Fatalf("first server request = %q err=%v", line, err)
	}
	_ = first.Stdin.Close()

	second := awaitAttach(t, d)
	secondReader := bufio.NewReader(second.Stdout)
	line, err = secondReader.ReadBytes('\n')
	if err != nil || !bytes.Contains(line, []byte(`"id":700`)) {
		t.Fatalf("replayed server request = %q err=%v", line, err)
	}
	if _, err := fmt.Fprintln(second.Stdin, `{"id":700,"result":{"decision":"accept"}}`); err != nil {
		t.Fatal(err)
	}
	line, err = secondReader.ReadBytes('\n')
	if err != nil || !bytes.Contains(line, []byte(`"method":"approval/received"`)) {
		t.Fatalf("provider did not continue after replayed approval = %q err=%v", line, err)
	}
	_ = second.Stdin.Close()
	if err := Shutdown(context.Background(), dataDir, cfg.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAttachRejectsBadCapabilityAndVersion(t *testing.T) {
	dataDir := t.TempDir()
	workdir := t.TempDir()
	cfg := Config{SessionID: "project-8", DataDir: dataDir, Workdir: workdir,
		Env:  append(os.Environ(), "AO_CHAT_HOST_PROVIDER_HELPER=1"),
		Argv: []string{os.Args[0], "-test.run=TestProviderHelper"}}
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), cfg) }()
	d := awaitDescriptor(t, dataDir, cfg.SessionID)
	bad := d
	bad.Token = "wrong"
	if _, err := attach(context.Background(), bad, true); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad token error = %v", err)
	}
	bad = d
	bad.Version++
	if _, err := attach(context.Background(), bad, true); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("bad version error = %v", err)
	}
	if err := Shutdown(context.Background(), dataDir, cfg.SessionID); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestAttachHonorsContextAfterDial(t *testing.T) {
	address, accepted := silentHandshakePeer(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, attachErr := attach(ctx, Descriptor{
			Version: ProtocolVersion,
			Address: address,
			Token:   "token",
		}, true)
		result <- attachErr
	}()

	serverConn := awaitSilentHandshake(t, accepted)
	defer func() { _ = serverConn.Close() }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("attach error = %v, want context canceled", err)
		}
	case <-time.After(3 * time.Second):
		_ = serverConn.Close()
		<-result
		t.Fatal("attach ignored its context after connecting")
	}
}

func TestConnectOrStartTimesOutSilentLiveHost(t *testing.T) {
	address, accepted := silentHandshakePeer(t)
	dataDir := t.TempDir()
	const sessionID = "silent-connect"
	workdir := t.TempDir()
	cfg := Config{
		SessionID: sessionID,
		DataDir:   dataDir,
		Workdir:   workdir,
		Argv:      []string{os.Args[0], "-test.run=TestProviderHelper"},
	}
	if err := writeDescriptor(dataDir, Descriptor{
		Version: ProtocolVersion, SessionID: sessionID, OwnershipFingerprint: cfg.OwnershipFingerprint,
		Address: address, Token: "token", PID: os.Getpid(),
	}); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := ConnectOrStart(context.Background(), cfg)
		result <- err
	}()

	serverConn := awaitSilentHandshake(t, accepted)
	defer func() { _ = serverConn.Close() }()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrOwnershipInconclusive) {
			t.Fatalf("connect error = %v, want deadline exceeded ownership-inconclusive error", err)
		}
	case <-time.After(2 * time.Second):
		_ = serverConn.Close()
		<-result
		t.Fatal("connect to silent live host exceeded the handshake limit")
	}
}

func TestConnectOrStartRejectsChangedProviderOwner(t *testing.T) {
	dataDir := t.TempDir()
	cfg := Config{SessionID: "launch-fingerprint", DataDir: dataDir, Workdir: t.TempDir(),
		Env:  append(os.Environ(), "AO_CHAT_HOST_PROVIDER_HELPER=1"),
		Argv: []string{os.Args[0], "-test.run=TestProviderHelper"}, Protocol: ProtocolACP, OwnershipFingerprint: "owner"}
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), cfg) }()
	_ = awaitDescriptor(t, dataDir, cfg.SessionID)
	t.Cleanup(func() {
		_ = Shutdown(context.Background(), dataDir, cfg.SessionID)
		<-done
	})

	changed := cfg
	changed.OwnershipFingerprint = "different-owner"
	if _, err := ConnectOrStart(context.Background(), changed); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("changed launch error = %v, want ErrIncompatible", err)
	}
}

func TestConnectOrStartPreparesOnlyWhenLaunchingProvider(t *testing.T) {
	t.Run("live host", func(t *testing.T) {
		dataDir := t.TempDir()
		cfg := Config{
			SessionID: "prepare-live", DataDir: dataDir, Workdir: t.TempDir(),
			Env:  append(os.Environ(), "AO_CHAT_HOST_PROVIDER_HELPER=1"),
			Argv: []string{os.Args[0], "-test.run=TestProviderHelper"},
		}
		done := make(chan error, 1)
		go func() { done <- Run(context.Background(), cfg) }()
		_ = awaitDescriptor(t, dataDir, cfg.SessionID)
		t.Cleanup(func() {
			_ = Shutdown(context.Background(), dataDir, cfg.SessionID)
			<-done
		})
		prepareCalls := 0
		cfg.Prepare = func(context.Context) (PreparedProvider, error) {
			prepareCalls++
			return PreparedProvider{}, errors.New("must not prepare a live host")
		}
		transport, err := ConnectOrStart(context.Background(), cfg)
		if err != nil {
			t.Fatalf("ConnectOrStart: %v", err)
		}
		_ = transport.Stdin.Close()
		if prepareCalls != 0 {
			t.Fatalf("live host preparation calls = %d", prepareCalls)
		}
	})

	t.Run("new host", func(t *testing.T) {
		dataDir := t.TempDir()
		cfg := Config{
			SessionID: "prepare-new", DataDir: dataDir, Workdir: t.TempDir(),
			Env: os.Environ(), Argv: []string{os.Args[0], "-test.run=TestProviderHelper"},
		}
		prepareCalls := 0
		cfg.Prepare = func(context.Context) (PreparedProvider, error) {
			prepareCalls++
			return PreparedProvider{
				Env: append(os.Environ(), "AO_CHAT_HOST_PROVIDER_HELPER=1"), Argv: cfg.Argv,
			}, nil
		}
		transport, err := ConnectOrStart(context.Background(), cfg)
		if err != nil {
			t.Fatalf("ConnectOrStart: %v", err)
		}
		_ = transport.Stdin.Close()
		t.Cleanup(func() { _ = Shutdown(context.Background(), dataDir, cfg.SessionID) })
		if prepareCalls != 1 {
			t.Fatalf("new host preparation calls = %d, want 1", prepareCalls)
		}
	})
}

func TestValidateDescriptorAllowsLegacyRawHostOnly(t *testing.T) {
	cfg := Config{Workdir: t.TempDir(), Argv: []string{"provider"}}
	if err := validateDescriptor(cfg, Descriptor{Protocol: ProtocolRaw}); err != nil {
		t.Fatalf("legacy raw descriptor: %v", err)
	}
	cfg.Protocol = ProtocolACP
	if err := validateDescriptor(cfg, Descriptor{Protocol: ProtocolACP, OwnershipFingerprint: "owner"}); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("legacy ACP descriptor error = %v, want ErrIncompatible", err)
	}
}

func TestShutdownHonorsContextAfterDial(t *testing.T) {
	address, accepted := silentHandshakePeer(t)
	dataDir := t.TempDir()
	const sessionID = "silent-shutdown"
	if err := writeDescriptor(dataDir, Descriptor{
		Version:   ProtocolVersion,
		SessionID: sessionID,
		Address:   address,
		Token:     "token",
		PID:       os.Getpid(),
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- Shutdown(ctx, dataDir, sessionID) }()

	serverConn := awaitSilentHandshake(t, accepted)
	defer func() { _ = serverConn.Close() }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown error = %v, want context canceled", err)
		}
	case <-time.After(3 * time.Second):
		_ = serverConn.Close()
		<-result
		t.Fatal("shutdown ignored its context after connecting")
	}
}

func TestShutdownPreservesUnknownOwnerButAcceptsConclusiveDeath(t *testing.T) {
	dataDir := t.TempDir()
	if err := Shutdown(context.Background(), dataDir, "missing"); err != nil {
		t.Fatalf("missing host: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	for _, tc := range []struct {
		name    string
		version int
		pid     int
		want    error
	}{
		{"dead", ProtocolVersion, 2147483647, nil},
		{"incompatible", ProtocolVersion + 1, os.Getpid(), ErrIncompatible},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := writeDescriptor(dataDir, Descriptor{
				Version: tc.version, SessionID: tc.name, Address: address, PID: tc.pid, Token: "test",
			}); err != nil {
				t.Fatal(err)
			}
			if err := Shutdown(context.Background(), dataDir, tc.name); !errors.Is(err, tc.want) {
				t.Fatalf("shutdown = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestShutdownTimesOutSilentLiveHost(t *testing.T) {
	address, accepted := silentHandshakePeer(t)
	dataDir := t.TempDir()
	const sessionID = "silent-shutdown-timeout"
	if err := writeDescriptor(dataDir, Descriptor{
		Version:   ProtocolVersion,
		SessionID: sessionID,
		Address:   address,
		Token:     "token",
		PID:       os.Getpid(),
	}); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() { result <- Shutdown(context.Background(), dataDir, sessionID) }()

	serverConn := awaitSilentHandshake(t, accepted)
	defer func() { _ = serverConn.Close() }()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown error = %v, want context deadline exceeded", err)
		}
	case <-time.After(2 * time.Second):
		_ = serverConn.Close()
		<-result
		t.Fatal("shutdown of silent live host exceeded the handshake limit")
	}
}

func TestAttachedTransportOutlivesHandshakeContext(t *testing.T) {
	dataDir := t.TempDir()
	cfg := Config{SessionID: "context-detached", DataDir: dataDir, Workdir: t.TempDir(),
		Env:  append(os.Environ(), "AO_CHAT_HOST_PROVIDER_HELPER=1"),
		Argv: []string{os.Args[0], "-test.run=TestProviderHelper"}}
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), cfg) }()
	d := awaitDescriptor(t, dataDir, cfg.SessionID)

	ctx, cancel := context.WithCancel(context.Background())
	transport, err := attach(ctx, d, true)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if pid := requestProviderPID(t, transport, 1, "still-connected"); pid <= 0 {
		t.Fatalf("provider pid after handshake cancellation = %d", pid)
	}

	_ = transport.Stdin.Close()
	if err := Shutdown(context.Background(), dataDir, cfg.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type silentHandshake struct {
	conn net.Conn
	err  error
}

func silentHandshakePeer(t *testing.T) (string, <-chan silentHandshake) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan silentHandshake, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_, acceptErr = bufio.NewReader(conn).ReadBytes('\n')
		}
		accepted <- silentHandshake{conn: conn, err: acceptErr}
	}()
	return listener.Addr().String(), accepted
}

func awaitSilentHandshake(t *testing.T, accepted <-chan silentHandshake) net.Conn {
	t.Helper()
	select {
	case result := <-accepted:
		if result.err != nil {
			t.Fatalf("accept silent handshake: %v", result.err)
		}
		return result.conn
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for silent handshake")
		return nil
	}
}

func TestReconcileKeepsDurableSessionAndStopsOrphan(t *testing.T) {
	dataDir := t.TempDir()
	workdir := t.TempDir()
	start := func(sessionID string) <-chan error {
		done := make(chan error, 1)
		cfg := Config{SessionID: sessionID, DataDir: dataDir, Workdir: workdir,
			Env:  append(os.Environ(), "AO_CHAT_HOST_PROVIDER_HELPER=1"),
			Argv: []string{os.Args[0], "-test.run=TestProviderHelper"}}
		go func() { done <- Run(context.Background(), cfg) }()
		_ = awaitDescriptor(t, dataDir, sessionID)
		return done
	}
	keepDone := start("keep")
	orphanDone := start("orphan")

	if err := Reconcile(context.Background(), dataDir, map[string]struct{}{"keep": {}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	select {
	case err := <-orphanDone:
		if err != nil {
			t.Fatalf("orphan host exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("orphan host survived reconciliation")
	}

	d := awaitDescriptor(t, dataDir, "keep")
	transport, err := attach(context.Background(), d, true)
	if err != nil {
		t.Fatalf("kept host is not attachable: %v", err)
	}
	_ = transport.Stdin.Close()
	if err := Shutdown(context.Background(), dataDir, "keep"); err != nil {
		t.Fatalf("shutdown kept host: %v", err)
	}
	<-keepDone
}

func TestHostLaunchLockFencesConcurrentProvider(t *testing.T) {
	dataDir := t.TempDir()
	cfg := Config{SessionID: "fenced", DataDir: dataDir, Workdir: t.TempDir(),
		Env:  append(os.Environ(), "AO_CHAT_HOST_PROVIDER_HELPER=1"),
		Argv: []string{os.Args[0], "-test.run=TestProviderHelper"}}
	firstDone := make(chan error, 1)
	go func() { firstDone <- Run(context.Background(), cfg) }()
	d := awaitDescriptor(t, dataDir, cfg.SessionID)
	if err := Run(context.Background(), cfg); !errors.Is(err, ErrHostExists) {
		t.Fatalf("second Run error = %v, want ErrHostExists", err)
	}
	if err := Shutdown(context.Background(), dataDir, cfg.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if processalive := d.PID; processalive <= 0 {
		t.Fatalf("host descriptor pid = %d", processalive)
	}
}

func TestAcquireHostLockReclaimsDeadOwner(t *testing.T) {
	dataDir := t.TempDir()
	sessionID := "stale-lock"
	dir, err := hostDir(dataDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := lockPath(dataDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("2147483647\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	release, err := acquireHostLock(dataDir, sessionID)
	if err != nil {
		t.Fatalf("acquire stale host lock: %v", err)
	}
	release()
}

func TestConnectOrStartWaitsForOldControllerToDetach(t *testing.T) {
	dataDir := t.TempDir()
	cfg := Config{SessionID: "overlap", DataDir: dataDir, Workdir: t.TempDir(),
		Env:  append(os.Environ(), "AO_CHAT_HOST_PROVIDER_HELPER=1"),
		Argv: []string{os.Args[0], "-test.run=TestProviderHelper"}}
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), cfg) }()
	d := awaitDescriptor(t, dataDir, cfg.SessionID)
	old, err := attach(context.Background(), d, false)
	if err != nil {
		t.Fatal(err)
	}
	connected := make(chan *Transport, 1)
	connectErr := make(chan error, 1)
	go func() {
		transport, err := ConnectOrStart(context.Background(), cfg)
		if err != nil {
			connectErr <- err
			return
		}
		connected <- transport
	}()
	select {
	case <-connected:
		t.Fatal("replacement attached while old controller still owned the host")
	case err := <-connectErr:
		t.Fatalf("replacement failed during expected overlap: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	_ = old.Stdin.Close()
	var replacement *Transport
	select {
	case replacement = <-connected:
	case err := <-connectErr:
		t.Fatalf("replacement attach: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("replacement did not attach after old controller detached")
	}
	_ = replacement.Stdin.Close()
	if err := Shutdown(context.Background(), dataDir, cfg.SessionID); err != nil {
		t.Fatal(err)
	}
	<-done
}

func awaitDescriptor(t *testing.T, dataDir, sessionID string) Descriptor {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		d, err := readDescriptor(dataDir, sessionID)
		if err == nil {
			return d
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("chat host descriptor not published")
	return Descriptor{}
}

func awaitAttach(t *testing.T, d Descriptor) *Transport {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		transport, err := attach(context.Background(), d, true)
		if err == nil {
			return transport
		}
		if !errors.Is(err, ErrAttached) {
			t.Fatalf("attach: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("host never released detached controller")
	return nil
}

func requestProviderPID(t *testing.T, transport *Transport, id int64, method string) int {
	t.Helper()
	if _, err := fmt.Fprintf(transport.Stdin, `{"id":%d,"method":%q}`+"\n", id, method); err != nil {
		t.Fatalf("provider request: %v", err)
	}
	line, err := bufio.NewReader(transport.Stdout).ReadBytes('\n')
	if err != nil {
		t.Fatalf("provider response: %v", err)
	}
	var response struct {
		ID     int64 `json:"id"`
		Result struct {
			PID int `json:"pid"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatalf("decode provider response %q: %v", line, err)
	}
	if response.ID != id || response.Result.PID <= 0 {
		t.Fatalf("provider response = id:%d pid:%s", response.ID, strconv.Itoa(response.Result.PID))
	}
	return response.Result.PID
}
