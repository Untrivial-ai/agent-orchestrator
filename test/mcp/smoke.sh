#!/usr/bin/env bash
# Smoke-test ao mcp against a fake loopback daemon.
# Builds ao, serves a minimal /api/v1 surface, points AO_RUN_FILE at it,
# and verifies list_projects via the official Go MCP client over stdio.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/ao-mcp-smoke.XXXXXX")"
cleanup() {
  if [[ -n "${FAKE_PID:-}" ]]; then
    kill "$FAKE_PID" 2>/dev/null || true
    wait "$FAKE_PID" 2>/dev/null || true
  fi
  rm -rf "$TMP"
}
trap cleanup EXIT

mkdir -p "$TMP"
cat >"$TMP/fake_daemon.go" <<'EOF'
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
)

func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(os.Args[1], []byte(fmt.Sprintf("%d", port)), 0o644); err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"projects": []map[string]any{{
				"id": "smoke", "name": "Smoke", "kind": "git", "sessionPrefix": "smoke",
			}},
		})
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	_ = http.Serve(ln, mux)
}
EOF

FAKE_BIN="$TMP/fake-daemon"
FAKE_PORT_FILE="$TMP/port"
(cd "$TMP" && go build -o "$FAKE_BIN" ./fake_daemon.go)
"$FAKE_BIN" "$FAKE_PORT_FILE" &
FAKE_PID=$!

for _ in $(seq 1 50); do
  if [[ -s "$FAKE_PORT_FILE" ]]; then
    break
  fi
  sleep 0.05
done
FAKE_PORT="$(tr -d '[:space:]' <"$FAKE_PORT_FILE" 2>/dev/null || true)"
if [[ -z "$FAKE_PORT" ]]; then
  echo "fake daemon did not publish a port" >&2
  exit 1
fi

export AO_DATA_DIR="$TMP/data"
export AO_RUN_FILE="$TMP/running.json"
mkdir -p "$AO_DATA_DIR"
printf '{"pid":%s,"port":%s,"startedAt":"2026-01-01T00:00:00Z"}\n' "$FAKE_PID" "$FAKE_PORT" >"$AO_RUN_FILE"

AO_BIN="$TMP/ao"
(cd "$ROOT/backend" && go build -o "$AO_BIN" ./cmd/ao)

cat >"$TMP/go.mod" <<EOF
module ao-mcp-smoke

go 1.25.0

require github.com/modelcontextprotocol/go-sdk v1.8.0
EOF

cat >"$TMP/client.go" <<'EOF'
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "ao-mcp-smoke", Version: "v0"}, nil)
	cmd := exec.CommandContext(ctx, os.Args[1], "mcp")
	cmd.Env = os.Environ()
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list tools: %v\n", err)
		os.Exit(1)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{
		"list_projects", "list_sessions", "spawn_worker", "send_message",
		"read_session_output", "get_pr_status", "kill_session",
	} {
		if !names[want] {
			fmt.Fprintf(os.Stderr, "missing tool %q\n", want)
			os.Exit(1)
		}
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_projects",
		Arguments: map[string]any{},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "list_projects: %v\n", err)
		os.Exit(1)
	}
	if res.IsError {
		fmt.Fprintf(os.Stderr, "list_projects tool error\n")
		os.Exit(1)
	}
	var text string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if !strings.Contains(text, `"id": "smoke"`) {
		fmt.Fprintf(os.Stderr, "unexpected list_projects payload: %s\n", text)
		os.Exit(1)
	}
	fmt.Println("ao mcp smoke ok")
}
EOF

(cd "$TMP" && go mod tidy && go run ./client.go "$AO_BIN")
