import os from "node:os";
import path from "node:path";
import { IncompatibleRemoteVersionError, probeRemote, readRemoteIdentity, type RemoteHealth } from "./remote-request";
import { findRemote, removeSavedRemote, toHostViews, updateSavedRemote } from "./remotes-ipc";
import type { RemoteRegistry } from "./remote-registry";
import { addRemote, readRemotes, type RemoteChanges, type RemoteEntry } from "./remotes-store";

// An isolated desktop run must not read or write the real user's credentials.
// Without an override, keep the existing ~/.ao location.
export function remotesFilePath(): string {
	return path.join(process.env.AO_DATA_DIR?.trim() || path.join(os.homedir(), ".ao"), "remotes.json");
}

// The slice of Electron's ipcMain these handlers need, so tests need no Electron.
type IpcMainLike = {
	// eslint-disable-next-line @typescript-eslint/no-explicit-any -- the listener
	// args must unify Electron's IpcMain (any[]) with a test fake (unknown[]),
	// and `never[]` rejects both; `any[]` here leaks nowhere past registration.
	handle(channel: string, listener: (event: unknown, ...args: any[]) => Promise<unknown>): void;
};

export type RemotesIpcDeps = {
	file: string;
	registry: RemoteRegistry;
	probe?: (entry: RemoteEntry) => Promise<RemoteHealth>;
	identity?: (entry: Pick<RemoteEntry, "url">) => Promise<string>;
};

/**
 * Saved AO daemons, shared with the CLI's ~/.ao/remotes.json. Everything the
 * renderer receives back is password-free (see remotes-ipc.ts); the plaintext
 * password only travels renderer -> main when adding or editing a credential.
 */
export function registerRemotesIpc(
	ipcMain: IpcMainLike,
	{ file, registry, probe = probeRemote, identity = readRemoteIdentity }: RemotesIpcDeps,
): void {
	const disconnect = (url: string) => registry.disconnect(url);
	const checkedProbe = async (entry: RemoteEntry): Promise<RemoteHealth> => {
		if (!entry.hostId) throw new Error("remote host must be paired again to record its identity");
		let actual: string;
		try {
			actual = await identity(entry);
		} catch (error) {
			if (error instanceof IncompatibleRemoteVersionError) return "incompatible";
			return "offline";
		}
		if (actual !== entry.hostId) throw new Error(`remote host identity changed for ${entry.url}; connection refused`);
		return probe(entry);
	};
	// Keep edits and connects ordered so an update cannot leave a stale proxy.
	// ponytail: one queue includes network probes; split by host if five-host setup is too slow.
	let pending: Promise<void> = Promise.resolve();
	const ordered = <T>(operation: () => Promise<T>): Promise<T> => {
		const result = pending.then(operation, operation);
		pending = result.then(() => undefined, () => undefined);
		return result;
	};

	ipcMain.handle("remotes:list", async () => toHostViews(await readRemotes(file)));
	ipcMain.handle("remotes:add", async (_event, input: RemoteEntry) => ordered(async () => {
		// Probe before saving: a host that never answered is worse than no host,
		// because it looks configured.
		let hostId: string;
		try {
			hostId = await identity(input);
		} catch (error) {
			if (error instanceof IncompatibleRemoteVersionError) return "incompatible" as RemoteHealth;
			return "offline" as RemoteHealth;
		}
		const entry = { label: input.label, url: input.url, password: input.password, hostId };
		const health = await checkedProbe(entry);
		if (health === "online") {
			const previous = (await readRemotes(file)).find((saved) => saved.hostId === hostId);
			await addRemote(file, entry);
			if (previous && previous.url !== entry.url) await disconnect(previous.url);
			await disconnect(entry.url);
		}
		return health;
	}));
	ipcMain.handle("remotes:update", async (_event, url: string, changes: RemoteChanges) => ordered(() =>
		updateSavedRemote(file, url, changes, disconnect, checkedProbe),
	));
	ipcMain.handle("remotes:remove", async (_event, url: string) => ordered(() =>
		removeSavedRemote(file, url, disconnect),
	));
	ipcMain.handle("remotes:connect", async (_event, url: string, hostId?: string) => ordered(async () => {
		const entry = await findRemote(file, url, hostId);
		const health = await checkedProbe(entry);
		if (health !== "online") throw new Error(`host ${url} is ${health}`);
		return registry.connect(entry);
	}));
	ipcMain.handle("remotes:disconnect", async (_event, url: string) => ordered(() => disconnect(url)));
	ipcMain.handle("remotes:previewUrl", async (_event, hostId: string, sessionId: string, sourceUrl: string) =>
		registry.previewUrl(hostId, sessionId, sourceUrl));
	ipcMain.handle("remotes:resolvePreviewUrl", async (_event, hostId: string, sessionId: string, viewedUrl: string) =>
		registry.resolvePreviewUrl(hostId, sessionId, viewedUrl));
}
