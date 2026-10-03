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
	browserStreamAddress?: string;
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
	const { pid, port, startedAt, owner, appRunId, browserRuntimeAddress, browserStreamAddress } = raw as {
		pid?: unknown;
		port?: unknown;
		startedAt?: unknown;
		owner?: unknown;
		appRunId?: unknown;
		browserRuntimeAddress?: unknown;
		browserStreamAddress?: unknown;
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
		browserStreamAddress: typeof browserStreamAddress === "string" ? browserStreamAddress : undefined,
	};
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
