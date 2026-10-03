import { describe, expect, it, vi } from "vitest";
import { chmod, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { registerRemotesIpc, remotesFilePath } from "./remotes-main";
import { RemoteRegistry } from "./remote-registry";

type Handler = (event: unknown, ...args: unknown[]) => Promise<unknown>;

// ipcMain stand-in: records what was registered and lets a test invoke it.
function fakeIpc() {
	const handlers = new Map<string, Handler>();
	return {
		ipcMain: { handle: (channel: string, handler: Handler) => void handlers.set(channel, handler) },
		invoke: (channel: string, ...args: unknown[]) => {
			const handler = handlers.get(channel);
			if (!handler) throw new Error(`no handler for ${channel}`);
			return handler({}, ...args);
		},
		channels: () => [...handlers.keys()].sort(),
	};
}

async function tempFile(): Promise<string> {
	const dir = await mkdtemp(join(tmpdir(), "ao-remotes-main-"));
	const path = join(dir, "remotes.json");
	await writeFile(path, '{"remotes":[{"hostId":"h_workbox","label":"workbox","url":"http://192.0.2.1:1","password":"old"}]}', "utf8");
	await chmod(path, 0o600);
	return path;
}

describe("remotesFilePath", () => {
	it("keeps an isolated AO_DATA_DIR out of the real home credentials", () => {
		vi.stubEnv("AO_DATA_DIR", "/tmp/ao-remote-proof");
		try {
			expect(remotesFilePath()).toBe("/tmp/ao-remote-proof/remotes.json");
		} finally {
			vi.unstubAllEnvs();
		}
	});
});

describe("registerRemotesIpc", () => {
	it("saves the observed host ID and authenticates only after the identity probe", async () => {
		const requests: Array<{ path: string; authorization: string | undefined }> = [];
		const server = createServer((request, response) => {
			requests.push({ path: request.url ?? "", authorization: request.headers.authorization });
			response.setHeader("content-type", "application/json");
			response.end(JSON.stringify(request.url === "/api/v1/identity"
				? { hostId: "h_new", apiVersion: 1 }
				: { status: "ok", service: "agent-orchestrator-daemon", pid: 1234 }));
		});
		await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
		try {
			const address = server.address();
			if (!address || typeof address === "string") throw new Error("missing test port");
			const url = `http://127.0.0.1:${address.port}`;
			const file = await tempFile();
			const ipc = fakeIpc();
			registerRemotesIpc(ipc.ipcMain, {
				file,
				registry: new RemoteRegistry(async () => { throw new Error("unused"); }),
			});
			await expect(ipc.invoke("remotes:add", { label: "mini", url, password: "secret" })).resolves.toBe("online");
			const saved = JSON.parse(await readFile(file, "utf8")).remotes as Array<{ url: string; hostId: string }>;
			expect(saved.find((entry) => entry.url === url)?.hostId).toBe("h_new");
			expect(requests[0]).toEqual({ path: "/api/v1/identity", authorization: undefined });
			expect(requests.filter((request) => request.authorization)).toEqual([
				{ path: "/healthz", authorization: "Bearer secret" },
			]);
		} finally {
			await new Promise<void>((resolve) => server.close(() => resolve()));
		}
	});

	it("refuses a changed host before sending the saved password", async () => {
		let authenticated = false;
		const server = createServer((request, response) => {
			if (request.headers.authorization) authenticated = true;
			response.setHeader("content-type", "application/json");
			response.end(JSON.stringify(request.url === "/api/v1/identity"
				? { hostId: "h_other", apiVersion: 1 }
				: { status: "ok", service: "agent-orchestrator-daemon", pid: 1234 }));
		});
		await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
		try {
			const address = server.address();
			if (!address || typeof address === "string") throw new Error("missing test port");
			const url = `http://127.0.0.1:${address.port}`;
			const file = await tempFile();
			await writeFile(file, JSON.stringify({ remotes: [{ label: "workbox", url, password: "secret", hostId: "h_expected" }] }));
			const ipc = fakeIpc();
			registerRemotesIpc(ipc.ipcMain, {
				file,
				registry: new RemoteRegistry(async () => { throw new Error("proxy must not start"); }),
			});
			await expect(ipc.invoke("remotes:connect", url)).rejects.toThrow(/identity|host/i);
			expect(authenticated).toBe(false);
		} finally {
			await new Promise<void>((resolve) => server.close(() => resolve()));
		}
	});

	it("reports an incompatible host before sending or saving its credential", async () => {
		let authenticated = false;
		const server = createServer((request, response) => {
			if (request.headers.authorization) authenticated = true;
			response.setHeader("content-type", "application/json");
			response.end(JSON.stringify({ hostId: "h_new", apiVersion: 2 }));
		});
		await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
		try {
			const address = server.address();
			if (!address || typeof address === "string") throw new Error("missing test port");
			const url = `http://127.0.0.1:${address.port}`;
			const file = await tempFile();
			const ipc = fakeIpc();
			registerRemotesIpc(ipc.ipcMain, {
				file,
				registry: new RemoteRegistry(async () => { throw new Error("proxy must not start"); }),
			});
			await expect(ipc.invoke("remotes:add", { label: "new", url, password: "secret" })).resolves.toBe("incompatible");
			expect(authenticated).toBe(false);
			expect((JSON.parse(await readFile(file, "utf8")).remotes as Array<{ url: string }>).some((host) => host.url === url)).toBe(false);
		} finally {
			await new Promise<void>((resolve) => server.close(() => resolve()));
		}
	});

	it("keeps the real host connected when another saved host points at its URL", async () => {
		const file = await tempFile();
		const url = "http://127.0.0.1:9123";
		await writeFile(file, JSON.stringify({ remotes: [
			{ hostId: "h_a", label: "A", url, password: "a" },
			{ hostId: "h_b", label: "B", url, password: "b" },
		] }));
		const closed = vi.fn().mockResolvedValue(undefined);
		const registry = new RemoteRegistry(async () => ({ base: "http://127.0.0.1:5000/token", previewUrl: (_sessionId, sourceUrl) => sourceUrl, resolvePreviewUrl: (_sessionId, viewedUrl) => viewedUrl, close: closed }));
		await registry.connect({ hostId: "h_b", label: "B", url, password: "b" });
		const ipc = fakeIpc();
		registerRemotesIpc(ipc.ipcMain, { file, registry, identity: async () => "h_b", probe: async () => "online" });

		await expect(ipc.invoke("remotes:connect", url, "h_a")).rejects.toThrow(/identity changed/);
		await expect(ipc.invoke("remotes:connect", url, "h_b")).resolves.toMatchObject({ hostId: "h_b" });
		expect(closed).not.toHaveBeenCalled();
	});

	it("reports an unreachable new address as offline without changing the saved host", async () => {
		const file = await tempFile();
		const ipc = fakeIpc();
		registerRemotesIpc(ipc.ipcMain, {
			file,
			registry: new RemoteRegistry(async () => { throw new Error("proxy must not start"); }),
			identity: async () => { throw new TypeError("fetch failed"); },
		});

		await expect(ipc.invoke("remotes:update", "http://192.0.2.1:1", { url: "https://new.trycloudflare.com" })).resolves.toBe("offline");
		expect((JSON.parse(await readFile(file, "utf8")).remotes as Array<{ url: string }>)[0].url).toBe("http://192.0.2.1:1");
	});

	it("lists hosts without their passwords", async () => {
		const ipc = fakeIpc();
		registerRemotesIpc(ipc.ipcMain, { file: await tempFile(), registry: new RemoteRegistry(async () => { throw new Error("unused"); }) });
		await expect(ipc.invoke("remotes:list")).resolves.toEqual([{ hostId: "h_workbox", label: "workbox", url: "http://192.0.2.1:1" }]);
	});

	it("saves a new host only after it answers as a daemon", async () => {
		const ipc = fakeIpc();
		const file = await tempFile();
		const probe = vi.fn().mockResolvedValueOnce("offline" as const).mockResolvedValueOnce("online" as const);
		registerRemotesIpc(ipc.ipcMain, {
			file,
			registry: new RemoteRegistry(async () => { throw new Error("unused"); }),
			probe,
			identity: async () => "h_mini",
		});
		const mini = { label: "mini", url: "http://192.0.2.9:9", password: "m" };

		await expect(ipc.invoke("remotes:add", mini)).resolves.toBe("offline");
		expect(JSON.parse(await readFile(file, "utf8")).remotes).toHaveLength(1);

		await expect(ipc.invoke("remotes:add", mini)).resolves.toBe("online");
		expect(JSON.parse(await readFile(file, "utf8")).remotes).toHaveLength(2);
		expect(JSON.parse(await readFile(file, "utf8")).remotes[1].hostId).toBe("h_mini");
	});

	it("drops the proxy of a removed host", async () => {
		const ipc = fakeIpc();
		const closed = vi.fn().mockResolvedValue(undefined);
		const registry = new RemoteRegistry(async () => ({ base: "http://127.0.0.1:7654/token", previewUrl: (_sessionId, sourceUrl) => sourceUrl, resolvePreviewUrl: (_sessionId, viewedUrl) => viewedUrl, close: closed }));
		await registry.connect({ hostId: "h_workbox", label: "workbox", url: "http://192.0.2.1:1", password: "old" });
		registerRemotesIpc(ipc.ipcMain, { file: await tempFile(), registry });
		await ipc.invoke("remotes:remove", "http://192.0.2.1:1");
		expect(closed).toHaveBeenCalledOnce();
	});
});
