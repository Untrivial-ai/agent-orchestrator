// @vitest-environment node
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

describe("editor handoff", () => {
	it("detects Dock-installed apps and keeps Finder and Terminal as safe fallbacks", async () => {
		const handoff = createEditorHandoff(deps());
		const state = await handoff.getState("ao-1");
		expect(state).toMatchObject({ preferredEditorId: "cursor", workspaceAvailable: true });
		expect(state.targets.map(({ id }) => id)).toEqual(["cursor", "vscode", "file-manager", "terminal"]);
	});

	it("opens Neovim in macOS Terminal with shell-safe workspace quoting", async () => {
		const workspacePath = "/work trees/it's & safe";
		const input = deps({
			resolveWorkspace: vi.fn().mockResolvedValue(workspacePath),
			isExecutable: (candidatePath) => candidatePath === "/bin/nvim",
			isDirectory: () => false,
		});
		const handoff = createEditorHandoff(input);

		const state = await handoff.getState("ao-1");
		expect(state.targets.map(({ id }) => id)).toContain("neovim");
		await handoff.open({ sessionId: "ao-1", targetId: "neovim" });

		expect(input.launch).toHaveBeenCalledWith(
			"/usr/bin/osascript",
			["-e", `tell application "Terminal"\nactivate\ndo script "exec '/bin/nvim' '/work trees/it'\\\\''s & safe'"\nend tell`],
			workspacePath,
		);
	});

	it.each([
		["x-terminal-emulator", ["-e"]],
		["gnome-terminal", ["--"]],
		["konsole", ["-e"]],
		["xfce4-terminal", ["--execute"]],
		["kitty", []],
		["alacritty", ["-e"]],
	] as const)("opens Neovim through the Linux %s launcher", async (terminalCommand, argsBeforeCommand) => {
		const workspacePath = "/work trees/ao-1";
		const input = deps({
			platform: "linux",
			env: { PATH: "/bin" },
			resolveWorkspace: vi.fn().mockResolvedValue(workspacePath),
			isExecutable: (candidatePath) => ["/bin/nvim", `/bin/${terminalCommand}`].includes(candidatePath),
			isDirectory: () => false,
		});
		const handoff = createEditorHandoff(input);

		await handoff.open({ sessionId: "ao-1", targetId: "neovim" });

		expect(input.launch).toHaveBeenCalledWith(
			`/bin/${terminalCommand}`,
			[...argsBeforeCommand, "/bin/nvim", workspacePath],
			workspacePath,
		);
	});

	it("opens Neovim through Command Prompt on Windows", async () => {
		const workspacePath = "C:\\work trees\\feature & fix";
		const input = deps({
			platform: "win32",
			env: {
				PATH: "C:\\bin",
				PATHEXT: ".EXE",
				ComSpec: "C:\\Windows\\System32\\cmd.exe",
			},
			homeDir: "C:\\Users\\tester",
			resolveWorkspace: vi.fn().mockResolvedValue(workspacePath),
			isExecutable: (candidatePath) => candidatePath === "C:\\bin\\nvim.exe",
			isDirectory: () => false,
		});
		const handoff = createEditorHandoff(input);

		await handoff.open({ sessionId: "ao-1", targetId: "neovim" });

		expect(input.launch).toHaveBeenCalledWith(
			"C:\\Windows\\System32\\cmd.exe",
			["/d", "/s", "/v:off", "/k", `""C:\\bin\\nvim.exe" "${workspacePath}""`],
			workspacePath,
		);
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
