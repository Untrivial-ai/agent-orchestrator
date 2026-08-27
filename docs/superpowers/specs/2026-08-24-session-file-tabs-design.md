# Session File Tabs Design

## Goal

Turn the Files inspector into an editor-style navigator: selecting files in the right rail opens language-aware tabs in the center workspace while preserving the live terminal as a permanent destination. Add a compact, read-only branch indicator to the session chrome.

## Scope

- Open files from the docked Files inspector in the center workspace.
- Keep multiple file tabs open per session.
- Show the existing language/file icons in the tree and center tabs.
- Preserve the terminal or chat agent surface and make it directly selectable.
- Close one file or all files, returning to the terminal when no file remains active.
- Show the session's current branch as read-only context.
- Preserve the existing maximized Files explorer for focused tree/content review.

Branch switching, file editing, file creation, and file persistence across application restarts are outside this change.

## Interaction Model

The center workspace has one permanent agent tab and zero or more file tabs. The permanent tab is labelled from the active agent surface (for example, Terminal or Claude) and cannot be closed.

Selecting a file in the docked tree opens it once and activates its center tab. Selecting an already-open file only activates it. Each file tab shows the resolved programming-language icon, the basename, and a close button. The active file surface shows its relative path and the existing read-only or diff content.

Closing the active file selects the nearest remaining file. Closing the final file, or choosing **Close all files** from the tab overflow menu, activates the permanent agent tab. Selecting the permanent agent tab returns to the live terminal/chat without closing file tabs.

Open files and the active center tab are session-scoped. Navigating between sessions must not show another session's file tabs. State only needs to survive component navigation during the current application run.

## Architecture

`SessionView` owns the session-scoped center-workspace state because it already coordinates the agent surface, terminal targets, inspector, and full-window overlays. A small dedicated tab-strip component renders the permanent agent tab, file tabs, close controls, and close-all menu. A center file surface reuses `FileContentPane`, keeping daemon file loading, diff rendering, syntax highlighting, and annotations behind the existing boundary.

`SessionFileExplorer` accepts a file-open callback. In docked mode, selection calls that callback and leaves the tree visible rather than switching to its current narrow master/detail content view. Maximized mode keeps its local side-by-side tree/content behavior and does not populate center tabs.

The current filename-to-icon behavior moves behind a reusable component so `FileTree` and the center tab strip resolve icons identically through `@react-symbols/icons`.

## Branch Context

The session branch comes from the existing `WorkspaceSession.branch` field. Render it as a compact read-only branch pill in the upper-right session chrome, with a branch icon and a tooltip containing the full branch. It performs no checkout, fetch, or mutation and is omitted when the branch is unavailable.

## State Rules

- File paths are the stable tab identity within a session.
- Opening is idempotent and preserves existing tab order.
- Closing a tab removes only that path.
- Closing the active tab activates the next tab, then the previous tab, then the agent surface.
- Changing sessions restores that session's in-memory tab state or defaults to the agent surface.
- The terminal/chat component remains mounted while a file is active so its runtime connection, scrollback, drafts, and process state survive tab switches.
- Existing maximized Files and Browser overlays retain priority over center-workspace content.

## Error and Empty States

File loading continues to use `FileContentPane` retry and error behavior. If a previously open path disappears, its tab remains closeable and its content surface reports the daemon error. Missing branch data renders no branch pill. Closing all file tabs never terminates or resets the agent surface.

## Testing

Focused component tests will cover opening and deduplicating file tabs, file-icon rendering, switching between file and agent tabs, individual close selection, close-all fallback, session isolation, docked-tree callback behavior, maximized-view preservation, and read-only branch rendering. After focused tests and typecheck pass, the actual Electron app will be restarted from this worktree for manual inspection of narrow and maximized layouts.
