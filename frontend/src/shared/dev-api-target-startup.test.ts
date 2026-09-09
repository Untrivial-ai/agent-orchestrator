import path from "node:path";
import { describe, expect, it } from "vitest";

import { DEFAULT_DAEMON_PORT, DEV_DAEMON_PORT } from "./daemon-attach";
import { processIsAlive } from "./daemon-discovery";
import { resolveDevApiTarget } from "./dev-api-target";

const HOME = "/home/tester";
const ENOENT = () => {
	throw Object.assign(new Error("ENOENT"), { code: "ENOENT" });
};

/** No run file anywhere — the state a clean machine is in before Electron starts. */
function noRunFiles(env: Record<string, string | undefined>) {
	return {
		env,
		homeDir: HOME,
		platform: "linux" as NodeJS.Platform,
		joinPath: path.join,
		isPidLive: () => true,
		readRunFile: ENOENT,
	};
}

describe("fresh launch, before any daemon has written a run file", () => {
	// Forge loads this config and starts Vite before Electron exists, so on a
	// clean `npm run dev` there is no run file to read and the target is fixed
	// for the whole Vite lifetime. Falling back to the standalone default aimed
	// the proxy at 3001 while the supervised daemon came up on 3002 — the 502 in
	// #4324, or a silent hit on another AO instance holding 3001.
	it("targets the Electron dev daemon port", () => {
		expect(resolveDevApiTarget(noRunFiles({}))).toBe(`http://127.0.0.1:${DEV_DAEMON_PORT}`);
	});

	// `dev:web` has no Electron and no supervised daemon, so it must keep
	// pointing at the standalone default.
	it("keeps the standalone default under dev:web", () => {
		expect(resolveDevApiTarget(noRunFiles({ VITE_NO_ELECTRON: "1" }))).toBe(
			`http://127.0.0.1:${DEFAULT_DAEMON_PORT}`,
		);
	});

	// An explicit AO_PORT wins in both modes, as it does when main.ts builds the
	// daemon environment.
	it("honours an explicit AO_PORT", () => {
		expect(resolveDevApiTarget(noRunFiles({ AO_PORT: "3456" }))).toBe("http://127.0.0.1:3456");
		expect(resolveDevApiTarget(noRunFiles({ AO_PORT: "3456", VITE_NO_ELECTRON: "1" }))).toBe(
			"http://127.0.0.1:3456",
		);
	});
});

describe("a recorded port is only trusted behind a positive, live pid", () => {
	function withRunFile(body: unknown, alive = true) {
		const devRunFile = path.join(HOME, ".ao", "dev", "running.json");
		return {
			env: {},
			homeDir: HOME,
			platform: "linux" as NodeJS.Platform,
			joinPath: path.join,
			isPidLive: () => alive,
			readRunFile: (p: string) => {
				if (p === devRunFile) return JSON.stringify(body);
				return ENOENT();
			},
		};
	}

	const fallback = `http://127.0.0.1:${DEV_DAEMON_PORT}`;

	// parseRunFile reports a missing or non-integer pid as 0, so without a
	// positive-pid requirement these were all accepted without ever calling the
	// liveness check.
	it.each([
		["missing pid", { port: 4321 }],
		["string pid", { port: 4321, pid: "4242" }],
		["zero pid", { port: 4321, pid: 0 }],
		["negative pid", { port: 4321, pid: -1 }],
		["float pid", { port: 4321, pid: 42.5 }],
	])("rejects a run file with a %s", (_name, body) => {
		expect(resolveDevApiTarget(withRunFile(body))).toBe(fallback);
	});

	it("rejects a positive pid that is not live", () => {
		expect(resolveDevApiTarget(withRunFile({ port: 4321, pid: 4242 }, false))).toBe(fallback);
	});

	it("accepts a positive, live pid", () => {
		expect(resolveDevApiTarget(withRunFile({ port: 4321, pid: 4242 }, true))).toBe("http://127.0.0.1:4321");
	});
});

describe("processIsAlive", () => {
	// EPERM means the process exists but is owned by another user. Reading that
	// as dead would discard a perfectly good run file.
	it("counts a live pid as alive", () => {
		expect(processIsAlive(process.pid)).toBe(true);
	});

	it("counts an unused pid as dead", () => {
		expect(processIsAlive(2147483646)).toBe(false);
	});

	// These never reach process.kill. 0 signals the caller's own process group
	// and a negative pid signals the group named by its absolute value, so both
	// would succeed and report "alive" for a pid that names no process — and 0 is
	// exactly what parseRunFile yields for a run file with no usable pid.
	it.each([
		["zero", 0],
		["negative", -1],
		["a whole process group", -process.pid],
		["a float", 42.5],
		["NaN", Number.NaN],
	])("counts %s as dead without signalling anything", (_name, pid) => {
		expect(processIsAlive(pid)).toBe(false);
	});
});
