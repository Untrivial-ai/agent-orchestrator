package project_test

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// gitRepoWithRemote builds a committed repository whose origin is remote, so
// the project-added telemetry path sees a real remote URL to derive an owner
// from.
func gitRepoWithRemote(t *testing.T, remote string) string {
	t.Helper()
	dir := gitRepo(t)
	if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", remote).CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v (%s)", err, out)
	}
	return dir
}

// stubClassifier answers ClassifyRepoOwner with a fixed result. release, when
// non-nil, is waited on first so a test can prove Add does not block on the
// lookup.
type stubClassifier struct {
	ownerType string
	err       error
	release   chan struct{}
	owners    chan string
}

func (c *stubClassifier) ClassifyRepoOwner(ctx context.Context, owner string) (string, error) {
	if c.owners != nil {
		select {
		case c.owners <- owner:
		default:
		}
	}
	if c.release != nil {
		select {
		case <-c.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return c.ownerType, c.err
}

func addProjectForTelemetry(t *testing.T, sink *captureSink, classifier project.RepoOwnerClassifier, remote string) {
	t.Helper()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	m := project.NewWithDeps(project.Deps{Store: store, Telemetry: sink, RepoOwners: classifier})
	if _, err := m.Add(context.Background(), project.AddInput{Path: gitRepoWithRemote(t, remote), ProjectID: ptr("ao")}); err != nil {
		t.Fatalf("Add: %v", err)
	}
}

// repo_owner is the honest name for what has always been captured: the owner
// segment of the remote. github_org keeps shipping alongside it for one
// release so dashboards built on the old name keep resolving.
func TestManager_AddEmitsRepoOwnerAndDeprecatedGithubOrg(t *testing.T) {
	sink := &captureSink{}
	addProjectForTelemetry(t, sink, nil, "git@github.com:aoagents/agent-orchestrator.git")

	events := sink.waitFor(t, 2)
	for _, ev := range events {
		if ev.Payload["repo_owner"] != "aoagents" {
			t.Fatalf("%s repo_owner = %#v, want aoagents", ev.Name, ev.Payload["repo_owner"])
		}
		if ev.Payload["github_org"] != "aoagents" {
			t.Fatalf("%s github_org = %#v, want the deprecated alias to still ship", ev.Name, ev.Payload["github_org"])
		}
		// Nothing beyond the owner segment may ever reach the payload.
		for _, forbidden := range []string{"repo", "repo_name", "remote", "repo_url"} {
			if _, ok := ev.Payload[forbidden]; ok {
				t.Fatalf("%s payload carries %q: %#v", ev.Name, forbidden, ev.Payload)
			}
		}
		if _, ok := ev.Payload["repo_owner_type"]; ok {
			t.Fatalf("%s carries repo_owner_type without a classifier: %#v", ev.Name, ev.Payload)
		}
	}
}

func TestManager_AddStampsRepoOwnerType(t *testing.T) {
	cases := []struct {
		name   string
		remote string
		want   string
	}{
		{"organization", "git@github.com:aoagents/agent-orchestrator.git", "Organization"},
		{"personal-account", "https://github.com/octocat/hello.git", "User"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &captureSink{}
			owners := make(chan string, 1)
			addProjectForTelemetry(t, sink, &stubClassifier{ownerType: tc.want, owners: owners}, tc.remote)

			for _, ev := range sink.waitFor(t, 2) {
				if ev.Payload["repo_owner_type"] != tc.want {
					t.Fatalf("%s repo_owner_type = %#v, want %q", ev.Name, ev.Payload["repo_owner_type"], tc.want)
				}
			}
			select {
			case got := <-owners:
				if got == "" {
					t.Fatal("classifier received an empty owner")
				}
			default:
				t.Fatal("classifier was never asked to classify the owner")
			}
		})
	}
}

// A failed classification must cost the property and nothing else: the
// project-added events still ship, still carry the owner, and Add still
// succeeded.
func TestManager_AddOmitsRepoOwnerTypeWhenClassificationFails(t *testing.T) {
	sink := &captureSink{}
	addProjectForTelemetry(t, sink, &stubClassifier{err: errors.New("rate limited")}, "git@github.com:aoagents/agent-orchestrator.git")

	for _, ev := range sink.waitFor(t, 2) {
		if _, ok := ev.Payload["repo_owner_type"]; ok {
			t.Fatalf("%s carries repo_owner_type after a failed lookup: %#v", ev.Name, ev.Payload)
		}
		if ev.Payload["repo_owner"] != "aoagents" {
			t.Fatalf("%s lost repo_owner after a failed lookup: %#v", ev.Name, ev.Payload)
		}
	}
}

// The GitHub round trip must never sit on the project-add path: Add has to
// return while the classifier is still blocked, and the events land once it
// answers.
func TestManager_AddDoesNotWaitForOwnerClassification(t *testing.T) {
	sink := &captureSink{}
	release := make(chan struct{})
	classifier := &stubClassifier{ownerType: "Organization", release: release}
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	m := project.NewWithDeps(project.Deps{Store: store, Telemetry: sink, RepoOwners: classifier})

	path := gitRepoWithRemote(t, "git@github.com:aoagents/agent-orchestrator.git")
	if _, err := m.Add(context.Background(), project.AddInput{Path: path, ProjectID: ptr("ao")}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Add returned; the classifier is still blocked, so nothing has been
	// emitted yet. Any event here would mean the lookup ran inline.
	if got := sink.snapshot(); len(got) != 0 {
		t.Fatalf("events emitted before the classifier answered: %#v", got)
	}
	close(release)
	for _, ev := range sink.waitFor(t, 2) {
		if ev.Payload["repo_owner_type"] != "Organization" {
			t.Fatalf("%s repo_owner_type = %#v", ev.Name, ev.Payload["repo_owner_type"])
		}
		if ev.OccurredAt.IsZero() || time.Since(ev.OccurredAt) > time.Minute {
			t.Fatalf("%s OccurredAt = %v, want the add time, not the send time", ev.Name, ev.OccurredAt)
		}
	}
}

// A remote AO cannot derive a GitHub owner from must not trigger a lookup at
// all, and must not invent either property.
func TestManager_AddSkipsClassificationWithoutAGitHubOwner(t *testing.T) {
	sink := &captureSink{}
	owners := make(chan string, 1)
	addProjectForTelemetry(t, sink, &stubClassifier{ownerType: "User", owners: owners}, "git@gitlab.com:group/repo.git")

	events := sink.waitFor(t, 2)
	if len(owners) != 0 {
		t.Fatalf("classifier called for a non-GitHub remote: %q", <-owners)
	}
	for _, ev := range events {
		for _, key := range []string{"repo_owner", "repo_owner_type", "github_org"} {
			if _, ok := ev.Payload[key]; ok {
				t.Fatalf("%s carries %q for a non-GitHub remote: %#v", ev.Name, key, ev.Payload)
			}
		}
		if ev.Payload["has_git_remote"] != true {
			t.Fatalf("%s has_git_remote = %#v, want true", ev.Name, ev.Payload["has_git_remote"])
		}
	}
}
