// Deciding whether to ATTACH to an already-running daemon or SPAWN a fresh one.
//
// Two independent signals are consulted, in order:
//   1. the running.json handshake file (whatever the last daemon wrote), and
//   2. a direct probe of the expected port, independent of the run-file.
//
// (2) is the defensive backstop for issue #367: a standalone `ao daemon` may be
// serving the port while running.json is missing, stale, unparseable, names a
// dead PID, or reports a PID that disagrees with /healthz. In every one of those
// cases the run-file check yields null; without the port probe the supervisor
// would spawn a child daemon that the Go bind guard then refuses ("daemon
// already running … refusing to start") and exits 1.
//
// These functions are kept side-effect free and dependency-injected (no node:*
// or electron imports — the vite-plugin-electron-renderer polyfill breaks node:*
// under vitest, see daemon-discovery.ts) so they can be exercised directly; the
// Electron main process owns the real fs reads, process signals, fetch, and
// path identity check.

import type { DaemonStatus } from "./daemon-status";
import { parseRunFile } from "./daemon-discovery";

// The daemon's default bind port (backend/internal/config). AO_PORT overrides it.
export const DEFAULT_DAEMON_PORT = 3001;
// The `service` field every genuine AO daemon stamps on its health payloads.
export const DAEMON_SERVICE_NAME = "agent-orchestrator-daemon";

export type DaemonProbe = {
	status: string;
	service: string;
	pid: number;
	executablePath?: string;
	workingDirectory?: string;
	startupWorkingDirectory?: string;
	/** Stable outer .AppImage path (AO_APPIMAGE), present only when the daemon's launcher runs under AppImage. */
	appImagePath?: string;
};

/** A /healthz|/readyz probe of a loopback port; resolves null when nothing valid answers. */
export type DaemonProber = (port: number, endpoint: "healthz" | "readyz") => Promise<DaemonProbe | null>;

/**
 * The port a freshly spawned daemon is expected to bind: AO_PORT when set and
 * valid, otherwise the daemon's default. Used to probe for an already-serving
 * daemon before spawning a child that would only refuse and exit.
 */
export function expectedDaemonPort(env: Record<string, string | undefined>): number {
	const configured = env.AO_PORT ? Number(env.AO_PORT) : NaN;
	return Number.isInteger(configured) && configured >= 1 && configured <= 65535 ? configured : DEFAULT_DAEMON_PORT;
}

/**
 * Validate a /healthz or /readyz JSON body against the daemon contract. Returns
 * the typed probe, or null when the body is the wrong shape, status, or service
 * (e.g. some unrelated server happens to occupy the port).
 */
export function parseDaemonProbe(endpoint: "healthz" | "readyz", body: unknown): DaemonProbe | null {
	if (typeof body !== "object" || body === null) return null;
	const candidate = body as Partial<DaemonProbe>;
	if (candidate.status !== (endpoint === "healthz" ? "ok" : "ready")) return null;
	if (candidate.service !== DAEMON_SERVICE_NAME) return null;
	if (typeof candidate.pid !== "number" || !Number.isInteger(candidate.pid)) return null;
	return {
		status: candidate.status,
		service: candidate.service,
		pid: candidate.pid,
		executablePath: typeof candidate.executablePath === "string" ? candidate.executablePath : undefined,
		workingDirectory: typeof candidate.workingDirectory === "string" ? candidate.workingDirectory : undefined,
		startupWorkingDirectory:
			typeof candidate.startupWorkingDirectory === "string" ? candidate.startupWorkingDirectory : undefined,
		appImagePath: typeof candidate.appImagePath === "string" ? candidate.appImagePath : undefined,
	};
}

export type RunFileResolveDeps = {
	/** running.json contents, or null when the file has no path or could not be read. */
	runFileContents: string | null;
	isProcessAlive: (pid: number) => boolean;
	probe: DaemonProber;
	/** Foreign-daemon check (dev/bundled identity); returns a message, or null when it is ours. */
	identityError: (probe: DaemonProbe) => string | null;
};

/**
 * Attach decision driven by the running.json handshake. Returns:
 *   - a "ready" status   → attach to the recorded daemon,
 *   - an "error" status  → a daemon is up but unusable (not ready / foreign);
 *                          surface it rather than spawn,
 *   - null               → the run-file is absent/stale/inconsistent; the caller
 *                          should fall through to {@link resolveDaemonFromPort}.
 */
export async function resolveDaemonFromRunFile(deps: RunFileResolveDeps): Promise<DaemonStatus | null> {
	const { runFileContents, isProcessAlive, probe, identityError } = deps;
	if (runFileContents === null) return null;
	const info = parseRunFile(runFileContents);
	if (!info || !isProcessAlive(info.pid)) return null;

	const health = await probe(info.port, "healthz");
	// The recorded PID must match the live daemon; otherwise the run-file points
	// at the wrong process — return null so the caller falls through to the port
	// probe rather than trusting a stale handshake.
	if (!health || health.pid !== info.pid) return null;
	return readinessStatus(info.port, info.pid, health, probe, identityError);
}

export type PortProbeResolveDeps = {
	expectedPort: number;
	probe: DaemonProber;
	/** Foreign-daemon check (dev/bundled identity); returns a message, or null when it is ours. */
	identityError: (probe: DaemonProbe) => string | null;
};

/**
 * Attach decision driven by a direct /healthz probe of the expected port,
 * independent of the run-file (issue #367 backstop). Returns:
 *   - a "ready" status  → attach to the daemon serving the port,
 *   - an "error" status → a daemon serves the port but is unusable (not ready, or
 *                         a foreign binary the identity check refuses); surface it
 *                         rather than spawn (spawning would only collide and die),
 *   - null              → nothing genuine answers the port; the caller should spawn.
 *
 * This mirrors {@link resolveDaemonFromRunFile}'s post-handshake validation
 * (/readyz + identity), anchoring on the PID /healthz reports instead of the
 * run-file's, so attaching via the port is no laxer than attaching via the file.
 */
export async function resolveDaemonFromPort(deps: PortProbeResolveDeps): Promise<DaemonStatus | null> {
	const { expectedPort, probe, identityError } = deps;
	const health = await probe(expectedPort, "healthz");
	if (!health) return null;
	return readinessStatus(expectedPort, health.pid, health, probe, identityError);
}

/**
 * Shared tail of both attach paths: given a daemon confirmed serving /healthz on
 * `port` with PID `pid`, confirm it is ready and is the daemon we expect, and
 * build the resulting DaemonStatus. Returns an "error" status (never null) — by
 * here a daemon is definitely occupying the port, so spawning is never the right
 * move.
 */
async function readinessStatus(
	port: number,
	pid: number,
	health: DaemonProbe,
	probe: DaemonProber,
	identityError: (probe: DaemonProbe) => string | null,
): Promise<DaemonStatus> {
	const ready = await probe(port, "readyz");
	if (!ready || ready.pid !== pid) {
		return {
			state: "error",
			port,
			pid,
			executablePath: health.executablePath,
			workingDirectory: health.workingDirectory,
			message: "An AO daemon is already running, but it is not ready yet.",
			code: "not_ready",
		};
	}

	const message = identityError(ready);
	if (message) {
		return {
			state: "error",
			port,
			pid,
			executablePath: ready.executablePath,
			workingDirectory: ready.workingDirectory,
			message,
			code: "identity_mismatch",
		};
	}

	return {
		state: "ready",
		port,
		pid,
		executablePath: ready.executablePath,
		workingDirectory: ready.workingDirectory,
	};
}
