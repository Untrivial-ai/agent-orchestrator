# Cloud Workspace Review Parity Design

**Date:** 2026-09-20

## Goal

Give Cloud sessions the same user-visible File Diff and File View capabilities as local sessions while keeping the local implementation unchanged. The Cloud implementation must work through the common worker protocol for Docker, NodeOps, and Coder.

Parity includes:

- switching between changed files and all repository files;
- browsing a lazy repository tree and searching paths/content;
- separating committed, staged, unstaged, and untracked changes;
- selecting commits and viewing their file summaries;
- unified and split diffs with configurable context and whitespace handling;
- current, before, and after file revisions;
- binary, deleted, renamed, oversized, and truncated-file states;
- file view, rendered Markdown view, safe editing/saving, viewed state, and review annotations;
- refresh behavior when an agent or browser changes the workspace.

## Non-goals and protected surfaces

This work will not change the local workspace implementation. In particular, it will not modify local daemon workspace services or routes, `SessionFileExplorer`, `FileTree`, `WorkspaceReviewPane`, `FileContentPane`, or the local workspace hooks. Existing local behavior remains the reference behavior, not a refactoring target.

Cloud will have a separate implementation under Cloud-specific backend and frontend paths. The shared `SessionView` may receive only the minimum Cloud-branch wiring needed to pass Cloud file-tab options; its local branch and local data flow must remain unchanged. Existing generic UI primitives may be imported without modification.

The work will not deploy to staging or production. Validation will use unit/integration tests and the local Docker Cloud stack.

## Why the current implementation fails

The current Cloud feature is a summary adapter, not the local review system. `workspace.diff` combines changes from a moving Git comparison base and returns one flat file list. `workspace.diff-file` returns one current file and one combined patch. The Cloud UI therefore has no model for staged versus unstaged changes, commit selection, full repository browsing, revision sides, batch patches, or the workspace-version checks used by local review.

The current Cloud `/workspace/files` route is also semantically different from local: it is a paginated directory listing, while local `/workspace/files` is the complete workspace review summary. Reusing names without preserving semantics would keep producing partial parity.

## Architecture

### 1. Cloud-only review contract

Add Cloud-specific request and response types mirroring the behavior of the local review model without importing local controller or service packages. The main Cloud read model contains:

- an opaque `workspaceVersion` derived from relevant Git/index/worktree state;
- the recorded comparison base SHA and display ref;
- summaries for all files in the repository;
- `staged`, `unstaged`, `untracked`, and `committed` sections;
- recent commits and each commit's changed files;
- aggregate additions/deletions and ahead/behind information;
- truncation metadata.

Supporting operations provide:

- lazy directory tree reads;
- repository search;
- section- and commit-aware file details;
- batch patches for selected paths and scope;
- before/after revision content;
- fingerprint-checked file writes.

These operations use new, explicit worker request kinds rather than overloading the existing lightweight commands. The old routes can remain temporarily compatible while the Cloud UI moves to the full contract.

### 2. Worker-owned Git and filesystem behavior

All Git and filesystem inspection runs in `ao-worker` inside the session sandbox. This makes the implementation provider-neutral: Docker, NodeOps, and Coder execute the same worker binary and receive identical requests.

The worker implementation will follow the local behavior but remain Cloud-owned:

- paths are repository-relative and resolved through the existing confined workspace root;
- tracked files come from Git and untracked files honor ignore rules;
- staged changes compare `HEAD` to the index;
- unstaged changes compare the index to the worktree;
- untracked files receive bounded synthetic patches;
- committed changes compare the recorded checkout base with `HEAD`;
- commit browsing uses bounded Git history and per-commit stats;
- rename/copy metadata preserves the previous path;
- binary and oversized content returns metadata instead of unsafe text;
- all output, execution time, file size, result size, and item counts are bounded.

### 3. Stable comparison base

New checkouts record their initial comparison SHA in a private AO Git ref inside the workspace. That ref survives normal branch changes and prevents a later fetch or pull from moving the review baseline.

Existing Cloud sessions that predate the private ref resolve a fallback once from the configured default remote branch using merge-base, then create the private ref. If that remote ref is unavailable, the worker falls back to the earliest safe reachable point and clearly reports the selected base. It must never silently treat the current moving `HEAD` as the base when committed changes exist.

This design avoids a control-plane database migration: the comparison base belongs to the checked-out repository and travels with checkpoint/restore data. The control plane only proxies the worker result.

### 4. Workspace consistency

Every summary response includes `workspaceVersion`. Batch diff, revision, and write requests include the version or file fingerprint they were based on. When the workspace changes during a review, the worker returns a conflict response so the UI refreshes rather than combining stale summaries with new patches.

Browser-originated writes continue to emit `workspace.changed`. Because agents can change files without using the HTTP write route, Cloud queries also poll while the Files surface is visible. Session events trigger immediate invalidation; polling is the fallback and is paused when the surface is inactive.

### 5. Cloud-only frontend

Replace the simplified `CloudWorkspaceDiff` internals with Cloud-specific components and hooks that reproduce the local interaction model. The implementation may copy behavior and presentation structure from local, but it will not alter or route through local hooks.

The Cloud component tree will include:

- a Cloud file explorer shell with Changes/All Files, search, split/unified controls, and maximize behavior;
- a Cloud lazy file tree;
- a Cloud review pane with scope and commit selection;
- a Cloud diff file renderer fed by Cloud revision endpoints;
- a Cloud file content pane with file/diff/rendered modes and editing;
- Cloud-specific React Query keys, invalidation, and client methods.

Cloud file tabs carry the same scope, commit, display-mode, and editing state used by the local experience, but their content always calls the Cloud control plane. They must never fall through to loopback daemon workspace endpoints.

Viewed state and annotations remain UI/session state just as in the local experience. Existing generic annotation and diff presentation primitives may be consumed read-only; Cloud-specific adapters own all data fetching.

## HTTP and transport flow

The browser calls the authenticated Cloud control plane. The control plane validates organization/session access, resumes an idle sandbox if necessary, creates a bounded durable worker request, waits for the fenced worker response, validates its shape, and returns it.

The proposed Cloud routes are:

- `GET /workspace/review` - complete review summary;
- `GET /workspace/tree` - one lazy tree level;
- `GET /workspace/search` - bounded search;
- `GET /workspace/review/file` - scoped or commit-specific detail;
- `POST /workspace/review/diffs` - batch patches;
- `GET /workspace/review/revision` - before/after content;
- `PUT /workspace/review/file` - fingerprint-checked write.

Using `/workspace/review` avoids changing the established meaning of Cloud `/workspace/files`. Existing lightweight `/workspace/diff` and `/workspace/file/diff` routes remain compatibility endpoints until no caller depends on them.

## Error handling

Invalid paths and unsupported scopes return validation errors before dispatch. Disconnected or unavailable workers preserve the existing Cloud error envelope. Worker timeouts, stale workspace versions, stale file fingerprints, binary content, oversized files, and truncated results have distinct machine-readable codes so the UI can show the same targeted states as local.

A failure in one deferred large-file patch does not blank the entire review. The summary and other files remain visible, with retry available for the failed item.

## Verification

Implementation proceeds test-first and includes:

- worker tests for all four change sections, commits, renames, binaries, untracked files, stable base capture, revisions, search, tree browsing, writes, stale versions, and path confinement;
- supervisor protocol dispatch tests for every new request kind;
- control-plane handler tests proving provider-independent dispatch for Docker, NodeOps, and Coder and preserving auth/error envelopes;
- Cloud client URL/body/response tests;
- Cloud UI tests for Changes/All Files, section and commit selection, tree expansion, search, split/unified diffs, file/revision/rendered views, editing, refresh, empty states, and errors;
- regression assertions that Cloud sessions never call local daemon workspace endpoints;
- local workspace tests run unchanged as a safety check;
- Cloud Go tests, frontend typecheck/tests/build, and the local Docker Cloud smoke flow.

Visual verification will exercise one Cloud session in the desktop app against the local Docker stack, including a committed change, a staged change, an unstaged change, and an untracked file. Staging deployment is explicitly excluded.

## Delivery sequencing

1. Establish worker behavior and protocol types with failing tests.
2. Add control-plane routes and provider-independent dispatch tests.
3. Add Cloud client types and hooks.
4. Build the Cloud-only tree, review, diff, and file-view components.
5. Wire Cloud file tabs without changing the local path.
6. Run regression, integration, build, Docker smoke, and visual checks.

Each layer remains usable and testable independently, and compatibility routes stay intact until the full Cloud UI has switched over.
