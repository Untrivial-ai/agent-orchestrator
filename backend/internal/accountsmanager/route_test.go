package accountsmanager

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coresession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestRouteCapabilityAuthenticatesOnlyItsMintedToken(t *testing.T) {
	capability, err := newRouteCapability(make([]byte, 32))
	if err != nil {
		t.Fatalf("newRouteCapability: %v", err)
	}
	token, err := capability.Mint("session-1")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	secondToken, err := capability.Mint("session-1")
	if err != nil {
		t.Fatalf("second Mint: %v", err)
	}
	if secondToken != token {
		t.Fatalf("route capability changed for the same session: first=%q second=%q", token, secondToken)
	}

	req := httptestRequestWithToken(token)
	result, authErr := capability.Authenticate(context.Background(), req)
	if authErr != nil {
		t.Fatalf("Authenticate returned error: %v", authErr)
	}
	if result == nil || result.Principal == "" {
		t.Fatal("Authenticate did not return a principal")
	}
	scope := coresession.CallerScope(result.Principal)
	if got, ok := capability.sessionForScope(scope); !ok || got != "session-1" {
		t.Fatalf("sessionForScope = (%q, %t), want (session-1, true)", got, ok)
	}

	tampered := token[:len(token)-1] + "x"
	_, authErr = capability.Authenticate(context.Background(), httptestRequestWithToken(tampered))
	if authErr == nil {
		t.Fatal("tampered route token was accepted")
	}
}

func TestExactRouteSelectorFollowsMutableSessionPin(t *testing.T) {
	routes, err := newRouteState(t.TempDir() + "/routes.json")
	if err != nil {
		t.Fatalf("newRouteState: %v", err)
	}
	if err := routes.setAccountForSession("session-1", "account-a"); err != nil {
		t.Fatalf("setAccountForSession: %v", err)
	}
	capability, err := newRouteCapability(make([]byte, 32))
	if err != nil {
		t.Fatalf("newRouteCapability: %v", err)
	}
	token, err := capability.Mint("session-1")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	result, authErr := capability.Authenticate(context.Background(), httptestRequestWithToken(token))
	if authErr != nil {
		t.Fatalf("Authenticate returned error: %v", authErr)
	}
	scope := coresession.CallerScope(result.Principal)
	opts := coreexecutor.Options{Metadata: map[string]any{coreexecutor.CallerScopeMetadataKey: scope}}
	auths := []*coreauth.Auth{
		{ID: "account-a", Provider: "codex", Status: coreauth.StatusActive},
		{ID: "account-b", Provider: "codex", Status: coreauth.StatusActive},
	}
	selector := newExactRouteSelector(capability, routes, nil)
	selected, err := selector.Pick(context.Background(), "mixed", "gpt-5", opts, auths)
	if err != nil || selected == nil || selected.ID != "account-a" {
		t.Fatalf("mixed-provider Pick = (%v, %v), want account-a", selected, err)
	}
	if err := routes.setAccountForSession("session-1", "account-b"); err != nil {
		t.Fatalf("switch route pin: %v", err)
	}
	selected, err = selector.Pick(context.Background(), "codex", "gpt-5", opts, auths)
	if err != nil || selected == nil || selected.ID != "account-b" {
		t.Fatalf("switched Pick = (%v, %v), want account-b", selected, err)
	}
	auths[1].Unavailable = true
	if _, err := selector.Pick(context.Background(), "codex", "gpt-5", opts, auths); err == nil || !strings.Contains(err.Error(), ports.ErrCodexProxyAccountUnavailable.Error()) {
		t.Fatalf("unavailable pinned account error = %v, want account unavailable", err)
	}
	if _, err := selector.Pick(context.Background(), "claude", "claude-3", opts, auths); err == nil {
		t.Fatal("route capability was allowed to select a non-Codex provider")
	}
}

func TestExactRouteAuthUsableRejectsCooldown(t *testing.T) {
	auth := &coreauth.Auth{Provider: "codex", Status: coreauth.StatusActive, NextRetryAfter: time.Now().Add(time.Minute)}
	if exactRouteAuthUsable(auth, "", time.Now()) {
		t.Fatal("auth in cooldown was considered usable")
	}
}

func httptestRequestWithToken(token string) *http.Request {
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}
