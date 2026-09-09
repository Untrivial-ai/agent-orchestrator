import path from "node:path";
import { describe, expect, it } from "vitest";

import { DEFAULT_DAEMON_PORT, DEV_DAEMON_PORT } from "./daemon-attach";
import { resolveDevApiTarget } from "./dev-api-target";

const HOME = "/home/tester";
const DEV_RUN_FILE = path.join(HOME, ".ao", "dev", "running.json");
const STANDALONE_RUN_FILE = path.join(HOME, ".ao", "running.json");
// These cases pass no VITE_NO_ELECTRON, i.e. the `npm run dev` path, where the
// daemon Electron supervises comes up on DEV_DAEMON_PORT. The standalone
// default is covered separately in dev-api-target-startup.test.ts.
const DEFAULT_TARGET = `http://127.0.0.1:${DEV_DAEMON_PORT}`;

const ENOENT = () => {
	throw Object.assign(new Error("ENOENT"), { code: "ENOENT" });
};

function runFile(port: number, pid = 4242): string {
	return JSON.stringify({ pid, port, startedAt: "2026-01-01T00:00:00Z" });
}

/** Serves fixed contents per path and records which paths were consulted. */
function deps(
	files: Record<string, string | (() => never)>,
	overrides: { env?: Record<string, string | undefined>; alive?: boolean } = {},
) {
	const seen: string[] = [];
	return {
		seen,
		opts: {
			env: overrides.env ?? {},
			homeDir: HOME,
			platform: "linux" as NodeJS.Platform,
			joinPath: path.join,
			isPidLive: () => overrides.alive ?? true,
			readRunFile: (p: string) => {
				seen.push(p);
				const entry = files[p];
				if (entry === undefined) return ENOENT();
				return typeof entry === "function" ? entry() : entry;
			},
		},
	};
}

describe("resolveDevApiTarget", () => {
	it("prefers an explicit AO_DEV_API_TARGET over every other source", () => {
		const { opts, seen } = deps(
			{ [DEV_RUN_FILE]: runFile(3002) },
			{ env: { AO_DEV_API_TARGET: "http://127.0.0.1:9999", AO_PORT: "3005" } },
		);
		expect(resolveDevApiTarget(opts)).toBe("http://127.0.0.1:9999");
		// Short-circuits: a developer pointing at a remote or containerised daemon
		// must not be overridden by a local run file.
		expect(seen).toEqual([]);
	});

	it("trims a padded override and ignores a blank one", () => {
		expect(
			resolveDevApiTarget(deps({}, { env: { AO_DEV_API_TARGET: "  http://h:1  " } }).opts),
		).toBe("http://h:1");
		expect(resolveDevApiTarget(deps({}, { env: { AO_DEV_API_TARGET: "   " } }).opts)).toBe(
			DEFAULT_TARGET,
		);
	});

	// The regression this module exists for: `npm run dev` puts the daemon on a
	// dev-only port, and targeting the standalone default instead makes the proxy
	// answer 502 for /api/v1/* while that daemon answers 200 directly (#4324).
	it("uses the port recorded by the Electron dev supervisor", () => {
		const { opts, seen } = deps({ [DEV_RUN_FILE]: runFile(3002) });
		expect(resolveDevApiTarget(opts)).toBe("http://127.0.0.1:3002");
		expect(seen[0]).toBe(DEV_RUN_FILE);
	});

	// AO agent sessions and isolated checkouts export AO_RUN_FILE, so the daemon
	// records its port somewhere else entirely; reading only the dev path would
	// silently fall back to the default and reintroduce the 502.
	it("honours AO_RUN_FILE ahead of the conventional paths", () => {
		const custom = "/tmp/iso/running.json";
		const { opts, seen } = deps(
			{ [custom]: runFile(4100), [DEV_RUN_FILE]: runFile(3002) },
			{ env: { AO_RUN_FILE: custom } },
		);
		expect(resolveDevApiTarget(opts)).toBe("http://127.0.0.1:4100");
		expect(seen).toEqual([custom]);
	});

	// docs/development.md's CLI-only workflow: `dev:web` starts no daemon, so it
	// is paired with a standalone `ao start`, which writes ~/.ao/running.json
	// rather than the dev-scoped file.
	it("reads the standalone run file first under dev:web", () => {
		const { opts, seen } = deps(
			{ [STANDALONE_RUN_FILE]: runFile(3009) },
			{ env: { VITE_NO_ELECTRON: "1" } },
		);
		expect(resolveDevApiTarget(opts)).toBe("http://127.0.0.1:3009");
		// Stops there: a live standalone daemon is the documented pairing, so the
		// dev file is never consulted.
		expect(seen).toEqual([STANDALONE_RUN_FILE]);
	});

	// dev:web has no daemon of its own, so attaching to a `npm run dev` instance
	// is the only thing left to do rather than a cross-profile mistake.
	it("still finds the dev daemon under dev:web when no standalone one is running", () => {
		const { opts } = deps(
			{ [DEV_RUN_FILE]: runFile(3002) },
			{ env: { VITE_NO_ELECTRON: "1" } },
		);
		expect(resolveDevApiTarget(opts)).toBe("http://127.0.0.1:3002");
	});

	// The inverse, and the reason the standalone path is not a blanket fallback:
	// under `npm run dev` the daemon Electron supervises is the only correct
	// target. ~/.ao/running.json belongs to an installed app or a CLI `ao start`
	// serving ~/.ao/data, so proxying there would quietly show the wrong
	// instance's sessions instead of the dev profile's — worse than the 502.
	it("ignores a live standalone daemon under `npm run dev`", () => {
		const { opts, seen } = deps({ [STANDALONE_RUN_FILE]: runFile(3009) });
		expect(resolveDevApiTarget(opts)).toBe(DEFAULT_TARGET);
		expect(seen).toEqual([DEV_RUN_FILE]);
	});

	// running.json is removed only on graceful shutdown, so a hard-killed dev run
	// leaves one behind. Trusting it would point the proxy at a dead port — the
	// same 502, with the ports swapped.
	it("skips a run file whose process is gone and keeps looking", () => {
		const { opts } = deps(
			{ [DEV_RUN_FILE]: runFile(3002, 999999), [STANDALONE_RUN_FILE]: runFile(3009, 4242) },
			{ alive: false, env: { VITE_NO_ELECTRON: "1" } },
		);
		expect(resolveDevApiTarget(opts)).toBe(`http://127.0.0.1:${DEFAULT_DAEMON_PORT}`);
	});

	it("uses a live run file even when an earlier candidate is stale", () => {
		let call = 0;
		const opts = {
			...deps(
				{ [DEV_RUN_FILE]: runFile(3002, 111), [STANDALONE_RUN_FILE]: runFile(3009, 222) },
				{ env: { VITE_NO_ELECTRON: "1" } },
			).opts,
			// first candidate dead, second alive
			isPidLive: () => ++call > 1,
		};
		expect(resolveDevApiTarget(opts)).toBe("http://127.0.0.1:3002");
	});

	it("honours AO_PORT when no run file yields a live daemon", () => {
		expect(resolveDevApiTarget(deps({}, { env: { AO_PORT: "3005" } }).opts)).toBe(
			"http://127.0.0.1:3005",
		);
	});

	it("falls back to the daemon default when nothing is recorded", () => {
		expect(resolveDevApiTarget(deps({}).opts)).toBe(DEFAULT_TARGET);
	});

	it.each([
		["corrupt JSON", "{"],
		["null payload", "null"],
		["missing port", '{"pid":1}'],
		["string port", '{"pid":1,"port":"3002"}'],
		["zero", '{"pid":1,"port":0}'],
		["above the port range", '{"pid":1,"port":70000}'],
	])("ignores an unusable run file (%s) instead of throwing", (_name, contents) => {
		expect(resolveDevApiTarget(deps({ [DEV_RUN_FILE]: contents }).opts)).toBe(DEFAULT_TARGET);
	});
});
