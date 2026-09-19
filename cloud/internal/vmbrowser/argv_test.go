package vmbrowser

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNativeArguments(t *testing.T) {
	tests := []struct {
		name     string
		action   string
		args     map[string]any
		want     []string
		wantErr  string
		wantCode string
	}{
		{name: "open requires url", action: "open", args: nil, wantErr: "url is required"},
		{name: "open keeps explicit url", action: "open", args: map[string]any{"url": "https://example.com"}, want: []string{"open", "https://example.com"}},
		{name: "open rejects bare hostnames at argv layer", action: "open", args: map[string]any{"url": "localhost:3000"}, wantErr: "Unsupported browser URL scheme", wantCode: "BROWSER_URL_FORBIDDEN"},
		{name: "open rejects file scheme", action: "open", args: map[string]any{"url": "file:///tmp/x"}, wantErr: "Unsupported browser URL scheme", wantCode: "BROWSER_URL_FORBIDDEN"},
		{name: "snapshot default", action: "snapshot", want: []string{"snapshot", "--compact"}},
		{name: "snapshot interactive", action: "snapshot", args: map[string]any{"interactive": true}, want: []string{"snapshot", "--interactive", "--compact"}},
		{name: "snapshot interactive false stays off", action: "snapshot", args: map[string]any{"interactive": false}, want: []string{"snapshot", "--compact"}},
		{name: "click strips and adds at", action: "click", args: map[string]any{"ref": "e2"}, want: []string{"click", "@e2"}},
		{name: "click accepts prefilled at ref", action: "click", args: map[string]any{"ref": "@e2"}, want: []string{"click", "@e2"}},
		{name: "click uppercase ref preserves case", action: "click", args: map[string]any{"ref": "E2"}, want: []string{"click", "@E2"}},
		{name: "click keeps non-element refs verbatim", action: "click", args: map[string]any{"ref": "custom-ref"}, want: []string{"click", "custom-ref"}},
		{name: "click requires ref", action: "click", wantErr: "ref is required"},
		{name: "fill", action: "fill", args: map[string]any{"ref": "e3", "text": "hi"}, want: []string{"fill", "@e3", "hi"}},
		{name: "fill allows empty text", action: "fill", args: map[string]any{"ref": "e3", "text": ""}, want: []string{"fill", "@e3", ""}},
		{name: "press", action: "press", args: map[string]any{"key": "Enter"}, want: []string{"press", "Enter"}},
		{name: "drag", action: "drag", args: map[string]any{"ref": "e1", "targetRef": "e2"}, want: []string{"drag", "@e1", "@e2"}},
		{name: "select", action: "select", args: map[string]any{"ref": "e6", "value": "large"}, want: []string{"select", "@e6", "large"}},
		{name: "tabs", action: "tabs", want: []string{"tab", "list"}},
		{name: "tab new with url", action: "tab-new", args: map[string]any{"url": "https://example.com"}, want: []string{"tab", "new", "https://example.com"}},
		{name: "tab new bare", action: "tab-new", want: []string{"tab", "new"}},
		{name: "tab select", action: "tab-select", args: map[string]any{"tabId": "t-1"}, want: []string{"tab", "t-1"}},
		{name: "tab select requires id", action: "tab-select", wantErr: "tabId is required"},
		{name: "tab close explicit", action: "tab-close", args: map[string]any{"tabId": "t-1"}, want: []string{"tab", "close", "t-1"}},
		{name: "tab close default", action: "tab-close", want: []string{"tab", "close"}},
		{name: "scroll validates direction", action: "scroll", args: map[string]any{"direction": "sideways"}, wantErr: "direction must be up, down, left, or right"},
		{name: "scroll normalizes direction case", action: "scroll", args: map[string]any{"direction": "Down", "amount": 900}, want: []string{"scroll", "down", "900"}},
		{name: "scroll defaults amount", action: "scroll", args: map[string]any{"direction": "up"}, want: []string{"scroll", "up", "600"}},
		{name: "scroll rejects out of range amount", action: "scroll", args: map[string]any{"direction": "up", "amount": 999999}, wantErr: "Numeric argument must be between 1 and 5000"},
		{name: "get url no ref", action: "get", args: map[string]any{"property": "url"}, want: []string{"get", "url"}},
		{name: "get text with ref", action: "get", args: map[string]any{"property": "Text", "ref": "e4"}, want: []string{"get", "text", "@e4"}},
		{name: "get rejects unknown property", action: "get", args: map[string]any{"property": "html"}, wantErr: "Unsupported browser property"},
		{name: "get url rejects ref", action: "get", args: map[string]any{"property": "url", "ref": "e4"}, wantErr: "does not accept an element ref"},
		{name: "get value requires ref", action: "get", args: map[string]any{"property": "value"}, wantErr: "requires an element ref"},
		{name: "wait text", action: "wait", args: map[string]any{"text": "Ready", "timeoutMs": 5000}, want: []string{"wait", "--text", "Ready", "--timeout", "5000"}},
		{name: "wait text gone", action: "wait", args: map[string]any{"textGone": "Loading", "timeoutMs": 1000}, want: []string{"wait", "text=Loading", "--state", "hidden", "--timeout", "1000"}},
		{name: "wait selector", action: "wait", args: map[string]any{"selector": ".done", "timeoutMs": 1000}, want: []string{"wait", ".done", "--timeout", "1000"}},
		{name: "wait selector gone", action: "wait", args: map[string]any{"selectorGone": ".done", "timeoutMs": 1000}, want: []string{"wait", ".done", "--state", "detached", "--timeout", "1000"}},
		{name: "wait url", action: "wait", args: map[string]any{"url": "/dashboard", "timeoutMs": 1000}, want: []string{"wait", "--url", "**/dashboard**", "--timeout", "1000"}},
		{name: "wait load", action: "wait", args: map[string]any{"load": true, "timeoutMs": 1000}, want: []string{"wait", "--load", "load", "--timeout", "1000"}},
		{name: "wait fixed ms", action: "wait", args: map[string]any{"ms": 250}, want: []string{"wait", "250"}},
		{name: "wait defaults timeout", action: "wait", args: map[string]any{"text": "x"}, want: []string{"wait", "--text", "x", "--timeout", "10000"}},
		{name: "wait requires condition", action: "wait", args: map[string]any{"timeoutMs": 1000}, wantErr: "A wait condition is required"},
		{name: "frame main", action: "frame", args: map[string]any{"target": "main"}, want: []string{"frame", "main"}},
		{name: "frame ref", action: "frame", args: map[string]any{"target": "e5"}, want: []string{"frame", "@e5"}},
		{name: "dialog accept with text", action: "dialog", args: map[string]any{"operation": "accept", "text": "ok"}, want: []string{"dialog", "accept", "ok"}},
		{name: "dialog status", action: "dialog", args: map[string]any{"operation": "status"}, want: []string{"dialog", "status"}},
		{name: "dialog validates operation", action: "dialog", args: map[string]any{"operation": "explode"}, wantErr: "dialog operation must be accept, dismiss, or status"},
		{name: "console bare", action: "console", want: []string{"console"}},
		{name: "errors bare", action: "errors", want: []string{"errors"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NativeArguments(tt.action, tt.args)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("want error %q, got %v", tt.wantErr, got)
				}
				var ce *CommandError
				if !errors.As(err, &ce) {
					t.Fatalf("want CommandError, got %#v", err)
				}
				wantCode := tt.wantCode
				if wantCode == "" {
					wantCode = "INVALID_ARGUMENT"
				}
				if ce.Code != wantCode {
					t.Fatalf("code = %q, want %q", ce.Code, wantCode)
				}
				if !strings.Contains(ce.Message, tt.wantErr) {
					t.Fatalf("message = %q, want it to contain %q", ce.Message, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NativeArguments: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestNativeArgumentsWaitDomStabilityExpression(t *testing.T) {
	got, err := NativeArguments("wait", map[string]any{"stableMs": 750, "timeoutMs": 5000})
	if err != nil {
		t.Fatalf("NativeArguments: %v", err)
	}
	if got[0] != "wait" || got[1] != "--fn" || got[3] != "--timeout" || got[4] != "5000" {
		t.Fatalf("shape = %#v", got)
	}
	expression := got[2]
	for _, want := range []string{"__aoDomStability", "MutationObserver", "750"} {
		if !strings.Contains(expression, want) {
			t.Errorf("stability expression missing %q: %s", want, expression)
		}
	}
}

func TestCommandErrorRendering(t *testing.T) {
	err := &CommandError{Code: "STALE_REFERENCE", Message: "Unknown ref"}
	if err.Error() != "Unknown ref (STALE_REFERENCE)" {
		t.Fatalf("Error() = %q", err.Error())
	}
	bare := &CommandError{Message: "plain"}
	if bare.Error() != "plain" {
		t.Fatalf("bare Error() = %q", bare.Error())
	}
}
