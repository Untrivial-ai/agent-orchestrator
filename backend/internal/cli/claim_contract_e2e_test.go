package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type claimContractSCM struct {
	observation ports.SCMObservation
}

// claimContractSpawnService keeps the production ClaimPR implementation while
// returning the pre-seeded workspace session from Spawn, avoiding an agent
// process launch in this HTTP contract test.
type claimContractSpawnService struct {
	*sessionsvc.Service
	spawned domain.Session
}

func (s claimContractSpawnService) Spawn(context.Context, ports.SpawnConfig) (domain.Session, int, int, error) {
	return s.spawned, 0, 0, nil
}

func (claimContractSCM) ParseRepository(string) (ports.SCMRepo, bool) {
	return ports.SCMRepo{
		Provider: "github",
		Host:     "github.com",
		Owner:    "acme",
		Name:     "repo",
		Repo:     "acme/repo",
	}, true
}

func (s claimContractSCM) FetchPullRequests(context.Context, []ports.SCMPRRef) ([]ports.SCMObservation, error) {
	return []ports.SCMObservation{s.observation}, nil
}

func (claimContractSCM) FetchReviewThreads(context.Context, ports.SCMPRRef) (ports.SCMReviewObservation, error) {
	return ports.SCMReviewObservation{}, nil
}

// This guards the complete metadata-only claim contract for both CLI forms:
// the production service's BranchChanged=false must survive controller
// serialization and CLI decoding, while the command leaves the real workspace
// branch and HEAD alone.
func TestE2E_ClaimPRMetadataOnlyContract(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spawn bool
	}{
		{name: "session claim-pr"},
		{name: "spawn --claim-pr", spawn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			runClaimContractGit(t, repo, "init")
			runClaimContractGit(t, repo, "config", "user.email", "ao@example.com")
			runClaimContractGit(t, repo, "config", "user.name", "AO Tests")
			runClaimContractGit(t, repo, "commit", "--allow-empty", "-m", "worker base")
			runClaimContractGit(t, repo, "branch", "ao/demo-1/root")
			runClaimContractGit(t, repo, "checkout", "-b", "pr-topic")
			runClaimContractGit(t, repo, "commit", "--allow-empty", "-m", "PR head")
			prHead := strings.TrimSpace(runClaimContractGit(t, repo, "rev-parse", "HEAD"))
			runClaimContractGit(t, repo, "checkout", "ao/demo-1/root")
			beforeBranch := strings.TrimSpace(runClaimContractGit(t, repo, "branch", "--show-current"))
			beforeHead := strings.TrimSpace(runClaimContractGit(t, repo, "rev-parse", "HEAD"))
			if beforeHead == prHead {
				t.Fatal("fixture must start the workspace away from the PR head")
			}

			ctx := context.Background()
			store := sqlitetest.MustOpen(t)
			now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
			if err := store.UpsertProject(ctx, domain.ProjectRecord{
				ID:            "demo",
				Path:          repo,
				RepoOriginURL: "https://github.com/acme/repo",
				RegisteredAt:  now,
			}); err != nil {
				t.Fatalf("seed project: %v", err)
			}
			session, err := store.CreateSession(ctx, domain.SessionRecord{
				ProjectID: "demo",
				Kind:      domain.KindWorker,
				Harness:   domain.HarnessCodex,
				Activity:  domain.Activity{State: domain.ActivityActive, LastActivityAt: now},
				Metadata: domain.SessionMetadata{
					Branch:        beforeBranch,
					WorkspacePath: repo,
				},
				CreatedAt: now,
				UpdatedAt: now,
			})
			if err != nil {
				t.Fatalf("seed session: %v", err)
			}

			svc := sessionsvc.NewWithDeps(sessionsvc.Deps{
				Store: store,
				SCM: claimContractSCM{observation: ports.SCMObservation{
					Fetched:  true,
					Provider: "github",
					Host:     "github.com",
					Repo:     "acme/repo",
					PR: ports.SCMPRObservation{
						URL:          "https://github.com/acme/repo/pull/7",
						Number:       7,
						SourceBranch: "pr-topic",
						HeadSHA:      prHead,
					},
				}},
			})
			if tc.spawn {
				startDriftTestDaemon(t, claimContractSpawnService{
					Service: svc,
					spawned: domain.Session{SessionRecord: session, Status: domain.StatusIdle},
				}, &fakeProjectManager{})
			} else {
				startDriftTestDaemon(t, svc, &fakeProjectManager{})
			}

			var responseBody []byte
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				resp, err := http.DefaultTransport.RoundTrip(req)
				if err != nil || req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/pr/claim") {
					return resp, err
				}
				body, readErr := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if readErr != nil {
					return nil, readErr
				}
				responseBody = body
				resp.Body = io.NopCloser(bytes.NewReader(responseBody))
				return resp, nil
			})
			var out bytes.Buffer
			root := NewRootCommand(Deps{
				Out:          &out,
				Err:          &out,
				HTTPClient:   &http.Client{Transport: transport},
				ProcessAlive: func(int) bool { return true },
			})
			args := []string{"session", "claim-pr", string(session.ID), "https://github.com/acme/repo/pull/7"}
			if tc.spawn {
				args = []string{"spawn", "--project", "demo", "--agent", "codex", "--name", "worker", "--skip-agent-check", "--claim-pr", "https://github.com/acme/repo/pull/7"}
			}
			root.SetArgs(args)
			if err := root.Execute(); err != nil {
				t.Fatalf("execute: %v\noutput: %s", err, out.String())
			}

			if want := "  checkout: not performed; workspace unchanged\n"; !strings.Contains(out.String(), want) {
				t.Fatalf("output = %q, want %q", out.String(), want)
			}
			var response map[string]json.RawMessage
			if err := json.Unmarshal(responseBody, &response); err != nil {
				t.Fatalf("decode controller response %q: %v", responseBody, err)
			}
			rawBranchChanged, ok := response["branchChanged"]
			if !ok || string(rawBranchChanged) != "false" {
				t.Fatalf("controller branchChanged = %s (present=%t), want explicit false", rawBranchChanged, ok)
			}
			if got := strings.TrimSpace(runClaimContractGit(t, repo, "branch", "--show-current")); got != beforeBranch {
				t.Fatalf("workspace branch = %q, want unchanged %q", got, beforeBranch)
			}
			if got := strings.TrimSpace(runClaimContractGit(t, repo, "rev-parse", "HEAD")); got != beforeHead {
				t.Fatalf("workspace HEAD = %q, want unchanged %q", got, beforeHead)
			}
			if got := runClaimContractGit(t, repo, "status", "--porcelain"); got != "" {
				t.Fatalf("claim modified workspace: %s", got)
			}
		})
	}
}

func runClaimContractGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %s: %v\n%s", dir, strings.Join(args, " "), err, out)
	}
	return string(out)
}
