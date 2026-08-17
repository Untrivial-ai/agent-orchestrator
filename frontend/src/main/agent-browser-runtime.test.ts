import { EventEmitter } from "node:events";
import { mkdir, mkdtemp, readFile, readdir, rm, stat, symlink, utimes, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { describe, expect, it, vi } from "vitest";
import {
	AgentBrowserRuntime,
	AGENT_BROWSER_UNIX_SOCKET_PATH_MAX_BYTES,
	BROWSER_RUNTIME_RECLAIM_GRACE_MS,
	agentBrowserSocketPath,
	nativeArgumentsForAction,
	parseAgentBrowserJSON,
	scavengeBrowserRuntime,
	scavengeBrowserSocketAliases,
	validateAgentBrowserArguments,
} from "./agent-browser-runtime";

const shortTempDir = process.platform === "win32" ? os.tmpdir() : "/tmp";

describe("agent-browser command policy", () => {
	it("allows the focused semantic workflow", () => {
		expect(() => validateAgentBrowserArguments(["snapshot", "-i", "--json"])).not.toThrow();
		expect(() => validateAgentBrowserArguments(["find", "role", "button", "click", "--name", "Save"])).not.toThrow();
		expect(() => validateAgentBrowserArguments(["wait", "--text", "Saved"])).not.toThrow();
		expect(() => validateAgentBrowserArguments(["open", "http://localhost:5173"])).not.toThrow();
	});

	it("blocks browser ownership, persistence, arbitrary evaluation, and unsafe navigation", () => {
		for (const args of [
			["connect", "9222"],
			["eval", "document.cookie"],
			["snapshot", "--cdp", "9222"],
			["snapshot", "--profile", "Default"],
			["get", "cdp-url"],
			["open", "file:///tmp/secret"],
			["network", "route", "*", "--abort"],
		]) {
			expect(() => validateAgentBrowserArguments(args)).toThrow();
		}
	});
});

describe("AO action translation", () => {
	it("maps the public snapshot and ref contract to native arguments", () => {
		expect(nativeArgumentsForAction("snapshot", { interactive: true })).toEqual([
			"snapshot",
			"--interactive",
			"--compact",
		]);
		expect(nativeArgumentsForAction("click", { ref: "e2" })).toEqual(["click", "@e2"]);
		expect(nativeArgumentsForAction("drag", { ref: "e2", targetRef: "@e5" })).toEqual([
			"drag",
			"@e2",
			"@e5",
		]);
	});

	it("maps waits, tabs, frames, and dialogs without accepting arbitrary evaluation", () => {
		expect(nativeArgumentsForAction("wait", { textGone: "Saving...", timeoutMs: 2_500 })).toEqual([
			"wait",
			"text=Saving...",
			"--state",
			"hidden",
			"--timeout",
			"2500",
		]);
		expect(nativeArgumentsForAction("tab-select", { tabId: "t2" })).toEqual(["tab", "t2"]);
		expect(nativeArgumentsForAction("get", { property: "text" })).toEqual(["get", "text"]);
		const stableWait = nativeArgumentsForAction("wait", { stableMs: 750, timeoutMs: 2_500 });
		expect(stableWait.slice(0, 2)).toEqual(["wait", "--fn"]);
		expect(stableWait[2]).toContain("750");
		expect(stableWait.slice(-2)).toEqual(["--timeout", "2500"]);
		expect(nativeArgumentsForAction("frame", { target: "e7" })).toEqual(["frame", "@e7"]);
		expect(nativeArgumentsForAction("dialog", { operation: "accept", text: "yes" })).toEqual([
			"dialog",
			"accept",
			"yes",
		]);
		expect(() => nativeArgumentsForAction("eval", { expression: "document.cookie" })).toThrow();
	});
});

describe("agent-browser runtime lifecycle", () => {
	const provider = {
		listTargets: () => [],
		createTarget: async () => {
			throw new Error("unexpected target creation");
		},
		activateTarget: () => undefined,
		closeTarget: () => undefined,
	};

	async function fixture(overrides: {
		start?: () => Promise<string>;
		close?: () => Promise<void>;
		platform?: NodeJS.Platform;
		processRunner?: (...args: unknown[]) => Promise<{ stdout: string; stderr: string; exitCode: number }>;
		socketDir?: string;
	} = {}) {
		const dataDir = await mkdtemp(path.join(shortTempDir, "ao-browser-runtime-test-"));
		const bridge = {
			start: vi.fn(overrides.start ?? (async () => "ws://127.0.0.1:1/fixture")),
			close: vi.fn(overrides.close ?? (async () => undefined)),
		};
		const processRunner = vi.fn(
			overrides.processRunner ?? (async () => ({ stdout: "", stderr: "", exitCode: 0 })),
		);
		const runtime = new AgentBrowserRuntime({
			binaryPath: process.execPath,
			dataDir,
			...(overrides.socketDir ? { socketDir: overrides.socketDir } : {}),
			...(overrides.platform ? { platform: overrides.platform } : {}),
			bridgeFactory: () => bridge,
			processRunner: processRunner as never,
			processAlive: () => false,
		});
		return { dataDir, bridge, processRunner, runtime };
	}

	async function cleanup(dataDir: string): Promise<void> {
		await rm(dataDir, { recursive: true, force: true });
	}

	it("serializes concurrent initialization and removes the owned run root", async () => {
		const { dataDir, bridge, runtime } = await fixture();
		try {
			await Promise.all([
				runtime.run("session-1", ["snapshot"], provider),
				runtime.run("session-1", ["snapshot"], provider),
			]);
			expect(bridge.start).toHaveBeenCalledTimes(1);

			await runtime.dispose();
			expect(await readdir(dataDir)).toEqual([]);
		} finally {
			await cleanup(dataDir);
		}
	});

	it("uses a private explicit socket directory and a bounded namespace", async () => {
		const { dataDir, processRunner, runtime } = await fixture();
		try {
			await runtime.run("session-1", ["snapshot"], provider);
			const environment = (processRunner.mock.calls[0] as unknown[])[2] as NodeJS.ProcessEnv;
			const namespace = environment.AGENT_BROWSER_NAMESPACE!;
			const socketDir = environment.AGENT_BROWSER_SOCKET_DIR!;
			if (process.platform === "win32") {
				expect(socketDir).toContain(path.join(dataDir, "r-"));
				expect(socketDir).toMatch(/[\\/]s$/);
			} else {
				expect(socketDir).toMatch(/^\/tmp\/ao-br-/);
			}
			expect(namespace).toMatch(/^ao-[0-9a-f]{4}-[0-9a-f]{12}$/);
			const socketPath = agentBrowserSocketPath(socketDir, namespace);
			expect(Buffer.byteLength(socketPath, "utf8")).toBeLessThanOrEqual(
				process.platform === "win32" ? Number.MAX_SAFE_INTEGER : AGENT_BROWSER_UNIX_SOCKET_PATH_MAX_BYTES,
			);
			if (process.platform !== "win32") {
				expect((await stat(socketDir)).mode & 0o777).toBe(0o700);
			}
		} finally {
			await runtime.dispose();
			await cleanup(dataDir);
		}
	});

	it("passes only the native runtime allowlist and AO-scoped variables", async () => {
		vi.stubEnv("AWS_SECRET_ACCESS_KEY", "should-not-cross-process");
		vi.stubEnv("HTTP_PROXY", "http://user:secret@example.test:8080");
		const { dataDir, processRunner, runtime } = await fixture();
		try {
			await runtime.run("session-1", ["snapshot"], provider);
			const environment = (processRunner.mock.calls[0] as unknown[])[2] as NodeJS.ProcessEnv;
			expect(environment.AWS_SECRET_ACCESS_KEY).toBeUndefined();
			expect(environment.HTTP_PROXY).toBeUndefined();
			expect(environment.AGENT_BROWSER_CDP).toBe("ws://127.0.0.1:1/fixture");
			expect(environment.HOME).toContain(path.join(dataDir, "r-"));
		} finally {
			await runtime.dispose();
			await cleanup(dataDir);
			vi.unstubAllEnvs();
		}
	});

	it("re-disables streaming before every command across native daemon replacement", async () => {
		let streamingEnabled = true;
		const calls: string[][] = [];
		const { dataDir, runtime } = await fixture({
			processRunner: async (...args) => {
				const command = args[1] as string[];
				calls.push(command);
				if (command[0] === "stream" && command[1] === "disable") {
					streamingEnabled = false;
					return { stdout: "", stderr: "", exitCode: 0 };
				}
				if (command[0] === "close") return { stdout: "", stderr: "", exitCode: 0 };
				if (streamingEnabled) return { stdout: "", stderr: "streaming remained enabled", exitCode: 1 };
				// Simulate the native daemon expiring after this command. Its next
				// generation starts with the input-capable stream enabled again.
				streamingEnabled = true;
				return { stdout: "", stderr: "", exitCode: 0 };
			},
		});
		try {
			await runtime.run("session-1", ["snapshot"], provider);
			await runtime.run("session-1", ["get", "url"], provider);
			expect(calls).toEqual([
				["stream", "disable"],
				["snapshot"],
				["stream", "disable"],
				["get", "url"],
			]);
		} finally {
			await runtime.dispose();
			await cleanup(dataDir);
		}
	});

	it("runs tab new when streaming is already disabled", async () => {
		const calls: string[][] = [];
		const { dataDir, runtime } = await fixture({
			processRunner: async (...args) => {
				const command = args[1] as string[];
				calls.push(command);
				if (command[0] === "stream" && command[1] === "disable") {
					return {
						stdout: "",
						stderr: "Streaming is not enabled for this session",
						exitCode: 1,
					};
				}
				return { stdout: '{"success":true}', stderr: "", exitCode: 0 };
			},
		});
		try {
			const result = await runtime.run("session-1", ["tab", "new", "https://theuselessweb.com", "--json"], provider);
			expect(result.exitCode).toBe(0);
			expect(calls).toEqual([
				["stream", "disable"],
				["tab", "new", "https://theuselessweb.com", "--json"],
			]);
		} finally {
			await runtime.dispose();
			await cleanup(dataDir);
		}
	});

	it("does not hide genuine stream-disable failures", async () => {
		const { dataDir, runtime } = await fixture({
			processRunner: async () => ({ stdout: "", stderr: "daemon transport failed", exitCode: 1 }),
		});
		try {
			await expect(
				runtime.run("session-1", ["tab", "new", "https://theuselessweb.com", "--json"], provider),
			).rejects.toMatchObject({
				code: "AGENT_BROWSER_START_FAILED",
				message: "daemon transport failed",
			});
		} finally {
			await runtime.dispose();
			await cleanup(dataDir);
		}
	});

	it("rejects an oversized Unix socket path before starting the bridge", async () => {
		const socketBase = await mkdtemp(path.join(os.tmpdir(), "ao-browser-runtime-long-path-test-"));
		const socketDir = path.join(socketBase, "x".repeat(120));
		const { dataDir, runtime, bridge } = await fixture({ platform: "darwin", socketDir });
		try {
			await expect(runtime.run("session-1", ["snapshot"], provider)).rejects.toMatchObject({
				code: "AGENT_BROWSER_START_FAILED",
			});
			expect(bridge.start).not.toHaveBeenCalled();
		} finally {
			await runtime.dispose();
			await cleanup(dataDir);
			await cleanup(socketBase);
		}
	});

	it("makes dispose await a close already started by session destruction", async () => {
		let releaseClose!: () => void;
		const closeFinished = new Promise<void>((resolve) => {
			releaseClose = resolve;
		});
		const { dataDir, processRunner, runtime } = await fixture({
			processRunner: async (...args) => {
				if (Array.isArray(args[1]) && args[1][0] === "close") await closeFinished;
				return { stdout: "", stderr: "", exitCode: 0 };
			},
		});
		try {
			await runtime.run("session-1", ["snapshot"], provider);
			const closing = runtime.closeSession("session-1");
			await vi.waitFor(() => expect(processRunner).toHaveBeenCalledWith(
				process.execPath,
				["close"],
				expect.any(Object),
				undefined,
				10_000,
			));
			const disposing = runtime.dispose();
			let disposed = false;
			void disposing.then(() => {
				disposed = true;
			});
			await Promise.resolve();
			expect(disposed).toBe(false);
			releaseClose();
			await Promise.all([closing, disposing]);
			expect(await readdir(dataDir)).toEqual([]);
		} finally {
			releaseClose();
			await cleanup(dataDir);
		}
	});

	it("removes the session directory even when bridge close fails", async () => {
		const { dataDir, bridge, runtime } = await fixture({
			close: async () => {
				throw new Error("bridge already closed");
			},
		});
		try {
			await runtime.run("session-1", ["snapshot"], provider);
			await runtime.dispose();
			expect(bridge.close).toHaveBeenCalledTimes(1);
			expect(await readdir(dataDir)).toEqual([]);
		} finally {
			await cleanup(dataDir);
		}
	});

	it("recovers only confirmed-dead marked run roots", async () => {
		const dataDir = await mkdtemp(path.join(os.tmpdir(), "ao-browser-scavenge-test-"));
		try {
			const dead = path.join(dataDir, "run-101-aaaaaaaaaaaa");
			const shortDead = path.join(dataDir, "r-eeeeeeeeee");
			const alive = path.join(dataDir, "run-202-bbbbbbbbbbbb");
			const malformed = path.join(dataDir, "run-303-cccccccccccc");
			const empty = path.join(dataDir, "run-404-dddddddddddd");
			const legacy = path.join(dataDir, "ao-legacy-session");
			await Promise.all([mkdir(dead), mkdir(shortDead), mkdir(alive), mkdir(malformed), mkdir(empty), mkdir(legacy)]);
			const deadOwnerPath = path.join(dead, "owner.json");
			const shortDeadOwnerPath = path.join(shortDead, "owner.json");
			await writeFile(
				deadOwnerPath,
				JSON.stringify({ marker: "AO_BROWSER_RUNTIME_V1", pid: 101, startedAt: new Date().toISOString(), token: "a".repeat(32) }),
			);
			await writeFile(
				shortDeadOwnerPath,
				JSON.stringify({ marker: "AO_BROWSER_RUNTIME_V1", pid: 101, startedAt: new Date().toISOString(), token: "e".repeat(32) }),
			);
			const staleAt = new Date(Date.now() - BROWSER_RUNTIME_RECLAIM_GRACE_MS - 1_000);
			await Promise.all([utimes(deadOwnerPath, staleAt, staleAt), utimes(shortDeadOwnerPath, staleAt, staleAt)]);
			await writeFile(
				path.join(alive, "owner.json"),
				JSON.stringify({ marker: "AO_BROWSER_RUNTIME_V1", pid: 202, startedAt: new Date().toISOString(), token: "b".repeat(32) }),
			);
			await writeFile(path.join(malformed, "owner.json"), "not-json");
			await writeFile(path.join(legacy, "config.json"), "{}\n");

			await scavengeBrowserRuntime(dataDir, (pid) => pid === 202);

			expect((await readdir(dataDir)).sort()).toEqual([
				"ao-legacy-session",
				"run-202-bbbbbbbbbbbb",
				"run-303-cccccccccccc",
				"run-404-dddddddddddd",
			]);
			expect(await readFile(path.join(legacy, "config.json"), "utf8")).toBe("{}\n");
		} finally {
			await cleanup(dataDir);
		}
	});

	it.skipIf(process.platform === "win32")("removes only confirmed-dead socket aliases owned by this data root", async () => {
		const dataDir = await mkdtemp(path.join(shortTempDir, "ao-browser-alias-data-"));
		const aliasRoot = await mkdtemp(path.join(shortTempDir, "ao-browser-alias-root-"));
		const foreignRoot = await mkdtemp(path.join(shortTempDir, "ao-browser-alias-foreign-"));
		try {
			const deadTarget = path.join(dataDir, "r-aaaaaaaaaa", "s");
			const liveTarget = path.join(dataDir, "r-bbbbbbbbbb", "s");
			const foreignTarget = path.join(foreignRoot, "r-cccccccccc", "s");
			await Promise.all([
				mkdir(deadTarget, { recursive: true }),
				mkdir(liveTarget, { recursive: true }),
				mkdir(foreignTarget, { recursive: true }),
			]);
			await Promise.all([
				symlink(deadTarget, path.join(aliasRoot, "ao-br-101-aaaaaaaaaaaa"), "dir"),
				symlink(liveTarget, path.join(aliasRoot, "ao-br-202-bbbbbbbbbbbb"), "dir"),
				symlink(foreignTarget, path.join(aliasRoot, "ao-br-303-cccccccccccc"), "dir"),
				symlink(deadTarget, path.join(aliasRoot, "ao-br-not-owned"), "dir"),
			]);

			await scavengeBrowserSocketAliases(dataDir, (pid) => pid === 202, undefined, aliasRoot);

			expect((await readdir(aliasRoot)).sort()).toEqual([
				"ao-br-202-bbbbbbbbbbbb",
				"ao-br-303-cccccccccccc",
				"ao-br-not-owned",
			]);
		} finally {
			await Promise.all([
				rm(aliasRoot, { recursive: true, force: true }),
				rm(dataDir, { recursive: true, force: true }),
				rm(foreignRoot, { recursive: true, force: true }),
			]);
		}
	});
});

describe("agent-browser structured output", () => {
	it("preserves the native root content boundary metadata", () => {
		const result = parseAgentBrowserJSON(
			JSON.stringify({
				success: true,
				data: { snapshot: "page text", _boundary: { nonce: "page-spoof", origin: "page" } },
				_boundary: { nonce: "native-nonce", origin: "https://example.test/" },
			}),
		);
		expect(result).toMatchObject({
			snapshot: "page text",
			_boundary: { nonce: "native-nonce", origin: "https://example.test/" },
			untrustedExternalContent: true,
		});
	});

	it("does not treat a page-shaped boundary field as trusted metadata", () => {
		const result = parseAgentBrowserJSON(JSON.stringify({ success: true, data: { value: "page", _boundary: "spoof" } }));
		expect(result._boundary).toBeUndefined();
		expect(result.untrustedExternalContent).toBe(true);
	});
});

const nativeBinary = process.env.AO_AGENT_BROWSER_TEST_BINARY;
describe.skipIf(!nativeBinary)("agent-browser native compatibility", () => {
	it("connects the pinned native daemon and keeps native tab lifecycle commands aligned", async () => {
		class DebuggerFixture extends EventEmitter {
			attached = false;
			attach() {
				this.attached = true;
			}
			detach() {
				this.attached = false;
				this.emit("detach");
			}
			isAttached() {
				return this.attached;
			}
			async sendCommand(method: string) {
				if (method === "Runtime.evaluate") {
					return { result: { type: "string", value: "http://localhost:5173/" } };
				}
				if (method === "Page.captureScreenshot") {
					return {
						data: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
					};
				}
				return {};
			}
		}
		const targets = [
			{
				id: "t1",
				url: "http://localhost:5173/",
				title: "Fixture",
				debugger: new DebuggerFixture(),
			},
		];
		const provider = {
			listTargets: () => targets,
			createTarget: async (url: string) => {
				const target = {
					id: `t${targets.length + 1}`,
					url,
					title: "New tab",
					debugger: new DebuggerFixture(),
				};
				targets.push(target);
				return target;
			},
			activateTarget: () => undefined,
			closeTarget: (targetId: string) => {
				const index = targets.findIndex((target) => target.id === targetId);
				if (index >= 0) targets.splice(index, 1);
			},
		};
		const runtime = new AgentBrowserRuntime({
			binaryPath: nativeBinary!,
			dataDir: path.join(shortTempDir, "ao-agent-browser-native-test"),
		});
		try {
			const result = await runtime.runAction("native-fixture", "get", { property: "url" }, provider);
			expect(result.url).toBe("http://localhost:5173/");
			const created = await runtime.runAction("native-fixture", "tab-new", {}, provider);
			expect(created.tabId).toBe("t2");
			expect(targets.map((target) => target.id)).toEqual(["t1", "t2"]);
			await runtime.runAction("native-fixture", "tab-close", { tabId: "t2" }, provider);
			expect(targets.map((target) => target.id)).toEqual(["t1"]);
			const screenshot = await runtime.screenshot("native-fixture", provider);
			expect(screenshot).toMatchObject({ width: 1, height: 1 });
		} finally {
			await runtime.dispose();
		}
	});
});
