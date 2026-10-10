package provideraccounts

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// limits is a reading whose general limits have these fractions left.
func limits(left ...float64) domain.ProviderAccountUsage {
	usage := domain.ProviderAccountUsage{Status: "available"}
	for _, fraction := range left {
		usage.Windows = append(usage.Windows, domain.ProviderAccountUsageWindow{RemainingFraction: fraction})
	}
	return usage
}

// spentAccount signs in alice, who has run out and names bob, and bob, who has not.
func spentAccount(t *testing.T) (h *harness, alice, bob string) {
	h = setup(t)
	alice, bob = h.signIn("codex", "alice@example.com"), h.signIn("codex", "bob@example.com")
	h.assign("s1", domain.HarnessCodex, alice)
	h.assign("s2", domain.HarnessCodex, alice)
	h.assign("s3", domain.HarnessCodex, bob)
	if err := h.settings(alice, ports.ProviderAccountAction{OnLimit: new(bob)}); err != nil {
		t.Fatal(err)
	}
	h.helper.usageBy = map[string]domain.ProviderAccountUsage{"alice@example.com-auth": limits(0, 0.5), "bob@example.com-auth": limits(0.4, 0.9)}
	h.helper.usageFail = map[string]bool{}
	return h, alice, bob
}

func TestAnAccountThatRanOutHandsItsSessionsAndTheDefaultToTheAccountItNames(t *testing.T) {
	h, alice, bob := spentAccount(t)
	carol := h.signIn("codex", "carol@example.com")
	h.assign("s4", domain.HarnessCodex, carol)
	if err := h.settings(bob, ports.ProviderAccountAction{Reserved: new(true)}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.switchOnLimit(h.ctx); err != nil {
		t.Fatal(err)
	}
	for session, want := range map[string]string{"s1": bob, "s2": bob, "s3": bob, "s4": carol} {
		h.route(session, want)
	}
	// New sessions follow, so the account they now start on is no longer held back.
	if view := h.view(bob); h.defaultOf("codex") != bob || view.Reserved || view.Moved != nil {
		t.Fatalf("default=%q bob=%+v", h.defaultOf("codex"), view)
	}
	want := &domain.ProviderAccountMove{To: bob, At: "2026-10-10T09:00:00Z", Sessions: 2}
	if view := h.view(alice); !reflect.DeepEqual(view.Moved, want) || len(view.Sessions) != 0 || view.OnLimit != bob {
		t.Fatalf("alice=%+v moved=%+v", view, view.Moved)
	}
	// With nothing left to move, the next pass records nothing new.
	h.advance(time.Minute)
	if err := h.svc.switchOnLimit(h.ctx); err != nil || !reflect.DeepEqual(h.view(alice).Moved, want) {
		t.Fatalf("a second pass: moved=%+v err=%v", h.view(alice).Moved, err)
	}
}

func TestAnAccountThatIsNotTheDefaultHandsOnlyItsSessions(t *testing.T) {
	h, alice, bob := spentAccount(t)
	if err := h.act(bob, "primary"); err != nil {
		t.Fatal(err)
	}
	// Bob has run out as well, and names nobody: his sessions stay.
	h.helper.usageBy["bob@example.com-auth"] = limits(0, 0)
	carol := h.signIn("codex", "carol@example.com")
	h.helper.usageBy["carol@example.com-auth"] = limits(1)
	if err := h.settings(alice, ports.ProviderAccountAction{OnLimit: new(carol)}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.switchOnLimit(h.ctx); err != nil {
		t.Fatal(err)
	}
	for session, want := range map[string]string{"s1": carol, "s2": carol, "s3": bob} {
		h.route(session, want)
	}
	if moved := h.view(alice).Moved; h.defaultOf("codex") != bob || moved == nil || moved.To != carol || moved.Sessions != 2 || h.view(bob).Moved != nil {
		t.Fatalf("default=%q moved=%+v", h.defaultOf("codex"), moved)
	}
}

func TestSessionsStayWhenTheAccountNamedCannotTakeThem(t *testing.T) {
	for name, arrange := range map[string]func(h *harness, alice, bob string){
		"it has run out too": func(h *harness, _, _ string) { h.helper.usageBy["bob@example.com-auth"] = limits(0.4, 0) },
		"it is signed out": func(h *harness, alice, bob string) {
			if err := h.act(bob, "sign-out"); err != nil {
				h.t.Fatal(err)
			}
		},
		"it has no reading": func(h *harness, _, _ string) { h.helper.usageFail["bob@example.com-auth"] = true },
		"the account itself has something left": func(h *harness, _, _ string) {
			h.helper.usageBy["alice@example.com-auth"] = limits(0.01, 0.5)
		},
		"only a limit on one model has run out": func(h *harness, _, _ string) {
			usage := limits(0.2, 0.5)
			usage.Windows = append(usage.Windows, domain.ProviderAccountUsageWindow{Scope: domain.ProviderUsageScopeModel, Name: "Opus"})
			h.helper.usageBy["alice@example.com-auth"] = usage
		},
		"the account names nobody": func(h *harness, alice, _ string) {
			if err := h.settings(alice, ports.ProviderAccountAction{OnLimit: new("")}); err != nil {
				h.t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, alice, bob := spentAccount(t)
			arrange(h, alice, bob)
			before := h.store.get()
			if err := h.svc.switchOnLimit(h.ctx); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(h.store.get(), before) || h.view(alice).Moved != nil || h.defaultOf("codex") != alice {
				t.Fatalf("state=%+v moved=%+v", h.store.get(), h.view(alice).Moved)
			}
			if asked := len(h.helper.usageFor) > 0; asked != (name != "the account names nobody") {
				t.Fatalf("usage read=%t", asked)
			}
		})
	}
}

func TestRunMovesSessionsOffAnAccountThatRanOutOnceAMinute(t *testing.T) {
	h, alice, bob := spentAccount(t)
	h.svc.tick = time.Millisecond
	pushes := h.helper.applies
	ctx, stop := context.WithCancel(h.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.svc.Run(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for h.defaultOf("codex") != bob && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	stop()
	<-done
	h.route("s1", bob)
	// The sixth pass is the first to look: six syncs, then the move itself.
	if moved := h.view(alice).Moved; moved == nil || moved.Sessions != 2 || h.helper.applies-pushes < 7 {
		t.Fatalf("moved=%+v pushes=%d", moved, h.helper.applies-pushes)
	}
}
