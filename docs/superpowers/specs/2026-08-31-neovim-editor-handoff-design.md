# Neovim Editor Handoff Design

## Goal

Add Neovim to AO's desktop "Open in Editor" picker and make opening a session workspace work on macOS, Linux, and Windows.

## Scope

This change adds Neovim only. The existing catalog already covers the mainstream GUI editors and IDE families used by AO. Vim, Emacs, and Helix are also absent, but adding them would expand this issue from one requested editor into a broader terminal-editor product decision. The implementation will make future terminal editors straightforward without exposing unrequested targets now.

## Approaches considered

1. Launch `nvim <workspace>` through the existing detached GUI launcher. This is the smallest diff, but it gives Neovim no interactive terminal because the child process has ignored stdio.
2. Add a terminal-aware editor launch path inside the existing Electron handoff service. This preserves the current IPC and preference contracts while launching Neovim in a real terminal on every platform. This is the selected approach.
3. Add Neovim, Vim, Emacs, and Helix through a generalized editor plugin registry. That creates more product surface and abstraction than this issue needs.

## Architecture

`frontend/src/shared/editor-handoff.ts` remains the authoritative editor ID union and gains `neovim`. `frontend/src/main/editor-handoff.ts` remains responsible for local detection and launch resolution. Editor candidates can declare that they require a terminal; only Neovim does so in this change.

Resolved launchers will build their complete argument list from a workspace path. GUI editors continue to receive the workspace directly. Neovim resolves the `nvim` executable first, then wraps it with:

- macOS: Apple Terminal via `osascript`, using shell and AppleScript escaping.
- Linux: the first supported installed terminal, using its argv-based execute form.
- Windows: a detached Command Prompt kept open with `/k`, using quoted command arguments.

Path parsing and joining use `path.win32` for Windows and `path.posix` for macOS/Linux. This makes platform simulations faithful on any CI host and preserves native behavior.

## Error handling

Neovim appears only when both `nvim` and a usable terminal launcher are available. Launch failures keep the existing path-free user message and logging behavior. Choosing a missing preferred Neovim installation continues to produce the existing "not installed" error rather than silently selecting another editor.

## Testing

Vitest tests will cover detection and launch arguments for macOS, Linux, and Windows, including workspace paths with spaces and shell metacharacters. Existing GUI-editor, file-manager, preference, workspace-unavailable, and launcher-failure tests remain unchanged. Verification will include the focused suite, frontend typecheck, frontend build, and the full frontend test suite. Native packaged builds remain covered by AO's existing macOS/Windows/Linux build matrices.

## Local demo

Run the Electron development app from the isolated worktree. If `nvim` is not installed on the host, report that clearly; AO intentionally hides unavailable editors.
