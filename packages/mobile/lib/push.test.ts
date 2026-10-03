import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ServerConfig } from "./config";

const plain = new Map<string, string>();
const secure = new Map<string, string>();

vi.mock("@react-native-async-storage/async-storage", () => ({
	default: {
		getItem: vi.fn(async (key: string) => plain.get(key) ?? null),
		setItem: vi.fn(async (key: string, value: string) => void plain.set(key, value)),
		removeItem: vi.fn(async (key: string) => void plain.delete(key)),
	},
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(async (key: string) => secure.get(key) ?? null),
	setItemAsync: vi.fn(async (key: string, value: string) => void secure.set(key, value)),
	deleteItemAsync: vi.fn(async (key: string) => void secure.delete(key)),
}));
vi.mock("expo-constants", () => ({ default: { expoConfig: { extra: { eas: { projectId: "test-project" } } } } }));
vi.mock("expo-device", () => ({ isDevice: true, deviceName: "phone" }));
vi.mock("expo-notifications", () => ({
	getPermissionsAsync: vi.fn(async () => ({ status: "granted", canAskAgain: true })),
	getExpoPushTokenAsync: vi.fn(async () => ({ data: "ExponentPushToken[test]" })),
}));
vi.mock("react-native", () => ({ Platform: { OS: "ios" }, Linking: { openSettings: vi.fn() } }));
vi.mock("./installId", () => ({ getInstallId: vi.fn(async () => "phone-install-id") }));
vi.mock("./api", () => ({
	ApiError: class ApiError extends Error {},
	registerPushDevice: vi.fn(async () => {}),
	unregisterPushDevice: vi.fn(async () => {}),
	unpairFromDaemon: vi.fn(async () => {}),
}));

const { getPushStatus, onManualPushRegistration, registerForPush, unregisterFromPush } = await import("./push");
const { registerPushDevice, unpairFromDaemon, unregisterPushDevice } = await import("./api");
const { forgetServer } = await import("./disconnect");
const { loadHosts, saveHost, setActiveHost } = await import("./hosts");

function config(hostId: string, host = "192.168.1.42"): ServerConfig {
	return { hostId, host, httpPort: "3011", muxPort: "", secure: false, password: `token-${hostId}` };
}

describe("push registration across machines", () => {
	beforeEach(() => {
		plain.clear();
		secure.clear();
		vi.clearAllMocks();
		vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, json: async () => ({ hostId: "h_b", apiVersion: 1 }) })));
	});

	it("does not send A's bearer to B when B takes A's old address", async () => {
		await registerForPush(config("h_a"));
		await registerForPush(config("h_b"));

		expect(unregisterPushDevice).not.toHaveBeenCalled();
		expect((await getPushStatus(config("h_b"))).registered).toBe(true);
	});

	it("keeps A registered when B connects", async () => {
		await registerForPush(config("h_a"));
		vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, json: async () => ({ hostId: "h_a", apiVersion: 1 }) })));

		await registerForPush(config("h_b", "100.101.102.103"));

		expect(unregisterPushDevice).not.toHaveBeenCalled();
		expect((await getPushStatus(config("h_a"))).registered).toBe(true);
		expect((await getPushStatus(config("h_b", "100.101.102.103"))).registered).toBe(true);
	});

	it("registers the paired host label for OS banners on each machine", async () => {
		await saveHost({ id: "h_a", name: "Host A", platform: "linux", endpoints: [], token: "token-h_a", lastConnected: 1 });
		await saveHost({ id: "h_b", name: "Host B", platform: "linux", endpoints: [], token: "token-h_b", lastConnected: 2 });
		await registerForPush(config("h_a"));
		await registerForPush(config("h_b"));
		expect(registerPushDevice).toHaveBeenNthCalledWith(1, config("h_a"), expect.objectContaining({ hostName: "Host A" }));
		expect(registerPushDevice).toHaveBeenNthCalledWith(2, config("h_b"), expect.objectContaining({ hostName: "Host B" }));
	});

	it("keeps one registration when the same machine changes address", async () => {
		await registerForPush(config("h_a"));
		await registerForPush(config("h_a", "100.101.102.103"));

		expect(unregisterPushDevice).not.toHaveBeenCalled();
	});

	it("refreshes connected machines after the user enables push", async () => {
		const refresh = vi.fn();
		const off = onManualPushRegistration(refresh);
		await registerForPush(config("h_a"), { ask: true });
		await registerForPush(config("h_b"), { ask: false });
		off();

		expect(refresh).toHaveBeenCalledOnce();
	});

	it("keeps both registrations when A's earlier request finishes late", async () => {
		let signalA!: () => void;
		let releaseA!: () => void;
		const aStarted = new Promise<void>((resolve) => { signalA = resolve; });
		const aCanFinish = new Promise<void>((resolve) => { releaseA = resolve; });
		vi.mocked(registerPushDevice).mockImplementationOnce(async () => {
			signalA();
			await aCanFinish;
		});
		vi.stubGlobal("fetch", vi.fn(async (url: string) => ({
			ok: true,
			json: async () => ({ hostId: url.includes("192.168.1.42") ? "h_a" : "h_b", apiVersion: 1 }),
		})));

		const onA = registerForPush(config("h_a"));
		await aStarted;
		const onB = registerForPush(config("h_b", "100.101.102.103"));
		await new Promise((resolve) => setTimeout(resolve, 0));
		releaseA();
		await Promise.all([onA, onB]);

		expect((await getPushStatus(config("h_a"))).registered).toBe(true);
		expect((await getPushStatus(config("h_b", "100.101.102.103"))).registered).toBe(true);
		expect(unregisterPushDevice).not.toHaveBeenCalled();
	});

	it("migrates the saved single-host registration without losing its status", async () => {
		const old = { token: "ExponentPushToken[test]", hostId: "h_a", host: "192.168.1.42", httpPort: "3011", secure: false, password: "token-h_a" };
		secure.set("ao.pushRegistration", JSON.stringify(old));

		expect((await getPushStatus(config("h_b"))).registered).toBe(false);
		expect((await getPushStatus(config("h_a"))).registered).toBe(true);
		expect(secure.has("ao.pushRegistration")).toBe(false);
		expect(JSON.parse(secure.get("ao.pushRegistration.h_a") ?? "null")).toEqual(old);
	});

	it("replaces a legacy registration without sending its bearer to an unverified address", async () => {
		await registerForPush({ ...config("h_a"), hostId: undefined });
		expect((await getPushStatus(config("h_a"))).registered).toBe(false);
		await registerForPush(config("h_a"));

		expect(unregisterPushDevice).not.toHaveBeenCalled();
		expect((await getPushStatus(config("h_a"))).registered).toBe(true);
	});

	it("replays only pending unregisters whose old host identity is verified", async () => {
		const old = { token: "ExponentPushToken[old]", host: "192.168.1.42", httpPort: "3011", secure: false, password: "old-secret" };
		secure.set("ao.pushPendingUnregister", JSON.stringify([{ ...old, hostId: "h_a" }, old]));

		await registerForPush(config("h_b"));
		expect(unregisterPushDevice).not.toHaveBeenCalled();

		vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, json: async () => ({ hostId: "h_a", apiVersion: 1 }) })));
		await registerForPush(config("h_b"));
		expect(unregisterPushDevice).toHaveBeenCalledExactlyOnceWith(
			expect.objectContaining({ hostId: "h_a", host: old.host, password: old.password }),
			"ExponentPushToken[old]",
		);
		expect(secure.has("ao.pushPendingUnregister")).toBe(false);
	});

	it("drops an old switch-time unregister when that host remains registered", async () => {
		await registerForPush(config("h_a"));
		secure.set("ao.pushPendingUnregister", JSON.stringify([{
			token: "ExponentPushToken[test]", hostId: "h_a", host: "192.168.1.42", httpPort: "3011", secure: false, password: "token-h_a",
		}]));
		vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, json: async () => ({ hostId: "h_a", apiVersion: 1 }) })));

		await registerForPush(config("h_b"));

		expect(unregisterPushDevice).not.toHaveBeenCalled();
		expect(secure.has("ao.pushPendingUnregister")).toBe(false);
		expect((await getPushStatus(config("h_a"))).registered).toBe(true);
	});

	it("turns off B without changing A's registration", async () => {
		await registerForPush(config("h_a"));
		await registerForPush(config("h_b", "100.101.102.103"));
		vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, json: async () => ({ hostId: "h_b", apiVersion: 1 }) })));

		await unregisterFromPush(config("h_b", "100.101.102.103"));

		expect((await getPushStatus(config("h_a"))).registered).toBe(true);
		expect((await getPushStatus(config("h_b", "100.101.102.103"))).registered).toBe(false);
		expect(unregisterPushDevice).toHaveBeenCalledExactlyOnceWith(config("h_b", "100.101.102.103"), "ExponentPushToken[test]");
	});

	it("turns A's local switch off without sending A's bearer to a replacement host", async () => {
		await registerForPush(config("h_a"));

		await unregisterFromPush(config("h_a"));

		expect(unregisterPushDevice).not.toHaveBeenCalled();
		expect((await getPushStatus(config("h_a"))).registered).toBe(false);
	});

	it("forgets a matching legacy registration locally without sending its old bearer", async () => {
		const legacy = { ...config("h_a"), hostId: undefined, password: "legacy-secret" };
		await registerForPush(legacy);
		await saveHost({
			id: "h_a", name: "A", platform: "linux",
			endpoints: [{ kind: "lan", host: legacy.host, port: 3011, secure: false }],
			token: "current-pairing-token", lastConnected: 1,
		});
		await setActiveHost("h_a");
		vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, json: async () => ({ hostId: "h_a", apiVersion: 1 }) })));

		expect((await getPushStatus(legacy)).registered).toBe(true);
		await forgetServer();

		expect((await getPushStatus(legacy)).registered).toBe(false);
		expect(unregisterPushDevice).not.toHaveBeenCalled();
		expect(unpairFromDaemon).toHaveBeenCalledExactlyOnceWith(
			expect.objectContaining({ hostId: "h_a", password: "current-pairing-token" }),
			"phone-install-id",
		);
	});

	it("forgets selected B without unpairing A or clearing A's push registration", async () => {
		await registerForPush(config("h_a"));
		await registerForPush(config("h_b"));
		await saveHost({ id: "h_a", name: "A", platform: "darwin", endpoints: [], token: "token-h_a", lastConnected: 1 });
		await saveHost({
			id: "h_b", name: "B", platform: "linux",
			endpoints: [{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false }],
			token: "token-h_b", lastConnected: 2,
		});
		await setActiveHost("h_b");

		await forgetServer();

		expect(unpairFromDaemon).toHaveBeenCalledExactlyOnceWith(
			expect.objectContaining({ hostId: "h_b", host: "192.168.1.42", password: "token-h_b" }),
			"phone-install-id",
		);
		expect((await loadHosts()).map((host) => host.id)).toEqual(["h_a"]);
		expect((await getPushStatus(config("h_a"))).registered).toBe(true);
		expect((await getPushStatus(config("h_b"))).registered).toBe(false);
	});

	it("clears B's registration when forgetting B with no reachable endpoints", async () => {
		await registerForPush(config("h_b"));
		await saveHost({ id: "h_b", name: "B", platform: "linux", endpoints: [], token: "token-h_b", lastConnected: 2 });
		await setActiveHost("h_b");

		await forgetServer();

		expect((await getPushStatus(config("h_b"))).registered).toBe(false);
	});

	it("does not send B's credential when its saved address answers as A", async () => {
		await registerForPush(config("h_a"));
		await saveHost({
			id: "h_b", name: "B", platform: "linux",
			endpoints: [{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false }],
			token: "token-h_b", lastConnected: 2,
		});
		await setActiveHost("h_b");
		vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, json: async () => ({ hostId: "h_a", apiVersion: 1 }) })));

		await forgetServer();

		expect(unpairFromDaemon).not.toHaveBeenCalled();
		expect((await getPushStatus(config("h_a"))).registered).toBe(true);
	});
});
