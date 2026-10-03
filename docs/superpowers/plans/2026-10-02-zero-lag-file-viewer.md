# Zero-Lag File Viewer Implementation Plan

**Goal:** Make opening Files, revealing a directory, and selecting a warm file feel instantaneous while keeping workspace data correct as agents edit the worktree.

**Architecture:** Move expensive Git discovery out of the click path. The daemon maintains an immutable, versioned review manifest in memory and refreshes it behind the existing workspace watcher. The renderer keeps that manifest and a stable path-first tree model warm, paints the last valid snapshot synchronously, and applies versioned updates without rebuilding the whole surface. File contents and diffs remain lazy, but their first visible batch is fetched and parsed ahead of lower-priority work.

**Pierre Trees lessons applied:** canonical paths are the public identity; large inputs are sorted and prepared once outside render; the tree model survives data refreshes; path and Git-status changes are applied as patches; only the visible row window mounts; search, selection, and focus share the same model. Do not use Pierre's SSR path in Electron. Do not serialize a library-private prepared-input object into the daemon API.

**Tech stack:** Go, Git CLI, Chi HTTP/SSE, React 19, TanStack Query, `@pierre/trees`, `@pierre/diffs`, Web Workers, Vitest, Testing Library, Go benchmarks.

**Research basis:** Pierre Trees documents canonical path identity, prepared and presorted input, stable model mutation methods, Git-status patches, and viewport virtualization. AO already uses a virtualized `react-arborist` tree, a lazy one-directory endpoint, TanStack Query caching, workspace SSE invalidation, and Pierre Diffs. This plan adopts the Pierre model where it removes repeated renderer work, while treating AO's Git-heavy cold read as a separate daemon problem.

## Performance contract

“Zero lag” means no user action waits for repository-wide Git work before useful content appears. Measure these targets in a packaged-style Electron development build on the repository fixtures described below:

| Interaction | Target |
| --- | --- |
| Reopen Files with a warm snapshot | useful content in the first animation frame; p95 click-to-paint under 16 ms |
| First Files open after session selection | cached/empty shell in first frame and populated changed-file manifest p95 under 100 ms |
| Expand a previously loaded directory | first-frame update; p95 under 16 ms |
| Expand an uncached directory | loading row in first frame and entries p95 under 75 ms |
| Select a prefetched changed file | file header in first frame and diff/content p95 under 50 ms |
| Select an uncached ordinary file | file header in first frame and content p95 under 100 ms |
| Main-thread long tasks caused by tree/diff work | none over 50 ms; p95 render/commit work under 8 ms |

Use p50/p95 and fixture size in every benchmark report. Never claim literal zero execution time. The product requirement is zero *blocking* or *perceived* latency.

## Correctness invariants

- A response is identified by an opaque `workspaceVersion`; data from different versions is never merged silently.
- A watcher event marks the current snapshot stale immediately, but the last valid snapshot remains readable while refresh runs.
- Only a completed refresh replaces the current snapshot. Cancellation, Git errors, and partial output never publish half a manifest.
- Failed or unknown Git probes do not imply that a workspace or session is dead.
- The daemon owns Git and filesystem reads. The renderer remains a thin client.
- Multi-repository, scratch, deleted, renamed, binary, untracked, staged, partially staged, and degraded workspaces retain their current semantics.
- No derived review snapshot is written to SQLite. It is an in-memory performance cache rebuilt after daemon restart.
- Existing file-write conflict checks continue to use file fingerprints, not the review cache.

## Non-goals

- Do not change the loopback listener or authentication model.
- Do not add SSR to the Electron renderer.
- Do not eagerly read every file or generate every patch.
- Do not make the public API depend on Pierre Trees' opaque prepared-input representation.
- Do not replace `@pierre/diffs` or change diff presentation as part of the tree migration.
- Do not persist a second source of truth for Git state.

## Fixture matrix

Add reusable repositories for benchmarks and integration tests:

- small: 500 tracked files, 10 changed files;
- medium: 10,000 tracked files, 250 changed files, 25 directories expanded;
- large: 100,000 tracked files, 1,000 changed files, including a lockfile and a 1 MiB text file;
- state-heavy: staged, unstaged, partially staged, untracked, renamed, deleted, binary, and 250 commits after the base;
- workspace project: four repositories with one slow repository fixture;
- scratch: 10,000 files with AO-managed control files that must remain hidden.

Fixture creation must be deterministic and local. Do not add network access to tests.

## Task 1: Establish latency observability and a reproducible baseline

**Files:**

- Add: `backend/internal/service/session/workspace_files_benchmark_test.go`
- Add: `frontend/src/renderer/lib/file-viewer-performance.ts`
- Add: `frontend/src/renderer/lib/file-viewer-performance.test.ts`
- Modify: `frontend/src/renderer/components/SessionFileExplorer.tsx`
- Modify: `frontend/src/renderer/components/FileContentPane.tsx`
- Modify: `frontend/src/renderer/components/diffs/WorkspaceReviewPane.tsx`

**Steps:**

1. Add Go benchmarks for cold and warm workspace summary, one-directory listing, one-file detail, and batch diff generation across the fixture matrix. Report allocations, Git process count, and bytes read in addition to elapsed time.
2. Add renderer marks for `files-click`, `files-shell-painted`, `manifest-ready`, `tree-painted`, `file-selected`, `file-header-painted`, and `file-content-painted`.
3. Implement a bounded in-memory performance recorder that is disabled by default, available in development diagnostics, and never emits telemetry or file paths.
4. Capture the baseline before changing behavior and save it in the PR description. Do not encode wall-clock thresholds in shared CI; use deterministic assertions for request count, recomputation count, and mounted-row count.

**Verification:**

```bash
cd backend && go test ./internal/service/session -run '^$' -bench 'Workspace(File|Tree|Diff)' -benchmem
cd frontend && npm test -- --run src/renderer/lib/file-viewer-performance.test.ts
```

**Commit:** `test(files): establish file viewer performance baselines`

## Task 2: Introduce a fast review-manifest contract

**Files:**

- Modify: `backend/internal/httpd/controllers/dto.go`
- Modify: `backend/internal/httpd/controllers/sessions.go`
- Modify: `backend/internal/httpd/controllers/sessions_test.go`
- Modify: `backend/internal/httpd/apispec/specgen/build.go`
- Modify: `backend/internal/service/session/service.go`
- Add: `backend/internal/service/session/workspace_manifest.go`
- Add: `backend/internal/service/session/workspace_manifest_test.go`
- Regenerate: `backend/internal/httpd/apispec/openapi.yaml`
- Regenerate: `frontend/src/api/schema.ts`

**Steps:**

1. Add `GET /api/v1/sessions/{sessionId}/workspace/manifest`. It returns only data needed for the initial Changes paint: session id, `workspaceVersion`, compare identity, changed file summaries, staged/unstaged/untracked/committed sections, aggregate counts, truncation, stale/refreshing flags, and degraded state.
2. Exclude the complete tracked-file inventory, commit history, ahead/behind, file contents, full patches, and revision contents from this response.
3. Add `GET /api/v1/sessions/{sessionId}/workspace/commits` for the commit picker and move commit-log enrichment behind that explicit request.
4. Preserve `/workspace/files` during migration. Implement it by composing the fast manifest with the existing all-files and commit enrichment so older clients keep their wire contract.
5. Keep summaries deterministically sorted by canonical forward-slashed path.
6. Generate OpenAPI and frontend types with `npm run api`.

**Tests:** happy path, empty repository, every Git state, multi-repository prefixes, scratch workspace, truncation, degraded enrichment, daemon error envelope, request id, and exact route/spec parity.

**Verification:**

```bash
cd backend && go test ./internal/service/session ./internal/httpd/...
npm run api
git diff --exit-code -- backend/internal/httpd/apispec/openapi.yaml frontend/src/api/schema.ts
```

The final `git diff --exit-code` is run only after generated files have been staged or compared against a second regeneration; the intent is to prove regeneration is stable, not that the feature branch has no changes.

**Commit:** `feat(files): add fast workspace review manifest`

## Task 3: Maintain an immutable live manifest in the daemon

**Files:**

- Add: `backend/internal/service/session/workspace_manifest_index.go`
- Add: `backend/internal/service/session/workspace_manifest_index_test.go`
- Modify: `backend/internal/service/session/service.go`
- Modify: `backend/internal/service/session/workspace_files.go`
- Modify: `backend/internal/httpd/controllers/sessions.go`
- Modify: `backend/internal/httpd/controllers/sessions_test.go`

**Steps:**

1. Add a per-session manifest index containing an immutable last-good snapshot, its version, freshness state, refresh generation, and at most one in-flight refresh.
2. Warm the index when workspace observation starts, before the user opens Files. The HTTP read path returns the current snapshot without launching Git when one exists.
3. Debounce watcher bursts over a short bounded window. Mark the snapshot stale immediately, refresh in the background, then atomically swap the complete result.
4. If no snapshot exists, singleflight the initial computation. Never make concurrent Files/count/detail consumers start duplicate repository scans.
5. Cancel index work during session teardown and bound all Git output and refresh duration using existing workspace command helpers.
6. Keep the old snapshot after a failed refresh and expose `stale: true`, `refreshing: false`, plus the existing degraded code. A later event or explicit retry starts another refresh.
7. Add per-session and global memory caps. Evict terminated or least-recently-read indexes without affecting correctness.

**Tests:** initial warm, concurrent readers, event-burst coalescing, invalidation during refresh, stale-while-refresh, failed refresh, retry, session teardown, eviction, multi-repo partial failure, and race coverage.

**Verification:**

```bash
cd backend && go test ./internal/service/session -run 'WorkspaceManifest|WorkspaceCache'
cd backend && go test -race ./internal/service/session -run 'WorkspaceManifest|WorkspaceCache'
```

**Commit:** `perf(files): maintain live workspace manifests`

## Task 4: Reduce Git and filesystem work in the manifest refresh

**Files:**

- Modify: `backend/internal/service/session/workspace_manifest.go`
- Modify: `backend/internal/service/session/workspace_files.go`
- Modify: `backend/internal/service/session/workspace_files_test.go`
- Modify: `backend/internal/service/session/workspace_files_benchmark_test.go`

**Steps:**

1. Inventory every Git process in the baseline. Combine compatible status/count passes where Git provides an unambiguous `-z` format, while retaining rename and binary correctness.
2. Do not call `git ls-files` for the Changes manifest. Reserve complete inventory work for `/workspace/tree` and backward-compatible `/workspace/files` composition.
3. Stat and sample only changed files needed by the initial review. Defer unchanged file size/binary detection until a directory or file is opened.
4. Move commit history and ahead/behind computation out of the manifest critical path.
5. Cache compare-base resolution with the manifest generation. A single refresh must not resolve the same base repeatedly.
6. Keep repositories concurrent but cap concurrency for workspace projects so a many-repository workspace cannot exhaust process or file-descriptor limits.

**Acceptance:** on the medium fixture, a warm manifest read starts zero Git processes; a refresh starts a bounded number independent of reader count; opening Changes no longer runs `git ls-files` or commit-log commands.

**Verification:**

```bash
cd backend && go test ./internal/service/session -run 'WorkspaceManifest|WorkspaceFiles|WorkspaceGitState'
cd backend && go test ./internal/service/session -run '^$' -bench 'WorkspaceManifest' -benchmem
```

**Commit:** `perf(files): remove repository scans from manifest reads`

## Task 5: Send versioned workspace updates instead of blind invalidations

**Files:**

- Modify: `backend/internal/httpd/controllers/sessions.go`
- Modify: `backend/internal/httpd/controllers/sessions_test.go`
- Modify: `frontend/src/renderer/lib/workspace-file-events.ts`
- Modify: `frontend/src/renderer/lib/workspace-file-events.test.ts`
- Modify: `frontend/src/renderer/hooks/useSessionWorkspaceFiles.ts`

**Steps:**

1. Extend the existing workspace SSE event with event kind, completed `workspaceVersion`, and refresh/degraded state. Do not send paths or file contents in the coarse event.
2. Emit “dirty” when refresh begins and “version” only after a complete snapshot is published.
3. On “dirty”, retain rendered data and show only a subtle refreshing state. Do not clear the tree or diff.
4. On “version”, fetch the manifest conditionally. If the version already matches the TanStack cache, do nothing.
5. Fence every response by session and version so a slower old request cannot overwrite a newer snapshot.
6. Keep degraded polling as a fallback when SSE is disconnected.

**Tests:** ordered events, reconnect, duplicate version, out-of-order response, refresh failure, session switch, and no loading-state flash while stale data exists.

**Verification:**

```bash
cd backend && go test ./internal/httpd/... -run 'Workspace.*Event'
cd frontend && npm test -- --run src/renderer/lib/workspace-file-events.test.ts src/renderer/hooks/useSessionWorkspaceFiles.test.ts
```

**Commit:** `perf(files): stream versioned manifest updates`

## Task 6: Keep the file-viewer data and model warm

**Files:**

- Add: `frontend/src/renderer/hooks/useSessionWorkspaceManifest.ts`
- Add: `frontend/src/renderer/hooks/useSessionWorkspaceManifest.test.ts`
- Modify: `frontend/src/renderer/components/SessionView.tsx`
- Modify: `frontend/src/renderer/components/SessionFileExplorer.tsx`
- Modify: `frontend/src/renderer/hooks/useSessionWorkspaceFiles.ts`
- Modify: `frontend/src/renderer/stores/ui-store.ts`

**Steps:**

1. Mount one session-scoped manifest observer at `SessionView`, not separately in the badge, inspector, and review pane. Seed all consumers from the same TanStack cache entry.
2. Start the manifest query when a session becomes active and keep it warm for the session's lifetime. Opening Files must not create the first request.
3. Preserve the last valid manifest during refetch and across Files tab close/reopen. Clear it only when the session identity changes or the daemon explicitly reports that the workspace no longer exists.
4. Store only interaction state in Zustand: selected path, expanded paths, view mode, and scroll anchor. Keep repository data in TanStack Query/the tree model.
5. Render the Files shell and last snapshot synchronously. Refreshing, empty, error, and degraded states must not replace usable cached content with a spinner.

**Tests:** first-frame cached render, one request shared across consumers, tab reopen, session isolation, reconnect, stale refresh, and deleted workspace.

**Verification:**

```bash
cd frontend && npm test -- --run src/renderer/hooks/useSessionWorkspaceManifest.test.ts src/renderer/components/SessionFileExplorer.test.tsx
```

**Commit:** `perf(files): keep session file manifests warm`

## Task 7: Replace rebuild-on-render tree state with a Pierre Trees model

**Files:**

- Modify: `frontend/package.json`
- Modify: `frontend/package-lock.json`
- Replace internals: `frontend/src/renderer/components/FileTree.tsx`
- Modify: `frontend/src/renderer/components/FileTree.test.tsx`
- Modify: `frontend/src/renderer/components/SessionFileExplorer.test.tsx`
- Add: `frontend/src/renderer/lib/workspace-tree-model.ts`
- Add: `frontend/src/renderer/lib/workspace-tree-model.test.ts`
- Modify: `frontend/src/renderer/hooks/useSessionWorkspaceTree.ts`

**Steps:**

1. Pin a reviewed `@pierre/trees` version and record its license. Keep the dependency change isolated from the backend work.
2. Create one model per session/view source, keyed by canonical workspace-relative path. Preserve it across component rerenders and tab switches.
3. Sort daemon paths once. Feed changed-file trees through `preparePresortedFileTreeInput`; do not recursively rebuild and sort nested `TreeNode` objects during React render.
4. Update status-only changes with `setGitStatus`/`applyGitStatusPatch`. Use batched add/remove/move operations when the version delta is known; use prepared `resetPaths` only when replacement is cheaper or required for recovery.
5. Map existing selection, focus, keyboard navigation, expansion, search, file icons, Git decorations, and accessibility labels onto the Pierre model. Preserve current AO styling through supported variables/composition before considering `unsafeCSS`.
6. Continue lazy directory API reads for All Files. Insert a loaded directory's canonical children in one batch and remember loaded directory versions. Do not fetch descendants merely to prepare the visible model.
7. Configure a fixed item height and small overscan. Assert that mounted rows remain proportional to viewport height, not repository size.
8. Keep the current `react-arborist` implementation behind a temporary internal feature flag until parity and performance acceptance pass; remove it and the dependency in the same PR before merge.

**Parity tests:** expand/collapse, lazy load retry, changed-only mode, All Files mode, search, selected file, renamed path, directory change marker, icons, keyboard controls, screen-reader labels, session switch, resize, and maximized/docked layouts.

**Verification:**

```bash
cd frontend && npm test -- --run src/renderer/lib/workspace-tree-model.test.ts src/renderer/components/FileTree.test.tsx src/renderer/components/SessionFileExplorer.test.tsx
npm run frontend:typecheck
```

**Commit:** `perf(files): adopt a stable Pierre tree model`

## Task 8: Prioritize visible file content and diffs

**Files:**

- Modify: `frontend/src/renderer/hooks/useSessionWorkspaceFiles.ts`
- Modify: `frontend/src/renderer/components/FileContentPane.tsx`
- Modify: `frontend/src/renderer/components/diffs/WorkspaceReviewPane.tsx`
- Modify: `frontend/src/renderer/components/diffs/WorkspaceReviewPane.test.tsx`
- Add: `frontend/src/renderer/workers/pierre-diff-parser.worker.ts`
- Add: `frontend/src/renderer/lib/pierre-diff-loader.ts`
- Add: `frontend/src/renderer/lib/pierre-diff-loader.test.ts`

**Steps:**

1. Replace the fixed “first 400 files” prefetch with priority tiers: selected file, visible review rows, next/previous neighbors, then idle batches.
2. Abort obsolete content and diff requests on selection, scope, commit, session, or workspace-version change.
3. Put selected-file requests ahead of background EOF revision fetches. Cap background request and parse concurrency.
4. Move large `@pierre/diffs` `parsePatchFiles` work off the renderer thread using a shared worker. Keep small patches synchronous when worker overhead is greater than parsing cost.
5. Cache parsed metadata by complete content identity: workspace version, scope, commit, repository, and a collision-resistant patch digest. Do not use only patch length/prefix/suffix.
6. Render the file header and a stable content skeleton synchronously on selection. Replace it in place without shifting the surrounding layout.
7. Retain the currently visible file while its newer version loads, with a refreshing indication. Never combine old parsed metadata with a new manifest.

**Tests:** request priority, cancellation, concurrency cap, version fencing, worker failure fallback, hash collision resistance, selected-file paint, rapid navigation, and large lockfile deferral.

**Verification:**

```bash
cd frontend && npm test -- --run src/renderer/lib/pierre-diff-loader.test.ts src/renderer/components/diffs/WorkspaceReviewPane.test.tsx src/renderer/components/FileContentPane.test.tsx
```

**Commit:** `perf(files): prioritize visible diff work`

## Task 9: Make full-tree search and expansion incremental

**Files:**

- Modify: `backend/internal/service/session/workspace_review.go`
- Modify: `backend/internal/service/session/workspace_review_test.go`
- Modify: `frontend/src/renderer/hooks/useSessionWorkspaceTree.ts`
- Modify: `frontend/src/renderer/components/FileTree.tsx`

**Steps:**

1. Preserve one-level directory pagination and return a directory version/fingerprint so unchanged expansions can reuse cached children after unrelated workspace changes.
2. Stop invalidating every mounted directory query on every coarse event. Invalidate the manifest immediately; invalidate loaded directories only when their fingerprint changes or when the event cannot be localized safely.
3. Debounce search input, cancel obsolete searches, and stream/paginate bounded results into one stable model rather than rebuilding the entire tree for each keystroke.
4. Preserve expansion, focus, and scroll anchor while search opens/closes and while status patches arrive.

**Tests:** unrelated directory change, directory rename/delete, pagination, stale fingerprint, fast typing, canceled search, search reset, and expansion/scroll preservation.

**Verification:**

```bash
cd backend && go test ./internal/service/session -run 'WorkspaceTree|WorkspaceSearch'
cd frontend && npm test -- --run src/renderer/components/FileTree.test.tsx src/renderer/hooks/useSessionWorkspaceTree.test.ts
```

**Commit:** `perf(files): update workspace trees incrementally`

## Task 10: Enforce the performance contract and remove migration code

**Files:**

- Modify: `backend/internal/service/session/workspace_files_benchmark_test.go`
- Modify: `frontend/src/renderer/lib/file-viewer-performance.test.ts`
- Modify: `frontend/src/renderer/components/FileTree.tsx`
- Modify: `frontend/package.json`
- Modify: `frontend/package-lock.json`
- Add: `docs/performance/file-viewer.md`

**Steps:**

1. Run the same fixture matrix used for the baseline. Record before/after p50, p95, allocations, Git process count, bytes read, mounted rows, and renderer long tasks.
2. Add deterministic regression tests proving warm reads run no Git commands, event bursts produce one refresh, tab reopen starts no request, and mounted rows are viewport-bounded.
3. Verify the performance targets in the real Electron app using isolated scratch data. Test docked/maximized layouts, rapid session switching, active agent writes, large diffs, degraded Git, and SSE reconnect.
4. Remove the temporary tree feature flag, `react-arborist`, and compatibility adapters only after visual and accessibility parity passes.
5. Document the data flow, cache lifecycle, performance diagnostics, expected fallback behavior, and how to reproduce benchmarks.

**Full verification:**

```bash
npm run lint
npm run frontend:typecheck
cd backend && go test -race ./...
cd backend && go vet ./...
cd frontend && npm run build
npx @redwoodjs/agent-ci run --all
git diff --check
```

If Docker, a native runner, or credentials make any command unavailable, report that exact gap and verify the corresponding remote CI job before handoff.

**Commit:** `docs(files): verify zero-lag file viewer architecture`

## Rollout and rollback

1. Land backend manifest/index support first while the existing `/workspace/files` client remains unchanged.
2. Switch the frontend to the manifest behind an internal development flag and compare old/new timings against identical sessions.
3. Enable the manifest path by default after correctness, race, and API-drift suites pass.
4. Migrate the tree model behind its own temporary flag; remove the old implementation before merge once parity is proven.
5. Enable prioritized diff loading last, because it changes request scheduling rather than repository semantics.
6. Rollback is component-wise: the compatibility `/workspace/files` route remains until the frontend migration is stable; the manifest index can be bypassed to synchronous computation without changing response shapes.

## Expected result

Opening Files no longer initiates repository-wide work. It paints the session's last complete manifest and stable tree model immediately. Workspace changes refresh that model in the background through versioned events. Directory expansion performs one bounded lazy read only when necessary, and selecting a file prioritizes that content ahead of bulk diff work. Pierre Trees improves the renderer's scaling behavior, but the daemon-side live manifest is what removes Git latency from the user's click.
