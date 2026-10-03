// @vitest-environment node
import { mkdtempSync, rmSync, statSync } from "node:fs";
import { readFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { closeDaemonLog, daemonLogStreamForTest, openDaemonLog, writeDaemonLog } from "./daemon-log";

const tmpdirs: string[] = [];

function tmpLog(): string {
	const dir = mkdtempSync(path.join(os.tmpdir(), "ao-daemon-log-"));
	tmpdirs.push(dir);
	return path.join(dir, "daemon.log");
}

afterEach(async () => {
	await closeDaemonLog();
	for (const dir of tmpdirs.splice(0)) rmSync(dir, { recursive: true, force: true });
});

describe("daemon log", () => {
	it("flushes what it was given before close", async () => {
		const logPath = tmpLog();
		openDaemonLog(logPath);
		writeDaemonLog("line one\n");
		writeDaemonLog("line two\n");
		await closeDaemonLog();
		const contents = await readFile(logPath, "utf8");
		expect(contents).toContain("line one\n");
		expect(contents).toContain("line two\n");
	});

	it("rotates an oversized log on the next open and keeps writing to the fresh file", async () => {
		const logPath = tmpLog();
		openDaemonLog(logPath, 32);
		writeDaemonLog("x".repeat(40));
		// Rotation reads the size from disk, so flush first — the same way a
		// crash-loop sees the log: already flushed by the previous run.
		await closeDaemonLog();
		openDaemonLog(logPath, 32);
		writeDaemonLog("after rotation\n");
		await closeDaemonLog();
		expect(statSync(`${logPath}.1`).size).toBe(40);
		expect(await readFile(logPath, "utf8")).toBe("after rotation\n");
	});

	it("a late error from a replaced stream does not disable the current one", async () => {
		const logPath = tmpLog();
		openDaemonLog(logPath);
		const replaced = daemonLogStreamForTest();
		expect(replaced).toBeDefined();
		// Reopen: the previous stream is ended and a fresh one takes over, the
		// same thing an in-line rotation does mid-session.
		openDaemonLog(logPath);
		writeDaemonLog("after reopen\n");
		// Simulate the replaced stream failing late (e.g. ENOSPC surfacing only
		// on flush). Before the self-guard this culled the new stream too, and
		// logging went silently dark for the rest of the app's life.
		replaced?.emit("error", new Error("late failure"));
		writeDaemonLog("after late error\n");
		await closeDaemonLog();
		const contents = await readFile(logPath, "utf8");
		expect(contents).toContain("after reopen\n");
		expect(contents).toContain("after late error\n");
	});
});
