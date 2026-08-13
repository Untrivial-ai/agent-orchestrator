# Workspace Child Default Branch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect, persist, and expose the default branch of every direct child repository registered in a workspace project.

**Architecture:** Extend the existing `workspace_repos` relational record with a required `default_branch` column and carry it through domain, sqlc, store, service, API, and CLI DTO boundaries. Registration reuses `resolveDefaultBranch`, whose remote-first behavior prevents an active feature branch from being mistaken for the repository default.

**Tech Stack:** Go 1.x, SQLite/goose migrations, sqlc, code-first OpenAPI generation, TypeScript generated API schema.

---

### Task 1: Persist workspace child default branches

**Files:**
- Create: `backend/internal/storage/sqlite/migrations/0022_workspace_repo_default_branch.sql`
- Modify: `backend/internal/storage/sqlite/queries/workspace.sql`
- Modify: `backend/internal/domain/project.go`
- Modify: `backend/internal/storage/sqlite/store/project_store.go`
- Test: `backend/internal/storage/sqlite/store/store_test.go`
- Generate: `backend/internal/storage/sqlite/gen/models.go`
- Generate: `backend/internal/storage/sqlite/gen/workspace.sql.go`

- [ ] **Step 1: Write the failing storage round-trip test**

Add a test that upserts a workspace project with one child and verifies the branch survives `ListWorkspaceRepos`:

```go
func TestWorkspaceReposRoundTripDefaultBranch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := domain.ProjectRecord{
		ID: "ws", Path: t.TempDir(), DisplayName: "ws",
		RegisteredAt: time.Now(), Kind: domain.ProjectKindWorkspace,
	}
	want := domain.WorkspaceRepoRecord{
		ProjectID: "ws", Name: "api", RelativePath: "api",
		RepoOriginURL: "https://github.com/acme/api.git",
		DefaultBranch: "develop", RegisteredAt: time.Now(),
	}
	if err := s.UpsertWorkspaceProject(ctx, project, []domain.WorkspaceRepoRecord{want}); err != nil {
		t.Fatalf("upsert workspace project: %v", err)
	}
	got, err := s.ListWorkspaceRepos(ctx, "ws")
	if err != nil {
		t.Fatalf("list workspace repos: %v", err)
	}
	if len(got) != 1 || got[0].DefaultBranch != "develop" {
		t.Fatalf("workspace repos = %#v, want default branch develop", got)
	}
}
```

- [ ] **Step 2: Run the test to verify RED**

Run: `cd backend && go test ./internal/storage/sqlite/store -run TestWorkspaceReposRoundTripDefaultBranch -count=1`

Expected: compilation fails because `WorkspaceRepoRecord.DefaultBranch` does not exist.

- [ ] **Step 3: Add the migration, domain field, and query columns**

Create the additive migration:

```sql
-- +goose Up
ALTER TABLE workspace_repos ADD COLUMN default_branch TEXT NOT NULL DEFAULT 'main';

-- +goose Down
ALTER TABLE workspace_repos DROP COLUMN default_branch;
```

Add to `WorkspaceRepoRecord`:

```go
DefaultBranch string
```

Update `UpsertWorkspaceRepo` to insert and update `default_branch`, and update `ListWorkspaceRepos` to select it:

```sql
INSERT INTO workspace_repos (project_id, name, relative_path, repo_origin_url, default_branch, registered_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (project_id, name) DO UPDATE SET
    relative_path = excluded.relative_path,
    repo_origin_url = excluded.repo_origin_url,
    default_branch = excluded.default_branch,
    registered_at = excluded.registered_at;

SELECT project_id, name, relative_path, repo_origin_url, default_branch, registered_at
FROM workspace_repos
WHERE project_id = ?
ORDER BY name;
```

- [ ] **Step 4: Regenerate sqlc and wire store mappings**

Run: `npm run sqlc`

Expected: exit 0; generated `WorkspaceRepo` and `UpsertWorkspaceRepoParams` include `DefaultBranch`.

Pass `repo.DefaultBranch` into `gen.UpsertWorkspaceRepoParams`, and copy `row.DefaultBranch` into the returned `domain.WorkspaceRepoRecord`.

- [ ] **Step 5: Run the storage test to verify GREEN**

Run: `cd backend && go test ./internal/storage/sqlite/store -run TestWorkspaceReposRoundTripDefaultBranch -count=1`

Expected: PASS.

- [ ] **Step 6: Commit the persistence slice**

```bash
git add backend/internal/domain/project.go backend/internal/storage/sqlite/migrations/0022_workspace_repo_default_branch.sql backend/internal/storage/sqlite/queries/workspace.sql backend/internal/storage/sqlite/gen backend/internal/storage/sqlite/store/project_store.go backend/internal/storage/sqlite/store/store_test.go
git commit -m "feat: persist workspace repo default branches"
```

### Task 2: Detect child defaults during registration

**Files:**
- Modify: `backend/internal/service/project/workspace_registration.go`
- Test: `backend/internal/service/project/service_test.go`

- [ ] **Step 1: Write a failing remote-first registration test**

Add a workspace test that creates a child repository on `develop`, creates `refs/remotes/origin/HEAD -> origin/develop`, checks out `feature`, registers the workspace, and asserts both Add and Get report `develop`:

```go
func TestManager_AddWorkspaceDetectsChildDefaultBranch(t *testing.T) {
	configureCommitter(t)
	ctx := context.Background()
	m := newManager(t)
	parent := t.TempDir()
	child := gitRepoWithCommit(t, filepath.Join(parent, "api"))
	for _, args := range [][]string{
		{"branch", "-m", "develop"},
		{"update-ref", "refs/remotes/origin/develop", "HEAD"},
		{"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/develop"},
		{"switch", "-c", "feature"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", child}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	proj, err := m.Add(ctx, project.AddInput{Path: parent, ProjectID: ptr("ws"), AsWorkspace: true})
	if err != nil {
		t.Fatalf("Add workspace: %v", err)
	}
	if len(proj.WorkspaceRepos) != 1 || proj.WorkspaceRepos[0].DefaultBranch != "develop" {
		t.Fatalf("WorkspaceRepos = %#v, want child default develop", proj.WorkspaceRepos)
	}
	got, err := m.Get(ctx, "ws")
	if err != nil || got.Project == nil || got.Project.WorkspaceRepos[0].DefaultBranch != "develop" {
		t.Fatalf("Get workspace = %#v, err=%v", got, err)
	}
}
```

- [ ] **Step 2: Run the test to verify RED**

Run: `cd backend && go test ./internal/service/project -run TestManager_AddWorkspaceDetectsChildDefaultBranch -count=1`

Expected: compilation fails because service `WorkspaceRepo.DefaultBranch` does not exist.

- [ ] **Step 3: Add minimal detection and service mapping**

Add the API-facing field:

```go
type WorkspaceRepo struct {
	Name          string `json:"name"`
	RelativePath  string `json:"relativePath"`
	Repo          string `json:"repo"`
	DefaultBranch string `json:"defaultBranch"`
}
```

While constructing each child record, set:

```go
DefaultBranch: resolveDefaultBranch(child),
```

Map it in `workspaceReposFromRecords`:

```go
DefaultBranch: rec.DefaultBranch,
```

- [ ] **Step 4: Run project tests to verify GREEN**

Run: `cd backend && go test ./internal/service/project -run 'TestManager_AddWorkspace' -count=1`

Expected: PASS, including the remote-first regression test.

- [ ] **Step 5: Commit the registration slice**

```bash
git add backend/internal/service/project/types.go backend/internal/service/project/workspace_registration.go backend/internal/service/project/service_test.go
git commit -m "feat: detect workspace repo default branches"
```

### Task 3: Expose the field through generated and mirrored API contracts

**Files:**
- Modify: `backend/internal/cli/project.go`
- Test: `backend/internal/httpd/controllers/projects_test.go`
- Test: `backend/internal/cli/project_test.go`
- Generate: `backend/internal/httpd/apispec/openapi.yaml`
- Generate: `frontend/src/api/schema.ts`

- [ ] **Step 1: Add failing HTTP and CLI decoding assertions**

Extend the workspace project HTTP test's response DTO with:

```go
type workspaceRepoBody struct {
	Name          string `json:"name"`
	RelativePath  string `json:"relativePath"`
	Repo          string `json:"repo"`
	DefaultBranch string `json:"defaultBranch"`
}
```

Decode `workspaceRepos` and assert the registered child returns `main`. Extend the nearest CLI project-detail JSON fixture with `"defaultBranch":"develop"`, add `DefaultBranch` to `workspaceRepoDetails`, and assert it decodes as `develop`.

- [ ] **Step 2: Run contract tests to verify RED**

Run: `cd backend && go test ./internal/httpd/controllers ./internal/cli -run 'Workspace|ProjectShow' -count=1`

Expected: the CLI assertion fails because its mirrored DTO drops `defaultBranch`; API schema drift remains until regeneration.

- [ ] **Step 3: Add the CLI mirrored DTO field**

```go
type workspaceRepoDetails struct {
	Name          string `json:"name"`
	RelativePath  string `json:"relativePath"`
	Repo          string `json:"repo"`
	DefaultBranch string `json:"defaultBranch"`
}
```

Keep the existing human-readable project output stable; the field is available to JSON consumers and future lifecycle/UI code.

- [ ] **Step 4: Regenerate API artifacts**

Run: `npm run api`

Expected: exit 0; `WorkspaceRepo.defaultBranch` appears as required in `openapi.yaml` and `frontend/src/api/schema.ts`.

- [ ] **Step 5: Run targeted contract verification**

Run: `cd backend && go test ./internal/httpd/... ./internal/cli ./internal/service/project ./internal/storage/sqlite/store -count=1`

Expected: PASS.

Run: `npm run frontend:typecheck`

Expected: exit 0.

- [ ] **Step 6: Run repository-level verification**

Run: `npm run lint`

Expected: backend tests pass and golangci-lint reports no findings.

Run: `git diff --check`

Expected: no output and exit 0.

- [ ] **Step 7: Commit generated contracts and final tests**

```bash
git add backend/internal/cli/project.go backend/internal/cli/project_test.go backend/internal/httpd/controllers/projects_test.go backend/internal/httpd/apispec/openapi.yaml frontend/src/api/schema.ts
git commit -m "feat: expose workspace repo default branches"
```
