// Command terminal-echo-probe measures end-to-end terminal echo latency
// against a cloud session's workspace shell. Diagnostic used to reproduce
// the latency numbers in issue #4763 — not shipped.
//
// Local usage:
//
//	AO_BASE=http://127.0.0.1:8081 AO_DEV_TOKEN=... AO_ORG=... AO_SESSION=... \
//	    go run ./cloud/cmd/terminal-echo-probe
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// hostOverrideTransport routes requests by a Host header different from the
// dialed address — used to reach an environment through its load balancer
// before its DNS record exists (AO_HOST_HEADER), optionally skipping TLS
// verification when the SNI certificate cannot match (AO_TLS_INSECURE=1).
type hostOverrideTransport struct {
	base http.RoundTripper
	host string
}

func (t *hostOverrideTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.host != "" {
		req.Host = t.host
	}
	return t.base.RoundTrip(req)
}

func probeHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if os.Getenv("AO_TLS_INSECURE") == "1" {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &http.Client{
		Transport: &hostOverrideTransport{
			base: transport,
			host: strings.TrimSpace(os.Getenv("AO_HOST_HEADER")),
		},
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	host := strings.TrimRight(os.Getenv("AO_BASE"), "/")
	if host == "" {
		host = "http://127.0.0.1:8081"
	}
	base := host + "/api/cloud/v1"
	wsBase := strings.Replace(base, "http", "ws", 1)
	token := strings.TrimSpace(os.Getenv("AO_DEV_TOKEN"))
	org := os.Getenv("AO_ORG")
	session := os.Getenv("AO_SESSION")
	if token == "" || org == "" || session == "" {
		return fmt.Errorf("AO_DEV_TOKEN, AO_ORG, AO_SESSION required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Terminal ticket.
	req, _ := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/orgs/%s/sessions/%s/terminal-ticket", base, org, session),
		bytes.NewReader([]byte(`{"kind":"workspace"}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	httpClient := probeHTTPClient()
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var ticket struct {
		Ticket string `json:"ticket"`
		URL    string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ticket); err != nil || ticket.Ticket == "" {
		return fmt.Errorf("ticket response status=%d err=%v", resp.StatusCode, err)
	}

	wsURL := fmt.Sprintf(
		"%s/terminal?ticket=%s&kind=workspace&protocol=2",
		wsBase, ticket.Ticket,
	)
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: httpClient})
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")
	conn.SetReadLimit(1 << 20)

	type serverMsg struct {
		Type     string `json:"type"`
		Data     string `json:"data"`
		Sequence int64  `json:"sequence"`
	}
	msgs := make(chan serverMsg, 256)
	go func() {
		defer close(msgs)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var m serverMsg
			if json.Unmarshal(data, &m) == nil {
				msgs <- m
			}
		}
	}()

	// Wait for replay_complete, then let the shell prompt settle.
	for m := range msgs {
		if m.Type == "replay_complete" {
			break
		}
	}
	settle := time.After(3 * time.Second)
	for draining := true; draining; {
		select {
		case <-msgs:
		case <-settle:
			draining = false
		}
	}

	send := func(s string) error {
		payload, _ := json.Marshal(map[string]string{"type": "input", "data": s})
		return conn.Write(ctx, websocket.MessageText, payload)
	}

	// Echo samples: single printable chars; shell echoes each back.
	var samples []time.Duration
	chars := "abcdefghijklmnopqrstuvwxyz0123"
	for i := 0; i < len(chars); i++ {
		start := time.Now()
		if err := send(string(chars[i])); err != nil {
			return err
		}
		deadline := time.After(10 * time.Second)
	sampleWait:
		for {
			select {
			case m, ok := <-msgs:
				if !ok {
					return fmt.Errorf("sample %d: connection closed", i)
				}
				if m.Type != "output" {
					continue
				}
				samples = append(samples, time.Since(start))
				break sampleWait
			case <-deadline:
				return fmt.Errorf("sample %d: echo timeout", i)
			}
		}
		// Drain any trailing frames before the next sample.
		quiet := time.After(300 * time.Millisecond)
		for draining := true; draining; {
			select {
			case <-msgs:
			case <-quiet:
				draining = false
			}
		}
	}
	// Clean up the typed junk.
	_ = send("\x15") // ctrl-u clears the line

	sort.Slice(samples, func(a, b int) bool { return samples[a] < samples[b] })
	var sum time.Duration
	for _, s := range samples {
		fmt.Printf("echo rtt: %v\n", s.Round(time.Millisecond))
		sum += s
	}
	fmt.Printf("samples=%d min=%v median=%v mean=%v max=%v\n",
		len(samples),
		samples[0].Round(time.Millisecond),
		samples[len(samples)/2].Round(time.Millisecond),
		(sum / time.Duration(len(samples))).Round(time.Millisecond),
		samples[len(samples)-1].Round(time.Millisecond),
	)
	return nil
}
