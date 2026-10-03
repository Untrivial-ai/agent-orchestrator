# Command Palette Workspace File Search Implementation Plan

**Goal:** Let people find and open workspace files from Command-K without silently mixing different session worktrees.

**Architecture:** Keep Command-K as a renderer-owned navigation surface. Reuse the existing bounded workspace path-search APIs for local, connected-host, and Cloud sessions; do not read the filesystem from Electron and do not add a second index. Search the current session directly. When only a project is in scope, enter a session/worktree picker before searching. Deliver the selected path to `SessionView` through a transient one-shot UI-store request, then reuse the existing Files inspector and center-file tab flow.

**Tech Stack:** React 19, TypeScript, TanStack Query, TanStack Router, Zustand, cmdk, Vitest, Testing Library.

## Product behavior

### In a session

- The empty Command-K suggestion list includes **Search files…** in the Current group.
- Typing at least two non-whitespace characters in the root palette also searches that session's worktree and adds a **Files** result group alongside projects, sessions, PRs, and commands.
- Choosing **Search files…** enters a file-only search view for the current session. This is useful when ordinary command results would be distracting.
- A result shows the basename as its title and the relative parent directory as its subtitle. The complete path remains in its searchable keywords and accessible name.
- Selecting a result closes Command-K, reveals the Files inspector, switches it to All Files, and opens the existing center-file tab.

### On a project page without a current session

- The empty Command-K suggestion list includes **Search files…** in the Current group when the project has at least one searchable worker session.
- Selecting it opens a nested **Choose a session** view. Rows show the session title, branch, and lifecycle state so duplicate filenames are not presented without worktree context.
- Active sessions sort before terminated sessions; within each class preserve the workspace/session ordering already supplied by AO.
- Selecting a session enters a file-only search view scoped to that worktree.
- Backspace on an empty input and Escape return one level at a time: file search → session picker → root palette.

### Other scopes and states

- On a page with neither a current session nor a current project, do not offer file search.
- Do not include orchestrator sessions in the project picker for the first version. The feature targets agent worktrees where authored code lives.
- A provisioning or failed session is not searchable. A terminated session may remain searchable while its worktree exists; if it has already been removed, show the existing API error in the palette and let the user choose another session.
- Use a 200 ms debounce, require two characters, request at most 20 results, and keep the palette's existing 20-row overall search cap.
- Path search is case-insensitive and covers tracked plus untracked, non-ignored files. Searching file contents, symbols, or lines is explicitly out of scope and should later use a separate bounded content-search API.

## Existing contracts to preserve

- Local and connected-host search: `GET /api/v1/sessions/{sessionId}/workspace/search`, exposed by `sessionWorkspaceSearchQueryOptions`.
- Cloud search: `CloudCpClient.searchWorkspaceReview`, exposed by `cloudWorkspaceReviewSearchQueryOptions`.
- Local/remote file opening: the `SessionView` path resolution and `SessionFileExplorer` reveal flow.
- Cloud file opening: `SessionView.openCenterFile` and `CloudFileContentPane`.
- Command-K remains `shouldFilter: false`; AO owns ranking, grouping, selection, and the global result cap.
- The request that opens a file is transient renderer state. It must not be persisted and must not become daemon/domain state.

No backend, OpenAPI, database, or generated API changes are expected.

## Task 1: Add file-search command models and deterministic ranking

**Files:**

- Modify: `frontend/src/renderer/lib/command-palette.ts`
- Modify: `frontend/src/renderer/lib/command-palette.test.ts`
- Modify: `frontend/src/renderer/lib/command-palette-icons.ts`

**Step 1: Write failing model tests**

Cover:

- `files` is a valid command group and appears after Current/attention results but before generic Global commands when scores tie;
- a root **Search files…** command is present only when a current session or a current project with a searchable worker session exists;
- provisioning, failed, and orchestrator sessions are excluded from the project picker;
- active picker rows sort ahead of terminated rows;
- file items use a collision-safe id containing the session id and complete path;
- basename prefix/exact matches outrank parent-directory and full-path substring matches;
- the complete path is still searchable;
- adding file results never exceeds `MAX_SEARCH_RESULTS` and does not remove the palette's reserved attention behavior.

Run:

```bash
npm --prefix frontend test -- src/renderer/lib/command-palette.test.ts
```

Expected: FAIL because file groups, actions, and builders do not exist.

**Step 2: Add minimal pure helpers**

Add:

- `files` to `CommandGroupId`, group ordering, and translated group labels;
- `open-file-search` and `open-workspace-file` command actions;
- a small `WorkspaceFileSearchTarget` type carrying `projectId`, `sessionId`, optional `hostId`, and optional Cloud org id;
- `buildFileSearchCommand`, `buildFileSessionCommands`, and `buildWorkspaceFileCommands` pure builders;
- a file-specific score based on basename first and full path second, without weakening the existing score behavior for non-file commands;
- Folder/Search and file-type-aware icons using the existing `WorkspaceEntryIcon` conventions where practical; otherwise use Lucide `File` for the first version.

Do not put fetching, React state, or navigation inside `command-palette.ts`.

**Step 3: Re-run the focused tests**

Expected: PASS.

**Step 4: Commit**

```bash
git add frontend/src/renderer/lib/command-palette.ts frontend/src/renderer/lib/command-palette.test.ts frontend/src/renderer/lib/command-palette-icons.ts
git commit -m "feat: model command palette file search"
```

## Task 2: Create one palette-facing search adapter for local, remote, and Cloud sessions

**Files:**

- Create: `frontend/src/renderer/lib/command-palette-file-search.ts`
- Create: `frontend/src/renderer/lib/command-palette-file-search.test.ts`
- Modify: `frontend/src/renderer/hooks/useSessionWorkspaceFiles.ts`
- Modify: `frontend/src/renderer/hooks/useSessionWorkspaceFiles.test.ts`
- Modify: `frontend/src/renderer/hooks/useCloudWorkspaceReview.ts`
- Modify: `frontend/src/renderer/hooks/useCloudWorkspaceReview.test.tsx`

**Step 1: Write failing adapter tests**

Assert that:

- a local target uses the loopback workspace-search query;
- a connected-host target passes `hostId` and uses that host's client;
- a Cloud target uses `searchWorkspaceReview` with the resolved base URL and org id;
- every transport receives the same trimmed query and limit of 20;
- local and Cloud query keys include session, host/base/org identity, query, and limit, preventing cache collisions;
- the query function forwards TanStack Query's `AbortSignal` where the transport supports cancellation;
- local, remote, and Cloud response shapes normalize to `{ path, status, size, binary }[]` plus `truncated`;
- missing Cloud readiness/org data disables the query rather than falling back to the local daemon.

Run:

```bash
npm --prefix frontend test -- src/renderer/lib/command-palette-file-search.test.ts src/renderer/hooks/useSessionWorkspaceFiles.test.ts src/renderer/hooks/useCloudWorkspaceReview.test.tsx
```

Expected: FAIL because the palette adapter and configurable limits/signals do not exist.

**Step 2: Generalize existing query options without changing Files explorer behavior**

- Let `sessionWorkspaceSearchQueryOptions` accept optional `limit` and `signal` support while retaining its current default limit of 100 for `FileTree`.
- Include the limit in the React Query key.
- Let `cloudWorkspaceReviewSearchQueryOptions` pass the query-function signal to the Cloud client and retain its existing default behavior for current callers.
- Implement a pure/query-options adapter that selects the correct transport from the target kind and returns one normalized result type.

Do not create a new daemon endpoint, direct `fs` bridge, worker request, or client-side repository index.

**Step 3: Re-run focused tests**

Expected: PASS, including the existing Files explorer search tests.

**Step 4: Commit**

```bash
git add frontend/src/renderer/lib/command-palette-file-search.ts frontend/src/renderer/lib/command-palette-file-search.test.ts frontend/src/renderer/hooks/useSessionWorkspaceFiles.ts frontend/src/renderer/hooks/useSessionWorkspaceFiles.test.ts frontend/src/renderer/hooks/useCloudWorkspaceReview.ts frontend/src/renderer/hooks/useCloudWorkspaceReview.test.tsx
git commit -m "feat: unify workspace path search for command palette"
```

## Task 3: Add the transient open-file handoff

**Files:**

- Modify: `frontend/src/renderer/stores/ui-store.ts`
- Modify: `frontend/src/renderer/stores/ui-store.test.ts`
- Modify: `frontend/src/renderer/components/SessionView.tsx`
- Modify: `frontend/src/renderer/components/SessionView.test.tsx`

**Step 1: Write failing store tests**

Specify a one-shot request:

```ts
workspaceFileOpenRequest: {
  sessionId: string;
  hostId?: string;
  path: string;
  nonce: number;
} | null
```

Test that:

- requesting the same file twice increments the nonce;
- local and remote sessions with the same session id remain distinct through `sessionUiKey` semantics;
- clearing succeeds only for the matching nonce, so an older consumer cannot erase a newer request;
- the request is initialized in memory only and is not written to local storage.

**Step 2: Implement the store request and consumer contract**

Add `requestWorkspaceFileOpen` and `clearWorkspaceFileOpenRequest`. Keep the target information minimal; project routing belongs to Command-K, while file presentation belongs to `SessionView`.

**Step 3: Write failing SessionView tests**

Cover:

- a matching request on initial mount is consumed, not skipped;
- a request arriving while the same session is already mounted is consumed;
- a request for another session or host is ignored and left for its destination;
- local/remote paths use the existing `prepareFilesInspector` plus reveal/path-resolution flow;
- Cloud paths use the existing center-file flow and do not call the local workspace-files endpoint;
- after success or a terminal file-open error, the matching nonce is cleared exactly once;
- a newer request that arrives during async path resolution is not cleared by the older request.

Run:

```bash
npm --prefix frontend test -- src/renderer/stores/ui-store.test.ts src/renderer/components/SessionView.test.tsx
```

Expected: FAIL before wiring the request.

**Step 4: Consume the request in SessionView**

- Observe the request after the existing file callbacks are defined.
- Match by the UI session key, not session id alone.
- For local/remote sessions call the same handler used by chat file links so path normalization, All Files selection, inspector reveal, and center-tab opening cannot drift.
- For Cloud sessions call `prepareFilesInspector` and `openCenterFile(path, { mode: "file" })` without attempting a local inventory fetch.
- Clear with compare-and-clear semantics after handling.

**Step 5: Re-run focused tests**

Expected: PASS.

**Step 6: Commit**

```bash
git add frontend/src/renderer/stores/ui-store.ts frontend/src/renderer/stores/ui-store.test.ts frontend/src/renderer/components/SessionView.tsx frontend/src/renderer/components/SessionView.test.tsx
git commit -m "feat: open workspace files from global actions"
```

## Task 4: Build the Command-K session picker and async Files group

**Files:**

- Modify: `frontend/src/renderer/components/CommandPalette.tsx`
- Modify: `frontend/src/renderer/components/CommandPalette.test.tsx`
- Modify: `frontend/src/renderer/i18n/en.json`
- Modify: all other locale JSON files with the English fallback strings used by this repository's current localization policy

**Step 1: Write failing interaction tests**

Test the complete keyboard flow:

1. **Current local session:** open Command-K, type two characters, wait for debounce, see Files results, press Enter, assert the open-file request and unchanged current route.
2. **Current connected-host session:** assert the host-aware search client and host-aware request target.
3. **Current Cloud session:** assert the Cloud search client is used and the local API mock is untouched.
4. **Project page:** choose **Search files…**, see only eligible worker sessions from that project, choose one, type a query, and open a result.
5. **Project page with duplicate branches/paths:** assert no file request occurs before a session is selected and the chosen session id is retained.
6. **Direct file-only mode from a session:** choose **Search files…** and verify non-file command results are absent.
7. **Debounce:** one request for the settled query, no request below two characters, and stale responses cannot replace results for the latest query.
8. **Navigation:** local/Cloud project sessions use `/projects/$projectId/sessions/$sessionId`; remote sessions use `/host/$hostId/project/$projectId/session/$sessionId` or the standalone host route as appropriate.
9. **Keyboard behavior:** Backspace on empty and Escape pop nested views one level; IME composition does not trigger premature navigation or back behavior.
10. **States:** loading text, no matching files, truncated-results notice, recoverable API error, and removed terminated worktree.
11. **Selection stability:** results arriving must not unexpectedly move selection away from an item the user already moved to; if the selected item disappears, select the first enabled visible item.
12. **Caps:** root search keeps the combined 20-result cap; file-only mode renders no more than 20 files.

Use fake timers for the 200 ms debounce and mocked query functions; do not add network calls.

Run:

```bash
npm --prefix frontend test -- src/renderer/components/CommandPalette.test.tsx
```

Expected: FAIL because the views and async results are not wired.

**Step 2: Extend the palette state machine**

Add:

```ts
type PaletteView =
  | { mode: "root" }
  | { mode: "session-actions"; sessionId: string }
  | { mode: "new-task"; projectId: string }
  | { mode: "file-session-picker"; projectId: string }
  | { mode: "file-search"; target: WorkspaceFileSearchTarget };
```

- Include `hostId` in route params read with `strict: false`.
- Resolve the current workspace/session once and derive the search target from that identity.
- Keep the 200 ms debounced value separate from the visible input value. Clear both when changing palette levels or closing.
- Run the normalized query only while the palette is open, the relevant view is visible, the target is valid, and the trimmed debounced query has at least two characters.
- Turn normalized hits into pure command items and merge them before the existing `displayGroups` call for root searches.
- In file-only mode, render only the Files group and its loading/empty/error/truncation state.
- Freeze or merge asynchronous results in a way that preserves the current selected id when it remains visible.

**Step 3: Implement selection and navigation**

When a file result is chosen:

1. call `requestWorkspaceFileOpen` with session/host/path;
2. navigate only when the destination session is not already the active route;
3. use the correct local, standalone, remote-project, or remote-standalone route;
4. close the palette after the request has been published.

The store request must be published before navigation so the destination `SessionView` can consume it on its first mount.

**Step 4: Add copy and accessibility text**

Add translation keys for:

- Files group;
- Search files action;
- Choose a session/worktree;
- Search files in `<session>` placeholder;
- Loading, empty, error, and truncated messages;
- Branch/status subtitles where existing translated status labels cannot be reused.

Ensure the command row's accessible name contains both basename and full relative path, and loading/error notices use appropriate live-region semantics without stealing input focus.

**Step 5: Re-run focused tests**

Expected: PASS.

**Step 6: Commit**

```bash
git add frontend/src/renderer/components/CommandPalette.tsx frontend/src/renderer/components/CommandPalette.test.tsx frontend/src/renderer/i18n/*.json
git commit -m "feat: search workspace files from command palette"
```

## Task 5: Add regression coverage at the existing Files boundaries

**Files:**

- Modify: `frontend/src/renderer/components/FileTree.test.tsx`
- Modify: `frontend/src/renderer/components/SessionFileExplorer.test.tsx`
- Modify: `frontend/src/renderer/lib/workspace-file-events.test.ts` if the query-key change affects invalidation assertions

**Step 1: Add regression tests**

Prove that:

- the Files explorer still requests up to 100 path results, independent of the palette's 20-result limit;
- selecting/revealing a Command-K result switches from Changed Only to All Files;
- the selected path is visible in the tree/preview and opens one center tab rather than duplicates;
- workspace file-change events invalidate both the existing explorer search and palette search keys through their shared prefix;
- an ignored file never appears because all callers still rely on the daemon/Cloud contract rather than renderer filesystem traversal.

Run:

```bash
npm --prefix frontend test -- src/renderer/components/FileTree.test.tsx src/renderer/components/SessionFileExplorer.test.tsx src/renderer/lib/workspace-file-events.test.ts
```

Expected: PASS after any necessary narrow test updates. If production changes beyond query-key compatibility are needed here, keep them surgical and add a failing test first.

**Step 2: Commit**

```bash
git add frontend/src/renderer/components/FileTree.test.tsx frontend/src/renderer/components/SessionFileExplorer.test.tsx frontend/src/renderer/lib/workspace-file-events.test.ts
git commit -m "test: cover command palette file reveal regressions"
```

## Task 6: Verify the real desktop interaction

Follow `.agents/skills/ao-desktop-dev/SKILL.md` and the repository's isolated desktop-lab instructions. Do not run the user's production checkout or use their real `~/.ao` data.

Create throwaway data containing:

- one project with two worker sessions on different branches;
- the same relative filename with different contents in both worktrees;
- a newly created untracked file;
- enough similarly named files to exercise ranking;
- if credentials/test infrastructure permit, one connected-host or Cloud session. Otherwise leave those transport paths covered by automated tests and report the visual verification gap explicitly.

Verify visually and by interaction:

- Command-K opens and focuses immediately before deferred search work begins;
- a current-session query produces a Files group after the debounce;
- basename and directory typography are readable and truncation preserves the meaningful filename;
- the project-page picker makes branch/worktree identity obvious;
- selecting the duplicate path opens the version from the chosen session;
- the inspector switches to Files/All Files and the center tab opens once;
- keyboard-only navigation, Backspace, Escape, Enter, and IME composition remain correct;
- loading, no-results, error, and truncated states do not resize or flash the palette unexpectedly;
- no renderer console errors or failed requests remain after closing/reopening the palette.

Capture screenshots or a short recording under `docs/screenshots/` only if the issue/PR requires durable visual evidence; otherwise report the manual cases and results in the PR body.

## Task 7: Run complete verification and prepare the PR

**Step 1: Run the full frontend suite**

```bash
npm --prefix frontend test
npm run frontend:typecheck
cd frontend && npm run build
```

Expected: PASS.

**Step 2: Run repository CI-equivalent validation**

```bash
npm run lint
npx @redwoodjs/agent-ci run --all
```

Expected: PASS. The workflow validator requires Docker; if Docker or a native runner is unavailable, record the exact unrun job and verify it in remote CI.

**Step 3: Review generated/API drift**

Confirm there are no changes to:

- `backend/internal/httpd/apispec/openapi.yaml`;
- `frontend/src/api/schema.ts`;
- generated sqlc files.

Any change in those files means the implementation accidentally expanded beyond the existing contracts and should be investigated before proceeding.

**Step 4: Prepare the PR**

- Follow `.agents/skills/pr-description/SKILL.md` for the required change-count header.
- Summarize the two UX paths, transport reuse, and transient file-open handoff.
- List focused tests, full suites, typecheck/build, real-desktop verification, and any environment-specific gaps.
- State explicitly that this searches paths only; content/symbol search remains a separate future feature.
- Attach visual evidence if captured.

## Acceptance criteria

- Command-K can find tracked and untracked, non-ignored files in the current session worktree.
- A project page never silently combines file results from multiple session worktrees.
- The user can choose a worker session, search that worktree, and open a result without leaving Command-K prematurely.
- Duplicate relative paths on different branches open the chosen session's version.
- Local, connected-host, and Cloud sessions use their existing search transports.
- Selecting a result reuses the existing Files inspector and center-file tabs.
- Search begins only after two characters and a 200 ms debounce, is cancellable where supported, and is capped at 20 results.
- Keyboard navigation, selection stability, Backspace/Escape behavior, IME input, accessibility announcements, and palette result caps remain correct.
- No new filesystem bridge, repository index, backend route, database state, or generated API change is introduced.
- Full frontend tests, typecheck, build, repository lint, and available workflow validation pass.

## Deferred follow-up: code-content search

Do not fold content search into this implementation. A later design should define a separate bounded API with:

- literal versus regex semantics;
- ignore/binary/size rules;
- repository and nested-workspace confinement;
- timeout and output-byte caps;
- path, line, column, and safe snippet responses;
- pagination/cancellation and Cloud worker parity;
- explicit UI affordance distinguishing **Files** from **Code contents**.
