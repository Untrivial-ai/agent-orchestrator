package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestACPDriverPromptResponseFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		category string
		title    string
		details  string
		actions  []any
		reauth   bool
	}{
		{"subscription", "access", "This account does not have access to Claude Code.", "Choose an eligible plan to continue.", nil, false},
		{"authentication", "access", "Your login has expired.", "Run /login to sign in again.", []any{"login"}, true},
		{"quota", "limit", "Usage limit reached", "Resets at 10:00 tomorrow.", nil, false},
		{"rate limit", "limit", "Too many requests", "Retry after 30 seconds.", []any{"retry"}, false},
		{"network", "connection", "Connection closed", "", []any{"new_session"}, false},
		{"unknown category", "future-category", "任意のエラー 👋", "line one\nline two\nhttps://example.com/help", []any{nil, map[string]any{"login": true}}, false},
		{"duplicate detail", "service", "Provider unavailable", "Provider unavailable", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := testPromptFailureMeta(map[string]any{
				"id": "incident-1", "revision": 1, "category": tc.category,
				"severity": "error", "title": tc.title, "details": tc.details, "actions": tc.actions,
			})
			meta[persistenthost.ACPEventIDMetaKey] = "terminal-event-1"
			agent := &fakeAgent{promptResponse: &acpsdk.PromptResponse{
				StopReason: acpsdk.StopReasonEndTurn, Meta: meta,
				Usage: &acpsdk.Usage{InputTokens: 12, OutputTokens: 3, TotalTokens: 15},
			}}
			driver := New(Config{
				Harness:      domain.HarnessClaudeCode,
				Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
				Launch:       func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
			}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			driver.useTestProcess(fakeSpawn(agent))
			opened, err := driver.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()
			_ = nextEvent(t, opened.Events())
			for attempt := 0; attempt < 2; attempt++ {
				ref, err := opened.SendTurn(context.Background(), ports.ChatUserMessage{Text: "hello"})
				if err != nil {
					t.Fatal(err)
				}
				if err := opened.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
					t.Fatal(err)
				}
				var usagesSeen, completionsSeen int
				for {
					event := nextEvent(t, opened.Events())
					switch event.Kind {
					case ports.ChatEventError:
						t.Fatalf("terminal failure emitted a second timeline event: %#v", event)
					case ports.ChatEventAccountChanged:
						t.Fatalf("terminal failure emitted a second account event: %#v", event)
					case ports.ChatEventUsage:
						usagesSeen++
						if event.Usage == nil || event.Usage.TotalTokens != 15 {
							t.Fatalf("usage = %#v", event)
						}
					case ports.ChatEventTurnCompleted:
						completionsSeen++
						want := domain.TurnStateFailed
						if attempt == 1 {
							want = domain.TurnStateCompleted
						}
						if event.TurnState != want || event.ProviderTurnID != ref.ProviderTurnID {
							t.Fatalf("completion = %#v", event)
						}
						if attempt == 0 && event.ProviderEventID != "terminal-event-1" {
							t.Fatalf("lost host event ID: %#v", event)
						}
						if attempt == 0 {
							var failure *ports.ChatProviderFailure
							if !errors.As(event.Err, &failure) {
								t.Fatalf("completion error = %#v", event.Err)
							}
							wantDetail := tc.details
							if wantDetail == tc.title {
								wantDetail = ""
							}
							if failure.Title != tc.title || failure.Detail != wantDetail {
								t.Fatalf("failure = %#v", failure)
							}
							wantRecovery := ports.ChatProviderRecovery("")
							if tc.reauth {
								wantRecovery = ports.ChatProviderRecoveryReauthenticate
							}
							if failure.Recovery != wantRecovery {
								t.Fatalf("recovery = %q; want %q", failure.Recovery, wantRecovery)
							}
						} else if event.Err != nil {
							t.Fatalf("successful completion retained prior failure: %#v", event.Err)
						}
					}
					if event.Kind == ports.ChatEventControllerState && event.ControllerState == ports.ChatControllerReady {
						break
					}
				}
				wantUsage := 1
				if attempt == 1 {
					wantUsage = 0
				}
				if usagesSeen != wantUsage || completionsSeen != 1 {
					t.Fatalf("events: usage=%d completions=%d", usagesSeen, completionsSeen)
				}
				// A successful follow-up must not inherit the preceding error.
				agent.mu.Lock()
				agent.promptResponse = &acpsdk.PromptResponse{StopReason: acpsdk.StopReasonEndTurn}
				agent.mu.Unlock()
			}
		})
	}
}

func testPromptFailureMeta(failure map[string]any) map[string]any {
	return map[string]any{"jetbrains": map[string]any{"air": map[string]any{
		"version": float64(1), "sessionFailure": failure,
	}}}
}

func TestPromptResponseFailureIgnoresNonErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		meta map[string]any
	}{
		{"absent", nil},
		{"wrong namespace type", map[string]any{"jetbrains": "error"}},
		{"missing version", map[string]any{"jetbrains": map[string]any{"air": map[string]any{"sessionFailure": map[string]any{"id": "1", "title": "error", "severity": "error"}}}}},
		{"warning", testPromptFailureMeta(map[string]any{"id": "1", "title": "Trying again", "severity": "warning"})},
		{"unknown severity", testPromptFailureMeta(map[string]any{"id": "1", "title": "Notice", "severity": "future"})},
		{"missing severity", testPromptFailureMeta(map[string]any{"id": "1", "title": "Notice"})},
		{"missing id", testPromptFailureMeta(map[string]any{"title": "Failure", "severity": "error"})},
		{"blank title", testPromptFailureMeta(map[string]any{"id": "1", "title": " \n ", "severity": "error"})},
		{"invalid title", testPromptFailureMeta(map[string]any{"id": "1", "title": []any{"error"}, "severity": "error"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if failure := promptResponseFailure(tc.meta); failure != nil {
				t.Fatalf("failure=%#v", failure)
			}
		})
	}
}

func TestACPReplayedPromptFailure(t *testing.T) {
	meta := testPromptFailureMeta(map[string]any{
		"id": "failure-1", "severity": "error", "title": "Provider rejected this request", "details": "Original provider details",
	})
	for _, cancelled := range []bool{false, true} {
		for _, replay := range []bool{false, true} {
			response := acpsdk.PromptResponse{StopReason: acpsdk.StopReasonEndTurn, Meta: meta}
			if cancelled {
				response.StopReason = acpsdk.StopReasonCancelled
			}
			conv := &conversation{
				activeTurn: "durable-turn", events: make(chan ports.ChatEvent, 16),
				log: slog.New(slog.NewTextHandler(io.Discard, nil)),
			}
			if replay {
				payload, err := json.Marshal(map[string]any{"eventId": "host:1", "result": response})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := conv.HandleExtensionMethod(context.Background(), persistenthost.ACPPromptResultMethod, payload); err != nil {
					t.Fatal(err)
				}
			} else {
				conv.finishPrompt("durable-turn", response, nil)
			}
			close(conv.events)
			var completions int
			for event := range conv.events {
				if event.Kind == ports.ChatEventError {
					t.Fatalf("terminal failure emitted a second timeline event: %#v", event)
				}
				if event.Kind == ports.ChatEventTurnCompleted {
					completions++
					want := domain.TurnStateFailed
					if cancelled {
						want = domain.TurnStateInterrupted
					}
					if event.TurnState != want {
						t.Fatalf("cancelled=%v replay=%v: completion=%#v", cancelled, replay, event)
					}
					if replay && event.ProviderEventID != "host:1" {
						t.Fatalf("lost replay identity: %#v", event)
					}
					if cancelled {
						if event.Err != nil {
							t.Fatalf("cancelled completion has error: %#v", event)
						}
					} else {
						var failure *ports.ChatProviderFailure
						if !errors.As(event.Err, &failure) || failure.Error() != "Provider rejected this request\n\nOriginal provider details" {
							t.Fatalf("lost failure detail: %#v", event)
						}
					}
				}
			}
			if completions != 1 {
				t.Fatalf("cancelled=%v replay=%v: completions=%d", cancelled, replay, completions)
			}
		}
	}
}
