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
