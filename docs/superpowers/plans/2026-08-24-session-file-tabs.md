# Session File Tabs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Open files from the docked Files rail as language-aware, closeable tabs in the center workspace while preserving the live agent surface and showing the current branch read-only.

**Architecture:** `SessionView` coordinates session-scoped open-file state and keeps the existing terminal/chat tree mounted underneath the selected center surface. Focused components render shared file icons, the center tab strip, and the active file pane; `SessionFileExplorer` emits docked file-open events while retaining its independent maximized layout.

**Tech Stack:** React 19, TypeScript, TanStack Query, Zustand, Vitest/Testing Library, Tailwind CSS, `@react-symbols/icons`.

**Spec:** `docs/superpowers/specs/2026-08-24-session-file-tabs-design.md`

## Global Constraints

- The terminal/chat surface must remain mounted while files are viewed.
- File tabs are session-scoped and in-memory only.
- The agent tab is permanent and cannot be closed.
- Branch display is read-only and omitted when unavailable.
- Maximized Files retains its existing side-by-side explorer behavior.
- No daemon or API contract changes are required.

---

### Task 1: Shared language-aware entry icon

**Files:**
- Create: `frontend/src/renderer/components/WorkspaceEntryIcon.tsx`
- Create: `frontend/src/renderer/components/WorkspaceEntryIcon.test.tsx`
- Modify: `frontend/src/renderer/components/FileTree.tsx`
- Modify: `frontend/src/renderer/components/FileTree.test.tsx`

**Interfaces:**
- Produces: `WorkspaceEntryIcon({ kind, name, className, testId })`, where `kind` is `"file" | "dir"`.
- Consumes: `FileIcon` and `FolderIcon` from `@react-symbols/icons/utils`.

- [ ] **Step 1: Write the failing shared-icon test**

```tsx
render(
  <>
    <WorkspaceEntryIcon kind="file" name="App.tsx" testId="tsx" />
    <WorkspaceEntryIcon kind="file" name="README.md" testId="markdown" />
    <WorkspaceEntryIcon kind="dir" name="src" testId="src" />
  </>,
);
expect(screen.getByTestId("tsx").innerHTML).not.toBe(screen.getByTestId("markdown").innerHTML);
expect(screen.getByTestId("src").tagName).toBe("svg");
```

- [ ] **Step 2: Run the focused test and confirm the component is missing**

Run: `cd frontend && npm test -- WorkspaceEntryIcon.test.tsx`

Expected: FAIL because `WorkspaceEntryIcon` does not exist.

- [ ] **Step 3: Implement the shared icon component**

```tsx
export function WorkspaceEntryIcon({ kind, name, className, testId }: WorkspaceEntryIconProps) {
  const props = { "aria-hidden": true, className, "data-testid": testId } as const;
  return kind === "dir" ? (
    <FolderIcon folderName={name.toLowerCase()} {...props} />
  ) : (
    <FileIcon autoAssign fileName={name} {...props} />
  );
}
```

- [ ] **Step 4: Replace direct package usage in `FileTree`**

Render `WorkspaceEntryIcon` with `kind={isDir ? "dir" : "file"}`, `name={entry.name}`, and the existing `size-icon-md shrink-0` styling. Keep chevrons, status badges, row selection, and lazy loading unchanged.

- [ ] **Step 5: Run icon and tree tests**

Run: `cd frontend && npm test -- WorkspaceEntryIcon.test.tsx FileTree.test.tsx`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/renderer/components/WorkspaceEntryIcon.tsx frontend/src/renderer/components/WorkspaceEntryIcon.test.tsx frontend/src/renderer/components/FileTree.tsx frontend/src/renderer/components/FileTree.test.tsx
git commit -m "refactor(files): share workspace entry icons"
```

### Task 2: Center workspace tab strip and tab-state reducer

**Files:**
- Create: `frontend/src/renderer/components/SessionFileTabs.tsx`
- Create: `frontend/src/renderer/components/SessionFileTabs.test.tsx`
- Create: `frontend/src/renderer/lib/session-file-tabs.ts`
- Create: `frontend/src/renderer/lib/session-file-tabs.test.ts`

**Interfaces:**
- Produces: `SessionFileTabState = { openPaths: string[]; activePath: string | null }`.
- Produces: `openSessionFile`, `activateSessionFile`, `closeSessionFile`, and `closeAllSessionFiles` pure transitions.
- Produces: `SessionFileTabs({ agentLabel, state, onActivateAgent, onActivateFile, onCloseFile, onCloseAll })`.
- Consumes: `WorkspaceEntryIcon` from Task 1.

- [ ] **Step 1: Write failing reducer tests**

```ts
expect(openSessionFile({ openPaths: [], activePath: null }, "src/App.tsx")).toEqual({
  openPaths: ["src/App.tsx"],
  activePath: "src/App.tsx",
});
expect(openSessionFile({ openPaths: ["src/App.tsx"], activePath: null }, "src/App.tsx")).toEqual({
  openPaths: ["src/App.tsx"],
  activePath: "src/App.tsx",
});
expect(closeSessionFile({ openPaths: ["a.ts", "b.ts"], activePath: "a.ts" }, "a.ts")).toEqual({
  openPaths: ["b.ts"],
  activePath: "b.ts",
});
expect(closeAllSessionFiles()).toEqual({ openPaths: [], activePath: null });
```

- [ ] **Step 2: Run reducer tests and confirm missing exports**

Run: `cd frontend && npm test -- session-file-tabs.test.ts`

Expected: FAIL because the transition module does not exist.

- [ ] **Step 3: Implement pure, idempotent tab transitions**

Implement literal path identity, stable insertion order, next-then-previous close fallback, and `activePath: null` for the agent surface.

- [ ] **Step 4: Write failing tab-strip behavior tests**

```tsx
render(<SessionFileTabs agentLabel="Claude" state={{ openPaths: ["src/App.tsx"], activePath: "src/App.tsx" }} {...handlers} />);
expect(screen.getByRole("tab", { name: "Claude" })).toBeInTheDocument();
expect(screen.getByRole("tab", { name: "App.tsx" })).toHaveAttribute("aria-selected", "true");
await user.click(screen.getByRole("button", { name: "Close App.tsx" }));
expect(handlers.onCloseFile).toHaveBeenCalledWith("src/App.tsx");
await user.click(screen.getByRole("menuitem", { name: "Close all files" }));
expect(handlers.onCloseAll).toHaveBeenCalled();
```

- [ ] **Step 5: Implement the accessible tab strip**

Use `role="tablist"`, permanent agent tab, basename labels with full-path titles, `WorkspaceEntryIcon` per file, stop-propagation close buttons, horizontal overflow, and an overflow dropdown containing **Close all files**. Reuse existing `Button` and dropdown primitives.

- [ ] **Step 6: Run tab tests**

Run: `cd frontend && npm test -- session-file-tabs.test.ts SessionFileTabs.test.tsx`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/renderer/components/SessionFileTabs.tsx frontend/src/renderer/components/SessionFileTabs.test.tsx frontend/src/renderer/lib/session-file-tabs.ts frontend/src/renderer/lib/session-file-tabs.test.ts
git commit -m "feat(files): add center workspace file tabs"
```

### Task 3: Docked Files rail opens center tabs

**Files:**
- Modify: `frontend/src/renderer/components/SessionFileExplorer.tsx`
- Modify: `frontend/src/renderer/components/SessionFileExplorer.test.tsx`

**Interfaces:**
- Produces: optional `onOpenFile(path: string)` prop on `SessionFileExplorer`.
- Consumes: center file-open callback supplied later by `SessionView`.

- [ ] **Step 1: Write failing docked and maximized behavior tests**

```tsx
const onOpenFile = vi.fn();
renderExplorer(<SessionFileExplorer sessionId="sess-1" onOpenFile={onOpenFile} />);
await user.click(screen.getByRole("button", { name: "select src/App.tsx" }));
expect(onOpenFile).toHaveBeenCalledWith("src/App.tsx");
expect(screen.getByTestId("tree-changed-only")).toBeVisible();

renderExplorer(<SessionFileExplorer isMaximized sessionId="sess-1" onOpenFile={onOpenFile} />);
await user.click(screen.getByRole("button", { name: "select src/App.tsx" }));
expect(screen.getByTestId("content-pane")).toHaveTextContent("src/App.tsx");
```

- [ ] **Step 2: Run the explorer test and confirm docked mode still enters local detail**

Run: `cd frontend && npm test -- SessionFileExplorer.test.tsx`

Expected: FAIL because docked selection does not call `onOpenFile` and hides the tree.

- [ ] **Step 3: Route docked selection outward and preserve maximized selection locally**

Add one handler:

```ts
const handleSelectPath = (node: TreeNode) => {
  if (!isMaximized && onOpenFile) {
    onOpenFile(node.path);
    return;
  }
  setSelectedPath(node.path);
};
```

Use it for both tree instances. Remove the docked master/detail branch when `onOpenFile` is provided, while retaining the existing fallback when the explorer is rendered without a coordinator.

- [ ] **Step 4: Run explorer tests**

Run: `cd frontend && npm test -- SessionFileExplorer.test.tsx`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/renderer/components/SessionFileExplorer.tsx frontend/src/renderer/components/SessionFileExplorer.test.tsx
git commit -m "feat(files): open docked files in center workspace"
```

### Task 4: Integrate session-scoped center files without unmounting the agent

**Files:**
- Create: `frontend/src/renderer/components/SessionFileWorkspace.tsx`
- Create: `frontend/src/renderer/components/SessionFileWorkspace.test.tsx`
- Modify: `frontend/src/renderer/components/SessionView.tsx`
- Modify: `frontend/src/renderer/components/SessionView.test.tsx`

**Interfaces:**
- Produces: `SessionFileWorkspace({ sessionId, path })`, reusing `FileContentPane` and its annotation model.
- Consumes: Task 2 state transitions and tab strip; Task 3 `onOpenFile` callback.

- [ ] **Step 1: Write failing `SessionView` integration tests**

Extend the existing `SessionFileExplorer` mock to expose `onOpenFile`. Assert:

```tsx
await user.click(screen.getByRole("button", { name: "open src/App.tsx" }));
expect(screen.getByRole("tab", { name: "App.tsx" })).toHaveAttribute("aria-selected", "true");
expect(screen.getByTestId("session-file-workspace")).toHaveTextContent("src/App.tsx");
expect(screen.getByTestId("terminal-interaction-surface")).toBeInTheDocument();

await user.click(screen.getByRole("tab", { name: /Claude|Terminal/ }));
expect(screen.getByTestId("terminal-interaction-surface")).toBeVisible();
```

Add a rerender/navigation case proving tabs from `sess-1` do not appear in `sess-2`.

- [ ] **Step 2: Run the integration test and confirm center tabs are absent**

Run: `cd frontend && npm test -- SessionView.test.tsx`

Expected: FAIL on the missing center file workspace and tabs.

- [ ] **Step 3: Implement `SessionFileWorkspace`**

Render a center header with `WorkspaceEntryIcon`, full relative path, and the existing `FileContentPane` inside a full-height scroll surface. Own annotation state here using the same send/cancel behavior already used by `SessionFileExplorer`; extract a shared annotation hook only if both real call sites otherwise duplicate the complete behavior.

- [ ] **Step 4: Add per-session tab state to `SessionView`**

Store `Record<string, SessionFileTabState>` in `SessionView` state. Wire open, activate agent, activate file, close file, and close-all callbacks through Task 2 pure transitions. Reset only overlay state on route changes; do not erase another session's tab entry.

- [ ] **Step 5: Layer center file content over the mounted agent surface**

Wrap the existing chat/terminal surface in a stable full-height container. Keep it mounted and toggle visibility/inertness based on `activePath`. Mount `SessionFileWorkspace` as the visible sibling when a file is active. Render `SessionFileTabs` above both surfaces and pass the docked explorer `onOpenFile`.

- [ ] **Step 6: Run center integration tests**

Run: `cd frontend && npm test -- SessionFileWorkspace.test.tsx SessionView.test.tsx`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/renderer/components/SessionFileWorkspace.tsx frontend/src/renderer/components/SessionFileWorkspace.test.tsx frontend/src/renderer/components/SessionView.tsx frontend/src/renderer/components/SessionView.test.tsx
git commit -m "feat(files): open files beside the live agent surface"
```

### Task 5: Read-only branch context

**Files:**
- Create: `frontend/src/renderer/components/SessionBranchBadge.tsx`
- Create: `frontend/src/renderer/components/SessionBranchBadge.test.tsx`
- Modify: `frontend/src/renderer/components/SessionView.tsx`
- Modify: `frontend/src/renderer/components/SessionView.test.tsx`

**Interfaces:**
- Produces: `SessionBranchBadge({ branch?: string })`.
- Consumes: existing `WorkspaceSession.branch`.

- [ ] **Step 1: Write failing badge tests**

```tsx
render(<SessionBranchBadge branch="feat/session-file-tabs" />);
expect(screen.getByText("feat/session-file-tabs")).toBeInTheDocument();
expect(screen.queryByRole("button")).not.toBeInTheDocument();

const { container } = render(<SessionBranchBadge />);
expect(container).toBeEmptyDOMElement();
```

- [ ] **Step 2: Run the badge test and confirm the component is missing**

Run: `cd frontend && npm test -- SessionBranchBadge.test.tsx`

Expected: FAIL because the badge does not exist.

- [ ] **Step 3: Implement and integrate the badge**

Render `GitBranch` plus a truncated branch label in a non-interactive element with `title={branch}`. Place it before `ShellTopbar` within `sessionHeaderActions` so both TUI and chat surfaces receive the same branch context without duplicating their topbars.

- [ ] **Step 4: Run branch and session tests**

Run: `cd frontend && npm test -- SessionBranchBadge.test.tsx SessionView.test.tsx CenterPane.test.tsx`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/renderer/components/SessionBranchBadge.tsx frontend/src/renderer/components/SessionBranchBadge.test.tsx frontend/src/renderer/components/SessionView.tsx frontend/src/renderer/components/SessionView.test.tsx
git commit -m "feat(sessions): show current branch in session chrome"
```

### Task 6: Verification and native Electron review

**Files:**
- Modify only files required by issues found during verification.

**Interfaces:**
- Consumes all prior tasks.

- [ ] **Step 1: Run focused feature tests together**

Run:

```bash
cd frontend
npm test -- WorkspaceEntryIcon.test.tsx FileTree.test.tsx SessionFileTabs.test.tsx session-file-tabs.test.ts SessionFileExplorer.test.tsx SessionFileWorkspace.test.tsx SessionBranchBadge.test.tsx SessionView.test.tsx
```

Expected: PASS with zero failures.

- [ ] **Step 2: Run frontend static verification**

Run: `cd frontend && npm run typecheck`

Expected: exit 0.

- [ ] **Step 3: Restart the real Electron checkout**

Use the `ao-desktop-dev` workflow. Because this task changes renderer code only, hot reload may apply, but restart the existing checkout-scoped Forge process if dependency optimization or stale renderer state obscures the result. Confirm renderer `http://localhost:5173`, daemon `127.0.0.1:3002`, and successful session/file API traffic.

- [ ] **Step 4: Exercise the native flow**

In the Electron window: open Files in the right rail; select two files with different extensions; verify distinct icons in tree and tabs; switch to the permanent agent tab without closing files; return to a file; close the active file; use Close all; confirm the live terminal returns with its prior state; verify the branch badge; maximize Files and confirm its independent side-by-side layout.

- [ ] **Step 5: Inspect the final diff and commit verification fixes**

Run: `git diff --check && git status --short --branch`.

If verification required changes, commit them with a narrowly scoped conventional message. Do not commit local run state or generated development artifacts.
