package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/agentlaunch"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var (
	managedEnv = map[string]string{"AO_PROXY_ENDPOINT": "http://127.0.0.1:4567", "AO_PROXY_TICKET": "private-session-ticket"}
	nativeEnv  = map[string]string{"AO_PROXY_ENDPOINT": "", "AO_PROXY_TICKET": ""}
)

func TestCodexHostIsPointedAtTheAccountHelperOnlyForAManagedSession(t *testing.T) {
	for _, env := range []map[string]string{managedEnv, nativeEnv} {
		driver, _ := newTestDriver(t)
		driver.persistent = true
		captured := errors.New("host captured")
		var cfg persistenthost.Config
		driver.connectHost = func(_ context.Context, next persistenthost.Config) (*persistenthost.Transport, error) {
			cfg = next
			return nil, captured
		}
		if _, err := driver.Start(context.Background(), ports.ChatStartConfig{SessionID: "s", WorkspacePath: t.TempDir(), DataDir: t.TempDir(), Env: env}); !errors.Is(err, captured) {
			t.Fatal(err)
		}
		argv, managed := strings.Join(cfg.Argv, " "), env["AO_PROXY_TICKET"] != ""
		if !reflect.DeepEqual(cfg.Argv, agentlaunch.CodexProxyArgv([]string{"codex", "app-server"}, env)) || strings.Contains(argv, `model_provider="ao-managed"`) != managed || strings.Contains(argv, "private-session-ticket") {
			t.Fatalf("managed=%v argv=%v", managed, cfg.Argv)
		}
		// The ticket travels in the environment, never in the arguments.
		if slices.Contains(cfg.Env, "AO_PROXY_TICKET=private-session-ticket") != managed {
			t.Fatalf("managed=%v env=%v", managed, cfg.Env)
		}
	}
}

func TestCodexPreparedHostUsesTheEnvironmentPreparedAtLaunch(t *testing.T) {
	fresh := map[string]string{"AO_PROXY_ENDPOINT": "http://127.0.0.1:5000", "AO_PROXY_TICKET": "current-private-ticket"}
	failure := errors.New("account routing unavailable")
	for _, prepareErr := range []error{nil, failure} {
		driver, _ := newTestDriver(t)
		driver.persistent = true
		captured := errors.New("host captured")
		driver.connectHost = func(ctx context.Context, cfg persistenthost.Config) (*persistenthost.Transport, error) {
			prepared, err := cfg.Prepare(ctx)
			if !errors.Is(err, prepareErr) {
				t.Fatalf("prepare=%v", err)
			}
			// A failed preparation supplies nothing to launch with instead.
			if prepareErr != nil && len(prepared.Argv)+len(prepared.Env) != 0 {
				t.Fatalf("failed preparation prepared %+v", prepared)
			}
			if prepareErr == nil && (!reflect.DeepEqual(prepared.Argv, agentlaunch.CodexProxyArgv([]string{"codex", "app-server"}, fresh)) || !slices.Contains(prepared.Env, "AO_PROXY_TICKET=current-private-ticket")) {
				t.Fatalf("prepared host kept the stale launch: %+v", prepared)
			}
			return nil, captured
		}
		_, err := driver.Start(context.Background(), ports.ChatStartConfig{SessionID: "s", WorkspacePath: t.TempDir(), DataDir: t.TempDir(), Env: managedEnv, PrepareEnv: func(context.Context) (map[string]string, error) {
			return fresh, prepareErr
		}})
		if !errors.Is(err, captured) {
			t.Fatal(err)
		}
	}
}

// Codex resumes a thread on the provider it was created with, so a thread that
// began on this computer's own sign-in reaches the helper only if the resume
// names it.
func TestCodexResumeNamesTheAccountHelperOnlyWhenRouted(t *testing.T) {
	for want, env := range map[string]map[string]string{"ao-managed": managedEnv, "": nil} {
		driver, server := newTestDriver(t)
		server.reply("thread/resume", `{"thread":{"id":"saved-native-thread","turns":[]}}`)
		conversation, err := driver.Resume(context.Background(), ports.ChatResumeConfig{SessionID: "saved-session", WorkspacePath: t.TempDir(), ProviderConversationID: "saved-native-thread", Env: env})
		if err != nil {
			t.Fatal(err)
		}
		var params struct {
			ModelProvider string `json:"modelProvider"`
		}
		resume := server.awaitFrame(func(f frame) bool { return f.Method == "thread/resume" })
		if err := json.Unmarshal(resume.Params, &params); err != nil || params.ModelProvider != want {
			t.Fatalf("resumed on provider %q, want %q (err=%v)", params.ModelProvider, want, err)
		}
		if conversation.ProviderConversationID() != "saved-native-thread" || server.sentMethod("thread/start") {
			t.Fatal("resume replaced the saved thread")
		}
		_ = conversation.Close()
	}
}
