package cli

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/pkg/browsercontract"
	"github.com/aoagents/agent-orchestrator/backend/pkg/clibrowser"
)

// The browser verb tree lives in pkg/clibrowser so the cloud in-VM CLI hosts
// the identical surface. This file is the desktop transport shim plus the
// package-local aliases its tests use.

const browserCapabilityHeader = browsercontract.CapabilityHeader

type (
	browserCommandRequestDTO    = clibrowser.CommandRequest
	browserCommandResponseDTO   = clibrowser.Response
	browserScreenshotFileResult = clibrowser.ScreenshotFileResult
)

const (
	browserUntrustedBegin = clibrowser.UntrustedBegin
	browserUntrustedEnd   = clibrowser.UntrustedEnd
)

func browserUntrustedText(value string) string { return clibrowser.UntrustedText(value) }

func writeBrowserResult(cmd *cobra.Command, action string, result map[string]any) error {
	return clibrowser.WriteResult(cmd, action, result)
}

func newBrowserCommand(ctx *commandContext) *cobra.Command {
	return clibrowser.NewCommand(
		daemonBrowserTransport{ctx: ctx},
		func() time.Time { return ctx.deps.Now() },
		"",
	)
}

// daemonBrowserTransport executes browser verbs against the loopback daemon
// using the same run-file discovery as every other daemon client.
type daemonBrowserTransport struct {
	ctx *commandContext
}

func (t daemonBrowserTransport) BrowserAction(
	ctx context.Context,
	action string,
	args map[string]any,
) (clibrowser.Response, error) {
	sessionID, capability, err := clibrowser.CurrentIdentity()
	if err != nil {
		return clibrowser.Response{}, err
	}
	var out clibrowser.Response
	err = t.ctx.doJSONPathWithHeaders(
		ctx,
		http.MethodPost,
		browsercontract.RouteCommands,
		clibrowser.CommandRequest{SessionID: sessionID, Action: action, Args: args},
		&out,
		map[string]string{browserCapabilityHeader: capability},
	)
	return out, err
}

func (t daemonBrowserTransport) BrowserStatus(ctx context.Context) (clibrowser.Status, error) {
	sessionID, capability, err := clibrowser.CurrentIdentity()
	if err != nil {
		return clibrowser.Status{}, err
	}
	var out clibrowser.Status
	err = t.ctx.doJSONPathWithHeaders(
		ctx,
		http.MethodGet,
		browsercontract.RouteStatus+"?sessionId="+url.QueryEscape(sessionID),
		nil,
		&out,
		map[string]string{browserCapabilityHeader: capability},
	)
	return out, err
}
