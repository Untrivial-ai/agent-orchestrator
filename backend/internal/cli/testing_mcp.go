package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const testingCapabilityHeader = "X-AO-Test-Capability"

// Keep these schemas in sync with the checkpoint 0 daemon tool contract.
var testingMCPTools = []struct {
	name, description, schema string
}{
	{"observe", "Observe the bound window first: image and bounded accessibility from the same capture. Prefer elementId for actions; coordinates are a fallback. IDs are consumed by input and replaced by the next observation. If inputReady is false, observe again before input.", `{"type":"object","properties":{},"additionalProperties":false}`},
	{"screenshot", "Capture the bound window as image plus accessibility. Coordinates use the returned image pixels; full-resolution evidence is retained.", `{"type":"object","properties":{},"additionalProperties":false}`},
	{"click", "Call observe first; prefer elementId, with x/y as a pointer fallback. Send one click and return a fresh observation. Delivery is not proof of effect; never repeat input after capture failure.", `{"type":"object","properties":{"screenshotId":{"type":"string","minLength":1},"elementId":{"type":"string","minLength":1,"maxLength":128},"x":{"type":"integer","minimum":0},"y":{"type":"integer","minimum":0},"button":{"enum":["left","right","middle"]}},"required":["screenshotId"],"oneOf":[{"required":["elementId"],"not":{"anyOf":[{"required":["x"]},{"required":["y"]}]}},{"required":["x","y"],"not":{"required":["elementId"]}}],"additionalProperties":false}`},
	{"type", "Call observe first; prefer elementId, with x/y as a pointer-focus fallback. Enter text once and return a fresh observation. Inspect its value/image; never repeat input after capture failure.", `{"type":"object","properties":{"screenshotId":{"type":"string","minLength":1},"elementId":{"type":"string","minLength":1,"maxLength":128},"x":{"type":"integer","minimum":0},"y":{"type":"integer","minimum":0},"text":{"type":"string","maxLength":16384}},"required":["screenshotId","text"],"oneOf":[{"required":["elementId"],"not":{"anyOf":[{"required":["x"]},{"required":["y"]}]}},{"required":["x","y"],"not":{"required":["elementId"]}}],"additionalProperties":false}`},
	{"key", "Send one key or chord using the latest observation and return a fresh observation. Never repeat input after capture failure.", `{"type":"object","properties":{"screenshotId":{"type":"string","minLength":1},"keys":{"type":"array","items":{"type":"string","minLength":1},"minItems":1,"maxItems":4}},"required":["screenshotId","keys"],"additionalProperties":false}`},
	{"read_target_logs", "Read bounded target logs. maxBytes defaults to 65536.", `{"type":"object","properties":{"cursor":{"type":"string","maxLength":256},"maxBytes":{"type":"integer","minimum":1,"maximum":262144}},"additionalProperties":false}`},
	{"target_daemon_query", "Read bound target projects/sessions, or reviews/conversation for one sessionId.", `{"type":"object","properties":{"resource":{"enum":["projects","sessions","reviews","conversation"]},"sessionId":{"type":"string","minLength":1,"maxLength":128,"pattern":"^[A-Za-z0-9_-]+$"}},"required":["resource"],"oneOf":[{"properties":{"resource":{"enum":["projects","sessions"]}},"not":{"required":["sessionId"]}},{"properties":{"resource":{"enum":["reviews","conversation"]}},"required":["sessionId"]}],"additionalProperties":false}`},
	{"submit_report", "Save the attempt outcome and Markdown report, up to 64 KiB.", `{"type":"object","properties":{"outcome":{"enum":["reproduced","not_reproduced","needs_information","environment_blocked","partial","cancelled"]},"markdown":{"type":"string","maxLength":65536}},"required":["outcome","markdown"],"additionalProperties":false}`},
}

func newTestingMCPCommand(ctx *commandContext) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve the worker session's testing tools over stdio",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			for _, name := range []string{"AO_TEST_CAPABILITY", "AO_SESSION_ID", "AO_RUN_FILE"} {
				if strings.TrimSpace(os.Getenv(name)) == "" {
					return fmt.Errorf("testing mcp requires %s in its launch environment", name)
				}
			}
			capability := os.Getenv("AO_TEST_CAPABILITY")
			server := ctx.newTestingMCPServer(os.Getenv("AO_SESSION_ID"), capability)
			reader, ok := cmd.InOrStdin().(io.ReadCloser)
			if !ok {
				reader = io.NopCloser(cmd.InOrStdin())
			}
			err := server.Run(cmd.Context(), &mcp.IOTransport{
				Reader: reader, Writer: testingMCPWriter{cmd.OutOrStdout()},
			})
			if err != nil {
				return errors.New(redactTestingCapability(err.Error(), capability))
			}
			return nil
		},
	}
}

type testingMCPWriter struct{ io.Writer }

func (testingMCPWriter) Close() error { return nil }

func redactTestingCapability(text, capability string) string {
	return strings.ReplaceAll(text, capability, "[redacted]")
}

func (c *commandContext) newTestingMCPServer(sessionID, capability string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "ao-testing", Version: Version}, nil)
	// SDK validation errors can quote invalid argument values, so redact them too.
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if toolResult, ok := result.(*mcp.CallToolResult); ok {
				for _, content := range toolResult.Content {
					if text, ok := content.(*mcp.TextContent); ok {
						text.Text = redactTestingCapability(text.Text, capability)
					}
				}
			}
			if err != nil && strings.Contains(err.Error(), capability) {
				err = errors.New(redactTestingCapability(err.Error(), capability))
			}
			return result, err
		}
	})
	// Do not allow redirects to move the capability outside the loopback daemon.
	client := *c.deps.HTTPClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	forward := &commandContext{deps: c.deps}
	forward.deps.HTTPClient = &client
	for _, tool := range testingMCPTools {
		mcp.AddTool(server, &mcp.Tool{
			Name: tool.name, Description: tool.description, InputSchema: json.RawMessage(tool.schema),
		}, func(ctx context.Context, req *mcp.CallToolRequest, input map[string]any) (*mcp.CallToolResult, any, error) {
			if tool.name == "submit_report" {
				markdown, ok := input["markdown"].(string)
				if !ok || len(markdown) > 64<<10 {
					return nil, nil, errors.New("report markdown must be a string of at most 64 KiB")
				}
			}
			arguments := req.Params.Arguments
			if len(arguments) == 0 {
				arguments = json.RawMessage(`{}`)
			}
			var response json.RawMessage
			err := forward.doJSONPathWithHeaders(ctx, http.MethodPost,
				"/api/v1/testing/sessions/"+url.PathEscape(sessionID)+"/tools/"+tool.name,
				testingToolRequestDTO{SessionID: sessionID, RequestID: uuid.NewString(), Input: arguments},
				&response, map[string]string{testingCapabilityHeader: capability})
			if err != nil {
				return nil, nil, errors.New(redactTestingCapability(err.Error(), capability))
			}
			if tool.name != "screenshot" && tool.name != "observe" && tool.name != "click" && tool.name != "type" && tool.name != "key" {
				return &mcp.CallToolResult{Content: []mcp.Content{
					&mcp.TextContent{Text: redactTestingCapability(string(response), capability)},
				}}, nil, nil
			}
			var envelope testingScreenshotDTO
			if err := json.Unmarshal(response, &envelope); err != nil {
				return nil, nil, errors.New("daemon returned an invalid screenshot response")
			}
			screenshot := envelope.Screenshot
			if screenshot == nil && envelope.Action != nil {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{
					&mcp.TextContent{Text: redactTestingCapability(string(response), capability)},
					&mcp.TextContent{Text: "Post-action observation failed. Inspect delivery above; do not repeat the input. Call observe to recover."},
				}}, nil, nil
			}
			if screenshot == nil || screenshot.MIMEType != "image/png" || len(screenshot.Data) == 0 ||
				screenshot.Frame.ScreenshotID == "" || screenshot.Frame.Width <= 0 || screenshot.Frame.Height <= 0 || screenshot.Frame.CapturedAt.IsZero() {
				return nil, nil, errors.New("daemon returned an incomplete PNG screenshot")
			}
			frame := screenshot.Frame
			metadata, err := json.Marshal(struct {
				ScreenshotID      string                       `json:"screenshotId"`
				Width             int                          `json:"width"`
				Height            int                          `json:"height"`
				CapturedAt        string                       `json:"capturedAt"`
				Evidence          []domain.TestEvidenceReceipt `json:"evidence"`
				Elements          []domain.TestElement         `json:"elements"`
				Truncated         bool                         `json:"truncated"`
				InputReady        bool                         `json:"inputReady"`
				Action            *domain.TestActionResult     `json:"action,omitempty"`
				ObservationStatus string                       `json:"observationStatus,omitempty"`
				ObservationError  string                       `json:"observationError,omitempty"`
			}{frame.ScreenshotID, frame.Width, frame.Height, frame.CapturedAt.Format(time.RFC3339Nano), envelope.Evidence,
				screenshot.Elements, screenshot.Truncated, screenshot.InputReady, envelope.Action, envelope.ObservationStatus, envelope.ObservationError})
			if err != nil {
				return nil, nil, err
			}
			return &mcp.CallToolResult{IsError: envelope.ObservationStatus == "failed", Content: []mcp.Content{
				&mcp.ImageContent{Data: screenshot.Data, MIMEType: screenshot.MIMEType},
				&mcp.TextContent{Text: redactTestingCapability(string(metadata), capability)},
			}}, nil, nil
		})
	}
	return server
}
