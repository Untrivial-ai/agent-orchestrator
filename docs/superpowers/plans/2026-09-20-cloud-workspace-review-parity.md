# Cloud Workspace Review Parity Implementation Plan

> **For Codex:** REQUIRED SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Follow superpowers:test-driven-development for every behavior change and superpowers:verification-before-completion before claiming success.

**Goal:** Reproduce the complete local File Diff and File View experience for Cloud sessions across Docker, NodeOps, and Coder without modifying the local workspace implementation.

**Architecture:** Add a Cloud-only review protocol executed by the common `ao-worker`, proxy it through Cloud-only HTTP routes, and render it through Cloud-specific React hooks/components. Preserve existing lightweight Cloud routes during migration. Store the immutable checkout baseline in a private Git ref inside each workspace so committed changes remain visible across provider restarts and checkpoint restores.

**Tech Stack:** Go, Git CLI, Chi HTTP, durable Cloud worker requests, React 19, TypeScript, TanStack Query, Vitest, Testing Library, Docker Compose.

**Design:** `docs/superpowers/specs/2026-09-20-cloud-workspace-review-parity-design.md`

## Protected local surfaces

Do not modify any of the following:

- `backend/**`
- `frontend/src/renderer/components/SessionFileExplorer.tsx`
- `frontend/src/renderer/components/FileTree.tsx`
- `frontend/src/renderer/components/diffs/WorkspaceReviewPane.tsx`
- `frontend/src/renderer/components/diffs/AoDiffFile.tsx`
- `frontend/src/renderer/components/FileContentPane.tsx`
- `frontend/src/renderer/hooks/useSessionWorkspaceFiles.ts`
- `frontend/src/renderer/hooks/useSessionWorkspaceTree.ts`

`frontend/src/renderer/components/SessionView.tsx` may change only inside existing `session.cloud` branches and Cloud callback plumbing. Generic presentation primitives may be imported but not edited.

## Task 1: Normalize the pending Cloud migration number

**Files:**

- Delete: `cloud/internal/postgres/migrations/00037_workspace_diff_file_requests.sql`
- Add: `cloud/internal/postgres/migrations/00040_workspace_diff_file_requests.sql`

**Step 1: Confirm the collision**

Run: `ls cloud/internal/postgres/migrations/00037*`

Expected: both the existing turns migration and the workspace request migration use version 37 in commit `9cc3bf14f`.

**Step 2: Verify the rename is content-only**

Run: `git diff --no-index cloud/internal/postgres/migrations/00037_workspace_diff_file_requests.sql cloud/internal/postgres/migrations/00040_workspace_diff_file_requests.sql`

Expected: no content difference when both paths are available; otherwise compare the new file with `git show HEAD:cloud/internal/postgres/migrations/00037_workspace_diff_file_requests.sql`.

**Step 3: Run migration tests**

Run: `cd cloud && go test ./internal/postgres/...`

Expected: PASS with no duplicate Goose version panic.

**Step 4: Commit only the rename**

```bash
git add cloud/internal/postgres/migrations/00037_workspace_diff_file_requests.sql cloud/internal/postgres/migrations/00040_workspace_diff_file_requests.sql
git commit -m "fix(cloud): assign unique workspace request migration"
```

## Task 2: Define the Cloud review protocol

**Files:**

- Create: `cloud/internal/worker/workspace_review.go`
- Create: `cloud/internal/worker/workspace_review_test.go`

**Step 1: Write failing JSON-contract tests**

Cover exact JSON names and round trips for:

- `WorkspaceReviewScope`: `combined`, `committed`, `staged`, `unstaged`, `untracked`;
- file summary, sections, commit summary, aggregate summary, and review response;
- review-file, batch-diff, revision, tree, search, and write requests/responses;
- optional `commitSha`, `previousPath`, truncation, fingerprint, and workspace version fields.

Run: `cd cloud && go test ./internal/worker -run WorkspaceReview`

Expected: FAIL because the Cloud review types do not exist.

**Step 2: Add the minimal protocol types**

Use Cloud-owned types only. Do not import local daemon DTOs. Keep the existing lightweight types in `protocol.go` for backward compatibility.

**Step 3: Re-run tests**

Run: `cd cloud && go test ./internal/worker -run WorkspaceReview`

Expected: PASS.

**Step 4: Commit**

```bash
git add cloud/internal/worker/workspace_review.go cloud/internal/worker/workspace_review_test.go
git commit -m "feat(cloud): define workspace review protocol"
```

## Task 3: Capture an immutable checkout comparison base

**Files:**

- Modify: `cloud/internal/worker/checkout.go`
- Modify: `cloud/internal/worker/checkout_test.go`
- Modify: `cloud/cmd/ao-worker/main.go`

**Step 1: Write failing checkout-base tests**

Add fake-runner tests proving:

- a new checkout creates `refs/ao/diff-base` at the merge-base of `origin/<defaultBranch>` and `HEAD`;
- an existing private ref is never moved during resume/fetch;
- a missing remote branch falls back deterministically to the current initial commit;
- scratch repositories receive a stable initial base after initialization;
- `prepareWorkspace` passes the configured default branch.

Run: `cd cloud && go test ./internal/worker ./cmd/ao-worker -run 'ReviewBase|PrepareWorkspace'`

Expected: FAIL because base capture is absent.

**Step 2: Implement `EnsureWorkspaceReviewBase`**

Use `git show-ref --verify --quiet refs/ao/diff-base` as the guard. Resolve merge-base against `origin/<defaultBranch>` when present, otherwise resolve the initial safe commit, then call `git update-ref refs/ao/diff-base <sha>`. Never overwrite an existing ref.

Call this after checkout/branch configuration and after scratch initialization, before `MarkWorkspaceReady`.

**Step 3: Re-run focused tests**

Run: `cd cloud && go test ./internal/worker ./cmd/ao-worker -run 'ReviewBase|PrepareWorkspace'`

Expected: PASS.

**Step 4: Commit**

```bash
git add cloud/internal/worker/checkout.go cloud/internal/worker/checkout_test.go cloud/cmd/ao-worker/main.go
git commit -m "feat(cloud): preserve workspace review base"
```

## Task 4: Build section-aware workspace summaries

**Files:**

- Create: `cloud/internal/workertransport/workspace_review.go`
- Create: `cloud/internal/workertransport/workspace_review_test.go`
- Modify: `cloud/internal/workertransport/workspace.go`

**Step 1: Create a real-repository test fixture**

Build a temporary repository with:

- a commit after `refs/ao/diff-base`;
- one staged file;
- one unstaged file;
- one untracked file;
- a rename and a binary file;
- at least one unchanged tracked file.

**Step 2: Write failing summary tests**

Assert the review response contains all repository files, separate section lists, commit summaries, previous rename paths, correct line counts, binary flags, aggregate totals, ahead/behind values, and a non-empty `workspaceVersion`.

Also assert an entirely clean repository still returns its full file inventory instead of “no file changes.”

Run: `cd cloud && go test ./internal/workertransport -run 'WorkspaceReviewSummary|WorkspaceVersion'`

Expected: FAIL.

**Step 3: Implement Git parsing and summary construction**

Use bounded Git commands for:

- `git ls-files --cached --others --exclude-standard`;
- `git diff --cached` for staged;
- `git diff` for unstaged;
- `git status --porcelain=v1 -z --untracked-files=all` for untracked and rename-safe parsing;
- `git diff refs/ao/diff-base...HEAD` for committed;
- bounded `git log` plus per-commit numstat/status;
- `git rev-list --left-right --count` for ahead/behind.

Generate `workspaceVersion` from base SHA, `HEAD`, index metadata/hash, and worktree status output. Sort paths deterministically.

**Step 4: Re-run focused tests**

Run: `cd cloud && go test ./internal/workertransport -run 'WorkspaceReviewSummary|WorkspaceVersion'`

Expected: PASS.

**Step 5: Commit**

```bash
git add cloud/internal/workertransport/workspace_review.go cloud/internal/workertransport/workspace_review_test.go cloud/internal/workertransport/workspace.go
git commit -m "feat(cloud): summarize workspace review sections"
```

## Task 5: Add tree browsing and search

**Files:**

- Modify: `cloud/internal/workertransport/workspace_review.go`
- Modify: `cloud/internal/workertransport/workspace_review_test.go`

**Step 1: Write failing tree and search tests**

Cover:

- one-level lazy tree responses with deterministic directories-first ordering;
- `hasChildren` and recursive `hasChanges` flags;
- ignored and `.git` paths excluded;
- path-name search and bounded text-content search;
- binary and oversized files skipped for content matches;
- traversal, symlink escape, result-limit, and cancellation behavior.

Run: `cd cloud && go test ./internal/workertransport -run 'WorkspaceTree|WorkspaceSearch'`

Expected: FAIL.

**Step 2: Implement tree and search operations**

Build the allowed file set from Git tracked/untracked commands, then map it into lazy directory responses. Search only that allowed set and enforce result, byte, and time bounds.

**Step 3: Re-run tests**

Run: `cd cloud && go test ./internal/workertransport -run 'WorkspaceTree|WorkspaceSearch'`

Expected: PASS.

**Step 4: Commit**

```bash
git add cloud/internal/workertransport/workspace_review.go cloud/internal/workertransport/workspace_review_test.go
git commit -m "feat(cloud): browse and search workspace files"
```

## Task 6: Add scoped details, batch diffs, and revisions

**Files:**

- Modify: `cloud/internal/workertransport/workspace_review.go`
- Modify: `cloud/internal/workertransport/workspace_review_test.go`

**Step 1: Write failing scoped review tests**

Cover each scope and commit SHA for:

- file detail status/content/fingerprint;
- unified patches with requested context lines and whitespace handling;
- multiple requested paths returned in request order;
- untracked synthetic patches;
- before/after revisions for added, modified, deleted, renamed, binary, and oversized files;
- stale `workspaceVersion` conflict;
- invalid commit, path traversal, and non-repository paths;
- bounded per-file and combined responses.

Run: `cd cloud && go test ./internal/workertransport -run 'WorkspaceReviewFile|WorkspaceReviewDiffs|WorkspaceRevision'`

Expected: FAIL.

**Step 2: Implement scope-to-Git mappings**

Use explicit revisions:

- staged: `HEAD` versus index;
- unstaged: index versus worktree;
- untracked: `/dev/null` versus worktree;
- committed: base/selected commit parents versus commit;
- combined: base versus worktree.

Use `git show`, `git diff`, and index reads rather than temporary checkout mutations.

**Step 3: Re-run tests**

Run: `cd cloud && go test ./internal/workertransport -run 'WorkspaceReviewFile|WorkspaceReviewDiffs|WorkspaceRevision'`

Expected: PASS.

**Step 4: Commit**

```bash
git add cloud/internal/workertransport/workspace_review.go cloud/internal/workertransport/workspace_review_test.go
git commit -m "feat(cloud): read scoped workspace revisions"
```

## Task 7: Add fingerprint-checked Cloud file writes

**Files:**

- Modify: `cloud/internal/workertransport/workspace_review.go`
- Modify: `cloud/internal/workertransport/workspace_review_test.go`

**Step 1: Write failing write tests**

Assert:

- matching fingerprints allow atomic UTF-8 writes;
- stale fingerprints reject without changing the file;
- binary, oversized, symlink, directory, traversal, and missing-parent targets reject;
- successful responses return the new fingerprint and workspace version.

Run: `cd cloud && go test ./internal/workertransport -run WorkspaceReviewWrite`

Expected: FAIL.

**Step 2: Implement the safe write**

Reuse the confined `os.Root` and atomic temp-file/rename pattern from the existing Cloud write path. Add fingerprint verification and new response metadata without changing the compatibility route.

**Step 3: Re-run tests**

Run: `cd cloud && go test ./internal/workertransport -run WorkspaceReviewWrite`

Expected: PASS.

**Step 4: Commit**

```bash
git add cloud/internal/workertransport/workspace_review.go cloud/internal/workertransport/workspace_review_test.go
git commit -m "feat(cloud): safely edit reviewed workspace files"
```

## Task 8: Dispatch the review protocol from every worker provider

**Files:**

- Modify: `cloud/internal/workertransport/supervisor.go`
- Modify: `cloud/internal/workertransport/supervisor_test.go`

**Step 1: Write failing dispatch-table tests**

For every new request kind, encode a payload, call the supervisor handler, and assert the operation receives the decoded request and returns the typed response. Include invalid payload and typed conflict/error mapping.

Request kinds:

- `workspace.review.summary`
- `workspace.review.tree`
- `workspace.review.search`
- `workspace.review.file`
- `workspace.review.diffs`
- `workspace.review.revision`
- `workspace.review.write`

Run: `cd cloud && go test ./internal/workertransport -run WorkspaceReviewDispatch`

Expected: FAIL.

**Step 2: Add dispatch cases**

Dispatch only through the existing common supervisor; add no provider checks or provider-specific implementations.

**Step 3: Re-run tests**

Run: `cd cloud && go test ./internal/workertransport -run WorkspaceReviewDispatch`

Expected: PASS.

**Step 4: Commit**

```bash
git add cloud/internal/workertransport/supervisor.go cloud/internal/workertransport/supervisor_test.go
git commit -m "feat(cloud): dispatch workspace review requests"
```

## Task 9: Expose authenticated Cloud review routes

**Files:**

- Create: `cloud/internal/httpapi/workspace_review_handlers.go`
- Create: `cloud/internal/httpapi/workspace_review_handlers_test.go`
- Modify: `cloud/internal/httpapi/server.go`

**Step 1: Write failing handler tests**

Table-test all seven routes for:

- exact request-kind and payload mapping;
- Docker, NodeOps, and Coder sessions producing the same dispatch;
- tenant/session lookup and resume behavior;
- required path/scope/version validation;
- invalid worker JSON and worker failure envelopes;
- stale version/fingerprint mapped to HTTP 409;
- successful writes emitting `workspace.changed`.

Run: `cd cloud && go test ./internal/httpapi -run WorkspaceReview`

Expected: FAIL.

**Step 2: Implement handlers and register routes**

Add the routes from the design under the existing authenticated session router. Reuse `runWorkspaceRequest`; keep existing `/workspace/files`, `/workspace/file`, `/workspace/diff`, and `/workspace/file/diff` behavior intact.

**Step 3: Re-run tests**

Run: `cd cloud && go test ./internal/httpapi -run WorkspaceReview`

Expected: PASS.

**Step 4: Commit**

```bash
git add cloud/internal/httpapi/workspace_review_handlers.go cloud/internal/httpapi/workspace_review_handlers_test.go cloud/internal/httpapi/server.go
git commit -m "feat(cloud): expose workspace review API"
```

## Task 10: Add Cloud TypeScript contracts and client methods

**Files:**

- Modify: `frontend/src/renderer/lib/cloud-cp/types.ts`
- Modify: `frontend/src/renderer/lib/cloud-cp/client.ts`
- Modify: `frontend/src/renderer/lib/cloud-cp/client.test.ts`
- Modify: `frontend/src/renderer/lib/cloud-cp/index.ts` if exports require it

**Step 1: Write failing client tests**

Assert URL encoding, query parameters, and JSON bodies for summary, tree, search, detail, batch diff, revision, and write calls. Include `scope`, `commitSha`, `workspaceVersion`, `contextLines`, `ignoreWhitespace`, and fingerprint.

Run: `npm --prefix frontend test -- src/renderer/lib/cloud-cp/client.test.ts`

Expected: FAIL because methods are absent.

**Step 2: Add Cloud-only TypeScript types and methods**

Model the Cloud contract directly; do not import generated local API schemas. Keep old client methods while callers migrate.

**Step 3: Re-run tests and typecheck**

Run: `npm --prefix frontend test -- src/renderer/lib/cloud-cp/client.test.ts`

Expected: PASS.

Run: `npm run frontend:typecheck`

Expected: PASS.

**Step 4: Commit**

```bash
git add frontend/src/renderer/lib/cloud-cp/types.ts frontend/src/renderer/lib/cloud-cp/client.ts frontend/src/renderer/lib/cloud-cp/client.test.ts frontend/src/renderer/lib/cloud-cp/index.ts
git commit -m "feat(cloud): add workspace review client"
```

## Task 11: Add Cloud review query hooks and invalidation

**Files:**

- Create: `frontend/src/renderer/hooks/useCloudWorkspaceReview.ts`
- Create: `frontend/src/renderer/hooks/useCloudWorkspaceReview.test.tsx`

**Step 1: Write failing hook tests**

Assert:

- every operation has Cloud-specific cache keys containing base URL, org, session, scope, commit, and path as applicable;
- polling runs only while enabled/visible;
- `workspace.changed` invalidates Cloud review queries;
- a 409 stale-version response invalidates summary before retry;
- no request uses a local `/api/v1/sessions/.../workspace` URL.

Run: `npm --prefix frontend test -- src/renderer/hooks/useCloudWorkspaceReview.test.tsx`

Expected: FAIL.

**Step 2: Implement query options and mutations**

Use `useCloudCp`, TanStack Query, the existing Cloud event subscription, and a five-second fallback polling interval. Keep query construction independent of local workspace hooks.

**Step 3: Re-run tests**

Run: `npm --prefix frontend test -- src/renderer/hooks/useCloudWorkspaceReview.test.tsx`

Expected: PASS.

**Step 4: Commit**

```bash
git add frontend/src/renderer/hooks/useCloudWorkspaceReview.ts frontend/src/renderer/hooks/useCloudWorkspaceReview.test.tsx
git commit -m "feat(cloud): query workspace review data"
```

## Task 12: Build the Cloud All Files tree and search

**Files:**

- Create: `frontend/src/renderer/components/cloud-workspace/CloudFileTree.tsx`
- Create: `frontend/src/renderer/components/cloud-workspace/CloudFileTree.test.tsx`
- Create: `frontend/src/renderer/components/cloud-workspace/CloudWorkspaceExplorer.tsx`
- Create: `frontend/src/renderer/components/cloud-workspace/CloudWorkspaceExplorer.test.tsx`

**Step 1: Write failing UI tests**

Cover Changes/All Files switching, lazy directory expansion, changed markers, search/debounce, result selection, empty/loading/error states, and maximize layout.

Run: `npm --prefix frontend test -- src/renderer/components/cloud-workspace/CloudFileTree.test.tsx src/renderer/components/cloud-workspace/CloudWorkspaceExplorer.test.tsx`

Expected: FAIL.

**Step 2: Implement the Cloud-only explorer and tree**

Match local labels, keyboard behavior, icons, sizing, and layout using existing UI primitives. Fetch exclusively through Cloud hooks.

**Step 3: Re-run tests**

Run: `npm --prefix frontend test -- src/renderer/components/cloud-workspace/CloudFileTree.test.tsx src/renderer/components/cloud-workspace/CloudWorkspaceExplorer.test.tsx`

Expected: PASS.

**Step 4: Commit**

```bash
git add frontend/src/renderer/components/cloud-workspace/CloudFileTree.tsx frontend/src/renderer/components/cloud-workspace/CloudFileTree.test.tsx frontend/src/renderer/components/cloud-workspace/CloudWorkspaceExplorer.tsx frontend/src/renderer/components/cloud-workspace/CloudWorkspaceExplorer.test.tsx
git commit -m "feat(cloud): browse all workspace files"
```

## Task 13: Build Cloud change and commit review

**Files:**

- Create: `frontend/src/renderer/components/cloud-workspace/CloudWorkspaceReviewPane.tsx`
- Create: `frontend/src/renderer/components/cloud-workspace/CloudWorkspaceReviewPane.test.tsx`
- Create: `frontend/src/renderer/components/cloud-workspace/CloudDiffFile.tsx`
- Create: `frontend/src/renderer/components/cloud-workspace/CloudDiffFile.test.tsx`

**Step 1: Write failing review UI tests**

Cover:

- initial selection priority: unstaged, staged, untracked, then latest commit;
- separate staged/unstaged/untracked controls and combined mode;
- commit list and commit-file selection;
- aggregate `+/-` counts;
- batch patch loading and deferred binary/large file behavior;
- split/unified rendering, context, whitespace, collapse/expand, and viewed state;
- before/after revision loading;
- annotation controls;
- stale workspace refresh and per-file retry.

Run: `npm --prefix frontend test -- src/renderer/components/cloud-workspace/CloudWorkspaceReviewPane.test.tsx src/renderer/components/cloud-workspace/CloudDiffFile.test.tsx`

Expected: FAIL.

**Step 2: Implement the review pane**

Copy the local interaction behavior into Cloud-owned components, but replace every data access with Cloud hooks. Import generic, stateless diff/annotation presentation primitives without editing them.

**Step 3: Re-run tests**

Run: `npm --prefix frontend test -- src/renderer/components/cloud-workspace/CloudWorkspaceReviewPane.test.tsx src/renderer/components/cloud-workspace/CloudDiffFile.test.tsx`

Expected: PASS.

**Step 4: Commit**

```bash
git add frontend/src/renderer/components/cloud-workspace/CloudWorkspaceReviewPane.tsx frontend/src/renderer/components/cloud-workspace/CloudWorkspaceReviewPane.test.tsx frontend/src/renderer/components/cloud-workspace/CloudDiffFile.tsx frontend/src/renderer/components/cloud-workspace/CloudDiffFile.test.tsx
git commit -m "feat(cloud): review categorized workspace changes"
```

## Task 14: Build the Cloud file content view and editor

**Files:**

- Create: `frontend/src/renderer/components/cloud-workspace/CloudFileContentPane.tsx`
- Create: `frontend/src/renderer/components/cloud-workspace/CloudFileContentPane.test.tsx`

**Step 1: Write failing content-view tests**

Cover file/diff/rendered Markdown modes, scoped and commit-specific views, before/after complete content, deleted/binary/large-file messages, edit/save/cancel, dirty state, stale fingerprint conflicts, and retry.

Run: `npm --prefix frontend test -- src/renderer/components/cloud-workspace/CloudFileContentPane.test.tsx`

Expected: FAIL.

**Step 2: Implement the Cloud content pane**

Use Cloud hooks only. Reuse the existing editor and Markdown presentation components as imports without editing their source. Return dirty-state changes to the session shell.

**Step 3: Re-run tests**

Run: `npm --prefix frontend test -- src/renderer/components/cloud-workspace/CloudFileContentPane.test.tsx`

Expected: PASS.

**Step 4: Commit**

```bash
git add frontend/src/renderer/components/cloud-workspace/CloudFileContentPane.tsx frontend/src/renderer/components/cloud-workspace/CloudFileContentPane.test.tsx
git commit -m "feat(cloud): view and edit workspace files"
```

## Task 15: Replace the simplified Cloud shell and wire file-tab options

**Files:**

- Modify: `frontend/src/renderer/components/CloudWorkspaceDiff.tsx`
- Modify: `frontend/src/renderer/components/CloudWorkspaceDiff.test.tsx`
- Modify: `frontend/src/renderer/components/SessionView.tsx` (Cloud branches only)
- Modify: `frontend/src/renderer/components/SessionView.test.tsx`

**Step 1: Write failing integration tests**

Assert:

- `CloudWorkspaceDiff` delegates to the new Cloud explorer;
- opening a changed file preserves scope, commit SHA, mode, and editing options;
- the center Cloud pane receives those options;
- maximized Cloud review uses the same selected state;
- Cloud sessions never invoke local workspace APIs;
- non-Cloud sessions still render the original local components and props unchanged.

Run: `npm --prefix frontend test -- src/renderer/components/CloudWorkspaceDiff.test.tsx src/renderer/components/SessionView.test.tsx`

Expected: FAIL.

**Step 2: Wire only the Cloud branches**

Keep the public `CloudWorkspaceDiff` export as a compatibility wrapper around `CloudWorkspaceExplorer`. Replace its old center pane export with the new Cloud content pane. Pass the existing center-file request options through Cloud branches without changing local branches.

**Step 3: Re-run integration tests and typecheck**

Run: `npm --prefix frontend test -- src/renderer/components/CloudWorkspaceDiff.test.tsx src/renderer/components/SessionView.test.tsx`

Expected: PASS.

Run: `npm run frontend:typecheck`

Expected: PASS.

**Step 4: Commit**

```bash
git add frontend/src/renderer/components/CloudWorkspaceDiff.tsx frontend/src/renderer/components/CloudWorkspaceDiff.test.tsx frontend/src/renderer/components/SessionView.tsx frontend/src/renderer/components/SessionView.test.tsx
git commit -m "feat(cloud): match local workspace review experience"
```

## Task 16: Extend the local Docker Cloud smoke fixture

**Files:**

- Modify: `cloud/scripts/test-cloud-local.sh`
- Modify: `cloud/README.md`

**Step 1: Add failing smoke assertions**

Create committed, staged, unstaged, and untracked changes inside the Docker worker. Assert the review summary categorizes each path, All Files includes an unchanged tracked file, scoped diffs/revisions return content, search/tree work, and a fingerprint-checked write succeeds.

Run: `npm run cloud:local:smoke`

Expected: FAIL before the new assertions are supported.

**Step 2: Update documentation**

Document the full Cloud review routes and explicitly state that Docker, NodeOps, and Coder share the implementation.

**Step 3: Re-run smoke**

Run: `npm run cloud:local:smoke`

Expected: PASS.

**Step 4: Commit**

```bash
git add cloud/scripts/test-cloud-local.sh cloud/README.md
git commit -m "test(cloud): cover complete workspace review flow"
```

## Task 17: Run full verification and inspect protected files

**Files:** None unless a test exposes a defect. Fix only the owning Cloud layer and repeat its red/green cycle.

**Step 1: Format changed Go files**

Run: `gofmt -w cloud/internal/worker/workspace_review.go cloud/internal/worker/workspace_review_test.go cloud/internal/worker/checkout.go cloud/internal/worker/checkout_test.go cloud/internal/workertransport/workspace_review.go cloud/internal/workertransport/workspace_review_test.go cloud/internal/workertransport/supervisor.go cloud/internal/workertransport/supervisor_test.go cloud/internal/httpapi/workspace_review_handlers.go cloud/internal/httpapi/workspace_review_handlers_test.go cloud/internal/httpapi/server.go cloud/cmd/ao-worker/main.go`

Expected: no semantic changes, formatting only.

**Step 2: Run Cloud Go suites**

Run: `cd cloud && go test ./...`

Expected: PASS.

Run: `cd cloud && go test -race ./...`

Expected: PASS.

Run: `cd cloud && go vet ./...`

Expected: PASS.

**Step 3: Run frontend suites**

Run: `npm run frontend:typecheck`

Expected: PASS.

Run: `npm --prefix frontend test`

Expected: PASS.

Run: `npm --prefix frontend run build`

Expected: PASS.

**Step 4: Run local regression checks unchanged**

Run: `cd backend && go test ./internal/service/session/... ./internal/httpd/...`

Expected: PASS with no local implementation edits.

**Step 5: Prove protected files are untouched**

Run: `git diff origin/main...HEAD -- backend frontend/src/renderer/components/SessionFileExplorer.tsx frontend/src/renderer/components/FileTree.tsx frontend/src/renderer/components/diffs/WorkspaceReviewPane.tsx frontend/src/renderer/components/diffs/AoDiffFile.tsx frontend/src/renderer/components/FileContentPane.tsx frontend/src/renderer/hooks/useSessionWorkspaceFiles.ts frontend/src/renderer/hooks/useSessionWorkspaceTree.ts`

Expected: no diff introduced by this feature. If an earlier unrelated branch commit already differs, compare against commit `98cace541` and require no new diff.

**Step 6: Run the Docker Cloud flow**

Run: `npm run cloud:local`

Expected: control plane and worker become healthy.

Run: `npm run cloud:local:smoke`

Expected: PASS with all four change categories and All Files coverage.

**Step 7: Perform desktop visual verification**

Use the repository's isolated desktop-lab procedure and scratch `AO_DATA_DIR`. Connect it to the local Docker Cloud control plane, open the prepared session, and verify:

- Changes and All Files;
- committed, staged, unstaged, and untracked selectors;
- commit browsing;
- unified and split diffs;
- file and Markdown views;
- editing and stale-write feedback;
- maximize/minimize and center tabs.

Capture console output and screenshots for failures. Do not deploy staging.

**Step 8: Review the final diff**

Run: `git status --short`

Expected: clean.

Run: `git diff --stat 98cace541..HEAD`

Expected: only Cloud backend, Cloud-specific frontend, the minimal Cloud branches in `SessionView`, tests, and Cloud documentation.

**Step 9: Final verification commit if formatting or test fixes remain**

```bash
git add <only verified Cloud files>
git commit -m "test(cloud): verify workspace review parity"
```

Do not push or deploy without a separate user request.
