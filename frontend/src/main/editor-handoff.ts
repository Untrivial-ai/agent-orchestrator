import { constants, statSync } from "node:fs";
import path from "node:path";
import {
	isEditorId,
	type EditorHandoffState,
	type EditorId,
	type OpenSessionTargetInput,
	type OpenSessionTargetResult,
	type OpenTarget,
} from "../shared/editor-handoff";

type Platform = NodeJS.Platform;

type ResolvedCommand = {
	command: string;
	argsBeforeWorkspace?: string[];
};

type EditorCandidate = {
	id: EditorId;
	name: string;
	commands: string[];
	macApps?: string[];
	/**
	 * Windows-only fallback install locations, relative to a resolved root
	 * (e.g. `%LOCALAPPDATA%\Programs\Cursor\bin`). These are probed only on
	 * win32 and only AFTER a PATH scan fails, so an explicit PATH install
	 * always wins. Paths are built from environment variables at resolve time,
	 * never hard-coded to a specific machine.
	 */
	winInstallDirs?: string[][];
};

const PROGRAM_FILES = "%ProgramFiles%";
const PROGRAM_FILES_X86 = "%ProgramFiles(x86)%";
const LOCAL_APPDATA_PROGRAMS = "%LOCALAPPDATA%\\Programs";

const EDITOR_CANDIDATES: EditorCandidate[] = [
	{ id: "cursor", name: "Cursor", commands: ["cursor"], macApps: ["Cursor"], winInstallDirs: [[LOCAL_APPDATA_PROGRAMS, "Cursor", "resources", "app", "bin"], [PROGRAM_FILES, "Cursor", "bin"]] },
	{ id: "vscode", name: "VS Code", commands: ["code"], macApps: ["Visual Studio Code"], winInstallDirs: [[LOCAL_APPDATA_PROGRAMS, "Microsoft VS Code", "bin"], [PROGRAM_FILES, "Microsoft VS Code", "bin"]] },
	{ id: "windsurf", name: "Windsurf", commands: ["windsurf"], macApps: ["Windsurf"], winInstallDirs: [[LOCAL_APPDATA_PROGRAMS, "Windsurf", "resources", "app", "bin"], [PROGRAM_FILES, "Windsurf", "bin"]] },
	{ id: "zed", name: "Zed", commands: ["zed"], macApps: ["Zed"], winInstallDirs: [[LOCAL_APPDATA_PROGRAMS, "Zed", "bin"]] },
	{ id: "trae", name: "Trae", commands: ["trae"], macApps: ["Trae"], winInstallDirs: [[LOCAL_APPDATA_PROGRAMS, "Trae", "resources", "app", "bin"], [PROGRAM_FILES, "Trae", "bin"]] },
	{ id: "kiro", name: "Kiro", commands: ["kiro"], macApps: ["Kiro"], winInstallDirs: [[LOCAL_APPDATA_PROGRAMS, "Kiro", "bin"]] },
	{ id: "positron", name: "Positron", commands: ["positron"], macApps: ["Positron"], winInstallDirs: [[LOCAL_APPDATA_PROGRAMS, "Positron", "resources", "app", "bin"]] },
	{ id: "vscodium", name: "VSCodium", commands: ["codium"], macApps: ["VSCodium"], winInstallDirs: [[LOCAL_APPDATA_PROGRAMS, "VSCodium", "resources", "app", "bin"], [PROGRAM_FILES, "VSCodium", "bin"]] },
	{ id: "vscode-insiders", name: "VS Code Insiders", commands: ["code-insiders"], macApps: ["Visual Studio Code - Insiders"], winInstallDirs: [[LOCAL_APPDATA_PROGRAMS, "Microsoft VS Code Insiders", "bin"], [PROGRAM_FILES, "Microsoft VS Code Insiders", "bin"]] },
	{ id: "sublime", name: "Sublime Text", commands: ["subl"], macApps: ["Sublime Text"], winInstallDirs: [[PROGRAM_FILES, "Sublime Text"], [PROGRAM_FILES, "Sublime Text 3"], [LOCAL_APPDATA_PROGRAMS, "Sublime Text"]] },
	{ id: "intellij", name: "IntelliJ IDEA", commands: ["idea"], macApps: ["IntelliJ IDEA", "IntelliJ IDEA CE"], winInstallDirs: [[PROGRAM_FILES, "JetBrains", "IntelliJ IDEA", "bin"], [LOCAL_APPDATA_PROGRAMS, "JetBrains", "IntelliJ IDEA", "bin"]] },
	{ id: "webstorm", name: "WebStorm", commands: ["webstorm"], macApps: ["WebStorm"], winInstallDirs: [[PROGRAM_FILES, "JetBrains", "WebStorm", "bin"], [LOCAL_APPDATA_PROGRAMS, "JetBrains", "WebStorm", "bin"]] },
	{ id: "pycharm", name: "PyCharm", commands: ["pycharm"], macApps: ["PyCharm", "PyCharm CE"], winInstallDirs: [[PROGRAM_FILES, "JetBrains", "PyCharm", "bin"], [LOCAL_APPDATA_PROGRAMS, "JetBrains", "PyCharm", "bin"]] },
	{ id: "goland", name: "GoLand", commands: ["goland"], macApps: ["GoLand"], winInstallDirs: [[PROGRAM_FILES, "JetBrains", "GoLand", "bin"], [LOCAL_APPDATA_PROGRAMS, "JetBrains", "GoLand", "bin"]] },
	{ id: "phpstorm", name: "PhpStorm", commands: ["phpstorm"], macApps: ["PhpStorm"], winInstallDirs: [[PROGRAM_FILES, "JetBrains", "PhpStorm", "bin"], [LOCAL_APPDATA_PROGRAMS, "JetBrains", "PhpStorm", "bin"]] },
	{ id: "rubymine", name: "RubyMine", commands: ["rubymine"], macApps: ["RubyMine"], winInstallDirs: [[PROGRAM_FILES, "JetBrains", "RubyMine", "bin"], [LOCAL_APPDATA_PROGRAMS, "JetBrains", "RubyMine", "bin"]] },
	{ id: "clion", name: "CLion", commands: ["clion"], macApps: ["CLion"], winInstallDirs: [[PROGRAM_FILES, "JetBrains", "CLion", "bin"], [LOCAL_APPDATA_PROGRAMS, "JetBrains", "CLion", "bin"]] },
	{ id: "rider", name: "Rider", commands: ["rider"], macApps: ["Rider"], winInstallDirs: [[PROGRAM_FILES, "JetBrains", "Rider", "bin"], [LOCAL_APPDATA_PROGRAMS, "JetBrains", "Rider", "bin"]] },
	{ id: "android-studio", name: "Android Studio", commands: ["studio"], macApps: ["Android Studio"], winInstallDirs: [[PROGRAM_FILES, "Android", "Android Studio", "bin"]] },
	{ id: "fleet", name: "Fleet", commands: ["fleet"], macApps: ["Fleet"], winInstallDirs: [[PROGRAM_FILES, "JetBrains", "Fleet", "bin"]] },
];

const WIN_INSTALL_ROOTS: Record<string, (env: NodeJS.ProcessEnv) => string | undefined> = {
	[LOCAL_APPDATA_PROGRAMS]: (env) => {
		const localAppData = env.LOCALAPPDATA;
		return localAppData ? path.join(localAppData, "Programs") : undefined;
	},
	[PROGRAM_FILES]: (env) => env.ProgramFiles || env.PROGRAMFILES,
	[PROGRAM_FILES_X86]: (env) => env["ProgramFiles(x86)"] || env.PROGRAMFILES_X86,
};

export type EditorHandoffDeps = {
	platform: Platform;
	env: NodeJS.ProcessEnv;
	homeDir: string;
	resolveWorkspace: (sessionId: string) => Promise<string>;
	readPreference: () => Promise<EditorId>;
	writePreference: (editorId: EditorId) => Promise<void>;
	launch: (command: string, args: readonly string[], cwd: string) => Promise<void>;
	openDirectory: (workspacePath: string) => Promise<void>;
	isExecutable?: (candidatePath: string) => boolean;
	isDirectory?: (candidatePath: string) => boolean;
	logError?: (message: string, error: unknown) => void;
};

export type EditorHandoff = {
	getState(sessionId: string): Promise<EditorHandoffState>;
	open(input: OpenSessionTargetInput): Promise<OpenSessionTargetResult>;
};

function defaultIsExecutable(candidatePath: string, platform: Platform): boolean {
	try {
		const stat = statSync(candidatePath);
		return stat.isFile() && (platform === "win32" || (stat.mode & constants.X_OK) !== 0);
	} catch {
		return false;
	}
}

function defaultIsDirectory(candidatePath: string): boolean {
	try {
		return statSync(candidatePath).isDirectory();
	} catch {
		return false;
	}
}

function executableNames(command: string, platform: Platform, env: NodeJS.ProcessEnv): string[] {
	if (platform !== "win32" || path.extname(command)) return [command];
	const extensions = (env.PATHEXT || ".COM;.EXE;.BAT;.CMD").split(";").filter(Boolean);
	const extended = [
		...extensions.map((extension) => command + extension.toLowerCase()),
		...extensions.map((extension) => command + extension.toUpperCase()),
	];
	// On win32, probe Windows-native launchers (.exe/.cmd/.bat/etc.) before the
	// bare, extension-less name. Editors like VS Code and Cursor ship a `.cmd`
	// batch shim and a bare `#!/usr/bin/env sh` script side by side; returning
	// the bare script makes spawn fail because Windows cannot execute an `sh`
	// script directly. Preferring the extension shims keeps the real launcher.
	return [...extended, command];
}

function commandSearchDirs(platform: Platform, env: NodeJS.ProcessEnv): string[] {
	const fromPath = (env.PATH || "").split(path.delimiter).filter(Boolean);
	if (platform === "darwin") return [...fromPath, "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"];
	if (platform === "linux") return [...fromPath, "/usr/local/bin", "/usr/bin"];
	return fromPath;
}

function resolveOnPath(
	command: string,
	platform: Platform,
	env: NodeJS.ProcessEnv,
	isExecutable: (candidatePath: string) => boolean,
): string | undefined {
	for (const directory of commandSearchDirs(platform, env)) {
		for (const name of executableNames(command, platform, env)) {
			const candidatePath = path.join(directory, name);
			if (isExecutable(candidatePath)) return candidatePath;
		}
	}
	return undefined;
}

// Expands a candidate's Windows install locations into real paths. Each entry
// is a sequence of path segments whose root is one of the WIN_INSTALL_ROOTS
// keys (resolved from env) followed by literal segments. Returns the concrete
// directories, skipping any whose root env var is unset.
function winInstallDirsFor(candidate: EditorCandidate, env: NodeJS.ProcessEnv): string[] {
	const dirs: string[] = [];
	for (const segments of candidate.winInstallDirs ?? []) {
		const [rootToken, ...rest] = segments;
		const resolver = WIN_INSTALL_ROOTS[rootToken];
		if (!resolver) continue;
		const root = resolver(env);
		if (!root) continue;
		dirs.push(path.join(root, ...rest));
	}
	return dirs;
}

function resolveInDirs(
	command: string,
	platform: Platform,
	env: NodeJS.ProcessEnv,
	dirs: readonly string[],
	isExecutable: (candidatePath: string) => boolean,
): string | undefined {
	for (const directory of dirs) {
		for (const name of executableNames(command, platform, env)) {
			const candidatePath = path.join(directory, name);
			if (isExecutable(candidatePath)) return candidatePath;
		}
	}
	return undefined;
}

function resolveEditor(
	candidate: EditorCandidate,
	deps: EditorHandoffDeps,
	isExecutable: (candidatePath: string) => boolean,
	isDirectory: (candidatePath: string) => boolean,
): ResolvedCommand | undefined {
	for (const command of candidate.commands) {
		const resolved = resolveOnPath(command, deps.platform, deps.env, isExecutable);
		if (resolved) return { command: resolved };
	}
	// Windows-only fallback: probe standard per-user and system install locations
	// after PATH misses. Keep this on the PATH-first path so an explicit PATH
	// install always wins, and gate strictly on win32 so macOS/Linux are untouched.
	if (deps.platform === "win32") {
		const installDirs = winInstallDirsFor(candidate, deps.env);
		for (const command of candidate.commands) {
			const resolved = resolveInDirs(command, deps.platform, deps.env, installDirs, isExecutable);
			if (resolved) return { command: resolved };
		}
	}
	if (deps.platform !== "darwin") return undefined;
	for (const appName of candidate.macApps ?? []) {
		for (const root of ["/Applications", path.join(deps.homeDir, "Applications")]) {
			if (isDirectory(path.join(root, `${appName}.app`))) {
				return { command: "/usr/bin/open", argsBeforeWorkspace: ["-a", appName] };
			}
		}
	}
	return undefined;
}

function resolveTerminal(
	deps: EditorHandoffDeps,
	isExecutable: (candidatePath: string) => boolean,
): { target: OpenTarget; command: ResolvedCommand } | undefined {
	if (deps.platform === "darwin") {
		return {
			target: { id: "terminal", name: "Terminal", kind: "terminal" },
			command: { command: "/usr/bin/open", argsBeforeWorkspace: ["-a", "Terminal"] },
		};
	}
	if (deps.platform === "win32") {
		return {
			target: { id: "terminal", name: "Command Prompt", kind: "terminal" },
			command: { command: deps.env.ComSpec || deps.env.COMSPEC || "cmd.exe" },
		};
	}
	for (const command of ["x-terminal-emulator", "gnome-terminal", "konsole", "xfce4-terminal", "kitty"]) {
		const resolved = resolveOnPath(command, deps.platform, deps.env, isExecutable);
		if (resolved) {
			return {
				target: { id: "terminal", name: "Terminal", kind: "terminal" },
				command: { command: resolved },
			};
		}
	}
	return undefined;
}

export function createEditorHandoff(deps: EditorHandoffDeps): EditorHandoff {
	const isExecutable = deps.isExecutable ?? ((candidatePath) => defaultIsExecutable(candidatePath, deps.platform));
	const isDirectory = deps.isDirectory ?? defaultIsDirectory;
	const fileManager: OpenTarget = {
		id: "file-manager",
		name: deps.platform === "darwin" ? "Finder" : deps.platform === "win32" ? "File Explorer" : "File Manager",
		kind: "file_manager",
	};

	// Re-resolve editors and terminal on every call instead of freezing them at
	// construction. Editor discovery reads deps.env each time, so an editor
	// installed (or removed) after startup is picked up instead of being stale.
	const resolveAll = () => {
		const editors = EDITOR_CANDIDATES.flatMap((candidate) => {
			const command = resolveEditor(candidate, deps, isExecutable, isDirectory);
			return command ? [{ target: { id: candidate.id, name: candidate.name, kind: "editor" } as OpenTarget, command }] : [];
		});
		const terminal = resolveTerminal(deps, isExecutable);
		const targets = [...editors.map(({ target }) => target), fileManager, ...(terminal ? [terminal.target] : [])];
		return { editors, terminal, targets };
	};

	const workspaceUnavailable = (error: unknown) =>
		error instanceof Error && error.message.trim() ? error.message : "Session workspace is not available.";

	return {
		async getState(sessionId) {
			const { targets } = resolveAll();
			const preferredEditorId = await deps.readPreference();
			try {
				await deps.resolveWorkspace(sessionId);
				return { targets, preferredEditorId, workspaceAvailable: true };
			} catch (error) {
				return {
					targets,
					preferredEditorId,
					workspaceAvailable: false,
					unavailableReason: workspaceUnavailable(error),
				};
			}
		},

		async open(input) {
			const sessionId = input.sessionId.trim();
			if (!sessionId) throw new Error("Session is required.");
			const { editors, terminal, targets } = resolveAll();
			const preferredEditorId = await deps.readPreference();
			const targetId = input.targetId ?? preferredEditorId;
			if (input.targetId && input.targetId !== "file-manager" && input.targetId !== "terminal" && !isEditorId(input.targetId)) {
				throw new Error("That open target is not supported.");
			}
			const target = targets.find((target) => target.id === targetId);
			if (!target) {
				if (isEditorId(targetId)) throw new Error("That editor is not installed. Choose another option.");
				throw new Error("That open target is not available.");
			}
			const workspacePath = await deps.resolveWorkspace(sessionId);
			try {
				if (target.kind === "file_manager") {
					await deps.openDirectory(workspacePath);
				} else {
					const resolved = target.kind === "terminal"
						? terminal?.command
						: editors.find(({ target: editor }) => editor.id === target.id)?.command;
					if (!resolved) throw new Error("target command was not resolved");
					const args = [...(resolved.argsBeforeWorkspace ?? []), ...(target.kind === "terminal" && deps.platform !== "darwin" ? [] : [workspacePath])];
					await deps.launch(resolved.command, args, workspacePath);
				}
			} catch (error) {
				deps.logError?.(`failed to open session target ${target.id}`, error);
				throw new Error(`Could not open ${target.name}. Check that it is installed and try again.`);
			}
			if (target.kind === "editor") await deps.writePreference(target.id as EditorId);
			return target;
		},
	};
}
