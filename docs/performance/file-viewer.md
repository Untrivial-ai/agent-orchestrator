# File viewer performance

The local workspace viewer optimizes for zero blocking latency: the Files shell
and last complete changed-file snapshot render without waiting for a new Git
scan. Literal zero execution time is not possible, so performance work should
be evaluated as time to first useful tree and first visible diff.

## Data flow

1. The daemon builds a compact changed-file manifest without enumerating the
   complete tracked-file inventory or reading commit history.
2. A bounded per-session LRU index retains the last complete manifest. Reads of
   a warm entry launch no Git process.
3. Workspace events mark the entry stale, retain it for readers, and coalesce a
   background refresh. A generation check retries when another change arrives
   during that refresh.
4. SSE sends a coarse `dirty` event and then a completed manifest version. The
   renderer keeps its current tree for `dirty`, deduplicates versions, and
   invalidates the shared TanStack query only for a new completed version.
5. Commit/upstream metadata loads from `/workspace/history` after the manifest;
   the legacy `/workspace/files` inventory remains available to older clients.
6. `@pierre/trees` retains a canonical path model and virtualizes both Changed
   Files and the lazy All Files browser. Diff patches start with a 24-file
   viewport-sized batch, then continue in bounded background waves.

The index is a performance cache only. Git and the worktree remain the source
of truth. Refresh failures retain a stale last-good snapshot; missing initial
snapshots return the existing API error. The index evicts least-recently-read
sessions after 128 entries.

## Diagnostics

Renderer timing marks are disabled by default and kept only in a 128-entry
in-memory ring. Enable them from development diagnostics with
`setFileViewerPerformanceEnabled(true)`, reproduce the interaction, and inspect
`getFileViewerPerformanceEntries()`. Entries contain only phase names and
timestamps—never session IDs, repositories, or file paths—and are not emitted
as telemetry.

Useful phases are `files-click`, `files-shell-painted`, `manifest-ready`,
`tree-painted`, `file-selected`, `file-header-painted`, and
`file-content-painted`.

## Verification

```bash
cd backend
go test ./internal/service/session -run 'WorkspaceManifest|WorkspaceCache'
go test -race ./internal/service/session -run 'WorkspaceManifest|WorkspaceCache'

cd ../frontend
npm test -- --run src/renderer/lib/file-viewer-performance.test.ts \
  src/renderer/components/FileTree.test.tsx \
  src/renderer/components/diffs/WorkspaceReviewPane.test.tsx
npm run typecheck
```

For real-app checks, use an isolated AO desktop lab and compare first useful
paint for docked/maximized Files, tab reopen, rapid file navigation, active
agent writes, large lockfiles, and SSE reconnect. Report p50/p95 with repository
size; do not describe the result as literal zero milliseconds.
