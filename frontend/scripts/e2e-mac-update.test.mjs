import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";
import { assertSentinelCapable, launchEnv, parseArgs, removeRunFile, seedUpdateSettings } from "./e2e-mac-update.mjs";

// The harness itself needs a real macOS runner, a real signed N-1 install and a
// real published N feed, so it cannot run here. What IS testable anywhere is the
// flag contract: a typo in the CI job's arguments should fail loudly at parse
// time rather than half-run an update test and report a confusing timeout.

describe("e2e-mac-update parseArgs", () => {
	const required = ["--app", "/Applications/Agent Orchestrator.app", "--expect-version", "0.10.4"];

	it("parses the required flags and defaults the state dir to ~/.ao", () => {
		const opts = parseArgs(required);
		expect(opts.app).toBe("/Applications/Agent Orchestrator.app");
		expect(opts.expectVersion).toBe("0.10.4");
		expect(opts.appName).toBe("Agent Orchestrator");
		// All app state lives under ~/.ao only (see AGENTS.md hard rules).
		expect(opts.stateDir).toBe(join(homedir(), ".ao"));
		expect(opts.runFile).toBe(join(homedir(), ".ao", "running.json"));
		expect(opts.channel).toBe("latest");
	});

	it("requires --app and --expect-version", () => {
		expect(() => parseArgs([])).toThrow(/--app is required/);
		expect(() => parseArgs(["--app", "/x/Foo.app"])).toThrow(/--expect-version is required/);
	});

	it("rejects an --app path that is not a bundle", () => {
		expect(() => parseArgs(["--app", "/Applications/Foo", "--expect-version", "1.0.0"])).toThrow(/\.app bundle/);
	});

	it("rejects an unknown channel", () => {
		expect(() => parseArgs([...required, "--channel", "beta"])).toThrow(/latest or nightly/);
	});

	it("accepts the nightly channel", () => {
		expect(parseArgs([...required, "--channel", "nightly"]).channel).toBe("nightly");
	});

	it("rejects a flag with a missing value", () => {
		expect(() => parseArgs(["--app", "--expect-version", "0.10.4"])).toThrow(/--app needs a value/);
	});

	it("rejects unknown flags", () => {
		expect(() => parseArgs([...required, "--turbo"])).toThrow(/unknown flag: --turbo/);
	});

	it("converts timeout flags from seconds to milliseconds and rejects nonpositive values", () => {
		expect(parseArgs([...required, "--swap-timeout", "90"]).swapTimeoutMs).toBe(90_000);
		expect(() => parseArgs([...required, "--swap-timeout", "0"])).toThrow(/positive number of seconds/);
		expect(() => parseArgs([...required, "--download-timeout", "soon"])).toThrow(/positive number of seconds/);
	});

	it("allows overriding the state dir and run file", () => {
		const opts = parseArgs([...required, "--state-dir", "/tmp/ao-e2e", "--run-file", "/tmp/ao-e2e/run.json"]);
		expect(opts.stateDir).toBe("/tmp/ao-e2e");
		expect(opts.runFile).toBe("/tmp/ao-e2e/run.json");
	});

	// The app does `stateDir = path.dirname(runFilePath())` and reads
	// update-settings.json from there (main.ts initAutoUpdates), so the settings
	// directory follows --run-file, not --state-dir, when the two diverge.
	it("derives the settings dir from the run file, which is what the app reads", () => {
		expect(parseArgs(required).settingsDir).toBe(join(homedir(), ".ao"));
		const moved = parseArgs([...required, "--state-dir", "/tmp/ao-e2e", "--run-file", "/tmp/elsewhere/run.json"]);
		expect(moved.settingsDir).toBe("/tmp/elsewhere");
	});

	// Mirrors backend/internal/config resolveDataDir's default of <ao home>/data,
	// so an overridden --state-dir keeps the daemon's SQLite out of the real ~/.ao.
	it("derives the daemon data dir under the state dir", () => {
		expect(parseArgs([...required, "--state-dir", "/tmp/ao-e2e"]).dataDir).toBe(join("/tmp/ao-e2e", "data"));
	});

	it("constrains --run-file to an absolute path", () => {
		expect(() => parseArgs([...required, "--run-file", "run.json"])).toThrow(/absolute path/);
		expect(() => parseArgs([...required, "--state-dir", "relative/dir"])).toThrow(/absolute path/);
	});

	it("refuses to delete an unrelated absolute JSON file passed as --run-file", () => {
		const dir = mkdtempSync(join(tmpdir(), "ao-e2e-run-file-"));
		const unrelated = join(dir, "package.json");
		writeFileSync(unrelated, '{"name":"not-a-run-file"}\n');
		try {
			expect(() => removeRunFile(unrelated)).toThrow(/not an AO running\.json handshake/);
			expect(readFileSync(unrelated, "utf8")).toContain("not-a-run-file");
		} finally {
			rmSync(dir, { recursive: true, force: true });
		}
	});
});

// The bug these cover: --state-dir and --run-file were parsed and used by the
// harness, but neither launch passed AO_DATA_DIR or AO_RUN_FILE to the app. The
// harness seeded and polled paths the app never used, so it could only time out.
describe("e2e-mac-update launch environment", () => {
	const opts = parseArgs([
		"--app",
		"/Applications/Agent Orchestrator.app",
		"--expect-version",
		"0.10.4",
		"--state-dir",
		"/tmp/ao-e2e",
	]);

	it("hands the app every override the harness itself relies on", () => {
		const env = launchEnv(opts, "/tmp/sentinel.json", { PATH: "/usr/bin" });
		expect(env.AO_E2E_UPDATE_SENTINEL).toBe("/tmp/sentinel.json");
		expect(env.AO_RUN_FILE).toBe(opts.runFile);
		expect(env.AO_DATA_DIR).toBe(opts.dataDir);
		// Still an inherited environment, not a replacement one.
		expect(env.PATH).toBe("/usr/bin");
	});

	it("seeds update settings where the app will look for them", () => {
		const dir = mkdtempSync(join(tmpdir(), "ao-e2e-settings-"));
		try {
			const moved = parseArgs([
				"--app",
				"/Applications/Agent Orchestrator.app",
				"--expect-version",
				"0.10.4",
				"--run-file",
				join(dir, "nested", "running.json"),
			]);
			seedUpdateSettings(moved.settingsDir, "nightly");
			const written = JSON.parse(readFileSync(join(dir, "nested", "update-settings.json"), "utf8"));
			// Shape must match UpdateSettings in src/main/update-settings.ts, and
			// enabled:true is what makes startAutoUpdates run without a dialog.
			expect(written).toEqual({ enabled: true, channel: "nightly", nightlyAck: true, feature: null });
		} finally {
			rmSync(dir, { recursive: true, force: true });
		}
	});
});

// A baseline published before the AO_E2E_UPDATE_SENTINEL listener existed
// ignores the env var, so the harness could only ever burn its full download
// timeout and report a "never staged" failure that looks like a broken update.
describe("e2e-mac-update assertSentinelCapable", () => {
	let dir;
	const bundle = (name, asarContents) => {
		const app = join(dir, `${name}.app`);
		mkdirSync(join(app, "Contents", "Resources"), { recursive: true });
		if (asarContents !== null) writeFileSync(join(app, "Contents", "Resources", "app.asar"), asarContents);
		return app;
	};

	beforeAll(() => {
		dir = mkdtempSync(join(tmpdir(), "ao-e2e-baseline-"));
	});
	afterAll(() => {
		rmSync(dir, { recursive: true, force: true });
	});

	it("accepts a bundle whose app.asar carries the sentinel env var", () => {
		expect(() => assertSentinelCapable(bundle("Capable", 'process.env["AO_E2E_UPDATE_SENTINEL"]'))).not.toThrow();
	});

	it("fails fast, naming the cause, on a baseline that predates the listener", () => {
		expect(() => assertSentinelCapable(bundle("Old", "some older bundle without it"))).toThrow(
			/no AO_E2E_UPDATE_SENTINEL listener/,
		);
	});

	it("fails when the path is not a packaged bundle at all", () => {
		expect(() => assertSentinelCapable(bundle("Unpackaged", null))).toThrow(/packaged build/);
	});
});

// assertSentinelCapable is a substring scan of a BUILT app.asar, so it can only
// fail after a release is cut — and it fails by refusing to run, which reads as
// "old baseline" rather than "the app stopped emitting the signal". That is
// exactly how the listener stayed missing from #3012 until #4254: five weeks in
// which no build could satisfy the harness and the macOS update-hop job could
// never run against any baseline.
//
// This asserts the coupling at its source instead, so deleting the listener
// fails a unit test in the same commit rather than silently disabling a job.
describe("update-hop coverage is wired to the app", () => {
	it("keeps the sentinel listener the harness scans for in auto-updater.ts", () => {
		// Resolved from the runner's cwd rather than import.meta.url: the test
		// transform rewrites import.meta.url to a non-file scheme. Both
		// candidates are listed so this works whether vitest is invoked from
		// frontend/ (the npm script) or from the repo root.
		const sourcePath = ["src/main/auto-updater.ts", "frontend/src/main/auto-updater.ts"]
			.map((rel) => join(process.cwd(), rel))
			.find((candidate) => existsSync(candidate));
		expect(sourcePath, "could not locate auto-updater.ts from " + process.cwd()).toBeDefined();
		const source = readFileSync(sourcePath, "utf8");
		expect(source).toContain('E2E_UPDATE_SENTINEL_ENV = "AO_E2E_UPDATE_SENTINEL"');
		// The native updater is the only signal that means "staged, will swap on
		// quit"; electron-updater's own update-downloaded fires before Squirrel
		// has fetched anything, so hanging the sentinel there stages nothing.
		expect(source).toMatch(/nativeAutoUpdater\.on\("update-downloaded"/);
	});
});
