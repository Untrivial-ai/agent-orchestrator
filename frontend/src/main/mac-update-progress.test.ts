// @vitest-environment node
import { EventEmitter } from "node:events";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { acknowledgeMacUpdateRestart, startMacUpdateProgress } from "./mac-update-progress";

const { spawn } = vi.hoisted(() => ({ spawn: vi.fn() }));
vi.mock("node:child_process", () => ({ spawn }));
let root: string;
let child: EventEmitter & { stdout: EventEmitter & { destroy: ReturnType<typeof vi.fn> }; kill: ReturnType<typeof vi.fn>; unref: ReturnType<typeof vi.fn> };
beforeEach(async () => {
	root = await mkdtemp(path.join(os.tmpdir(), "ao-update-progress-"));
	await mkdir(path.join(root, "resources", "update-helper"), { recursive: true });
	await writeFile(path.join(root, "resources", "update-helper", "ao-update-progress"), "signed executable bytes", { mode: 0o755 });
	child = Object.assign(new EventEmitter(), {
		stdout: Object.assign(new EventEmitter(), { destroy: vi.fn() }), kill: vi.fn(), unref: vi.fn(),
	});
	spawn.mockReset().mockImplementation(() => {
		queueMicrotask(() => child.stdout.emit("data", Buffer.from("READY\n")));
		return child;
	});
});
afterEach(async () => { vi.useRealTimers(); await rm(root, { recursive: true, force: true }); });

const appPath = "/Applications/Agent Orchestrator.app";
function start() {
	return startMacUpdateProgress({ stateDir: root, resourcesPath: path.join(root, "resources"), appPath, version: "2.0.0" });
}
async function attemptPath() {
	const active = JSON.parse(await readFile(path.join(root, "update-restart", "active.json"), "utf8"));
	return path.join(root, "update-restart", active.attempt);
}

it("copies signed bytes outside the app and waits for its detached window before handing off", async () => {
	const progress = await start();
	const attempt = await attemptPath();
	expect(spawn).toHaveBeenCalledWith(path.join(attempt, "ao-update-progress"), [attempt], { detached: true, stdio: ["ignore", "pipe", "ignore"] });
	expect(await readFile(path.join(attempt, "ao-update-progress"), "utf8")).toBe("signed executable bytes");
	expect(child.unref).toHaveBeenCalledOnce();
	await progress.fail("Could not quit");
	expect(JSON.parse(await readFile(path.join(attempt, "error.json"), "utf8"))).toEqual({ message: "Could not quit" });
});

it("rejects helper startup failure so AO can stay open", async () => {
	spawn.mockImplementation(() => { queueMicrotask(() => child.emit("error", new Error("not signed"))); return child; });
	await expect(start()).rejects.toThrow("not signed");
	expect(child.kill).toHaveBeenCalledOnce();
	await expect(readFile(path.join(root, "update-restart", "active.json"))).rejects.toThrow();
});

it("acknowledges only the new process running the expected version and bundle", async () => {
	await start();
	const options = { stateDir: root, appPath, version: "2.0.0", pid: process.pid + 1 };
	expect(await acknowledgeMacUpdateRestart({ ...options, pid: process.pid })).toBe(false);
	expect(await acknowledgeMacUpdateRestart({ ...options, version: "1.0.0" })).toBe(false);
	expect(await acknowledgeMacUpdateRestart({ ...options, appPath: "/Downloads/Agent Orchestrator.app" })).toBe(false);
	expect(await acknowledgeMacUpdateRestart(options)).toBe(true);
	const complete = JSON.parse(await readFile(path.join(await attemptPath(), "complete.json"), "utf8"));
	expect(complete).toMatchObject({ version: "2.0.0", appPath, pid: process.pid + 1, parentPID: process.pid });
});

it("ignores old, future and malformed attempts without preventing app startup", async () => {
	await start();
	const options = { stateDir: root, appPath, version: "2.0.0", pid: process.pid + 1 };
	expect(await acknowledgeMacUpdateRestart({ ...options, now: Date.now() + 31 * 60_000 })).toBe(false);
	expect(await acknowledgeMacUpdateRestart({ ...options, now: 0 })).toBe(false);
	await writeFile(path.join(root, "update-restart", "active.json"), '{"attempt":"../../elsewhere"}');
	expect(await acknowledgeMacUpdateRestart(options)).toBe(false);
	await writeFile(path.join(root, "update-restart", "active.json"), 'broken');
	expect(await acknowledgeMacUpdateRestart(options)).toBe(false);
});
