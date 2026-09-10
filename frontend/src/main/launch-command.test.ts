// @vitest-environment node
import { EventEmitter } from "node:events";
import type { ChildProcess } from "node:child_process";
import { describe, expect, it, vi } from "vitest";
import { launchCommand } from "./launch-command";

function childProcessDouble() {
	const child = new EventEmitter() as ChildProcess;
	child.unref = vi.fn(() => child);
	return child;
}

describe("launchCommand", () => {
	it("rejects a process that spawns but immediately exits non-zero", async () => {
		const child = childProcessDouble();
		const result = launchCommand("cursor", ["/workspace"], "/workspace", {
			spawnProcess: () => child,
			settleMs: 50,
		});
		child.emit("spawn");
		child.emit("exit", 1, null);
		await expect(result).rejects.toThrow("launcher exited with code 1");
	});

	it("rejects a spawn error", async () => {
		const child = childProcessDouble();
		const result = launchCommand("missing", [], "/workspace", { spawnProcess: () => child });
		child.emit("error", new Error("ENOENT"));
		await expect(result).rejects.toThrow("ENOENT");
	});

	it("accepts and detaches a process that stays alive through the settling window", async () => {
		const child = childProcessDouble();
		const result = launchCommand("cursor", ["/workspace"], "/workspace", {
			spawnProcess: () => child,
			settleMs: 0,
		});
		child.emit("spawn");
		await expect(result).resolves.toBeUndefined();
		expect(child.unref).toHaveBeenCalledOnce();
	});

	it("routes Windows batch launchers through ComSpec without interpolating an unquoted workspace", async () => {
		const child = childProcessDouble();
		const spawnProcess = vi.fn(() => child);
		const result = launchCommand("C:\\bin\\code.cmd", ["C:\\work trees\\feature & fix"], "C:\\work trees\\feature & fix", {
			spawnProcess,
			settleMs: 0,
			platform: "win32",
			env: { ComSpec: "C:\\Windows\\System32\\cmd.exe" },
		});
		child.emit("spawn");
		await result;
		expect(spawnProcess).toHaveBeenCalledWith(
			"C:\\Windows\\System32\\cmd.exe",
			["/d", "/s", "/c", `""C:\\bin\\code.cmd" "C:\\work trees\\feature & fix""`],
			expect.objectContaining({ windowsVerbatimArguments: true }),
		);
	});
});
