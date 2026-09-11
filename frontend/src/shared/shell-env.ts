// Recovering the login-shell environment so a Finder/Dock launch (started by
// launchd, not a shell) gets the same PATH and exported credentials a terminal
// launch would. See docs/daemon-environment.md for the root cause.
//
// Kept pure and dependency-injected (no node:* or electron imports — the
// vite-plugin-electron-renderer polyfill breaks node:* under vitest, see
// daemon-attach.ts) so the parsing/merging logic is testable directly; the real
// shell spawn lives in main.ts and is injected as a ShellRunner.

import type { TerminalShellPreference } from "./ui-locale";

export const SHELL_ENV_SENTINEL = "__AO_SHELL_ENV__";

// PATH floor: dirs a working macOS/Linux box keeps tools in, appended when the
// shell probe fails so zellij/git/agents still resolve.
export const FALLBACK_PATH_DIRS = [
	"/opt/homebrew/bin",
	"/opt/homebrew/sbin",
	"/usr/local/bin",
	"/usr/bin",
	"/bin",
	"/usr/sbin",
	"/sbin",
];

// Ask the login shell (-l sources zprofile, -i sources zshrc) to print a
// sentinel then a NUL-separated env dump (-0 keeps values with newlines intact).
export function shellEnvArgs(): string[] {
	return ["-ilc", `printf '%s' '${SHELL_ENV_SENTINEL}'; env -0`];
}

export type WindowsExecutableLookup = (candidate: string) => string | null;

export type ShellEnvProbe = {
	shellPath: string;
	args: string[];
};

function windowsDirname(value: string): string {
	const normalized = value.replaceAll("\\", "/").replace(/\/+$/, "");
	const slash = normalized.lastIndexOf("/");
	return slash > 0 ? normalized.slice(0, slash) : "";
}

function windowsJoin(...parts: string[]): string {
	return parts
		.map((part) => part.replaceAll("/", "\\").replace(/^[\\/]+|[\\/]+$/g, ""))
		.filter(Boolean)
		.join("\\");
}

function windowsBasename(value: string): string {
	const normalized = value.replaceAll("\\", "/");
	return normalized.slice(normalized.lastIndexOf("/") + 1).toLowerCase();
}

function windowsProbeArgs(shellPath: string): string[] {
	switch (windowsBasename(shellPath)) {
		case "bash.exe":
		case "sh.exe":
			return ["--login", "-i", "-c", `printf '%s' '${SHELL_ENV_SENTINEL}'; env -0`];
		case "pwsh.exe":
		case "powershell.exe":
			return [
				"-NoLogo",
				"-Command",
				`Write-Output '${SHELL_ENV_SENTINEL}'; Get-ChildItem Env: | ForEach-Object { "$($_.Name)=$($_.Value)" }`,
			];
		case "cmd.exe":
			return ["/d", "/s", "/c", `echo ${SHELL_ENV_SENTINEL} & set`];
		default:
			return ["--login", "-i", "-c", `printf '%s' '${SHELL_ENV_SENTINEL}'; env -0`];
	}
}

function findGitBash(lookup: WindowsExecutableLookup): string | null {
	const gitPath = lookup("git.exe");
	const candidates = gitPath
		? [
				windowsJoin(windowsDirname(windowsDirname(gitPath)), "bin", "bash.exe"),
				windowsJoin(windowsDirname(windowsDirname(gitPath)), "usr", "bin", "bash.exe"),
			]
		: [];
	candidates.push("bash.exe");
	for (const candidate of candidates) {
		const resolved = lookup(candidate);
		if (resolved) return resolved;
	}
	return null;
}

/** Resolve the executable and probe command for the Windows login environment. */
export function resolveWindowsShellProbe(
	preference: TerminalShellPreference,
	env: Record<string, string | undefined>,
	lookup: WindowsExecutableLookup,
): ShellEnvProbe | null {
	const candidates: string[] = [];
	switch (preference.kind) {
		case "auto":
			candidates.push("pwsh.exe", "powershell.exe");
			if (env.ComSpec?.trim()) candidates.push(env.ComSpec.trim());
			candidates.push("cmd.exe");
			break;
		case "git-bash": {
			const resolved = findGitBash(lookup);
			if (resolved) candidates.push(resolved);
			break;
		}
		case "pwsh":
			candidates.push("pwsh.exe");
			break;
		case "powershell":
			candidates.push("powershell.exe");
			break;
		case "cmd":
			if (env.ComSpec?.trim()) candidates.push(env.ComSpec.trim());
			candidates.push("cmd.exe");
			break;
		case "custom":
			if (preference.path?.trim()) candidates.push(preference.path.trim());
			break;
	}
	for (const candidate of candidates) {
		const shellPath = lookup(candidate);
		if (shellPath) return { shellPath, args: windowsProbeArgs(shellPath) };
	}
	return null;
}

// Slice after the sentinel (drops banner/motd/prompt noise printed before it),
// split on NUL, split each record on the first '='.
export function parseEnvBlock(stdout: string): Record<string, string> {
	const at = stdout.lastIndexOf(SHELL_ENV_SENTINEL);
	const block = at === -1 ? stdout : stdout.slice(at + SHELL_ENV_SENTINEL.length);
	const out: Record<string, string> = {};
	const records = block.includes("\0") ? block.split("\0") : block.split(/\r?\n/);
	for (const rec of records) {
		if (rec === "") continue;
		const eq = rec.indexOf("=");
		if (eq <= 0) continue; // skip records with no key or a leading '='
		out[rec.slice(0, eq)] = rec.slice(eq + 1);
	}
	return out;
}

// Prefer $SHELL (the user's login shell); under launchd it may be absent, so
// fall back to /bin/zsh.
export function resolveShellPath(env: Record<string, string | undefined>): string {
	const shell = env.SHELL?.trim();
	return shell && shell.length > 0 ? shell : "/bin/zsh";
}

// Append any missing floor dirs to PATH, preserving the existing order/priority
// and de-duping.
export function withFallbackPath(currentPath: string | undefined): string {
	const result = (currentPath ?? "").split(":").filter(Boolean);
	const present = new Set(result);
	for (const dir of FALLBACK_PATH_DIRS) {
		if (!present.has(dir)) {
			present.add(dir);
			result.push(dir);
		}
	}
	return result.join(":");
}

function normalizeTerm(term: string | undefined): string {
	const trimmed = term?.trim();
	if (!trimmed || trimmed === "dumb") return "xterm-256color";
	return trimmed;
}

// Base = shell env, overlaid by processEnv so Electron/AO runtime vars win, then
// PATH forced to the shell's PATH (with floor), TERM forced to a tmux-usable
// value, then explicit overrides.
//
// TERM defaults to xterm-256color (what the renderer's xterm.js emulates): a
// Finder/Dock launch starts under launchd with no controlling tty, so TERM is
// unset, and the daemon's tmux attach client inherits that and dies with
// "open terminal failed: terminal does not support clear". Seeded as the base
// so a real TERM from the shell/process env still wins.
export function buildDaemonEnv(
	processEnv: NodeJS.ProcessEnv,
	shellEnv: Record<string, string> | null,
	overrides: Record<string, string>,
): NodeJS.ProcessEnv {
	const merged: NodeJS.ProcessEnv = { TERM: "xterm-256color", ...(shellEnv ?? {}), ...processEnv };
	merged.PATH = withFallbackPath(shellEnv?.PATH ?? processEnv.PATH);
	merged.TERM = normalizeTerm(merged.TERM);
	return { ...merged, ...overrides };
}

// Credential-management routes accept only explicitly trusted renderer
// origins. Packaged builds use app://renderer from the daemon defaults; the
// Vite renderer has a launch-specific HTTP origin that Electron must pass to
// the dev daemon. An operator-provided allowlist remains authoritative.
export function devDaemonAllowedOrigins(
	configuredAllowedOrigins: string | undefined,
	rendererURL: string,
): string {
	if (configuredAllowedOrigins?.trim()) return configuredAllowedOrigins;
	return `app://renderer,${new URL(rendererURL).origin}`;
}

export type ShellRunner = (shellPath: string, args: string[]) => Promise<string | null>;

export async function resolveShellEnvWithSpec(spec: ShellEnvProbe, run: ShellRunner): Promise<Record<string, string> | null> {
	try {
		const stdout = await run(spec.shellPath, spec.args);
		if (stdout == null) return null;
		const parsed = parseEnvBlock(stdout);
		return parsed.PATH ? parsed : null;
	} catch {
		return null;
	}
}

// Run the probe via an injected runner (main.ts supplies the real spawn).
// Returns null on any failure or if the result lacks PATH; the caller then falls
// back to the static floor.
export async function resolveShellEnv(
	env: Record<string, string | undefined>,
	run: ShellRunner,
): Promise<Record<string, string> | null> {
	return resolveShellEnvWithSpec({ shellPath: resolveShellPath(env), args: shellEnvArgs() }, run);
}
