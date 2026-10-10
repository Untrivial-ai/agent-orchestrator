package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTestingMCPKeepsDeliveryWhenPostActionCaptureFails(t *testing.T) {
	cfg := setConfigEnv(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"action":{"delivered":true,"inputPath":"unknown","requestedInputPath":"ax"},"observationStatus":"failed","observationError":"TEST_SCREENSHOT_FAILED","evidence":[]}`)
	}))
	t.Cleanup(server.Close)
	writeRunFileFor(t, cfg, server)
	session, finish := testingMCPPipe(t, "2026-07-28")
	defer finish()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "click", Arguments: json.RawMessage(`{"screenshotId":"s","elementId":"e"}`)})
	if err != nil || !result.IsError || calls != 1 || len(result.Content) != 2 {
		t.Fatal(result, err, calls)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, `"delivered":true`) || !strings.Contains(text, "TEST_SCREENSHOT_FAILED") || !strings.Contains(result.Content[1].(*mcp.TextContent).Text, "do not repeat") {
		t.Fatal("delivery hidden or retry encouraged", result)
	}
}

func TestTestingMCPRetainsUnsettledImageWithoutInputReadiness(t *testing.T) {
	cfg := setConfigEnv(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"screenshot":{"frame":{"screenshotId":"retained","width":1,"height":1,"capturedAt":"2026-10-06T17:00:00Z"},"mimeType":"image/png","data":"AQ==","elements":[],"inputReady":false},"action":{"delivered":true,"inputPath":"unknown","requestedInputPath":"ax"},"observationStatus":"unsettled","observationError":"TEST_SETTLE_TIMEOUT","evidence":[]}`)
	}))
	t.Cleanup(server.Close)
	writeRunFileFor(t, cfg, server)
	session, finish := testingMCPPipe(t, "2026-07-28")
	defer finish()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "click", Arguments: json.RawMessage(`{"screenshotId":"s","elementId":"e"}`)})
	if err != nil || result.IsError || calls != 1 || len(result.Content) != 2 {
		t.Fatal(result, err, calls)
	}
	image, ok := result.Content[0].(*mcp.ImageContent)
	if !ok || image.MIMEType != "image/png" || len(image.Data) != 1 {
		t.Fatal("retained visual observation missing", result)
	}
	var metadata struct {
		InputReady        bool   `json:"inputReady"`
		ObservationStatus string `json:"observationStatus"`
		ObservationError  string `json:"observationError"`
		Action            struct {
			Delivered          bool   `json:"delivered"`
			InputPath          string `json:"inputPath"`
			RequestedInputPath string `json:"requestedInputPath"`
		} `json:"action"`
	}
	if err := json.Unmarshal([]byte(result.Content[1].(*mcp.TextContent).Text), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.InputReady || metadata.ObservationStatus != "unsettled" || metadata.ObservationError != "TEST_SETTLE_TIMEOUT" || !metadata.Action.Delivered || metadata.Action.InputPath != "unknown" || metadata.Action.RequestedInputPath != "ax" {
		t.Fatal("retained observation readiness or delivery misreported", metadata)
	}
}
