import { constants, statSync } from "node:fs";
import path from "node:path";
import {
	isEditorId,
	type EditorHandoffState,
	type EditorId,
	type OpenSessionTargetInput,
	type OpenSessionTargetResult,
	type OpenTarget,
	type OpenTargetId,
} from "../shared/editor-handoff";

type Platform = NodeJS.Platform;

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

const EDITOR_CANDIDATES: EditorCandidate[] = [
	{ id: "cursor", name: "Cursor", commands: ["cursor"], macApps: ["Cursor"] },
	{ id: "vscode", name: "VS Code", commands: ["code"], macApps: ["Visual Studio Code"] },
	{ id: "neovim", name: "Neovim", commands: ["nvim"], requiresTerminal: true },
	{ id: "windsurf", name: "Windsurf", commands: ["windsurf"], macApps: ["Windsurf"] },
	{ id: "zed", name: "Zed", commands: ["zed"], macApps: ["Zed"] },
	{ id: "trae", name: "Trae", commands: ["trae"], macApps: ["Trae"] },
	{ id: "kiro", name: "Kiro", commands: ["kiro"], macApps: ["Kiro"] },
	{ id: "positron", name: "Positron", commands: ["positron"], macApps: ["Positron"] },
	{ id: "vscodium", name: "VSCodium", commands: ["codium"], macApps: ["VSCodium"] },
	{ id: "vscode-insiders", name: "VS Code Insiders", commands: ["code-insiders"], macApps: ["Visual Studio Code - Insiders"] },
	{ id: "sublime", name: "Sublime Text", commands: ["subl"], macApps: ["Sublime Text"] },
	{ id: "intellij", name: "IntelliJ IDEA", commands: ["idea"], macApps: ["IntelliJ IDEA", "IntelliJ IDEA CE"] },
	{ id: "webstorm", name: "WebStorm", commands: ["webstorm"], macApps: ["WebStorm"] },
	{ id: "pycharm", name: "PyCharm", commands: ["pycharm"], macApps: ["PyCharm", "PyCharm CE"] },
	{ id: "goland", name: "GoLand", commands: ["goland"], macApps: ["GoLand"] },
	{ id: "phpstorm", name: "PhpStorm", commands: ["phpstorm"], macApps: ["PhpStorm"] },
	{ id: "rubymine", name: "RubyMine", commands: ["rubymine"], macApps: ["RubyMine"] },
	{ id: "clion", name: "CLion", commands: ["clion"], macApps: ["CLion"] },
	{ id: "rider", name: "Rider", commands: ["rider"], macApps: ["Rider"] },
	{ id: "android-studio", name: "Android Studio", commands: ["studio"], macApps: ["Android Studio"] },
	{ id: "fleet", name: "Fleet", commands: ["fleet"], macApps: ["Fleet"] },
];

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

function pathAPI(platform: Platform) {
	return platform === "win32" ? path.win32 : path.posix;
}

function executableNames(command: string, platform: Platform, env: NodeJS.ProcessEnv): string[] {
	if (platform !== "win32" || pathAPI(platform).extname(command)) return [command];
	const extensions = (env.PATHEXT || ".COM;.EXE;.BAT;.CMD").split(";").filter(Boolean);
	return [command, ...extensions.map((extension) => command + extension.toLowerCase()), ...extensions.map((extension) => command + extension.toUpperCase())];
}

function commandSearchDirs(platform: Platform, env: NodeJS.ProcessEnv): string[] {
	const fromPath = (env.PATH || "").split(pathAPI(platform).delimiter).filter(Boolean);
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
			const candidatePath = pathAPI(platform).join(directory, name);
			if (isExecutable(candidatePath)) return candidatePath;
		}
	}
	return undefined;
}

function shellQuote(value: string): string {
	return `'${value.replaceAll("'", `'\\''`)}'`;
}

function appleScriptString(value: string): string {
	return `"${value.replaceAll("\\", "\\\\").replaceAll('"', '\\"')}"`;
}

function windowsCommandArg(value: string): string {
	return `"${value.replaceAll('"', '""')}"`;
}

const LINUX_TERMINAL_LAUNCHERS = [
	{ command: "x-terminal-emulator", argsBeforeCommand: ["-e"] },
	{ command: "gnome-terminal", argsBeforeCommand: ["--"] },
	{ command: "konsole", argsBeforeCommand: ["-e"] },
	{ command: "xfce4-terminal", argsBeforeCommand: ["--execute"] },
	{ command: "kitty", argsBeforeCommand: [] },
	{ command: "alacritty", argsBeforeCommand: ["-e"] },
] as const;

function resolveTerminalEditor(editorCommand: string, deps: EditorHandoffDeps, isExecutable: (candidatePath: string) => boolean): ResolvedCommand | undefined {
	if (deps.platform === "darwin") {
		return {
			command: "/usr/bin/osascript",
			argsForWorkspace: (workspacePath) => {
				const commandLine = `exec ${shellQuote(editorCommand)} ${shellQuote(workspacePath)}`;
				const script = `tell application "Terminal"\nactivate\ndo script ${appleScriptString(commandLine)}\nend tell`;
				return ["-e", script];
			},
		};
	}
	if (deps.platform === "win32") {
		return {
			command: deps.env.ComSpec || deps.env.COMSPEC || "cmd.exe",
			argsForWorkspace: (workspacePath) => {
				const commandLine = [editorCommand, workspacePath].map(windowsCommandArg).join(" ");
				// /s strips the first and last quotes around the command string. The
				// extra outer pair preserves the quotes around both executable paths.
				return ["/d", "/s", "/v:off", "/k", `"${commandLine}"`];
			},
		};
	}
	for (const launcher of LINUX_TERMINAL_LAUNCHERS) {
		const resolved = resolveOnPath(launcher.command, deps.platform, deps.env, isExecutable);
		if (resolved) {
			return {
				command: resolved,
				argsForWorkspace: (workspacePath) => [...launcher.argsBeforeCommand, editorCommand, workspacePath],
			};
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
		if (resolved) {
			if (candidate.requiresTerminal) return resolveTerminalEditor(resolved, deps, isExecutable);
			return {
				command: resolved,
				argsForWorkspace: (workspacePath) => [workspacePath],
			};
		}
	}
	if (deps.platform !== "darwin") return undefined;
	for (const appName of candidate.macApps ?? []) {
		for (const root of ["/Applications", path.posix.join(deps.homeDir, "Applications")]) {
			if (isDirectory(path.posix.join(root, `${appName}.app`))) {
				return {
					command: "/usr/bin/open",
					argsForWorkspace: (workspacePath) => ["-a", appName, workspacePath],
				};
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
			command: {
				command: "/usr/bin/open",
				argsForWorkspace: (workspacePath) => ["-a", "Terminal", workspacePath],
			},
		};
	}
	if (deps.platform === "win32") {
		return {
			target: { id: "terminal", name: "Command Prompt", kind: "terminal" },
			command: {
				command: deps.env.ComSpec || deps.env.COMSPEC || "cmd.exe",
				argsForWorkspace: () => [],
			},
		};
	}
	for (const launcher of LINUX_TERMINAL_LAUNCHERS) {
		const resolved = resolveOnPath(launcher.command, deps.platform, deps.env, isExecutable);
		if (resolved) {
			return {
				target: { id: "terminal", name: "Terminal", kind: "terminal" },
				command: { command: resolved, argsForWorkspace: () => [] },
			};
		}
	}
	return undefined;
}

export function createEditorHandoff(deps: EditorHandoffDeps): EditorHandoff {
	const isExecutable = deps.isExecutable ?? ((candidatePath) => defaultIsExecutable(candidatePath, deps.platform));
	const isDirectory = deps.isDirectory ?? defaultIsDirectory;
	const editors = EDITOR_CANDIDATES.flatMap((candidate) => {
		const command = resolveEditor(candidate, deps, isExecutable, isDirectory);
		return command ? [{ target: { id: candidate.id, name: candidate.name, kind: "editor" } as OpenTarget, command }] : [];
	});
	const fileManager: OpenTarget = {
		id: "file-manager",
		name: deps.platform === "darwin" ? "Finder" : deps.platform === "win32" ? "File Explorer" : "File Manager",
		kind: "file_manager",
	};
	const terminal = resolveTerminal(deps, isExecutable);
	const targets = [...editors.map(({ target }) => target), fileManager, ...(terminal ? [terminal.target] : [])];

	const resolveTarget = (targetId: OpenTargetId) => targets.find((target) => target.id === targetId);
	const workspaceUnavailable = (error: unknown) =>
		error instanceof Error && error.message.trim() ? error.message : "Session workspace is not available.";

	return {
		async getState(sessionId) {
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
			const preferredEditorId = await deps.readPreference();
			const targetId = input.targetId ?? preferredEditorId;
			if (input.targetId && input.targetId !== "file-manager" && input.targetId !== "terminal" && !isEditorId(input.targetId)) {
				throw new Error("That open target is not supported.");
			}
			const target = resolveTarget(targetId);
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
					await deps.launch(resolved.command, resolved.argsForWorkspace(workspacePath), workspacePath);
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
