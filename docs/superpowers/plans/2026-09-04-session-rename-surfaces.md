# Session Rename Surfaces Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let users rename a worker session inline from either its sidebar row or its owning terminal tab by double-clicking or choosing Rename from a context menu.

**Architecture:** Keep the existing daemon `PATCH /api/v1/sessions/{sessionId}` contract unchanged. Extract the existing renderer rename state/persistence into one hook used by both surfaces, while each component retains layout-specific input styling and interaction guards.

**Tech Stack:** React 19, TypeScript, TanStack Query, Radix context menus, Testing Library, Vitest, Electron Forge.

---

### Task 1: Shared session rename behavior

**Files:**
- Create: `frontend/src/renderer/hooks/useSessionRename.ts`
- Modify: `frontend/src/renderer/components/Sidebar.tsx:1700-1905`
- Test: `frontend/src/renderer/components/Sidebar.test.tsx:700-750`

- [ ] **Step 1: Extend the sidebar regression tests for the complete interaction**

Add tests that double-click the full session button (not only the text node), right-click the row and choose `Rename fix login`, then verify Enter saves a trimmed title, Escape cancels, and neither empty nor unchanged input calls `renameSessionMock`.

```tsx
it("starts inline rename from the full row double-click target", async () => {
	renderSidebar({ workspaces: [{ ...workspace, sessions: [session] }] });
	fireEvent.doubleClick(screen.getByRole("button", { name: "Open fix login" }));
	expect(screen.getByRole("textbox", { name: "Rename fix login" })).toHaveValue("fix login");
});

it("starts the same inline rename from the session context menu", async () => {
	const user = userEvent.setup();
	renderSidebar({ workspaces: [{ ...workspace, sessions: [session] }] });
	await user.pointer({ keys: "[MouseRight]", target: screen.getByRole("button", { name: "Open fix login" }) });
	await user.click(screen.getByRole("menuitem", { name: "Rename fix login" }));
	expect(screen.getByRole("textbox", { name: "Rename fix login" })).toHaveFocus();
});
```

- [ ] **Step 2: Run the sidebar tests and observe the new failures**

Run: `cd frontend && npm test -- --run src/renderer/components/Sidebar.test.tsx`

Expected: the full-button double-click test opens the session instead of entering edit mode, and no session Rename menu item is found.

- [ ] **Step 3: Extract the shared state and persistence hook**

Create a focused hook that owns edit state, draft normalization, cancellation, API persistence, and workspace-query invalidation.

```ts
import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useRef, useState } from "react";
import { workspaceQueryKey } from "./useWorkspaceQuery";
import { renameSession } from "../lib/rename-session";
import type { WorkspaceSession } from "../types/workspace";

export const MAX_SESSION_DISPLAY_NAME_LEN = 20;

export function useSessionRename(session: Pick<WorkspaceSession, "id" | "title">) {
	const queryClient = useQueryClient();
	const [isEditing, setIsEditing] = useState(false);
	const [draft, setDraft] = useState(session.title);
	const cancelledRef = useRef(false);

	const begin = useCallback(() => {
		cancelledRef.current = false;
		setDraft(session.title);
		setIsEditing(true);
	}, [session.title]);

	const cancel = useCallback(() => {
		cancelledRef.current = true;
		setDraft(session.title);
		setIsEditing(false);
	}, [session.title]);

	const commit = useCallback(async () => {
		if (cancelledRef.current) {
			cancelledRef.current = false;
			setIsEditing(false);
			return;
		}
		setIsEditing(false);
		const name = draft.trim();
		if (!name || name === session.title) return;
		try {
			await renameSession(session.id, name);
			await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
		} catch (error) {
			console.error("Failed to rename session:", error);
		}
	}, [draft, queryClient, session.id, session.title]);

	return { begin, cancel, commit, draft, isEditing, setDraft };
}
```

- [ ] **Step 4: Rewire the sidebar row and expose the two entry points**

Replace the component-local rename state with `useSessionRename(session)`, move `onDoubleClick` to the full session button, and wrap the non-editing row in the existing Radix menu primitives.

```tsx
const rename = useSessionRename(session);

<ContextMenu>
	<ContextMenuTrigger asChild>{row}</ContextMenuTrigger>
	<ContextMenuContent className="min-w-44">
		<ContextMenuItem onSelect={rename.begin}>
			{t("shell.renameSession", { title: session.title })}
		</ContextMenuItem>
	</ContextMenuContent>
</ContextMenu>
```

The button handler must use `onDoubleClick={(event) => { event.preventDefault(); event.stopPropagation(); rename.begin(); }}` and the editor must call `rename.commit()` on blur/Enter and `rename.cancel()` on Escape. Keep `maxLength={MAX_SESSION_DISPLAY_NAME_LEN}` and stop context-menu/pointer events from triggering navigation or drag.

- [ ] **Step 5: Run sidebar tests until the behavior passes**

Run: `cd frontend && npm test -- --run src/renderer/components/Sidebar.test.tsx`

Expected: all Sidebar tests pass, including both rename entry points and save/cancel/no-op cases.

- [ ] **Step 6: Commit the sidebar/shared behavior**

```bash
git add frontend/src/renderer/hooks/useSessionRename.ts frontend/src/renderer/components/Sidebar.tsx frontend/src/renderer/components/Sidebar.test.tsx
git commit -m "feat(ui): expose session rename in sidebar"
```

### Task 2: Owning terminal-tab rename behavior

**Files:**
- Modify: `frontend/src/renderer/components/CenterPane.tsx:600-660,907-965`
- Modify: `frontend/src/renderer/components/CenterPane.test.tsx:1-180,1010-1140`

- [ ] **Step 1: Add failing terminal-tab interaction tests**

Mock `renameSession`, provide a fresh `QueryClient` through `QueryClientProvider` in `renderCenterPane`, and add tests for double-click, context-menu selection, F2, Enter, Escape, and no navigation while editing.

```tsx
it("renames the owning session from a double-click on its terminal tab", async () => {
	const user = userEvent.setup();
	renderCenterPane({ session: worker });
	await user.dblClick(screen.getByRole("tab", { name: /^do the thing/ }));
	const input = screen.getByRole("textbox", { name: "Rename do the thing" });
	await user.clear(input);
	await user.type(input, "  clearer task  {Enter}");
	await waitFor(() => expect(renameSessionMock).toHaveBeenCalledWith("sess-1", "clearer task"));
});

it("opens terminal-tab rename from its context menu", async () => {
	const user = userEvent.setup();
	renderCenterPane({ session: worker });
	await user.pointer({ keys: "[MouseRight]", target: screen.getByRole("tab", { name: /^do the thing/ }) });
	await user.click(screen.getByRole("menuitem", { name: "Rename do the thing" }));
	expect(screen.getByRole("textbox", { name: "Rename do the thing" })).toHaveFocus();
});
```

- [ ] **Step 2: Run the CenterPane tests and verify the tests fail for missing rename UI**

Run: `cd frontend && npm test -- --run src/renderer/components/CenterPane.test.tsx`

Expected: no rename textbox or context-menu item exists for the owning session tab.

- [ ] **Step 3: Add rename mode to `SessionPaneTab` without changing tab chrome**

When `session` is present, call `useSessionRename(session)`. Pass a layout-matched input through `TerminalTabFrame.editingContent`, using the same agent avatar and existing tab dimensions.

```tsx
const editingContent = rename.isEditing ? (
	<div className="flex h-full min-w-0 flex-1 items-center gap-2 px-2">
		{tabIcon}
		<input
			aria-label={t("shell.renameSession", { title: session.title })}
			autoFocus
			className="min-w-0 flex-1 rounded-xs border border-accent bg-background px-1 text-control text-foreground outline-none ring-1 ring-accent"
			maxLength={MAX_SESSION_DISPLAY_NAME_LEN}
			onBlur={() => void rename.commit()}
			onChange={(event) => rename.setDraft(event.target.value)}
			onFocus={(event) => event.currentTarget.select()}
			onKeyDown={handleRenameKeyDown}
			value={rename.draft}
		/>
	</div>
) : undefined;
```

The normal tab button starts editing on double-click and F2, while retaining its current click selection and roving-tab behavior. Events from the input must not select, reorder, or reach the terminal.

- [ ] **Step 4: Add the existing context-menu treatment around the owning tab**

Wrap `TerminalTabFrame` in `ContextMenu` only when `session` exists and add one `ContextMenuItem` using the existing translated `shell.renameSession` label. Selecting it calls the same `rename.begin()` function as double-click and F2.

```tsx
<ContextMenu>
	<ContextMenuTrigger asChild>{tabFrame}</ContextMenuTrigger>
	<ContextMenuContent className="min-w-44">
		<ContextMenuItem onSelect={rename.begin}>
			{t("shell.renameSession", { title: session.title })}
		</ContextMenuItem>
	</ContextMenuContent>
</ContextMenu>
```

- [ ] **Step 5: Run the terminal-tab and sidebar regression suites**

Run: `cd frontend && npm test -- --run src/renderer/components/CenterPane.test.tsx src/renderer/components/Sidebar.test.tsx`

Expected: both component suites pass with no test warnings or unhandled promise rejections.

- [ ] **Step 6: Commit the terminal-tab behavior**

```bash
git add frontend/src/renderer/components/CenterPane.tsx frontend/src/renderer/components/CenterPane.test.tsx
git commit -m "feat(ui): rename sessions from terminal tabs"
```

### Task 3: Verify, publish the review branch, and launch locally

**Files:**
- Verify only; no generated API files should change.

- [ ] **Step 1: Run formatter/diff checks**

Run: `git diff --check origin/main...HEAD`

Expected: exit 0 with no whitespace errors.

- [ ] **Step 2: Run frontend typecheck**

Run: `npm run frontend:typecheck`

Expected: exit 0 with no TypeScript diagnostics.

- [ ] **Step 3: Run the production frontend build**

Run: `cd frontend && npm run build`

Expected: exit 0 with renderer, preload, and Electron main bundles produced successfully.

- [ ] **Step 4: Push the feature branch and open one PR against `main`**

```bash
git push -u fork codex/session-rename-surfaces
gh pr create --repo AgentWrapper/agent-orchestrator \
  --base main \
  --head Pulkit7070:codex/session-rename-surfaces \
  --title "feat(ui): rename sessions from navigation surfaces" \
  --body "## Summary
- let users rename sessions by double-clicking the sidebar row or owning terminal tab
- add Rename to both context menus while preserving the existing inline visual treatment
- share persistence through the existing daemon rename endpoint and workspace-query refresh

## Verification
- cd frontend && npm test -- --run src/renderer/components/CenterPane.test.tsx src/renderer/components/Sidebar.test.tsx
- npm run frontend:typecheck
- cd frontend && npm run build

The daemon and generated API contract are unchanged."
```

The PR body must summarize both interaction surfaces, list the exact tests run, and state that the daemon/API contract is unchanged.

- [ ] **Step 5: Launch the real Electron app from this checkout**

Follow `.agents/skills/ao-desktop-dev/SKILL.md` to select the correct isolated/real-data launch mode, start the repository's Electron development app, and leave it running so the user can rename a real session from both surfaces.

- [ ] **Step 6: Report the PR number and local app state**

Provide the PR link/number, branch name, verification commands with outcomes, and the exact local data mode used for the running app.
