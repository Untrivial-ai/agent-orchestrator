# Connect Mobile — LAN Listener Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a physical phone use Agent Orchestrator over the local network through a second, on-demand, password-authenticated HTTP listener inside the daemon, without changing the existing loopback behaviour.

**Architecture:** The daemon keeps its `127.0.0.1` **Loopback Listener** exactly as today (desktop/CLI, unauthenticated). A new **LAN Listener** binds `0.0.0.0` only while "Connect Mobile" is enabled; it wraps the _same_ chi router in one extra `authMiddleware`. Auth is decided by _which socket the request arrived on_, not by inspecting the request. Transport is plaintext HTTP (home-network-only). The phone pairs by scanning a QR that carries only `host`+`port`, then types the rotating 8-char password (shown on the desktop) into a popup; the password rides as `Authorization: Bearer <pw>` on REST and the RN WebSocket.

> **Historical design note:** This plan records the original host/port-only, out-of-band-password design and has diverged from the shipped pairing contract. Current desktop builds emit a v2 pairing offer containing all advertised endpoints and the bearer connection password, and the mobile app completes pairing in one scan. Do not use the QR or password instructions below as current product guidance; see `frontend/src/landing/content/docs/configuration/remote-access.mdx` for the current workflow. The implementation record remains otherwise unchanged for historical context.

**Tech Stack:** Go (chi, coder/websocket), Electron + React + TanStack Router + shadcn/ui (typed daemon client), Expo/React Native (expo-camera, AsyncStorage).

## Global Constraints

- All state resolves under `~/.ao` (overridable via `AO_DATA_DIR`). Mobile state lives in `~/.ao/mobile/config.json`. Never touch `~/Library/Application Support`.
- The **Loopback Listener must remain byte-for-byte unchanged** — no auth, same bind, same routes. Zero desktop/CLI regression.
- Daemon API is code-first: edit `backend/internal/httpd/controllers/dto.go` + `backend/internal/httpd/apispec/specgen/build.go`, then run `npm run api` to regenerate the OpenAPI spec + frontend TS types. Never hand-edit generated artifacts.
- CLI stays a thin HTTP client; do not open storage/runtime directly.
- Renderer clones agent-orchestrator's look; build UI from `frontend/src/renderer/components/ui/*` primitives (per DESIGN.md).
- Password format: **8 chars, alphanumeric `[A-Za-z0-9]`**, generated with `crypto/rand`. Stored **hashed only** (SHA-256 hex is sufficient here — it is a rotating LAN enabler, not a human password; constant-time compare on the hash). Never persist the plaintext to disk.
- Auth scheme everywhere: `Authorization: Bearer <password>`.
- Default LAN port **3011**; ephemeral fallback if taken; the QR/status must always report the _actually-bound_ port.
- Lockout: **per-source** (remote IP), threshold **5** failures → cooldown; reset on success. Never global.
- Config file writes are **atomic** (temp + rename), like `runfile.Write`.

---

## File Structure

**Backend (Go)**

- `backend/internal/mobilebridge/config.go` — the `~/.ao/mobile/config.json` store (load/save/atomic), password gen + hash, state struct. _New package, no httpd deps._
- `backend/internal/mobilebridge/config_test.go`
- `backend/internal/mobilebridge/netiface.go` — autopick LAN IP + enumerate candidates.
- `backend/internal/mobilebridge/netiface_test.go`
- `backend/internal/httpd/auth.go` — `authMiddleware` + per-source `lockout` limiter + bearer extraction + constant-time check.
- `backend/internal/httpd/auth_test.go`
- `backend/internal/httpd/lan_listener.go` — `LANManager`: start/stop a second `http.Server` at runtime, report bound addr, own the shared router+auth wrap.
- `backend/internal/httpd/lan_listener_test.go`
- `backend/internal/httpd/controllers/mobile.go` — REST controller for `GET/POST /api/v1/mobile/...` (status, enable, disable, regenerate).
- `backend/internal/httpd/controllers/mobile_test.go`
- `backend/internal/httpd/controllers/dto.go` — **modify**: add mobile DTOs.
- `backend/internal/httpd/apispec/specgen/build.go` — **modify**: register mobile operations + schema names.
- `backend/internal/httpd/terminal_mux.go` — **modify**: no change to loopback path; auth for `/mux` is applied by the LAN router wrap (see Task 7), not here.
- `backend/internal/daemon/daemon.go` — **modify**: construct `LANManager`, wire it into the mobile controller, restore persisted enabled-state on boot.

**Desktop (Electron/React)**

- `frontend/src/renderer/components/ui/dialog.tsx` — **new** shadcn Dialog primitive (only `sheet.tsx` exists today).
- `frontend/src/renderer/components/ConnectMobileButton.tsx` — the "Connect Mobile" button that opens the modal.
- `frontend/src/renderer/components/ConnectMobileModal.tsx` — modal: enable/disable, QR, IP:port, password, regenerate, warning.
- `frontend/src/renderer/components/ConnectMobileModal.test.tsx`
- `frontend/src/renderer/components/GlobalSettingsForm.tsx` — **modify**: add `<ConnectMobileButton/>` section.
- `frontend/src/renderer/lib/qr.ts` — tiny QR-SVG generator (self-contained; no external host per CSP) or a vendored generator.

**Mobile (Expo)**

- `packages/mobile/lib/config.ts` — **modify**: add `password` to `ServerConfig`, derive auth header helper.
- `packages/mobile/lib/api.ts` — **modify**: attach `Authorization` header to every fetch.
- `packages/mobile/lib/mux.ts` — **modify**: attach `Authorization` header to the WebSocket via RN's `headers` option.
- `packages/mobile/lib/pairing.ts` — **new**: parse the scanned QR payload `{v,host,port}`.
- `packages/mobile/app/pair.tsx` — **new**: camera scanner screen (expo-camera).
- `packages/mobile/app/(tabs)/settings.tsx` — **modify**: "Scan QR" entry + password popup + manual host/port/password.
- `packages/mobile/package.json` / `app.json` — **modify**: add `expo-camera` + camera permission.

**Docs**

- `AGENTS.md` — **modify**: scope the loopback-only hard rule to the Loopback Listener.
- `docs/architecture.md` — **modify**: one paragraph on the two-listener model.

---

## PHASE 1 — Backend: config store & password

### Task 1: mobilebridge config store (state + atomic persistence)

**Files:**

- Create: `backend/internal/mobilebridge/config.go`
- Test: `backend/internal/mobilebridge/config_test.go`

**Interfaces:**

- Produces:
  - `type State struct { Enabled bool `json:"enabled"`; PasswordHash string `json:"passwordHash"`; LastPort int `json:"lastPort"` }`
  - `func Path(dataDir string) string` → `filepath.Join(dataDir, "mobile", "config.json")`
  - `func Load(path string) (State, error)` — missing file returns zero `State{}`, nil error.
  - `func Save(path string, s State) error` — atomic (temp+rename), `mkdir -p` the dir, file mode `0o600`.
  - `func GeneratePassword() (string, error)` — 8 chars from `[A-Za-z0-9]` via `crypto/rand`.
  - `func HashPassword(pw string) string` — `hex(sha256(pw))`.
  - `func PasswordMatches(hash, pw string) bool` — `subtle.ConstantTimeCompare` over the hex hashes.

- [ ] **Step 1: Write the failing test**

```go
package mobilebridge

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := Path(dir)
	want := State{Enabled: true, PasswordHash: "abc", LastPort: 3011}
	if err := Save(p, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != want {
		t.Fatalf("round trip: got %+v want %+v", got, want)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v want 0600", info.Mode().Perm())
	}
}

func TestLoadMissingIsZero(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "mobile", "config.json"))
	if err != nil || got != (State{}) {
		t.Fatalf("missing file: got %+v err %v", got, err)
	}
}

func TestGeneratePasswordFormat(t *testing.T) {
	pw, err := GeneratePassword()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9]{8}$`).MatchString(pw) {
		t.Fatalf("password %q not 8 alnum", pw)
	}
}

func TestPasswordMatches(t *testing.T) {
	pw, _ := GeneratePassword()
	h := HashPassword(pw)
	if !PasswordMatches(h, pw) {
		t.Fatal("expected match")
	}
	if PasswordMatches(h, pw+"x") {
		t.Fatal("expected mismatch")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/mobilebridge/ -run TestSaveLoad -v`
Expected: FAIL — package/functions do not exist.

- [ ] **Step 3: Write minimal implementation**

```go
// Package mobilebridge owns the durable state and helpers for the Connect
// Mobile LAN listener: the ~/.ao/mobile/config.json store and the rotating
// connection password. It has no httpd/daemon dependencies.
package mobilebridge

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type State struct {
	Enabled      bool   `json:"enabled"`
	PasswordHash string `json:"passwordHash"`
	LastPort     int    `json:"lastPort"`
}

func Path(dataDir string) string { return filepath.Join(dataDir, "mobile", "config.json") }

func Load(path string) (State, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read mobile config: %w", err)
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("parse mobile config: %w", err)
	}
	return s, nil
}

func Save(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir mobile dir: %w", err)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

const pwAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func GeneratePassword() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i, b := range buf {
		buf[i] = pwAlphabet[int(b)%len(pwAlphabet)]
	}
	return string(buf), nil
}

func HashPassword(pw string) string {
	sum := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(sum[:])
}

func PasswordMatches(hash, pw string) bool {
	return subtle.ConstantTimeCompare([]byte(hash), []byte(HashPassword(pw))) == 1
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/mobilebridge/ -v && go vet ./internal/mobilebridge/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/mobilebridge/config.go backend/internal/mobilebridge/config_test.go
git commit -m "feat(mobile): mobilebridge config store + rotating password"
```

---

### Task 2: Autopick LAN IP

**Files:**

- Create: `backend/internal/mobilebridge/netiface.go`
- Test: `backend/internal/mobilebridge/netiface_test.go`

**Interfaces:**

- Produces:
  - `func PrivateIPv4Candidates(ifaces []net.Interface, addrsOf func(net.Interface) ([]net.Addr, error)) []string` — pure, testable core; returns private, non-loopback, non-link-local IPv4s, skipping down/loopback/VPN(`utun`)/docker interfaces, in a stable preference order.
  - `func AutopickLANIP() string` — wraps the pure core with `net.Interfaces`; returns `""` if none.

- [ ] **Step 1: Write the failing test**

```go
package mobilebridge

import (
	"net"
	"testing"
)

func TestPrivateIPv4Candidates(t *testing.T) {
	ifaces := []net.Interface{
		{Index: 1, Name: "lo0", Flags: net.FlagUp | net.FlagLoopback},
		{Index: 2, Name: "en0", Flags: net.FlagUp},
		{Index: 3, Name: "utun3", Flags: net.FlagUp},   // VPN — skip
		{Index: 4, Name: "en5", Flags: 0},              // down — skip
	}
	addrs := map[string][]net.Addr{
		"lo0":   {cidr("127.0.0.1/8")},
		"en0":   {cidr("192.168.1.42/24"), cidr("fe80::1/64")},
		"utun3": {cidr("10.9.9.9/24")},
		"en5":   {cidr("192.168.5.5/24")},
	}
	got := PrivateIPv4Candidates(ifaces, func(i net.Interface) ([]net.Addr, error) {
		return addrs[i.Name], nil
	})
	if len(got) != 1 || got[0] != "192.168.1.42" {
		t.Fatalf("got %v want [192.168.1.42]", got)
	}
}

func cidr(s string) net.Addr {
	ip, ipnet, _ := net.ParseCIDR(s)
	ipnet.IP = ip
	return ipnet
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/mobilebridge/ -run TestPrivateIPv4 -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package mobilebridge

import (
	"net"
	"strings"
)

func skipInterface(i net.Interface) bool {
	if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
		return true
	}
	n := strings.ToLower(i.Name)
	for _, bad := range []string{"utun", "tun", "tap", "docker", "bridge", "vmnet", "llw", "awdl"} {
		if strings.HasPrefix(n, bad) {
			return true
		}
	}
	return false
}

func PrivateIPv4Candidates(ifaces []net.Interface, addrsOf func(net.Interface) ([]net.Addr, error)) []string {
	var out []string
	for _, i := range ifaces {
		if skipInterface(i) {
			continue
		}
		addrs, err := addrsOf(i)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			ip4 := ip.To4()
			if ip4 == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if ip4.IsPrivate() {
				out = append(out, ip4.String())
			}
		}
	}
	return out
}

func AutopickLANIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	c := PrivateIPv4Candidates(ifaces, net.Interface.Addrs)
	if len(c) == 0 {
		return ""
	}
	return c[0]
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/mobilebridge/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/mobilebridge/netiface.go backend/internal/mobilebridge/netiface_test.go
git commit -m "feat(mobile): autopick private LAN IPv4"
```

---

## PHASE 2 — Backend: auth middleware & lockout

### Task 3: Bearer auth middleware with per-source lockout

**Files:**

- Create: `backend/internal/httpd/auth.go`
- Test: `backend/internal/httpd/auth_test.go`

**Interfaces:**

- Consumes: `mobilebridge.PasswordMatches` (Task 1).
- Produces:
  - `type authState struct { hash atomic.Pointer[string] }` with `func (a *authState) setHash(h string)` and `func (a *authState) currentHash() string`.
  - `func newLockout(limit int, cooldown time.Duration, now func() time.Time) *lockout` with `func (l *lockout) blocked(src string) bool`, `func (l *lockout) fail(src string)`, `func (l *lockout) reset(src string)`.
  - `func authMiddleware(state *authState, lock *lockout) func(http.Handler) http.Handler` — extracts `Authorization: Bearer`, checks lockout → 429, checks password → 401 (+`lock.fail`), success → `lock.reset` + call through. Uses `r.RemoteAddr` host as source key.

- [ ] **Step 1: Write the failing test**

```go
package httpd

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

func newAuthUnderTest(pw string, now func() time.Time) (http.Handler, *lockout) {
	st := &authState{}
	h := mobilebridge.HashPassword(pw)
	st.setHash(h)
	lock := newLockout(5, time.Minute, now)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	return authMiddleware(st, lock)(ok), lock
}

func req(auth string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	r.RemoteAddr = "192.168.1.50:5555"
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	return r
}

func TestAuthRejectsMissingAndWrong(t *testing.T) {
	h, _ := newAuthUnderTest("secret12", time.Now)
	for _, tc := range []struct{ name, auth string; want int }{
		{"missing", "", http.StatusUnauthorized},
		{"wrong", "Bearer nope", http.StatusUnauthorized},
		{"right", "Bearer secret12", http.StatusOK},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req(tc.auth))
		if w.Code != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, w.Code, tc.want)
		}
	}
}

func TestAuthLockoutAfterFive(t *testing.T) {
	now := time.Now()
	h, _ := newAuthUnderTest("secret12", func() time.Time { return now })
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req("Bearer wrong"))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d want 401", i, w.Code)
		}
	}
	// 6th attempt — even with the RIGHT password — is locked out.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("Bearer secret12"))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("locked attempt: got %d want 429", w.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/httpd/ -run TestAuth -v`
Expected: FAIL — undefined `authState`/`newLockout`/`authMiddleware`.

- [ ] **Step 3: Write minimal implementation**

```go
package httpd

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

// authState holds the current password hash for the LAN listener. Swapped
// atomically on regenerate so an in-flight request never sees a torn value.
type authState struct{ hash atomic.Pointer[string] }

func (a *authState) setHash(h string)     { a.hash.Store(&h) }
func (a *authState) currentHash() string {
	if p := a.hash.Load(); p != nil {
		return *p
	}
	return ""
}

// lockout throttles password guessing per source address.
type lockout struct {
	mu       sync.Mutex
	limit    int
	cooldown time.Duration
	now      func() time.Time
	fails    map[string]int
	until    map[string]time.Time
}

func newLockout(limit int, cooldown time.Duration, now func() time.Time) *lockout {
	return &lockout{limit: limit, cooldown: cooldown, now: now, fails: map[string]int{}, until: map[string]time.Time{}}
}

func (l *lockout) blocked(src string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.until[src]
	return ok && l.now().Before(t)
}

func (l *lockout) fail(src string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[src]++
	if l.fails[src] >= l.limit {
		l.until[src] = l.now().Add(l.cooldown)
	}
}

func (l *lockout) reset(src string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, src)
	delete(l.until, src)
}

func sourceKey(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

func authMiddleware(state *authState, lock *lockout) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			src := sourceKey(r)
			if lock.blocked(src) {
				envelope.WriteAPIError(w, r, http.StatusTooManyRequests, "too_many_requests", "LOCKED_OUT",
					"too many failed attempts; try again shortly", nil)
				return
			}
			if mobilebridge.PasswordMatches(state.currentHash(), bearerToken(r)) {
				lock.reset(src)
				next.ServeHTTP(w, r)
				return
			}
			lock.fail(src)
			envelope.WriteAPIError(w, r, http.StatusUnauthorized, "unauthorized", "BAD_PASSWORD",
				"missing or invalid connection password", nil)
		})
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/httpd/ -run TestAuth -v && go test -race ./internal/httpd/ -run TestAuth`
Expected: PASS (including `-race`).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/httpd/auth.go backend/internal/httpd/auth_test.go
git commit -m "feat(mobile): bearer auth middleware with per-source lockout"
```

---

## PHASE 3 — Backend: runtime LAN listener

### Task 4: LANManager — start/stop a second listener at runtime

**Files:**

- Create: `backend/internal/httpd/lan_listener.go`
- Test: `backend/internal/httpd/lan_listener_test.go`

**Interfaces:**

- Consumes: `authMiddleware`, `authState`, `newLockout` (Task 3); the shared `http.Handler` router built by `NewRouterWithControl`.
- Produces:
  - `type LANManager struct { ... }`
  - `func NewLANManager(handler http.Handler, state *authState, defaultPort int, log *slog.Logger) *LANManager` — wraps `handler` once with `authMiddleware`.
  - `func (m *LANManager) Start(port int) (boundPort int, err error)` — binds `0.0.0.0:port`, ephemeral fallback on `EADDRINUSE`, serves in a goroutine, idempotent (no-op if already running). Returns the actually-bound port.
  - `func (m *LANManager) Stop(ctx context.Context) error` — graceful shutdown; idempotent.
  - `func (m *LANManager) Running() bool`
  - `func (m *LANManager) BoundPort() int`

- [ ] **Step 1: Write the failing test**

```go
package httpd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

func TestLANManagerAuthGatesSharedHandler(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	})
	st := &authState{}
	st.setHash(mobilebridge.HashPassword("secret12"))
	m := NewLANManager(inner, st, 0, slog.Default()) // port 0 → ephemeral
	port, err := m.Start(0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop(context.Background())
	if !m.Running() || m.BoundPort() != port {
		t.Fatalf("running=%v boundPort=%d port=%d", m.Running(), m.BoundPort(), port)
	}

	base := fmt.Sprintf("http://127.0.0.1:%d/anything", port)
	// no auth → 401
	resp, _ := http.Get(base)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-auth: got %d want 401", resp.StatusCode)
	}
	// with auth → 200
	req, _ := http.NewRequest(http.MethodGet, base, nil)
	req.Header.Set("Authorization", "Bearer secret12")
	resp2, _ := http.DefaultClient.Do(req)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("auth: got %d want 200", resp2.StatusCode)
	}
}

func TestLANManagerStartStopIdempotent(t *testing.T) {
	m := NewLANManager(http.NotFoundHandler(), &authState{}, 0, slog.Default())
	p1, _ := m.Start(0)
	p2, _ := m.Start(0) // idempotent — same port, no error
	if p1 != p2 {
		t.Fatalf("second start changed port: %d != %d", p1, p2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if m.Running() {
		t.Fatal("still running after stop")
	}
	_ = m.Stop(ctx) // second stop is a no-op
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/httpd/ -run TestLANManager -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package httpd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"
)

// LANManager owns the daemon's second, network-facing HTTP listener. It binds
// 0.0.0.0 only while Connect Mobile is enabled and wraps the shared router in
// authMiddleware. The loopback listener is unaffected.
type LANManager struct {
	handler     http.Handler // shared router, already auth-wrapped
	defaultPort int
	log         *slog.Logger

	mu    sync.Mutex
	srv   *http.Server
	ln    net.Listener
	bound int
}

func NewLANManager(handler http.Handler, state *authState, defaultPort int, log *slog.Logger) *LANManager {
	lock := newLockout(5, time.Minute, time.Now)
	return &LANManager{
		handler:     authMiddleware(state, lock)(handler),
		defaultPort: defaultPort,
		log:         loggerOrDefault(log),
	}
}

func (m *LANManager) Start(port int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.srv != nil {
		return m.bound, nil // idempotent
	}
	if port == 0 {
		port = m.defaultPort
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		if !errors.Is(err, syscall.EADDRINUSE) {
			return 0, fmt.Errorf("bind LAN 0.0.0.0:%d: %w", port, err)
		}
		if ln, err = net.Listen("tcp", "0.0.0.0:0"); err != nil {
			return 0, fmt.Errorf("bind LAN ephemeral: %w", err)
		}
		m.log.Warn("LAN port in use; bound ephemeral", "wanted", port, "bound", ln.Addr())
	}
	m.ln = ln
	m.bound = ln.Addr().(*net.TCPAddr).Port
	m.srv = &http.Server{Handler: m.handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := m.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.log.Error("LAN listener serve", "err", err)
		}
	}()
	m.log.Info("LAN listener started", "addr", ln.Addr())
	return m.bound, nil
}

func (m *LANManager) Stop(ctx context.Context) error {
	m.mu.Lock()
	srv := m.srv
	m.srv, m.ln, m.bound = nil, nil, 0
	m.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

func (m *LANManager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.srv != nil
}

func (m *LANManager) BoundPort() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bound
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test -race ./internal/httpd/ -run TestLANManager -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/httpd/lan_listener.go backend/internal/httpd/lan_listener_test.go
git commit -m "feat(mobile): runtime-controlled LAN listener manager"
```

---

## PHASE 4 — Backend: REST control endpoints

### Task 5: Mobile control service + DTOs

**Files:**

- Create: `backend/internal/httpd/controllers/mobile.go`
- Test: `backend/internal/httpd/controllers/mobile_test.go`
- Modify: `backend/internal/httpd/controllers/dto.go`

**Interfaces:**

- Consumes: `mobilebridge` (Task 1/2), `LANManager` + `authState` (Task 3/4).
- Produces (DTOs in `dto.go`):
  - `type MobileStatusResponse struct { Enabled bool `json:"enabled"`; Host string `json:"host"`; Port int `json:"port"`; Password string `json:"password"`; Warning string `json:"warning"` }`
  - Controller `MobileController` with methods `Status`, `Enable`, `Disable`, `Regenerate`, each `func(http.ResponseWriter, *http.Request)`.
  - A small port interface the controller depends on so it is unit-testable without a real listener:
    `type mobileBridge interface { Enable() (MobileStatusResponse, error); Disable() error; Regenerate() (MobileStatusResponse, error); Status() MobileStatusResponse }`
- The concrete `mobileBridge` impl (`bridgeService`) lives in `mobile.go` and closes over `*LANManager`, `*authState`, the config path, and default port. `Password` is only populated when enabled (empty string when disabled). `Warning` is a constant: `"Traffic on this connection is not encrypted. Only use it on a network you trust."`

- [ ] **Step 1: Write the failing test** (controller against a fake bridge)

```go
package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeBridge struct{ enabled bool }

func (f *fakeBridge) Status() MobileStatusResponse {
	return MobileStatusResponse{Enabled: f.enabled, Host: "192.168.1.42", Port: 3011}
}
func (f *fakeBridge) Enable() (MobileStatusResponse, error) {
	f.enabled = true
	r := f.Status()
	r.Password = "abcd1234"
	return r, nil
}
func (f *fakeBridge) Disable() error { f.enabled = false; return nil }
func (f *fakeBridge) Regenerate() (MobileStatusResponse, error) {
	r := f.Status()
	r.Password = "wxyz5678"
	return r, nil
}

func TestMobileEnableReturnsPassword(t *testing.T) {
	c := &MobileController{Bridge: &fakeBridge{}}
	w := httptest.NewRecorder()
	c.Enable(w, httptest.NewRequest(http.MethodPost, "/api/v1/mobile/enable", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
	var got MobileStatusResponse
	json.NewDecoder(w.Body).Decode(&got)
	if !got.Enabled || got.Password != "abcd1234" || got.Warning == "" {
		t.Fatalf("bad response: %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/httpd/controllers/ -run TestMobile -v`
Expected: FAIL — undefined types.

- [ ] **Step 3: Write minimal implementation** (controller + concrete bridge)

Add DTO to `dto.go` (near other response DTOs), then create `mobile.go`:

```go
package controllers

import (
	"context"
	"net/http"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

const mobileUnencryptedWarning = "Traffic on this connection is not encrypted. Only use it on a network you trust."

type mobileBridge interface {
	Status() MobileStatusResponse
	Enable() (MobileStatusResponse, error)
	Disable() error
	Regenerate() (MobileStatusResponse, error)
}

type MobileController struct{ Bridge mobileBridge }

func (c *MobileController) Status(w http.ResponseWriter, r *http.Request) {
	envelope.WriteJSON(w, http.StatusOK, c.Bridge.Status())
}
func (c *MobileController) Enable(w http.ResponseWriter, r *http.Request) {
	res, err := c.Bridge.Enable()
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "MOBILE_ENABLE", err.Error(), nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, res)
}
func (c *MobileController) Disable(w http.ResponseWriter, r *http.Request) {
	if err := c.Bridge.Disable(); err != nil {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "MOBILE_DISABLE", err.Error(), nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, c.Bridge.Status())
}
func (c *MobileController) Regenerate(w http.ResponseWriter, r *http.Request) {
	res, err := c.Bridge.Regenerate()
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "MOBILE_REGEN", err.Error(), nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, res)
}

// LANController is the runtime hook set the concrete bridge needs. httpd's
// LANManager + authState satisfy it (adapter wired in daemon.go).
type LANController interface {
	Start(port int) (int, error)
	Stop(ctx context.Context) error
	Running() bool
	BoundPort() int
	SetPasswordHash(hash string)
}

// BridgeService is the production mobileBridge. It persists state and drives
// the LAN listener. Password plaintext exists only transiently in the response.
type BridgeService struct {
	LAN         LANController
	ConfigPath  string
	DefaultPort int
}

func (b *BridgeService) currentHost() string { return mobilebridge.AutopickLANIP() }

func (b *BridgeService) Status() MobileStatusResponse {
	st, _ := mobilebridge.Load(b.ConfigPath)
	return MobileStatusResponse{
		Enabled: st.Enabled && b.LAN.Running(),
		Host:    b.currentHost(),
		Port:    b.LAN.BoundPort(),
		Warning: mobileUnencryptedWarning,
	}
}

func (b *BridgeService) enableWithPassword(pw string) (MobileStatusResponse, error) {
	hash := mobilebridge.HashPassword(pw)
	b.LAN.SetPasswordHash(hash)
	port, err := b.LAN.Start(b.DefaultPort)
	if err != nil {
		return MobileStatusResponse{}, err
	}
	if err := mobilebridge.Save(b.ConfigPath, mobilebridge.State{Enabled: true, PasswordHash: hash, LastPort: port}); err != nil {
		return MobileStatusResponse{}, err
	}
	res := b.Status()
	res.Password = pw // transient — never persisted in plaintext
	return res, nil
}

func (b *BridgeService) Enable() (MobileStatusResponse, error) {
	pw, err := mobilebridge.GeneratePassword()
	if err != nil {
		return MobileStatusResponse{}, err
	}
	return b.enableWithPassword(pw)
}

func (b *BridgeService) Regenerate() (MobileStatusResponse, error) {
	pw, err := mobilebridge.GeneratePassword()
	if err != nil {
		return MobileStatusResponse{}, err
	}
	return b.enableWithPassword(pw) // rotate → drops current phone (new hash)
}

func (b *BridgeService) Disable() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.LAN.Stop(ctx); err != nil {
		return err
	}
	st, _ := mobilebridge.Load(b.ConfigPath)
	st.Enabled = false
	return mobilebridge.Save(b.ConfigPath, st)
}
```

Note for the implementer: add `SetPasswordHash(hash string)` to `LANManager` in `lan_listener.go` — it stores the hash on the shared `*authState` (`m.state.setHash(hash)`); keep a `state *authState` field on `LANManager` and set it in `NewLANManager`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/httpd/controllers/ -run TestMobile -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/httpd/controllers/mobile.go backend/internal/httpd/controllers/mobile_test.go backend/internal/httpd/controllers/dto.go backend/internal/httpd/lan_listener.go
git commit -m "feat(mobile): mobile control endpoints + bridge service"
```

---

### Task 6: Register routes on the LOOPBACK router + regenerate API artifacts

**Files:**

- Modify: `backend/internal/httpd/router.go` (add `mountMobile` — these control routes live on the loopback router so the _desktop_ drives them; the phone never enables/disables itself).
- Modify: `backend/internal/httpd/apispec/specgen/build.go` (register the 4 operations + `MobileStatusResponse` schema name).

**Interfaces:**

- Consumes: `MobileController` (Task 5).
- Produces: routes `GET /api/v1/mobile/status`, `POST /api/v1/mobile/enable`, `POST /api/v1/mobile/disable`, `POST /api/v1/mobile/regenerate`, each gated by `localControlRequest` (desktop/loopback only — the phone must not toggle its own access).

- [ ] **Step 1: Write the failing test**

```go
// backend/internal/httpd/mobile_routes_test.go
package httpd

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMobileStatusRouteIsLoopbackGated(t *testing.T) {
	r := newTestRouterWithMobile(t) // helper builds router with a fake controller
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mobile/status", nil)
	req.Host = "192.168.1.9:3011" // non-loopback → must be refused
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-loopback status: got %d want 403", w.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/httpd/ -run TestMobileStatusRoute -v`
Expected: FAIL — route not mounted / helper undefined.

- [ ] **Step 3: Implement** `mountMobile(r, controller)` in `router.go`, call it from `NewRouterWithControl`, wrapping each handler with the existing `localControlRequest` check (mirror `mountControl`). Add the operations to `build.go` with a `schemaNames` entry for `MobileStatusResponse`. Provide the `newTestRouterWithMobile` helper in the test file.

- [ ] **Step 4: Verify + regenerate artifacts**

Run:

```bash
cd backend && go test ./internal/httpd/ -run TestMobile -v
cd .. && npm run api        # regenerate OpenAPI + frontend TS types
npm run frontend:typecheck
```

Expected: tests PASS; `npm run api` updates spec + `frontend/src/api/*` with the new types; typecheck PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/httpd/router.go backend/internal/httpd/mobile_routes_test.go backend/internal/httpd/apispec/ frontend/src/api/
git commit -m "feat(mobile): mount loopback-gated mobile control routes + regen API"
```

---

### Task 7: Wire LANManager into the daemon + restore-on-boot

**Files:**

- Modify: `backend/internal/daemon/daemon.go`

**Interfaces:**

- Consumes: `httpd.NewLANManager`, `controllers.BridgeService`, `mobilebridge.Load/Path`.
- Produces: a running daemon where (a) the loopback router serves as today, (b) a `LANManager` is constructed over the same handler + a shared `authState`, (c) the mobile controller drives it, (d) on boot, if persisted `State.Enabled` is true, the LAN listener is re-started with the persisted `PasswordHash` (no new password — the paired phone keeps working).

- [ ] **Step 1: Write the failing test** (boot restore)

```go
// backend/internal/daemon/mobile_restore_test.go — table test at the seam.
// If daemon.Run is too heavy to unit-test, assert the restore helper instead:
func TestRestoreEnabledStartsListener(t *testing.T) {
	dir := t.TempDir()
	path := mobilebridge.Path(dir)
	_ = mobilebridge.Save(path, mobilebridge.State{Enabled: true, PasswordHash: "h", LastPort: 3011})
	lan := &fakeLAN{}
	restoreMobileOnBoot(path, lan) // helper added in daemon package
	if !lan.started {
		t.Fatal("expected LAN listener started from persisted enabled state")
	}
	if lan.hash != "h" {
		t.Fatalf("expected persisted hash reused, got %q", lan.hash)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/daemon/ -run TestRestoreEnabled -v`
Expected: FAIL — `restoreMobileOnBoot`/`fakeLAN` undefined.

- [ ] **Step 3: Implement** `restoreMobileOnBoot(path string, lan httpd.LANController)` in the daemon package: `Load` the state; if `Enabled`, `lan.SetPasswordHash(state.PasswordHash)` then `lan.Start(state.LastPort)`. In `Run`, after constructing `srv`, build the shared `authState`, the `LANManager` over the router handler, the `BridgeService`, pass the controller into `NewWithDeps`/router deps, and call `restoreMobileOnBoot` before serving. Stop the LAN listener during shutdown alongside the other teardown.

  Implementation note: the router handler must be reachable to hand to `NewLANManager`. Either expose the built `chi.Router` from the `Server` (add `func (s *Server) Handler() http.Handler`) or build the router once in `daemon.Run` and pass it to both the loopback `Server` and the `LANManager`. Prefer the latter to keep a single handler instance.

- [ ] **Step 4: Verify end-to-end**

Run:

```bash
cd backend && go build ./... && go test ./... && go test -race ./internal/httpd/ ./internal/daemon/ ./internal/mobilebridge/
```

Then a manual smoke:

```bash
go run ./cmd/ao start &   # daemon up
curl -s -XPOST localhost:3001/api/v1/mobile/enable | tee /tmp/enable.json   # returns password + port
# NOTE: envelope.WriteJSON encodes the DTO directly (no "data" wrapper).
PW=$(python3 -c "import json;print(json.load(open('/tmp/enable.json'))['password'])")
PORT=$(python3 -c "import json;print(json.load(open('/tmp/enable.json'))['port'])")
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:$PORT/api/v1/sessions            # expect 401
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $PW" http://127.0.0.1:$PORT/api/v1/sessions  # expect 200
curl -s -XPOST localhost:3001/api/v1/mobile/disable                                         # closes LAN socket
```

Expected: build+tests PASS; unauth 401, authed 200; disable closes the port.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/daemon/
git commit -m "feat(mobile): wire LAN listener into daemon with restore-on-boot"
```

---

## PHASE 5 — Desktop UI (Electron/React)

### Task 8: shadcn Dialog primitive

**Files:**

- Create: `frontend/src/renderer/components/ui/dialog.tsx`

**Interfaces:**

- Produces: `Dialog`, `DialogTrigger`, `DialogContent`, `DialogHeader`, `DialogTitle`, `DialogDescription`, `DialogFooter` — the standard shadcn Radix Dialog wrappers, styled to match the existing `sheet.tsx` tokens (only `sheet.tsx` exists; add `dialog.tsx` beside it).

- [ ] **Step 1:** Copy the canonical shadcn `dialog.tsx` (Radix `@radix-ui/react-dialog`), matching class tokens used in `sheet.tsx`. Confirm `@radix-ui/react-dialog` is already a dep (it backs `sheet.tsx`); if not, add it.
- [ ] **Step 2:** `cd frontend && npm run typecheck` → PASS.
- [ ] **Step 3: Commit**

```bash
git add frontend/src/renderer/components/ui/dialog.tsx frontend/package.json
git commit -m "feat(ui): add shadcn dialog primitive"
```

---

### Task 9: QR generator + Connect Mobile modal

**Files:**

- Create: `frontend/src/renderer/lib/qr.ts` (self-contained QR→SVG string; **no external host** per CSP).
- Create: `frontend/src/renderer/components/ConnectMobileModal.tsx`
- Create: `frontend/src/renderer/components/ConnectMobileModal.test.tsx`
- Create: `frontend/src/renderer/components/ConnectMobileButton.tsx`
- Modify: `frontend/src/renderer/components/GlobalSettingsForm.tsx`

**Interfaces:**

- Consumes: generated mobile client types (Task 6) via `api-client.ts`; `Dialog` (Task 8).
- Produces:
  - `ConnectMobileButton` — a button rendered in `GlobalSettingsForm`; opens the modal.
  - `ConnectMobileModal` — reads `GET /api/v1/mobile/status`; when OFF shows an **Enable** button; when ON shows a **QR** (encoding `{"v":1,"host":<host>,"port":<port>}` — **password NOT included**), the `host:port` text, the **password** in plaintext, **Regenerate** and **Disable** buttons, and the unencrypted-network **warning** text from `status.warning`.
- The QR payload builder: `function pairingPayload(host: string, port: number): string { return JSON.stringify({ v: 1, host, port }); }` — assert in a test that it excludes any password field.

- [ ] **Step 1: Write the failing test**

```tsx
// ConnectMobileModal.test.tsx
import { render, screen } from "@testing-library/react";
import { pairingPayload } from "./ConnectMobileModal";

test("QR payload never contains the password", () => {
	const s = pairingPayload("192.168.1.42", 3011);
	expect(JSON.parse(s)).toEqual({ v: 1, host: "192.168.1.42", port: 3011 });
	expect(s.toLowerCase()).not.toContain("password");
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && npx vitest run src/renderer/components/ConnectMobileModal.test.tsx`
Expected: FAIL — module not found.

- [ ] **Step 3: Implement** `qr.ts`, `ConnectMobileModal.tsx` (with exported `pairingPayload`), `ConnectMobileButton.tsx`, and add `<ConnectMobileButton/>` as a new section in `GlobalSettingsForm.tsx`. Use `useQuery`/`useMutation` against the generated client for status/enable/disable/regenerate. Show the password with a "Copy" affordance; render the QR SVG inline from `pairingPayload(...)`. Display `status.warning` prominently.

- [ ] **Step 4: Verify + demo**

Run:

```bash
cd frontend && npx vitest run src/renderer/components/ConnectMobileModal.test.tsx && npm run typecheck
```

Then, per CLAUDE.md, demo it in-session:

```bash
ao preview   # render the settings screen with Connect Mobile in the desktop browser panel
```

Expected: test PASS, typecheck PASS, modal renders with QR + password + warning when enabled.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/renderer/lib/qr.ts frontend/src/renderer/components/ConnectMobile*.tsx frontend/src/renderer/components/ConnectMobileModal.test.tsx frontend/src/renderer/components/GlobalSettingsForm.tsx
git commit -m "feat(mobile): desktop Connect Mobile modal with QR, password, warning"
```

---

## PHASE 6 — Mobile app (Expo)

### Task 10: ServerConfig password + auth headers

**Files:**

- Modify: `packages/mobile/lib/config.ts`
- Modify: `packages/mobile/lib/api.ts`
- Modify: `packages/mobile/lib/mux.ts`

**Interfaces:**

- Produces:
  - `ServerConfig` gains `password: string`.
  - `function authHeaders(cfg: ServerConfig): Record<string,string>` in `config.ts` → `cfg.password ? { Authorization: `Bearer ${cfg.password}` } : {}`.
  - `api.ts`: every `fetch` spreads `authHeaders(cfg)` into request headers.
  - `mux.ts`: the `WebSocket` is constructed with RN's options arg — `new WebSocket(muxUrl(cfg), undefined, { headers: authHeaders(cfg) })`.

- [ ] **Step 1: Write the failing test** (config helper; mobile uses tsc — add a tiny node/vitest or a typecheck-guarded assertion)

If the mobile package has no test runner, encode the contract as a typed unit and verify via `npm run typecheck`; otherwise:

```ts
import { authHeaders, DEFAULT_CONFIG } from "./config";
test("authHeaders present only with a password", () => {
	expect(authHeaders({ ...DEFAULT_CONFIG, password: "" })).toEqual({});
	expect(authHeaders({ ...DEFAULT_CONFIG, password: "abcd1234" })).toEqual({ Authorization: "Bearer abcd1234" });
});
```

- [ ] **Step 2: Run** `cd packages/mobile && npm run typecheck` (and the test if a runner exists) → FAIL (missing `password`/`authHeaders`).
- [ ] **Step 3: Implement** the `password` field (default `""`), `authHeaders`, and thread it through `api.ts` fetches and the `mux.ts` WebSocket.
- [ ] **Step 4: Run** `cd packages/mobile && npm run typecheck` → PASS.
- [ ] **Step 5: Commit**

```bash
git add packages/mobile/lib/config.ts packages/mobile/lib/api.ts packages/mobile/lib/mux.ts
git commit -m "feat(mobile): send Authorization bearer on REST + mux"
```

---

### Task 11: QR scanning + pairing + password popup

**Files:**

- Create: `packages/mobile/lib/pairing.ts`
- Create: `packages/mobile/app/pair.tsx`
- Modify: `packages/mobile/app/(tabs)/settings.tsx`
- Modify: `packages/mobile/package.json`, `packages/mobile/app.json`

**Interfaces:**

- Produces:
  - `function parsePairingPayload(raw: string): { host: string; port: string } | null` in `pairing.ts` — parse `{v,host,port}`, validate `v===1`, coerce `port` to string, reject anything else.
  - `app/pair.tsx` — an `expo-camera` scanner; on scan, `parsePairingPayload` → navigate back to settings with host/port filled.
  - `settings.tsx` — a **"Scan QR"** button (→ `pair.tsx`), the existing manual host/port fields, a **password** field, and a **Connect** action that opens a popup (RN `Alert.prompt` on iOS or a small modal component cross-platform) asking for the password, then saves the full `ServerConfig` and connects. On a `401` from the daemon, re-open the popup.

- [ ] **Step 1: Write the failing test**

```ts
import { parsePairingPayload } from "./pairing";
test("parses a valid payload and rejects junk", () => {
	expect(parsePairingPayload('{"v":1,"host":"192.168.1.42","port":3011}')).toEqual({
		host: "192.168.1.42",
		port: "3011",
	});
	expect(parsePairingPayload('{"v":2,"host":"x","port":1}')).toBeNull();
	expect(parsePairingPayload("not json")).toBeNull();
	expect(parsePairingPayload('{"host":"x"}')).toBeNull();
});
```

- [ ] **Step 2: Run** the mobile test/typecheck → FAIL (module missing).
- [ ] **Step 3: Implement** `parsePairingPayload`; add `expo-camera` to `package.json` and its permission to `app.json` (`ios.infoPlist.NSCameraUsageDescription`, `android.permissions: ["CAMERA"]`, plus the `expo-camera` config plugin); build `pair.tsx` and the settings wiring with the password popup. Keep `secure:false` (plaintext).
- [ ] **Step 4: Verify**

Run:

```bash
cd packages/mobile && npm run typecheck
npx expo prebuild --clean   # regenerate native projects with the camera permission (dev build)
```

Then a device smoke: scan the desktop QR, enter the password from the desktop modal, confirm the session list and a terminal load over LAN.
Expected: typecheck PASS; on-device pairing connects and the terminal streams.

- [ ] **Step 5: Commit**

```bash
git add packages/mobile/lib/pairing.ts packages/mobile/app/pair.tsx "packages/mobile/app/(tabs)/settings.tsx" packages/mobile/package.json packages/mobile/app.json
git commit -m "feat(mobile): QR scan pairing + password popup"
```

---

## PHASE 7 — Docs

### Task 12: Amend AGENTS.md + architecture note; retire the manual proxy

**Files:**

- Modify: `AGENTS.md`
- Modify: `docs/architecture.md`
- Modify: `packages/mobile/scripts/README.md` (mark `ao-phone-proxy.js` superseded by the built-in LAN listener)

- [ ] **Step 1:** In `AGENTS.md`, change the hard rule from _"The daemon is a loopback-only sidecar. Do not make the bind host configurable or expose it beyond `127.0.0.1`."_ to scope it to the **Loopback Listener**, and add the LAN Listener's rules:

  > - The daemon's **primary (loopback) listener** stays bound to `127.0.0.1` and unauthenticated; do not change its bind or add auth to it.
  > - A **second, opt-in LAN listener** (Connect Mobile) may bind `0.0.0.0` **only** while enabled, **only** behind the bearer-password `authMiddleware`, serving the app API but never the loopback-gated control routes. Plaintext, home-network-only, by decision in `docs/adr/0001-lan-listener-for-mobile.md`.

- [ ] **Step 2:** Add a short two-listener paragraph to `docs/architecture.md` pointing at ADR 0001 and `CONTEXT.md`.
- [ ] **Step 3:** Note in the mobile scripts README that `ao-phone-proxy.js` is superseded by the in-app LAN listener (keep the file for now; do not delete without user sign-off).
- [ ] **Step 4:** `npm run lint` (docs don't break Go, but run the repo lint gate for safety) → PASS.
- [ ] **Step 5: Commit**

```bash
git add AGENTS.md docs/architecture.md packages/mobile/scripts/README.md
git commit -m "docs(mobile): scope loopback-only rule to loopback listener; document LAN listener"
```

---

## Self-Review

**Spec coverage** — every decision maps to a task:

- Second LAN listener inside daemon → Tasks 4, 7. Loopback unchanged → enforced by not touching the loopback `Server`; asserted implicitly (existing tests still pass in Task 7 Step 4).
- On-demand off-by-default + persistence + restore-on-boot → Tasks 1, 5, 7.
- Single rotating 8-char alnum password, hashed, constant-time → Task 1; rotate drops phone → Task 5 (`Regenerate` → new hash).
- Bearer on REST + RN WebSocket → Tasks 3 (server), 10 (client).
- Per-source lockout after 5 → Task 3.
- App API only; control loopback-only → Tasks 4 (wrap), 6 (`localControlRequest` on mobile control routes).
- Plaintext home-network-only + warning → Task 5 (`Warning`), 9 (displayed), 12 (docs).
- QR host+port only, password out-of-band → Tasks 9 (`pairingPayload` excludes pw, tested), 11 (`parsePairingPayload`).
- Default 3011 + ephemeral fallback + report bound port → Task 4, surfaced in Task 5 `Status`.
- Autopick LAN IP → Task 2.
- Desktop modal from a button with toggle/QR/ip:port/password/regen/disable/warning → Tasks 8, 9.
- expo-camera + password on ServerConfig → Tasks 10, 11.
- Amend AGENTS.md → Task 12.

**Placeholder scan** — no "TBD"/"add error handling"/"write tests for the above"; every code step carries real code or an exact command.

**Type consistency** — `MobileStatusResponse`, `mobileBridge`/`BridgeService`, `LANController` (with `SetPasswordHash`), `authState`/`lockout`/`authMiddleware`, `pairingPayload`/`parsePairingPayload`, `authHeaders` are named identically wherever referenced across tasks. `LANManager` gains `SetPasswordHash` (noted in Task 5) so it satisfies `LANController`.

**Known follow-ups (out of scope, by decision):** TLS + fingerprint pinning (ADR 0001 "Consequences"); multi-device passwords; QR expiry.
