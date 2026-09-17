import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Endpoint } from "./endpoints";

const plain = new Map<string, string>();
const secure = new Map<string, string>();

vi.mock("@react-native-async-storage/async-storage", () => ({
	default: {
		getItem: vi.fn(async (k: string) => plain.get(k) ?? null),
		setItem: vi.fn(async (k: string, v: string) => void plain.set(k, v)),
		removeItem: vi.fn(async (k: string) => void plain.delete(k)),
	},
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(async (k: string) => secure.get(k) ?? null),
	setItemAsync: vi.fn(async (k: string, v: string) => void secure.set(k, v)),
	deleteItemAsync: vi.fn(async (k: string) => void secure.delete(k)),
}));

const lan = (host: string): Endpoint => ({ kind: "lan", host, port: 3011, secure: false });

async function mod() {
	return await import("./hosts");
}

describe("host store", () => {
	beforeEach(() => {
		plain.clear();
		secure.clear();
		vi.resetModules();
	});

	it("saves a host and reads it back", async () => {
		const { saveHost, loadHosts } = await mod();
		await saveHost({
			id: "h_one",
			name: "prasad-mbp",
			platform: "darwin",
			endpoints: [lan("192.168.1.42")],
			token: "pw",
			lastConnected: 1,
		});

		const got = await loadHosts();
		expect(got).toHaveLength(1);
		expect(got[0].id).toBe("h_one");
		expect(got[0].endpoints).toEqual([lan("192.168.1.42")]);
	});

	// The whole point of the workstream: more than one machine.
	it("keeps several machines, most recently connected first", async () => {
		const { saveHost, loadHosts } = await mod();
		await saveHost({ id: "h_old", name: "a", platform: "linux", endpoints: [], token: "", lastConnected: 10 });
		await saveHost({ id: "h_new", name: "b", platform: "darwin", endpoints: [], token: "", lastConnected: 99 });

		const got = await loadHosts();
		expect(got.map((h) => h.id)).toEqual(["h_new", "h_old"]);
	});

	it("replaces a host rather than duplicating it on re-pair", async () => {
		const { saveHost, loadHosts } = await mod();
		await saveHost({ id: "h_one", name: "before", platform: "darwin", endpoints: [], token: "", lastConnected: 1 });
		await saveHost({ id: "h_one", name: "after", platform: "darwin", endpoints: [], token: "", lastConnected: 2 });

		const got = await loadHosts();
		expect(got).toHaveLength(1);
		expect(got[0].name).toBe("after");
	});

	// The connection token authorises terminal input, spawns and PR actions. It
	// must never reach AsyncStorage, which is plaintext in the app sandbox.
	it("keeps the token out of plaintext storage", async () => {
		const { saveHost } = await mod();
		await saveHost({
			id: "h_one", name: "a", platform: "darwin",
			endpoints: [], token: "super-secret", lastConnected: 1,
		});

		const plaintext = JSON.stringify([...plain.values()]);
		expect(plaintext).not.toContain("super-secret");
		expect([...secure.values()]).toContain("super-secret");
	});

	it("forgets both tiers when a machine is removed", async () => {
		const { saveHost, removeHost, loadHosts } = await mod();
		await saveHost({
			id: "h_one", name: "a", platform: "darwin",
			endpoints: [], token: "super-secret", lastConnected: 1,
		});

		await removeHost("h_one");

		expect(await loadHosts()).toHaveLength(0);
		// Leaving the token behind would silently resurrect it on a later re-pair.
		expect([...secure.values()]).not.toContain("super-secret");
	});

	// Endpoints are refreshed on every successful connect, which is what makes a
	// rotated tunnel hostname or a new LAN address self-heal.
	it("refreshes a host's endpoints without touching its token", async () => {
		const { saveHost, updateHostEndpoints, loadHosts } = await mod();
		await saveHost({
			id: "h_one", name: "a", platform: "darwin",
			endpoints: [lan("192.168.1.42")], token: "pw", lastConnected: 1,
		});

		await updateHostEndpoints("h_one", [lan("10.0.0.5")]);

		const got = await loadHosts();
		expect(got[0].endpoints).toEqual([lan("10.0.0.5")]);
		expect(got[0].token).toBe("pw");
	});

	it("caps the list so it cannot grow without bound", async () => {
		const { saveHost, loadHosts, MAX_HOSTS } = await mod();
		for (let i = 0; i < MAX_HOSTS + 5; i++) {
			await saveHost({
				id: `h_${i}`, name: `m${i}`, platform: "darwin",
				endpoints: [], token: "", lastConnected: i,
			});
		}

		const got = await loadHosts();
		expect(got).toHaveLength(MAX_HOSTS);
		// The oldest fall off, not the newest.
		expect(got[0].id).toBe(`h_${MAX_HOSTS + 4}`);
	});

	it("survives corrupted storage instead of crashing the app", async () => {
		plain.set("ao.hosts", "{not json");
		const { loadHosts } = await mod();
		expect(await loadHosts()).toEqual([]);
	});
});

describe("migration from the single-server config", () => {
	beforeEach(() => {
		plain.clear();
		secure.clear();
		vi.resetModules();
	});

	// Anyone upgrading has one paired machine stored the old way. Losing it
	// would silently unpair every existing user.
	it("carries an existing pairing across the upgrade", async () => {
		plain.set(
			"ao.serverConfig",
			JSON.stringify({ host: "192.168.1.42", httpPort: "3011", muxPort: "14801", secure: false }),
		);
		secure.set("ao.serverPassword", "legacy-pw");

		const { migrateLegacyConfig, loadHosts } = await mod();
		await migrateLegacyConfig();

		const got = await loadHosts();
		expect(got).toHaveLength(1);
		expect(got[0].endpoints).toEqual([
			{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false },
		]);
		expect(got[0].token).toBe("legacy-pw");
	});

	// Builds older still kept the password inside the AsyncStorage config blob;
	// loadConfig() is what moves it into SecureStore, and that runs *after* this
	// migration. Reading SecureStore alone left those users with an empty token
	// and a machine they could not authenticate to.
	it("takes the password from the legacy blob when SecureStore has none", async () => {
		plain.set(
			"ao.serverConfig",
			JSON.stringify({ host: "192.168.1.42", httpPort: "3011", password: "blob-pw" }),
		);

		const { migrateLegacyConfig, loadHosts } = await mod();
		await migrateLegacyConfig();

		expect((await loadHosts())[0].token).toBe("blob-pw");
	});

	// SecureStore is the newer home for it, so it wins where both exist.
	it("prefers the SecureStore password over the legacy blob", async () => {
		plain.set(
			"ao.serverConfig",
			JSON.stringify({ host: "192.168.1.42", httpPort: "3011", password: "blob-pw" }),
		);
		secure.set("ao.serverPassword", "secure-pw");

		const { migrateLegacyConfig, loadHosts } = await mod();
		await migrateLegacyConfig();

		expect((await loadHosts())[0].token).toBe("secure-pw");
	});

	// The old config carries no host id — the daemon issues those, and this
	// pairing predates them. The migrated machine therefore starts unverified
	// and adopts its identity on the first successful connect.
	it("leaves the migrated machine without an identity until it connects", async () => {
		plain.set("ao.serverConfig", JSON.stringify({ host: "192.168.1.42", httpPort: "3011" }));
		const { migrateLegacyConfig, loadHosts } = await mod();
		await migrateLegacyConfig();

		expect((await loadHosts())[0].id).toBe("");
	});

	it("preserves a TLS pairing as a secure endpoint", async () => {
		plain.set(
			"ao.serverConfig",
			JSON.stringify({ host: "mbp.tail1234.ts.net", httpPort: "443", secure: true }),
		);
		const { migrateLegacyConfig, loadHosts } = await mod();
		await migrateLegacyConfig();

		expect((await loadHosts())[0].endpoints[0]).toEqual({
			kind: "tailscale",
			host: "mbp.tail1234.ts.net",
			port: 443,
			secure: true,
		});
	});

	it("is a no-op when there is nothing to migrate", async () => {
		const { migrateLegacyConfig, loadHosts } = await mod();
		await migrateLegacyConfig();
		expect(await loadHosts()).toEqual([]);
	});

	// Running twice must not produce two copies of the same machine.
	it("is idempotent", async () => {
		plain.set("ao.serverConfig", JSON.stringify({ host: "192.168.1.42", httpPort: "3011" }));
		const { migrateLegacyConfig, loadHosts } = await mod();
		await migrateLegacyConfig();
		await migrateLegacyConfig();

		expect(await loadHosts()).toHaveLength(1);
	});

	it("does not migrate over an already-populated host list", async () => {
		plain.set("ao.serverConfig", JSON.stringify({ host: "192.168.1.42", httpPort: "3011" }));
		const { saveHost, migrateLegacyConfig, loadHosts } = await mod();
		await saveHost({
			id: "h_real", name: "a", platform: "darwin",
			endpoints: [], token: "", lastConnected: 5,
		});

		await migrateLegacyConfig();

		const got = await loadHosts();
		expect(got).toHaveLength(1);
		expect(got[0].id).toBe("h_real");
	});
});

describe("adopting an identity", () => {
	beforeEach(() => {
		plain.clear();
		secure.clear();
		vi.resetModules();
	});

	// A machine migrated from the single-server config connects once with no id
	// and learns one. Its token has to move with it, or the next connect finds
	// a machine it cannot authenticate to.
	it("rekeys a migrated machine and carries its token across", async () => {
		const { saveHost, adoptHostIdentity, loadHosts } = await mod();
		await saveHost({
			id: "", name: "192.168.1.42", platform: "",
			endpoints: [lan("192.168.1.42")], token: "legacy-pw", lastConnected: 1,
		});

		await adoptHostIdentity("", "h_learned");

		const got = await loadHosts();
		expect(got).toHaveLength(1);
		expect(got[0].id).toBe("h_learned");
		expect(got[0].token).toBe("legacy-pw");
	});

	it("leaves an already-identified machine alone", async () => {
		const { saveHost, adoptHostIdentity, loadHosts } = await mod();
		await saveHost({
			id: "h_real", name: "a", platform: "darwin",
			endpoints: [], token: "pw", lastConnected: 1,
		});

		await adoptHostIdentity("h_real", "h_something_else");

		expect((await loadHosts())[0].id).toBe("h_real");
	});
})

// Which machine is "active" used to be emergent — loadHosts is ordered
// most-recent-first and callers took hosts[0]. That is why a manual connection
// snapped back to the previous machine on reload, and why forgetting a server
// left it reachable: nothing owned the selection, so nothing could change it.
describe("the active host", () => {
	beforeEach(() => {
		plain.clear();
		secure.clear();
		vi.resetModules();
	});

	const host = (id: string) => ({
		id,
		name: id,
		platform: "darwin",
		endpoints: [{ kind: "lan" as const, host: "192.168.1.42", port: 3011, secure: false }],
		token: `tok-${id}`,
		lastConnected: 1,
	});

	it("falls back to the most recent machine when nothing is selected", async () => {
		const { saveHost, activeHost } = await mod();
		await saveHost(host("a"));
		await saveHost(host("b"));

		expect((await activeHost())?.id).toBe("b");
	});

	it("honours an explicit selection over recency", async () => {
		const { saveHost, setActiveHost, activeHost } = await mod();
		await saveHost(host("a"));
		await saveHost(host("b"));

		await setActiveHost("a");

		expect((await activeHost())?.id).toBe("a");
	});

	it("reads active host metadata without opening the token store", async () => {
		const SecureStore = await import("expo-secure-store");
		const { saveHost, setActiveHost, activeHostMetadata } = await mod();
		await saveHost(host("a"));
		await setActiveHost("a");
		vi.mocked(SecureStore.getItemAsync).mockClear();

		const got = await activeHostMetadata();

		expect(got?.id).toBe("a");
		expect(SecureStore.getItemAsync).not.toHaveBeenCalled();
	});

	// A selection pointing at a machine that has since been forgotten must not
	// strand the app with no host at all.
	it("falls back to recency when the selected machine is gone", async () => {
		const { saveHost, setActiveHost, removeHost, activeHost } = await mod();
		await saveHost(host("a"));
		await saveHost(host("b"));
		await setActiveHost("a");

		await removeHost("a");

		expect((await activeHost())?.id).toBe("b");
	});

	it("has no active host once every machine is forgotten", async () => {
		const { saveHost, removeHost, activeHost } = await mod();
		await saveHost(host("a"));
		await removeHost("a");

		expect(await activeHost()).toBeNull();
	});

	// Forgetting must take the credential with it, or the machine stays
	// reachable by anything that reads the token store.
	it("drops the token when a machine is forgotten", async () => {
		const { saveHost, removeHost } = await mod();
		await saveHost(host("a"));
		expect(secure.get("ao.hostToken.a")).toBe("tok-a");

		await removeHost("a");

		expect(secure.get("ao.hostToken.a")).toBeUndefined();
	});
});
