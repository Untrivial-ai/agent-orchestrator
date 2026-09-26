// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { spawn, type ChildProcess } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import nodePath from "node:path";
import {
  consumeUpdateRelaunchFailure,
  LINUX_UPDATE_WATCHDOG_FILE,
  LINUX_UPDATE_WATCHDOG_SCRIPT,
  RELAUNCH_FAILURE_FILE,
} from "./linux-update-watchdog";

// The watchdog integration tests execute the embedded script as a real child
// process, exactly as the app spawns it (process.execPath + the script args).
// A short-lived `node -e "setTimeout(...)"` child stands in for the "old app"
// whose pid is passed as --parent-pid, and a shell script named *.AppImage
// stands in for the AppImage build, writing a marker line when it boots.

let tmp: string;
let stateDir: string;
let appImageDir: string;

beforeEach(() => {
  tmp = mkdtempSync(nodePath.join(os.tmpdir(), "ao-watchdog-test-"));
  stateDir = nodePath.join(tmp, "state");
  appImageDir = nodePath.join(tmp, "apps");
  mkdirSync(stateDir, { recursive: true });
  mkdirSync(appImageDir, { recursive: true });
});

afterEach(() => {
  rmSync(tmp, { recursive: true, force: true });
});

function writeWatchdogScript(directory: string): string {
  const file = nodePath.join(directory, LINUX_UPDATE_WATCHDOG_FILE);
  writeFileSync(file, LINUX_UPDATE_WATCHDOG_SCRIPT);
  return file;
}

function spawnParent(lifetimeMs: number): ChildProcess {
  return spawn(process.execPath, ["-e", `setTimeout(() => {}, ${lifetimeMs})`], {
    stdio: "ignore",
  });
}

function makeFakeAppImage(markerPath: string): string {
  const file = nodePath.join(appImageDir, "agent-orchestrator.AppImage");
  writeFileSync(
    file,
    "#!/bin/sh\n" +
      `echo booted "$(date +%s%N)" >> "${markerPath}"\n` +
      "sleep 30\n",
  );
  chmodSync(file, 0o755);
  return file;
}

function runWatchdog(
  scriptFile: string,
  args: string[],
  extraEnv: Record<string, string> = {},
): Promise<number | null> {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [scriptFile, ...args], {
      stdio: "ignore",
      env: { ...process.env, ...extraEnv },
    });
    child.on("error", reject);
    child.on("exit", (code) => resolve(code));
  });
}

const FAST = ["--poll-ms=40", "--grace-ms=150", "--hard-timeout-ms=600"];

async function waitFor(predicate: () => boolean, timeoutMs = 3000): Promise<void> {
  const started = Date.now();
  while (!predicate()) {
    if (Date.now() - started > timeoutMs) throw new Error("condition not met in time");
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
}

describe("linux update-relaunch watchdog script", () => {
  it("exits quietly when the old app never quits", async () => {
    const script = writeWatchdogScript(tmp);
    const parent = spawnParent(5000);
    const code = await runWatchdog(script, [
      "--state-dir=" + stateDir,
      "--appimage=" + nodePath.join(appImageDir, "agent-orchestrator.AppImage"),
      "--version=2.0.0",
      "--parent-pid=" + String(parent.pid),
      ...FAST,
    ]);
    expect(code).toBe(0);
    expect(existsSync(nodePath.join(stateDir, "update-restart", RELAUNCH_FAILURE_FILE))).toBe(false);
    parent.kill();
  });

  it("exits 0 when the next build is already running", async () => {
    const script = writeWatchdogScript(tmp);
    const marker = nodePath.join(tmp, "booted.log");
    const appImage = makeFakeAppImage(marker);
    // The "new app" boots before the old process dies, as in quitAndInstall.
    const relaunched = spawn(appImage, [], { stdio: "ignore", env: { ...process.env, RESCUE_MARKER: marker } });
    const parent = spawnParent(120);
    const code = await runWatchdog(script, [
      "--state-dir=" + stateDir,
      "--appimage=" + appImage,
      "--version=2.0.0",
      "--parent-pid=" + String(parent.pid),
      ...FAST,
    ]);
    expect(code).toBe(0);
    relaunched.kill();
    parent.kill();
    // Exactly one boot: the watchdog must not respawn a second instance.
    expect(readFileSync(marker, "utf8").trim().split("\n")).toHaveLength(1);
  });

  it("relaunches the AppImage when nothing comes up", async () => {
    const script = writeWatchdogScript(tmp);
    const marker = nodePath.join(tmp, "booted.log");
    const appImage = makeFakeAppImage(marker);
    const restartDir = nodePath.join(stateDir, "update-restart");
    mkdirSync(restartDir, { recursive: true });
    writeFileSync(nodePath.join(restartDir, "relaunch-flag.json"), JSON.stringify({ version: "2.0.0", fromPID: 99999, startedAt: Date.now() }));
    const parent = spawnParent(120);
    const code = await runWatchdog(script, [
      "--state-dir=" + stateDir,
      "--appimage=" + appImage,
      "--version=2.0.0",
      "--parent-pid=" + String(parent.pid),
      "--poll-ms=40",
      "--grace-ms=150",
      "--hard-timeout-ms=600",
      "--has-flag=1",
    ], { RESCUE_MARKER: marker });
    expect(code).toBe(0);
    parent.kill();
    await waitFor(() => existsSync(marker));
    expect(readFileSync(marker, "utf8").trim().split("\n")).toHaveLength(1);
    expect(existsSync(nodePath.join(stateDir, "update-restart", RELAUNCH_FAILURE_FILE))).toBe(false);
  });

  it("records a failure and retires the update state when no build can start", async () => {
    const script = writeWatchdogScript(tmp);
    const parent = spawnParent(120);
    const restartDir = nodePath.join(stateDir, "update-restart");
    mkdirSync(restartDir, { recursive: true });
    const flagFile = nodePath.join(restartDir, "relaunch-flag.json");
    const stagedFile = nodePath.join(stateDir, "staged-update.json");
    writeFileSync(flagFile, JSON.stringify({ version: "2.0.0", fromPID: parent.pid, startedAt: Date.now() }));
    writeFileSync(stagedFile, JSON.stringify({ version: "2.0.0", stagedAt: Date.now(), channel: "latest" }));
    // No AppImage exists anywhere, so no respawn target can be chosen.
    const code = await runWatchdog(script, [
      "--state-dir=" + stateDir,
      "--appimage=" + nodePath.join(appImageDir, "agent-orchestrator.AppImage"),
      "--version=2.0.0",
      "--parent-pid=" + String(parent.pid),
      "--poll-ms=40",
      "--grace-ms=100",
      "--hard-timeout-ms=600",
      "--max-respawn-attempts=1",
    ]);
    expect(code).toBe(1);
    parent.kill();
    const failure = JSON.parse(readFileSync(nodePath.join(restartDir, RELAUNCH_FAILURE_FILE), "utf8"));
    expect(failure.version).toBe("2.0.0");
    expect(failure.error).toMatch(/did not start/);
    expect(existsSync(stagedFile)).toBe(false);
    expect(existsSync(flagFile)).toBe(false);
  });

  it("counts marker consumption as a successful relaunch", async () => {
    const script = writeWatchdogScript(tmp);
    const marker = nodePath.join(tmp, "booted.log");
    const appImage = makeFakeAppImage(marker);
    const restartDir = nodePath.join(stateDir, "update-restart");
    mkdirSync(restartDir, { recursive: true });
    const flagFile = nodePath.join(restartDir, "relaunch-flag.json");
    writeFileSync(flagFile, JSON.stringify({ version: "2.0.0", fromPID: 12345, startedAt: Date.now() }));
    const parent = spawnParent(300);
    // The new build consumes the marker (deletes it) while the old process is
    // still finishing its quit.
    const consumed = setTimeout(() => rmSync(flagFile, { force: true }), 120);
    const code = await runWatchdog(script, [
      "--state-dir=" + stateDir,
      "--appimage=" + appImage,
      "--version=2.0.0",
      "--parent-pid=" + String(parent.pid),
      ...FAST,
    ]);
    clearTimeout(consumed);
    expect(code).toBe(0);
    parent.kill();
    // The watchdog must not have respawned anything (no second instance).
    expect(existsSync(marker)).toBe(false);
  });
});

describe("armLinuxUpdateWatchdog", () => {
  beforeEach(() => {
    vi.resetModules();
  });

  it("writes the script, spawns the watcher, and arms once", async () => {
    const mod = await import("./linux-update-watchdog");
    const stateDir = nodePath.join(tmp, "arm-state");
    mkdirSync(stateDir, { recursive: true });
    const appImage = nodePath.join(appImageDir, "agent-orchestrator.AppImage");
    writeFileSync(appImage, "#!/bin/sh\n");
    chmodSync(appImage, 0o755);
    const parent = spawnParent(3000);
    const armed = mod.armLinuxUpdateWatchdog({
      stateDir,
      appImagePath: appImage,
      version: "2.0.0",
      parentPid: parent.pid,
      pollMs: 40,
      graceMs: 150,
      hardTimeoutMs: 600,
    });
    expect(armed).toBe(true);
    expect(mod.isLinuxUpdateWatchdogArmed()).toBe(true);
    const scriptFile = nodePath.join(stateDir, "update-restart", LINUX_UPDATE_WATCHDOG_FILE);
    expect(existsSync(scriptFile)).toBe(true);
    expect(readFileSync(scriptFile, "utf8")).toBe(LINUX_UPDATE_WATCHDOG_SCRIPT);
    // A second arm (the explicit path followed by before-quit) is a no-op.
    expect(mod.armLinuxUpdateWatchdog({
      stateDir,
      appImagePath: appImage,
      version: "3.0.0",
      parentPid: parent.pid,
      pollMs: 40,
    })).toBe(true);
    expect(mod.isLinuxUpdateWatchdogArmed()).toBe(true);
    // Let the watcher hit its short hard timeout (parent stayed alive) and exit
    // before tearing the parent down, so it cannot fall into the respawn path.
    await new Promise((resolve) => setTimeout(resolve, 800));
    parent.kill();
  });

  it("fails open without an AppImage path", async () => {
    const mod = await import("./linux-update-watchdog");
    expect(mod.armLinuxUpdateWatchdog({
      stateDir,
      appImagePath: undefined,
      version: "2.0.0",
    })).toBe(false);
  });

  it("fails open without a version", async () => {
    const mod = await import("./linux-update-watchdog");
    expect(mod.armLinuxUpdateWatchdog({
      stateDir,
      appImagePath: nodePath.join(appImageDir, "agent-orchestrator.AppImage"),
      version: undefined,
    })).toBe(false);
  });
});

describe("consumeUpdateRelaunchFailure", () => {
  it("reads and deletes the one-shot failure marker", async () => {
    const restartDir = nodePath.join(stateDir, "update-restart");
    mkdirSync(restartDir, { recursive: true });
    const file = nodePath.join(restartDir, RELAUNCH_FAILURE_FILE);
    writeFileSync(file, JSON.stringify({ version: "2.0.0", error: "boom", at: "2026-01-01T00:00:00.000Z" }));
    await expect(consumeUpdateRelaunchFailure({ stateDir })).resolves.toEqual({
      version: "2.0.0",
      error: "boom",
      at: "2026-01-01T00:00:00.000Z",
    });
    expect(existsSync(file)).toBe(false);
  });

  it("returns undefined when there is no marker", async () => {
    await expect(consumeUpdateRelaunchFailure({ stateDir })).resolves.toBeUndefined();
  });

  it("deletes a corrupt marker without returning it", async () => {
    const restartDir = nodePath.join(stateDir, "update-restart");
    mkdirSync(restartDir, { recursive: true });
    const file = nodePath.join(restartDir, RELAUNCH_FAILURE_FILE);
    writeFileSync(file, "not json");
    await expect(consumeUpdateRelaunchFailure({ stateDir })).resolves.toBeUndefined();
    expect(existsSync(file)).toBe(false);
  });
});