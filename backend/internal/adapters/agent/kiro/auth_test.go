package kiro

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Fixtures follow upstream WhoamiArgs in amazon-q-developer-cli's
// crates/chat-cli/src/cli/user.rs. Unexpired local tokens skip validation.
func TestKiroWhoamiAuthStatusJSON(t *testing.T) {
	for _, tt := range []struct {
		name, out string
		err       error
		want      ports.AgentAuthStatus
	}{
		{"builder session", `{"accountType":"BuilderId","startUrl":null,"region":"us-east-1"}`, nil, ports.AgentAuthStatusConfigured},
		{"identity center session", `{"accountType":"IamIdentityCenter","startUrl":"https://example.awsapps.com/start","region":"us-east-1"}`, nil, ports.AgentAuthStatusConfigured},
		{"signed out", `{"account":null}`, nil, ports.AgentAuthStatusUnauthorized},
		{"signed out exit one", `{"account":null}`, errors.New("exit status 1"), ports.AgentAuthStatusUnauthorized},
		{"missing identity center url", `{"accountType":"IamIdentityCenter","region":"us-east-1"}`, nil, ports.AgentAuthStatusUnknown},
		{"unknown version", `{"accountType":"FutureProvider","region":"us-east-1"}`, nil, ports.AgentAuthStatusUnknown},
		{"empty object", `{}`, nil, ports.AgentAuthStatusUnknown},
		{"malformed negative", `{"message":"not logged in"`, nil, ports.AgentAuthStatusUnknown},
		{"nested negative", `{"diagnostic":{"account":null,"message":"not logged in"}}`, nil, ports.AgentAuthStatusUnknown},
		{"generic positive", `{"authenticated":true}`, nil, ports.AgentAuthStatusUnknown},
		{"generic negative", `{"authenticated":false}`, nil, ports.AgentAuthStatusUnknown},
		{"null body", `null`, nil, ports.AgentAuthStatusUnknown},
		{"wrong account type", `{"accountType":false}`, nil, ports.AgentAuthStatusUnknown},
		{"contradictory account", `{"account":null,"accountType":"BuilderId","region":"us-east-1"}`, nil, ports.AgentAuthStatusUnknown},
		{"execution error", `{"accountType":"BuilderId","startUrl":null,"region":"us-east-1"}`, errors.New("secret failure"), ports.AgentAuthStatusUnknown},
		{"timeout", `{"account":null}`, context.DeadlineExceeded, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stubKiroAuthCommand(t, []byte(tt.out), tt.err)
			got, err := kiroWhoamiAuthStatus(context.Background(), "kiro-cli")
			if err != nil || got != tt.want {
				t.Fatalf("status = %q, err = %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestKiroAuthStatusForScope(t *testing.T) {
	for _, tt := range []struct {
		name, inherited string
		check           ports.AgentAuthCheck
		out             string
		want            ports.AgentAuthStatus
	}{
		{"headless key", "ksk_test_key", ports.AgentAuthCheck{Interactive: false}, `{"account":null}`, ports.AgentAuthStatusConfigured},
		{"interactive ignores key", "ksk_test_key", ports.AgentAuthCheck{Interactive: true}, `{"account":null}`, ports.AgentAuthStatusUnauthorized},
		{"scoped headless key", "", ports.AgentAuthCheck{Env: map[string]string{"KIRO_API_KEY": "ksk_scoped_key"}}, `{"account":null}`, ports.AgentAuthStatusConfigured},
		{"cleared inherited key", "ksk_test_key", ports.AgentAuthCheck{Env: map[string]string{"KIRO_API_KEY": ""}}, `{"account":null}`, ports.AgentAuthStatusUnauthorized},
		{"unknown with key", "ksk_test_key", ports.AgentAuthCheck{}, `{}`, ports.AgentAuthStatusConfigured},
		{"browser before key", "ksk_test_key", ports.AgentAuthCheck{}, `{"accountType":"BuilderId","startUrl":null,"region":"us-east-1"}`, ports.AgentAuthStatusConfigured},
		{"browser before malformed key", "key", ports.AgentAuthCheck{}, `{"accountType":"BuilderId","startUrl":null,"region":"us-east-1"}`, ports.AgentAuthStatusConfigured},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("KIRO_API_KEY", tt.inherited)
			calls := stubKiroAuthCommand(t, []byte(tt.out), nil)
			checker, ok := any(&Plugin{resolvedBinary: "kiro-cli"}).(ports.AgentScopedAuthChecker)
			if !ok {
				t.Fatal("Kiro does not check scoped credentials")
			}
			got, err := checker.AuthStatusFor(context.Background(), tt.check)
			if err != nil || got != tt.want {
				t.Fatalf("status = %q, err = %v; want %q", got, err, tt.want)
			}
			if *calls != 1 {
				t.Fatalf("whoami calls = %d, want 1 before key fallback", *calls)
			}
		})
	}
}

func TestKiroAuthStatusForMalformedAPIKey(t *testing.T) {
	for _, tt := range []struct{ name, key string }{
		{"wrong prefix", "key"},
		{"empty suffix", "ksk_"},
		{"whitespace suffix", "ksk_ "},
		{"internal whitespace", "ksk_bad key"},
		{"surrounding whitespace", " ksk_test_key "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("KIRO_API_KEY", tt.key)
			stubKiroAuthCommand(t, []byte(`{"account":null}`), nil)
			p := &Plugin{resolvedBinary: "kiro-cli"}
			got, err := p.AuthStatusFor(context.Background(), ports.AgentAuthCheck{})
			if err != nil || got != ports.AgentAuthStatusUnknown {
				t.Fatalf("status = %q, err = %v; want unknown", got, err)
			}
		})
	}
	t.Run("scoped malformed key overrides valid inherited key", func(t *testing.T) {
		t.Setenv("KIRO_API_KEY", "ksk_test_key")
		stubKiroAuthCommand(t, []byte(`{}`), nil)
		got, err := (&Plugin{resolvedBinary: "kiro-cli"}).AuthStatusFor(context.Background(), ports.AgentAuthCheck{Env: map[string]string{"KIRO_API_KEY": "scope-key"}})
		if err != nil || got != ports.AgentAuthStatusUnknown {
			t.Fatalf("status = %q, err = %v; want unknown", got, err)
		}
	})
}

func TestKiroAuthStatusGlobalIgnoresHeadlessKey(t *testing.T) {
	t.Setenv("KIRO_API_KEY", "ksk_test_key")
	calls := stubKiroAuthCommand(t, []byte(`{"account":null}`), nil)
	got, err := (&Plugin{resolvedBinary: "kiro-cli"}).AuthStatus(context.Background())
	if err != nil || got != ports.AgentAuthStatusUnauthorized || *calls != 1 {
		t.Fatalf("status = %q, err = %v, calls = %d", got, err, *calls)
	}
}

func TestKiroAuthStatusCanceled(t *testing.T) {
	t.Setenv("KIRO_API_KEY", "ksk_test_key")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := (&Plugin{resolvedBinary: "kiro-cli"}).AuthStatus(ctx)
	if got != ports.AgentAuthStatusUnknown || !errors.Is(err, context.Canceled) {
		t.Fatalf("status = %q, err = %v", got, err)
	}
}

func stubKiroAuthCommand(t *testing.T, out []byte, err error) *int {
	t.Helper()
	previous := authprobe.CmdRunner
	calls := 0
	authprobe.CmdRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls++
		if name != "kiro-cli" || !reflect.DeepEqual(args, []string{"whoami", "--format", "json"}) {
			t.Fatalf("unexpected command: %s %#v", name, args)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("whoami probe has no deadline")
		}
		return out, err
	}
	t.Cleanup(func() { authprobe.CmdRunner = previous })
	return &calls
}
