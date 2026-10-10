package host

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func openTestRoutes(t *testing.T) *Routes {
	t.Helper()
	return OpenRoutes(filepath.Join(t.TempDir(), "run", "routes.json"))
}
func route(ticket, provider, account string) Route {
	return Route{TicketHash: TicketHash(ticket), Provider: provider, AuthID: account}
}

// admitted reports which account a ticket is admitted on, "" when it is denied.
func admitted(r *Routes, ticket string) string {
	got, release, ok := r.Acquire(ticket)
	if !ok {
		return ""
	}
	release()
	return got.AuthID
}

func TestUnknownEmptyAndUnassignedTicketsAreDenied(t *testing.T) {
	r := openTestRoutes(t)
	// A route for the hash of the empty ticket must still not admit an empty ticket.
	if err := r.Apply([]Route{route("valid", "codex", "alice"), route("waiting", "claude", ""), route("", "codex", "alice")}, []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	for _, ticket := range []string{"", "unknown", "VALID", " valid", "valid ", "waiting", TicketHash("valid")} {
		if _, release, ok := r.Acquire(ticket); ok || release != nil {
			t.Fatalf("ticket %q was admitted", ticket)
		}
	}
	if admitted(r, "valid") != "alice" {
		t.Fatal("the valid ticket was denied")
	}
}

func TestPushReplacesTheTableAndARequestInFlightKeepsItsAccount(t *testing.T) {
	r := openTestRoutes(t)
	both := []string{"alice", "bob"}
	if err := r.Apply([]Route{route("a", "codex", "alice"), route("b", "codex", "bob")}, both); err != nil {
		t.Fatal(err)
	}
	running, release, ok := r.Acquire("a")
	if !ok {
		t.Fatal("ticket denied")
	}
	// Moving the busy session is allowed: its request finishes where it was admitted.
	moved := []Route{route("a", "codex", "bob"), route("c", "claude", "carol")}
	if err := r.Apply(moved, append(both, "carol")); err != nil {
		t.Fatalf("a push that keeps every busy account was refused: %v", err)
	}
	if running.AuthID != "alice" || admitted(r, "a") != "bob" || admitted(r, "b") != "" || admitted(r, "c") != "carol" {
		t.Fatal("the push did not replace the whole table")
	}
	// Dropping the account that is still serving a request is refused, and changes nothing.
	before, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range [][]Route{{}, {route("a", "codex", "carol")}} {
		if err := r.Apply(table, []string{"bob", "carol"}); !errors.Is(err, ErrBusy) {
			t.Fatalf("dropping a busy account: %v", err)
		}
	}
	after, _ := os.ReadFile(r.path)
	if string(after) != string(before) || admitted(r, "a") != "bob" || admitted(r, "c") != "carol" {
		t.Fatal("a refused push changed the table or its cache")
	}
	release()
	if err := r.Apply([]Route{}, nil); err != nil {
		t.Fatalf("idle accounts could not be dropped: %v", err)
	}
	if admitted(r, "a") != "" {
		t.Fatal("a removed ticket is still admitted")
	}
}

func TestEveryRequestInFlightHoldsItsAccount(t *testing.T) {
	r := openTestRoutes(t)
	if err := r.Apply([]Route{route("ticket", "codex", "alice")}, []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	var releases []func()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, release, ok := r.Acquire("ticket")
			if !ok {
				t.Error("ticket denied")
				return
			}
			mu.Lock()
			releases = append(releases, release)
			mu.Unlock()
		}()
	}
	wg.Wait()
	for i, release := range releases {
		if err := r.Apply(nil, nil); !errors.Is(err, ErrBusy) {
			t.Fatalf("before release %d: %v", i, err)
		}
		release()
	}
	if err := r.Apply(nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSigningOutAndBackInKeepsTheSessionTicket(t *testing.T) {
	r := openTestRoutes(t)
	for _, step := range []struct{ account, want string }{{"alice", "alice"}, {"", ""}, {"new-account", "new-account"}} {
		ids := []string{}
		if step.account != "" {
			ids = append(ids, step.account)
		}
		if err := r.Apply([]Route{route("stable-ticket", "claude", step.account)}, ids); err != nil {
			t.Fatal(err)
		}
		if got := admitted(r, "stable-ticket"); got != step.want {
			t.Fatalf("account %q: admitted on %q", step.account, got)
		}
	}
}

func TestRouteCacheSurvivesARestartAndHoldsNoTicket(t *testing.T) {
	r := openTestRoutes(t)
	if err := r.Apply([]Route{route("ticket-a", "codex", "alice"), route("ticket-b", "claude", "bob")}, []string{"alice", "bob"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(r.path)
	if err != nil || strings.Contains(string(data), "ticket-a") || strings.Contains(string(data), "ticket-b") {
		t.Fatalf("the cache is missing or holds a raw ticket: %v", err)
	}
	if info, err := os.Stat(r.path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the cache is not owner-only: %v %v", info, err)
	}
	restarted := OpenRoutes(r.path)
	if admitted(restarted, "ticket-a") != "alice" || admitted(restarted, "ticket-b") != "bob" || admitted(restarted, "unknown") != "" {
		t.Fatal("a restart lost the routes")
	}
}

func TestUnreadableRouteCacheIsIgnoredAndRefilled(t *testing.T) {
	for _, content := range []string{"not json", `null`, `[{"ticket_hash":"x"}]`, `{"routes":"old shape"}`} {
		path := filepath.Join(t.TempDir(), "routes.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		r := OpenRoutes(path)
		if admitted(r, "ticket") != "" || admitted(r, "") != "" {
			t.Fatalf("cache %q admitted a ticket", content)
		}
		if err := r.Apply([]Route{route("ticket", "codex", "alice")}, []string{"alice"}); err != nil {
			t.Fatalf("cache %q could not be refilled: %v", content, err)
		}
		if admitted(OpenRoutes(path), "ticket") != "alice" {
			t.Fatalf("cache %q was not replaced", content)
		}
	}
}

func TestFailedCacheWriteKeepsThePreviousTable(t *testing.T) {
	r := openTestRoutes(t)
	if err := r.Apply([]Route{route("ticket", "codex", "alice")}, []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	// A directory where the file should go fails the rename on every platform.
	r.path = filepath.Join(t.TempDir(), "occupied")
	if err := os.Mkdir(r.path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := r.Apply([]Route{route("ticket", "codex", "bob")}, []string{"bob"}); err == nil || errors.Is(err, ErrBusy) {
		t.Fatalf("the failed write was not reported: %v", err)
	}
	if admitted(r, "ticket") != "alice" {
		t.Fatal("a push that could not be saved took effect")
	}
}
