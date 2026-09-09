// Helpers for discovering the daemon's actually-bound port. The configured
// AO_PORT is only a request — the daemon may bind a different port (port 0,
// operator overrides), so the supervisor trusts what the daemon reports:
//   - the slog text line `msg="daemon listening" addr=127.0.0.1:<port>`
//     (backend/internal/httpd/server.go, written to stderr), and
//   - the running.json handshake file (backend/internal/runfile).
// These functions are kept side-effect free and dependency-free (no node:*
// imports — vite-plugin-electron-renderer's polyfill breaks them under vitest)
// so tests can exercise them directly; the Electron main process owns the
// streams, fs polling, and timers.

// Minimal join: "/" works for fs access on every platform Node supports,
// including Windows paths that already contain backslashes (e.g. %APPDATA%).
function joinPath(...segments: string[]): string {
	return segments.map((segment) => segment.replace(/[/\\]+$/, "")).join("/");
}

/**
 * Parse one daemon log line for the listen announcement. slog's TextHandler
 * emits `time=… level=INFO msg="daemon listening" addr=127.0.0.1:3001 pid=…`;
 * the addr value never contains spaces, so it is unquoted. Returns the bound
 * port, or null when the line is not the announcement.
 */
export function parseDaemonListenPort(line: string): number | null {
	if (!line.includes('msg="daemon listening"')) return null;
	const addr = /(?:^|\s)addr=("?)([^"\s]+)\1/.exec(line)?.[2];
	if (!addr) return null;
	return portFromAddr(addr);
}

// Take the segment after the last ":" so IPv6 literals like [::1]:3001 parse too.
function portFromAddr(addr: string): number | null {
	const separator = addr.lastIndexOf(":");
	if (separator === -1) return null;
	const port = Number(addr.slice(separator + 1));
	return Number.isInteger(port) && port >= 1 && port <= 65535 ? port : null;
}

/**
 * Incrementally scan a stdio stream for the listen announcement. Returns a
 * chunk consumer that line-buffers (chunks can split a line anywhere) and
 * invokes onPort exactly once, for the first announcement seen.
 */
export function createListenPortScanner(onPort: (port: number) => void): (chunk: string) => void {
	let pending = "";
	let done = false;
	return (chunk) => {
		if (done) return;
		pending += chunk;
		const lines = pending.split("\n");
		pending = lines.pop() ?? "";
		for (const line of lines) {
			const port = parseDaemonListenPort(line);
			if (port !== null) {
				done = true;
				onPort(port);
				return;
			}
		}
	};
}

/** Parsed running.json handshake — see backend/internal/runfile.Info. */
export type RunFileInfo = {
	pid: number;
	port: number;
	/** startedAt in epoch ms; 0 when missing/unparseable. */
	startedAtMs: number;
	/**
	 * Daemon ownership tag — read from running.json so the attach-path link
	 * decision uses the daemon's durable record, not the current process env.
	 * "app" = desktop-spawned (re-link on attach); "persistent" = spawned under
	 * AO_KEEP_DAEMON (stays alive across app quit, never re-linked);
	 * undefined/empty = headless `ao start` daemon.
	 */
	owner?: string;
	/** Desktop launch that supplied this daemon's private browser token. */
	appRunId?: string;
	browserRuntimeAddress?: string;
};

/** Parse running.json contents. Returns null for malformed JSON or an invalid port. */
export function parseRunFile(contents: string): RunFileInfo | null {
	let raw: unknown;
	try {
		raw = JSON.parse(contents);
	} catch {
		return null;
	}
	if (typeof raw !== "object" || raw === null) return null;
	const { pid, port, startedAt, owner, appRunId, browserRuntimeAddress } = raw as {
		pid?: unknown;
		port?: unknown;
		startedAt?: unknown;
		owner?: unknown;
		appRunId?: unknown;
		browserRuntimeAddress?: unknown;
	};
	if (typeof port !== "number" || !Number.isInteger(port) || port < 1 || port > 65535) return null;
	const startedAtMs = typeof startedAt === "string" ? Date.parse(startedAt) : NaN;
	return {
		pid: typeof pid === "number" && Number.isInteger(pid) ? pid : 0,
		port,
		startedAtMs: Number.isNaN(startedAtMs) ? 0 : startedAtMs,
		owner: typeof owner === "string" ? owner : undefined,
		appRunId: typeof appRunId === "string" ? appRunId : undefined,
		browserRuntimeAddress: typeof browserRuntimeAddress === "string" ? browserRuntimeAddress : undefined,
	};
}

/**
 * Subdirectory of ~/.ao holding dev-profile state, so a `npm run dev` daemon
 * never collides with a concurrently running installed-app one. main.ts derives
 * the dev data and state dirs from this same constant, and the subdir also
 * isolates supervise.sock on Unix (the backend derives it as
 * dir(RunFilePath)/supervise.sock) and the named pipe on Windows
 * (supervisorPipeFromRunFile derives it from the same dir basename).
 */
export const DEV_STATE_SUBDIR = "dev";

/**
 * Run file the Electron dev daemon writes. Kept here so the Vite dev-server
 * config and main.ts agree on one path instead of each spelling out
 * `~/.ao/dev/running.json`.
 */
export function devRunFilePath(homeDir: string, joinPath: (...parts: string[]) => string): string {
	return joinPath(homeDir, ".ao", DEV_STATE_SUBDIR, "running.json");
}

/**
 * Whether a pid names a live process.
 *
 * `process.kill(pid, 0)` sends no signal; it throws iff the pid cannot be
 * signalled. EPERM means the process exists but belongs to another user, so it
 * counts as alive — treating it as dead would discard a perfectly good run
 * file written by a daemon running under a different account.
 *
 * Non-positive pids are rejected before the call, not passed through:
 * `process.kill(0, 0)` signals the caller's own process group and a negative
 * pid signals the group named by its absolute value, so either would answer
 * "alive" for a pid that names no process. parseRunFile reports a missing or
 * non-integer pid as 0, so that is exactly what a malformed run file yields.
 */
export function processIsAlive(pid: number): boolean {
	if (!Number.isInteger(pid) || pid <= 0) return false;
	try {
		process.kill(pid, 0);
		return true;
	} catch (error) {
		return (error as NodeJS.ErrnoException).code === "EPERM";
	}
}

/**
 * Where the daemon writes running.json when AO_RUN_FILE is unset. Matches
 * backend/internal/config's canonical AO home default so the supervisor reads
 * the same file the daemon writes. Returns null when the user home directory
 * cannot be resolved.
 */
export function defaultRunFilePath(
	platform: NodeJS.Platform,
	_env: Record<string, string | undefined>,
	homeDir: string,
): string | null {
	void platform;
	if (!homeDir) return null;
	return joinPath(homeDir, ".ao", "running.json");
}
