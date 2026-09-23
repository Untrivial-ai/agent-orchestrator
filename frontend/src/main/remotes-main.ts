import os from "node:os";
import path from "node:path";
import { probeRemote, remoteRequest, type RemoteHealth, type RemoteRequestInit } from "./remote-request";
import { findRemote, removeSavedRemote, toHostViews, updateSavedRemote } from "./remotes-ipc";
import { addRemote, readRemotes, type RemoteChanges, type RemoteEntry } from "./remotes-store";

// The CLI resolves this file through config.StateDir(), which is ~/.ao
// unconditionally — it does NOT honour AO_DATA_DIR (that points at the daemon's
// data dir). Following AO_DATA_DIR here would make the app read a different
// file than `ao --url` writes, which defeats sharing one host list and one
// credential store.
export function remotesFilePath(): string {
	return path.join(os.homedir(), ".ao", "remotes.json");
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
	/** Stop whatever is serving this url; a no-op for a host that is not connected. */
	disconnect: (url: string) => Promise<void>;
	probe?: (entry: RemoteEntry) => Promise<RemoteHealth>;
};

/**
 * Saved AO daemons, shared with the CLI's ~/.ao/remotes.json. Everything the
 * renderer receives back is password-free (see remotes-ipc.ts); the plaintext
 * password only ever travels renderer -> main, on `add`.
 */
export function registerRemotesIpc(ipcMain: IpcMainLike, { file, disconnect, probe = probeRemote }: RemotesIpcDeps): void {
	ipcMain.handle("remotes:list", async () => toHostViews(await readRemotes(file)));
	ipcMain.handle("remotes:add", async (_event, input: RemoteEntry) => {
		// Probe before saving: a host that never answered is worse than no host,
		// because it looks configured.
		const health = await probe(input);
		if (health === "online") await addRemote(file, input);
		return health;
	});
	ipcMain.handle("remotes:probe", async (_event, url: string) => probe(await findRemote(file, url)));
	ipcMain.handle("remotes:request", async (_event, url: string, init: RemoteRequestInit) =>
		remoteRequest(await findRemote(file, url), init),
	);
	ipcMain.handle("remotes:update", async (_event, url: string, changes: RemoteChanges) =>
		updateSavedRemote(file, url, changes, disconnect, probe),
	);
	ipcMain.handle("remotes:remove", async (_event, url: string) => removeSavedRemote(file, url, disconnect));
}
