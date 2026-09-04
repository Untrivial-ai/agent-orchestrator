package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

// A route the daemon blocks on its LAN listener must not reach the operator as
// ROUTE_NOT_FOUND. That code reads as "that daemon is too old / that endpoint
// does not exist" and sends whoever hits it off to audit daemon builds, when
// the truth is that the block is a deliberate policy.
//
// The stand-in remote is the REAL LAN listener (httpd.NewMobileLAN), not a
// hand-written envelope, so this pins what the CLI actually renders for what
// the daemon actually sends — the two cannot drift apart. The inner handler
// records every request it sees: it must record none, since the block sits
// outside both auth and the router.
func TestRemoteRendersLoopbackOnlyBlockNotMissingRoute(t *testing.T) {
	setBrowserIdentity(t)
	aoHome(t)
	t.Setenv("AO_TOKEN", "s3cret12")

	var reached []string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"sessionId":"ao-1","connected":true,"transport":"electron-webcontents-debugger"}`)
	})
	lan := httpd.NewMobileLAN(inner, 0, slog.Default(), nil)
	lan.SetPasswordHash(mobilebridge.HashPassword("s3cret12"))
	port, err := lan.Start(0)
	if err != nil {
		t.Fatalf("start LAN listener: %v", err)
	}
	defer lan.Stop(context.Background())

	var out bytes.Buffer
	err = executeWithDeps(
		Deps{Out: &out, Err: &out, ProcessAlive: func(int) bool { return true }},
		[]string{"browser", "status", "--url", fmt.Sprintf("http://127.0.0.1:%d", port)},
	)
	if err == nil {
		t.Fatalf("a LAN-blocked route succeeded: %s", out.String())
	}
	msg := err.Error()
	for _, want := range []string{"ROUTE_LOOPBACK_ONLY", "loopback listener only"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not read as a policy block (missing %q): %v", want, err)
		}
	}
	for _, unwanted := range []string{"ROUTE_NOT_FOUND", "has no handler"} {
		if strings.Contains(msg, unwanted) {
			t.Errorf("error still reads as a missing route (%q): %v", unwanted, err)
		}
	}
	if len(reached) != 0 {
		t.Fatalf("blocked request reached the router: %v", reached)
	}
}
