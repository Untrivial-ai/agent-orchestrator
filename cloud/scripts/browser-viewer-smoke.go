//go:build ignore

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/browserstream"
	"github.com/coder/websocket"
)

type smokeState struct {
	Token     string `json:"token"`
	OrgID     string `json:"orgId"`
	SessionID string `json:"sessionId"`
}

type viewerTicket struct {
	Ticket          string `json:"ticket"`
	ProtocolVersion int    `json:"protocolVersion"`
	CanOperate      bool   `json:"canOperate"`
}

type wireMessage struct {
	control *browserstream.Control
	frame   *browserstream.Frame
	err     error
}

type controlResult struct {
	control browserstream.Control
	err     error
}

type smokeViewer struct {
	connection    *websocket.Conn
	messages      <-chan wireMessage
	lastFrame     browserstream.Frame
	lastState     browserstream.Control
	epoch         uint64
	inputSequence uint64
}

func main() {
	if len(os.Args) != 4 {
		fatal(errors.New("usage: browser-viewer-smoke <control-plane-url> <state-file> <worker-container>"))
	}
	baseURL := strings.TrimRight(os.Args[1], "/")
	stateBytes, err := os.ReadFile(os.Args[2])
	if err != nil {
		fatal(err)
	}
	var state smokeState
	if err := json.Unmarshal(stateBytes, &state); err != nil {
		fatal(err)
	}
	if state.Token == "" || state.OrgID == "" || state.SessionID == "" {
		fatal(errors.New("smoke state is incomplete"))
	}
	workerContainer := os.Args[3]
	if err := startFixture(workerContainer); err != nil {
		fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	started := time.Now()
	connection, viewer, err := connectViewer(ctx, baseURL, state)
	if err != nil {
		fatal(err)
	}
	defer connection.CloseNow()
	if _, err := viewer.waitControl(ctx, func(control browserstream.Control) bool { return control.Type == "attached" }); err != nil {
		fatal(err)
	}
	if err := viewer.send(ctx, browserstream.Control{Type: "viewport", Width: 1280, Height: 720}); err != nil {
		fatal(err)
	}
	if _, err := viewer.waitControl(ctx, func(control browserstream.Control) bool { return control.Type == "viewport_ack" }); err != nil {
		fatal(err)
	}
	if err := viewer.waitNextFrame(ctx); err != nil {
		fatal(err)
	}
	attachToFirstFrame := time.Since(started)

	if err := viewer.send(ctx, browserstream.Control{
		Type: "navigate", InputSeq: 2, Operation: "open", URL: "http://localhost:3000",
	}); err != nil {
		fatal(err)
	}
	navigationAck, err := viewer.waitInputAck(ctx, 2)
	if err != nil {
		fatal(err)
	}
	if _, err := viewer.waitControl(ctx, func(control browserstream.Control) bool {
		return control.Type == "state" &&
			strings.Contains(control.URL, "localhost:3000") &&
			control.Title == "VM browser smoke" &&
			!control.IsLoading
	}); err != nil {
		fatal(err)
	}
	if err := viewer.waitFrame(ctx, navigationAck.MinFrameSeq); err != nil {
		fatal(err)
	}
	originalTabID := viewer.lastState.ActiveTabID
	if originalTabID == "" {
		fatal(errors.New("viewer state did not report the fixture tab"))
	}
	if err := viewer.send(ctx, browserstream.Control{
		Type: "tab", InputSeq: 3, Operation: "new", URL: "http://localhost:3000/?second=1",
	}); err != nil {
		fatal(err)
	}
	if _, err := viewer.waitInputAck(ctx, 3); err != nil {
		fatal(fmt.Errorf("open viewer tab: %w", err))
	}
	newTabState, err := viewer.waitControl(ctx, func(control browserstream.Control) bool {
		return control.Type == "state" && len(control.Tabs) == 2 &&
			control.ActiveTabID != "" && control.ActiveTabID != originalTabID
	})
	if err != nil {
		fatal(fmt.Errorf("wait for opened viewer tab: %w", err))
	}
	newTabID := newTabState.ActiveTabID
	if err := viewer.send(ctx, browserstream.Control{
		Type: "tab", InputSeq: 4, Operation: "select", TabID: originalTabID,
	}); err != nil {
		fatal(err)
	}
	if _, err := viewer.waitInputAck(ctx, 4); err != nil {
		fatal(fmt.Errorf("select viewer tab: %w", err))
	}
	if _, err := viewer.waitControl(ctx, func(control browserstream.Control) bool {
		return control.Type == "state" && control.ActiveTabID == originalTabID
	}); err != nil {
		fatal(fmt.Errorf("wait for selected viewer tab: %w", err))
	}
	if err := viewer.send(ctx, browserstream.Control{
		Type: "tab", InputSeq: 5, Operation: "close", TabID: newTabID,
	}); err != nil {
		fatal(err)
	}
	if _, err := viewer.waitInputAck(ctx, 5); err != nil {
		fatal(fmt.Errorf("close viewer tab: %w", err))
	}
	if _, err := viewer.waitControl(ctx, func(control browserstream.Control) bool {
		return control.Type == "state" && len(control.Tabs) == 1 &&
			control.ActiveTabID == originalTabID && control.Tabs[0].ID == originalTabID
	}); err != nil {
		fatal(fmt.Errorf("wait for closed viewer tab: %w", err))
	}

	inputStarted := time.Now()
	sequence := uint64(6)
	for _, control := range []browserstream.Control{
		{Type: "input", InputSeq: sequence, Kind: "pointerDown", X: 80, Y: 40, Button: "left", Buttons: 1, ClickCount: 1},
		{Type: "input", InputSeq: sequence + 1, Kind: "pointerUp", X: 80, Y: 40, Button: "left", ClickCount: 1},
		{Type: "input", InputSeq: sequence + 2, Kind: "pointerDown", X: 100, Y: 100, Button: "left", Buttons: 1, ClickCount: 1},
		{Type: "input", InputSeq: sequence + 3, Kind: "pointerUp", X: 100, Y: 100, Button: "left", ClickCount: 1},
		{Type: "input", InputSeq: sequence + 4, Kind: "text", Text: "viewer-typed"},
	} {
		if err := viewer.send(ctx, control); err != nil {
			fatal(err)
		}
		ack, err := viewer.waitInputAck(ctx, control.InputSeq)
		if err != nil {
			fatal(err)
		}
		if control.InputSeq == sequence+4 {
			if err := viewer.waitFrame(ctx, ack.MinFrameSeq); err != nil {
				fatal(err)
			}
		}
	}
	viewerInputVisible := time.Since(inputStarted)

	snapshot, err := browserCommand(workerContainer, "snapshot")
	if err != nil {
		fatal(err)
	}
	title, err := browserCommand(workerContainer, "get", "title")
	if err != nil {
		fatal(err)
	}
	if !strings.Contains(title, "clicks:1;input:viewer-typed") {
		fatal(errors.New("command title did not observe viewer click and text state"))
	}
	buttonRef := regexp.MustCompile(`button\s+"Agent action"[^\n]*ref=(e[0-9]+)`).FindStringSubmatch(snapshot)
	if len(buttonRef) != 2 {
		fatal(errors.New("command snapshot did not expose the fixture button reference"))
	}

	commandResult := make(chan error, 1)
	go func() {
		_, commandErr := browserCommand(workerContainer, "click", buttonRef[1])
		commandResult <- commandErr
	}()
	agentActionResult := make(chan controlResult, 1)
	go func() {
		control, waitErr := viewer.waitControl(ctx, func(control browserstream.Control) bool {
			return control.Type == "agent_action" && control.Running
		})
		agentActionResult <- controlResult{control: control, err: waitErr}
	}()
	commandDone := false
	var agentAction browserstream.Control
	select {
	case result := <-agentActionResult:
		if result.err != nil {
			fatal(fmt.Errorf("wait for command action: %w", result.err))
		}
		agentAction = result.control
	case err := <-commandResult:
		commandDone = true
		if err != nil {
			fatal(err)
		}
		result := <-agentActionResult
		if result.err != nil {
			fatal(fmt.Errorf("wait for completed command action: %w", result.err))
		}
		agentAction = result.control
	case <-ctx.Done():
		fatal(fmt.Errorf("wait for command start: %w", ctx.Err()))
	}
	agentStarted := time.Now()
	if err := viewer.waitFrame(ctx, agentAction.MinFrameSeq); err != nil {
		fatal(fmt.Errorf("wait for command frame: %w", err))
	}
	if !commandDone {
		select {
		case err := <-commandResult:
			if err != nil {
				fatal(err)
			}
		case <-ctx.Done():
			fatal(fmt.Errorf("wait for command completion: %w", ctx.Err()))
		}
	}
	title, err = browserCommand(workerContainer, "get", "title")
	if err != nil {
		fatal(err)
	}
	if !strings.Contains(title, "clicks:2;input:viewer-typed") {
		fatal(errors.New("command click did not update the shared browser state"))
	}
	agentInputVisible := time.Since(agentStarted)

	beforeReconnectTitle := title
	reconnectStarted := time.Now()
	connection.CloseNow()
	reconnected, replacementViewer, err := connectViewer(ctx, baseURL, state)
	if err != nil {
		fatal(fmt.Errorf("reconnect viewer: %w", err))
	}
	defer reconnected.CloseNow()
	viewer = replacementViewer
	if _, err := viewer.waitControl(ctx, func(control browserstream.Control) bool { return control.Type == "attached" }); err != nil {
		fatal(fmt.Errorf("wait for reconnect attach: %w", err))
	}
	if err := viewer.send(ctx, browserstream.Control{Type: "viewport", Width: 1280, Height: 720}); err != nil {
		fatal(err)
	}
	if _, err := viewer.waitControl(ctx, func(control browserstream.Control) bool { return control.Type == "viewport_ack" }); err != nil {
		fatal(err)
	}
	if err := viewer.waitNextFrame(ctx); err != nil {
		fatal(fmt.Errorf("wait for reconnect frame: %w", err))
	}
	viewerReconnect := time.Since(reconnectStarted)
	title, err = browserCommand(workerContainer, "get", "title")
	if err != nil {
		fatal(err)
	}
	if title != beforeReconnectTitle {
		fatal(errors.New("viewer reconnect changed browser state or replayed input"))
	}
	if _, err := browserCommand(workerContainer, "open", "http://localhost:3000/churn"); err != nil {
		fatal(err)
	}
	reconnected.CloseNow()
	unreadViewer, err := dialViewer(ctx, baseURL, state)
	if err != nil {
		fatal(fmt.Errorf("connect slow viewer: %w", err))
	}
	time.Sleep(4 * time.Second)
	unreadViewer.CloseNow()
	slowRecoveryStarted := time.Now()
	recoveredConnection, recoveredViewer, err := connectViewer(ctx, baseURL, state)
	if err != nil {
		fatal(fmt.Errorf("recover slow viewer: %w", err))
	}
	defer recoveredConnection.CloseNow()
	viewer = recoveredViewer
	if _, err := viewer.waitControl(ctx, func(control browserstream.Control) bool { return control.Type == "attached" }); err != nil {
		fatal(fmt.Errorf("wait for slow-viewer recovery attach: %w", err))
	}
	if err := viewer.waitNextFrame(ctx); err != nil {
		fatal(fmt.Errorf("wait for slow-viewer recovery frame: %w", err))
	}
	if len(viewer.lastFrame.JPEG) > browserstream.MaxFrameBytes {
		fatal(errors.New("slow viewer recovery exceeded the bounded frame limit"))
	}
	slowViewerRecovery := time.Since(slowRecoveryStarted)

	crashEpoch := viewer.lastFrame.StreamEpoch
	crashStarted := time.Now()
	if err := killChromium(workerContainer); err != nil {
		fatal(err)
	}
	attached, err := viewer.waitControl(ctx, func(control browserstream.Control) bool {
		return control.Type == "attached" && control.StreamEpoch > crashEpoch
	})
	if err != nil {
		fatal(fmt.Errorf("wait for browser restart attach: %w", err))
	}
	if err := viewer.waitEpochFrame(ctx, attached.StreamEpoch); err != nil {
		fatal(fmt.Errorf("wait for browser restart frame: %w", err))
	}
	crashRecovery := time.Since(crashStarted)

	result := map[string]any{
		"agentInputVisibleMs":      agentInputVisible.Milliseconds(),
		"attachToFirstFrameMs":     attachToFirstFrame.Milliseconds(),
		"codec":                    "jpeg",
		"frameBytes":               len(viewer.lastFrame.JPEG),
		"frameHeight":              viewer.lastFrame.Height,
		"frameWidth":               viewer.lastFrame.Width,
		"chromiumCrashRecoveryMs":  crashRecovery.Milliseconds(),
		"inputReplayPrevented":     true,
		"slowViewerRecovered":      true,
		"slowViewerRecoveryMs":     slowViewerRecovery.Milliseconds(),
		"tabControlsVerified":      true,
		"userInputVisibleMs":       viewerInputVisible.Milliseconds(),
		"viewerReconnectMs":        viewerReconnect.Milliseconds(),
		"viewerAndCommandShared":   true,
		"workerAndViewerDataPlane": true,
	}
	encoded, _ := json.Marshal(result)
	fmt.Printf("BROWSER_VIEWER_RESULT %s\n", encoded)
}

func connectViewer(ctx context.Context, baseURL string, state smokeState) (*websocket.Conn, *smokeViewer, error) {
	connection, err := dialViewer(ctx, baseURL, state)
	if err != nil {
		return nil, nil, err
	}
	return connection, &smokeViewer{connection: connection, messages: readMessages(ctx, connection)}, nil
}

func dialViewer(ctx context.Context, baseURL string, state smokeState) (*websocket.Conn, error) {
	ticket, err := issueTicket(ctx, baseURL, state)
	if err != nil {
		return nil, err
	}
	if ticket.ProtocolVersion != browserstream.Version || !ticket.CanOperate {
		return nil, errors.New("browser viewer ticket did not grant the expected protocol and control scope")
	}
	streamURL, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	if streamURL.Scheme == "https" {
		streamURL.Scheme = "wss"
	} else {
		streamURL.Scheme = "ws"
	}
	streamURL.Path = fmt.Sprintf(
		"/api/cloud/v1/orgs/%s/sessions/%s/browser-view/stream",
		url.PathEscape(state.OrgID), url.PathEscape(state.SessionID),
	)
	query := streamURL.Query()
	query.Set("ticket", ticket.Ticket)
	streamURL.RawQuery = query.Encode()
	connection, _, err := websocket.Dial(ctx, streamURL.String(), &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return nil, err
	}
	connection.SetReadLimit(browserstream.MaxFrameBytes + 512)
	return connection, nil
}

func issueTicket(ctx context.Context, baseURL string, state smokeState) (viewerTicket, error) {
	endpoint := fmt.Sprintf(
		"%s/api/cloud/v1/orgs/%s/sessions/%s/browser-view-ticket",
		baseURL, url.PathEscape(state.OrgID), url.PathEscape(state.SessionID),
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte("{}")))
	if err != nil {
		return viewerTicket{}, err
	}
	request.Header.Set("Authorization", "Bearer "+state.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return viewerTicket{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return viewerTicket{}, fmt.Errorf("browser ticket returned %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var ticket viewerTicket
	if err := json.NewDecoder(response.Body).Decode(&ticket); err != nil {
		return viewerTicket{}, err
	}
	return ticket, nil
}

func readMessages(ctx context.Context, connection *websocket.Conn) <-chan wireMessage {
	messages := make(chan wireMessage, 16)
	go func() {
		defer close(messages)
		for {
			kind, payload, err := connection.Read(ctx)
			message := wireMessage{err: err}
			if err == nil && kind == websocket.MessageBinary {
				frame, decodeErr := browserstream.DecodeFrame(payload)
				message.frame, message.err = &frame, decodeErr
			} else if err == nil && kind == websocket.MessageText {
				var control browserstream.Control
				if decodeErr := json.Unmarshal(payload, &control); decodeErr != nil {
					message.err = decodeErr
				} else {
					message.control = &control
				}
			}
			select {
			case messages <- message:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return messages
}

func (v *smokeViewer) send(ctx context.Context, control browserstream.Control) error {
	control.Version = browserstream.Version
	control.StreamEpoch = v.epoch
	if control.Type != "ping" && control.Type != "detach" {
		if control.InputSeq == 0 {
			v.inputSequence++
			control.InputSeq = v.inputSequence
		} else {
			v.inputSequence = control.InputSeq
		}
	}
	payload, err := json.Marshal(control)
	if err != nil {
		return err
	}
	return v.connection.Write(ctx, websocket.MessageText, payload)
}

func (v *smokeViewer) waitControl(ctx context.Context, match func(browserstream.Control) bool) (browserstream.Control, error) {
	if v.lastState.Type != "" && match(v.lastState) {
		return v.lastState, nil
	}
	for {
		select {
		case <-ctx.Done():
			return browserstream.Control{}, ctx.Err()
		case message, ok := <-v.messages:
			if !ok {
				return browserstream.Control{}, errors.New("browser viewer stream closed")
			}
			if message.err != nil {
				return browserstream.Control{}, message.err
			}
			if message.frame != nil {
				v.lastFrame = *message.frame
				continue
			}
			if message.control != nil {
				if message.control.StreamEpoch > 0 {
					v.epoch = message.control.StreamEpoch
				}
				if message.control.Type == "state" {
					v.lastState = *message.control
				}
				if message.control.Type == "input_rejected" {
					return browserstream.Control{}, fmt.Errorf("browser input rejected: %s", message.control.Code)
				}
				if match(*message.control) {
					return *message.control, nil
				}
			}
		}
	}
}

func (v *smokeViewer) waitInputAck(ctx context.Context, sequence uint64) (browserstream.Control, error) {
	return v.waitControl(ctx, func(control browserstream.Control) bool {
		return control.Type == "input_ack" && control.InputSeq == sequence
	})
}

func (v *smokeViewer) waitNextFrame(ctx context.Context) error {
	return v.waitFrame(ctx, v.lastFrame.Sequence+1)
}

func (v *smokeViewer) waitFrame(ctx context.Context, minimum uint64) error {
	if minimum > 0 && v.lastFrame.Sequence >= minimum {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case message, ok := <-v.messages:
			if !ok {
				return errors.New("browser viewer stream closed")
			}
			if message.err != nil {
				return message.err
			}
			if message.control != nil {
				if message.control.StreamEpoch > 0 {
					v.epoch = message.control.StreamEpoch
				}
				if message.control.Type == "state" {
					v.lastState = *message.control
				}
				if message.control.Type == "input_rejected" {
					return fmt.Errorf("browser input rejected: %s", message.control.Code)
				}
			}
			if message.frame != nil {
				v.lastFrame = *message.frame
				if minimum == 0 || v.lastFrame.Sequence >= minimum {
					return nil
				}
			}
		}
	}
}

func (v *smokeViewer) waitEpochFrame(ctx context.Context, epoch uint64) error {
	if v.lastFrame.StreamEpoch == epoch && v.lastFrame.Sequence > 0 {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case message, ok := <-v.messages:
			if !ok {
				return errors.New("browser viewer stream closed")
			}
			if message.err != nil {
				return message.err
			}
			if message.control != nil && message.control.Type == "state" {
				v.lastState = *message.control
			}
			if message.frame != nil {
				v.lastFrame = *message.frame
				if v.lastFrame.StreamEpoch == epoch && v.lastFrame.Sequence > 0 {
					return nil
				}
			}
		}
	}
}

func startFixture(container string) error {
	source := `const http=require("http");http.createServer((req,res)=>{if(req.url==="/assets/app.js"){res.writeHead(200,{"Content-Type":"application/javascript"});res.end("window.vmBrowserSmoke = true;");return;}res.writeHead(200,{"Content-Type":"text/html; charset=utf-8"});if(req.url==="/churn"){res.end('<!doctype html><html><head><title>frame churn</title></head><body style="margin:0;overflow:hidden"><canvas width="1280" height="720"></canvas><script>const c=document.querySelector("canvas"),x=c.getContext("2d"),d=x.createImageData(c.width,c.height);setInterval(()=>{for(let i=0;i<d.data.length;i+=65536)crypto.getRandomValues(d.data.subarray(i,Math.min(i+65536,d.data.length)));for(let i=3;i<d.data.length;i+=4)d.data[i]=255;x.putImageData(d,0,0)},80)</script></body></html>');return;}res.end('<!doctype html><html><head><title>VM browser smoke</title></head><body style="margin:0" data-clicks="0"><button aria-label="Agent action" style="position:absolute;left:20px;top:20px;width:140px;height:40px" onclick="const n=Number(document.body.dataset.clicks)+1;document.body.dataset.clicks=String(n);document.getElementById(\'click-log\').textContent=\'clicks-\'+n;document.title=\'clicks:\'+n+\';input:\'+document.getElementById(\'shared-input\').value">Agent action</button><input id="shared-input" aria-label="Shared input" style="position:absolute;left:20px;top:80px;width:240px;height:40px" oninput="document.getElementById(\'input-log\').textContent=this.value;document.title=\'clicks:\'+document.body.dataset.clicks+\';input:\'+this.value"><p id="click-log" style="position:absolute;top:130px">waiting-click</p><p id="input-log" style="position:absolute;top:160px">waiting-input</p><p style="position:absolute;top:190px">vm-browser-smoke</p><script src="/assets/app.js"></script></body></html>');}).listen(3000,"127.0.0.1");`
	command := exec.Command("docker", "exec", "-d", container, "node", "-e", source)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("start browser fixture: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func browserCommand(container string, arguments ...string) (string, error) {
	quoted := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		quoted = append(quoted, "'"+strings.ReplaceAll(argument, "'", "'\\''")+"'")
	}
	script := `set -e
agent_pid=""
for candidate in /proc/[0-9]*; do
  [ -r "$candidate/environ" ] || continue
  if tr '\0' '\n' < "$candidate/environ" | grep -q '^AO_BROWSER_API_URL='; then agent_pid="${candidate##*/}"; break; fi
done
[ -n "$agent_pid" ]
export AO_BROWSER_API_URL="$(tr '\0' '\n' < "/proc/$agent_pid/environ" | sed -n 's/^AO_BROWSER_API_URL=//p')"
export AO_BROWSER_CAPABILITY="$(tr '\0' '\n' < "/proc/$agent_pid/environ" | sed -n 's/^AO_BROWSER_CAPABILITY=//p')"
export AO_SESSION_ID="$(tr '\0' '\n' < "/proc/$agent_pid/environ" | sed -n 's/^AO_SESSION_ID=//p')"
ao browser ` + strings.Join(quoted, " ")
	command := exec.Command("docker", "exec", container, "bash", "-c", script)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("browser command failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func killChromium(container string) error {
	script := `set -e
found=0
for process in /proc/[0-9]*; do
  [ -r "$process/comm" ] || continue
  name="$(cat "$process/comm")"
  case "$name" in
    chromium|chrome|chrome-headless-shell|headless_shell)
      found=1
      kill -9 "${process##*/}" 2>/dev/null || true
      ;;
  esac
done
[ "$found" = 1 ]`
	command := exec.Command("docker", "exec", container, "sh", "-c", script)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("kill Chromium: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "browser viewer smoke failed:", err)
	os.Exit(1)
}
