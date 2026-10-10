package host

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	proxyapi "github.com/router-for-me/CLIProxyAPI/v8/sdk/api"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	executor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"gopkg.in/yaml.v3"
)

func openTestActivity(t *testing.T) *Activity {
	t.Helper()
	return OpenActivity(filepath.Join(t.TempDir(), "run", "activity.json"))
}

// reported is what the counter adds to an account's state, as AO reads it.
func reported(t *testing.T, a *Activity, account string, now time.Time) map[string]any {
	t.Helper()
	data, err := json.Marshal(a.report(gin.H{}, account, now))
	return parsed(t, string(data), err)
}
func parsed(t *testing.T, text string, err error) map[string]any {
	t.Helper()
	var out map[string]any
	if err != nil || json.Unmarshal([]byte(text), &out) != nil {
		t.Fatalf("not a JSON object: %v %s", err, text)
	}
	return out
}

// lastDays is the fourteen days up to now, oldest first, with the tokens given by days ago.
func lastDays(now time.Time, tokens map[int]int) string {
	days := []string{}
	for ago := 13; ago >= 0; ago-- {
		days = append(days, fmt.Sprintf(`{"date":%q,"tokens":%d}`, now.AddDate(0, 0, -ago).Format(time.DateOnly), tokens[ago]))
	}
	return "[" + strings.Join(days, ",") + "]"
}

func TestActivityCountsEachRequestUnderItsAccountDayModelAndSession(t *testing.T) {
	now, no := time.Now(), false
	ago := func(days int) time.Time { return now.AddDate(0, 0, -days) }
	a := openTestActivity(t)
	first, second := TicketHash("ticket-1"), TicketHash("ticket-2")
	a.requests.Store("request-1", first)
	a.requests.Store("request-2", second)
	for _, record := range []coreusage.Record{
		{AuthID: "account", Model: "opus", RequestedAt: ago(20), Detail: coreusage.Detail{TotalTokens: 1000}},
		{AuthID: "account", Model: "opus", RequestedAt: ago(10), Detail: coreusage.Detail{TotalTokens: 7}},
		{AuthID: "account", Model: "opus", RequestedAt: ago(3), TraceID: "request-1", Detail: coreusage.Detail{TotalTokens: 50}},
		{AuthID: "account", Model: "opus", RequestedAt: now, TraceID: "request-1", Detail: coreusage.Detail{TotalTokens: 100, InputTokens: 1}},
		// Without a total, the parts are added up; reasoning is part of the output.
		{AuthID: "account", Model: "haiku", RequestedAt: now, TraceID: "request-2", Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 5, ReasoningTokens: 99, CacheReadTokens: 3, CacheCreationTokens: 2}},
		{AuthID: "account", Model: "haiku", RequestedAt: now, TraceID: "request-of-no-session", Detail: coreusage.Detail{TotalTokens: 1}},
		// Not a generation, nothing used, another account.
		{AuthID: "account", Model: "opus", RequestedAt: now, TraceID: "request-1", Generate: &no, Detail: coreusage.Detail{TotalTokens: 500}},
		{AuthID: "account", Model: "opus", RequestedAt: now, TraceID: "request-1"},
		{AuthID: "other", Model: "opus", RequestedAt: now, TraceID: "request-2", Detail: coreusage.Detail{TotalTokens: 9}},
	} {
		a.HandleUsage(context.Background(), record)
	}
	for i := 1; i <= 8; i++ {
		a.HandleUsage(context.Background(), coreusage.Record{AuthID: "many", Model: fmt.Sprint("model-", i), RequestedAt: ago(i - 1), Detail: coreusage.Detail{TotalTokens: int64(i)}})
	}
	for account, want := range map[string]string{
		"account": fmt.Sprintf(`{"activity":{"today":121,"week":171,"total":1178,"since":%q,"days":%s,"models":[{"model":"opus","tokens":150},{"model":"haiku","tokens":21}],"sessions":{%q:100,%q:20}}}`,
			ago(20).Format(time.DateOnly), lastDays(now, map[int]int{10: 7, 3: 50, 0: 121}), first, second),
		"other": fmt.Sprintf(`{"activity":{"today":9,"week":9,"total":9,"since":%q,"days":%s,"models":[{"model":"opus","tokens":9}],"sessions":{%q:9}}}`,
			now.Format(time.DateOnly), lastDays(now, map[int]int{0: 9}), second),
		// The week's models, largest first, and no more than six of them.
		"many": fmt.Sprintf(`{"activity":{"today":1,"week":28,"total":36,"since":%q,"days":%s,"models":[{"model":"model-7","tokens":7},{"model":"model-6","tokens":6},{"model":"model-5","tokens":5},{"model":"model-4","tokens":4},{"model":"model-3","tokens":3},{"model":"model-2","tokens":2}]}}`,
			now.Format(time.DateOnly), lastDays(now, map[int]int{0: 1, 1: 2, 2: 3, 3: 4, 4: 5, 5: 6, 6: 7, 7: 8})),
		"unused": `{}`,
	} {
		if got := reported(t, a, account, now); !reflect.DeepEqual(got, parsed(t, want, nil)) {
			t.Fatalf("%s:\n got %v\nwant %s", account, got, want)
		}
	}
	// Tomorrow nothing has been used yet, by any session.
	tomorrow := reported(t, a, "account", now.AddDate(0, 0, 1))["activity"].(map[string]any)
	if tomorrow["today"] != float64(0) || tomorrow["week"] != float64(171) || tomorrow["sessions"] != nil {
		t.Fatalf("tomorrow: %v", tomorrow)
	}
}

func TestAccountStateCarriesWhatWasCounted(t *testing.T) {
	f := accountFixture(t, &fakeProvider{name: "claude"}, &coreauth.Auth{})
	state := func() map[string]any {
		response := f.send(http.MethodPost, "/ao/account-state", control, `{"auth_id":"account","provider":"claude"}`)
		return parsed(t, response.Body.String(), nil)
	}
	if got := state(); got["activity"] != nil || got["health"] != nil || got["requests"] == nil {
		t.Fatalf("an account nothing passed through: %v", got)
	}
	f.counted.HandleUsage(context.Background(), coreusage.Record{AuthID: "account", Model: "opus", RequestedAt: time.Now(), Stream: true, TTFT: 250 * time.Millisecond, Detail: coreusage.Detail{TotalTokens: 12}})
	f.counted.HandleUsage(context.Background(), coreusage.Record{AuthID: "account", Model: "opus", RequestedAt: time.Now(), Failed: true, Fail: coreusage.Failure{StatusCode: 429, Body: "private provider answer"}})
	got := state()
	activity, _ := got["activity"].(map[string]any)
	health, _ := got["health"].(map[string]any)
	if activity["total"] != float64(12) || len(activity["days"].([]any)) != 14 || health["firstWordMs"] != float64(250) || !reflect.DeepEqual(health["failures"], map[string]any{"limit": float64(1)}) || got["requests"] == nil {
		t.Fatalf("state: %v", got)
	}
}

func TestActivityKeepsItsDaysAcrossARestartAndNothingPrivate(t *testing.T) {
	now, path := time.Now(), filepath.Join(t.TempDir(), "run", "activity.json")
	ago := func(days int) time.Time { return now.AddDate(0, 0, -days) }
	const account = "codex-someone@example.test-pro.json"
	a := OpenActivity(path)
	if a.save(); reported(t, a, account, now)["activity"] != nil {
		t.Fatal("a missing file did not start empty")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("saved with nothing counted: %v", err)
	}
	a.requests.Store("request-1", TicketHash("ticket-1"))
	for _, used := range [][2]int{{40, 1000}, {2, 30}, {0, 12}} {
		a.HandleUsage(context.Background(), coreusage.Record{AuthID: account, Model: "gpt", RequestedAt: ago(used[0]), TraceID: "request-1", Detail: coreusage.Detail{TotalTokens: int64(used[1])}})
	}
	a.HandleUsage(context.Background(), coreusage.Record{AuthID: account, Model: "gpt", RequestedAt: now, Failed: true, Fail: coreusage.Failure{StatusCode: 401, Body: "private provider answer"}})
	before := reported(t, a, account, now)
	if before["health"] == nil || before["activity"].(map[string]any)["sessions"] == nil {
		t.Fatalf("before the restart: %v", before)
	}
	a.save()
	data, err := os.ReadFile(path)
	info, statErr := os.Stat(path)
	if err != nil || statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the counts are not saved owner-only: %v %v", err, statErr)
	}
	for _, private := range []string{"example.test", account, TicketHash("ticket-1"), "request-1", "private provider answer"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("the saved counts hold %q: %s", private, data)
		}
	}
	// Only the token counts come back: sessions, failures and first-token times start over.
	want := before["activity"].(map[string]any)
	delete(want, "sessions")
	restarted := OpenActivity(path)
	if got := reported(t, restarted, account, now); !reflect.DeepEqual(got, map[string]any{"activity": want}) || want["total"] != float64(1042) || want["since"] != ago(40).Format(time.DateOnly) {
		t.Fatalf("after the restart:\n got %v\nwant %v", got, want)
	}
	// Thirty days are kept: the oldest day is gone from the file, and still part of the total.
	if kept := restarted.accounts[TicketHash(account)].Days; len(kept) != 2 || kept[ago(40).Format(time.DateOnly)] != nil {
		t.Fatalf("days kept: %v", kept)
	}
	// Nothing changed since, so nothing is written.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if restarted.save(); !os.IsNotExist(func() error { _, err := os.Stat(path); return err }()) {
		t.Fatal("saved again with nothing new counted")
	}
	for _, unreadable := range []string{"{not json", "null", `{"account":null}`, `[1]`} {
		if err := os.WriteFile(path, []byte(unreadable), 0o600); err != nil {
			t.Fatal(err)
		}
		empty := OpenActivity(path)
		if got := reported(t, empty, account, now); len(got) != 0 {
			t.Fatalf("%s: %v", unreadable, got)
		}
		empty.HandleUsage(context.Background(), coreusage.Record{AuthID: account, Model: "gpt", RequestedAt: now, Detail: coreusage.Detail{TotalTokens: 5}})
		if empty.save(); reported(t, OpenActivity(path), account, now)["activity"].(map[string]any)["total"] != float64(5) {
			t.Fatalf("%s: the file was not replaced", unreadable)
		}
	}
}

func TestFailuresAreCountedByKindAndFirstTokenTimesByTheirMedian(t *testing.T) {
	now, no := time.Now(), false
	a := openTestActivity(t)
	failed := func(at time.Time, status int) coreusage.Record {
		return coreusage.Record{AuthID: "account", Model: "opus", RequestedAt: at, Failed: true, Fail: coreusage.Failure{StatusCode: status}}
	}
	a.HandleUsage(context.Background(), failed(now.Add(-4*time.Hour), 429))
	for _, tc := range []struct {
		status int
		kind   string
	}{{429, "limit"}, {401, "sign-in"}, {403, "sign-in"}, {500, "server"}, {529, "server"}, {0, "server"}, {400, "other"}, {404, "other"}, {499, "other"}} {
		if got := failureKind(tc.status); got != tc.kind {
			t.Fatalf("%d is %q, want %q", tc.status, got, tc.kind)
		}
		a.HandleUsage(context.Background(), failed(now.Add(-time.Minute), tc.status))
	}
	// A refused token count is a failure of the account too.
	refused := failed(now.Add(-time.Second), 401)
	refused.Generate = &no
	a.HandleUsage(context.Background(), refused)
	for _, first := range []struct {
		at     time.Time
		stream bool
		ttft   time.Duration
	}{{now.Add(-2 * time.Hour), true, 5 * time.Millisecond}, {now, true, 300 * time.Millisecond}, {now, true, 100 * time.Millisecond}, {now, true, 900 * time.Millisecond}, {now, false, time.Millisecond}, {now, true, 0}} {
		a.HandleUsage(context.Background(), coreusage.Record{AuthID: "account", Model: "opus", RequestedAt: first.at, Stream: first.stream, TTFT: first.ttft})
	}
	// A 499 is the user stopping the agent: it is not counted, and never the last failure.
	a.HandleUsage(context.Background(), failed(now, 499))
	want := fmt.Sprintf(`{"health":{"lastFailure":{"kind":"sign-in","at":%q,"status":401},"failures":{"limit":1,"signIn":3,"server":3,"other":2},"firstWordMs":300}}`, now.Add(-time.Second).UTC().Format(time.RFC3339))
	if got := reported(t, a, "account", now); !reflect.DeepEqual(got, parsed(t, want, nil)) {
		t.Fatalf("got %v\nwant %s", got, want)
	}
	// Three hours on, only the last failure is still told.
	later := reported(t, a, "account", now.Add(4*time.Hour))["health"].(map[string]any)
	if len(later) != 1 || later["lastFailure"] == nil {
		t.Fatalf("later: %v", later)
	}
}

// metered reports what a request used as the SDK's own providers do: from
// inside the request, with the context the SDK ran it in.
type metered struct {
	*fakeProvider
	publish func(context.Context, coreusage.Record)
	headers chan http.Header
}

func (p metered) Execute(ctx context.Context, a *coreauth.Auth, r executor.Request, o executor.Options) (executor.Response, error) {
	p.headers <- o.Headers.Clone()
	p.publish(ctx, coreusage.Record{AuthID: a.ID, Provider: p.name, Model: r.Model, RequestedAt: time.Now(), Detail: coreusage.Detail{TotalTokens: 7}})
	return p.fakeProvider.Execute(ctx, a, r, o)
}

// A usage record is handled after its request and carries none of its headers;
// the started SDK must still say which session each one belongs to.
func TestStartedSDKNamesTheSessionOfEachUsageRecord(t *testing.T) {
	root, port := t.TempDir(), freePort(t)
	routes, counted := openTestRoutes(t), openTestActivity(t)
	if err := routes.Apply([]Route{route("session-a", "codex", "account-a"), route("session-b", "codex", "account-b")}, []string{"account-a", "account-b"}); err != nil {
		t.Fatal(err)
	}
	// The SDK's own dispatcher stops for good with the first service a test run stops.
	dispatcher := coreusage.NewManager(0)
	dispatcher.Register(counted)
	t.Cleanup(dispatcher.Stop)
	boundary := Boundary{Routes: routes, ControlKey: control, InferenceKey: inference, Logins: newLogins(filepath.Join(root, "auth")), Activity: counted}
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
		WithServerOptions(proxyapi.WithEngineConfigurator(func(engine *gin.Engine) { engine.Use(boundary.Middleware) }), proxyapi.WithMiddleware(counted.bind), proxyapi.WithRouterConfigurator(func(engine *gin.Engine, h *handlers.BaseAPIHandler, cfg *config.Config) {
			boundary.Configure(engine, h, cfg)
			manager = h.AuthManager
		})).Build()
	if err != nil {
		t.Fatal(err)
	}
	call, _ := running(t, service, port)
	provider := metered{fakeProvider: &fakeProvider{name: "codex"}, publish: dispatcher.Publish, headers: make(chan http.Header, 3)}
	manager.RegisterExecutor(provider)
	for _, id := range []string{"account-a", "account-b"} {
		if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive}); err != nil {
			t.Fatal(err)
		}
		cliproxy.GlobalModelRegistry().RegisterClient(id, "codex", []*cliproxy.ModelInfo{{ID: testModel}})
		t.Cleanup(func() { cliproxy.GlobalModelRegistry().UnregisterClient(id) })
	}
	for _, ticket := range []string{"session-a", "session-b", "session-a"} {
		if status, body := call(http.MethodPost, "/v1/responses", ticket, `{"model":"`+testModel+`","input":"hi"}`); status != http.StatusOK {
			t.Fatalf("%s: %d %s", ticket, status, body)
		}
		// Nothing of the session is handed on for the provider to see.
		if seen := fmt.Sprint(<-provider.headers); strings.Contains(seen, TicketHash(ticket)) || strings.Contains(seen, ticket) {
			t.Fatalf("%s: the provider was handed the session: %s", ticket, seen)
		}
	}
	for account, want := range map[string]string{"account-a": fmt.Sprintf(`{%q:14}`, TicketHash("session-a")), "account-b": fmt.Sprintf(`{%q:7}`, TicketHash("session-b"))} {
		eventually(t, account+" to be counted", func() bool {
			status, body := call(http.MethodPost, "/ao/account-state", control, `{"auth_id":"`+account+`","provider":"codex"}`)
			return status == http.StatusOK && strings.Contains(body, `"sessions":`+want)
		})
	}
}
