package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	filestore "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// signInProvider scripts the sign-in provider's answers by path and records
// what it was sent.
type signInProvider struct {
	mu      sync.Mutex
	answers map[string][]string // per path: "status body", the last one repeating
	sent    []string            // "path body"
}

func (p *signInProvider) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, r.URL.Path+" "+string(body))
	script := p.answers[r.URL.Path]
	if len(script) == 0 {
		return nil, errors.New("sign-in provider is out of reach")
	}
	if len(script) > 1 {
		p.answers[r.URL.Path] = script[1:]
	}
	status, answer, _ := strings.Cut(script[0], " ")
	code := map[string]int{"200": 200, "400": 400, "401": 401, "403": 403, "404": 404, "429": 429, "500": 500, "503": 503}[status]
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(answer)), Header: http.Header{}, Request: r}, nil
}
func (p *signInProvider) calls(path string) (bodies []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, sent := range p.sent {
		if at, body, _ := strings.Cut(sent, " "); at == path || path == "" {
			bodies = append(bodies, body)
		}
	}
	return bodies
}

const (
	usercodePath = "/api/accounts/deviceauth/usercode"
	pollPath     = "/api/accounts/deviceauth/token"
	tokenPath    = "/oauth/token"
)

func deviceFixture(t *testing.T, answers map[string][]string) (*fixture, *signInProvider) {
	t.Helper()
	f, _ := bareFixture(t)
	provider := &signInProvider{answers: answers}
	f.logins.client.Transport = provider
	return f, provider
}
func (f *fixture) login(method, id string) (int, string) {
	path, body := "/ao/login/status?id="+url.QueryEscape(id), ""
	if method == http.MethodPost {
		path, body = "/ao/login/device/start", string(mustJSON(map[string]string{"id": id}))
	}
	response := f.send(method, path, control, body)
	return response.Code, strings.TrimSpace(response.Body.String())
}
func eventually(t *testing.T, what string, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !done(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestDeviceSignInShowsOnlyTheCodeAndStopsWhenCancelled(t *testing.T) {
	f, provider := deviceFixture(t, map[string][]string{
		usercodePath: {`200 {"device_auth_id":"private-device-id","user_code":"ABCD-EFGH","interval":60}`},
		pollPath:     {`403 {"error":"authorization_pending"}`},
	})
	waiting := `{"id":"attempt","provider":"codex","mode":"device","status":"waiting","url":"` + devicePage + `","code":"ABCD-EFGH"}`
	if code, body := f.login(http.MethodPost, "attempt"); code != http.StatusOK || body != waiting {
		t.Fatalf("start: %d %s", code, body)
	}
	if code, body := f.login(http.MethodGet, "attempt"); code != http.StatusOK || body != waiting {
		t.Fatalf("status: %d %s", code, body)
	}
	if asked := provider.calls(usercodePath); len(asked) != 1 || asked[0] != `{"client_id":"`+deviceClientID+`"}` {
		t.Fatalf("the provider was asked for a code with %v", asked)
	}
	eventually(t, "the first poll", func() bool { return len(provider.calls(pollPath)) == 1 })
	if poll := provider.calls(pollPath)[0]; poll != `{"device_auth_id":"private-device-id","user_code":"ABCD-EFGH"}` {
		t.Fatalf("poll=%s", poll)
	}
	// A second start with the same id never reaches the provider.
	if code, _ := f.login(http.MethodPost, "attempt"); code != http.StatusBadRequest || len(provider.calls(usercodePath)) != 1 {
		t.Fatalf("duplicate start: %d", code)
	}
	if code, body := f.login(http.MethodDelete, "attempt"); code != http.StatusNoContent || body != "" {
		t.Fatalf("cancel: %d %s", code, body)
	}
	if _, body := f.login(http.MethodGet, "attempt"); body != strings.Replace(waiting, "waiting", "cancelled", 1) {
		t.Fatalf("after cancel: %s", body)
	}
	if code, _ := f.login(http.MethodGet, "unknown"); code != http.StatusNotFound {
		t.Fatalf("unknown login: %d", code)
	}
	if code, _ := f.login(http.MethodDelete, "unknown"); code != http.StatusNoContent {
		t.Fatalf("cancelling an unknown login: %d", code)
	}
}

func TestDeviceSignInRejectsUnsafeIDsBeforeCallingTheProvider(t *testing.T) {
	f, provider := deviceFixture(t, map[string][]string{usercodePath: {`200 {"device_auth_id":"d","user_code":"C"}`}})
	for _, id := range []string{"", "bad/id", `bad\id`, "bad.id", "..", " bad", "bad ", "bad\n", strings.Repeat("a", 129)} {
		if code, _ := f.login(http.MethodPost, id); code != http.StatusBadRequest {
			t.Fatalf("id %q: %d", id, code)
		}
	}
	if response := f.send(http.MethodPost, "/ao/login/device/start", control, "not json"); response.Code != http.StatusBadRequest {
		t.Fatalf("unreadable start: %d", response.Code)
	}
	if sent := provider.calls(""); len(sent) != 0 {
		t.Fatalf("the provider was called for a rejected id: %v", sent)
	}
	if code, _ := f.login(http.MethodPost, strings.Repeat("a", 128)); code != http.StatusOK {
		t.Fatalf("longest valid id: %d", code)
	}
	f.login(http.MethodDelete, strings.Repeat("a", 128))
}

func TestDeviceSignInStartFailsWithoutEchoingTheProvider(t *testing.T) {
	for _, answer := range []string{`200 {"user_code":"ABCD"}`, `200 {"device_auth_id":"private"}`, `200 {}`, `200 []`, `200 null`, `200 not-json`, `200 `,
		`400 {"access_token":"SECRET"}`, `401 {"access_token":"SECRET"}`, `403 {"access_token":"SECRET"}`, `404 SECRET`, `429 SECRET`, `500 SECRET`, `503 SECRET`, ""} {
		answers := map[string][]string{usercodePath: {answer}}
		if answer == "" {
			answers = nil
		}
		f, _ := deviceFixture(t, answers)
		if code, body := f.login(http.MethodPost, "attempt"); code != http.StatusBadGateway || body != "" {
			t.Fatalf("answer %q: %d %s", answer, code, body)
		}
		if code, _ := f.login(http.MethodGet, "attempt"); code != http.StatusNotFound {
			t.Fatalf("answer %q left a login behind", answer)
		}
	}
}

// signedInAnswers is a provider that lets the person in on the second poll.
func signedInAnswers(token string) map[string][]string {
	return map[string][]string{
		usercodePath: {`200 {"device_auth_id":"PRIVATE-DEVICE","usercode":"WXYZ","interval":"1"}`},
		pollPath:     {`403 {}`, `200 {"authorization_code":"AUTH-CODE","code_verifier":"VERIFIER","code_challenge":"CHALLENGE"}`},
		tokenPath:    {token},
	}
}

var deviceIDToken = idToken(`{"email":" person@example.test ","https://api.openai.com/auth":{"chatgpt_account_id":"acct-1","chatgpt_plan_type":"pro"}}`)

func TestDeviceSignInSavesAnOwnerOnlyCredentialTheSDKLoads(t *testing.T) {
	f, provider := deviceFixture(t, signedInAnswers(`200 {"access_token":"ACCESS","refresh_token":"REFRESH","id_token":"`+deviceIDToken+`","expires_in":3600}`))
	if code, body := f.login(http.MethodPost, "attempt"); code != http.StatusOK || !strings.Contains(body, `"code":"WXYZ"`) {
		t.Fatalf("start: %d %s", code, body)
	}
	file := filepath.Join(f.logins.authDir, "ao-attempt.json")
	eventually(t, "the credential file", func() bool { _, err := os.Stat(file); return err == nil })
	info, _ := os.Stat(file)
	data, _ := os.ReadFile(file)
	var saved map[string]string
	if err := json.Unmarshal(data, &saved); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode=%v err=%v", info.Mode(), err)
	}
	for key, want := range map[string]string{"type": "codex", "access_token": "ACCESS", "refresh_token": "REFRESH", "id_token": deviceIDToken, "email": "person@example.test", "account_id": "acct-1", "plan_type": "pro"} {
		if saved[key] != want {
			t.Fatalf("saved %s=%q, want %q", key, saved[key], want)
		}
	}
	refreshed, _ := time.Parse(time.RFC3339, saved["last_refresh"])
	expires, _ := time.Parse(time.RFC3339, saved["expired"])
	if time.Since(refreshed) > time.Minute || expires.Sub(refreshed) != time.Hour {
		t.Fatalf("last_refresh=%s expired=%s", saved["last_refresh"], saved["expired"])
	}
	exchange, _ := url.ParseQuery(provider.calls(tokenPath)[0])
	if exchange.Get("grant_type") != "authorization_code" || exchange.Get("code") != "AUTH-CODE" || exchange.Get("code_verifier") != "VERIFIER" || exchange.Get("client_id") != deviceClientID || exchange.Get("redirect_uri") != deviceRedirect {
		t.Fatalf("token exchange=%v", exchange)
	}
	// The login is complete only once the SDK holds the credential.
	if _, body := f.login(http.MethodGet, "attempt"); !strings.Contains(body, `"status":"waiting"`) {
		t.Fatalf("before the SDK loaded the file: %s", body)
	}
	f.account(&coreauth.Auth{ID: "loaded", FileName: "ao-attempt.json", Provider: "codex", Metadata: map[string]any{"email": saved["email"]}})
	eventually(t, "completion", func() bool {
		_, body := f.login(http.MethodGet, "attempt")
		return strings.Contains(body, `"status":"complete"`)
	})
	for _, secret := range []string{"ACCESS", "REFRESH", "PRIVATE-DEVICE", "AUTH-CODE", "VERIFIER"} {
		if _, body := f.login(http.MethodGet, "attempt"); strings.Contains(body, secret) {
			t.Fatalf("the login shows %s: %s", secret, body)
		}
	}
	want := `{"auth_id":"loaded","credential_ref":"ao-attempt.json","email":"person@example.test","kind":"oauth","provider":"codex"}`
	if response := f.send(http.MethodGet, "/ao/login-result/attempt", control, ""); strings.TrimSpace(response.Body.String()) != want {
		t.Fatalf("login result: %s", response.Body.String())
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("a completed login lost its credential: %v", err)
	}
}

func TestDeviceSignInThatDoesNotCompleteLeavesNoCredential(t *testing.T) {
	good := `200 {"access_token":"ACCESS","refresh_token":"REFRESH","id_token":"` + deviceIDToken + `"}`
	for name, tc := range map[string]struct{ token, status string }{
		"cancelled before the SDK loaded it": {good, "cancelled"},
		"no refresh token":                   {`200 {"access_token":"ACCESS","id_token":"` + deviceIDToken + `"}`, "failed"},
		"no identity":                        {`200 {"access_token":"ACCESS","refresh_token":"REFRESH","id_token":"a.b.c"}`, "failed"},
		"exchange refused":                   {`400 {"error":"SECRET"}`, "failed"},
		"exchange unreadable":                {`200 not-json`, "failed"},
	} {
		t.Run(name, func(t *testing.T) {
			answers := signedInAnswers(tc.token)
			answers[pollPath] = answers[pollPath][1:]
			f, provider := deviceFixture(t, answers)
			f.login(http.MethodPost, "attempt")
			file := filepath.Join(f.logins.authDir, "ao-attempt.json")
			if tc.status == "cancelled" {
				eventually(t, "the credential file", func() bool { _, err := os.Stat(file); return err == nil })
				f.login(http.MethodDelete, "attempt")
			}
			eventually(t, tc.status, func() bool {
				_, body := f.login(http.MethodGet, "attempt")
				return strings.Contains(body, `"status":"`+tc.status+`"`)
			})
			eventually(t, "the credential to go", func() bool { _, err := os.Stat(file); return errors.Is(err, os.ErrNotExist) })
			if _, body := f.login(http.MethodGet, "attempt"); strings.Contains(body, "SECRET") || len(provider.calls(pollPath)) != 1 {
				t.Fatalf("status=%s polls=%d", body, len(provider.calls(pollPath)))
			}
		})
	}
	// A provider that stops answering the poll with "not yet" ends the login.
	f, _ := deviceFixture(t, map[string][]string{usercodePath: {`200 {"device_auth_id":"d","user_code":"C"}`}, pollPath: {`500 SECRET`}})
	f.login(http.MethodPost, "attempt")
	eventually(t, "failure", func() bool {
		_, body := f.login(http.MethodGet, "attempt")
		return strings.Contains(body, `"status":"failed"`)
	})
}

func TestLoginTagComesOnlyFromThePrivateHeaderAndSurvivesPersistence(t *testing.T) {
	tagged := func(id string) context.Context {
		header := http.Header{}
		header.Set("X-AO-Login-ID", id)
		return coreauth.WithRequestInfo(context.Background(), &coreauth.RequestInfo{Headers: header})
	}
	for name, tc := range map[string]struct {
		ctx  context.Context
		want any
	}{
		"tagged":     {tagged("attempt-one"), "attempt-one"},
		"longest":    {tagged(strings.Repeat("a", 128)), strings.Repeat("a", 128)},
		"too long":   {tagged(strings.Repeat("a", 129)), nil},
		"no header":  {tagged(""), nil},
		"no request": {context.Background(), nil},
	} {
		auth := &coreauth.Auth{ID: "account", Provider: "codex", Metadata: map[string]any{"email": "alice@example.test", "access_token": "token"}}
		if err := tagLogin(tc.ctx, auth); err != nil || auth.Metadata["ao_login_id"] != tc.want || auth.Metadata["email"] != "alice@example.test" || auth.Metadata["access_token"] != "token" {
			t.Fatalf("%s: metadata=%v err=%v", name, auth.Metadata, err)
		}
	}
	// A credential that arrives with no fields of its own is tagged too, and the tag is saved with it.
	root := t.TempDir()
	store := filestore.NewFileTokenStore()
	store.SetBaseDir(root)
	auth := &coreauth.Auth{ID: "account.json", FileName: "account.json", Provider: "codex"}
	if err := tagLogin(tagged("browser-attempt"), auth); err != nil || auth.Metadata["ao_login_id"] != "browser-attempt" {
		t.Fatalf("metadata=%v err=%v", auth.Metadata, err)
	}
	auth.Metadata["type"], auth.Metadata["email"] = "codex", "alice@example.test"
	path, err := store.Save(context.Background(), auth)
	if info, statErr := os.Stat(path); err != nil || statErr != nil || info.Mode().Perm() != 0o600 || filepath.Dir(path) != root {
		t.Fatalf("saved to %s: %v %v", path, err, statErr)
	}
	reloaded, err := store.List(context.Background())
	if err != nil || len(reloaded) != 1 || reloaded[0].Metadata["ao_login_id"] != "browser-attempt" || reloaded[0].Provider != "codex" {
		t.Fatalf("reloaded=%+v err=%v", reloaded, err)
	}
}

func TestLoginResultAsksTheProviderWhoseThisComputersClaudeLoginIs(t *testing.T) {
	profile := `{"account":{"email":" native@example.test ","has_claude_max":true},"organization":{"name":"Org"}}`
	provider := &fakeProvider{name: "claude", answer: func(r *http.Request, _ string) (int, string) {
		if r.Method != http.MethodGet || r.URL.String() != "https://api.anthropic.com/api/oauth/profile" || r.Header.Get("anthropic-beta") == "" {
			return http.StatusNotFound, ""
		}
		return http.StatusOK, profile
	}}
	f, _ := bareFixture(t, provider)
	f.account(&coreauth.Auth{ID: "native", FileName: "ao-native-1.json", Provider: "claude"})
	f.account(&coreauth.Auth{ID: "pasted", FileName: "ao-pasted.json", Provider: "claude"})
	result := func(id string) (int, string) {
		response := f.send(http.MethodGet, "/ao/login-result/"+id, control, "")
		return response.Code, strings.TrimSpace(response.Body.String())
	}
	if code, body := result("native-1"); code != http.StatusOK || body != `{"auth_id":"native","credential_ref":"ao-native-1.json","email":"native@example.test","kind":"oauth","provider":"claude"}` {
		t.Fatalf("native login: %d %s", code, body)
	}
	// Only this computer's own login is looked up; a pasted credential must name its owner itself.
	if code, body := result("pasted"); code != http.StatusOK || !strings.Contains(body, `"email":""`) || len(provider.requests) != 1 {
		t.Fatalf("pasted login: %d %s after %d calls", code, body, len(provider.requests))
	}
	for _, unusable := range []string{`{"account":{}}`, `not json`, `{"account":{"email_address":"  "}}`} {
		profile = unusable
		if code, body := result("native-1"); code != http.StatusBadGateway || body != "" {
			t.Fatalf("profile %s: %d %s", unusable, code, body)
		}
	}
	profile = `{"account":{"email_address":"other@example.test"}}`
	if _, body := result("native-1"); !strings.Contains(body, `"email":"other@example.test"`) {
		t.Fatalf("email_address: %s", body)
	}
}

func TestJWTClaimsReadOnlyAWellFormedPayload(t *testing.T) {
	claims := jwtClaims(idToken(`{"email":"person@example.test","nested":{"value":"ok"}}`))
	if nested, _ := claims["nested"].(map[string]any); claims["email"] != "person@example.test" || nested["value"] != "ok" {
		t.Fatalf("claims=%v", claims)
	}
	for _, token := range []string{"", "one", "one.two", "one.two.three.four", "one.!@#.three", idToken("not-json"), idToken(`"text"`)} {
		if got := jwtClaims(token); got != nil {
			t.Fatalf("jwtClaims(%q)=%v", token, got)
		}
	}
}
