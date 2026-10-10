package host

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const (
	deviceClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	devicePage     = "https://auth.openai.com/codex/device"
	deviceRedirect = "https://auth.openai.com/deviceauth/callback"
)

type deviceLogin struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Mode     string `json:"mode"`
	Status   string `json:"status"`
	URL      string `json:"url"`
	Code     string `json:"code"`
	cancel   context.CancelFunc
}

// Logins runs Codex device-code sign-ins; every other sign-in is the SDK's own.
type Logins struct {
	mu      sync.Mutex
	authDir string
	client  *http.Client
	auth    *coreauth.Manager
	ops     map[string]deviceLogin
}

func newLogins(authDir string) *Logins {
	return &Logins{authDir: authDir, client: &http.Client{Timeout: 15 * time.Second}, ops: map[string]deviceLogin{}}
}
func (l *Logins) status(id string) (int, any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if op, ok := l.ops[id]; ok {
		return http.StatusOK, op
	}
	return http.StatusNotFound, nil
}

// settle ends a login that is still waiting, and reports whether it was.
func (l *Logins) settle(id, status string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	op, ok := l.ops[id]
	if ok = ok && op.Status == "waiting"; ok {
		op.Status = status
		l.ops[id] = op
		op.cancel()
	}
	return ok
}
func (l *Logins) start(ctx context.Context, id string) (int, any) {
	// The id becomes part of a credential file name.
	if found, _ := l.status(id); found == http.StatusOK || id == "" || len(id) > 128 || strings.ContainsAny(id, "/\\.") || strings.TrimSpace(id) != id {
		return http.StatusBadRequest, nil
	}
	code, _ := l.post(ctx, "/api/accounts/deviceauth/usercode", "application/json", mustJSON(map[string]string{"client_id": deviceClientID}))
	poll := map[string]string{"device_auth_id": text(code["device_auth_id"]), "user_code": cmp.Or(text(code["user_code"]), text(code["usercode"]))}
	if poll["device_auth_id"] == "" || poll["user_code"] == "" {
		return http.StatusBadGateway, nil
	}
	interval, _ := strconv.Atoi(fmt.Sprint(code["interval"]))
	if interval < 1 || interval > 60 {
		interval = 5
	}
	worker, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	op := deviceLogin{ID: id, Provider: "codex", Mode: "device", Status: "waiting", URL: devicePage, Code: poll["user_code"], cancel: cancel}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ops[id] = op
	go l.run(worker, id, mustJSON(poll), time.Duration(interval)*time.Second)
	return http.StatusOK, op
}
func (l *Logins) run(ctx context.Context, id string, poll []byte, interval time.Duration) {
	defer l.settle(id, "failed")
	grant, status := l.post(ctx, "/api/accounts/deviceauth/token", "application/json", poll)
	// The provider answers 403 or 404 until the person has entered the code.
	for status == http.StatusForbidden || status == http.StatusNotFound {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		grant, status = l.post(ctx, "/api/accounts/deviceauth/token", "application/json", poll)
	}
	if status != http.StatusOK {
		return
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {deviceClientID}, "code": {text(grant["authorization_code"])}, "redirect_uri": {deviceRedirect}, "code_verifier": {text(grant["code_verifier"])}}
	token, _ := l.post(ctx, "/oauth/token", "application/x-www-form-urlencoded", []byte(form.Encode()))
	claims := jwtClaims(text(token["id_token"]))
	scope, _ := claims["https://api.openai.com/auth"].(map[string]any)
	expires, _ := token["expires_in"].(float64)
	now, name := time.Now().UTC(), "ao-"+id+".json"
	saved := map[string]string{"type": "codex", "access_token": text(token["access_token"]), "refresh_token": text(token["refresh_token"]), "id_token": text(token["id_token"]),
		"email": text(claims["email"]), "account_id": text(scope["chatgpt_account_id"]), "plan_type": text(scope["chatgpt_plan_type"]),
		"last_refresh": now.Format(time.RFC3339), "expired": now.Add(time.Duration(expires) * time.Second).Format(time.RFC3339)}
	file := filepath.Join(l.authDir, name)
	if saved["access_token"] == "" || saved["refresh_token"] == "" || saved["email"] == "" || ctx.Err() != nil || writePrivate(file, mustJSON(saved)) != nil {
		return
	}
	// The SDK loads the file by itself; a login cancelled meanwhile leaves no credential behind.
	_, err := waitAuth(ctx, l.auth, func(a *coreauth.Auth) bool { return a.Provider == "codex" && filepath.Base(a.FileName) == name })
	if err != nil || !l.settle(id, "complete") {
		_ = os.Remove(file)
	}
}
func waitAuth(ctx context.Context, manager *coreauth.Manager, match func(*coreauth.Auth) bool) (*coreauth.Auth, error) {
	for {
		for _, a := range manager.List() {
			if match(a) {
				return a, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// post sends one sign-in request: the provider's JSON object with status 200, or its failing status, or 0 without a usable answer.
func (l *Logins) post(ctx context.Context, path, contentType string, body []byte) (out map[string]any, status int) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://auth.openai.com"+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, 0
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, resp.StatusCode
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out) != nil {
		return nil, 0
	}
	return out, http.StatusOK
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func text(value any) string { s, _ := value.(string); return strings.TrimSpace(s) }

// jwtClaims reads a token's payload unverified: only ever a token the provider has just issued or the SDK saved.
func jwtClaims(token string) (claims map[string]any) {
	if parts := strings.Split(token, "."); len(parts) == 3 {
		payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
		_ = json.Unmarshal(payload, &claims)
	}
	return claims
}
