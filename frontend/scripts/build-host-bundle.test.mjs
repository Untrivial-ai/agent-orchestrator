import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, readlinkSync, rmSync, statSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "vitest";
import { buildHostBundle } from "./build-host-bundle.mjs";

test("headless archive contains the daemon, Claude ACP runtime, and tmux in the expected layout", () => {
	const root = mkdtempSync(join(tmpdir(), "ao-host-bundle-test-"));
	try {
		const daemon = join(root, "daemon", "ao");
		const node = join(root, "resources", "acp-runtime", "node", "bin", "node");
		const adapter = join(root, "resources", "acp-runtime", "node_modules", "@agentclientprotocol", "claude-agent-acp", "dist", "index.js");
		const tmux = join(root, "tmux", "bin", "tmux");
		for (const file of [daemon, node, adapter, tmux]) {
			mkdirSync(dirname(file), { recursive: true });
			writeFileSync(file, file);
		}
		for (const file of [daemon, node, tmux]) chmodSync(file, 0o444);
		const adapterLink = join(root, "resources", "acp-runtime", "node_modules", ".bin", "claude-agent-acp");
		mkdirSync(dirname(adapterLink), { recursive: true });
		symlinkSync("../@agentclientprotocol/claude-agent-acp/dist/index.js", adapterLink);
		const archive = buildHostBundle({ source: root, platform: "linux", arch: "x64" });
		assert.equal(archive, join(root, "dist-host", "ao-host-linux-x64.tar.gz"));
		const extracted = join(root, "extracted");
		mkdirSync(extracted);
		execFileSync("tar", ["-xzf", archive, "-C", extracted]);
		for (const [original, bundled] of [
			[daemon, "resources/daemon/ao"],
			[node, "resources/acp-runtime/node/bin/node"],
			[adapter, "resources/acp-runtime/node_modules/@agentclientprotocol/claude-agent-acp/dist/index.js"],
			[tmux, "resources/tmux/bin/tmux"],
		]) {
			assert.equal(readFileSync(join(extracted, bundled), "utf8"), original);
		}
		if (process.platform !== "win32") {
			for (const file of ["daemon/ao", "acp-runtime/node/bin/node", "tmux/bin/tmux"]) {
				assert.equal(statSync(join(extracted, "resources", file)).mode & 0o777, 0o755);
			}
		}
		assert.equal(readlinkSync(join(extracted, "resources", "acp-runtime", "node_modules", ".bin", "claude-agent-acp")), "../@agentclientprotocol/claude-agent-acp/dist/index.js");
		rmSync(adapter);
		assert.throws(() => buildHostBundle({ source: root, platform: "linux", arch: "x64" }), /Headless bundle requires/);
	} finally {
		rmSync(root, { recursive: true, force: true });
	}
});
