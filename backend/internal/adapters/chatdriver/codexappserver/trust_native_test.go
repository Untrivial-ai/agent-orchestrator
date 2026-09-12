package codexappserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/pkg/agentruntime"
)

// Run with AO_TEST_CODEX_BINARY=/absolute/path/to/codex. These tests use only
// disposable homes, harmless marker commands and a loopback Responses fixture.
// They make no account or model-service requests.
func TestNativeCodexProjectTrust(t *testing.T) {
	for _, linked := range []bool{false, true} {
		for _, trust := range []string{"", "untrusted", "trusted"} {
			t.Run(fmt.Sprintf("linked=%t/trust=%s", linked, trust), func(t *testing.T) {
				f := newNativeTrustFixture(t, linked)
				marker := filepath.Join(f.root, "mcp-ran")
				f.writeProjectConfig("[mcp_servers.project_marker]\ncommand=\"/bin/sh\"\nargs=[\"-c\"," + nativeQuote("printf started > "+nativeShellQuote(marker)) + "]\nstartup_timeout_sec=1\n")
				f.commitProject()
				f.writeUserConfig(trust, "")
				d := f.driver(nil)
				conv, err := d.Start(f.ctx, ports.ChatStartConfig{WorkspacePath: f.workdir, Permissions: ports.PermissionModeDefault, Env: f.env})
				if err != nil {
					t.Fatal(err)
				}
				f.t.Cleanup(func() { _ = conv.Close() })
				f.completeTurn(conv)
				f.assertMarker(marker, trust == "trusted")
				f.assertUserConfigUnchanged()
				id := conv.ProviderConversationID()
				if err := conv.Close(); err != nil {
					t.Fatal(err)
				}
				conv, err = d.Resume(f.ctx, ports.ChatResumeConfig{WorkspacePath: f.workdir, ProviderConversationID: id, Permissions: ports.PermissionModeDefault, Env: f.env})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conv.Close() })
				if _, err := conv.(*conversation).ReloadMCPServers(f.ctx); err != nil {
					t.Fatal(err)
				}
				f.completeTurn(conv)
				f.assertMarker(marker, trust == "trusted")
				f.assertUserConfigUnchanged()
			})
		}
	}
}

func TestNativeCodexScopedHookTrust(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprintf("linked=%t", linked), func(t *testing.T) {
			f := newNativeTrustFixture(t, linked)
			repoMarker := filepath.Join(f.root, "repository-hook-ran")
			userMarker := filepath.Join(f.root, "user-hook-ran")
			changedMarker := filepath.Join(f.root, "changed-hook-ran")
			var hooks []agentruntime.CodexHook
			for _, event := range []string{"SessionStart", "UserPromptSubmit", "PermissionRequest", "Stop"} {
				hooks = append(hooks, agentruntime.CodexHook{Event: event, Command: "printf started > " + nativeShellQuote(filepath.Join(f.root, event)), Timeout: 5})
			}
			args, err := agentruntime.CodexSessionHooks(hooks)
			if err != nil {
				t.Fatal(err)
			}
			f.writeProjectConfig("hooks.SessionStart=[{hooks=[{type=\"command\",command=" + nativeQuote("printf repo > "+nativeShellQuote(repoMarker)) + ",timeout=5}]}]\n")
			f.commitProject()
			userHooks := "hooks.SessionStart=[{hooks=[{type=\"command\",command=" + nativeQuote("printf user > "+nativeShellQuote(userMarker)) + ",timeout=5}]}]\n"
			if linked {
				// Session-layer trust must not undo an explicit user disable.
				userHooks += "hooks.state={\"/<session-flags>/config.toml:stop:0:0\"={enabled=false}}\n"
			}
			f.writeUserConfig("trusted", userHooks)
			d := f.driver(args)
			conv, err := d.Start(f.ctx, ports.ChatStartConfig{WorkspacePath: f.workdir, Permissions: ports.PermissionModeDefault, Env: f.env})
			if err != nil {
				t.Fatal(err)
			}
			f.t.Cleanup(func() { _ = conv.Close() })
			f.checkHookInventory(conv.(*conversation), 4)
			f.completeTurn(conv)
			for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
				f.assertMarker(filepath.Join(f.root, event), event != "Stop" || !linked)
			}
			f.assertMarker(repoMarker, false)
			f.assertMarker(userMarker, false)
			id := conv.ProviderConversationID()
			if err := conv.Close(); err != nil {
				t.Fatal(err)
			}
			// The source/event identity stays the same, but a changed command must
			// still require its own review when a native conversation is restored.
			f.writeProjectConfig("hooks.SessionStart=[{hooks=[{type=\"command\",command=" + nativeQuote("printf changed > "+nativeShellQuote(changedMarker)) + ",timeout=5}]}]\n")
			conv, err = d.Resume(f.ctx, ports.ChatResumeConfig{WorkspacePath: f.workdir, ProviderConversationID: id, Permissions: ports.PermissionModeDefault, Env: f.env})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conv.Close() })
			f.checkHookInventory(conv.(*conversation), 4)
			f.completeTurn(conv)
			f.assertMarker(changedMarker, false)
			f.assertUserConfigUnchanged()
		})
	}
}

func TestNativeCodexExistingHookApprovals(t *testing.T) {
	f := newNativeTrustFixture(t, false)
	userMarker := filepath.Join(f.root, "approved-user-hook")
	repoMarker := filepath.Join(f.root, "self-approved-project-hook")
	changedMarker := filepath.Join(f.root, "changed-user-hook")
	hookConfig := func(marker string) string {
		return "hooks.SessionStart=[{hooks=[{type=\"command\",command=" + nativeQuote("printf started > "+nativeShellQuote(marker)) + ",timeout=5}]}]\n"
	}
	f.writeProjectConfig(hookConfig(repoMarker))
	f.commitProject()
	f.writeUserConfig("trusted", hookConfig(userMarker))
	d := f.driver(nil)
	start := func() ports.ChatConversation {
		conv, err := d.Start(f.ctx, ports.ChatStartConfig{WorkspacePath: f.workdir, Permissions: ports.PermissionModeDefault, Env: f.env})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conv.Close() })
		return conv
	}
	conv := start()
	var userHook, projectHook nativeHookEntry
	for _, hook := range f.hookInventory(conv.(*conversation)) {
		if strings.Contains(hook.Key, "/home/codex/config.toml:") {
			userHook = hook
		} else if strings.Contains(hook.Key, "/repo/.codex/config.toml:") {
			projectHook = hook
		}
	}
	if userHook.CurrentHash == "" || projectHook.CurrentHash == "" {
		t.Fatal("native provider did not discover both hook sources")
	}
	f.completeTurn(conv)
	f.assertMarker(userMarker, false)
	f.assertMarker(repoMarker, false)
	if err := conv.Close(); err != nil {
		t.Fatal(err)
	}
	approval := func(hook nativeHookEntry) string {
		return "hooks.state={" + nativeQuote(hook.Key) + "={trusted_hash=" + nativeQuote(hook.CurrentHash) + "}}\n"
	}
	// A decision in user config is authoritative; the same hash placed in
	// repository config cannot authorize the repository's own command.
	f.writeUserConfig("trusted", hookConfig(userMarker)+approval(userHook))
	f.writeProjectConfig(hookConfig(repoMarker) + approval(projectHook))
	conv = start()
	f.completeTurn(conv)
	f.assertMarker(userMarker, true)
	f.assertMarker(repoMarker, false)
	f.assertUserConfigUnchanged()
	id := conv.ProviderConversationID()
	if err := conv.Close(); err != nil {
		t.Fatal(err)
	}
	// Keep the saved decision but change the approved command, then restore.
	f.writeUserConfig("trusted", hookConfig(changedMarker)+approval(userHook))
	conv, err := d.Resume(f.ctx, ports.ChatResumeConfig{WorkspacePath: f.workdir, ProviderConversationID: id, Permissions: ports.PermissionModeDefault, Env: f.env})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conv.Close() })
	f.completeTurn(conv)
	f.assertMarker(changedMarker, false)
	f.assertMarker(repoMarker, false)
	f.assertUserConfigUnchanged()
}

type nativeTrustFixture struct {
	t                        *testing.T
	ctx                      context.Context
	root, repo, workdir, bin string
	env                      map[string]string
	providerConfig           string
	userConfig               string
}

func newNativeTrustFixture(t *testing.T, linked bool) *nativeTrustFixture {
	t.Helper()
	bin := os.Getenv("AO_TEST_CODEX_BINARY")
	if bin == "" {
		t.Skip("set AO_TEST_CODEX_BINARY to run the native trust contract")
	}
	if !filepath.IsAbs(bin) {
		t.Fatal("AO_TEST_CODEX_BINARY must be absolute")
	}
	if runtime.GOOS == "windows" {
		t.Skip("native marker commands require a POSIX shell")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	// Git exports repository-selection variables to hooks. If this test is run
	// by a commit hook, none may redirect fixture Git commands or native Codex
	// probes back into the invoking checkout. Setenv registers their restoration.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "GIT_") {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	home := filepath.Join(root, "home")
	codexHome := filepath.Join(home, "codex")
	// Even a regression in AO's environment forwarding must not let this test
	// use the developer's account or provider configuration.
	for name, value := range map[string]string{"HOME": home, "CODEX_HOME": codexHome, "OPENAI_API_KEY": "", "CODEX_API_KEY": ""} {
		t.Setenv(name, value)
	}
	for _, path := range []string{filepath.Join(repo, ".codex"), codexHome} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"test-response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	t.Cleanup(server.Close)
	f := &nativeTrustFixture{t: t, ctx: ctx, root: root, repo: repo, workdir: repo, bin: bin,
		env:            map[string]string{"HOME": home, "CODEX_HOME": codexHome, "SHELL": "/bin/sh", "OPENAI_API_KEY": "", "CODEX_API_KEY": ""},
		providerConfig: "model=\"gpt-test\"\nmodel_provider=\"trust-test\"\n[model_providers.trust-test]\nname=\"trust-test\"\nbase_url=" + nativeQuote(server.URL) + "\nwire_api=\"responses\"\nrequest_max_retries=0\nstream_max_retries=0\nsupports_websockets=false\n",
	}
	f.git("init")
	f.git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "fixture")
	if linked {
		f.workdir = filepath.Join(root, "linked")
		f.git("worktree", "add", "--detach", f.workdir)
	}
	return f
}

func (f *nativeTrustFixture) git(args ...string) {
	f.t.Helper()
	cmd := exec.CommandContext(f.ctx, "git", append([]string{"-C", f.repo, "-c", "core.hooksPath=/dev/null"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		f.t.Fatalf("git: %v: %s", err, out)
	}
}

func (f *nativeTrustFixture) writeProjectConfig(config string) {
	f.t.Helper()
	// Codex discovers linked-worktree hooks in the registered root checkout,
	// while executable project config must also exist in the linked checkout.
	for _, dir := range []string{f.repo, f.workdir} {
		if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o700); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".codex", "config.toml"), []byte(config), 0o600); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *nativeTrustFixture) commitProject() {
	f.git("add", ".codex/config.toml")
	f.git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "project config")
	if f.workdir != f.repo {
		f.git("-C", f.workdir, "add", ".codex/config.toml")
		f.git("-C", f.workdir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "project config")
	}
}

func (f *nativeTrustFixture) writeUserConfig(trust, extra string) {
	f.t.Helper()
	f.userConfig = extra + f.providerConfig
	if trust != "" {
		root, err := filepath.EvalSymlinks(f.repo)
		if err != nil {
			f.t.Fatal(err)
		}
		f.userConfig += "\n[projects." + nativeQuote(root) + "]\ntrust_level=" + nativeQuote(trust) + "\n"
	}
	if err := os.WriteFile(filepath.Join(f.env["CODEX_HOME"], "config.toml"), []byte(f.userConfig), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *nativeTrustFixture) driver(args []string) *Driver {
	f.t.Helper()
	bin := f.bin
	if len(args) != 0 {
		bin = filepath.Join(f.root, "codex-with-ao-hooks")
		var wrapper strings.Builder
		wrapper.WriteString("#!/bin/sh\nexec " + nativeShellQuote(f.bin) + " \"$@\"")
		for _, arg := range args {
			wrapper.WriteString(" " + nativeShellQuote(arg))
		}
		wrapper.WriteByte('\n')
		if err := os.WriteFile(bin, []byte(wrapper.String()), 0o700); err != nil {
			f.t.Fatal(err)
		}
	}
	d := New(fakePlugin{bin: bin}, nil)
	d.persistent = false
	return d
}

func (f *nativeTrustFixture) completeTurn(conv ports.ChatConversation) {
	f.t.Helper()
	if _, err := conv.SendTurn(f.ctx, ports.ChatUserMessage{Text: "Reply OK without tools"}); err != nil {
		f.t.Fatal(err)
	}
	for {
		select {
		case event := <-conv.Events():
			if event.Kind == ports.ChatEventTurnCompleted {
				if event.TurnState != domain.TurnStateCompleted {
					f.t.Fatalf("turn failed: %+v", event)
				}
				return
			}
		case <-f.ctx.Done():
			f.t.Fatal(f.ctx.Err())
		}
	}
}

func (f *nativeTrustFixture) assertMarker(path string, want bool) {
	f.t.Helper()
	_, err := os.Stat(path)
	if (err == nil) != want || err != nil && !os.IsNotExist(err) {
		f.t.Fatalf("marker %s: err=%v, want present=%t", filepath.Base(path), err, want)
	}
}

func (f *nativeTrustFixture) assertUserConfigUnchanged() {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.env["CODEX_HOME"], "config.toml"))
	if err != nil || string(data) != f.userConfig {
		f.t.Fatalf("provider changed persisted user decisions: %v", err)
	}
}

type nativeHookEntry struct {
	Key         string `json:"key"`
	CurrentHash string `json:"currentHash"`
	TrustStatus string `json:"trustStatus"`
}

func (f *nativeTrustFixture) hookInventory(conv *conversation) []nativeHookEntry {
	f.t.Helper()
	var response struct {
		Data []struct {
			Hooks []nativeHookEntry `json:"hooks"`
		} `json:"data"`
	}
	if err := conv.conn.request(f.ctx, "hooks/list", map[string]any{"cwds": []string{f.workdir}}, &response); err != nil {
		f.t.Fatal(err)
	}
	var hooks []nativeHookEntry
	for _, item := range response.Data {
		hooks = append(hooks, item.Hooks...)
	}
	return hooks
}

func (f *nativeTrustFixture) checkHookInventory(conv *conversation, want int) {
	f.t.Helper()
	hooks := f.hookInventory(conv)
	trusted := 0
	for _, hook := range hooks {
		if hook.TrustStatus == "trusted" {
			if !strings.HasPrefix(hook.Key, "/<session-flags>/config.toml:") {
				f.t.Fatalf("unexpected hook approved: %+v", hook)
			}
			trusted++
		}
	}
	if trusted != want {
		f.t.Fatalf("trusted AO hooks = %d, want %d; inventory=%+v", trusted, want, hooks)
	}
}

func nativeQuote(s string) string {
	value, _ := json.Marshal(s)
	return string(value)
}

func nativeShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
