package host

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	proxyapi "github.com/router-for-me/CLIProxyAPI/v8/sdk/api"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"gopkg.in/yaml.v3"
)

// loaded is told each time the SDK's server has taken a configuration. Its handlers read, unguarded,
// what a load replaces, so a call made while one is under way is a data race inside the SDK.
var loaded = make(chan struct{}, 16)

// TestMain passes standard output on unchanged and watches it: a line there is the only sign the SDK gives of a finished load.
func TestMain(m *testing.M) {
	stdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	os.Stdout = write
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		for lines := bufio.NewReader(read); ; {
			line, err := lines.ReadString('\n')
			_, _ = stdout.WriteString(line)
			if strings.Contains(line, "server clients and configuration updated") {
				select {
				case loaded <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	code := m.Run()
	_ = write.Close()
	<-copied
	os.Exit(code)
}

// awaitLoad waits until the SDK's server has taken its next configuration and none has followed for
// the quiet time: the SDK goes on settling its accounts after saying so, and may load a saved change twice.
func awaitLoad(t *testing.T, quiet time.Duration) {
	t.Helper()
	select {
	case <-loaded:
	case <-time.After(30 * time.Second):
		t.Fatal("the SDK did not load its configuration")
	}
	for {
		select {
		case <-loaded:
		case <-time.After(quiet):
			return
		}
	}
}

func TestBuildRejectsUnsafeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		root               string
		port               int
		control, inference string
	}{
		{"relative", 1234, control, inference},
		{t.TempDir(), 0, control, inference},
		{t.TempDir(), 65536, control, inference},
		{t.TempDir(), 1234, "short", inference},
		{t.TempDir(), 1234, control, "short"},
		{t.TempDir(), 1234, control, control},
	} {
		if service, err := Build(tc.root, tc.port, tc.control, tc.inference, openTestRoutes(t), openTestActivity(t)); err == nil || service != nil {
			t.Fatalf("unsafe configuration accepted: root=%s port=%d", tc.root, tc.port)
		}
		if entries, _ := os.ReadDir(tc.root); len(entries) != 0 {
			t.Fatalf("a refused configuration wrote %v", entries)
		}
	}
	if err := Run(context.Background(), "relative", 0, "", ""); err == nil {
		t.Fatal("an invalid run started")
	}
}

func savedConfig(t *testing.T, root string) (config.Config, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "config.yaml"))
	var cfg config.Config
	if err != nil || yaml.Unmarshal(data, &cfg) != nil {
		t.Fatalf("saved configuration is unreadable: %v", err)
	}
	return cfg, string(data)
}

func TestBuildKeepsEverythingPrivateAndUnderItsDataDirectory(t *testing.T) {
	for _, name := range []string{"ordinary", "folder with spaces", "account#data?", "账户"} {
		root := filepath.Join(t.TempDir(), name)
		routes := OpenRoutes(filepath.Join(root, "run", "routes.json"))
		if service, err := Build(root, 12345, control, inference, routes, openTestActivity(t)); err != nil || service == nil {
			t.Fatalf("%s: %v", name, err)
		}
		cfg, raw := savedConfig(t, root)
		if cfg.Host != "127.0.0.1" || cfg.Port != 12345 || cfg.AuthDir != filepath.Join(root, "auth") {
			t.Fatalf("%s: the helper listens or stores outside its boundary: %s %d %s", name, cfg.Host, cfg.Port, cfg.AuthDir)
		}
		if len(cfg.APIKeys) != 1 || cfg.APIKeys[0] != inference || strings.Contains(raw, control) {
			t.Fatalf("%s: the control key is saved, or the inference key is not the only API key", name)
		}
		if !cfg.RemoteManagement.DisableControlPanel || !cfg.RemoteManagement.DisableAutoUpdatePanel || cfg.RemoteManagement.AllowRemote {
			t.Fatalf("%s: a management surface AO does not use is open", name)
		}
		if cfg.MaxRetryCredentials != 1 || cfg.Routing.Strategy != "fill-first" || cfg.Routing.SessionAffinity || !cfg.CommercialMode || !cfg.WebsocketAuth {
			t.Fatalf("%s: the SDK may choose an account itself", name)
		}
		for path, mode := range map[string]os.FileMode{"config.yaml": 0o600, "auth": 0o700} {
			if info, err := os.Stat(filepath.Join(root, path)); err != nil || info.Mode().Perm() != mode {
				t.Fatalf("%s: %s is not owner-only: %v", name, path, err)
			}
		}
		if err := routes.Apply([]Route{route("ticket", "codex", "account")}, []string{"account"}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, "run", "routes.json")); err != nil {
			t.Fatalf("%s: routes were not saved under the data directory: %v", name, err)
		}
	}
}

func TestBuildTightensTheCredentialDirectoryAndKeepsItsAccounts(t *testing.T) {
	root := t.TempDir()
	authDir := filepath.Join(root, "auth")
	if err := os.Mkdir(authDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(authDir, 0o755); err != nil {
		t.Fatal(err)
	}
	credential := []byte(`{"type":"codex","access_token":"fake-existing-token"}`)
	if err := os.WriteFile(filepath.Join(authDir, "kept.json"), credential, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(root, 12345, control, inference, openTestRoutes(t), openTestActivity(t)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(authDir)
	data, readErr := os.ReadFile(filepath.Join(authDir, "kept.json"))
	if err != nil || readErr != nil || info.Mode().Perm() != 0o700 || string(data) != string(credential) {
		t.Fatalf("mode=%v credential=%s", info.Mode(), data)
	}
}

func TestBuildFailsRatherThanRunOnStorageItCannotUse(t *testing.T) {
	for name, occupy := range map[string]func(root string) (path, content string){
		"the credential directory is a file": func(root string) (string, string) { return filepath.Join(root, "auth"), "occupied" },
		"the saved configuration is damaged": func(root string) (string, string) { return filepath.Join(root, "config.yaml"), "codex-api-key: [\n" },
	} {
		root := t.TempDir()
		path, content := occupy(root)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if service, err := Build(root, 12345, control, inference, openTestRoutes(t), openTestActivity(t)); err == nil || service != nil {
			t.Fatalf("%s: the helper was built anyway", name)
		}
		// Nothing is replaced: a damaged configuration may still hold the person's API keys.
		if data, err := os.ReadFile(path); err != nil || string(data) != content {
			t.Fatalf("%s: the file was replaced: %v", name, err)
		}
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config.yaml"), 0o700); err != nil {
		t.Fatal(err)
	}
	if service, err := Build(root, 12345, control, inference, openTestRoutes(t), openTestActivity(t)); err == nil || service != nil {
		t.Fatal("a configuration path that is a directory was accepted")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port
}

// running starts a built service and returns a function that calls it and one that stops it.
func running(t *testing.T, service *cliproxy.Service, port int) (call func(method, path, key, body string) (int, string), stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	for len(loaded) > 0 {
		<-loaded
	}
	go func() { finished <- service.Run(ctx) }()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("the service did not stop")
		}
	}
	t.Cleanup(stop)
	client := &http.Client{Timeout: 5 * time.Second}
	call = func(method, path, key, body string) (int, string) {
		request, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return 0, err.Error()
		}
		defer func() { _ = response.Body.Close() }()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, strings.TrimSpace(string(data))
	}
	// The SDK loads its configuration once more after it starts listening; nothing is asked of it before that.
	awaitLoad(t, 300*time.Millisecond)
	if status, body := call(http.MethodGet, "/ao/status", control, ""); status != http.StatusOK || body != `{"protocol_version":3}` {
		t.Fatalf("the started service answered %d %s", status, body)
	}
	return call, stop
}

func TestBuiltHelperServesTheSDKsSignInsAndKeepsAPIKeysAcrossRestarts(t *testing.T) {
	root, port := t.TempDir(), freePort(t)
	service, err := Build(root, port, control, inference, OpenRoutes(filepath.Join(root, "run", "routes.json")), openTestActivity(t))
	if err != nil {
		t.Fatal(err)
	}
	call, stop := running(t, service, port)
	for _, key := range []string{inference, "wrong-control", ""} {
		if status, _ := call(http.MethodGet, "/v8/management/credentials", key, ""); status != http.StatusUnauthorized {
			t.Fatalf("key %q reached management: %d", key, status)
		}
	}
	if status, _ := call(http.MethodGet, "/v8/management/config", control, ""); status != http.StatusNotFound {
		t.Fatalf("a management call outside the allowlist: %d", status)
	}
	if status, body := call(http.MethodGet, "/v8/management/credentials", control, ""); status != http.StatusOK {
		t.Fatalf("the SDK refused the control key: %d %s", status, body)
	}
	for _, provider := range []string{"codex", "claude"} {
		status, body := call(http.MethodGet, "/v8/management/oauth/auth-url?provider="+provider, control, "")
		var login struct{ URL, State string }
		if err := json.Unmarshal([]byte(body), &login); err != nil || status != http.StatusOK || login.State == "" || !strings.HasPrefix(login.URL, "https://") {
			t.Fatalf("%s sign-in did not start: %d %s", provider, status, body)
		}
		state := url.QueryEscape(login.State)
		if status, body = call(http.MethodGet, "/v8/management/oauth/status?state="+state, control, ""); status != http.StatusOK || !strings.Contains(body, `"wait"`) {
			t.Fatalf("%s sign-in is not waiting: %d %s", provider, status, body)
		}
		if status, _ = call(http.MethodDelete, "/v8/management/oauth/session?state="+state, control, ""); status/100 != 2 {
			t.Fatalf("%s sign-in could not be cancelled: %d", provider, status)
		}
	}
	// An API key saved through the SDK becomes an account AO can name.
	if status, body := call(http.MethodPut, "/v0/management/codex-api-key", control, `[{"api-key":"sk-test-key","base-url":"https://api.example.test"}]`); status/100 != 2 {
		t.Fatalf("saving a key: %d %s", status, body)
	}
	awaitLoad(t, time.Second)
	status, body := call(http.MethodPost, "/ao/tag-api-key", control, `{"id":"key-login","provider":"codex","api_key":"sk-test-key","base_url":"https://api.example.test/","label":"Work"}`)
	var tagged struct {
		AuthID string `json:"auth_id"`
	}
	if err := json.Unmarshal([]byte(body), &tagged); err != nil || status != http.StatusOK || tagged.AuthID == "" {
		t.Fatalf("tagging the key: %d %s", status, body)
	}
	status, body = call(http.MethodGet, "/ao/login-result/key-login", control, "")
	if status != http.StatusOK || !strings.Contains(body, `"kind":"api_key"`) || !strings.Contains(body, `"email":"Work"`) || !strings.Contains(body, `"credential_ref":"config-index:codex:`) || strings.Contains(body, "sk-test-key") {
		t.Fatalf("login result: %d %s", status, body)
	}
	account := `{"auth_id":"` + tagged.AuthID + `","provider":"codex"}`
	if status, body = call(http.MethodPost, "/ao/account-state", control, account); status != http.StatusOK || !strings.Contains(body, `"requests":[`) || strings.Contains(body, "signInEnding") {
		t.Fatalf("account state: %d %s", status, body)
	}
	if status, _ = call(http.MethodPost, "/ao/account-refresh", control, account); status != http.StatusBadRequest {
		t.Fatalf("refreshing an API key: %d", status)
	}
	stop()
	restartedPort := freePort(t)
	if _, err = Build(root, restartedPort, control, inference, OpenRoutes(filepath.Join(root, "run", "routes.json")), openTestActivity(t)); err != nil {
		t.Fatal(err)
	}
	cfg, _ := savedConfig(t, root)
	if cfg.Port != restartedPort || len(cfg.CodexKey) != 1 || cfg.CodexKey[0].APIKey != "sk-test-key" || cfg.CodexKey[0].BaseURL != "https://api.example.test" {
		t.Fatalf("a restart lost the saved key: port=%d keys=%d", cfg.Port, len(cfg.CodexKey))
	}
}

// The SDK configures its own account selection while it starts; AO's exact
// selector and model filter must still be the ones in force afterwards.
func TestStartedSDKStillRunsEachSessionOnItsOwnAccount(t *testing.T) {
	root, port := t.TempDir(), freePort(t)
	routes := openTestRoutes(t)
	if err := routes.Apply([]Route{route("session-a", "codex", "account-a"), route("session-b", "codex", "account-b")}, []string{"account-a", "account-b"}); err != nil {
		t.Fatal(err)
	}
	boundary := Boundary{Routes: routes, ControlKey: control, InferenceKey: inference, Logins: newLogins(filepath.Join(root, "auth"))}
	cfg := &config.Config{Host: "127.0.0.1", Port: port, AuthDir: filepath.Join(root, "auth"), CommercialMode: true, MaxRetryCredentials: 1}
	cfg.APIKeys = []string{inference}
	cfg.Routing.Strategy = "fill-first"
	cfg.RemoteManagement.DisableControlPanel, cfg.RemoteManagement.DisableAutoUpdatePanel = true, true
	path := filepath.Join(root, "config.yaml")
	data, _ := yaml.Marshal(cfg)
	if err := writePrivate(path, data); err != nil {
		t.Fatal(err)
	}
	var manager *coreauth.Manager
	service, err := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath(path).WithLocalManagementPassword(control).
		WithServerOptions(proxyapi.WithEngineConfigurator(func(engine *gin.Engine) { engine.Use(boundary.Middleware) }), proxyapi.WithRouterConfigurator(func(engine *gin.Engine, h *handlers.BaseAPIHandler, cfg *config.Config) {
			boundary.Configure(engine, h, cfg)
			manager = h.AuthManager
		})).Build()
	if err != nil {
		t.Fatal(err)
	}
	call, _ := running(t, service, port)
	// Added once the SDK has loaded its own (empty) set of accounts, which would replace these.
	provider := &fakeProvider{name: "codex"}
	manager.RegisterExecutor(provider)
	for _, id := range []string{"account-a", "account-b"} {
		if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive}); err != nil {
			t.Fatal(err)
		}
		cliproxy.GlobalModelRegistry().RegisterClient(id, "codex", []*cliproxy.ModelInfo{{ID: testModel}, {ID: "only-" + id}})
		t.Cleanup(func() { cliproxy.GlobalModelRegistry().UnregisterClient(id) })
	}
	for _, ticket := range []string{"session-a", "session-b", "session-a"} {
		if status, body := call(http.MethodPost, "/v1/responses", ticket, `{"model":"`+testModel+`","input":"hi"}`); status != http.StatusOK {
			t.Fatalf("%s: %d %s", ticket, status, body)
		}
	}
	if got := strings.Join(provider.ran(), ","); got != "account-a plain,account-b plain,account-a plain" {
		t.Fatalf("ran on %s", got)
	}
	for ticket, account := range map[string]string{"session-a": "account-a", "session-b": "account-b"} {
		status, body := call(http.MethodGet, "/v1/models", ticket, "")
		var list struct {
			Data []struct{ ID string }
		}
		if err := json.Unmarshal([]byte(body), &list); err != nil || status != http.StatusOK || len(list.Data) != 2 {
			t.Fatalf("%s model list: %d %s", ticket, status, body)
		}
		for _, model := range list.Data {
			if model.ID != testModel && model.ID != "only-"+account {
				t.Fatalf("%s was offered %s", ticket, model.ID)
			}
		}
	}
}
