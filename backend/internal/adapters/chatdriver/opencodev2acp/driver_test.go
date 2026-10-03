package opencodev2acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencode"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencodev2"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/opencodeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestMain(m *testing.M) {
	if len(os.Args) >= 7 && os.Args[1] == "chat-host" {
		protocol := persistenthost.ProtocolRaw
		fingerprint := ""
		separator := 5
		if os.Args[5] == string(persistenthost.ProtocolACP) {
			protocol = persistenthost.ProtocolACP
			fingerprint = os.Args[6]
			separator = 7
		}
		if len(os.Args) <= separator || os.Args[separator] != "--" {
			os.Exit(2)
		}
		err := persistenthost.Run(context.Background(), persistenthost.Config{
			SessionID: os.Args[2], DataDir: os.Args[3], Workdir: os.Args[4],
			Env: os.Environ(), Argv: os.Args[separator+1:], Protocol: protocol,
			OwnershipFingerprint: fingerprint,
		})
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestOpenCodeV2ACPProviderHelper(t *testing.T) {
	if os.Getenv("AO_TEST_OPENCODE_V2_ACP_PROVIDER") != "1" {
		return
	}
	for scanner := bufio.NewScanner(os.Stdin); scanner.Scan(); {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ModeID    string `json:"modeId"`
				SessionID string `json:"sessionId"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || len(request.ID) == 0 {
			continue
		}
		record := os.Getenv("AO_TEST_OPENCODE_V2_ACP_CALLS")
		if record != "" {
			f, err := os.OpenFile(record, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err == nil {
				detail := request.Params.ModeID
				if detail == "" {
					detail = request.Params.SessionID
				}
				_, _ = fmt.Fprintf(f, "%s:%s\n", request.Method, detail)
				_ = f.Close()
			}
		}
		switch request.Method {
		case "initialize":
			_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"resume":{}}},"authMethods":[],"agentInfo":{"name":"OpenCode","version":"2.0.0"}}}`+"\n", request.ID)
		case "session/new":
			_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"opencode-v2-provider","configOptions":[{"id":"model","name":"Model","category":"model","type":"select","currentValue":"provider/default","options":[{"value":"provider/default","name":"Default"}]},{"id":"effort","name":"Effort","category":"thought_level","type":"select","currentValue":"default","options":[{"value":"default","name":"Default"}]},{"id":"mode","name":"Session Mode","category":"mode","type":"select","currentValue":"build","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"}]}]}}`+"\n", request.ID)
		case "session/set_mode":
			if request.Params.ModeID != "build" && request.Params.ModeID != "plan" {
				_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"Invalid params: mode not found: %s"}}`+"\n", request.ID, request.Params.ModeID)
				continue
			}
			_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{}}`+"\n", request.ID)
		default:
			_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{}}`+"\n", request.ID)
		}
	}
	os.Exit(0)
}

func writeOpenCodeACPExecutable(t *testing.T, version string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "opencode")
	calls := filepath.Join(dir, "calls.log")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  printf '%s\n' "$AO_TEST_OPENCODE_VERSION"
  exit 0
fi
printf 'argv:%s\n' "$*" >> "$AO_TEST_OPENCODE_V2_ACP_CALLS"
printf 'config:%s\n' "$OPENCODE_CONFIG_CONTENT" >> "$AO_TEST_OPENCODE_V2_ACP_CALLS"
exec "$AO_TEST_OPENCODE_V2_ACP_BINARY" -test.run=TestOpenCodeV2ACPProviderHelper
`
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("AO_TEST_OPENCODE_VERSION", version)
	t.Setenv("AO_TEST_OPENCODE_V2_ACP_BINARY", os.Args[0])
	t.Setenv("AO_TEST_OPENCODE_V2_ACP_PROVIDER", "1")
	t.Setenv("AO_TEST_OPENCODE_V2_ACP_CALLS", calls)
	return binary, calls
}

func TestLaunchesOpenCodeACPWithTheBuiltInModeAdvertisedAtStartup(t *testing.T) {
	binary, calls := writeOpenCodeACPExecutable(t, "2.0.0")
	driver := New(opencodev2.New(), nil)
	if driver.Harness() != domain.HarnessOpenCodeV2 {
		t.Fatalf("harness = %q", driver.Harness())
	}
	conversation, err := driver.Start(context.Background(), ports.ChatStartConfig{
		SessionID: "v2-acp", DataDir: t.TempDir(), WorkspacePath: t.TempDir(),
		SystemPrompt: "Follow AO rules.", Permissions: ports.PermissionModeAuto,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = conversation.(ports.ChatProviderTerminator).Terminate() }()

	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "argv:acp\n") {
		t.Fatalf("v2 launch did not execute %s acp:\n%s", binary, data)
	}
	if !strings.Contains(string(data), `"agents"`) || !strings.Contains(string(data), `"default_agent":"build"`) {
		t.Fatalf("v2 launch config missing the configured build agent:\n%s", data)
	}
	if !strings.Contains(string(data), "session/set_mode:build\n") {
		t.Fatalf("v2 approval mode was not applied through ACP:\n%s", data)
	}
}

func TestWrongOpenCodeMajorIsRejectedBeforeACPProcessLaunch(t *testing.T) {
	for _, test := range []struct {
		name, version, want string
		driver              func() ports.ChatDriver
	}{
		{name: "v2 driver with v1 binary", version: "1.18.33", want: "requires OpenCode 2", driver: func() ports.ChatDriver { return New(opencodev2.New(), nil) }},
		{name: "v1 driver with v2 binary", version: "2.0.0", want: "requires OpenCode 1", driver: func() ports.ChatDriver { return opencodeacp.New(opencode.New(), nil) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, calls := writeOpenCodeACPExecutable(t, test.version)
			_, err := test.driver().Start(context.Background(), ports.ChatStartConfig{
				SessionID: "wrong-major", DataDir: t.TempDir(), WorkspacePath: t.TempDir(),
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Start error = %v, want %q", err, test.want)
			}
			if data, readErr := os.ReadFile(calls); readErr == nil && len(data) != 0 {
				t.Fatalf("wrong-major binary launched ACP process: %s", data)
			} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				t.Fatal(readErr)
			}
		})
	}
}

func TestRejectsProviderNameBeforeResolvingV2Binary(t *testing.T) {
	plugin := &unresolvedPlugin{}
	driver := New(plugin, nil)
	_, err := driver.Start(context.Background(), ports.ChatStartConfig{
		WorkspacePath: t.TempDir(), Model: "TensorMux",
	})
	if !errors.Is(err, ports.ErrChatConfigOptionInvalid) || !strings.Contains(err.Error(), "provider/model") {
		t.Fatalf("error = %v, want provider/model validation", err)
	}
	if plugin.resolved {
		t.Fatal("invalid model reached OpenCode 2 binary resolution")
	}
}

type unresolvedPlugin struct{ resolved bool }

func (p *unresolvedPlugin) ResolveBinary(context.Context) (string, error) {
	p.resolved = true
	return "", errors.New("unexpected binary resolution")
}
func (*unresolvedPlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return ports.AgentAuthStatusUnknown, nil
}

func TestModelAndEffortOptionsKeepProviderOrder(t *testing.T) {
	if got := sessionOptions(ports.ChatTurnSettings{}); got != nil {
		t.Fatalf("empty options = %#v", got)
	}
	want := []acpdriver.SessionOption{{ID: "model", Value: "provider/model"}, {ID: "effort", Value: "max"}}
	if got := sessionOptions(ports.ChatTurnSettings{Model: "provider/model", Effort: "max"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %#v, want %#v", got, want)
	}
	if err := validateTurnSettings(ports.PermissionModeDefault, ports.ChatTurnSettings{Model: "provider/model"}); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"provider", "/model", "provider/", " provider/model"} {
		if err := validateTurnSettings(ports.PermissionModeDefault, ports.ChatTurnSettings{Model: model}); !errors.Is(err, ports.ErrChatConfigOptionInvalid) {
			t.Fatalf("model %q error = %v", model, err)
		}
	}
}

func TestApprovalModesSelectV2AgentsAndPermissionReplies(t *testing.T) {
	for _, test := range []struct {
		mode ports.PermissionMode
		want string
	}{
		{ports.PermissionModeDefault, "build"},
		{ports.PermissionModeAcceptEdits, "build"},
		{ports.PermissionModeAuto, "build"},
		{ports.PermissionModeBypassPermissions, "build"},
	} {
		if got := sessionMode(test.mode); got != test.want {
			t.Errorf("sessionMode(%q) = %q, want %q", test.mode, got, test.want)
		}
	}

	edit, execute := acpsdk.ToolKindEdit, acpsdk.ToolKindExecute
	options := []acpsdk.PermissionOption{{OptionId: "once", Kind: acpsdk.PermissionOptionKindAllowOnce}}
	for _, test := range []struct {
		mode    ports.PermissionMode
		kind    *acpsdk.ToolKind
		handled bool
	}{
		{ports.PermissionModeDefault, &edit, false},
		{ports.PermissionModeAcceptEdits, &edit, true},
		{ports.PermissionModeAcceptEdits, &execute, false},
		{ports.PermissionModeAuto, &execute, true},
		{ports.PermissionModeBypassPermissions, &execute, true},
	} {
		id, handled := permissionPolicy(test.mode, acpsdk.RequestPermissionRequest{
			ToolCall: acpsdk.ToolCallUpdate{Kind: test.kind}, Options: options,
		})
		if handled != test.handled || (handled && id != "once") {
			t.Errorf("permissionPolicy(%q) = (%q, %v)", test.mode, id, handled)
		}
	}
}

func TestOtherOpenCodeHarnessCannotResumePersistentConversation(t *testing.T) {
	for _, test := range []struct {
		name, version string
		owner, other  func() ports.ChatDriver
	}{
		{name: "v1 as v2", version: "1.18.33", owner: func() ports.ChatDriver { return opencodeacp.New(opencode.New(), nil) }, other: func() ports.ChatDriver { return New(opencodev2.New(), nil) }},
		{name: "v2 as v1", version: "2.0.0", owner: func() ports.ChatDriver { return New(opencodev2.New(), nil) }, other: func() ports.ChatDriver { return opencodeacp.New(opencode.New(), nil) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			writeOpenCodeACPExecutable(t, test.version)
			dataDir, workspace := t.TempDir(), t.TempDir()
			owner, err := test.owner().Start(context.Background(), ports.ChatStartConfig{
				SessionID: "shared-ao-session", DataDir: dataDir, WorkspacePath: workspace,
			})
			if err != nil {
				t.Fatalf("owner Start: %v", err)
			}
			providerID := owner.ProviderConversationID()
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = persistenthost.Shutdown(ctx, dataDir, "shared-ao-session")
			})

			_, err = test.other().Resume(context.Background(), ports.ChatResumeConfig{
				SessionID: "shared-ao-session", DataDir: dataDir, WorkspacePath: workspace,
				ProviderConversationID: providerID,
			})
			if !errors.Is(err, ports.ErrChatResumeFailed) {
				t.Fatalf("cross-harness Resume error = %v, want ErrChatResumeFailed", err)
			}
		})
	}
}

func TestOtherOpenCodeHarnessCannotResumeColdConversation(t *testing.T) {
	for _, test := range []struct {
		name, version, otherVersion string
		owner, other                func() ports.ChatDriver
	}{
		{name: "v1 as v2", version: "1.18.33", otherVersion: "2.0.0", owner: func() ports.ChatDriver { return opencodeacp.New(opencode.New(), nil) }, other: func() ports.ChatDriver { return New(opencodev2.New(), nil) }},
		{name: "v2 as v1", version: "2.0.0", otherVersion: "1.18.33", owner: func() ports.ChatDriver { return New(opencodev2.New(), nil) }, other: func() ports.ChatDriver { return opencodeacp.New(opencode.New(), nil) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, calls := writeOpenCodeACPExecutable(t, test.version)
			dataDir, workspace := t.TempDir(), t.TempDir()
			owner, err := test.owner().Start(context.Background(), ports.ChatStartConfig{
				SessionID: "cold-ao-session", DataDir: dataDir, WorkspacePath: workspace,
			})
			if err != nil {
				t.Fatalf("owner Start: %v", err)
			}
			providerID := owner.ProviderConversationID()
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := persistenthost.Shutdown(shutdownCtx, dataDir, "cold-ao-session"); err != nil {
				t.Fatalf("stop original host: %v", err)
			}
			same, err := test.owner().Resume(context.Background(), ports.ChatResumeConfig{
				SessionID: "cold-ao-session", DataDir: dataDir, WorkspacePath: workspace,
				ProviderConversationID: providerID,
			})
			if err != nil {
				t.Fatalf("same-harness cold Resume: %v", err)
			}
			if same.ProviderConversationID() != providerID {
				t.Fatalf("durable provider ID changed: got %q want %q", same.ProviderConversationID(), providerID)
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "session/resume:opencode-v2-provider\n") ||
				strings.Contains(string(data), "session/resume:ao-harness:") {
				t.Fatalf("resume did not send the raw provider ID:\n%s", data)
			}
			if err := same.Close(); err != nil {
				t.Fatal(err)
			}
			shutdownCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := persistenthost.Shutdown(shutdownCtx, dataDir, "cold-ao-session"); err != nil {
				t.Fatalf("stop same-harness resumed host: %v", err)
			}
			t.Setenv("AO_TEST_OPENCODE_VERSION", test.otherVersion)

			resumed, err := test.other().Resume(context.Background(), ports.ChatResumeConfig{
				SessionID: "cold-ao-session", DataDir: dataDir, WorkspacePath: workspace,
				ProviderConversationID: providerID,
			})
			if err == nil {
				_ = resumed.(ports.ChatProviderTerminator).Terminate()
				t.Fatal("cross-harness cold Resume succeeded")
			}
			if !errors.Is(err, ports.ErrChatResumeFailed) {
				t.Fatalf("cross-harness cold Resume error = %v, want ErrChatResumeFailed", err)
			}
		})
	}
}
