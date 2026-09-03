# Neovim Editor Handoff Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a detected Neovim target that opens session workspaces in an interactive terminal on macOS, Linux, and Windows.

**Architecture:** Keep editor discovery and launching in the Electron main-process handoff service. Extend resolved launch commands so they build platform-specific arguments from the workspace path, and mark Neovim as a terminal-required editor while leaving GUI-editor behavior intact.

**Tech Stack:** Electron, TypeScript, Node.js child processes/path utilities, Vitest.

---

### Task 1: Specify Neovim behavior on all desktop platforms

**Files:**
- Modify: `frontend/src/main/editor-handoff.test.ts`

- [ ] **Step 1: Add failing macOS, Linux, and Windows tests**

Add behavior tests that construct `createEditorHandoff` with each platform, expose only the needed fake executables, open `targetId: "neovim"`, and assert these launch contracts:

```ts
expect(macInput.launch).toHaveBeenCalledWith(
	"/usr/bin/osascript",
	["-e", expect.stringContaining("exec '/bin/nvim'")],
	"/worktrees/ao-1",
);

expect(linuxInput.launch).toHaveBeenCalledWith(
	"/bin/gnome-terminal",
	["--", "/bin/nvim", "/work trees/ao-1"],
	"/work trees/ao-1",
);

expect(windowsInput.launch).toHaveBeenCalledWith(
	"C:\\Windows\\System32\\cmd.exe",
	["/d", "/s", "/k", '"C:\\bin\\nvim.exe" "C:\\work trees\\feature & fix"'],
	"C:\\work trees\\feature & fix",
);
```

The macOS case must use a workspace containing an apostrophe and assert that the generated AppleScript contains the shell-safe split-quote sequence rather than interpolating the raw path as executable syntax.

- [ ] **Step 2: Run the focused test and verify failure**

Run:

```bash
cd frontend
npx vitest run --config vite.renderer.config.ts src/main/editor-handoff.test.ts
```

Expected: the new cases fail because `neovim` is not a supported target.

### Task 2: Add terminal-aware Neovim detection and launch resolution

**Files:**
- Modify: `frontend/src/shared/editor-handoff.ts`
- Modify: `frontend/src/main/editor-handoff.ts`

- [ ] **Step 1: Add the shared editor ID**

Insert `"neovim"` in `EDITOR_IDS` beside the other command-discovered editors:

```ts
export const EDITOR_IDS = [
	"cursor",
	"vscode",
	"neovim",
	"windsurf",
	"zed",
	"trae",
	"kiro",
	"positron",
	"vscodium",
	"vscode-insiders",
	"sublime",
	"intellij",
	"webstorm",
	"pycharm",
	"goland",
	"phpstorm",
	"rubymine",
	"clion",
	"rider",
	"android-studio",
	"fleet",
] as const;
```

- [ ] **Step 2: Make path operations platform-specific**

Select the path implementation from the requested platform and use it for `PATH` splitting, extension checks, and joins:

```ts
function pathAPI(platform: Platform): path.PlatformPath {
	return platform === "win32" ? path.win32 : path.posix;
}
```

This allows a Windows resolver to produce Windows paths even when its tests run on macOS or Linux.

- [ ] **Step 3: Let resolved commands build workspace arguments**

Replace the fixed prefix shape with a workspace-aware builder:

```ts
type ResolvedCommand = {
	command: string;
	argsForWorkspace: (workspacePath: string) => string[];
};

type EditorCandidate = {
	id: EditorId;
	name: string;
	commands: string[];
	macApps?: string[];
	requiresTerminal?: boolean;
};
```

Existing CLI and macOS app-bundle editors return their current arguments through `argsForWorkspace`, preserving behavior.

- [ ] **Step 4: Add safe terminal launch builders**

Add quoting helpers and a terminal-editor resolver with these contracts:

```ts
function shellQuote(value: string): string {
	return `'${value.replaceAll("'", `'\\''`)}'`;
}

function appleScriptString(value: string): string {
	return `"${value.replaceAll("\\", "\\\\").replaceAll('"', '\\"')}"`;
}

function windowsCommandArg(value: string): string {
	return `"${value.replaceAll('"', '""')}"`;
}
```

macOS launches `/usr/bin/osascript` and asks Terminal to activate and `do script` an escaped `exec <nvim> <workspace>` command. Linux resolves one of `x-terminal-emulator`, `gnome-terminal`, `konsole`, `xfce4-terminal`, `kitty`, or `alacritty` and uses that terminal's argv execute prefix. Windows launches `ComSpec`/`COMSPEC`/`cmd.exe` with `/d /s /k` and a quoted Neovim command line.

- [ ] **Step 5: Register Neovim and use generated arguments**

Add the candidate:

```ts
{ id: "neovim", name: "Neovim", commands: ["nvim"], requiresTerminal: true },
```

When opening a resolved editor or terminal, pass `resolved.argsForWorkspace(workspacePath)` to the existing injected launcher.

- [ ] **Step 6: Run focused tests and typecheck**

Run:

```bash
cd frontend
npx vitest run --config vite.renderer.config.ts src/main/editor-handoff.test.ts
npm run typecheck
```

Expected: all editor-handoff tests pass and TypeScript exits 0.

- [ ] **Step 7: Commit the implementation**

```bash
git add frontend/src/shared/editor-handoff.ts frontend/src/main/editor-handoff.ts frontend/src/main/editor-handoff.test.ts
git commit -m "feat: add cross-platform Neovim editor handoff"
```

### Task 3: Verify the complete frontend and local desktop flow

**Files:**
- Verify only.

- [ ] **Step 1: Run all frontend unit tests**

Run:

```bash
cd frontend
npm test
```

Expected: all Vitest files pass.

- [ ] **Step 2: Build the frontend**

Run:

```bash
cd frontend
npm run build
```

Expected: Vite/Electron build exits 0.

- [ ] **Step 3: Check the final diff**

Run:

```bash
git diff origin/main...HEAD --check
git status --short
```

Expected: no whitespace errors and no uncommitted implementation files.

- [ ] **Step 4: Start AO for interactive testing**

Run from the worktree:

```bash
cd frontend
npm run dev
```

Expected: the Electron development app opens. Neovim appears in the editor picker only when `nvim` is installed and discoverable in the app's environment.
