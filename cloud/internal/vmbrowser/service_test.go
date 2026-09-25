package vmbrowser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/browsercontract"
)

// fakeEngine implements EngineLike with canned responses per action.
type fakeEngine struct {
	mu         sync.Mutex
	executed   []string
	execute    func(action string, args map[string]any) (map[string]any, error)
	screenshot func() (string, int, int, error)
}

func (f *fakeEngine) Execute(_ context.Context, action string, args map[string]any) (map[string]any, error) {
	f.mu.Lock()
	f.executed = append(f.executed, action)
	f.mu.Unlock()
	if f.execute != nil {
		return f.execute(action, args)
	}
	return map[string]any{"untrustedExternalContent": true}, nil
}

func (f *fakeEngine) Screenshot(context.Context) (string, int, int, error) {
	if f.screenshot != nil {
		return f.screenshot()
	}
	return "cG5n", 10, 20, nil
}

func (f *fakeEngine) Close(context.Context) error { return nil }

func newTestService(t *testing.T, engine EngineLike) (*Service, string) {
	t.Helper()
	authority := browsercontract.NewAuthority()
	token, verifier, err := authority.Issue("sess-1")
	if err != nil {
		t.Fatalf("issue capability: %v", err)
	}
	service := NewService(ServiceOptions{
		SessionID:          "sess-1",
		CapabilityVerifier: verifier,
		Engine:             engine,
	})
	return service, token
}

func doBrowserRequest(t *testing.T, handler http.Handler, method, target, capability, body string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	if capability != "" {
		request.Header.Set(browsercontract.CapabilityHeader, capability)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

func envelopeField(t *testing.T, body []byte, field string) string {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode envelope %s: %v", body, err)
	}
	value, _ := decoded[field].(string)
	return value
}

func decodeResult(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var decoded struct {
		RequestID string         `json:"requestId"`
		SessionID string         `json:"sessionId"`
		Action    string         `json:"action"`
		Result    map[string]any `json:"result"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode response %s: %v", body, err)
	}
	if decoded.RequestID == "" || decoded.SessionID != "sess-1" {
		t.Fatalf("contract fields wrong: %+v", decoded)
	}
	return decoded.Result
}

func TestServiceCommandHappyPathMirrorsDesktopContract(t *testing.T) {
	engine := &fakeEngine{execute: func(action string, _ map[string]any) (map[string]any, error) {
		if action == "open" {
			return map[string]any{"url": "https://example.com/", "title": "Example"}, nil
		}
		return map[string]any{"ok": true}, nil
	}}
	service, capability := newTestService(t, engine)
	status, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
		capability, `{"sessionId":"sess-1","action":"open","args":{"url":"https://example.com"}}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	result := decodeResult(t, body)
	if result["url"] != "https://example.com/" || result["title"] != "Example" {
		t.Fatalf("open result = %#v", result)
	}
	if result["untrustedExternalContent"] != true {
		t.Error("result must be marked untrustedExternalContent")
	}
}

func TestServiceOpenNormalizesBareLocalhost(t *testing.T) {
	var captured map[string]any
	engine := &fakeEngine{execute: func(action string, args map[string]any) (map[string]any, error) {
		if action == "open" {
			captured = args
		}
		return map[string]any{"url": "http://localhost:3000/"}, nil
	}}
	service, capability := newTestService(t, engine)
	status, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
		capability, `{"sessionId":"sess-1","action":"open","args":{"url":"localhost:3000"}}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if captured["url"] != "http://localhost:3000/" {
		t.Fatalf("engine received url = %#v, want normalized http://localhost:3000/ (href form)", captured["url"])
	}
}

func TestServiceOpenRejectsLocalFiles(t *testing.T) {
	for _, raw := range []string{"/etc/passwd", "file:///tmp/x", "C:\\Users\\x"} {
		service, capability := newTestService(t, &fakeEngine{})
		status, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
			capability, fmt.Sprintf(`{"sessionId":"sess-1","action":"open","args":{"url":%q}}`, raw))
		if status != http.StatusBadRequest {
			t.Fatalf("%q: status = %d", raw, status)
		}
		if envelopeField(t, body, "code") != "BROWSER_URL_FORBIDDEN" {
			t.Fatalf("%q: code = %q", raw, envelopeField(t, body, "code"))
		}
	}
}

func TestServiceOpenRequiresURL(t *testing.T) {
	service, capability := newTestService(t, &fakeEngine{})
	status, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
		capability, `{"sessionId":"sess-1","action":"open"}`)
	if status != http.StatusBadRequest || envelopeField(t, body, "code") != "URL_REQUIRED" {
		t.Fatalf("status = %d code = %q", status, envelopeField(t, body, "code"))
	}
}

func TestServiceRejectsWrongCapabilityWithForbiddenEnvelope(t *testing.T) {
	service, _ := newTestService(t, &fakeEngine{})
	status, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
		"wrong-token", `{"sessionId":"sess-1","action":"console"}`)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
	if envelopeField(t, body, "code") != "BROWSER_CAPABILITY_INVALID" {
		t.Fatalf("code = %q", envelopeField(t, body, "code"))
	}
	if envelopeField(t, body, "requestId") == "" || envelopeField(t, body, "message") == "" {
		t.Fatalf("envelope must carry message and requestId: %s", body)
	}
}

func TestServiceRejectsOtherSessions(t *testing.T) {
	service, capability := newTestService(t, &fakeEngine{})
	status, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
		capability, `{"sessionId":"sess-2","action":"console"}`)
	if status != http.StatusForbidden || envelopeField(t, body, "code") != "BROWSER_CAPABILITY_INVALID" {
		t.Fatalf("status = %d code = %q", status, envelopeField(t, body, "code"))
	}
}

func TestServiceRejectsUnsupportedAction(t *testing.T) {
	service, capability := newTestService(t, &fakeEngine{})
	status, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
		capability, `{"sessionId":"sess-1","action":"navigate"}`)
	if status != http.StatusBadRequest || envelopeField(t, body, "code") != "BROWSER_ACTION_UNSUPPORTED" {
		t.Fatalf("status = %d code = %q", status, envelopeField(t, body, "code"))
	}
}

func TestServiceCloudUnsupportedVerbs(t *testing.T) {
	for _, action := range []string{"network-start", "network-status", "network-list", "network-stop",
		"network-clear", "unhighlight"} {
		t.Run(action, func(t *testing.T) {
			service, capability := newTestService(t, &fakeEngine{})
			status, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
				capability, fmt.Sprintf(`{"sessionId":"sess-1","action":%q}`, action))
			if status != http.StatusBadRequest {
				t.Fatalf("%s: status = %d", action, status)
			}
			if code := envelopeField(t, body, "code"); code != "BROWSER_ACTION_UNSUPPORTED_CLOUD" {
				t.Fatalf("%s: code = %q", action, code)
			}
		})
	}
}

func TestServicePassthroughPrevalidationCodes(t *testing.T) {
	tests := []struct {
		body string
		code string
	}{
		{`{"sessionId":"sess-1","action":"click"}`, "REFERENCE_REQUIRED"},
		{`{"sessionId":"sess-1","action":"fill","args":{"ref":"e1"}}`, "INVALID_ARGUMENT"},
		{`{"sessionId":"sess-1","action":"drag","args":{"ref":"e1"}}`, "REFERENCE_REQUIRED"},
		{`{"sessionId":"sess-1","action":"press"}`, "INVALID_ARGUMENT"},
		{`{"sessionId":"sess-1","action":"tab-select"}`, "TAB_ID_REQUIRED"},
	}
	for _, tt := range tests {
		t.Run(tt.code+"/"+tt.body[strings.Index(tt.body, `"action":"`)+10:], func(t *testing.T) {
			service, capability := newTestService(t, &fakeEngine{})
			status, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
				capability, tt.body)
			if status != http.StatusBadRequest || envelopeField(t, body, "code") != tt.code {
				t.Fatalf("status = %d code = %q body = %s", status, envelopeField(t, body, "code"), body)
			}
		})
	}
}

func TestServiceSnapshotShape(t *testing.T) {
	engine := &fakeEngine{execute: func(action string, _ map[string]any) (map[string]any, error) {
		if action == "snapshot" {
			return map[string]any{
				"snapshot": "- button \"Login\" [ref=e1]",
				"refs":     map[string]any{"e1": map[string]any{"role": "button", "name": "Login"}},
			}, nil
		}
		return map[string]any{}, nil
	}}
	service, capability := newTestService(t, engine)
	_, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
		capability, `{"sessionId":"sess-1","action":"snapshot","args":{"interactive":true}}`)
	result := decodeResult(t, body)
	if result["text"] != "- button \"Login\" [ref=e1]" || result["refs"] == nil {
		t.Fatalf("snapshot result = %#v", result)
	}
}

func TestServiceActComposite(t *testing.T) {
	var verbs []string
	engine := &fakeEngine{execute: func(action string, args map[string]any) (map[string]any, error) {
		switch action {
		case "snapshot":
			return map[string]any{
				"snapshot": "- button \"Login\" [ref=e1]",
				"refs":     map[string]any{"e1": map[string]any{"role": "button", "name": "Login"}},
			}, nil
		case "click", "dblclick", "focus", "hover", "fill", "type", "check", "uncheck":
			verbs = append(verbs, action)
			return map[string]any{"clicked": args["ref"]}, nil
		}
		return map[string]any{}, nil
	}}
	service, capability := newTestService(t, engine)
	_, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
		capability, `{"sessionId":"sess-1","action":"act","args":{"instruction":"the Login button"}}`)
	result := decodeResult(t, body)
	if result["outcome"] != "matched" || result["resolvedRef"] != "e1" {
		t.Fatalf("act result = %#v", result)
	}
	candidate, _ := result["candidate"].(map[string]any)
	if candidate["role"] != "button" || candidate["name"] != "Login" {
		t.Fatalf("candidate = %#v", candidate)
	}
	if result["retried"] != false || result["untrustedExternalContent"] != true {
		t.Fatalf("act flags wrong: %#v", result)
	}
}

func TestServiceActRetriesOnceOnStaleReference(t *testing.T) {
	clickCount := 0
	engine := &fakeEngine{execute: func(action string, args map[string]any) (map[string]any, error) {
		switch action {
		case "snapshot":
			ref := "e1"
			if clickCount > 0 {
				ref = "e2"
			}
			return map[string]any{
				"snapshot": "- button \"Login\" [ref=" + ref + "]",
				"refs":     map[string]any{ref: map[string]any{"role": "button", "name": "Login"}},
			}, nil
		case "click":
			clickCount++
			if clickCount == 1 {
				return nil, &CommandError{Code: "STALE_REFERENCE", Message: "Unknown ref: @e1"}
			}
			return map[string]any{"clicked": args["ref"]}, nil
		}
		return map[string]any{}, nil
	}}
	service, capability := newTestService(t, engine)
	_, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
		capability, `{"sessionId":"sess-1","action":"act","args":{"instruction":"the Login button"}}`)
	result := decodeResult(t, body)
	if result["outcome"] != "matched" || result["resolvedRef"] != "e2" || result["retried"] != true {
		t.Fatalf("act retry result = %#v", result)
	}
}

func TestServiceActAmbiguousReturnsCandidates(t *testing.T) {
	engine := &fakeEngine{execute: func(action string, _ map[string]any) (map[string]any, error) {
		if action == "snapshot" {
			return map[string]any{
				"snapshot": "- buttons",
				"refs": map[string]any{
					"e1": map[string]any{"role": "button", "name": "Add to Cart"},
					"e2": map[string]any{"role": "button", "name": "Add to Cart"},
				},
			}, nil
		}
		return map[string]any{}, nil
	}}
	service, capability := newTestService(t, engine)
	_, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
		capability, `{"sessionId":"sess-1","action":"act","args":{"instruction":"add to cart"}}`)
	result := decodeResult(t, body)
	if result["outcome"] != "ambiguous" {
		t.Fatalf("outcome = %#v", result["outcome"])
	}
	candidates, _ := result["candidates"].([]any)
	if len(candidates) != 2 {
		t.Fatalf("candidates = %#v", result["candidates"])
	}
}

func TestServiceTabsShapesAndSanitization(t *testing.T) {
	engine := &fakeEngine{execute: func(action string, _ map[string]any) (map[string]any, error) {
		switch action {
		case "tabs":
			return map[string]any{"tabs": []any{
				map[string]any{"tabId": "t1", "title": "First", "url": "https://u:p@a.test/x?token=1#frag", "active": false},
				map[string]any{"tabId": "t2", "title": "Second", "url": "https://b.test/", "active": true},
			}}, nil
		case "tab-new":
			return map[string]any{}, nil
		}
		return map[string]any{}, nil
	}}
	service, capability := newTestService(t, engine)
	_, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
		capability, `{"sessionId":"sess-1","action":"tabs"}`)
	result := decodeResult(t, body)
	if result["activeTabId"] != "t2" {
		t.Fatalf("activeTabId = %#v", result["activeTabId"])
	}
	tabs, _ := result["tabs"].([]any)
	first, _ := tabs[0].(map[string]any)
	if first["url"] != "https://a.test/x?token=%5Bredacted%5D" {
		t.Fatalf("tab url not sanitized: %#v", first["url"])
	}
	if _, exists := first["viewId"]; exists {
		t.Error("viewId is Electron-only and must not appear")
	}

	_, body = doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
		capability, `{"sessionId":"sess-1","action":"tab-new","args":{"url":"example.com"}}`)
	tab := decodeResult(t, body)
	if tab["id"] != "t2" || tab["active"] != true {
		t.Fatalf("tab-new result = %#v (want bare active tab object)", tab)
	}
}

func TestServiceGetSanitizesURLAndTitle(t *testing.T) {
	engine := &fakeEngine{execute: func(action string, _ map[string]any) (map[string]any, error) {
		if action == "get" {
			return map[string]any{"url": "https://u:p@x.test/a?tok=1#f"}, nil
		}
		return map[string]any{}, nil
	}}
	service, capability := newTestService(t, engine)
	_, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
		capability, `{"sessionId":"sess-1","action":"get","args":{"property":"url"}}`)
	result := decodeResult(t, body)
	if result["value"] != "https://x.test/a?tok=%5Bredacted%5D" {
		t.Fatalf("get url = %#v", result["value"])
	}
}

func TestServiceConsoleNormalizesAndMarksUntrusted(t *testing.T) {
	engine := &fakeEngine{execute: func(action string, _ map[string]any) (map[string]any, error) {
		return map[string]any{"messages": []any{
			"plain https://u:p@x.test/a?tok=1",
			map[string]any{"level": "warn", "message": "object message"},
		}}, nil
	}}
	service, capability := newTestService(t, engine)
	_, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
		capability, `{"sessionId":"sess-1","action":"console"}`)
	result := decodeResult(t, body)
	messages, _ := result["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", result["messages"])
	}
	first, _ := messages[0].(map[string]any)
	if !strings.Contains(first["message"].(string), untrustedBegin) {
		t.Fatalf("message not marked untrusted: %#v", first)
	}
	if strings.Contains(first["message"].(string), "u:p@") {
		t.Fatalf("URL in message not sanitized: %#v", first)
	}
	second, _ := messages[1].(map[string]any)
	if second["level"] != "warn" {
		t.Fatalf("level fallback wrong: %#v", second)
	}
}

func TestServiceScreenshot(t *testing.T) {
	service, capability := newTestService(t, &fakeEngine{})
	_, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
		capability, `{"sessionId":"sess-1","action":"screenshot"}`)
	result := decodeResult(t, body)
	if result["data"] != "cG5n" || result["width"] != float64(10) || result["height"] != float64(20) {
		t.Fatalf("screenshot result = %#v", result)
	}
}

func TestServiceStatusRoute(t *testing.T) {
	service, capability := newTestService(t, &fakeEngine{})
	_, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands",
		capability, `{"sessionId":"sess-1","action":"console"}`)
	status, body := doBrowserRequest(t, service.Handler(), http.MethodGet, "/api/v1/browser/status?sessionId=sess-1", capability, "")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	var decoded struct {
		SessionID string `json:"sessionId"`
		Connected bool   `json:"connected"`
		Transport string `json:"transport"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if decoded.SessionID != "sess-1" || !decoded.Connected || decoded.Transport != "vm-chromium" {
		t.Fatalf("status = %#v", decoded)
	}
}

func TestServiceStatusRequiresSessionAndCapability(t *testing.T) {
	service, capability := newTestService(t, &fakeEngine{})
	status, body := doBrowserRequest(t, service.Handler(), http.MethodGet, "/api/v1/browser/status", capability, "")
	if status != http.StatusBadRequest || envelopeField(t, body, "code") != "SESSION_ID_REQUIRED" {
		t.Fatalf("missing sessionId: status = %d", status)
	}
	status, body = doBrowserRequest(t, service.Handler(), http.MethodGet, "/api/v1/browser/status?sessionId=sess-1", "", "")
	if status != http.StatusForbidden {
		t.Fatalf("missing capability: status = %d", status)
	}
}

func TestServiceRejectsOversizedBody(t *testing.T) {
	service, capability := newTestService(t, &fakeEngine{})
	big := bytes.Repeat([]byte("a"), (1<<20)+10)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/browser/commands", strings.NewReader(`{"sessionId":"sess-1","action":"fill","args":{"ref":"e1","text":"`+string(big)+`"}}`))
	request.Header.Set(browsercontract.CapabilityHeader, capability)
	recorder := httptest.NewRecorder()
	service.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversized body status = %d, want 400", recorder.Code)
	}
}
