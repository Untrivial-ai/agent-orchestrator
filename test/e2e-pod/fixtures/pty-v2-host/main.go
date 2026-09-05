// Command pty-v2-host is a test-only facsimile of the protocol-v2 detached
// PTY host shipped before host identity tokens were introduced. Build it to an
// executable named agent-orchestrator so the production legacy verifier can
// exercise its real OS process/listener checks without weakening RunHost.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const (
	msgTerminalData       byte = 0x01
	msgTerminalInput      byte = 0x02
	msgResize             byte = 0x03
	msgGetOutputReq       byte = 0x04
	msgGetOutputRes       byte = 0x05
	msgStatusReq          byte = 0x06
	msgStatusRes          byte = 0x07
	msgKillReq            byte = 0x08
	msgGetStyledOutputReq byte = 0x09
	msgGetStyledOutputRes byte = 0x0a

	maxFramePayload = 4 << 20
	maxReplayBytes  = 1 << 20
)

func main() {
	if len(os.Args) < 2 {
		fatalf("usage: agent-orchestrator <pty-host|agent-process> ...")
	}
	var err error
	switch os.Args[1] {
	case "pty-host":
		err = runHost(os.Args[2:])
	case "agent-process":
		err = runSupervisor(os.Args[1:])
	default:
		err = fmt.Errorf("unsupported fixture command %q", os.Args[1])
	}
	if err != nil {
		fatalf("%v", err)
	}
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "pty-v2-host fixture: "+format+"\n", args...)
	os.Exit(1)
}

type supervisorInvocation struct {
	sessionID string
	launchID  string
	command   []string
}

func parseSupervisorInvocation(args []string) (supervisorInvocation, error) {
	if len(args) < 3 || args[0] != "agent-process" || args[1] != "supervise" {
		return supervisorInvocation{}, errors.New("expected agent-process supervise")
	}
	var invocation supervisorInvocation
	for index := 2; index < len(args); index++ {
		switch args[index] {
		case "--session":
			index++
			if index >= len(args) {
				return supervisorInvocation{}, errors.New("--session requires a value")
			}
			invocation.sessionID = args[index]
		case "--launch":
			index++
			if index >= len(args) {
				return supervisorInvocation{}, errors.New("--launch requires a value")
			}
			invocation.launchID = args[index]
		case "--":
			invocation.command = append([]string(nil), args[index+1:]...)
			if invocation.sessionID == "" || invocation.launchID == "" || len(invocation.command) == 0 {
				return supervisorInvocation{}, errors.New("supervisor requires session, launch, and command")
			}
			return invocation, nil
		default:
			return supervisorInvocation{}, fmt.Errorf("unexpected supervisor argument %q", args[index])
		}
	}
	return supervisorInvocation{}, errors.New("supervisor command separator is missing")
}

// runSupervisor deliberately remains an agent-orchestrator process while its
// shell child runs. The production Darwin/Linux verifier therefore observes
// the same host -> AO supervisor relationship as a released protocol-v2 host.
func runSupervisor(args []string) error {
	invocation, err := parseSupervisorInvocation(args)
	if err != nil {
		return err
	}
	command := exec.Command(invocation.command[0], invocation.command[1:]...) // #nosec G204 -- fixed test-fixture argv
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("start supervised fixture shell: %w", err)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-signals:
		_ = command.Process.Kill()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		return nil
	}
}

type childState struct {
	mu       sync.RWMutex
	alive    bool
	exitCode int
	pid      int
}

func (s *childState) status() (alive bool, pid, exitCode int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.alive, s.pid, s.exitCode
}

func (s *childState) exited(code int) {
	s.mu.Lock()
	s.alive = false
	s.exitCode = code
	s.mu.Unlock()
}

type replayBuffer struct {
	mu   sync.RWMutex
	data []byte
}

func (r *replayBuffer) append(chunk []byte) {
	r.mu.Lock()
	r.data = append(r.data, chunk...)
	if len(r.data) > maxReplayBytes {
		r.data = append([]byte(nil), r.data[len(r.data)-maxReplayBytes:]...)
	}
	r.mu.Unlock()
}

func (r *replayBuffer) snapshot() []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]byte(nil), r.data...)
}

func (r *replayBuffer) tail(lines int) []byte {
	data := r.snapshot()
	if lines <= 0 {
		lines = 50
	}
	end := len(data)
	for count := 0; count < lines && end > 0; {
		end--
		if data[end] == '\n' {
			count++
		}
	}
	if end > 0 {
		end++
	}
	return data[end:]
}

type fixtureClient struct {
	conn net.Conn
	mu   sync.Mutex
}

func (c *fixtureClient) send(messageType byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	frame := make([]byte, 5+len(payload))
	frame[0] = messageType
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload))) // #nosec G115 -- payload is capped on input and fixture output is <= 1 MiB
	copy(frame[5:], payload)
	_, err := c.conn.Write(frame)
	return err
}

type fixtureHost struct {
	listener net.Listener
	child    *exec.Cmd
	input    io.WriteCloser
	output   io.ReadCloser
	state    childState
	replay   replayBuffer

	mu        sync.Mutex
	clients   map[*fixtureClient]struct{}
	closeOnce sync.Once
	done      chan struct{}
}

func runHost(args []string) error {
	if len(args) < 3 {
		return errors.New("usage: agent-orchestrator pty-host <session> <cwd> agent-process supervise ...")
	}
	sessionID, cwd := args[0], args[1]
	invocation, err := parseSupervisorInvocation(args[2:])
	if err != nil {
		return err
	}
	if invocation.sessionID != sessionID {
		return fmt.Errorf("supervisor session %q does not match host session %q", invocation.sessionID, sessionID)
	}
	if err := os.Chdir(cwd); err != nil {
		return fmt.Errorf("change fixture working directory: %w", err)
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("resolve fixture executable: %w", err)
	}
	child := exec.Command(executable, args[2:]...)
	child.Dir = cwd
	input, err := child.StdinPipe()
	if err != nil {
		_ = listener.Close()
		return err
	}
	output, err := child.StdoutPipe()
	if err != nil {
		_ = input.Close()
		_ = listener.Close()
		return err
	}
	child.Stderr = child.Stdout
	if err := child.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		_ = listener.Close()
		return fmt.Errorf("start AO supervisor fixture: %w", err)
	}

	host := &fixtureHost{
		listener: listener,
		child:    child,
		input:    input,
		output:   output,
		state:    childState{alive: true, pid: child.Process.Pid},
		clients:  make(map[*fixtureClient]struct{}),
		done:     make(chan struct{}),
	}
	go host.pumpOutput()
	go host.waitChild()
	go host.accept()

	port := listener.Addr().(*net.TCPAddr).Port
	_, _ = fmt.Fprintf(os.Stdout, "READY:%d %d\n", child.Process.Pid, port)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case <-host.done:
	case <-signals:
		host.close()
		<-host.done
	}
	return nil
}

func (h *fixtureHost) waitChild() {
	err := h.child.Wait()
	code := 0
	if h.child.ProcessState != nil {
		code = h.child.ProcessState.ExitCode()
	} else if err != nil {
		code = -1
	}
	h.state.exited(code)
}

func (h *fixtureHost) pumpOutput() {
	buffer := make([]byte, 32*1024)
	for {
		count, err := h.output.Read(buffer)
		if count > 0 {
			chunk := append([]byte(nil), buffer[:count]...)
			h.replay.append(chunk)
			h.broadcast(msgTerminalData, chunk)
		}
		if err != nil {
			return
		}
	}
}

func (h *fixtureHost) accept() {
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			return
		}
		client := &fixtureClient{conn: conn}
		h.mu.Lock()
		h.clients[client] = struct{}{}
		h.mu.Unlock()
		if replay := h.replay.snapshot(); len(replay) > 0 {
			_ = client.send(msgTerminalData, replay)
		}
		go h.serveClient(client)
	}
}

func (h *fixtureHost) serveClient(client *fixtureClient) {
	defer func() {
		h.mu.Lock()
		delete(h.clients, client)
		h.mu.Unlock()
		_ = client.conn.Close()
	}()
	for {
		messageType, payload, err := readFrame(client.conn)
		if err != nil {
			return
		}
		switch messageType {
		case msgTerminalInput:
			// A real PTY's line discipline maps carriage return to newline and
			// echoes typed input. Reproduce both behaviors over this pipe fixture.
			input := bytes.ReplaceAll(payload, []byte{'\r'}, []byte{'\n'})
			if _, err := h.input.Write(input); err != nil {
				return
			}
			h.replay.append(payload)
			h.broadcast(msgTerminalData, payload)
		case msgResize:
			// The fixture has no real PTY grid; accepting resize is sufficient.
		case msgGetOutputReq, msgGetStyledOutputReq:
			lines := requestedLines(payload)
			responseType := msgGetOutputRes
			if messageType == msgGetStyledOutputReq {
				responseType = msgGetStyledOutputRes
			}
			if err := client.send(responseType, h.replay.tail(lines)); err != nil {
				return
			}
		case msgStatusReq:
			alive, pid, exitCode := h.state.status()
			status := map[string]any{"alive": alive, "pid": pid, "protocolVersion": 2}
			if !alive {
				status["exitCode"] = exitCode
			}
			encoded, _ := json.Marshal(status)
			if err := client.send(msgStatusRes, encoded); err != nil {
				return
			}
		case msgKillReq:
			go h.close()
			return
		}
	}
}

func readFrame(reader io.Reader) (byte, []byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > maxFramePayload {
		return 0, nil, fmt.Errorf("frame payload is too large: %d", length)
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}

func requestedLines(payload []byte) int {
	var request struct {
		Lines json.Number `json:"lines"`
	}
	if json.Unmarshal(payload, &request) != nil {
		return 50
	}
	lines, err := strconv.Atoi(request.Lines.String())
	if err != nil || lines <= 0 {
		return 50
	}
	return lines
}

func (h *fixtureHost) broadcast(messageType byte, payload []byte) {
	h.mu.Lock()
	clients := make([]*fixtureClient, 0, len(h.clients))
	for client := range h.clients {
		clients = append(clients, client)
	}
	h.mu.Unlock()
	for _, client := range clients {
		if err := client.send(messageType, payload); err != nil {
			_ = client.conn.Close()
		}
	}
}

func (h *fixtureHost) close() {
	h.closeOnce.Do(func() {
		_ = h.listener.Close()
		_ = h.input.Close()
		if h.child.Process != nil {
			_ = h.child.Process.Signal(os.Interrupt)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			alive, _, _ := h.state.status()
			if !alive || time.Now().After(deadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		alive, _, _ := h.state.status()
		if alive && h.child.Process != nil {
			_ = h.child.Process.Kill()
		}
		h.mu.Lock()
		for client := range h.clients {
			_ = client.conn.Close()
		}
		h.clients = make(map[*fixtureClient]struct{})
		h.mu.Unlock()
		close(h.done)
	})
}
