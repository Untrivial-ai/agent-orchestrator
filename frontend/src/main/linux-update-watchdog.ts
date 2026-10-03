import { spawn } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { readFile, rm } from "node:fs/promises";
import path from "node:path";

// Linux update-relaunch watchdog.
//
// electron-updater's AppImageUpdater swaps the AppImage in place and relaunches
// it as a detached, unobserved process; the app quits regardless of whether the
// new build ever comes up. A failed swap (unwritable directory) or a failed or
// stillborn relaunch leaves no AO process at all and no error anywhere (#5575).
//
// macOS closes this gap with a native helper that watches the install to
// completion. Linux gets this detached Node watcher instead: it is armed right
// before the app hands off to the installer (explicit path) or at quit time
// (install-on-quit path), waits for the old process to die, then confirms the
// new build came up, relaunches the AppImage itself when nothing does, and on
// total failure retires the update state so the next launch does not inherit a
// version that was never reached.
//
// The watcher ships as a self-contained CommonJS script embedded below. It is
// written to the state dir at arm time and spawned with `process.execPath
// <script>` under ELECTRON_RUN_AS_NODE=1, so in the packaged AppImage it runs
// as plain Node with no window and no GUI session dependency. The script uses
// only Node built-ins.

export const LINUX_UPDATE_WATCHDOG_FILE = "linux-update-watchdog.cjs";
export const RELAUNCH_FAILURE_FILE = "relaunch-failure.json";
const RESTART_DIR = "update-restart";
const DEFAULT_POLL_MS = 1500;
const DEFAULT_GRACE_MS = 45_000;
const DEFAULT_HARD_TIMEOUT_MS = 15 * 60_000;
const MAX_RESPAWN_ATTEMPTS = 2;

export const LINUX_UPDATE_WATCHDOG_SCRIPT = `"use strict";
// Linux update-relaunch watchdog. Spawned detached by the app (via
// ELECTRON_RUN_AS_NODE) just before it quits to install an update. Waits for
// the old process to die, confirms the next build came up, relaunches the
// AppImage when nothing does, and on total failure retires the update state and
// records a marker the next boot surfaces (issue #5575).
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");

function readArg(name, fallback) {
  const prefix = name + "=";
  for (const raw of process.argv.slice(2)) {
    if (raw.indexOf(prefix) === 0) return raw.slice(prefix.length);
  }
  return fallback;
}
function readNumArg(name, fallback) {
  const parsed = Number(readArg(name, ""));
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

const stateDir = readArg("--state-dir", "");
const appImagePath = readArg("--appimage", "");
const version = readArg("--version", "");
const parentPid = readNumArg("--parent-pid", 0);
// The explicit quitAndInstall path writes the relaunch marker before arming;
// the install-on-quit path does not. When the marker was tracked, its
// disappearance (the new build consumed it at boot) is a success signal.
const flagTracked = readArg("--has-flag", "1") === "1";
const pollMs = readNumArg("--poll-ms", 1500);
const graceMs = readNumArg("--grace-ms", 45000);
const hardTimeoutMs = readNumArg("--hard-timeout-ms", 900000);
const maxRespawnAttempts = readNumArg("--max-respawn-attempts", 2);

const restartDir = path.join(stateDir, "update-restart");
const flagFile = path.join(restartDir, "relaunch-flag.json");
const stagedFile = path.join(stateDir, "staged-update.json");
const failureFile = path.join(restartDir, "relaunch-failure.json");

// Mis-armed (missing required args): do nothing. The arm site knows whether an
// install is actually happening; staying silent is the fail-open choice.
if (!stateDir || !appImagePath || !parentPid) process.exit(0);

function pidAlive(pid) {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch (err) {
    return Boolean(err && err.code === "EPERM");
  }
}

// AppImage files to watch for and to choose a respawn target from: the path the
// app was running from, plus any sibling .AppImage in the same directory (the
// updater may have renamed the downloaded file into place).
function candidatePaths() {
  const out = [appImagePath];
  try {
    const dir = path.dirname(appImagePath);
    for (const name of fs.readdirSync(dir)) {
      if (name.toLowerCase().indexOf(".appimage") === -1) continue;
      const full = path.join(dir, name);
      if (out.indexOf(full) === -1) out.push(full);
    }
  } catch {
    // The directory may already be gone; the primary candidate still applies.
  }
  return out;
}

// AppImage-launched binaries keep the outer .AppImage file path in argv[0] (the
// random FUSE mount path is the inner binary), so a live process whose cmdline
// references one of the candidates is the next build coming up.
function anyCandidateRunning() {
  let entries;
  try {
    entries = fs.readdirSync("/proc");
  } catch {
    return false;
  }
  for (const candidate of candidatePaths()) {
    for (const entry of entries) {
      if (!/^[0-9]+$/.test(entry)) continue;
      const pid = Number(entry);
      if (pid === process.pid || pid === parentPid) continue;
      let cmdline = "";
      try {
        cmdline = fs.readFileSync("/proc/" + entry + "/cmdline", "utf8");
      } catch {
        continue;
      }
      if (cmdline.indexOf(candidate) !== -1) return true;
    }
  }
  return false;
}

function relaunchSeen() {
  // A running build wins over every other signal: a rescue respawns the OLD
  // version when the swap failed, and that build consumes the marker but
  // reports a different version, so "marker gone" alone would be misleading.
  if (anyCandidateRunning()) return true;
  // The new build consumed the relaunch marker at boot (update-relaunch-flag.ts
  // deletes it on read, then validates it against its own version).
  if (flagTracked && !fs.existsSync(flagFile)) return true;
  return false;
}

function bestRespawnTarget() {
  const matches = candidatePaths()
    .map((candidate) => {
      try {
        return { candidate, mtime: fs.statSync(candidate).mtimeMs };
      } catch {
        return undefined;
      }
    })
    .filter(Boolean)
    .sort((a, b) => b.mtime - a.mtime);
  return matches.length > 0 ? matches[0].candidate : undefined;
}

function writeFailure(reason) {
  try {
    fs.mkdirSync(restartDir, { recursive: true, mode: 0o700 });
    fs.writeFileSync(
      failureFile,
      JSON.stringify({
        version: version !== "" ? version : undefined,
        error:
          "The app closed to install " +
          (version !== "" ? "update " + version : "an update") +
          " but the new build did not start. " +
          reason,
        at: new Date().toISOString()
      }),
      { mode: 0o600 }
    );
  } catch {
    // The failure record is best-effort; the state cleanup below still runs.
  }
  // Retire the update state: a version that never reached the disk must not
  // leak into the next launch as if it had been installed.
  for (const file of [flagFile, stagedFile]) {
    try {
      fs.unlinkSync(file);
    } catch {
      // Already gone is the success case.
    }
  }
  process.exit(1);
}

function respawn() {
  const target = bestRespawnTarget();
  if (!target) {
    writeFailure("No AppImage was found at " + appImagePath + " or next to it to relaunch.");
    return false;
  }
  try {
    const child = spawn(target, [], {
      detached: true,
      stdio: "ignore",
      env: Object.assign({}, process.env)
    });
    child.on("error", function () {
      // Observed via the grace-window re-check, which then retries or fails.
    });
    child.unref();
    return true;
  } catch {
    return false;
  }
}

const startedAt = Date.now();
let respawnAttempts = 0;
function tick() {
  if (pidAlive(parentPid)) {
    // Parent still up: the quit was aborted or the sync install threw and the
    // app keeps running. The app is alive, so no rescue is needed; hard-stop
    // after a long wait so an abandoned watcher never lingers.
    if (Date.now() - startedAt > hardTimeoutMs) process.exit(0);
    setTimeout(tick, pollMs);
    return;
  }
  if (relaunchSeen()) process.exit(0);
  if (respawnAttempts >= maxRespawnAttempts) {
    writeFailure("Giving up after " + respawnAttempts + " relaunch attempts.");
    return;
  }
  respawnAttempts += 1;
  // A failed spawn (target vanished mid-flight) is retried promptly; a
  // successful one gets a full grace window to boot before being judged.
  setTimeout(tick, respawn() ? graceMs : pollMs);
}
tick();
`;

export type UpdateRelaunchFailure = {
  version?: string;
  error: string;
  at?: string;
};

let linuxWatchdogArmed = false;

export function isLinuxUpdateWatchdogArmed(): boolean {
  return linuxWatchdogArmed;
}

// armLinuxUpdateWatchdog writes the embedded watcher script into the state dir
// and spawns it detached, then returns whether the watch is in place. It is a
// no-op (and fails open) when there is nothing to watch: no AppImage path, no
// version, or a script write/spawn that failed. Called at most once per process:
// the first successful arm wins, so the explicit quitAndInstall path and the
// install-on-quit before-quit path cannot double-spawn watchers.
export function armLinuxUpdateWatchdog(options: {
  stateDir: string;
  appImagePath: string | undefined;
  version: string | undefined;
  parentPid?: number;
  flagTracked?: boolean;
  pollMs?: number;
  graceMs?: number;
  hardTimeoutMs?: number;
  maxRespawnAttempts?: number;
}): boolean {
  if (linuxWatchdogArmed) return true;
  const appImage = options.appImagePath;
  const version = options.version;
  if (!options.stateDir || !appImage || !version) return false;
  const restartDir = path.join(options.stateDir, RESTART_DIR);
  const scriptFile = path.join(restartDir, LINUX_UPDATE_WATCHDOG_FILE);
  try {
    mkdirSync(restartDir, { recursive: true, mode: 0o700 });
    writeFileSync(scriptFile, LINUX_UPDATE_WATCHDOG_SCRIPT, { mode: 0o600 });
  } catch (error) {
    console.warn("failed to write Linux update-relaunch watchdog:", error);
    return false;
  }
  const args = [
    "--state-dir=" + options.stateDir,
    "--appimage=" + appImage,
    "--version=" + version,
    "--parent-pid=" + String(options.parentPid ?? process.pid),
    "--has-flag=" + (options.flagTracked === false ? "0" : "1"),
    "--poll-ms=" + String(options.pollMs ?? DEFAULT_POLL_MS),
    "--grace-ms=" + String(options.graceMs ?? DEFAULT_GRACE_MS),
    "--hard-timeout-ms=" + String(options.hardTimeoutMs ?? DEFAULT_HARD_TIMEOUT_MS),
    "--max-respawn-attempts=" + String(options.maxRespawnAttempts ?? MAX_RESPAWN_ATTEMPTS),
  ];
  try {
    const child = spawn(process.execPath, [scriptFile, ...args], {
      detached: true,
      stdio: "ignore",
      env: { ...process.env, ELECTRON_RUN_AS_NODE: "1" },
    });
    child.on("error", (error) => {
      console.warn("Linux update-relaunch watchdog failed to start:", error);
    });
    child.unref();
  } catch (error) {
    console.warn("failed to spawn Linux update-relaunch watchdog:", error);
    return false;
  }
  linuxWatchdogArmed = true;
  return true;
}

// consumeUpdateRelaunchFailure reads and deletes the one-shot marker a failed
// Linux relaunch leaves behind, so the next boot can surface it. Returns the
// recorded failure, or undefined when there is nothing (or only corrupt junk).
export async function consumeUpdateRelaunchFailure(options: {
  stateDir: string;
}): Promise<UpdateRelaunchFailure | undefined> {
  const file = path.join(options.stateDir, RESTART_DIR, RELAUNCH_FAILURE_FILE);
  let contents: string;
  try {
    contents = await readFile(file, "utf8");
  } catch {
    return undefined;
  }
  await rm(file, { force: true }).catch(() => undefined);
  let parsed: unknown;
  try {
    parsed = JSON.parse(contents);
  } catch {
    return undefined;
  }
  if (typeof parsed !== "object" || parsed === null) return undefined;
  const failure = parsed as Partial<UpdateRelaunchFailure>;
  if (typeof failure.error !== "string" || failure.error.length === 0) return undefined;
  return {
    ...(typeof failure.version === "string" ? { version: failure.version } : {}),
    error: failure.error,
    ...(typeof failure.at === "string" ? { at: failure.at } : {}),
  };
}