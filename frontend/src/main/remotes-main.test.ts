import { describe, expect, it, vi } from "vitest";
import { chmod, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { registerRemotesIpc } from "./remotes-main";

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
	await writeFile(path, '{"remotes":[{"label":"workbox","url":"http://192.0.2.1:1","password":"old"}]}', "utf8");
	await chmod(path, 0o600);
	return path;
}

describe("registerRemotesIpc", () => {
	it("registers the saved-host surface", async () => {
		const ipc = fakeIpc();
		registerRemotesIpc(ipc.ipcMain, { file: await tempFile(), disconnect: async () => undefined });
		expect(ipc.channels()).toEqual([
			"remotes:add",
			"remotes:list",
			"remotes:probe",
			"remotes:remove",
			"remotes:request",
			"remotes:update",
		]);
	});

	it("lists hosts without their passwords", async () => {
		const ipc = fakeIpc();
		registerRemotesIpc(ipc.ipcMain, { file: await tempFile(), disconnect: async () => undefined });
		await expect(ipc.invoke("remotes:list")).resolves.toEqual([{ label: "workbox", url: "http://192.0.2.1:1" }]);
	});

	it("saves a new host only after it answers as a daemon", async () => {
		const ipc = fakeIpc();
		const file = await tempFile();
		const probe = vi.fn().mockResolvedValueOnce("offline" as const).mockResolvedValueOnce("online" as const);
		registerRemotesIpc(ipc.ipcMain, { file, disconnect: async () => undefined, probe });
		const mini = { label: "mini", url: "http://192.0.2.9:9", password: "m" };

		await expect(ipc.invoke("remotes:add", mini)).resolves.toBe("offline");
		expect(JSON.parse(await readFile(file, "utf8")).remotes).toHaveLength(1);

		await expect(ipc.invoke("remotes:add", mini)).resolves.toBe("online");
		expect(JSON.parse(await readFile(file, "utf8")).remotes).toHaveLength(2);
	});

	it("drops the proxy of a removed host", async () => {
		const ipc = fakeIpc();
		const disconnect = vi.fn().mockResolvedValue(undefined);
		registerRemotesIpc(ipc.ipcMain, { file: await tempFile(), disconnect });
		await ipc.invoke("remotes:remove", "http://192.0.2.1:1");
		expect(disconnect).toHaveBeenCalledWith("http://192.0.2.1:1");
	});
});
