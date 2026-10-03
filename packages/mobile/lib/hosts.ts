import AsyncStorage from "@react-native-async-storage/async-storage";
import * as SecureStore from "expo-secure-store";
import type { Endpoint } from "./endpoints";

/**
 * A paired machine.
 *
 * Endpoints are a list because the phone races them, and they are refreshed on
 * every successful connect — that is what lets a rotated tunnel hostname or a
 * new LAN address heal itself without the user re-pairing.
 */
export type Host = {
	/** Stable, daemon-issued. Every probe answer is checked against this before
	 * the endpoint is trusted or the token is presented. */
	id: string;
	/** Hostname by default; the user can rename it. */
	name: string;
	platform: string;
	endpoints: Endpoint[];
	/** Connection token. Lives in the device keystore, never in AsyncStorage. */
	token: string;
	lastConnected: number;
};

/** activeHost() uses explicit selection or the most recent machine. */

/** Ignore recency-only writes so another host reconnecting does not restart live connections. */
export function sameHostConnections(left: Host[], right: Host[]): boolean {
	const connectionFields = (hosts: Host[]) => hosts.map(({ id, name, platform, endpoints, token }) => [id, name, platform, endpoints, token]);
	return JSON.stringify(connectionFields(left)) === JSON.stringify(connectionFields(right));
}

const HOSTS_KEY = "ao.hosts";
const ACTIVE_HOST_KEY = "ao.activeHost";
const tokenKey = (id: string) => `ao.hostToken.${id}`;

/** What is written to AsyncStorage: everything except the token. */
export type HostMetadata = Omit<Host, "token">;
type StoredHost = HostMetadata;

function isStoredHost(v: unknown): v is StoredHost {
	if (typeof v !== "object" || v === null) return false;
	const h = v as Record<string, unknown>;
	// An empty id is valid: a machine migrated from the single-server config has
	// not been issued one yet and adopts it on first connect.
	return typeof h.id === "string" && Array.isArray(h.endpoints);
}

async function readStored(): Promise<StoredHost[]> {
	try {
		const raw = await AsyncStorage.getItem(HOSTS_KEY);
		if (!raw) return [];
		const parsed: unknown = JSON.parse(raw);
		if (!Array.isArray(parsed)) return [];
		return parsed.filter(isStoredHost);
	} catch {
		// Corrupted storage must not brick the app; the user re-pairs instead.
		return [];
	}
}

function sortHosts(hosts: StoredHost[]): StoredHost[] {
	return [...hosts].sort((a, b) => b.lastConnected - a.lastConnected);
}

async function writeStored(hosts: StoredHost[]): Promise<void> {
	await AsyncStorage.setItem(HOSTS_KEY, JSON.stringify(sortHosts(hosts)));
}

// Several hosts can reconnect together. Serialize read-modify-write so one
// host's endpoint refresh cannot overwrite another's update.
let pendingWrite: Promise<void> = Promise.resolve();
function mutateStored(change: (hosts: StoredHost[]) => StoredHost[]): Promise<void> {
	const write = pendingWrite.then(async () => writeStored(change(await readStored())));
	pendingWrite = write.catch(() => {});
	return write;
}

/** Every paired machine, most recently connected first. */
export async function loadHosts(): Promise<Host[]> {
	const stored = sortHosts(await readStored());
	return Promise.all(
		stored.map(async (h) => ({
			...h,
			token: (await SecureStore.getItemAsync(tokenKey(h.id))) ?? "",
		})),
	);
}

/** One machine by id, or null. */
export async function findHost(id: string): Promise<Host | null> {
	return (await loadHosts()).find((h) => h.id === id) ?? null;
}

/** Adds or replaces a machine. Re-pairing the same machine updates it in place
 * rather than adding a second entry for it. */
export async function saveHost(host: Host): Promise<void> {
	const { token, ...rest } = host;
	await mutateStored((stored) => [rest, ...stored.filter((h) => h.id !== host.id)]);
	if (token) {
		await SecureStore.setItemAsync(tokenKey(host.id), token);
	} else {
		await SecureStore.deleteItemAsync(tokenKey(host.id));
	}
}

/** Rename a pairing without rewriting its credential or discovered endpoints. */
export async function renameHost(id: string, name: string): Promise<void> {
	const trimmed = name.trim();
	if (!trimmed) throw new Error("Machine name cannot be empty");
	await mutateStored((stored) => stored.map((host) => host.id === id ? { ...host, name: trimmed } : host));
}

/**
 * Replaces a machine's endpoint list, leaving its token alone.
 *
 * Called after every successful connect with whatever the daemon now
 * advertises, so a rotated tunnel hostname or a changed LAN address is picked
 * up without the user doing anything.
 */
export async function updateHostEndpoints(id: string, endpoints: Endpoint[]): Promise<void> {
	await mutateStored((stored) => stored.map((h) => (h.id === id ? { ...h, endpoints } : h)));
}

/** Records a successful connection, so the list orders by recency. */
export async function touchHost(id: string, at: number = Date.now()): Promise<void> {
	await mutateStored((stored) => stored.map((h) => (h.id === id ? { ...h, lastConnected: at } : h)));
}

/**
 * Forgets a machine.
 *
 * Both tiers are cleared: wiping only the AsyncStorage entry would leave the
 * token in the keystore, and a later re-pair of the same machine would silently
 * resurrect it.
 */
/**
 * The machine the app is talking to.
 *
 * Selection used to be emergent: loadHosts is ordered most-recent-first and
 * callers took the head. Nothing owned it, so nothing could change it — which
 * is why a manual connection snapped back to the previous machine on reload,
 * and why forgetting a server left it reachable.
 *
 * Recency remains the fallback, so a first pairing needs no explicit selection
 * and a stale pointer cannot strand the app with no host at all.
 */
export async function activeHost(): Promise<Host | null> {
	const host = await activeHostMetadata();
	if (!host) return null;
	return { ...host, token: (await SecureStore.getItemAsync(tokenKey(host.id))) ?? "" };
}

/** The selected machine without opening the token store. Cleanup uses this so
 * a keychain read failure cannot prevent the user from forgetting a server. */
export async function activeHostMetadata(): Promise<HostMetadata | null> {
	const hosts = sortHosts(await readStored());
	if (hosts.length === 0) return null;
	const selected = await AsyncStorage.getItem(ACTIVE_HOST_KEY);
	return hosts.find((h) => h.id === selected) ?? hosts[0];
}

/** Point the app at a machine, overriding recency until it changes again. */
export async function setActiveHost(id: string): Promise<void> {
	await AsyncStorage.setItem(ACTIVE_HOST_KEY, id);
}

export async function clearActiveHost(): Promise<void> {
	await AsyncStorage.removeItem(ACTIVE_HOST_KEY);
}

export async function removeHost(id: string): Promise<void> {
	await mutateStored((stored) => stored.filter((h) => h.id !== id));
	await SecureStore.deleteItemAsync(tokenKey(id));
	// A pointer at a machine that no longer exists would resolve to nothing;
	// clearing it falls back to recency instead.
	if ((await AsyncStorage.getItem(ACTIVE_HOST_KEY)) === id) await clearActiveHost();
}

const LEGACY_CONFIG_KEY = "ao.serverConfig";
const LEGACY_PASSWORD_KEY = "ao.serverPassword";

/**
 * Brings a pre-existing pairing forward into the host list.
 *
 * Every current user has exactly one machine stored the old way — a single
 * address, port, TLS flag and password. Skipping this would silently unpair all
 * of them on upgrade.
 *
 * The old config carries no host id, because the daemon only started issuing
 * them alongside the endpoint race. A migrated machine therefore starts with an
 * empty id and adopts one on its first successful connect. Until then its
 * identity cannot be verified — which is exactly how the app behaved before
 * this change, so nothing regresses, and it self-corrects after one connect.
 *
 * Idempotent, and it never runs over an existing list.
 */
/** The password older builds kept inside the AsyncStorage config blob. */
function legacyPassword(legacy: Record<string, unknown>): string {
	return typeof legacy.password === "string" ? legacy.password : "";
}

export async function migrateLegacyConfig(): Promise<void> {
	if ((await readStored()).length > 0) return;

	let legacy: Record<string, unknown>;
	try {
		const raw = await AsyncStorage.getItem(LEGACY_CONFIG_KEY);
		if (!raw) return;
		const parsed: unknown = JSON.parse(raw);
		if (typeof parsed !== "object" || parsed === null) return;
		legacy = parsed as Record<string, unknown>;
	} catch {
		return;
	}

	const host = typeof legacy.host === "string" ? legacy.host.trim() : "";
	if (!host) return;

	const port = Number(legacy.httpPort) || 3011;
	const isSecure = legacy.secure === true;
	// A Tailscale pairing is the only way the old flow produced a TLS endpoint,
	// so secure implies the tailnet path; everything else was plain LAN.
	const kind = isSecure ? "tailscale" : "lan";

	await saveHost({
		id: "",
		name: host,
		platform: "",
		endpoints: [{ kind, host, port, secure: isSecure }],
		// SecureStore first, then the config blob. Builds older than the
		// SecureStore move kept the password inside ao.serverConfig, and the
		// code that relocates it (loadConfig) runs *after* this migration — so
		// reading SecureStore alone handed those users an empty token and a
		// machine they could not authenticate to.
		token: (await SecureStore.getItemAsync(LEGACY_PASSWORD_KEY)) || legacyPassword(legacy),
		lastConnected: Date.now(),
	});
}

/**
 * Gives a migrated machine the identity it just reported.
 *
 * A pairing carried over from the single-server config has no id — the daemon
 * only began issuing them alongside the endpoint race — so it connects once
 * unverified and adopts one here. From then on every endpoint it races is
 * checked against this value.
 *
 * The token is moved to the new key as part of the same operation. Leaving it
 * under the old one would strand it, and the next connect would find a machine
 * it cannot authenticate to.
 *
 * Only rekeys a machine that genuinely has no identity; a machine that already
 * has one must never be renamed by whatever answered.
 */
export async function adoptHostIdentity(oldId: string, hostId: string): Promise<void> {
	if (oldId !== "" || hostId === "") return;

	const token = (await SecureStore.getItemAsync(tokenKey(""))) ?? "";
	let adopted = false;
	await mutateStored((stored) => {
		adopted = stored.some((h) => h.id === "");
		return stored.map((h) => (h.id === "" ? { ...h, id: hostId } : h));
	});
	if (!adopted) return;
	if (token) {
		await SecureStore.setItemAsync(tokenKey(hostId), token);
		await SecureStore.deleteItemAsync(tokenKey(""));
	}
}
