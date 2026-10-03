package deepseekharnessacp

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"testing"
	"time"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestLiveDeepSeekACPModelCatalog drives the installed DeepSeek Harness through
// AO's own ACP client and asserts the contract this binding depends on: the
// session advertises a model select plus the reasoning_effort select, and every
// model it offers carries the opaque JSON-array value AO passes back.
//
// It is opt-in because it needs a real `dsh` install with a working model route:
//
//	AO_LIVE_DEEPSEEK=1 go test ./internal/adapters/chatdriver/deepseekacp/ -run Live -v
func TestLiveDeepSeekACPModelCatalog(t *testing.T) {
	if os.Getenv("AO_LIVE_DEEPSEEK") != "1" {
		t.Skip("set AO_LIVE_DEEPSEEK=1 to test the installed DeepSeek Harness")
	}
	binary, err := exec.LookPath("dsh")
	if err != nil {
		t.Fatalf("resolve DeepSeek Harness: %v", err)
	}
	workdir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	options, err := acpdriver.DiscoverConfigOptions(ctx, acpdriver.Launch{
		Command: binary,
		Args:    []string{"--profile", acpProfile},
	}, workdir, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		t.Fatalf("discover DeepSeek Harness ACP options: %v", err)
	}

	model := findOption(options, "model")
	if model == nil {
		t.Fatalf("session advertises no model option: %#v", options)
	}
	if model.Type != ports.ChatConfigOptionSelect {
		t.Fatalf("model option type = %q, want select", model.Type)
	}
	if len(model.Choices) == 0 {
		t.Fatal("model option offers no choices")
	}
	if model.Current.Select == "" {
		t.Fatal("model option has no current value")
	}
	for _, choice := range model.Choices {
		// AO stores the advertised value verbatim and sends it back, so the
		// JSON-array shape is a contract, not a formatting preference.
		if len(choice.Value) == 0 || choice.Value[0] != '[' || choice.Value[len(choice.Value)-1] != ']' {
			t.Fatalf("model choice %q is not a JSON array value", choice.Value)
		}
	}

	effort := findOption(options, "reasoning_effort")
	if effort == nil {
		t.Fatalf("session advertises no reasoning_effort option: %#v", options)
	}
	// The driver maps AO's vocabulary onto exactly these levels.
	offered := map[string]bool{}
	for _, choice := range effort.Choices {
		offered[choice.Value] = true
	}
	for _, want := range []string{"off", "low", "high", "max"} {
		if !offered[want] {
			t.Fatalf("reasoning_effort does not offer %q: %#v", want, effort.Choices)
		}
	}
}

func findOption(options []ports.ChatConfigOption, id string) *ports.ChatConfigOption {
	for i := range options {
		if options[i].ID == id {
			return &options[i]
		}
	}
	return nil
}
