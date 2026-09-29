package mimocode

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestProviderListCredentialEvidenceIsConfiguredNotAuthorized(t *testing.T) {
	for _, output := range []string{
		"Credentials ~/.local/share/mimocode/auth.json\n1 credential\n",
		"Environment\nAnthropic ANTHROPIC_API_KEY\n1 environment variable\n",
	} {
		status, known := providerListStatus(output)
		if !known || status != ports.AgentAuthStatusConfigured {
			t.Fatalf("providerListStatus(%q) = (%q, %v), want configured", output, status, known)
		}
	}
}

func TestProviderListEmptyAndMalformedAreNotAuthorized(t *testing.T) {
	for _, tc := range []struct {
		output string
		known  bool
	}{
		{output: "0 credentials\n0 environment variables\n", known: true},
		{output: "unexpected output", known: false},
	} {
		status, known := providerListStatus(tc.output)
		if known != tc.known || status != ports.AgentAuthStatusUnknown {
			t.Fatalf("providerListStatus(%q) = (%q, %v)", tc.output, status, known)
		}
	}
}

func TestAuthStatusUsesDocumentedProviderListCommand(t *testing.T) {
	old := runAuthProbe
	t.Cleanup(func() { runAuthProbe = old })
	runAuthProbe = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary != "mimo" || !reflect.DeepEqual(args, []string{"auth", "list"}) {
			t.Fatalf("probe = %q %q", binary, args)
		}
		return []byte("1 credential\n"), nil
	}
	status, err := (&Plugin{resolvedBinary: "mimo"}).AuthStatus(context.Background())
	if err != nil || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("AuthStatus = (%q, %v), want configured", status, err)
	}
}

func TestAuthStatusRecognizesFreeMiMoAutoWithoutCredentials(t *testing.T) {
	old := runAuthProbe
	t.Cleanup(func() { runAuthProbe = old })
	runAuthProbe = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary != "mimo" {
			t.Fatalf("probe binary = %q", binary)
		}
		switch {
		case reflect.DeepEqual(args, []string{"auth", "list"}):
			return []byte("0 credentials\n0 environment variables\n"), nil
		case reflect.DeepEqual(args, []string{"models", "mimo"}):
			return []byte("mimo/mimo-auto — window 1M, compacts at 900K\n"), nil
		default:
			t.Fatalf("unexpected probe args = %q", args)
			return nil, nil
		}
	}
	status, err := (&Plugin{resolvedBinary: "mimo"}).AuthStatus(context.Background())
	if err != nil || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("AuthStatus = (%q, %v), want configured", status, err)
	}
}

func TestFreeModelListedRequiresExactModelID(t *testing.T) {
	for _, output := range []string{
		"xiaomi/mimo-v2.5-pro — window 1M\n",
		"mimo/mimo-auto-premium — window 1M\n",
		"no models available\n",
	} {
		if freeModelListed(output) {
			t.Fatalf("freeModelListed(%q) = true", output)
		}
	}
}

func TestAuthStatusPropagatesCallerCancellation(t *testing.T) {
	old := runAuthProbe
	t.Cleanup(func() { runAuthProbe = old })
	runAuthProbe = func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status, err := (&Plugin{resolvedBinary: "mimo"}).AuthStatus(ctx)
	if status != ports.AgentAuthStatusUnknown || !errors.Is(err, context.Canceled) {
		t.Fatalf("AuthStatus = (%q, %v), want unknown/context canceled", status, err)
	}
}
