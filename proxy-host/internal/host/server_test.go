package host

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	proxycore "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// echoFixture puts a stand-in behind the boundary that reports what reached it.
func echoFixture(t *testing.T) *fixture {
	t.Helper()
	f, _ := bareFixture(t)
	f.route("codex-ticket codex alice", "claude-ticket claude bob")
	echo := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"auth": c.GetHeader(accountHeader), "provider": c.GetHeader(providerHeader), "authorization": c.GetHeader("Authorization"), "api_key": c.GetHeader("X-Api-Key"), "query": c.Request.URL.RawQuery})
	}
	for path := range sessionPaths {
		f.engine.Any(path, echo)
	}
	f.engine.Any("/v8/management/*path", echo)
	f.engine.Any("/v0/management/*path", echo)
	return f
}

func TestControlKeyAndSessionTicketsOpenDifferentDoors(t *testing.T) {
	f := echoFixture(t)
	private := []string{"GET /ao/status", "PUT /ao/routes", "POST /ao/account-models", "POST /ao/account-state", "POST /ao/provider-call", "POST /ao/account-resume",
		"POST /ao/account-refresh", "GET /ao/login-result/attempt", "POST /ao/login/device/start", "GET /ao/login/status", "DELETE /ao/login/status", "POST /ao/tag-api-key",
		"GET /v8/management/credentials", "GET /v8/management/oauth/status", "GET /v0/management/codex-api-key", "GET /v0/management/config"}
	for _, token := range []string{"", "unknown", "codex-ticket", "claude-ticket", inference, control + "x", control[1:]} {
		for _, call := range private {
			method, path, _ := strings.Cut(call, " ")
			response := f.send(method, path, token, `{"routes":[],"auth_ids":[]}`, "X-Api-Key: "+control)
			if response.Code != http.StatusUnauthorized || response.Body.Len() != 0 {
				t.Fatalf("%s with token %q: %d %s", call, token, response.Code, response.Body.String())
			}
		}
	}
	if admitted(f.routes, "codex-ticket") != "alice" {
		t.Fatal("a caller without the control key changed the routes")
	}
	for _, token := range []string{"", "unknown", control, inference} {
		for path := range sessionPaths {
			if response := f.send(http.MethodPost, path, token, ""); response.Code != http.StatusUnauthorized {
				t.Fatalf("%s with token %q: %d", path, token, response.Code)
			}
		}
	}
	if response := f.send(http.MethodGet, "/ao/status", control, ""); response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"protocol_version":3}` {
		t.Fatalf("status: %d %s", response.Code, response.Body.String())
	}
}

func TestManagementAllowlist(t *testing.T) {
	f := echoFixture(t)
	allowed := map[string]bool{}
	for _, call := range []string{"GET /v8/management/credentials", "POST /v8/management/credentials", "DELETE /v8/management/credentials", "GET /v8/management/oauth/auth-url",
		"GET /v8/management/oauth/status", "POST /v8/management/oauth/callback", "DELETE /v8/management/oauth/session", "GET /v0/management/codex-api-key",
		"PUT /v0/management/codex-api-key", "DELETE /v0/management/codex-api-key", "GET /v0/management/claude-api-key", "PUT /v0/management/claude-api-key", "DELETE /v0/management/claude-api-key"} {
		allowed[call] = true
	}
	if len(allowed) != len(management) {
		t.Fatalf("the allowlist has %d entries, want %d", len(management), len(allowed))
	}
	paths := []string{"credentials", "credentials/status", "credentials/refresh", "oauth/auth-url", "oauth/status", "oauth/callback", "oauth/session", "config", "auth-files", "api-keys",
		"routing/strategy", "routing/cooldown/reset", "quota/fetch", "quota/reset", "api-call", "codex-api-key", "claude-api-key", "gemini-api-key"}
	for _, version := range []string{"/v8/management/", "/v0/management/"} {
		for _, path := range paths {
			for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
				want := http.StatusNotFound
				if allowed[method+" "+version+path] {
					want = http.StatusOK
				}
				if response := f.send(method, version+path+"?name=account.json", control, "{}"); response.Code != want {
					t.Fatalf("%s %s%s: %d, want %d", method, version, path, response.Code, want)
				}
			}
		}
	}
	// The SDK's own OAuth forwarder would bind every interface; AO never lets it be asked for.
	response := f.send(http.MethodGet, "/v8/management/oauth/auth-url?provider=codex&is_webui=true", control, "")
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "is_webui") || !strings.Contains(response.Body.String(), "provider=codex") {
		t.Fatalf("auth-url query reached the SDK as %s", response.Body.String())
	}
}

func TestBoundaryPinsTheSessionAccountOverSpoofedHeaders(t *testing.T) {
	f := echoFixture(t)
	for _, tc := range []struct{ path, ticket, account, provider string }{
		{"/v1/responses", "codex-ticket", "alice", "codex"},
		{"/v1/responses/compact", "codex-ticket", "alice", "codex"},
		{"/v1/messages", "claude-ticket", "bob", "claude"},
		{"/v1/messages/count_tokens", "claude-ticket", "bob", "claude"},
		{"/v1/models?client_version=1", "codex-ticket", "alice", "codex"},
		{"/v1/models?client_version=1", "claude-ticket", "bob", "claude"},
	} {
		for _, viaAPIKey := range []bool{false, true} {
			headers := []string{accountHeader + ": spoof", providerHeader + ": spoof", "X-Api-Key: spoof"}
			token := tc.ticket
			if viaAPIKey {
				token, headers[2] = "", "X-Api-Key: "+tc.ticket
			}
			response := f.send(http.MethodPost, tc.path, token, "", headers...)
			var got map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || response.Code != http.StatusOK {
				t.Fatalf("%s: %d %s", tc.path, response.Code, response.Body.String())
			}
			if got["auth"] != tc.account || got["provider"] != tc.provider || got["authorization"] != "Bearer "+inference || got["api_key"] != "" {
				t.Fatalf("%s reached the SDK with %v", tc.path, got)
			}
		}
	}
}

func TestSessionsReachOnlyTheirOwnProvidersInferencePaths(t *testing.T) {
	f := echoFixture(t)
	for _, tc := range []struct{ path, ticket string }{{"/v1/responses", "claude-ticket"}, {"/v1/responses/compact", "claude-ticket"}, {"/v1/messages", "codex-ticket"}, {"/v1/messages/count_tokens", "codex-ticket"}} {
		if response := f.send(http.MethodPost, tc.path, tc.ticket, ""); response.Code != http.StatusBadRequest {
			t.Fatalf("%s with %s: %d", tc.path, tc.ticket, response.Code)
		}
	}
	if response := f.send(http.MethodGet, "/v1/responses", "codex-ticket", "", "Upgrade: websocket"); response.Code != http.StatusNotImplemented {
		t.Fatalf("websocket: %d", response.Code)
	}
	for _, path := range []string{"/", "/management.html", "/oauth/callback", "/v1/chat/completions", "/v1/images/generations", "/v1/models/gpt", "/redis", "/v1/../ao/status"} {
		if response := f.send(http.MethodGet, path, "codex-ticket", ""); response.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, response.Code)
		}
	}
	// A session ticket is not the control key, so the private surface does not say what exists.
	if response := f.send(http.MethodGet, "/v0/management/auth-files", "codex-ticket", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("management path with a ticket: %d", response.Code)
	}
}

func TestRoutesAPIReplacesTheTableOrRefusesWhole(t *testing.T) {
	f := echoFixture(t)
	push := func(routes, ids string) (int, string) {
		response := f.send(http.MethodPut, "/ao/routes", control, `{"routes":[`+routes+`],"auth_ids":[`+ids+`]}`)
		return response.Code, response.Body.String()
	}
	entry := func(ticket, account string) string {
		return fmt.Sprintf(`{"ticket_hash":%q,"provider":"codex","auth_id":%q}`, TicketHash(ticket), account)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if code, body := push(entry("codex-ticket", "carol")+","+entry("new-ticket", "alice"), `"alice","carol"`); code != http.StatusNoContent || body != "" {
			t.Fatalf("push %d: %d %s", attempt, code, body)
		}
	}
	if admitted(f.routes, "codex-ticket") != "carol" || admitted(f.routes, "new-ticket") != "alice" || admitted(f.routes, "claude-ticket") != "" {
		t.Fatal("the push did not replace the table")
	}
	_, release, _ := f.routes.Acquire("codex-ticket")
	if code, body := push(entry("codex-ticket", "alice"), `"alice"`); code != http.StatusConflict || strings.TrimSpace(body) != `{"code":"SESSION_BUSY"}` {
		t.Fatalf("busy push: %d %s", code, body)
	}
	if admitted(f.routes, "codex-ticket") != "carol" {
		t.Fatal("a refused push changed the table")
	}
	release()
	if code, _ := push("", ""); code != http.StatusNoContent || admitted(f.routes, "codex-ticket") != "" {
		t.Fatalf("empty push: %d", code)
	}
	// An empty table may arrive as null lists or with the lists left out.
	for _, body := range []string{`{"routes":null,"auth_ids":null}`, `{}`} {
		f.route("codex-ticket codex alice")
		if response := f.send(http.MethodPut, "/ao/routes", control, body); response.Code != http.StatusNoContent || admitted(f.routes, "codex-ticket") != "" {
			t.Fatalf("body %s: %d", body, response.Code)
		}
	}
	for _, body := range []string{"{invalid", "", `{"routes":"all"}`} {
		if response := f.send(http.MethodPut, "/ao/routes", control, body); response.Code != http.StatusBadRequest {
			t.Fatalf("body %q: %d", body, response.Code)
		}
	}
}

// modelsFixture answers /v1/models as the SDK does: every account's models,
// with other providers' ones under disguised names.
func modelsFixture(t *testing.T, status int, answer string) *fixture {
	t.Helper()
	f, _ := bareFixture(t)
	f.route("codex-ticket codex alice", "claude-ticket claude bob")
	registry := proxycore.GlobalModelRegistry()
	registry.RegisterClient("bob", "claude", []*proxycore.ModelInfo{{ID: "claude-one"}, {ID: "claude-two"}})
	registry.RegisterClient("alice", "codex", []*proxycore.ModelInfo{{ID: "gpt-one"}})
	t.Cleanup(func() { registry.UnregisterClient("bob"); registry.UnregisterClient("alice") })
	f.engine.GET("/v1/models", func(c *gin.Context) { c.Data(status, "application/json", []byte(answer)) })
	return f
}

func TestSessionModelListNamesOnlyTheSessionAccountsModels(t *testing.T) {
	everything := `{"data":[{"type":"model","id":"claude-one","display_name":"One"},{"type":"model","id":"claude-fable-5-dd-eno-tpg"},{"type":"model","id":"claude-two"},{"type":"model","id":"gpt-one"},{"type":"model","id":"claude-other-account"}],"has_more":false}`
	f := modelsFixture(t, http.StatusOK, everything)
	for ticket, want := range map[string]string{"claude-ticket": "claude-one,claude-two", "codex-ticket": "gpt-one"} {
		response := f.send(http.MethodGet, "/v1/models?limit=1000", ticket, "")
		var body struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
			HasMore *bool `json:"has_more"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusOK || body.HasMore == nil {
			t.Fatalf("%s: %d %s", ticket, response.Code, response.Body.String())
		}
		ids := []string{}
		for _, model := range body.Data {
			ids = append(ids, model.ID)
		}
		if strings.Join(ids, ",") != want || (ticket == "claude-ticket" && body.Data[0].DisplayName != "One") {
			t.Fatalf("%s was offered %v, want %s", ticket, body.Data, want)
		}
	}
}

func TestSessionModelListLeavesOtherAnswersAlone(t *testing.T) {
	for name, tc := range map[string]struct {
		status       int
		answer, path string
	}{
		"Codex's own catalogue": {http.StatusOK, `{"models":[{"slug":"gpt-other-account"}],"data":[{"id":"gpt-other-account"}]}`, "/v1/models?client_version=1.2.3"},
		"an error":              {http.StatusServiceUnavailable, `{"data":[{"id":"gpt-other-account"}]}`, "/v1/models"},
		"not a model list":      {http.StatusOK, `{"object":"list"}`, "/v1/models"},
		"not JSON":              {http.StatusOK, `plain text`, "/v1/models"},
	} {
		f := modelsFixture(t, tc.status, tc.answer)
		if response := f.send(http.MethodGet, tc.path, "codex-ticket", ""); response.Code != tc.status || response.Body.String() != tc.answer {
			t.Fatalf("%s: %d %s", name, response.Code, response.Body.String())
		}
	}
}

func TestAccountModelsListsTheRequestedAccountsModelsWithEfforts(t *testing.T) {
	f, _ := bareFixture(t)
	for _, id := range []string{"model-auth", "empty-auth"} {
		if _, err := f.manager.Register(coreauth.WithSkipPersist(t.Context()), &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive}); err != nil {
			t.Fatal(err)
		}
	}
	// Decoded rather than built, because the reasoning metadata type is internal to the SDK.
	var withLevels proxycore.ModelInfo
	if err := json.Unmarshal([]byte(`{"id":"account-gpt","display_name":" Account GPT ","type":"codex","thinking":{"levels":["low","medium","high"]}}`), &withLevels); err != nil {
		t.Fatal(err)
	}
	registry := proxycore.GlobalModelRegistry()
	registry.RegisterClient("model-auth", "codex", []*proxycore.ModelInfo{&withLevels, {ID: "account-plain", Type: "codex"}})
	registry.RegisterClient("other-auth", "codex", []*proxycore.ModelInfo{{ID: "other-accounts-model"}})
	t.Cleanup(func() { registry.UnregisterClient("model-auth"); registry.UnregisterClient("other-auth") })

	response := f.send(http.MethodPost, "/ao/account-models", control, `{"auth_id":"model-auth","provider":"codex"}`)
	want := `{"models":[{"efforts":["low","medium","high"],"id":"account-gpt","label":"Account GPT","provider":"codex"},{"id":"account-plain","label":"account-plain","provider":"codex"}]}`
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != want {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	for body, status := range map[string]int{
		`{"auth_id":"empty-auth","provider":"codex"}`:  http.StatusServiceUnavailable,
		`{"auth_id":"model-auth","provider":"claude"}`: http.StatusNotFound,
		`{"auth_id":"missing","provider":"codex"}`:     http.StatusNotFound,
		`{"provider":"codex"}`:                         http.StatusNotFound,
		`not json`:                                     http.StatusBadRequest,
	} {
		if response := f.send(http.MethodPost, "/ao/account-models", control, body); response.Code != status {
			t.Fatalf("%s: %d, want %d", body, response.Code, status)
		}
	}
}

func TestLoginResultNamesTheCredentialOfOneLoginAndNothingSecret(t *testing.T) {
	f, _ := bareFixture(t)
	for _, auth := range []*coreauth.Auth{
		{ID: "other", FileName: "other.json", Provider: "codex", Metadata: map[string]any{"ao_login_id": "other-attempt", "email": "other@example.test", "access_token": "PRIVATE-OTHER"}},
		{ID: "wanted", FileName: "wanted.json", Provider: "claude", Metadata: map[string]any{"ao_login_id": "wanted-attempt", "email": "wanted@example.test", "access_token": "PRIVATE-ACCESS", "refresh_token": "PRIVATE-REFRESH"}},
		{ID: "ao-import-attempt.json", FileName: "ao-import-attempt.json", Provider: "codex", Metadata: map[string]any{"email": "imported@example.test"}},
	} {
		f.account(auth)
	}
	for id, want := range map[string]string{
		"wanted-attempt": `{"auth_id":"wanted","credential_ref":"wanted.json","email":"wanted@example.test","kind":"oauth","provider":"claude"}`,
		"import-attempt": `{"auth_id":"ao-import-attempt.json","credential_ref":"ao-import-attempt.json","email":"imported@example.test","kind":"oauth","provider":"codex"}`,
	} {
		if response := f.send(http.MethodGet, "/ao/login-result/"+id, control, ""); response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != want {
			t.Fatalf("%s: %d %s", id, response.Code, response.Body.String())
		}
	}
	for _, id := range []string{"unknown", "wanted", "wanted.json", "attempt"} {
		if response := f.send(http.MethodGet, "/ao/login-result/"+id, control, ""); response.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", id, response.Code)
		}
	}
}

func TestTaggedAPIKeyIsReportedAsAnAPIKey(t *testing.T) {
	f, _ := bareFixture(t)
	f.account(&coreauth.Auth{ID: "key-auth", Provider: "codex", Attributes: map[string]string{"api_key": "secret", "base_url": "https://api.example/"}})
	f.account(&coreauth.Auth{ID: "signed-in", Provider: "codex", Metadata: map[string]any{"email": "person@example.test"}})
	tag := func(body string) (int, string) {
		response := f.send(http.MethodPost, "/ao/tag-api-key", control, body)
		return response.Code, strings.TrimSpace(response.Body.String())
	}
	if code, body := tag(`{"id":"login-1","provider":"codex","api_key":"secret","base_url":"https://api.example","label":" Work key "}`); code != http.StatusOK || body != `{"auth_id":"key-auth"}` {
		t.Fatalf("tag: %d %s", code, body)
	}
	response := f.send(http.MethodGet, "/ao/login-result/login-1", control, "")
	var verified struct {
		Provider, Email, Kind string
		CredentialRef         string `json:"credential_ref"`
		AuthID                string `json:"auth_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &verified); err != nil || response.Code != http.StatusOK {
		t.Fatalf("login result: %d %s", response.Code, response.Body.String())
	}
	if verified.Kind != "api_key" || verified.Email != "Work key" || verified.AuthID != "key-auth" || verified.Provider != "codex" || !strings.HasPrefix(verified.CredentialRef, "config-index:codex:") || len(verified.CredentialRef) == len("config-index:codex:") {
		t.Fatalf("verified=%+v", verified)
	}
	if strings.Contains(response.Body.String(), "secret") {
		t.Fatal("the login result carries the key")
	}
	// A key the helper does not hold is not found, and an empty key never matches a sign-in.
	short := func(body string) int {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		return sendContext(ctx, f.engine, http.MethodPost, "/ao/tag-api-key", control, body).Code
	}
	for _, body := range []string{`{"id":"login-2","provider":"codex","api_key":"other"}`, `{"id":"login-2","provider":"claude","api_key":"secret","base_url":"https://api.example"}`, `{"id":"login-2","provider":"codex","api_key":""}`} {
		if code := short(body); code != http.StatusNotFound {
			t.Fatalf("%s: %d", body, code)
		}
	}
	if signedIn, _ := f.manager.GetByID("signed-in"); signedIn.Metadata["ao_login_id"] != nil {
		t.Fatal("an empty key tagged a sign-in")
	}
	// A key just saved is waited for: the SDK loads it a moment after the save is answered.
	late := time.AfterFunc(100*time.Millisecond, func() {
		if _, err := f.manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{ID: "late-key", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "late"}}); err != nil {
			t.Error(err)
		}
	})
	defer late.Stop()
	if code, body := tag(`{"id":"login-3","provider":"codex","api_key":"late"}`); code != http.StatusOK || body != `{"auth_id":"late-key"}` {
		t.Fatalf("a key loaded after the call: %d %s", code, body)
	}
}
