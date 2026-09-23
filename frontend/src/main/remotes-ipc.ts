import { probeRemote, type RemoteHealth } from "./remote-request";
import {
	applyRemoteChanges,
	readRemotes,
	removeRemote,
	updateRemote,
	type RemoteChanges,
	type RemoteEntry,
} from "./remotes-store";

// What the renderer is allowed to see. The password stays in the main process.
export type RemoteHostView = {
	label: string;
	url: string;
};

export function toHostViews(entries: RemoteEntry[]): RemoteHostView[] {
	return entries.map(({ label, url }) => ({ label, url }));
}

export async function findRemote(path: string, url: string): Promise<RemoteEntry> {
	const entry = (await readRemotes(path)).find((candidate) => candidate.url === url);
	if (!entry) throw new Error(`no saved host for ${url}`);
	return entry;
}

// Whatever is serving this url must stop: after an edit it holds a stale
// address or password, after a removal it is an open door with no doorman.
// Called unconditionally — dropping a host that was never connected is a no-op.
type Disconnect = (url: string) => Promise<void>;

/**
 * Edit a saved host in place. Probes before saving exactly as adding does — an
 * edit is how a host gets fixed, and one that lands somewhere unreachable only
 * looks fixed.
 */
export async function updateSavedRemote(
	path: string,
	url: string,
	changes: RemoteChanges,
	disconnect: Disconnect,
	probe: (entry: RemoteEntry) => Promise<RemoteHealth> = probeRemote,
): Promise<RemoteHealth> {
	const health = await probe(applyRemoteChanges(await findRemote(path, url), changes));
	if (health !== "online") return health;
	await updateRemote(path, url, changes);
	await disconnect(url);
	return health;
}

/** Forget a saved host. */
export async function removeSavedRemote(path: string, url: string, disconnect: Disconnect): Promise<void> {
	await removeRemote(path, url);
	await disconnect(url);
}
