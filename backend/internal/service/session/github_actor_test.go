package session

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeIdentityResolver struct {
	identity ports.SCMIdentity
	err      error
}

func (f *fakeIdentityResolver) AuthenticatedIdentityForProvider(context.Context, string, string) (ports.SCMIdentity, error) {
	return f.identity, f.err
}

func TestGithubActorGatesOnIdentity(t *testing.T) {
	human := ports.SCMIdentity{Login: "octocat", Human: true}
	cases := []struct {
		name      string
		identity  ports.ScopedIdentityResolver
		wantLogin string
		wantOK    bool
	}{
		{
			name:      "human account resolves",
			identity:  &fakeIdentityResolver{identity: human},
			wantLogin: "octocat",
			wantOK:    true,
		},
		{
			name:     "resolver nil stays anonymous",
			identity: nil,
		},
		{
			name:     "identity error stays anonymous",
			identity: &fakeIdentityResolver{err: errors.New("GET /user failed")},
		},
		{
			name:     "non-human account stays anonymous",
			identity: &fakeIdentityResolver{identity: ports.SCMIdentity{Login: "acme-org", Human: false}},
		},
		{
			name:     "empty login stays anonymous",
			identity: &fakeIdentityResolver{identity: ports.SCMIdentity{Login: "", Human: true}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{githubIdentity: tc.identity}
			login, ok := svc.githubActor(context.Background())
			if ok != tc.wantOK || login != tc.wantLogin {
				t.Fatalf("githubActor = (%q, %v), want (%q, %v)", login, ok, tc.wantLogin, tc.wantOK)
			}
		})
	}
}

// bestEffortResolver implements both the authoritative and the best-effort
// identity interfaces so githubActor's fallback tier can be exercised.
type bestEffortResolver struct {
	identity      ports.SCMIdentity
	identityErr   error
	bestEffort    string
	bestEffortErr error
	beCalls       int
}

func (f *bestEffortResolver) AuthenticatedIdentityForProvider(context.Context, string, string) (ports.SCMIdentity, error) {
	return f.identity, f.identityErr
}

func (f *bestEffortResolver) BestEffortLoginForProvider(context.Context, string, string) (string, error) {
	f.beCalls++
	return f.bestEffort, f.bestEffortErr
}

func TestGithubActorFallsBackToBestEffort(t *testing.T) {
	t.Run("authoritative human wins, best-effort not consulted", func(t *testing.T) {
		r := &bestEffortResolver{identity: ports.SCMIdentity{Login: "octocat", Human: true}, bestEffort: "ssh-user"}
		login, ok := (&Service{githubIdentity: r}).githubActor(context.Background())
		if !ok || login != "octocat" {
			t.Fatalf("githubActor = (%q, %v), want (octocat, true)", login, ok)
		}
		if r.beCalls != 0 {
			t.Fatalf("best-effort called %d times; want 0 when authoritative succeeds", r.beCalls)
		}
	})

	t.Run("best-effort used when no credential is configured", func(t *testing.T) {
		r := &bestEffortResolver{identityErr: ports.ErrSCMNoCredentials, bestEffort: "Pulkit7070"}
		login, ok := (&Service{githubIdentity: r}).githubActor(context.Background())
		if !ok || login != "Pulkit7070" {
			t.Fatalf("githubActor = (%q, %v), want (Pulkit7070, true)", login, ok)
		}
	})

	t.Run("non-human token stays anonymous without best-effort", func(t *testing.T) {
		r := &bestEffortResolver{identity: ports.SCMIdentity{Login: "acme-org", Human: false}, bestEffort: "Pulkit7070"}
		login, ok := (&Service{githubIdentity: r}).githubActor(context.Background())
		if ok || login != "" {
			t.Fatalf("githubActor = (%q, %v), want empty anonymous (respect configured token)", login, ok)
		}
		if r.beCalls != 0 {
			t.Fatalf("best-effort called %d times; want 0 when a token resolves to a non-human account", r.beCalls)
		}
	})

	t.Run("transient error stays anonymous without best-effort", func(t *testing.T) {
		r := &bestEffortResolver{identityErr: errors.New("GET /user: 503"), bestEffort: "Pulkit7070"}
		login, ok := (&Service{githubIdentity: r}).githubActor(context.Background())
		if ok || login != "" {
			t.Fatalf("githubActor = (%q, %v), want empty anonymous (do not guess on transient failure)", login, ok)
		}
		if r.beCalls != 0 {
			t.Fatalf("best-effort called %d times; want 0 on a transient (non-no-credentials) error", r.beCalls)
		}
	})

	t.Run("anonymous when no credential and no local signal", func(t *testing.T) {
		r := &bestEffortResolver{identityErr: ports.ErrSCMNoCredentials, bestEffort: ""}
		if login, ok := (&Service{githubIdentity: r}).githubActor(context.Background()); ok || login != "" {
			t.Fatalf("githubActor = (%q, %v), want empty anonymous", login, ok)
		}
	})

	t.Run("anonymous when best-effort errors", func(t *testing.T) {
		r := &bestEffortResolver{identityErr: ports.ErrSCMNoCredentials, bestEffortErr: errors.New("ssh failed")}
		if login, ok := (&Service{githubIdentity: r}).githubActor(context.Background()); ok || login != "" {
			t.Fatalf("githubActor = (%q, %v), want empty anonymous", login, ok)
		}
	})
}

func TestGitHubConnectedEmitsHandle(t *testing.T) {
	sink := &fakeTelemetrySink{}
	svc := NewWithDeps(Deps{
		Telemetry:      sink,
		GithubIdentity: &fakeIdentityResolver{identity: ports.SCMIdentity{Login: "octocat", Human: true}},
	})
	svc.runBackground = runInline

	svc.GitHubConnected(context.Background())

	if len(sink.events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Name != "ao.github.connected" {
		t.Fatalf("event name = %q, want ao.github.connected", ev.Name)
	}
	if ev.Payload["github_actor"] != "octocat" {
		t.Fatalf("payload = %#v, want github_actor octocat", ev.Payload)
	}
}

// A connect that does not resolve to a human login has nothing worth sending,
// so no event is emitted rather than an empty one.
func TestGitHubConnectedSendsNothingWithoutHandle(t *testing.T) {
	for name, identity := range map[string]ports.ScopedIdentityResolver{
		"lookup fails": &fakeIdentityResolver{err: errors.New("GET /user failed")},
		"org account":  &fakeIdentityResolver{identity: ports.SCMIdentity{Login: "acme-org"}},
		"no resolver":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			sink := &fakeTelemetrySink{}
			svc := NewWithDeps(Deps{Telemetry: sink, GithubIdentity: identity})
			svc.runBackground = runInline

			svc.GitHubConnected(context.Background())

			if len(sink.events) != 0 {
				t.Fatalf("emitted %#v, want nothing", sink.events)
			}
		})
	}
}

// Each handle-carrying event is gated on its own kill switch, so silencing
// ao.session.spawned must not silence ao.github.connected, and a disabled
// connect event must not spend a GitHub lookup.
func TestGitHubConnectedHonorsItsOwnKillSwitch(t *testing.T) {
	for name, tc := range map[string]struct {
		events    map[string]bool
		wantEmits int
	}{
		"spawned disabled, connect enabled": {events: map[string]bool{"ao.github.connected": true}, wantEmits: 1},
		"connect disabled":                  {events: map[string]bool{"ao.session.spawned": true}, wantEmits: 0},
	} {
		t.Run(name, func(t *testing.T) {
			sink := &fakeTelemetrySink{}
			resolver := &countingIdentityResolver{identity: ports.SCMIdentity{Login: "octocat", Human: true}}
			svc := NewWithDeps(Deps{Telemetry: sink, GithubIdentity: resolver, GithubActorEvents: tc.events})
			svc.runBackground = runInline

			svc.GitHubConnected(context.Background())

			if len(sink.events) != tc.wantEmits {
				t.Fatalf("emitted %d events, want %d", len(sink.events), tc.wantEmits)
			}
			if tc.wantEmits == 0 && resolver.calls != 0 {
				t.Fatalf("resolver called %d times for a disabled event, want 0", resolver.calls)
			}
		})
	}
}

type countingIdentityResolver struct {
	identity ports.SCMIdentity
	calls    int
}

func (c *countingIdentityResolver) AuthenticatedIdentityForProvider(context.Context, string, string) (ports.SCMIdentity, error) {
	c.calls++
	return c.identity, nil
}
