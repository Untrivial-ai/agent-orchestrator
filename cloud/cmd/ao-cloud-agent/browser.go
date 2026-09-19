package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/browsercontract"
	"github.com/aoagents/agent-orchestrator/backend/pkg/clibrowser"
)

// runBrowser hosts the shared browser verb tree against the in-VM browser
// service. The verbs, validation, output shaping, and untrusted-content
// markers are the same code the desktop CLI uses (pkg/clibrowser).
func runBrowser(args []string) error {
	apiURL := strings.TrimSpace(os.Getenv("AO_BROWSER_API_URL"))
	if apiURL == "" {
		return errors.New("ao browser requires a cloud session browser service (AO_BROWSER_API_URL is not set)")
	}
	transport := &cloudBrowserTransport{
		baseURL: strings.TrimRight(apiURL, "/"),
		client:  &http.Client{Timeout: 2 * time.Minute},
	}
	cmd := clibrowser.NewCommand(
		transport,
		nil,
		"Inspect and control the target-isolated browser owned by the current AO session.\n\n"+
			"Commands operate the session's browser service inside this cloud sandbox.",
	)
	cmd.SetArgs(args)
	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)
	if err := cmd.Execute(); err != nil {
		var usage *clibrowser.UsageError
		if errors.As(err, &usage) {
			fmt.Fprintln(os.Stderr, "ao:", err)
			os.Exit(2)
		}
		return err
	}
	return nil
}

// cloudBrowserTransport carries browser commands to the loopback browserd.
type cloudBrowserTransport struct {
	baseURL string
	client  *http.Client
}

func (t *cloudBrowserTransport) BrowserAction(
	ctx context.Context,
	action string,
	args map[string]any,
) (clibrowser.Response, error) {
	sessionID, capability, err := clibrowser.CurrentIdentity()
	if err != nil {
		return clibrowser.Response{}, err
	}
	var out clibrowser.Response
	err = t.post(ctx, browsercontract.RouteCommands, clibrowser.CommandRequest{
		SessionID: sessionID, Action: action, Args: args,
	}, &out, capability)
	return out, err
}

func (t *cloudBrowserTransport) BrowserStatus(ctx context.Context) (clibrowser.Status, error) {
	sessionID, capability, err := clibrowser.CurrentIdentity()
	if err != nil {
		return clibrowser.Status{}, err
	}
	var out clibrowser.Status
	err = t.get(ctx, browsercontract.RouteStatus+"?sessionId="+sessionID, &out, capability)
	return out, err
}

type browserAPIError struct {
	Message   string `json:"message"`
	Code      string `json:"code"`
	RequestID string `json:"requestId"`
}

func (e browserAPIError) Error() string {
	message := e.Message
	if e.Code != "" {
		message = fmt.Sprintf("%s (%s)", message, e.Code)
	}
	if e.RequestID != "" {
		message = fmt.Sprintf("%s [request %s]", message, e.RequestID)
	}
	return message
}

func (t *cloudBrowserTransport) post(ctx context.Context, path string, body, out any, capability string) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	return t.do(request, out, capability)
}

func (t *cloudBrowserTransport) get(ctx context.Context, path string, out any, capability string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+path, nil)
	if err != nil {
		return err
	}
	return t.do(request, out, capability)
}

func (t *cloudBrowserTransport) do(request *http.Request, out any, capability string) error {
	request.Header.Set(browsercontract.CapabilityHeader, capability)
	response, err := t.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiErr browserAPIError
		if err := json.NewDecoder(response.Body).Decode(&apiErr); err == nil && apiErr.Message != "" {
			return apiErr
		}
		return fmt.Errorf("browser service returned HTTP %d", response.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(out)
}
