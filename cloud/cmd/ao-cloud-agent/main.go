// ao-cloud-agent is the session-local AO CLI installed as `ao` in Cloud
// workers. It is deliberately a thin client: every orchestration operation is
// authenticated and executed by the control plane.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

const (
	maxResponseBody = 2 << 20
	maxHookPayload  = 64 << 10
)

type client struct {
	baseURL   string
	tokenFile string
	http      *http.Client
}

type session struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	Harness          string `json:"harness"`
	DisplayName      string `json:"displayName"`
	Status           string `json:"status"`
	RuntimeConnected bool   `json:"runtimeConnected"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ao:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage(os.Stdout)
		return nil
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printUsage(os.Stdout)
		return nil
	}
	apiURL := strings.TrimRight(strings.TrimSpace(os.Getenv("AO_CLOUD_WORKER_API_URL")), "/")
	tokenFile := strings.TrimSpace(os.Getenv("AO_CLOUD_WORKER_TOKEN_FILE"))
	if apiURL == "" {
		publicURL := strings.TrimRight(
			strings.TrimSpace(os.Getenv("AO_CLOUD_PUBLIC_URL")), "/",
		)
		if publicURL != "" {
			apiURL = publicURL + "/api/cloud/v1"
		}
	}
	if tokenFile == "" {
		dataDir := strings.TrimSpace(os.Getenv("AO_DATA_DIR"))
		if dataDir != "" {
			tokenFile = filepath.Join(dataDir, "worker-token")
		}
	}
	if apiURL == "" || tokenFile == "" {
		return errors.New("this ao command must run inside an AO Cloud worker")
	}
	c := &client{
		baseURL: apiURL, tokenFile: tokenFile,
		http: &http.Client{Timeout: 30 * time.Second},
	}
	ctx := context.Background()
	switch args[0] {
	case "hooks":
		return runHook(ctx, c, args[1:], os.Stdin)
	case "spawn":
		return runSpawn(ctx, c, args[1:])
	case "list", "ls", "status":
		return runList(ctx, c, args[1:])
	case "send":
		return runSend(ctx, c, args[1:])
	case "kill", "delete", "rm":
		return runDelete(ctx, c, args[1:])
	case "claim-pr":
		return runClaimPullRequest(ctx, c, args[1:])
	default:
		return fmt.Errorf("unknown command %q; run `ao help`", args[0])
	}
}

func runHook(ctx context.Context, c *client, args []string, input io.Reader) error {
	if len(args) != 2 {
		return nil
	}
	payload, err := io.ReadAll(io.LimitReader(input, maxHookPayload+1))
	if err != nil || len(payload) > maxHookPayload {
		return nil
	}
	activity, ok := worker.ActivityEventFromHook(args[0], args[1], payload)
	if !ok {
		return nil
	}
	hookCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	_ = c.request(
		hookCtx,
		http.MethodPost,
		"/worker/events",
		worker.EventRequest{
			Type:    "agent.activity",
			Payload: activity,
		},
		false,
		nil,
	)
	// Hook delivery is best-effort and must never break the coding agent.
	return nil
}

func runSpawn(ctx context.Context, c *client, args []string) error {
	flags := flag.NewFlagSet("spawn", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var harness, name, prompt, mode, providerConnection string
	flags.StringVar(&harness, "harness", "claude-code", "agent harness")
	flags.StringVar(&harness, "agent", "claude-code", "alias for --harness")
	flags.StringVar(&name, "name", "", "child display name")
	flags.StringVar(&prompt, "prompt", "", "initial child prompt")
	flags.StringVar(&mode, "mode", "trusted", "standard or trusted")
	flags.StringVar(
		&providerConnection, "sandbox-provider-connection", "",
		"sandbox provider connection id",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("spawn requires --name")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return errors.New("spawn requires --prompt")
	}
	if mode != "standard" && mode != "trusted" {
		return errors.New("--mode must be standard or trusted")
	}
	body := map[string]any{
		"harness": harness, "displayName": name, "prompt": prompt, "mode": mode,
	}
	if providerConnection != "" {
		body["sandboxProviderConnectionId"] = providerConnection
	}
	var response struct {
		Session session `json:"session"`
	}
	if err := c.request(ctx, http.MethodPost, "/worker/children", body, true, &response); err != nil {
		return err
	}
	fmt.Printf("spawned %s (%s)\n", response.Session.ID, response.Session.Status)
	return nil
}

func runList(ctx context.Context, c *client, args []string) error {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	var response struct {
		Items []session `json:"items"`
	}
	if err := c.request(ctx, http.MethodGet, "/worker/children?limit=100", nil, false, &response); err != nil {
		return err
	}
	if *asJSON {
		encoded, err := json.MarshalIndent(response.Items, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	out := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(out, "ID\tNAME\tHARNESS\tSTATUS\tCONNECTED")
	for _, child := range response.Items {
		fmt.Fprintf(
			out, "%s\t%s\t%s\t%s\t%t\n",
			child.ID, child.DisplayName, child.Harness, child.Status,
			child.RuntimeConnected,
		)
	}
	return out.Flush()
}

func runSend(ctx context.Context, c *client, args []string) error {
	flags := flag.NewFlagSet("send", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	sessionID := flags.String("session", "", "child session id")
	message := flags.String("message", "", "message text")
	if err := flags.Parse(args); err != nil {
		return err
	}
	remaining := flags.Args()
	if *sessionID == "" && len(remaining) > 0 {
		*sessionID, remaining = remaining[0], remaining[1:]
	}
	if *message == "" && len(remaining) > 0 {
		*message = strings.Join(remaining, " ")
	}
	if strings.TrimSpace(*sessionID) == "" || strings.TrimSpace(*message) == "" {
		return errors.New("send requires a child session id and message")
	}
	path := "/worker/children/" + url.PathEscape(*sessionID) + "/messages"
	if err := c.request(
		ctx, http.MethodPost, path, map[string]string{"text": *message}, true, nil,
	); err != nil {
		return err
	}
	fmt.Println("message queued")
	return nil
}

func runDelete(ctx context.Context, c *client, args []string) error {
	flags := flag.NewFlagSet("kill", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	sessionID := flags.String("session", "", "child session id")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *sessionID == "" && len(flags.Args()) > 0 {
		*sessionID = flags.Args()[0]
	}
	if strings.TrimSpace(*sessionID) == "" {
		return errors.New("kill requires a child session id")
	}
	if err := c.request(
		ctx, http.MethodDelete,
		"/worker/children/"+url.PathEscape(*sessionID), nil, false, nil,
	); err != nil {
		return err
	}
	fmt.Println("session deletion requested")
	return nil
}

func runClaimPullRequest(ctx context.Context, c *client, args []string) error {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return errors.New("claim-pr requires a pull request number or URL")
	}
	var response worker.ClaimPullRequestResponse
	if err := c.request(
		ctx,
		http.MethodPost,
		"/worker/pull-requests/claim",
		worker.ClaimPullRequestRequest{Reference: strings.TrimSpace(args[0])},
		true,
		&response,
	); err != nil {
		return err
	}
	if response.ID == "" || response.Number <= 0 || response.HTMLURL == "" {
		return errors.New("control plane returned an incomplete pull request response")
	}
	fmt.Printf("claimed PR #%d %s\n", response.Number, response.HTMLURL)
	return nil
}

func (c *client) request(
	ctx context.Context,
	method, path string,
	body any,
	idempotent bool,
	out any,
) error {
	token, err := os.ReadFile(c.tokenFile)
	if err != nil {
		return fmt.Errorf("read rotating worker credential: %w", err)
	}
	if len(token) == 0 || len(token) > 16<<10 {
		return errors.New("rotating worker credential is invalid")
	}
	var encoded []byte
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(
		ctx, method, c.baseURL+path, bytes.NewReader(encoded),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Worker "+strings.TrimSpace(string(token)))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotent {
		request.Header.Set("Idempotency-Key", newIdempotencyKey())
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		value, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		var envelope struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		}
		if json.Unmarshal(value, &envelope) == nil && envelope.Message != "" {
			return fmt.Errorf("%s: %s", envelope.Code, envelope.Message)
		}
		return fmt.Errorf("control plane returned HTTP %d", response.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, maxResponseBody)).Decode(out)
}

func newIdempotencyKey() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("ao-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}

func printUsage(out io.Writer) {
	fmt.Fprintln(out, `AO Cloud orchestration commands:
  ao spawn --name NAME [--agent claude-code] [--prompt TEXT] [--mode standard|trusted]
  ao list [--json]
  ao send SESSION_ID MESSAGE
  ao kill SESSION_ID
	  ao claim-pr NUMBER_OR_URL

All commands are authenticated through the control plane. Child workers never
connect directly to one another.`)
}
