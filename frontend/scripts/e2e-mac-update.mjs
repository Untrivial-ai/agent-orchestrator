// End-to-end macOS auto-update harness (#3288 workstream 0).
//
// Does what a real user does: takes an already-installed version N-1 bundle,
// points it at the real published feed, and proves that version N downloads,
// STAGES, installs on quit, and that the relaunched app both reports version N
// and is actually alive.
//
// Dependency-free ESM (mirrors feed.mjs / nightly-version.mjs) so CI runs
// `node scripts/e2e-mac-update.mjs ...` directly and vitest unit-tests the pure
// argument parsing. macOS only: the whole point is Squirrel.Mac's ShipIt swap.
//
// Four things about this flow are counter-intuitive and were verified against
// the published electron-updater@6.8.9 tarball. Do not "simplify" them:
//
//  1. The app signals staging through AO_E2E_UPDATE_SENTINEL, which hangs off
//     the NATIVE updater's update-downloaded event (see auto-updater.ts).
//     electron-updater's own update-downloaded fires BEFORE Squirrel is told to
//     fetch, so keying on it stages nothing and the test flaps.
//  2. A plain macOS quit INSTALLS but does NOT relaunch. Relaunch only happens
//     through electron-updater's quitAndInstall(). So this harness quits the
//     app, waits out the ShipIt swap, and relaunches the app itself.
//  3. There is no completion signal for the ShipIt swap, so the wait is a
//     bounded poll on the installed bundle's Info.plist version, never a fixed
//     sleep.
//  4. Liveness is the daemon's running.json + loopback /healthz, the same
//     check backend/internal/cli/e2e_test.go uses. "A process exists" is not
//     proof the app came up.
//  5. Both launches spawn the bundle's executable DIRECTLY rather than going
//     through `open`, because the harness has to hand the app AO_RUN_FILE,
//     AO_DATA_DIR and the sentinel path. Measured on macOS 27.0: a direct spawn
//     propagates all three; `open -a` propagated only what happened to be in the
//     caller's ambient environment; `open --env VAR=value` works but is
//     undocumented in `open`'s usage output on older releases, so relying on it
//     would make the harness depend on the runner image's macOS version.
//
// usage:
//   node scripts/e2e-mac-update.mjs --app "/Applications/Agent Orchestrator.app" \
//     --expect-version 0.10.4 [--state-dir ~/.ao] [--channel latest|nightly]
import { spawn, execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { basename, dirname, isAbsolute, join } from "node:path";

const DEFAULTS = {
	channel: "latest",
	// A full mac zip is a few hundred MB; be generous but bounded.
	downloadTimeoutMs: 20 * 60 * 1000,
	// ShipIt's ditto swap of an unpacked bundle.
	swapTimeoutMs: 5 * 60 * 1000,
	// Cold start plus daemon boot.
	launchTimeoutMs: 3 * 60 * 1000,
	pollIntervalMs: 2000,
};

class UsageError extends Error {}

// parseArgs turns argv into harness options. Pure, so the flag contract is unit
// tested without launching anything. Throws UsageError on misuse (exit 2).
export function parseArgs(argv) {
	const opts = { ...DEFAULTS };
	for (let i = 0; i < argv.length; i += 1) {
		const flag = argv[i];
		const value = argv[i + 1];
		const needsValue = () => {
			if (value === undefined || value.startsWith("--")) throw new UsageError(`${flag} needs a value`);
			i += 1;
			return value;
		};
		switch (flag) {
			case "--app":
				opts.app = needsValue();
				break;
			case "--expect-version":
				opts.expectVersion = needsValue();
				break;
			case "--state-dir":
				opts.stateDir = needsValue();
				break;
			case "--run-file":
				opts.runFile = needsValue();
				break;
			case "--channel":
				opts.channel = needsValue();
				if (opts.channel !== "latest" && opts.channel !== "nightly") {
					throw new UsageError(`--channel must be latest or nightly, got ${opts.channel}`);
				}
				break;
			case "--download-timeout":
				opts.downloadTimeoutMs = positiveSeconds(flag, needsValue());
				break;
			case "--swap-timeout":
				opts.swapTimeoutMs = positiveSeconds(flag, needsValue());
				break;
			case "--launch-timeout":
				opts.launchTimeoutMs = positiveSeconds(flag, needsValue());
				break;
			default:
				throw new UsageError(`unknown flag: ${flag}`);
		}
	}
	if (!opts.app) throw new UsageError("--app is required");
	if (!opts.expectVersion) throw new UsageError("--expect-version is required");
	if (!opts.app.endsWith(".app")) throw new UsageError(`--app must point at a .app bundle, got ${opts.app}`);
	opts.stateDir ??= join(homedir(), ".ao");
	// The run file is deleted before each launch so the liveness poll cannot pass
	// on a stale file. It must be absolute; removeRunFile validates any existing
	// target as an AO run-file handshake before deletion.
	if (opts.runFile !== undefined) {
		if (!isAbsolute(opts.runFile)) throw new UsageError(`--run-file must be an absolute path, got ${opts.runFile}`);
	}
	if (!isAbsolute(opts.stateDir)) throw new UsageError(`--state-dir must be an absolute path, got ${opts.stateDir}`);
	opts.runFile ??= join(opts.stateDir, "running.json");
	// Where the APP looks for update-settings.json. Not a free choice: main.ts's
	// initAutoUpdates does `stateDir = path.dirname(runFilePath())` and hands that
	// to ensureUpdatePrefs/startAutoUpdates, and runFilePath() returns AO_RUN_FILE
	// when set. Seeding anywhere else leaves the first-run dialog showing and the
	// whole flow blocks. Equal to --state-dir unless --run-file moves it.
	opts.settingsDir = dirname(opts.runFile);
	// Durable daemon state. Mirrors backend/internal/config's resolveDataDir
	// default (<ao home>/data) so an overridden --state-dir keeps the daemon's
	// SQLite out of the real ~/.ao.
	opts.dataDir = join(opts.stateDir, "data");
	opts.appName = basename(opts.app, ".app");
	return opts;
}

// launchEnv is the environment BOTH launches hand the app. Exported so the
// contract is testable: every one of these has to reach the app, and the
// original harness passed only the sentinel, so --state-dir and --run-file were
// seeded and polled by the harness at paths the app never used.
export function launchEnv(opts, sentinel, baseEnv = process.env) {
	return {
		...baseEnv,
		AO_E2E_UPDATE_SENTINEL: sentinel,
		AO_RUN_FILE: opts.runFile,
		AO_DATA_DIR: opts.dataDir,
	};
}

// The env var the app reads to enable the staging sentinel. Kept in sync with
// E2E_UPDATE_SENTINEL_ENV in src/main/auto-updater.ts.
const SENTINEL_ENV = "AO_E2E_UPDATE_SENTINEL";

/**
 * assertSentinelCapable fails fast when the baseline bundle predates the
 * sentinel listener.
 *
 * The listener landed in #3294. Every release published before it ignores
 * AO_E2E_UPDATE_SENTINEL entirely, so the harness would wait out the full
 * download timeout (20 minutes by default) and report a timeout that looks like
 * a broken update but is really an incapable baseline. A 20 minute silent wait
 * is not an acceptable failure mode, so probe the packaged bundle for the env
 * var name before launching anything.
 *
 * The probe is a substring scan of app.asar rather than anything cleverer
 * because the name survives bundling and minification as a plain string literal
 * (auto-updater.ts reads it as process.env[E2E_UPDATE_SENTINEL_ENV]).
 */
export function assertSentinelCapable(appPath) {
	const asar = join(appPath, "Contents", "Resources", "app.asar");
	if (!existsSync(asar)) {
		throw new UsageError(`cannot check sentinel support: no ${asar}. Is ${appPath} a packaged build?`);
	}
	if (!readFileSync(asar).includes(SENTINEL_ENV)) {
		throw new UsageError(
			`baseline at ${appPath} has no ${SENTINEL_ENV} listener, so it can never signal that an update staged. ` +
				`Running anyway would just wait out the download timeout and report a false failure. Two different ` +
				`causes look identical here, so check both: the baseline may predate the listener (#3288 workstream 0), ` +
				`or the listener may have been removed from src/main/auto-updater.ts, in which case EVERY baseline ` +
				`built after that point fails this check and picking a newer one makes it worse. That second case is ` +
				`real: #3012 deleted it on 2026-07-31 and run 30758412650 failed here on a 2026-08-01 baseline while ` +
				`a 2026-07-30 baseline passed. Confirm the listener still exists in the app before changing baselines.`,
		);
	}
}

function positiveSeconds(flag, raw) {
	const seconds = Number(raw);
	if (!Number.isFinite(seconds) || seconds <= 0) throw new UsageError(`${flag} must be a positive number of seconds`);
	return seconds * 1000;
}

// bundleVersion reads CFBundleShortVersionString straight off the installed
// bundle, so it observes what ShipIt actually swapped in rather than trusting
// anything the app reports about itself.
export function bundleVersion(appPath) {
	const plist = join(appPath, "Contents", "Info.plist");
	return execFileSync("plutil", ["-extract", "CFBundleShortVersionString", "raw", "-o", "-", plist], {
		encoding: "utf8",
	}).trim();
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// waitFor polls until check() returns truthy or the budget runs out. Bounded
// polling, never a fixed sleep: the ShipIt swap has no completion signal and
// its duration varies with bundle size and disk speed.
async function waitFor(label, timeoutMs, check) {
	const deadline = Date.now() + timeoutMs;
	for (;;) {
		let result;
		try {
			result = await check();
		} catch {
			result = false;
		}
		if (result) return result;
		if (Date.now() >= deadline) throw new Error(`timed out after ${Math.round(timeoutMs / 1000)}s waiting for ${label}`);
		await sleep(DEFAULTS.pollIntervalMs);
	}
}

// seedUpdateSettings writes update-settings.json before first launch.
// ensureUpdatePrefs only shows its first-run dialog when no settings file
// exists, so pre-seeding is what makes the whole flow non-interactive. Shape
// must match UpdateSettings in src/main/update-settings.ts.
//
// settingsDir must be opts.settingsDir, i.e. dirname(AO_RUN_FILE), because that
// is the directory the app derives for itself (see parseArgs). Exported so a
// test can assert it lands where the app will actually look.
export function seedUpdateSettings(settingsDir, channel) {
	mkdirSync(settingsDir, { recursive: true });
	const settings = { enabled: true, channel, nightlyAck: channel === "nightly", feature: null };
	writeFileSync(join(settingsDir, "update-settings.json"), `${JSON.stringify(settings, null, 2)}\n`, { mode: 0o600 });
}

// removeRunFile clears a stale daemon handshake without treating an arbitrary
// JSON file as disposable harness state. The daemon always writes pid, port and
// startedAt; require that complete shape whenever a target already exists.
export function removeRunFile(runFile) {
	if (!existsSync(runFile)) return;
	let info;
	try {
		info = JSON.parse(readFileSync(runFile, "utf8"));
	} catch {
		throw new UsageError(`refusing to remove ${runFile}: existing file is not an AO running.json handshake`);
	}
	if (
		typeof info !== "object" ||
		info === null ||
		!Number.isInteger(info.pid) ||
		info.pid <= 0 ||
		!Number.isInteger(info.port) ||
		info.port < 1 ||
		info.port > 65535 ||
		typeof info.startedAt !== "string" ||
		Number.isNaN(Date.parse(info.startedAt))
	) {
		throw new UsageError(`refusing to remove ${runFile}: existing file is not an AO running.json handshake`);
	}
	rmSync(runFile);
}

function quitApp(appName) {
	try {
		execFileSync("osascript", ["-e", `tell application "${appName}" to quit`], { stdio: "ignore" });
	} catch {
		// Already gone, or never registered with LaunchServices. Either is fine.
	}
}

async function isDaemonAlive(runFile) {
	if (!existsSync(runFile)) return false;
	const { port } = JSON.parse(readFileSync(runFile, "utf8"));
	if (!port) return false;
	// Same liveness contract as backend/internal/cli/e2e_test.go: loopback only.
	const res = await fetch(`http://127.0.0.1:${port}/healthz`, { signal: AbortSignal.timeout(5000) });
	return res.ok;
}

async function run(opts) {
	if (process.platform !== "darwin") throw new UsageError(`macOS only; host is ${process.platform}`);
	if (!existsSync(opts.app)) throw new UsageError(`no such app bundle: ${opts.app}`);

	const startVersion = bundleVersion(opts.app);
	console.log(`installed baseline: ${startVersion}`);
	if (startVersion === opts.expectVersion) {
		throw new UsageError(`baseline is already ${opts.expectVersion}; --expect-version must be the NEW version`);
	}
	// Before anything slow: a baseline with no sentinel listener can only ever
	// time out, and a 20 minute silent wait reads as "the update is broken".
	assertSentinelCapable(opts.app);

	// A stale instance would hold the daemon port and confuse the liveness check.
	quitApp(opts.appName);

	const sentinel = join(tmpdir(), `ao-e2e-update-sentinel-${process.pid}.json`);
	rmSync(sentinel, { force: true });
	removeRunFile(opts.runFile);
	seedUpdateSettings(opts.settingsDir, opts.channel);

	// Launched as a child process rather than via `open` so the sentinel and the
	// state-dir overrides all reach the app (header note 5). The relaunch below
	// does the same, re-reading the executable name from the swapped bundle.
	const env = launchEnv(opts, sentinel);
	console.log(`launching ${opts.app} (channel: ${opts.channel}, run file: ${opts.runFile})`);
	const child = spawn(join(opts.app, "Contents", "MacOS", readExecutableName(opts.app)), [], {
		env,
		stdio: "inherit",
		detached: false,
	});
	const exited = new Promise((resolve) => child.once("exit", resolve));

	try {
		await waitFor("the native updater to stage the update", opts.downloadTimeoutMs, () => existsSync(sentinel));
		console.log(`update staged: ${readFileSync(sentinel, "utf8").trim()}`);
	} finally {
		// Even on failure, do not leave a packaged app running on the runner.
		quitApp(opts.appName);
	}

	// Squirrel installs the staged update during termination. It does NOT
	// relaunch (see the header note), so this quit is the install step only.
	await Promise.race([exited, sleep(60_000)]);

	await waitFor(`the ShipIt swap to land ${opts.expectVersion}`, opts.swapTimeoutMs, () => {
		const current = bundleVersion(opts.app);
		if (current !== startVersion) console.log(`bundle version now: ${current}`);
		return current === opts.expectVersion;
	});
	console.log(`installed bundle is now ${opts.expectVersion}`);

	console.log("relaunching the updated app");
	removeRunFile(opts.runFile);
	// Same direct spawn as the first launch: the relaunched app must get the same
	// AO_RUN_FILE/AO_DATA_DIR, or the liveness poll below watches a run file the
	// app never writes. The executable name is re-read from the SWAPPED bundle,
	// so a rename between N-1 and N is picked up. Detached and with stdio
	// ignored, so this harness can exit without waiting on the app.
	spawn(join(opts.app, "Contents", "MacOS", readExecutableName(opts.app)), [], {
		env,
		stdio: "ignore",
		detached: true,
	}).unref();

	await waitFor("the relaunched app's daemon to answer /healthz", opts.launchTimeoutMs, () =>
		isDaemonAlive(opts.runFile),
	);

	// Re-read after relaunch: a bundle that reverted mid-launch would still have
	// passed the swap poll above.
	const finalVersion = bundleVersion(opts.app);
	if (finalVersion !== opts.expectVersion) {
		throw new Error(`relaunched bundle reports ${finalVersion}, expected ${opts.expectVersion}`);
	}

	quitApp(opts.appName);
	rmSync(sentinel, { force: true });
	console.log(`PASS: ${startVersion} updated to ${finalVersion}, relaunched, daemon alive`);
}

// readExecutableName pulls CFBundleExecutable out of the bundle instead of
// hardcoding "agent-orchestrator", so a rename of packagerConfig.executableName
// does not silently break the harness.
function readExecutableName(appPath) {
	const plist = join(appPath, "Contents", "Info.plist");
	return execFileSync("plutil", ["-extract", "CFBundleExecutable", "raw", "-o", "-", plist], {
		encoding: "utf8",
	}).trim();
}

if (import.meta.url === `file://${process.argv[1]}`) {
	let opts;
	try {
		opts = parseArgs(process.argv.slice(2));
	} catch (err) {
		process.stderr.write(`${err.message}\n`);
		process.stderr.write("usage: node e2e-mac-update.mjs --app <bundle.app> --expect-version <version>\n");
		process.exit(2);
	}
	run(opts).catch((err) => {
		process.stderr.write(`${err.stack || err}\n`);
		process.exit(err instanceof UsageError ? 2 : 1);
	});
}
