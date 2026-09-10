// @vitest-environment node
import path from "node:path";
import { describe, expect, it, vi } from "vitest";
import { createEditorHandoff, type EditorHandoffDeps } from "./editor-handoff";

function deps(overrides: Partial<EditorHandoffDeps> = {}): EditorHandoffDeps {
	return {
		platform: "darwin",
		env: { PATH: "/bin" },
		homeDir: "/Users/tester",
		resolveWorkspace: vi.fn().mockResolvedValue("/worktrees/ao-1"),
		readPreference: vi.fn().mockResolvedValue("cursor"),
		writePreference: vi.fn().mockResolvedValue(undefined),
		launch: vi.fn().mockResolvedValue(undefined),
		openDirectory: vi.fn().mockResolvedValue(undefined),
		isExecutable: (candidatePath) => candidatePath === "/bin/code",
		isDirectory: (candidatePath) => candidatePath === "/Applications/Cursor.app",
		...overrides,
	};
}

// Builds win32 deps whose PATH and install-location probing can be driven
// entirely by mocks. Expected command paths are built with path.join so the
// assertions hold whether the suite runs on Windows (backslash separators) or
// POSIX CI runners (forward slashes). Each win32 test supplies an isExecutable
// that recognizes the specific installer shim it is exercising.
function winDeps(overrides: Partial<EditorHandoffDeps> = {}): EditorHandoffDeps {
	return deps({
		platform: "win32",
		// PATH uses a drive letter without a trailing colon. The resolver splits
		// PATH by path.delimiter, which is ":" on POSIX CI runners — a "C:" entry
		// would be broken in two ("C" and "/bin") and the directory never found.
		env: {
			PATH: path.join("C", "bin"),
			LOCALAPPDATA: path.join("C:", "Users", "tester", "AppData", "Local"),
			ProgramFiles: path.join("C:", "Program Files"),
			PATHEXT: ".COM;.EXE;.BAT;.CMD",
			ComSpec: "C:\\Windows\\System32\\cmd.exe",
		},
		homeDir: path.join("C:", "Users", "tester"),
		isDirectory: () => false,
		...overrides,
	});
}

function installedExecutables(...expectedPaths: string[]) {
	const expected = new Set(expectedPaths);
	return (candidatePath: string) => expected.has(candidatePath);
}

describe("editor handoff", () => {
	it("detects Dock-installed apps and keeps Finder and Terminal as safe fallbacks", async () => {
		const handoff = createEditorHandoff(deps());
		const state = await handoff.getState("ao-1");
		expect(state).toMatchObject({ preferredEditorId: "cursor", workspaceAvailable: true });
		expect(state.targets.map(({ id }) => id)).toEqual(["cursor", "vscode", "file-manager", "terminal"]);
	});

	it("reports a missing workspace without hiding the available targets", async () => {
		const handoff = createEditorHandoff(deps({
			resolveWorkspace: vi.fn().mockRejectedValue(new Error("Session workspace is not available.")),
		}));
		const state = await handoff.getState("ao-1");
		expect(state.workspaceAvailable).toBe(false);
		expect(state.unavailableReason).toBe("Session workspace is not available.");
		expect(state.targets).toHaveLength(4);
	});

	it("opens only the workspace root and persists a chosen editor", async () => {
		const input = deps();
		const handoff = createEditorHandoff(input);
		await expect(handoff.open({ sessionId: "ao-1", targetId: "vscode" })).resolves.toMatchObject({
			id: "vscode",
			kind: "editor",
		});
		expect(input.launch).toHaveBeenCalledWith("/bin/code", ["/worktrees/ao-1"], "/worktrees/ao-1");
		expect(input.writePreference).toHaveBeenCalledWith("vscode");
	});

	it("opens Finder without changing the editor preference", async () => {
		const input = deps();
		const handoff = createEditorHandoff(input);
		await handoff.open({ sessionId: "ao-1", targetId: "file-manager" });
		expect(input.openDirectory).toHaveBeenCalledWith("/worktrees/ao-1");
		expect(input.writePreference).not.toHaveBeenCalled();
	});

	it("does not silently replace a missing preferred editor", async () => {
		const handoff = createEditorHandoff(deps({
			isExecutable: () => false,
			isDirectory: () => false,
		}));
		await expect(handoff.open({ sessionId: "ao-1" })).rejects.toThrow(
			"That editor is not installed. Choose another option.",
		);
	});

	it("turns a launcher failure into a visible path-free error", async () => {
		const input = deps({ launch: vi.fn().mockRejectedValue(new Error("/private/path failed")) });
		const handoff = createEditorHandoff(input);
		await expect(handoff.open({ sessionId: "ao-1", targetId: "vscode" })).rejects.toThrow(
			"Could not open VS Code. Check that it is installed and try again.",
		);
	});
});

describe("editor handoff (win32 fallback discovery)", () => {
	// Cursor's Windows per-user install keeps its .cmd shim under
	// resources\app\bin (the VS Code fork layout), not a top-level bin dir.
	const cursorBin = path.join("C:", "Users", "tester", "AppData", "Local", "Programs", "Cursor", "resources", "app", "bin", "cursor.cmd");
	const vscodeSystemBin = path.join("C:", "Program Files", "Microsoft VS Code", "bin", "code.cmd");
	const vscodeAgentExec = path.join("C:", "Program Files", "Microsoft VS Code", "bin", "code.exe");

	it("finds an editor that is present only in the per-user LOCALAPPDATA install dir", async () => {
		const handoff = createEditorHandoff(winDeps({ isExecutable: installedExecutables(cursorBin) }));
		const state = await handoff.getState("ao-1");
		const cursor = state.targets.find(({ id }) => id === "cursor");
		expect(cursor).toBeDefined();
	});

	it("prefers a Windows-native .cmd shim over a bare extension-less sh script on PATH", async () => {
		// VS Code/Cursor ship a bare `code` sh script and a `code.cmd` batch
		// side by side; spawn must use the .cmd so cmd.exe can run it.
		const dir = path.join("C", "bin");
		const shScript = path.join(dir, "code");
		const cmdShim = path.join(dir, "code.cmd");
		const input = winDeps({ isExecutable: installedExecutables(shScript, cmdShim) });
		const handoff = createEditorHandoff(input);
		await handoff.open({ sessionId: "ao-1", targetId: "vscode" });
		expect(input.launch).toHaveBeenCalledWith(cmdShim, ["/worktrees/ao-1"], "/worktrees/ao-1");
	});

	it("finds an editor installed only in a system Program Files dir", async () => {
		const handoff = createEditorHandoff(winDeps({ isExecutable: installedExecutables(vscodeSystemBin) }));
		const state = await handoff.getState("ao-1");
		const vscode = state.targets.find(({ id }) => id === "vscode");
		expect(vscode).toBeDefined();
	});

	it("prefers a PATH install over the fallback install dirs", async () => {
		const pathCode = path.join("C", "bin", "code.cmd");
		const input = winDeps({ isExecutable: installedExecutables(pathCode, vscodeAgentExec) });
		const handoff = createEditorHandoff(input);
		await handoff.open({ sessionId: "ao-1", targetId: "vscode" });
		expect(input.launch).toHaveBeenCalledWith(pathCode, ["/worktrees/ao-1"], "/worktrees/ao-1");
	});

	it("resolves a .cmd shim from an install dir via PATHEXT", async () => {
		const handoff = createEditorHandoff(winDeps({ isExecutable: installedExecutables(vscodeSystemBin) }));
		const state = await handoff.getState("ao-1");
		expect(state.targets.some(({ id }) => id === "vscode")).toBe(true);
	});

	it("safely ignores nonexistent fallback roots (unset env vars) instead of throwing", async () => {
		const input = winDeps({
			env: {
				PATH: path.join("C", "bin"),
				PATHEXT: ".COM;.EXE;.BAT;.CMD",
			},
			isExecutable: () => false,
		});
		const handoff = createEditorHandoff(input);
		const state = await handoff.getState("ao-1");
		expect(state.targets).toEqual([
			{ id: "file-manager", name: "File Explorer", kind: "file_manager" },
			{ id: "terminal", name: "Command Prompt", kind: "terminal" },
		]);
	});

	it("leaves editors unavailable on win32 when neither PATH nor install dirs match", async () => {
		const handoff = createEditorHandoff(winDeps({ isExecutable: () => false }));
		const state = await handoff.getState("ao-1");
		expect(state.targets.some(({ id }) => id === "vscode")).toBe(false);
		expect(state.targets.some(({ id }) => id === "cursor")).toBe(false);
	});

	it("does not scan install dirs on non-win32 platforms", async () => {
		const macHandoff = createEditorHandoff(deps({ isExecutable: () => false, isDirectory: () => false }));
		const state = await macHandoff.getState("ao-1");
		expect(state.targets.some(({ id }) => id === "vscode")).toBe(false);
	});

	it("still resolves an editor on linux via its extra search dirs (not the win32 fallback)", async () => {
		const code = path.join("/usr/local/bin", "code");
		const input = deps({
			platform: "linux",
			env: { PATH: "/bin" },
			isExecutable: (candidatePath) => candidatePath === code,
			isDirectory: () => false,
		});
		const state = await createEditorHandoff(input).getState("ao-1");
		expect(state.targets.some(({ id }) => id === "vscode")).toBe(true);
	});

	it("re-resolves editors installed after handoff creation instead of freezing at startup", async () => {
		const installed = new Set<string>();
		const input = winDeps({
			isExecutable: (candidatePath) => installed.has(candidatePath),
			isDirectory: () => false,
		});
		const handoff = createEditorHandoff(input);
		expect((await handoff.getState("ao-1")).targets.some(({ id }) => id === "cursor")).toBe(false);

		installed.add(cursorBin);
		expect((await handoff.getState("ao-1")).targets.some(({ id }) => id === "cursor")).toBe(true);
	});
});
