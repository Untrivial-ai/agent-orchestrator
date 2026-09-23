package scm

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type remoteCatalogProvider struct {
	*fakeProvider
	catalog map[string]ports.SCMRepo
}

func (p *remoteCatalogProvider) ParseRepository(address string) (ports.SCMRepo, bool) {
	repository, exists := p.catalog[address]
	return repository, exists
}

func remoteAddress(repository ports.SCMRepo) string {
	return "https://" + repository.Host + "/" + repository.Repo + ".git"
}

func TestGitRemoteURLs_StopsWhenCanceled(t *testing.T) {
	directory := t.TempDir()
	mustGit(t, "init", directory)
	mustGit(t, "-C", directory, "remote", "add", "origin", "https://github.com/team/widget.git")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if urls := gitRemoteURLs(ctx, directory); len(urls) != 0 {
		t.Fatalf("git remotes after cancellation = %v; want none", urls)
	}
}

func TestPoll_RemoteAttributionMatrix(t *testing.T) {
	upstream := ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "team", Name: "widget", Repo: "team/widget"}
	personal := ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "dev", Name: "widget", Repo: "dev/widget"}
	backup := ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "backup", Name: "widget", Repo: "backup/widget"}
	labBase, labHead := upstream, personal
	labBase.Provider, labHead.Provider = "gitlab", "gitlab"
	labBase.Host, labHead.Host = "gitlab.com", "gitlab.com"
	privateHead := labHead
	privateHead.Host = "code.example"
	providerTwin := personal
	providerTwin.Provider = "gitlab"

	cases := []struct {
		name       string
		base       ports.SCMRepo
		fetch      []ports.SCMRepo
		push       []ports.SCMRepo
		pushRemote string
		heads      []string
		author     string
		branch     string
		accepted   []int
	}{
		{name: "base branch", base: upstream, heads: []string{upstream.Repo}, accepted: []int{71}},
		{name: "additional remote", base: upstream, fetch: []ports.SCMRepo{personal}, heads: []string{personal.Repo}, accepted: []int{71}},
		{name: "different push destination", base: upstream, push: []ports.SCMRepo{personal}, heads: []string{personal.Repo}, accepted: []int{71}},
		{name: "secondary remote push destination", base: upstream, fetch: []ports.SCMRepo{backup}, push: []ports.SCMRepo{personal}, pushRemote: "extra-0", heads: []string{personal.Repo}, accepted: []int{71}},
		{name: "all push destinations", base: upstream, push: []ports.SCMRepo{backup, personal, backup}, heads: []string{backup.Repo, personal.Repo}, accepted: []int{71, 72}},
		{name: "overlapping fetch and push destinations", base: upstream, fetch: []ports.SCMRepo{personal}, push: []ports.SCMRepo{personal}, heads: []string{personal.Repo}, accepted: []int{71}},
		{name: "provider mismatch", base: upstream, fetch: []ports.SCMRepo{labHead}, heads: []string{personal.Repo}},
		{name: "provider mismatch on same host", base: upstream, fetch: []ports.SCMRepo{providerTwin}, heads: []string{personal.Repo}},
		{name: "host mismatch", base: labBase, push: []ports.SCMRepo{privateHead}, heads: []string{labHead.Repo}},
		{name: "valid host beside another host", base: labBase, fetch: []ports.SCMRepo{privateHead}, push: []ports.SCMRepo{labHead}, heads: []string{labHead.Repo}, accepted: []int{71}},
		{name: "head absent from configuration", base: upstream, fetch: []ports.SCMRepo{backup}, heads: []string{personal.Repo}},
		{name: "head no longer exists", base: upstream, fetch: []ports.SCMRepo{personal}, heads: []string{""}},
		{name: "different author", base: upstream, fetch: []ports.SCMRepo{personal}, heads: []string{personal.Repo}, author: "someone-else"},
		{name: "unrelated branch", base: upstream, push: []ports.SCMRepo{personal}, heads: []string{personal.Repo}, branch: "other-work"},
		{name: "stacked branch", base: upstream, push: []ports.SCMRepo{personal}, heads: []string{personal.Repo}, branch: "delivery/followup", accepted: []int{71}},
		{name: "head name case folding", base: upstream, fetch: []ports.SCMRepo{personal}, heads: []string{strings.ToUpper(personal.Repo)}, accepted: []int{71}},
	}
	for _, layout := range []struct {
		name string
		kind domain.ProjectKind
	}{
		{"repository", domain.ProjectKindSingleRepo},
		{"workspace", domain.ProjectKindWorkspace},
	} {
		for _, scenario := range cases {
			t.Run(layout.name+"/"+scenario.name, func(t *testing.T) {
				parent := t.TempDir()
				checkout := parent
				project := domain.ProjectRecord{ID: "kit", Path: parent, Kind: layout.kind}
				if layout.kind == domain.ProjectKindWorkspace {
					checkout = filepath.Join(parent, "component")
				} else {
					project.RepoOriginURL = remoteAddress(scenario.base)
				}
				mustGit(t, "init", checkout)
				mustGit(t, "-C", checkout, "remote", "add", "origin", remoteAddress(scenario.base))
				provider := remotePollProvider(scenario.base, scenario.heads, scenario.author, scenario.branch)
				for index, destination := range scenario.fetch {
					address := remoteAddress(destination)
					provider.catalog[address] = destination
					mustGit(t, "-C", checkout, "remote", "add", fmt.Sprintf("extra-%d", index), address)
				}
				for _, destination := range scenario.push {
					address := remoteAddress(destination)
					provider.catalog[address] = destination
					remote := scenario.pushRemote
					if remote == "" {
						remote = "origin"
					}
					mustGit(t, "-C", checkout, "config", "--add", "remote."+remote+".pushurl", address)
				}
				store := remotePollStore(project)
				if layout.kind == domain.ProjectKindWorkspace {
					store.workspaceRepos["kit"] = []domain.WorkspaceRepoRecord{{Name: "component", RelativePath: "component", RepoOriginURL: remoteAddress(scenario.base)}}
				}
				verifyRemotePoll(t, store, provider, scenario.base, scenario.accepted)
			})
		}
	}
}

func TestPoll_SharedBaseCheckoutOrder(t *testing.T) {
	shared := ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "team", Name: "widget", Repo: "team/widget"}
	left := ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "left", Name: "widget", Repo: "left/widget"}
	right := ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "right", Name: "widget", Repo: "right/widget"}
	for _, order := range []struct {
		name     string
		children []ports.SCMRepo
	}{
		{"left then right", []ports.SCMRepo{left, right}},
		{"right then left", []ports.SCMRepo{right, left}},
	} {
		t.Run(order.name, func(t *testing.T) {
			parent := t.TempDir()
			store := remotePollStore(domain.ProjectRecord{ID: "kit", Path: parent, Kind: domain.ProjectKindWorkspace})
			provider := remotePollProvider(shared, []string{left.Repo, right.Repo}, "", "")
			for _, destination := range order.children {
				directory := filepath.Join(parent, destination.Owner)
				mustGit(t, "init", directory)
				mustGit(t, "-C", directory, "remote", "add", "origin", remoteAddress(shared))
				mustGit(t, "-C", directory, "remote", "add", "publish", remoteAddress(destination))
				provider.catalog[remoteAddress(destination)] = destination
				store.workspaceRepos["kit"] = append(store.workspaceRepos["kit"], domain.WorkspaceRepoRecord{
					Name: destination.Owner, RelativePath: destination.Owner, RepoOriginURL: remoteAddress(shared),
				})
			}
			// The right checkout must not authorize a head for the left fork itself.
			unrelated := provider.openPRs[prKey(shared, 0)][1]
			unrelated.Number = 90
			unrelated.URL = "https://github.com/left/widget/pull/90"
			provider.openPRs[prKey(left, 0)] = []ports.SCMPRObservation{unrelated}
			verifyRemotePoll(t, store, provider, shared, []int{71, 72})
		})
	}
}

func remotePollStore(project domain.ProjectRecord) *fakeStore {
	return &fakeStore{
		projects:       map[string]domain.ProjectRecord{"kit": project},
		sessions:       []domain.SessionRecord{{ID: "worker-7", ProjectID: "kit", Metadata: domain.SessionMetadata{Branch: "delivery"}}},
		workspaceRepos: make(map[string][]domain.WorkspaceRepoRecord),
	}
}

func remotePollProvider(base ports.SCMRepo, heads []string, author, branch string) *remoteCatalogProvider {
	if author == "" {
		author = "builder"
	}
	if branch == "" {
		branch = "delivery"
	}
	provider := &remoteCatalogProvider{
		fakeProvider: &fakeProvider{
			identity:     ports.SCMIdentity{Login: "builder", Human: true},
			openPRs:      make(map[string][]ports.SCMPRObservation),
			observations: make(map[string]ports.SCMObservation),
		},
		catalog: map[string]ports.SCMRepo{remoteAddress(base): base},
	}
	for index, repositoryName := range heads {
		number := 71 + index
		address := fmt.Sprintf("https://%s/%s/pull/%d", base.Host, base.Repo, number)
		listing := ports.SCMPRObservation{URL: address, Number: number, HeadRepo: repositoryName, SourceBranch: branch, TargetBranch: "trunk", Author: author, HeadSHA: "revision", State: "open"}
		provider.openPRs[prKey(base, 0)] = append(provider.openPRs[prKey(base, 0)], listing)
		provider.observations[prKey(base, number)] = ports.SCMObservation{
			Fetched: true, Provider: base.Provider, Host: base.Host, Repo: base.Repo, PR: listing,
			CI: ports.SCMCIObservation{Summary: string(domain.CIPassing), HeadSHA: listing.HeadSHA},
		}
	}
	return provider
}

func verifyRemotePoll(t *testing.T, store *fakeStore, provider *remoteCatalogProvider, base ports.SCMRepo, expected []int) {
	t.Helper()
	lifecycle := &fakeLifecycle{}
	observer := New(provider, store, lifecycle, Config{
		Clock: func() time.Time { return time.Unix(500, 0) }, Logger: quietSlog(), ScopedIdentityResolver: provider,
	})
	if err := observer.Poll(context.Background()); err != nil {
		t.Fatalf("remote discovery poll returned an error: %v", err)
	}
	var fetched []int
	for _, batch := range provider.fetchBatches {
		for _, reference := range batch {
			if reference.Repo != base {
				t.Errorf("refresh routed to %+v instead of base %+v", reference.Repo, base)
			}
			fetched = append(fetched, reference.Number)
		}
	}
	slices.Sort(fetched)
	if !slices.Equal(fetched, expected) {
		t.Fatalf("refreshed request numbers %v; expected %v", fetched, expected)
	}
	persisted := make(map[int]bool)
	for _, write := range store.writes {
		if write.pr.SessionID != "worker-7" || write.pr.Provider != base.Provider || write.pr.Host != base.Host || write.pr.Repo != base.Repo {
			t.Errorf("persisted ownership or repository is incorrect: %+v", write.pr)
		}
		persisted[write.pr.Number] = true
	}
	wanted := make(map[int]bool)
	for _, number := range expected {
		wanted[number] = true
	}
	if !reflect.DeepEqual(persisted, wanted) {
		t.Errorf("stored request numbers %v; expected %v", persisted, wanted)
	}
	if len(lifecycle.observed) != len(expected) {
		t.Errorf("lifecycle received %d observations for %d accepted requests", len(lifecycle.observed), len(expected))
	}
	destinations := make(map[string]bool)
	for _, repository := range provider.catalog {
		destinations[strings.ToLower(prKey(repository, 0))] = true
	}
	if provider.listCalls != len(destinations) || provider.repoGuardCalls != len(destinations) {
		t.Errorf("scan totals do not match configured repositories: lists=%d guards=%d distinct=%d", provider.listCalls, provider.repoGuardCalls, len(destinations))
	}
}
