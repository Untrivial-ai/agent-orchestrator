import { describe, expect, it } from "vitest";
import { bundledTmuxBinaryPath, stableBundledTmuxBinaryPath } from "./bundled-tmux";

describe("bundledTmuxBinaryPath", () => {
	it.each(["darwin", "linux"] as const)("uses the packaged tmux on %s", (platform) => {
		expect(bundledTmuxBinaryPath(true, "/opt/ao/resources", platform)).toBe(
			"/opt/ao/resources/tmux/bin/tmux",
		);
	});

	it("does not override tmux in development", () => {
		expect(bundledTmuxBinaryPath(false, "/opt/ao/resources", "darwin")).toBeNull();
	});

	it("does not require tmux on Windows", () => {
		expect(bundledTmuxBinaryPath(true, "C:\\AO\\resources", "win32")).toBeNull();
	});
});

describe("stableBundledTmuxBinaryPath", () => {
	it.each(["darwin", "linux"] as const)("uses durable versioned AO storage on %s", (platform) => {
		expect(stableBundledTmuxBinaryPath(true, "/home/me/.ao", "0.10.3", platform, "arm64")).toBe(
			`/home/me/.ao/runtime/tmux/0.10.3-${platform}-arm64/tmux`,
		);
	});

	it("sanitizes version components rather than allowing path traversal", () => {
		expect(stableBundledTmuxBinaryPath(true, "/home/me/.ao", "../next build", "linux", "x64")).toBe(
			"/home/me/.ao/runtime/tmux/.._next_build-linux-x64/tmux",
		);
	});

	it("does not stage a Windows binary", () => {
		expect(stableBundledTmuxBinaryPath(true, "C:\\AO", "0.10.3", "win32", "x64")).toBeNull();
	});
});
