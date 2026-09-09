# AO Diff and File Viewer V2 Implementation Plan

**Status:** Proposed

**Last updated:** 2026-09-10

**Scope:** AO daemon, HTTP API, Electron/React frontend, tests, rollout, and migration
**Primary objective:** Make reviewing an agent's workspace fast, stable, scalable, and tightly connected to the agent conversation without weakening AO's daemon-owned security and Git boundaries.

## 1. Executive decision

Adopt `@pierre/diffs` behind an AO-owned renderer adapter and a feature flag, while keeping the Go daemon as the source of truth for workspace confinement, Git comparison semantics, file revisions, and live invalidation.

The first release should improve the read and review experience only. It should not combine the renderer migration with file editing, staging, discarding, committing, or submitting remote reviews. Those actions have different safety and lifecycle requirements and should be added only after the read path is stable.

The target experience combines:

- AO's existing session-scoped file tree, sections, file tabs, images, and agent annotations.
- Superset's grouped patch loading, stable diff objects, deferred hydration, and virtualized multi-file review.
- GitHub's viewed progress, filters, whitespace controls, collapsible files, and review-oriented navigation.
- Codex's tight loop between local changes, file inspection, selected code, and the active agent conversation.

Codex's internal implementation is not public documentation; references to Codex in this plan describe product-level workflow inspiration, not claims about its architecture.

## 2. Goals

1. Make both changed and unchanged files first-class and predictable.
2. Support a continuous, virtualized review of many changed files as well as focused single-file tabs.
3. Preserve exact staged, unstaged, untracked, committed, renamed, and multi-repository semantics.
4. Let users select lines or ranges and send structured context to the agent without copying an entire file into the prompt.
5. Remain responsive for large repositories, large patches, long lines, generated files, and binary files.
6. Detect workspace drift and avoid showing a patch from one revision beside content from another.
7. Keep filesystem access, Git execution, path validation, and write policy in the daemon.
8. Roll out incrementally with the current renderer available as a fallback until parity is demonstrated.
9. Provide accessible keyboard navigation, search, and clear loading, empty, stale, truncated, and error states.

## 3. Non-goals for the renderer migration

- Replacing AO's terminal, chat, or agent runtime lifecycle.
- Moving Git or filesystem access into Electron's renderer.
- Replacing `react-arborist` with `@pierre/trees` in the first release.
- Implementing a full GitHub pull-request review workflow.
- Adding staging, reverting, committing, branch switching, or conflict resolution.
- Automatically sending file contents or diffs to telemetry or an external service.
- Rendering every binary format. Unsupported formats should have a useful metadata view and safe external-open action.
- Making file editing a prerequisite for shipping the improved viewer.

## 4. What AO already has

This work is an evolution of the existing implementation, not a greenfield viewer.

### Backend today

- `ListWorkspaceFiles`, `GetWorkspaceFile`, and `GetWorkspaceFileBlob` support normal, scratch, and multi-repository workspaces.
- Changes are divided into staged, unstaged, untracked, and committed sections.
- Compare-base selection handles recorded bases, current-ref merge bases, pull-request bases, divergent histories, and a head fallback.
- File summaries expose status, previous path, additions, deletions, size, and binary state.
- File details expose current content, unified diff, image metadata, and truncation flags.
- Git rename detection is enabled.
- Workspace paths are confined and tested against traversal and symlink escape.
- Workspace file maps are cached and singleflight-protected.
- The filesystem watcher emits `workspace_changed` events and invalidates cached data.
- Current limits are 5,000 files, 256 KiB of text content, 512 KiB of patch data, and 16 MiB images.
- The tree endpoint lazily enumerates one directory at a time.

### Frontend today

- A session Files rail can switch between all files and changed files.
- The maximized explorer shows a tree and content side by side.
- Files can open in session-scoped center tabs while the agent surface remains mounted.
- Unmodified text files use a syntax-highlighted read-only viewer.
- Changed text files use a custom unified/split diff renderer with line numbers and word-level highlighting.
- Unified diffs over 150 rows are virtualized; split diffs are not.
- Images support before/after display, while unsupported binary files receive an unavailable state.
- Diff text can be selected, copied, explained, or sent to the agent as a structured change request.
- Line annotations can be attached to the agent composer.
- Patch parsing moves to a shared worker above 50,000 characters.
- Workspace events use shared SSE, debouncing, reconnect backoff, and polling fallback.

### Existing constraints that must remain true

- The React/Electron application stays a thin supervisor and client of the daemon.
- Absolute workspace paths never cross into the renderer.
- Multi-repository prefixes remain unambiguous.
- The terminal and agent thread are not remounted when a file tab opens or closes.
- Failed runtime probes are not treated as proof that a session is dead.
- File updates do not silently discard a user's current selection or draft annotation.

## 5. Problems to solve

### Rendering and maintenance

- AO owns a growing custom parser, row model, word diff, syntax highlighter, split renderer, virtualizer integration, selection behavior, and theme layer.
- Split view does not virtualize, so large patches regress sharply when users choose it.
- Worker parsing reduces main-thread work but does not solve DOM, layout, or extremely long-line cost.
- Every new review feature currently expands AO-specific renderer code and test burden.

### Data delivery

- A file detail request couples current content and patch generation for one file.
- A continuous review would otherwise create an N+1 request pattern.
- The daemon returns a fixed three lines of patch context with no way to expand an unchanged region.
- A truncated patch cannot be hydrated with complete before/after text through a general text revision API.
- The image blob endpoint has before/after semantics, but text revisions do not have an equivalent contract.
- Live events are coarse and cause broad query invalidation.
- Responses do not carry a stable snapshot or revision identifier, so content can drift during a review.

### Navigation and review workflow

- Only one selected file is shown at a time; there is no continuous review mode.
- There is no viewed state, progress indicator, next/previous change command, or status/type filter.
- There is no hide-whitespace preference or expandable unchanged context.
- Searching all files recursively loads the full tree, which is expensive for large workspaces.
- Browser-style `Cmd/Ctrl+F` cannot reliably search a virtualized multi-file diff through the DOM.

### File viewing

- Generated, minified, lock, and oversized files need deliberate load-on-demand behavior.
- Rename-only and mode-only changes need a useful view even when no textual patch exists.
- Deleted files should offer the previous file body, not only an empty current-content state.
- Rich presentation should be extensible without coupling Markdown, images, PDFs, or future formats to the core diff component.

## 6. Product model

The Files experience has two complementary surfaces.

### Focused file tab

A file selected from the tree opens in a center tab. The tab shows one file in the most useful available mode:

- **Diff** for a textual change.
- **File** for unchanged text, the current revision, or the previous revision of a deletion.
- **Rendered** for supported rich content such as images and, later, Markdown.

The user can switch modes when more than one representation is available. The active agent/terminal remains mounted below the tab layer.

### Review Changes

A session-scoped review surface presents changed files continuously and virtually, grouped by staged, unstaged, untracked, and committed sections. It supports:

- file collapse and expansion;
- reviewed/unreviewed progress;
- file, status, repository, and file-type filtering;
- unified or split layout;
- whitespace and wrapping preferences;
- data-driven search;
- previous/next result and previous/next file;
- line and multi-line selection sent to the agent;
- load-on-demand for generated, binary, oversized, and truncated files.

Opening Review Changes must not eagerly download every full file. It starts with grouped patches and hydrates revisions only as the user expands deferred content or enters File/Rendered mode.

### Behavior when there is no textual diff

| Condition | Default presentation | Available alternatives |
| --- | --- | --- |
| Unmodified text | Full current file | Rendered mode when supported |
| Empty untracked file | Empty file with Added badge and metadata | None |
| Pure rename | Rename header plus full current file | Before/after file modes |
| Mode-only change | Mode-change metadata plus current file | Previous revision |
| Deleted text | Full previous file with Deleted badge | Diff if a patch exists |
| Added text | Full current file or added-lines diff | Toggle File/Diff |
| Image change | Before/after image comparison | Before only, after only, metadata |
| Unsupported binary | Metadata and explanation | Safe open externally or reveal action if supported |
| Oversized/generated/minified | Deferred placeholder with size and reason | Explicit Load diff or Load file |
| Truncated patch | Visible partial patch and warning | Hydrate complete revisions if within full-file policy |
| Missing or stale revision | Non-destructive stale/error state | Refresh against the newest snapshot |

## 7. Inspiration translated into AO decisions

### From Superset

- Use `@pierre/diffs/react` for `CodeView`, `FileDiff`, and `File` rather than reimplementing every rendering primitive.
- Group patch requests by comparison scope instead of fetching each file independently.
- Keep parsed `FileDiffMetadata` object identities stable because partial hydration mutates the metadata objects.
- Defer generated artifacts, lockfiles, huge patches, and extreme long-line files.
- Hydrate before/after contents only when a partial file is expanded or an alternate mode requires them.
- Search the parsed diff data rather than the mounted DOM because virtualization means most rows are not mounted.
- Keep the library behind an application adapter so AO owns product behavior and can upgrade or replace it.

### From GitHub

- Make files collapsible and markable as viewed.
- Show reviewed progress and make next-unviewed navigation easy.
- Reset viewed state when that file's content fingerprint changes.
- Offer unified/split, hide-whitespace, path filtering, and status filtering.
- Support a selected line range and file-level context, not only a single line.
- Keep rich rendering an explicit mode rather than silently replacing source.

### From Codex

- Keep local change review adjacent to the live agent thread.
- Make “explain this” and “make changes here” operate on structured selections.
- Preserve the active task context while navigating files.
- Optimize the default path for quick inspection rather than requiring a formal review transaction.
- Use concise, actionable empty, loading, drift, and failure states.

### AO-specific choices

- Retain AO's existing file tree for the first migration. A second tree-library migration adds risk without being required to improve diff rendering.
- Preserve AO's four change sections because they reflect local workspace state more accurately than a pull-request-only model.
- Keep annotations and agent actions local to AO's conversation model.
- Treat reviewed state as an inspection aid, not an approval or Git mutation.

## 8. Target architecture

```mermaid
flowchart LR
    W[Workspace and Git state] --> S[Go session service]
    S --> C[HTTP controllers]
    C --> Q[Typed frontend API and TanStack Query]
    Q --> M[AO review model]
    M --> A[AO Pierre adapter]
    A --> P[@pierre/diffs worker and views]
    M --> T[Existing AO file tree]
    M --> G[Agent selection and annotations]
    W --> E[Workspace watcher]
    E --> V[Versioned SSE event]
    V --> Q
```

The daemon owns:

- repository discovery and comparison-base resolution;
- path confinement and symlink policy;
- Git command construction and process limits;
- section-specific before/after semantics;
- patch grouping and output budgets;
- raw file revision access;
- file classification hints and fingerprints;
- workspace version and stale-snapshot checks;
- optional durable review progress in a later phase.

The frontend owns:

- view layout and user preferences;
- stable `@pierre/diffs` metadata caches;
- virtualization, search indexing, and selection presentation;
- file collapse and temporary reviewed state;
- routing selections into the existing agent composer;
- preserving scroll, selection, and drafts across live updates;
- choosing a registered renderer for text, image, Markdown, or unsupported binary content.

## 9. Backend contract

Keep all existing workspace routes during migration. Add new contracts instead of changing existing response meanings under the old UI.

### 9.1 Comparison scopes

Every patch and revision request uses the same explicit scope vocabulary:

| Scope | Before | After |
| --- | --- | --- |
| `combined` | resolved comparison base | working tree |
| `committed` | resolved comparison base | `HEAD` |
| `staged` | `HEAD` | index |
| `unstaged` | index | working tree |
| `untracked` | absent | working tree |

For a rename, `before` resolves `previousPath` and `after` resolves `path`. A deleted file has no after revision. An added file has no before revision. The daemon, not the renderer, applies these rules.

The existing UI section identifiers should be mapped once at the API boundary so callers do not invent revision logic.

### 9.2 Workspace snapshot and file fingerprints

Extend list and detail responses with opaque identifiers:

- `workspaceVersion`: changes whenever relevant `HEAD`, index, repository set, or watched worktree state changes.
- `fileFingerprint`: changes when the specific comparison inputs for a file change.
- `beforeRevision` and `afterRevision`: opaque revision identifiers when those sides exist.

These values are comparison tokens, not stored display status. They may be derived from Git object IDs plus confined worktree metadata/content digests and cached with the existing workspace map. The exact hash inputs remain internal.

Requests that include `workspaceVersion` and require consistency return `409 WORKSPACE_SNAPSHOT_STALE` when the inputs no longer match. The UI can then preserve its current scroll/selection, show a stale banner, and refresh deliberately.

### 9.3 Grouped patch endpoint

Add:

```text
POST /api/v1/sessions/{sessionId}/workspace/diffs
```

Request:

```json
{
  "scope": "unstaged",
  "paths": ["frontend/src/App.tsx", "backend/main.go"],
  "contextLines": 3,
  "ignoreWhitespace": false,
  "workspaceVersion": "opaque-version"
}
```

Response:

```json
{
  "workspaceVersion": "opaque-version",
  "groups": [
    {
      "repository": "frontend",
      "patch": "diff --git ...",
      "truncated": false,
      "includedPaths": ["frontend/src/App.tsx"],
      "deferred": [],
      "errors": []
    }
  ]
}
```

Requirements:

- Accept a bounded path count and total request size.
- Validate every path through existing workspace/repository resolution.
- Group paths by actual repository and comparison scope.
- Run a bounded number of Git processes, not one process per file.
- Return partial per-group results when one repository fails.
- Carry explicit deferred reasons such as `generated`, `oversized`, `binary`, `long_line`, and `budget_exceeded`.
- Preserve rename metadata and deterministic ordering.
- Cap output per group and overall response, reporting truncation instead of silently cutting a patch.
- Support `contextLines` within a safe range and `ignoreWhitespace` with documented Git semantics.

The response contains unified patch text, not Pierre-specific serialized objects. This keeps the daemon independent of a frontend rendering package.

### 9.4 File revision endpoint

Add:

```text
GET /api/v1/sessions/{sessionId}/workspace/file/revision
```

Parameters:

- `path`
- `scope`
- `side=before|after`
- optional `workspaceVersion`
- optional `expectedRevision`

Text response:

```json
{
  "path": "frontend/src/App.tsx",
  "side": "before",
  "revision": "opaque-revision",
  "mediaType": "text/typescript",
  "encoding": "utf-8",
  "size": 18342,
  "content": "...",
  "truncated": false
}
```

Requirements:

- Resolve the side according to the comparison-scope table.
- Return an explicit absent-side response for additions and deletions.
- Distinguish unsupported binary, invalid UTF-8, oversized, missing, and stale states.
- Permit a higher, independently configured hydration limit than the initial detail preview, while maintaining a hard ceiling.
- Reuse the image blob route or generalize it without placing base64 image data in JSON.
- Send `Cache-Control: no-store` and `X-Content-Type-Options: nosniff`.
- Never return host absolute paths.

This endpoint supplies `loadDiffFiles` for partial `@pierre/diffs` metadata and powers before/current modes for rename-only and deleted files.

### 9.5 Server-side file search

Avoid recursively downloading every tree node when the user filters All Files.

Either extend the tree endpoint with `q`, `cursor`, and `limit`, or add:

```text
GET /api/v1/sessions/{sessionId}/workspace/search?query=...&cursor=...&limit=...
```

The implementation should:

- search paths, not file contents, in the first release;
- combine tracked files with confined, non-ignored filesystem entries according to current All Files semantics;
- return repository identity and path segments needed to reveal a result in the tree;
- use cursor pagination and a result cap;
- cancel promptly through `context.Context`;
- exclude AO data, `.git` internals, and existing ignored paths.

Content search is a separate feature and should use a purpose-built bounded search process rather than reading files through the renderer.

### 9.6 Targeted workspace events

Extend `workspace_changed` data to include:

```json
{
  "workspaceVersion": "opaque-version",
  "repositories": ["frontend"],
  "changedPaths": ["frontend/src/App.tsx"],
  "overflow": false
}
```

- Coalesce events over the existing debounce interval.
- Bound the path list; set `overflow=true` when a full refresh is required.
- Do not include file contents, patch text, or host paths.
- Let the frontend invalidate only affected details and review items when safe.
- Keep the current broad invalidation fallback for old daemons and overflow events.

### 9.7 Optional review-progress persistence

Ship local in-memory/session persistence first. After the UX is validated, optionally store reviewed state in the daemon so it survives a desktop restart and can be shared by future clients.

If persisted, it is a durable user action, not derived session status. Key it by:

- session ID;
- comparison scope;
- repository-relative path;
- file fingerprint.

A changed fingerprint is automatically unreviewed. A migration must be additive, and storage writes must emit normal database change events rather than a parallel manual CDC path.

### 9.8 Git execution hardening

All new Git execution must use argv arrays and the existing process abstraction. For read-only viewer commands:

- disable the pager, external diff, and textconv;
- disable optional locks;
- prevent repository hooks from participating;
- avoid user-provided aliases or shell evaluation;
- apply time, stdout, stderr, path-count, and concurrency budgets;
- preserve `--find-renames` where semantically required;
- pass user-derived revisions only after resolving them through AO-owned comparison state;
- cancel child processes when the request context ends;
- return typed errors without exposing sensitive command lines or filesystem roots.

Reuse the secure reviewer gateway principles where applicable, while keeping this endpoint read-only and scoped to session workspaces.

## 10. Frontend design

### 10.1 AO-owned diff adapter

Add a narrow adapter layer, for example:

```text
frontend/src/renderer/components/diffs/
  AoDiffFile.tsx
  AoDiffReview.tsx
  AoFileView.tsx
  diff-options.ts
  diff-theme.ts
  pierre-metadata-cache.ts
  useDiffHydration.ts
  useDiffSearch.ts
```

Only this directory should import `@pierre/diffs`. AO components consume AO domain types and callbacks. This prevents package-specific state from leaking through `SessionView`, the tree, tabs, or the agent composer.

The adapter must:

- translate AO status and scope into Pierre file metadata;
- retain metadata object identity for the lifetime of a workspace version;
- release caches for closed sessions and superseded versions;
- map line/range selections to AO's existing `FileAnnotationTarget` and structured diff selection types;
- provide consistent loading, deferred, empty, truncated, binary, and error slots;
- translate theme tokens for light, dark, and high-contrast modes;
- expose a fallback to the current `WorkspaceDiffView` during rollout.

### 10.2 Dependency adoption gate

Before merging the first renderer PR:

- evaluate the current stable `@pierre/diffs` release rather than copying Superset's pin blindly;
- pin an exact tested version;
- record Apache-2.0 attribution and update third-party notices if required;
- measure production bundle size, worker asset size, startup cost, and duplicate Shiki/highlighter dependencies;
- confirm Electron CSP and worker packaging in development and packaged builds;
- validate keyboard, screen-reader, Shadow DOM styling, selection, and context-menu hooks;
- validate behavior on macOS, Windows, and Linux scale factors;
- document upgrade steps and known long-line constraints.

If a blocking limitation appears, retain the same AO adapter and trial an alternate implementation without changing backend contracts.

### 10.3 Patch loading and metadata cache

Create `useWorkspaceDiffGroups` to request patches only for visible or soon-visible sections. Parse group patches through Pierre's worker integration.

Cache parsed metadata by:

```text
sessionId + workspaceVersion + scope + repository + rendererOptions
```

Do not recreate parsed file metadata during ordinary React rerenders. Partial hydration updates the cached instance. Evict on session close, version replacement, bounded LRU pressure, or an explicit refresh.

Initial loading policy:

- fetch summaries first;
- fetch the first visible group immediately;
- prefetch the next small group while idle;
- defer collapsed sections;
- do not fetch full before/after revisions unless Pierre requests hydration;
- abort obsolete requests when scope, session, or workspace version changes.

### 10.4 Continuous review surface

Use `CodeView` for the virtualized multi-file surface. The surrounding AO UI owns:

- section headers and counts;
- repository labels;
- filters and search controls;
- reviewed progress;
- collapse state;
- empty/error/retry states;
- navigation commands;
- agent actions and annotation presentation.

Generated files and known lockfiles remain collapsed and unhydrated by default. Users can opt in per file. Very long-line or oversized files show a reason and safe actions rather than freezing the renderer.

### 10.5 Focused file surface

Replace the implementation behind `FileContentPane` incrementally:

- use the adapter's single-file diff for changed text;
- use the adapter's full-file view for unchanged text;
- retain the existing image comparison until the adapter's renderer registry absorbs it;
- preserve the current fallback when a patch is empty but content exists;
- add explicit Diff, File, and Rendered tabs only when each mode is available;
- show previous/current revision controls for deletions, renames, and modifications;
- keep current truncation and binary messaging until the new hydration path is active.

### 10.6 Review state and preferences

Persist display preferences locally:

- unified or split;
- wrap lines;
- ignore whitespace;
- context-line preference;
- show line numbers;
- default generated-file policy.

Keep collapse, active result, and reviewed state scoped to session and file fingerprint. Never write “viewed” into Git. Reset reviewed state when the fingerprint changes.

### 10.7 Data-driven search

Implement two distinct searches:

- **Path search:** server-side search over All Files and client filtering over already loaded changed-file summaries.
- **Review search:** search parsed patch data and hydrated content, independent of mounted DOM rows.

Review search must return file, side, and line coordinates, highlight mounted matches, use `CodeView` scrolling APIs, and navigate across virtualized files. It should state when deferred files have not been searched and offer to load them within a budget.

### 10.8 Agent interaction

Preserve and improve AO's existing structured selection flow:

- support one line, a contiguous range, or file-level selection;
- include path, previous path when relevant, scope, side, start/end lines, and workspace/file revisions;
- send only selected text plus a small configurable context window by default;
- show an estimated selection size before adding unusually large context;
- reject or refresh stale selections rather than silently referring to different code;
- keep Copy, Explain, and Make changes actions;
- support keyboard invocation and accessible labels;
- preserve a draft annotation during background workspace events.

This is a primary token-cost control: the agent receives precise context rather than the whole patch or file unless the user explicitly asks for it.

### 10.9 File renderer registry

Introduce an AO-owned registry selected by media type and file characteristics:

1. text source;
2. text diff;
3. image before/after;
4. Markdown source/rendered preview;
5. unsupported binary metadata;
6. future opt-in renderers such as PDF or dependency diffs.

Every rich renderer must offer a source or metadata fallback. Rich renderers must not execute workspace scripts, remote HTML, or arbitrary active content.

### 10.10 Live-update behavior

When a targeted event affects an unopened file, update its summary without disturbing the viewport. When it affects the active file:

- if there is no selection, draft, or pending hydration, refresh in place and preserve the nearest line anchor;
- if the user is selecting text or composing an annotation, show a non-blocking “Workspace changed” banner;
- if a request returns `WORKSPACE_SNAPSHOT_STALE`, keep the old view visibly marked stale until refresh;
- never combine a before revision from one workspace version with an after revision from another;
- clear reviewed state only for files whose fingerprint changed.

### 10.11 Accessibility and keyboard model

- `Cmd/Ctrl+P`: focus path search.
- `Cmd/Ctrl+F`: search the current file or review model.
- `F3` / `Shift+F3`: next/previous search match.
- `]c` / `[c`, plus command-palette equivalents: next/previous changed file.
- A command moves to the next unreviewed file.
- Enter expands/collapses the focused file; Space toggles viewed when focus is on its header.
- Focus must remain visible and must not be trapped inside a virtualized or Shadow DOM surface.
- Add accessible names for status, additions/deletions, side, truncation, and deferred reasons.
- Do not rely only on red/green color to communicate a change.

## 11. Performance budgets

Measure in a packaged desktop development build on a documented reference machine. Initial targets:

- changed-file summaries interactive within 500 ms after a warm daemon response;
- first visible diff rendered within 750 ms for a normal patch under 1 MiB;
- scroll stays at or above 50 fps through a 100,000-row synthetic review after initial parsing;
- no long task over 100 ms during ordinary scrolling or file collapse;
- no more than four concurrent patch/revision requests per session;
- no N+1 Git process per file in continuous review;
- bounded renderer metadata cache with observable eviction;
- opening a 1,000-file review does not hydrate 1,000 complete file revisions;
- a single minified line of several megabytes never enters word-level diff or unrestricted DOM rendering;
- worker, Shiki grammar, and patch caches release when a session closes;
- production bundle increase is measured and called out in the PR, with a target budget set after the dependency spike.

Performance telemetry may record durations, counts, byte buckets, deferred reasons, and failures. It must never include paths, source text, patch text, repository URLs, or user queries.

## 12. Backend implementation sequence

### Phase B1: Contract foundations

1. Extract comparison-scope resolution so list, patch, revision, and image paths share one implementation.
2. Add `workspaceVersion`, file fingerprints, and before/after revision fields to internal models.
3. Extend controller DTOs and the code-first API specification.
4. Run `npm run api` and commit the OpenAPI and frontend type outputs with the eventual implementation.
5. Preserve all current fields and old-client behavior.

Primary files:

- `backend/internal/service/session/workspace_files.go`
- new focused files under `backend/internal/service/session/` if needed for patch/revision logic
- `backend/internal/httpd/controllers/dto.go`
- `backend/internal/httpd/controllers/sessions.go`
- `backend/internal/httpd/apispec/specgen/build.go`
- generated `backend/internal/httpd/apispec/openapi.yaml`
- generated `frontend/src/api/schema.ts`

### Phase B2: Grouped patches and revisions

1. Implement bounded multi-path patch requests.
2. Add full text before/after revision retrieval.
3. Share existing confinement, rename, scratch, and multi-repository logic.
4. Return per-group typed errors and deferred reasons.
5. Add stale-snapshot checks and cancellation.
6. Harden Git environment and flags consistently.

### Phase B3: Search and targeted events

1. Add paginated server-side path search.
2. Add bounded path metadata to workspace events.
3. Preserve broad refresh compatibility.
4. Instrument request duration, output size buckets, truncation, and cancellation without sensitive values.

### Phase B4: Optional review persistence

Only after frontend review state is validated:

1. Add an additive SQLite migration and queries.
2. Store viewed state by file fingerprint.
3. Expose small list/update routes.
4. Let triggers provide database change events.
5. Prune state for removed sessions according to lifecycle policy.

## 13. Frontend implementation sequence

### Phase F0: Dependency and rendering spike

Build an internal spike covering unified, split, large file, long line, selection, annotation, worker packaging, themes, and Electron production build. Record findings in the implementation PR. Do not delete the current renderer.

### Phase F1: Single-file adapter behind a flag

1. Add the dependency and AO adapter.
2. Render a changed text file through the adapter.
3. Preserve image, binary, empty, and read-only fallbacks.
4. Map existing Copy, Explain, Make changes, and annotation actions.
5. Compare current and new rendering through tests and fixture screenshots.

### Phase F2: Backend hydration integration

1. Consume grouped patch and revision endpoints.
2. Add stable metadata caching.
3. Implement partial hydration and stale-version handling.
4. Add deferred generated/oversized behavior.
5. Keep legacy detail fetching as an old-daemon fallback during the compatibility window.

### Phase F3: Review Changes surface

1. Add continuous virtualized review with section grouping.
2. Add collapse, reviewed progress, next/previous, and next-unreviewed navigation.
3. Add status, repository, file-type, and path filters.
4. Add unified/split, wrap, context, and whitespace settings.
5. Preserve center file tabs and the maximized explorer.

### Phase F4: Search, live updates, and rich modes

1. Replace recursive All Files filtering with server-side path search.
2. Add data-driven review search.
3. Use targeted SSE invalidation and preserve scroll/selection.
4. Add the file renderer registry and safe Markdown preview.
5. Harden accessibility and keyboard behavior.

### Phase F5: Default-on and cleanup

1. Compare errors, performance, memory, and fallback rate during the opt-in period.
2. Make the new viewer default while retaining an emergency fallback for one release.
3. Remove the custom diff parser, renderer, highlighter, and tests only after feature parity and packaged-app validation.
4. Remove compatibility API paths only through a separately announced deprecation.

## 14. Optional editing phase

Editing should be a separate proposal and rollout after the viewer is reliable. If approved, use an explicit daemon write endpoint with:

- an expected after-revision for optimistic concurrency;
- UTF-8 and size validation;
- the existing path and symlink confinement policy;
- atomic replacement while preserving appropriate file mode;
- a `409` conflict that offers reload or intentional overwrite;
- clear dirty/external-change indicators;
- no automatic staging or committing;
- confirmation for overwriting a newer disk revision;
- tests for concurrent writes, deletion, rename, permissions, and watcher refresh.

The frontend editor may use Pierre's edit package only after its behavior, licensing, bundle impact, undo model, and conflict UX pass a separate spike. Viewing must not depend on loading editor code.

## 15. Testing strategy

### Backend unit and service tests

Cover:

- every comparison scope in the before/after matrix;
- modified, added, deleted, renamed, copied if supported, and mode-only files;
- partially staged files where index and working tree differ;
- untracked empty and non-empty files;
- compare-base, pull-request-base, divergent-history, and head-fallback behavior;
- scratch and multi-repository workspaces;
- invalid paths, `.git` paths, traversal, symlinks, and rename path confinement;
- binary, invalid UTF-8, image, large, truncated, generated, lock, and extreme long-line files;
- grouped partial success, cancellation, timeouts, output limits, and process concurrency;
- snapshot drift and revision mismatch;
- deterministic ordering and fingerprint changes;
- search pagination, ignore behavior, and cancellation;
- targeted event coalescing and overflow.

### HTTP and contract tests

- request validation and status codes;
- stale error envelope and request IDs;
- content types, nosniff, and no-store headers;
- route/spec parity;
- generated OpenAPI and TypeScript type drift;
- old endpoint compatibility.

### Frontend tests

- adapter translation for every file status and scope;
- stable metadata identity and cache eviction;
- unified/split, themes, wrapping, whitespace, and context settings;
- deferred hydration, error, retry, stale, and truncated states;
- added, deleted, pure rename, mode-only, empty, unchanged, binary, and image views;
- selection/range mapping and agent payload size boundaries;
- annotation drafts surviving unrelated SSE events;
- reviewed state resetting only on fingerprint changes;
- virtualized search navigation to initially unmounted rows;
- path search and reveal-in-tree;
- keyboard navigation, focus restoration, and accessible names;
- feature-flag fallback.

### Performance fixtures

Maintain deterministic fixtures for:

- 1,000 changed files;
- 100,000 rendered rows;
- a large split diff;
- a multi-megabyte minified line;
- large lockfiles and generated artifacts;
- many small multi-repository changes;
- binary and image before/after pairs;
- repeated live updates during scroll and selection.

Track parse time, first render, long tasks, memory growth, request count, Git process count, hydration bytes, and cache release.

### Electron end-to-end checks

- open Files, maximize, open tabs, switch sessions, and return without agent remount;
- review and annotate a change, then send it to the real session composer;
- change a file externally and verify targeted refresh/stale behavior;
- verify worker assets and themes in a packaged build;
- test offline daemon, daemon restart, watcher degradation, and retry states;
- test macOS, Windows, and Linux keyboard modifiers and scaling.

## 16. Rollout and observability

1. Land additive backend contracts first.
2. Ship the new renderer under a developer flag.
3. Add an opt-in setting for broader testing.
4. Compare old/new fallback rate, failures, latency, memory, and bundle cost.
5. Enable by default only after packaged-app and accessibility gates pass.
6. Retain the old renderer for one release as an emergency fallback.
7. Remove old code in a dedicated cleanup PR, not during the initial migration.

Useful privacy-safe metrics:

- list, grouped patch, revision, and search latency buckets;
- patch and hydration byte buckets;
- visible file count and deferred reason counts;
- parse/render duration and long-task counts;
- stale responses and SSE overflow events;
- fallback-renderer activations;
- worker failures and cache memory estimates.

Logs and telemetry must redact paths, refs that may contain user data, file contents, patches, selections, repository remotes, and search queries.

## 17. Suggested PR slices

1. **Backend snapshot and revision contract** — identifiers, scope resolver, revision endpoint, generated API artifacts, tests.
2. **Backend grouped patches and path search** — bounded batching, secure Git invocation, search, targeted events, tests.
3. **Pierre adapter spike and single-file flag** — dependency, adapter, themes, worker packaging, existing agent actions, packaged-build evidence.
4. **Continuous Review Changes** — `CodeView`, grouping, filters, collapse, reviewed state, navigation, preferences.
5. **Hydration, search, and live-update hardening** — partial file loading, data-driven search, stale UX, targeted invalidation, performance fixtures.
6. **Rich file modes and accessibility** — renderer registry, Markdown, image integration, keyboard and screen-reader validation.
7. **Default-on and legacy cleanup** — only after metrics and parity gates.
8. **Optional editing** — separate spec, API, security review, and PR series.

Each PR should state changed line counts, test line counts, generated line counts, commands run, packaged-app gaps, bundle impact where relevant, and known follow-ups.

## 18. Acceptance criteria

The read/review project is complete when:

- users can browse unchanged files and review all change sections without changing session context;
- focused and continuous views agree on status, revisions, and line numbers;
- unified and split modes remain responsive on the agreed large fixtures;
- before/after hydration works for staged, unstaged, committed, untracked, deleted, and renamed files;
- generated, oversized, binary, and long-line files cannot freeze the app;
- path and review search work with virtualized content;
- selection and annotation payloads are revision-aware and token-bounded;
- live changes never silently combine mismatched revisions;
- reviewed state resets when a file changes;
- all filesystem and Git access remains behind the daemon;
- traversal, symlink, `.git`, external-diff, textconv, hook, and resource-limit tests pass;
- current file tabs, images, multi-repository paths, scratch workspaces, SSE fallback, and agent mounting behavior remain intact;
- the packaged Electron app passes theme, worker, keyboard, and accessibility checks;
- the old renderer can be removed without losing a documented user-visible feature.

## 19. Default decisions and open questions

### Recommended defaults

- Use `@pierre/diffs` for the renderer and keep it behind an AO adapter.
- Keep `react-arborist` for navigation during this project.
- Default to unified view on narrow panes and the user's saved preference elsewhere.
- Start with three context lines and allow bounded expansion.
- Keep whitespace changes visible by default.
- Collapse generated and lock files by default.
- Store reviewed state locally first; evaluate daemon persistence later.
- Treat editing and Git mutations as separate projects.

### Decisions to confirm during the spike

- Exact `@pierre/diffs` version and acceptable production bundle budget.
- Whether the first continuous review includes all four sections at once or one selected section at a time. The recommended first implementation is one section at a time with counts for all sections, reducing ambiguity for partially staged files.
- Maximum full-text hydration size and whether an explicit second confirmation permits a larger local-only read.
- Which file patterns AO classifies as generated or lock files, and whether `.gitattributes` should influence that hint.
- Whether reviewed state needs restart persistence in the first default-on release.
- Whether safe Markdown rendering belongs in this project or a follow-up.

## 20. Reference links

- AO current file-tab design: [`docs/superpowers/specs/2026-08-24-session-file-tabs-design.md`](../specs/2026-08-24-session-file-tabs-design.md)
- AO current file-tab implementation plan: [`docs/superpowers/plans/2026-08-24-session-file-tabs.md`](./2026-08-24-session-file-tabs.md)
- AO architecture: [`docs/architecture.md`](../../architecture.md)
- AO secure interactive reviewer gateway ADR: [`docs/adr/0002-secure-interactive-reviewer-gateway.md`](../../adr/0002-secure-interactive-reviewer-gateway.md)
- Superset `@pierre/diffs` dependency: <https://github.com/superset-sh/superset/blob/main/apps/desktop/package.json#L79>
- Superset DiffPane: <https://github.com/superset-sh/superset/blob/main/apps/desktop/src/renderer/routes/_authenticated/_dashboard/v2-workspace/%24workspaceId/hooks/usePaneRegistry/components/DiffPane/DiffPane.tsx>
- Superset LightDiffViewer: <https://github.com/superset-sh/superset/blob/main/apps/desktop/src/renderer/screens/main/components/WorkspaceView/ChangesContent/components/LightDiffViewer/LightDiffViewer.tsx>
- Pierre diffs repository: <https://github.com/pierrecomputer/pierre/tree/main/packages/diffs>
- Pierre diffs documentation: <https://diffs.com/docs>
- Pierre performance article: <https://pierre.computer/writing/on-rendering-diffs>
- GitHub reviewing proposed changes: <https://docs.github.com/en/pull-requests/how-tos/review-pull-requests/reviewing-proposed-changes-in-a-pull-request>
- OpenAI Codex use cases: <https://developers.openai.com/codex/use-cases>
