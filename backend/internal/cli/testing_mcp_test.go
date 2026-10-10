package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const testingTestCapability = "test-capability-must-not-appear"

// Run the actual Cobra command over pipes, rather than an in-memory MCP server.
func testingMCPPipe(t *testing.T, version string) (*mcp.ClientSession, func() string) {
	t.Helper()
	t.Setenv("AO_TEST_CAPABILITY", testingTestCapability)
	t.Setenv("AO_SESSION_ID", "worker-1")
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	var frames, diagnostics bytes.Buffer
	// Server shutdown joins its writes, but SDK shutdown does not join its decoder.
	cmd := NewRootCommand(Deps{In: serverIn, Out: io.MultiWriter(serverOut, &frames), Err: &diagnostics, ProcessAlive: func(int) bool { return true }})
	cmd.SetArgs([]string{"testing", "mcp"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	cmd.SetContext(ctx)
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	client := mcp.NewClient(&mcp.Implementation{Name: "slice-c-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{
		Reader: clientIn, Writer: clientOut,
	}, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		cancel()
		_ = serverIn.Close()
		_ = clientIn.Close()
		_ = clientOut.Close()
		_ = serverOut.Close()
		t.Fatalf("connect SDK client: %v", err)
	}
	finished := false
	finish := func() string {
		t.Helper()
		if !finished {
			finished = true
			// Close stdin first, keeping stdout open for the final tool response.
			_ = clientOut.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("MCP shutdown: %v", err)
				}
			case <-ctx.Done():
				t.Error("MCP shutdown timed out")
			}
			_ = session.Close()
			cancel()
			_ = serverOut.Close()
			if strings.Contains(frames.String()+diagnostics.String(), testingTestCapability) {
				t.Error("capability appeared in stdio")
			}
			if diagnostics.Len() != 0 {
				t.Errorf("unexpected diagnostics: %s", diagnostics.String())
			}
			scanner := bufio.NewScanner(strings.NewReader(frames.String()))
			for scanner.Scan() {
				var frame map[string]json.RawMessage
				if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil || string(frame["jsonrpc"]) != `"2.0"` {
					t.Errorf("stdout contains a non-protocol frame: %s", scanner.Text())
				}
			}
			if err := scanner.Err(); err != nil {
				t.Error(err)
			}
		}
		return frames.String()
	}
	t.Cleanup(func() { finish() })
	return session, finish
}

func TestTestingMCPNegotiationAndSchemas(t *testing.T) {
	for _, version := range []string{"2024-11-05", "2025-06-18", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			setConfigEnv(t)
			session, finish := testingMCPPipe(t, version)
			if got := session.InitializeResult().ProtocolVersion; got != version {
				t.Fatalf("negotiated %q, want %q", got, version)
			}
			tools, err := session.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			var want map[string]any
			if err := json.Unmarshal([]byte(`{
				"screenshot":{"type":"object","properties":{},"additionalProperties":false},
				"click":{"type":"object","properties":{"screenshotId":{"type":"string","minLength":1},"x":{"type":"integer","minimum":0},"y":{"type":"integer","minimum":0},"button":{"enum":["left","right","middle"]},"elementId":{"type":"string","minLength":1,"maxLength":128}},"required":["screenshotId"],"additionalProperties":false,"oneOf":[{"required":["elementId"],"not":{"anyOf":[{"required":["x"]},{"required":["y"]}]}},{"required":["x","y"],"not":{"required":["elementId"]}}]},
				"type":{"type":"object","properties":{"screenshotId":{"type":"string","minLength":1},"x":{"type":"integer","minimum":0},"y":{"type":"integer","minimum":0},"text":{"type":"string","maxLength":16384},"elementId":{"type":"string","minLength":1,"maxLength":128}},"required":["screenshotId","text"],"additionalProperties":false,"oneOf":[{"required":["elementId"],"not":{"anyOf":[{"required":["x"]},{"required":["y"]}]}},{"required":["x","y"],"not":{"required":["elementId"]}}]},
				"key":{"type":"object","properties":{"screenshotId":{"type":"string","minLength":1},"keys":{"type":"array","items":{"type":"string","minLength":1},"minItems":1,"maxItems":4}},"required":["screenshotId","keys"],"additionalProperties":false},
				"read_target_logs":{"type":"object","properties":{"cursor":{"type":"string","maxLength":256},"maxBytes":{"type":"integer","minimum":1,"maximum":262144}},"additionalProperties":false},
				"target_daemon_query":{"type":"object","properties":{"resource":{"enum":["projects","sessions","reviews","conversation"]},"sessionId":{"type":"string","minLength":1,"maxLength":128,"pattern":"^[A-Za-z0-9_-]+$"}},"required":["resource"],"oneOf":[{"properties":{"resource":{"enum":["projects","sessions"]}},"not":{"required":["sessionId"]}},{"properties":{"resource":{"enum":["reviews","conversation"]}},"required":["sessionId"]}],"additionalProperties":false},
				"submit_report":{"type":"object","properties":{"outcome":{"enum":["reproduced","not_reproduced","needs_information","environment_blocked","partial","cancelled"]},"markdown":{"type":"string","maxLength":65536}},"required":["outcome","markdown"],"additionalProperties":false},
				"observe":{"type":"object","properties":{},"additionalProperties":false}
			}`), &want); err != nil {
				t.Fatal(err)
			}
			got := map[string]any{}
			for _, tool := range tools.Tools {
				data, err := json.Marshal(tool.InputSchema)
				if err != nil {
					t.Fatal(err)
				}
				var schema any
				if err := json.Unmarshal(data, &schema); err != nil {
					t.Fatal(err)
				}
				got[tool.Name] = schema
			}
			if len(tools.Tools) != 8 || !reflect.DeepEqual(got, want) {
				t.Fatalf("tools/list differs from the eight-tool contract: %#v", got)
			}
			finish()
		})
	}
}

func TestTestingMCPForwardsToolsAndScreenshot(t *testing.T) {
	cfg := setConfigEnv(t)
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	requestIDs := map[string]bool{}
	inputs := map[string]string{
		"screenshot": `{}`, "observe": `{}`, "click": `{"screenshotId":"shot-1","x":0,"y":0}`,
		"type": `{"screenshotId":"shot-1","elementId":"element-1","text":"hello"}`,
		"key":  `{"screenshotId":"shot-1","keys":["ENTER"]}`, "read_target_logs": `{}`,
		"target_daemon_query": `{"resource":"sessions"}`, "submit_report": `{"outcome":"partial","markdown":"# Observations"}`,
	}
	responses := map[string]string{
		"click":               `{"action":{"delivered":true},"evidence":[]}`,
		"type":                `{"action":{"delivered":true},"evidence":[]}`,
		"key":                 `{"action":{"delivered":true},"evidence":[]}`,
		"read_target_logs":    `{"logs":{"text":"target log","truncated":false},"evidence":[]}`,
		"target_daemon_query": `{"query":{"resource":"sessions","data":{"sessions":[]}},"evidence":[]}`,
		"submit_report":       `{"report":{"outcome":"partial","evidenceId":"report-1"},"evidence":[]}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/testing/sessions/worker-1/tools/")
		calls = append(calls, name)
		if r.Method != http.MethodPost || name == r.URL.Path || r.URL.RawQuery != "" || r.Header.Get(testingCapabilityHeader) != testingTestCapability {
			t.Errorf("wrong tool request: %s %s or missing capability", r.Method, r.URL.Path)
		}
		data, err := io.ReadAll(r.Body)
		if err != nil || !json.Valid(data) || strings.Contains(string(data), testingTestCapability) {
			t.Error("invalid body or capability in body")
		}
		var envelope testingToolRequestDTO
		if err := json.Unmarshal(data, &envelope); err != nil || envelope.SessionID != "worker-1" || envelope.RequestID == "" || len(envelope.RequestID) > 128 || requestIDs[envelope.RequestID] {
			t.Error("invalid session binding or duplicate request ID")
		}
		requestIDs[envelope.RequestID] = true
		var got, want any
		if err := json.Unmarshal(envelope.Input, &got); err != nil {
			t.Error(err)
		}
		if err := json.Unmarshal([]byte(inputs[name]), &want); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s input differs from call: %s", name, envelope.Input)
		}
		w.Header().Set("Content-Type", "application/json")
		if name == "screenshot" || name == "observe" || name == "click" || name == "type" || name == "key" {
			envelope := testingScreenshotDTO{Screenshot: &domain.TestScreenshot{
				Frame:    domain.TestDesktopFrame{ScreenshotID: "shot-1", Width: 1, Height: 1, CapturedAt: time.Date(2026, 10, 6, 1, 2, 3, 0, time.UTC)},
				MIMEType: "image/png", Data: png,
				Elements: []domain.TestElement{{ElementID: "element-1", Role: "AXTextField", Label: "Clone", Value: "hello", Frame: domain.TestWindowBounds{Width: 1, Height: 1}}},
			}, ObservationStatus: "captured", Evidence: []domain.TestEvidenceReceipt{{ID: "evidence-1", AttemptID: "attempt-1", Kind: "screenshot"}}}
			if name == "click" || name == "type" || name == "key" {
				envelope.Action = &domain.TestActionResult{Delivered: true, InputPath: "pointer"}
				envelope.ObservationStatus = "settled"
			}
			_ = json.NewEncoder(w).Encode(envelope)
		} else {
			_, _ = io.WriteString(w, responses[name])
		}
	}))
	t.Cleanup(server.Close)
	writeRunFileFor(t, cfg, server)
	session, finish := testingMCPPipe(t, "2025-06-18")
	for _, tc := range []struct{ name, input string }{
		{"screenshot", `{}`},
		{"observe", `{}`},
		{"click", `{"screenshotId":"shot-1","x":0,"y":0}`},
		{"type", `{"screenshotId":"shot-1","elementId":"element-1","text":"hello"}`},
		{"key", `{"screenshotId":"shot-1","keys":["ENTER"]}`},
		{"read_target_logs", `{}`},
		{"target_daemon_query", `{"resource":"sessions"}`},
		{"submit_report", `{"outcome":"partial","markdown":"# Observations"}`},
	} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.name, Arguments: json.RawMessage(tc.input)})
		if err != nil || result.IsError {
			t.Fatalf("%s result=%+v err=%v", tc.name, result, err)
		}
		if tc.name == "screenshot" || tc.name == "observe" || tc.name == "click" || tc.name == "type" || tc.name == "key" {
			if len(result.Content) != 2 {
				t.Fatalf("screenshot content: %+v", result.Content)
			}
			image, ok := result.Content[0].(*mcp.ImageContent)
			if !ok || image.MIMEType != "image/png" || !bytes.Equal(image.Data, png) {
				t.Fatalf("screenshot image: %+v", result.Content[0])
			}
			text := result.Content[1].(*mcp.TextContent).Text
			var metadata struct {
				ScreenshotID      string `json:"screenshotId"`
				Width, Height     int
				CapturedAt        string                       `json:"capturedAt"`
				Evidence          []domain.TestEvidenceReceipt `json:"evidence"`
				Elements          []domain.TestElement         `json:"elements"`
				Action            *domain.TestActionResult     `json:"action"`
				ObservationStatus string                       `json:"observationStatus"`
			}
			if err := json.Unmarshal([]byte(text), &metadata); err != nil || metadata.ScreenshotID != "shot-1" || metadata.Width != 1 || metadata.Height != 1 || metadata.CapturedAt != "2026-10-06T01:02:03Z" || len(metadata.Evidence) != 1 || metadata.Evidence[0].ID != "evidence-1" || len(metadata.Elements) != 1 || metadata.Elements[0].Value != "hello" {
				t.Fatalf("screenshot metadata: %s", text)
			}
			if (tc.name == "click" || tc.name == "type" || tc.name == "key") && (metadata.Action == nil || !metadata.Action.Delivered || metadata.ObservationStatus != "settled") {
				t.Fatal("action delivery or observation missing", text)
			}
			if strings.Contains(text, `"data"`) {
				t.Fatal("image duplicated in text", text)
			}
		} else if len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != responses[tc.name] {
			t.Fatalf("%s content=%+v", tc.name, result.Content)
		}
	}
	finish()
	if len(calls) != 8 {
		t.Fatalf("daemon calls: %v", calls)
	}
}

func TestTestingMCPDaemonErrorsStayToolErrorsAndRedactCapability(t *testing.T) {
	cfg := setConfigEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(apiError{Message: "expired " + testingTestCapability, Code: "TEST_EXPIRED", RequestID: "req-1"})
	}))
	t.Cleanup(server.Close)
	writeRunFileFor(t, cfg, server)
	session, finish := testingMCPPipe(t, "2026-07-28")
	for range 2 {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "screenshot", Arguments: map[string]any{}})
		if err != nil || !result.IsError {
			t.Fatalf("daemon error must be a tool error: result=%+v err=%v", result, err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if !strings.Contains(text, "expired [redacted]") || !strings.Contains(text, "TEST_EXPIRED") || !strings.Contains(text, "req-1") {
			t.Fatalf("error envelope lost: %q", text)
		}
	}
	finish()
}

func TestTestingMCPRejectsInvalidInputsBeforeDaemon(t *testing.T) {
	setConfigEnv(t)
	session, finish := testingMCPPipe(t, "2025-06-18")
	for _, tc := range []struct{ name, input string }{
		{"screenshot", `{"target":"supervisor"}`},
		{"click", `{"screenshotId":"s","elementId":"e","x":0,"y":0}`},
		{"click", `{"screenshotId":"s","elementId":""}`},
		{"observe", `{"target":"other"}`},
		{"click", `{"screenshotId":"s","x":-1,"y":0}`},
		{"click", `{"screenshotId":"s","x":0.5,"y":0}`},
		{"click", `{"screenshotId":"s","x":0,"y":0,"button":"other"}`},
		{"type", `{"screenshotId":"s","x":0,"y":0}`},
		{"key", `{"screenshotId":"s","keys":[]}`},
		{"key", `{"screenshotId":"s","keys":["a","b","c","d","e"]}`},
		{"read_target_logs", `{"maxBytes":262145}`},
		{"target_daemon_query", `{"resource":"/shutdown"}`},
		{"submit_report", `{"outcome":"pass","markdown":"ok"}`},
		{"submit_report", `{"outcome":"` + testingTestCapability + `","markdown":"ok"}`},
		{"submit_report", `{"outcome":"partial","markdown":"ok","target":"other"}`},
		{"submit_report", `{"outcome":"partial","markdown":"` + strings.Repeat("é", 32769) + `"}`},
	} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.name, Arguments: json.RawMessage(tc.input)})
		if err != nil || !result.IsError || strings.Contains(result.Content[0].(*mcp.TextContent).Text, "not running") {
			t.Fatalf("%s invalid input reached daemon: result=%+v err=%v", tc.name, result, err)
		}
	}
	finish()
}

func TestTestingMCPCancellationReachesDaemon(t *testing.T) {
	cfg := setConfigEnv(t)
	started, cancelled := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-release:
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	writeRunFileFor(t, cfg, server)
	session, finish := testingMCPPipe(t, "2025-06-18")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "screenshot", Arguments: map[string]any{}})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("tool did not reach daemon")
	}
	cancel()
	if err := <-done; err == nil {
		t.Error("cancelled tool succeeded")
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("MCP cancellation did not cancel daemon request")
	}
	finish()
}

func TestTestingMCPRequiresLaunchEnvironment(t *testing.T) {
	for _, missing := range []string{"AO_TEST_CAPABILITY", "AO_SESSION_ID", "AO_RUN_FILE"} {
		t.Run(missing, func(t *testing.T) {
			setConfigEnv(t)
			t.Setenv("AO_TEST_CAPABILITY", testingTestCapability)
			t.Setenv("AO_SESSION_ID", "worker-1")
			t.Setenv(missing, "")
			out, stderr, err := executeCLI(t, Deps{}, "testing", "mcp")
			if err == nil || ExitCode(err) != 1 || !strings.Contains(err.Error(), missing) || strings.Contains(err.Error(), testingTestCapability) || out != "" || stderr != "" {
				t.Fatalf("missing env: out=%q stderr=%q err=%v", out, stderr, err)
			}
		})
	}
}

func TestTestingMCPRefusesDaemonRedirect(t *testing.T) {
	cfg := setConfigEnv(t)
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("MCP followed daemon redirect")
	}))
	t.Cleanup(redirectTarget.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input testingToolRequestDTO
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || string(input.Input) != `{}` {
			t.Error("omitted MCP arguments must forward an empty object")
		}
		w.Header().Set("Location", redirectTarget.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	writeRunFileFor(t, cfg, server)
	session, finish := testingMCPPipe(t, "2025-06-18")
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "screenshot"})
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "HTTP 307") {
		t.Fatalf("redirect result=%+v err=%v", result, err)
	}
	finish()
}
