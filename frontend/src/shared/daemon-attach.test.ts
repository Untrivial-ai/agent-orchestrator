import { createServer, type Server } from "node:http";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	DAEMON_SERVICE_NAME,
	DEFAULT_DAEMON_PORT,
	type DaemonProbe,
	type DaemonProber,
	expectedDaemonPort,
	parseDaemonProbe,
	resolveDaemonFromPort,
	resolveDaemonFromRunFile,
} from "./daemon-attach";

// A run-file the daemon would write: pid+port+timestamp, as JSON.
function runFile(pid: number, port: number): string {
	return JSON.stringify({ pid, port, startedAt: "2026-06-10T16:15:04Z" });
}

// A probe map keyed by `${port}:${endpoint}` → fake an HTTP probe deterministically.
function fakeProbe(responses: Record<string, DaemonProbe | null>): DaemonProber {
	return (port, endpoint) => Promise.resolve(responses[`${port}:${endpoint}`] ?? null);
}

const ALIVE = () => true;
const DEAD = () => false;
const NO_IDENTITY_ERROR = () => null;

describe("expectedDaemonPort", () => {
	it("defaults to 3001 when AO_PORT is unset or empty", () => {
		expect(expectedDaemonPort({})).toBe(3001);
		expect(expectedDaemonPort({ AO_PORT: "" })).toBe(3001);
		expect(DEFAULT_DAEMON_PORT).toBe(3001);
	});

	it("honors a valid AO_PORT override", () => {
		expect(expectedDaemonPort({ AO_PORT: "3037" })).toBe(3037);
		expect(expectedDaemonPort({ AO_PORT: "1" })).toBe(1);
		expect(expectedDaemonPort({ AO_PORT: "65535" })).toBe(65535);
	});

	it("falls back to the default for an out-of-range or non-integer AO_PORT", () => {
		expect(expectedDaemonPort({ AO_PORT: "0" })).toBe(3001);
		expect(expectedDaemonPort({ AO_PORT: "70000" })).toBe(3001);
		expect(expectedDaemonPort({ AO_PORT: "3001.5" })).toBe(3001);
		expect(expectedDaemonPort({ AO_PORT: "not-a-number" })).toBe(3001);
		expect(expectedDaemonPort({ AO_PORT: "-1" })).toBe(3001);
	});
});

describe("parseDaemonProbe", () => {
	const healthBody = { status: "ok", service: DAEMON_SERVICE_NAME, pid: 4242 };
	const readyBody = { status: "ready", service: DAEMON_SERVICE_NAME, pid: 4242 };

	it("accepts a well-formed healthz body", () => {
		expect(parseDaemonProbe("healthz", healthBody)).toEqual({
			status: "ok",
			service: DAEMON_SERVICE_NAME,
			pid: 4242,
			executablePath: undefined,
			workingDirectory: undefined,
			startupWorkingDirectory: undefined,
		});
	});

	it("accepts a well-formed readyz body and carries identity fields", () => {
		expect(
			parseDaemonProbe("readyz", {
				...readyBody,
				executablePath: "/bin/ao",
				workingDirectory: "/work/data",
				startupWorkingDirectory: "/work",
			}),
		).toEqual({
			status: "ready",
			service: DAEMON_SERVICE_NAME,
			pid: 4242,
			executablePath: "/bin/ao",
			workingDirectory: "/work/data",
			startupWorkingDirectory: "/work",
		});
	});

	it("rejects a status that does not match the endpoint", () => {
		expect(parseDaemonProbe("healthz", readyBody)).toBeNull();
		expect(parseDaemonProbe("readyz", healthBody)).toBeNull();
	});

	it("rejects a foreign or missing service", () => {
		expect(parseDaemonProbe("healthz", { ...healthBody, service: "something-else" })).toBeNull();
		expect(parseDaemonProbe("healthz", { status: "ok", pid: 1 })).toBeNull();
	});

	it("rejects a missing or non-integer pid", () => {
		expect(parseDaemonProbe("healthz", { status: "ok", service: DAEMON_SERVICE_NAME })).toBeNull();
		expect(parseDaemonProbe("healthz", { ...healthBody, pid: 1.5 })).toBeNull();
		expect(parseDaemonProbe("healthz", { ...healthBody, pid: "4242" })).toBeNull();
	});

	it("rejects non-object bodies", () => {
		expect(parseDaemonProbe("healthz", null)).toBeNull();
		expect(parseDaemonProbe("healthz", "ok")).toBeNull();
		expect(parseDaemonProbe("healthz", 200)).toBeNull();
	});

	it("drops identity fields that are not strings", () => {
		const probe = parseDaemonProbe("healthz", { ...healthBody, executablePath: 5, workingDirectory: {} });
		expect(probe?.executablePath).toBeUndefined();
		expect(probe?.workingDirectory).toBeUndefined();
		expect(probe?.startupWorkingDirectory).toBeUndefined();
	});

	it("carries appImagePath when the daemon reports it", () => {
		const probe = parseDaemonProbe("readyz", {
			...readyBody,
			appImagePath: "/home/user/Apps/agent-orchestrator.AppImage",
		});
		expect(probe?.appImagePath).toBe("/home/user/Apps/agent-orchestrator.AppImage");
	});

	it("drops appImagePath when absent or not a string", () => {
		expect(parseDaemonProbe("healthz", healthBody)?.appImagePath).toBeUndefined();
		expect(parseDaemonProbe("healthz", { ...healthBody, appImagePath: 42 })?.appImagePath).toBeUndefined();
	});
});

describe("resolveDaemonFromRunFile", () => {
	it("returns null when there is no run-file (caller falls through to port probe)", async () => {
		const probe = vi.fn<DaemonProber>();
		const result = await resolveDaemonFromRunFile({
			runFileContents: null,
			isProcessAlive: ALIVE,
			probe,
			identityError: NO_IDENTITY_ERROR,
		});
		expect(result).toBeNull();
		expect(probe).not.toHaveBeenCalled(); // never probes without a parseable run-file
	});

	it("returns null for an unparseable run-file", async () => {
		const result = await resolveDaemonFromRunFile({
			runFileContents: "{not json",
			isProcessAlive: ALIVE,
			probe: fakeProbe({}),
			identityError: NO_IDENTITY_ERROR,
		});
		expect(result).toBeNull();
	});

	it("returns null when the recorded pid is not alive (#367 divergence)", async () => {
		const result = await resolveDaemonFromRunFile({
			runFileContents: runFile(4242, 3001),
			isProcessAlive: DEAD,
			probe: fakeProbe({
				"3001:healthz": { status: "ok", service: DAEMON_SERVICE_NAME, pid: 4242 },
			}),
			identityError: NO_IDENTITY_ERROR,
		});
		expect(result).toBeNull();
	});

	it("returns null when the health probe fails (#367 divergence)", async () => {
		const result = await resolveDaemonFromRunFile({
			runFileContents: runFile(4242, 3001),
			isProcessAlive: ALIVE,
			probe: fakeProbe({ "3001:healthz": null }),
			identityError: NO_IDENTITY_ERROR,
		});
		expect(result).toBeNull();
	});

	it("returns null when health.pid disagrees with the run-file pid (#367 divergence)", async () => {
		const result = await resolveDaemonFromRunFile({
			runFileContents: runFile(4242, 3001),
			isProcessAlive: ALIVE,
			probe: fakeProbe({
				"3001:healthz": { status: "ok", service: DAEMON_SERVICE_NAME, pid: 9999 },
			}),
			identityError: NO_IDENTITY_ERROR,
		});
		expect(result).toBeNull();
	});

	it("reports an error (not a spawn) when the daemon is up but not ready", async () => {
		const result = await resolveDaemonFromRunFile({
			runFileContents: runFile(4242, 3001),
			isProcessAlive: ALIVE,
			probe: fakeProbe({
				"3001:healthz": { status: "ok", service: DAEMON_SERVICE_NAME, pid: 4242 },
				"3001:readyz": null,
			}),
			identityError: NO_IDENTITY_ERROR,
		});
		expect(result).toEqual({
			state: "error",
			port: 3001,
			pid: 4242,
			executablePath: undefined,
			workingDirectory: undefined,
			message: "An AO daemon is already running, but it is not ready yet.",
			code: "not_ready",
		});
	});

	it("surfaces a foreign-daemon identity error", async () => {
		const result = await resolveDaemonFromRunFile({
			runFileContents: runFile(4242, 3001),
			isProcessAlive: ALIVE,
			probe: fakeProbe({
				"3001:healthz": { status: "ok", service: DAEMON_SERVICE_NAME, pid: 4242 },
				"3001:readyz": { status: "ready", service: DAEMON_SERVICE_NAME, pid: 4242, workingDirectory: "/other" },
			}),
			identityError: () => "Another AO daemon is already running from /other.",
		});
		expect(result).toMatchObject({
			state: "error",
			pid: 4242,
			port: 3001,
			message: "Another AO daemon is already running from /other.",
		});
	});

	it("attaches (ready) when the run-file, liveness, health, readiness, and identity all agree", async () => {
		const result = await resolveDaemonFromRunFile({
			runFileContents: runFile(4242, 3037),
			isProcessAlive: ALIVE,
			probe: fakeProbe({
				"3037:healthz": { status: "ok", service: DAEMON_SERVICE_NAME, pid: 4242 },
				"3037:readyz": {
					status: "ready",
					service: DAEMON_SERVICE_NAME,
					pid: 4242,
					executablePath: "/bin/ao",
					workingDirectory: "/work/backend",
				},
			}),
			identityError: NO_IDENTITY_ERROR,
		});
		expect(result).toEqual({
			state: "ready",
			port: 3037,
			pid: 4242,
			executablePath: "/bin/ao",
			workingDirectory: "/work/backend",
		});
	});
});

describe("resolveDaemonFromPort", () => {
	it("returns null when nothing valid answers the port (caller spawns)", async () => {
		const result = await resolveDaemonFromPort({
			expectedPort: 3001,
			probe: fakeProbe({}),
			identityError: NO_IDENTITY_ERROR,
		});
		expect(result).toBeNull();
	});

	it("attaches (ready) when a daemon answers /healthz and /readyz on the expected port", async () => {
		const result = await resolveDaemonFromPort({
			expectedPort: 3001,
			probe: fakeProbe({
				"3001:healthz": { status: "ok", service: DAEMON_SERVICE_NAME, pid: 777 },
				"3001:readyz": {
					status: "ready",
					service: DAEMON_SERVICE_NAME,
					pid: 777,
					executablePath: "/bin/ao",
					workingDirectory: "/work",
				},
			}),
			identityError: NO_IDENTITY_ERROR,
		});
		expect(result).toEqual({
			state: "ready",
			port: 3001,
			pid: 777,
			executablePath: "/bin/ao",
			workingDirectory: "/work",
		});
	});

	it("reports an error (not a spawn) when the serving daemon is not ready yet", async () => {
		const result = await resolveDaemonFromPort({
			expectedPort: 3001,
			probe: fakeProbe({
				"3001:healthz": { status: "ok", service: DAEMON_SERVICE_NAME, pid: 777 },
				"3001:readyz": null,
			}),
			identityError: NO_IDENTITY_ERROR,
		});
		expect(result).toEqual({
			state: "error",
			port: 3001,
			pid: 777,
			executablePath: undefined,
			workingDirectory: undefined,
			message: "An AO daemon is already running, but it is not ready yet.",
			code: "not_ready",
		});
	});

	it("reports an identity error (not a silent attach) for a foreign daemon binary on the port", async () => {
		const result = await resolveDaemonFromPort({
			expectedPort: 3001,
			probe: fakeProbe({
				"3001:healthz": { status: "ok", service: DAEMON_SERVICE_NAME, pid: 777 },
				"3001:readyz": { status: "ready", service: DAEMON_SERVICE_NAME, pid: 777, executablePath: "/old/ao" },
			}),
			identityError: (probe) =>
				probe.executablePath === "/new/ao"
					? null
					: `Another AO daemon is already running from ${probe.executablePath}.`,
		});
		expect(result).toMatchObject({
			state: "error",
			port: 3001,
			pid: 777,
			message: "Another AO daemon is already running from /old/ao.",
		});
	});

	it("probes exactly the expected port", async () => {
		const probe = vi.fn<DaemonProber>().mockResolvedValue(null);
		await resolveDaemonFromPort({ expectedPort: 4317, probe, identityError: NO_IDENTITY_ERROR });
		expect(probe).toHaveBeenCalledWith(4317, "healthz");
	});
});

// End-to-end against a REAL loopback HTTP server, exercising the actual network
// probe (Node's global fetch, mirroring main.ts's readDaemonProbe) and the real
// startup decision order: run-file first, then the #367 port-probe backstop.
describe("end-to-end against a real daemon server", () => {
	const servers: Server[] = [];

	afterEach(async () => {
		await Promise.all(servers.splice(0).map((s) => new Promise<void>((r) => s.close(() => r()))));
	});

	// Stand up a server on an ephemeral port. `service` lets us simulate a foreign
	// (non-AO) server squatting on the port.
	function startServer(opts: {
		pid: number;
		service?: string;
		executablePath?: string;
		workingDirectory?: string;
	}): Promise<number> {
		const service = opts.service ?? DAEMON_SERVICE_NAME;
		const server = createServer((req, res) => {
			const url = req.url ?? "";
			const base = {
				service,
				pid: opts.pid,
				executablePath: opts.executablePath,
				workingDirectory: opts.workingDirectory,
			};
			if (url === "/healthz") {
				res.writeHead(200, { "content-type": "application/json" });
				res.end(JSON.stringify({ status: "ok", ...base }));
				return;
			}
			if (url === "/readyz") {
				res.writeHead(200, { "content-type": "application/json" });
				res.end(JSON.stringify({ status: "ready", ...base }));
				return;
			}
			res.writeHead(404);
			res.end();
		});
		servers.push(server);
		return new Promise((resolve) => {
			server.listen(0, "127.0.0.1", () => {
				const addr = server.address();
				resolve(typeof addr === "object" && addr ? addr.port : 0);
			});
		});
	}

	// Faithful copy of main.ts's readDaemonProbe, but with global fetch in place of
	// electron's net.fetch — so this test exercises the real HTTP round-trip + parse.
	const realProbe: DaemonProber = async (port, endpoint) => {
		const controller = new AbortController();
		const timer = setTimeout(() => controller.abort(), 2_000);
		try {
			const response = await fetch(`http://127.0.0.1:${port}/${endpoint}`, { signal: controller.signal });
			if (!response.ok) return null;
			return parseDaemonProbe(endpoint, await response.json());
		} catch {
			return null;
		} finally {
			clearTimeout(timer);
		}
	};

	// The real startup decision: attach via run-file if possible, else fall back to
	// the direct port probe (the #367 fix), else null → caller spawns.
	async function startupDecision(opts: {
		runFileContents: string | null;
		isProcessAlive: (pid: number) => boolean;
		expectedPort: number;
		identityError?: (probe: DaemonProbe) => string | null;
	}) {
		const identityError = opts.identityError ?? NO_IDENTITY_ERROR;
		const fromRunFile = await resolveDaemonFromRunFile({
			runFileContents: opts.runFileContents,
			isProcessAlive: opts.isProcessAlive,
			probe: realProbe,
			identityError,
		});
		if (fromRunFile) return fromRunFile;
		return resolveDaemonFromPort({ expectedPort: opts.expectedPort, probe: realProbe, identityError });
	}

	it("attaches to a genuinely serving daemon via the direct port probe", async () => {
		const port = await startServer({ pid: 555 });
		const result = await resolveDaemonFromPort({
			expectedPort: port,
			probe: realProbe,
			identityError: NO_IDENTITY_ERROR,
		});
		expect(result).toEqual({
			state: "ready",
			port,
			pid: 555,
			executablePath: undefined,
			workingDirectory: undefined,
		});
	});

	it("returns null when the port is closed (caller spawns)", async () => {
		const port = await startServer({ pid: 1 });
		// Close the only server so the port is now refused.
		await Promise.all(servers.splice(0).map((s) => new Promise<void>((r) => s.close(() => r()))));
		const result = await resolveDaemonFromPort({
			expectedPort: port,
			probe: realProbe,
			identityError: NO_IDENTITY_ERROR,
		});
		expect(result).toBeNull();
	});

	it("does NOT attach to a foreign (non-AO) server squatting on the port", async () => {
		const port = await startServer({ pid: 1, service: "some-other-service" });
		const result = await resolveDaemonFromPort({
			expectedPort: port,
			probe: realProbe,
			identityError: NO_IDENTITY_ERROR,
		});
		expect(result).toBeNull();
	});

	// A foreign AO daemon (correct service, wrong binary) serving the port. The
	// identity check must surface an error rather than silently attach — the same
	// guard the run-file path enforces, now enforced on the port-probe path too.
	it("surfaces an identity error for a foreign AO binary serving the port (does not silently attach)", async () => {
		const port = await startServer({ pid: 909, executablePath: "/old/build/ao", workingDirectory: "/old/build" });
		const result = await startupDecision({
			runFileContents: null, // run-file diverged, so we reach the port probe
			isProcessAlive: ALIVE,
			expectedPort: port,
			identityError: (probe) =>
				probe.executablePath === "/expected/ao"
					? null
					: `Another AO daemon is already running from ${probe.executablePath}.`,
		});
		expect(result).toMatchObject({
			state: "error",
			port,
			pid: 909,
			message: "Another AO daemon is already running from /old/build/ao.",
		});
	});

	// THE #367 SCENARIO: a standalone `ao daemon` is serving the port, but the
	// run-file diverges — here it names a DEAD pid (e.g. a stale handshake from a
	// crashed launch). Pre-fix this fell through to spawn() and the Go child
	// refused with exit 1. Post-fix the port probe attaches instead.
	it("attaches when a daemon serves the port but the run-file names a dead pid", async () => {
		const port = await startServer({ pid: 6060, executablePath: "/bin/ao" });
		const result = await startupDecision({
			runFileContents: runFile(4242, port), // stale pid 4242 ...
			isProcessAlive: DEAD, // ... which is no longer alive
			expectedPort: port,
		});
		expect(result).toEqual({
			state: "ready",
			port,
			pid: 6060, // attached to the daemon actually serving the port
			executablePath: "/bin/ao",
			workingDirectory: undefined,
		});
	});

	// #367 VARIANT: run-file pid is alive, but /healthz reports a different pid
	// (run-file points at the wrong process). Old code returned null → spawn.
	it("attaches when the run-file pid disagrees with the daemon's reported pid", async () => {
		const port = await startServer({ pid: 8080 });
		const result = await startupDecision({
			runFileContents: runFile(4242, port),
			isProcessAlive: ALIVE,
			expectedPort: port,
		});
		expect(result).toMatchObject({ state: "ready", port, pid: 8080 });
	});

	it("still attaches via the run-file path when everything agrees (no regression)", async () => {
		const port = await startServer({
			pid: 4242,
			executablePath: "/work/backend/ao",
			workingDirectory: "/work/backend",
		});
		const result = await startupDecision({
			runFileContents: runFile(4242, port),
			isProcessAlive: ALIVE,
			expectedPort: port,
			identityError: (probe) => (probe.pid === 4242 ? null : "wrong daemon"),
		});
		expect(result).toMatchObject({ state: "ready", port, pid: 4242 });
	});
});
