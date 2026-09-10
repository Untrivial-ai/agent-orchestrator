package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver/codexproto"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAccountFactoryUsesManagedHomeAndFileCredentialStore(t *testing.T) {
	serverReads, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	var gotArgs []string
	var gotEnv []string
	factory := NewAccountFactory(fakePlugin{bin: "codex"}, slog.New(slog.DiscardHandler))
	factory.spawn = func(_ context.Context, _, _ string, env, args []string) (*process, error) {
		gotArgs, gotEnv = append([]string(nil), args...), append([]string(nil), env...)
		return &process{stdin: clientWrites, stdout: clientReads, stop: func() error { return serverWrites.Close() }}, nil
	}
	go serveAccountTestProtocol(serverReads, serverWrites, map[string]any{
		"initialize":   map[string]any{},
		"account/read": map[string]any{"account": map[string]any{"type": "chatgpt", "email": "person@example.com", "planType": "pro"}, "requiresOpenaiAuth": true},
	})
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	client, err := factory.Open(context.Background(), ports.CodexAccountContext{Home: home, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	account, err := client.Read(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotArgs, []string{"-c", `cli_auth_credentials_store="file"`, "app-server"}) {
		t.Fatalf("args = %#v", gotArgs)
	}
	if !slices.ContainsFunc(gotEnv, func(value string) bool {
		return len(value) > len("CODEX_HOME=") && value[:len("CODEX_HOME=")] == "CODEX_HOME="
	}) {
		t.Fatalf("env does not contain CODEX_HOME: %#v", gotEnv)
	}
	if account.Authentication != domain.AgentAuthenticationAuthorized || account.Method != domain.CodexAuthMethodChatGPT || account.Email == nil || *account.Email != "person@example.com" {
		t.Fatalf("account = %#v", account)
	}
}

func TestAccountReadMapsExplicitSignedOutAndNotApplicable(t *testing.T) {
	for _, test := range []struct {
		name     string
		requires bool
		want     domain.AgentAuthenticationState
	}{
		{"signed out", true, domain.AgentAuthenticationUnauthorized},
		{"not applicable", false, domain.AgentAuthenticationNotApplicable},
	} {
		t.Run(test.name, func(t *testing.T) {
			serverReads, clientWrites := io.Pipe()
			clientReads, serverWrites := io.Pipe()
			factory := NewAccountFactory(fakePlugin{bin: "codex"}, nil)
			factory.spawn = func(context.Context, string, string, []string, []string) (*process, error) {
				return &process{stdin: clientWrites, stdout: clientReads, stop: func() error { return serverWrites.Close() }}, nil
			}
			go serveAccountTestProtocol(serverReads, serverWrites, map[string]any{"initialize": map[string]any{}, "account/read": map[string]any{"requiresOpenaiAuth": test.requires}})
			client, err := factory.Open(context.Background(), ports.CodexAccountContext{Home: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			account, err := client.Read(context.Background(), false)
			if err != nil {
				t.Fatal(err)
			}
			if account.Authentication != test.want {
				t.Fatalf("authentication = %q, want %q", account.Authentication, test.want)
			}
		})
	}
}

func TestAccountFactoryDeviceDetectionDoesNotOverrideCredentialStore(t *testing.T) {
	serverReads, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	var gotArgs, gotEnv []string
	home := t.TempDir()
	factory := NewAccountFactory(fakePlugin{bin: "codex"}, nil)
	factory.spawn = func(_ context.Context, _, workdir string, env, args []string) (*process, error) {
		if workdir != home {
			t.Fatalf("workdir = %q, want %q", workdir, home)
		}
		gotArgs, gotEnv = append([]string(nil), args...), append([]string(nil), env...)
		return &process{stdin: clientWrites, stdout: clientReads, stop: func() error { return serverWrites.Close() }}, nil
	}
	go serveAccountTestProtocol(serverReads, serverWrites, map[string]any{"initialize": map[string]any{}})
	client, err := factory.Open(context.Background(), ports.CodexAccountContext{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if !slices.Equal(gotArgs, []string{"app-server"}) {
		t.Fatalf("args = %#v", gotArgs)
	}
	if slices.ContainsFunc(gotArgs, func(value string) bool { return value == `cli_auth_credentials_store="file"` }) {
		t.Fatalf("device credential store was overridden: %#v", gotArgs)
	}
	if !slices.Contains(gotEnv, "CODEX_HOME="+home) {
		t.Fatalf("env does not explicitly select existing home: %#v", gotEnv)
	}
}

func TestAccountClientReadsCapacityAndNormalizesResetCredits(t *testing.T) {
	serverReads, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	factory := NewAccountFactory(fakePlugin{bin: "codex"}, nil)
	factory.spawn = func(context.Context, string, string, []string, []string) (*process, error) {
		return &process{stdin: clientWrites, stdout: clientReads, stop: func() error { return serverWrites.Close() }}, nil
	}
	go serveAccountTestProtocol(serverReads, serverWrites, map[string]any{
		"initialize": map[string]any{},
		"account/rateLimits/read": map[string]any{
			"rateLimits":            map[string]any{"limitId": "codex", "planType": "pro", "primary": map[string]any{"usedPercent": 81}},
			"rateLimitsByLimitId":   map[string]any{"spark": map[string]any{"limitId": "spark", "primary": map[string]any{"usedPercent": 20}}},
			"rateLimitResetCredits": map[string]any{"availableCount": 2, "credits": []map[string]any{{"id": "opaque", "grantedAt": 1, "expiresAt": 4102444800, "resetType": "codexRateLimits", "status": "available"}}},
		},
	})
	client, err := factory.Open(context.Background(), ports.CodexAccountContext{Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	capacity, err := client.ReadCapacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capacity.Plan == nil || *capacity.Plan != "pro" || capacity.Overall == nil || capacity.Overall.Primary == nil || capacity.Overall.Primary.UsedPercent != 81 {
		t.Fatalf("capacity = %#v", capacity)
	}
	if len(capacity.AdditionalBuckets) != 1 || capacity.AdditionalBuckets[0].LimitID != "spark" {
		t.Fatalf("additional buckets = %#v", capacity.AdditionalBuckets)
	}
	if capacity.ResetCredits == nil || capacity.ResetCredits.AvailableCount != 2 || capacity.ResetCredits.NearestExpiresAt == nil {
		t.Fatalf("reset credits = %#v", capacity.ResetCredits)
	}
}

func TestAccountClientConsumesResetCreditIdempotently(t *testing.T) {
	serverReads, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	factory := NewAccountFactory(fakePlugin{bin: "codex"}, nil)
	factory.spawn = func(context.Context, string, string, []string, []string) (*process, error) {
		return &process{stdin: clientWrites, stdout: clientReads, stop: func() error { return serverWrites.Close() }}, nil
	}
	go serveAccountTestProtocol(serverReads, serverWrites, map[string]any{
		"initialize": map[string]any{},
		codexproto.MethodAccountRateLimitResetCreditConsume: map[string]any{"outcome": "reset"},
	})
	client, err := factory.Open(context.Background(), ports.CodexAccountContext{Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	outcome, err := client.ConsumeResetCredit(context.Background(), "reset-request-1")
	if err != nil || outcome != domain.CodexResetCreditReset {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
}

func TestInspectCodexSchemaDirectoryMapsSupportedUnsupportedAndUnreadable(t *testing.T) {
	dir := t.TempDir()
	allMethods := []string{
		codexproto.MethodAccountRead,
		codexproto.MethodAccountRateLimitsRead,
		codexproto.MethodAccountUsageRead,
		codexproto.MethodAccountRateLimitResetCreditConsume,
		codexproto.MethodThreadResume,
		codexproto.MethodAccountUpdated,
	}
	data, err := json.Marshal(map[string]any{"methods": allMethods})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	capabilities := inspectCodexSchemaDirectory(dir)
	if capabilities.AccountRead.State != domain.CodexCapabilitySupported || capabilities.CapacityRead.State != domain.CodexCapabilitySupported || capabilities.UsageRead.State != domain.CodexCapabilitySupported || capabilities.ResetCreditConsume.State != domain.CodexCapabilitySupported || capabilities.ThreadResume.State != domain.CodexCapabilitySupported {
		t.Fatalf("supported capabilities = %#v", capabilities)
	}
	if err := os.WriteFile(path, []byte(`{"methods":["account/read"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	capabilities = inspectCodexSchemaDirectory(dir)
	if capabilities.AccountRead.State != domain.CodexCapabilitySupported || capabilities.CapacityRead.State != domain.CodexCapabilityUnsupported || capabilities.UsageRead.State != domain.CodexCapabilityUnsupported || capabilities.ResetCreditConsume.State != domain.CodexCapabilityUnsupported || capabilities.ThreadResume.State != domain.CodexCapabilityUnsupported {
		t.Fatalf("read-only capabilities = %#v", capabilities)
	}
	if err := os.WriteFile(path, []byte(`not-json "account/read"`), 0o600); err != nil {
		t.Fatal(err)
	}
	capabilities = inspectCodexSchemaDirectory(dir)
	if capabilities.AccountRead.State != domain.CodexCapabilityUnknown || capabilities.CapacityRead.State != domain.CodexCapabilityUnknown || capabilities.UsageRead.State != domain.CodexCapabilityUnknown || capabilities.ResetCreditConsume.State != domain.CodexCapabilityUnknown || capabilities.ThreadResume.State != domain.CodexCapabilityUnknown {
		t.Fatalf("unreadable capabilities = %#v", capabilities)
	}
}

func TestSafeCodexAccountEmailRejectsControlCharacters(t *testing.T) {
	unsafe := "person@example.com\nsecret"
	if safeCodexAccountEmail(&unsafe) != nil {
		t.Fatal("unsafe account email was retained")
	}
	safe := " person@example.com "
	got := safeCodexAccountEmail(&safe)
	if got == nil || *got != "person@example.com" {
		t.Fatalf("safe email = %#v", got)
	}
}

func serveAccountTestProtocol(reader io.Reader, writer io.Writer, responses map[string]any) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || len(request.ID) == 0 {
			continue
		}
		result, ok := responses[request.Method]
		if !ok {
			result = map[string]any{}
		}
		response, _ := json.Marshal(map[string]any{"id": request.ID, "result": result})
		_, _ = writer.Write(append(response, '\n'))
	}
}
