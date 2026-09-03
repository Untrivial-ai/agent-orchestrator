package gitservice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type brokerFixture struct {
	t            *testing.T
	s            *sqlite.Store
	svc          *Service
	repo, remote string
	session      domain.SessionRecord
}

func newBrokerFixture(t *testing.T) *brokerFixture {
	t.Helper()
	root := t.TempDir()
	remote, repo := filepath.Join(root, "remote.git"), filepath.Join(root, "work")
	runGit(t, root, "init", "--bare", remote)
	runGit(t, root, "init", "-b", "main", repo)
	runGit(t, repo, "config", "user.name", "AO Test")
	runGit(t, repo, "config", "user.email", "ao@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "file.txt")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "remote", "add", "origin", remote)
	runGit(t, repo, "push", "-u", "origin", "main")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "file.txt")
	runGit(t, repo, "commit", "-m", "ready")
	s := sqlitetest.MustOpen(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.UpsertProject(context.Background(), domain.ProjectRecord{ID: "project", Path: repo, DisplayName: "Project", RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	session, err := s.CreateSession(context.Background(), domain.SessionRecord{ProjectID: "project", Kind: domain.KindWorker,
		Harness: domain.HarnessFake, Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		Metadata: domain.SessionMetadata{Branch: "main", WorkspacePath: repo, WorkspaceRepoPath: repo}, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(s)
	svc.allowLocalRemotes = true
	return &brokerFixture{t: t, s: s, svc: svc, repo: repo, remote: remote, session: session}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *brokerFixture) prepare(t *testing.T) Proposal {
	t.Helper()
	p, err := f.svc.Prepare(context.Background(), PrepareInput{SessionID: string(f.session.ID), RequestedBy: "test-user"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBrokerPushesExactApprovedHeadAndAudits(t *testing.T) {
	f := newBrokerFixture(t)
	p := f.prepare(t)
	result, err := f.svc.ApproveAndPush(context.Background(), p.ApprovalID, "human@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != domain.PushApprovalConsumed {
		t.Fatalf("status = %s", result.Status)
	}
	if got := runGit(t, f.remote, "rev-parse", "refs/heads/main"); got != p.HeadSHA {
		t.Fatalf("remote head = %s, want %s", got, p.HeadSHA)
	}
	audits, err := f.s.ListGitActionAuditsByApproval(context.Background(), p.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.GitAction{domain.GitActionPushRequested, domain.GitActionPushApproved, domain.GitActionPushStarted, domain.GitActionPushSucceeded}
	if len(audits) != len(want) {
		t.Fatalf("audits = %#v", audits)
	}
	for i := range want {
		if audits[i].Action != want[i] {
			t.Fatalf("audit %d = %s", i, audits[i].Action)
		}
		if audits[i].SessionID != f.session.ID || audits[i].Repository != f.repo || audits[i].HeadSHA != p.HeadSHA {
			t.Fatalf("audit %d lost Git binding: %#v", i, audits[i])
		}
	}
}

func TestBrokerRejectsChangedHeadBeforeConsumption(t *testing.T) {
	f := newBrokerFixture(t)
	p := f.prepare(t)
	if err := os.WriteFile(filepath.Join(f.repo, "later.txt"), []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.repo, "add", "later.txt")
	runGit(t, f.repo, "commit", "-m", "changed after request")
	if _, err := f.svc.ApproveAndPush(context.Background(), p.ApprovalID, "human"); !errorsIs(err, ErrRepositoryRace) {
		t.Fatalf("error = %v", err)
	}
	if got := runGit(t, f.remote, "rev-parse", "refs/heads/main"); got == runGit(t, f.repo, "rev-parse", "HEAD") {
		t.Fatal("changed HEAD was pushed")
	}
	audits, err := f.s.ListGitActionAuditsByApproval(context.Background(), p.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if audits[len(audits)-1].Action != domain.GitActionPushRejected {
		t.Fatalf("last audit = %s", audits[len(audits)-1].Action)
	}
}

func TestBrokerPersistsFailedPushAudit(t *testing.T) {
	f := newBrokerFixture(t)
	p := f.prepare(t)
	hook := filepath.Join(f.remote, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho rejected-for-test >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ApproveAndPush(context.Background(), p.ApprovalID, "human"); err == nil {
		t.Fatal("push unexpectedly succeeded")
	}
	rec, ok, err := f.s.GetPushApproval(context.Background(), p.ApprovalID)
	if err != nil || !ok {
		t.Fatalf("get approval: %v, %v", ok, err)
	}
	if rec.Status != domain.PushApprovalFailed {
		t.Fatalf("status = %s", rec.Status)
	}
	audits, err := f.s.ListGitActionAuditsByApproval(context.Background(), p.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if audits[len(audits)-1].Action != domain.GitActionPushFailed {
		t.Fatalf("last audit = %s", audits[len(audits)-1].Action)
	}
}

func TestBrokerRejectsChangedBranch(t *testing.T) {
	f := newBrokerFixture(t)
	p := f.prepare(t)
	runGit(t, f.repo, "switch", "-c", "other")
	if _, err := f.svc.ApproveAndPush(context.Background(), p.ApprovalID, "human"); !errorsIs(err, ErrRepositoryRace) {
		t.Fatalf("error = %v", err)
	}
}

func TestBrokerRejectsChangedRemote(t *testing.T) {
	f := newBrokerFixture(t)
	p := f.prepare(t)
	other := filepath.Join(filepath.Dir(f.remote), "other.git")
	runGit(t, filepath.Dir(f.remote), "init", "--bare", other)
	runGit(t, f.repo, "remote", "set-url", "--push", "origin", other)
	if _, err := f.svc.ApproveAndPush(context.Background(), p.ApprovalID, "human"); !errorsIs(err, ErrRepositoryRace) {
		t.Fatalf("error = %v", err)
	}
}

func TestBrokerRejectsExpiredApproval(t *testing.T) {
	f := newBrokerFixture(t)
	now := time.Now().UTC()
	f.svc.now = func() time.Time { return now }
	f.svc.ttl = time.Minute
	p := f.prepare(t)
	now = now.Add(2 * time.Minute)
	if _, err := f.svc.ApproveAndPush(context.Background(), p.ApprovalID, "human"); !errorsIs(err, ErrNotApprovable) {
		t.Fatalf("error = %v", err)
	}
}

func TestBrokerConsumesApprovalOnceUnderConcurrency(t *testing.T) {
	f := newBrokerFixture(t)
	p := f.prepare(t)
	var successes atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := f.svc.ApproveAndPush(context.Background(), p.ApprovalID, "human"); err == nil {
				successes.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful pushes = %d, want 1", got)
	}
}

func TestBrokerRejectsMissingAndAlreadyConsumedApproval(t *testing.T) {
	f := newBrokerFixture(t)
	if _, err := f.svc.ApproveAndPush(context.Background(), "missing", "human"); !errorsIs(err, ErrNotFound) {
		t.Fatalf("missing approval error = %v", err)
	}
	p := f.prepare(t)
	if _, err := f.svc.ApproveAndPush(context.Background(), p.ApprovalID, "human"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ApproveAndPush(context.Background(), p.ApprovalID, "human"); !errorsIs(err, ErrNotApprovable) {
		t.Fatalf("reuse error = %v", err)
	}
}

func errorsIs(err, target error) bool {
	if err == nil {
		return false
	}
	for {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
}

func TestRemoteURLRejectsEmbeddedSecrets(t *testing.T) {
	for _, raw := range []string{
		"https://token@example.test/repo.git",
		"https://example.test/repo.git?token=secret",
		"http://example.test/repo.git",
		"git://example.test/repo.git",
		"file:///tmp/repo.git",
		"ext::sh -c evil",
		"helper::payload",
		"ftp://example.test/repo.git",
		"git@example.test:owner/repo.git;touch-pwned",
		"ssh://git@example.test/owner/repo.git%20-oProxyCommand=evil",
		t.TempDir(),
	} {
		if err := validateRemoteURL(raw); err == nil {
			t.Fatalf("validateRemoteURL(%q) succeeded", raw)
		}
	}
	for _, raw := range []string{
		"https://example.test/owner/repo.git",
		"git@example.test:owner/repo.git",
		"ssh://git@example.test/owner/repo.git",
	} {
		if err := validateRemoteURL(raw); err != nil {
			t.Fatalf("valid remote %q rejected: %v", raw, err)
		}
	}
}

func TestRemoteNameRejectsOptionAndControlInjection(t *testing.T) {
	for _, remote := range []string{"--all", "origin\nmalicious", "origin name", "helper::name", `origin\name`} {
		if err := validateRemoteName(remote); err == nil {
			t.Fatalf("validateRemoteName(%q) succeeded", remote)
		}
	}
	if err := validateRemoteName("origin"); err != nil {
		t.Fatalf("origin rejected: %v", err)
	}
}

func TestBranchValidationUsesGitNativeRules(t *testing.T) {
	svc := New(nil)
	if err := svc.validateBranch(context.Background(), t.TempDir(), "feature/safe"); err != nil {
		t.Fatalf("valid branch rejected: %v", err)
	}
	for _, branch := range []string{"bad..branch", "-option", "bad branch"} {
		if err := svc.validateBranch(context.Background(), t.TempDir(), branch); err == nil {
			t.Fatalf("invalid branch %q accepted", branch)
		}
	}
}

func TestBrokerCancellationIsAuditedAndCannotBeReused(t *testing.T) {
	f := newBrokerFixture(t)
	p := f.prepare(t)
	if err := f.svc.Revoke(context.Background(), p.ApprovalID, "desktop-user"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ApproveAndPush(context.Background(), p.ApprovalID, "human"); !errorsIs(err, ErrNotApprovable) {
		t.Fatalf("cancelled approval reuse error = %v", err)
	}
	audits, err := f.s.ListGitActionAuditsByApproval(context.Background(), p.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if got := audits[len(audits)-1].Action; got != domain.GitActionPushCancelled {
		t.Fatalf("last audit = %s", got)
	}
}

func TestPushStartedAuditUsesCrashSafeUnknownResult(t *testing.T) {
	f := newBrokerFixture(t)
	p := f.prepare(t)
	if _, err := f.svc.ApproveAndPush(context.Background(), p.ApprovalID, "human"); err != nil {
		t.Fatal(err)
	}
	audits, err := f.s.ListGitActionAuditsByApproval(context.Background(), p.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	for _, audit := range audits {
		if audit.Action == domain.GitActionPushStarted && audit.Result != "UNKNOWN" {
			t.Fatalf("push-start result = %q", audit.Result)
		}
	}
}

func TestBrokerEnvironmentForcesNonInteractiveGit(t *testing.T) {
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GCM_INTERACTIVE", "Always")
	values := map[string]string{}
	for _, entry := range brokerEnvironment() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[strings.ToUpper(key)] = value
		}
	}
	if values["GIT_TERMINAL_PROMPT"] != "0" || values["GCM_INTERACTIVE"] != "Never" {
		t.Fatalf("broker env = %#v", values)
	}
}

func TestBrokerPushDoesNotExecuteRepositoryPrePushHook(t *testing.T) {
	f := newBrokerFixture(t)
	p := f.prepare(t)
	marker := filepath.Join(t.TempDir(), "pre-push-ran")
	hook := filepath.Join(f.repo, ".git", "hooks", "pre-push")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho invoked > '"+filepath.ToSlash(marker)+"'\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ApproveAndPush(context.Background(), p.ApprovalID, "human"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Worker pre-push hook executed: %v", err)
	}
}

func TestBrokerPushIgnoresRepositoryURLRewrite(t *testing.T) {
	f := newBrokerFixture(t)
	head := runGit(t, f.repo, "rev-parse", "HEAD")
	other := filepath.Join(filepath.Dir(f.remote), "rewrite-target.git")
	runGit(t, filepath.Dir(f.remote), "init", "--bare", other)
	runGit(t, f.repo, "config", "--local", "url."+other+".insteadOf", f.remote)
	rec := domain.PushApproval{Repository: f.repo, RemoteURL: f.remote, Branch: "main", ExpectedHeadSHA: head}
	if _, err := f.svc.pushApproved(context.Background(), rec, head+":refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, f.remote, "rev-parse", "refs/heads/main"); got != head {
		t.Fatalf("approved remote head = %s, want %s", got, head)
	}
	cmd := exec.Command("git", "rev-parse", "--verify", "refs/heads/main")
	cmd.Dir = other
	if err := cmd.Run(); err == nil {
		t.Fatal("repository-local URL rewrite changed the approved destination")
	}
}

func TestBrokerPushIgnoresRepositoryCredentialHelperAndSSHCommand(t *testing.T) {
	for _, tc := range []struct {
		name, key, remote string
	}{
		{"credential helper", "credential.helper", "http://127.0.0.1:1/repo.git"},
		{"ssh command", "core.sshCommand", "ssh://git@127.0.0.1:1/repo.git"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBrokerFixture(t)
			remote := tc.remote
			if tc.key == "credential.helper" {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("WWW-Authenticate", `Basic realm="helper-test"`)
					http.Error(w, "authentication required", http.StatusUnauthorized)
				}))
				defer server.Close()
				remote = server.URL + "/repo.git"
			}
			marker := filepath.Join(t.TempDir(), "malicious-command-ran")
			command := "!sh -c \"echo invoked > '" + filepath.ToSlash(marker) + "'; exit 1\""
			if tc.key == "core.sshCommand" {
				command = "sh -c \"echo invoked > '" + filepath.ToSlash(marker) + "'; exit 1\""
			}
			runGit(t, f.repo, "config", "--local", tc.key, command)
			head := runGit(t, f.repo, "rev-parse", "HEAD")
			rec := domain.PushApproval{Repository: f.repo, RemoteURL: remote, Branch: "main", ExpectedHeadSHA: head}
			_, _ = f.svc.pushApproved(context.Background(), rec, head+":refs/heads/main")
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("repository-local %s executed: %v", tc.key, err)
			}
		})
	}
}

func TestBrokerPushDoesNotSendRepositoryAuthorizationHeader(t *testing.T) {
	f := newBrokerFixture(t)
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
	}))
	defer server.Close()
	runGit(t, f.repo, "config", "--local", "http."+server.URL+".extraHeader", "Authorization: Bearer WORKER_SECRET")
	head := runGit(t, f.repo, "rev-parse", "HEAD")
	rec := domain.PushApproval{Repository: f.repo, RemoteURL: server.URL + "/repo.git", Branch: "main", ExpectedHeadSHA: head}
	_, _ = f.svc.pushApproved(context.Background(), rec, head+":refs/heads/main")
	if authorization != "" {
		t.Fatalf("repository-local Authorization header reached Broker request: %q", authorization)
	}
}
