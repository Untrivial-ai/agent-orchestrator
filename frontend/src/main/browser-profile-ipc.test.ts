import { mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { BrowserProfileViewState } from "../shared/browser-profiles";
import type { BrowserImportProgress, BrowserImportRequest } from "../shared/browser-profile-import";
import { BrowserProfileStore } from "./browser-profile-store";
import { registerBrowserProfileIpc, type BrowserProfileMenuItem } from "./browser-profile-ipc";

type Handler = (event: { sender: object }, ...args: unknown[]) => unknown;

const labels = {
	temporary: "Temporary",
	manage: "Manage profiles",
	switchTitle: "Switch profile?",
	switchMessage: "Pages reload.",
	switchDetail: "Unsaved state may be lost.",
	cancel: "No",
	confirm: "Yes",
};

const tempDirectories: string[] = [];

afterEach(async () => {
	await Promise.all(tempDirectories.splice(0).map((directory) => rm(directory, { recursive: true, force: true })));
});

async function setup(confirmSwitch = vi.fn(async () => true)) {
	const stateDir = await mkdtemp(path.join(os.tmpdir(), "ao-browser-profile-ipc-"));
	tempDirectories.push(stateDir);
	const store = new BrowserProfileStore({ stateDir });
	await store.load();
	const shell = { send: vi.fn() };
	const renderer = { getZoomFactor: vi.fn(() => 1) };
	const handlers = new Map<string, Handler>();
	const menuPopup = vi.fn();
	let menuItems: BrowserProfileMenuItem[] = [];
	let state: BrowserProfileViewState = { viewId: "1:worker-1", profileId: null, temporary: true };
	let switchInfo = { hasNavigated: false, agentActive: false };
	const host = {
		isRendererOwned: vi.fn((event: { sender: object }, viewId: string) => event.sender === renderer && viewId === state.viewId),
		getProfileState: vi.fn((viewId: string) => (viewId === state.viewId ? state : null)),
		getProfileSwitchInfo: vi.fn(() => switchInfo),
		switchProfile: vi.fn(async (_viewId: string, profileId: string | null) => {
			state = { viewId: state.viewId, profileId, temporary: profileId === null };
			return state;
		}),
		isProfileLive: vi.fn((profileId: string) => state.profileId === profileId),
		clearProfileData: vi.fn(async () => undefined),
	};
	const ipcMain = {
		handle: (channel: string, handler: Handler) => handlers.set(channel, handler),
		removeHandler: (channel: string) => handlers.delete(channel),
	};
	const importer = {
		discover: vi.fn(async () => ({ sources: [] })),
		import: vi.fn(async (request: BrowserImportRequest, onProgress: (progress: BrowserImportProgress) => void) => {
			onProgress({ requestId: request.requestId, phase: "reading", completed: 1, total: 1 });
			return { sourceName: "Chrome", entries: [] };
		}),
	};
	const ipc = registerBrowserProfileIpc({
		ipcMain: ipcMain as never,
		shellWebContents: shell as never,
		mainWindow: {},
		store,
		importer: importer as never,
		host,
		buildMenu: (items) => {
			menuItems = items;
			return { popup: menuPopup };
		},
		confirmSwitch,
	});
	const invoke = (channel: string, sender: object, ...args: unknown[]) => handlers.get(channel)!({ sender }, ...args);
	return { host, importer, invoke, ipc, menuItems: () => menuItems, menuPopup, renderer, shell, state: () => state, store };
}

describe("browser profile IPC", () => {
	it("rejects untrusted profile management and invalid renderer ownership", async () => {
		const { invoke, renderer, host } = await setup();

		expect(await invoke("browserProfiles:list", renderer)).toEqual({ profiles: [] });
		await expect(invoke("browserProfiles:create", renderer, { name: "Work" })).rejects.toMatchObject({
			code: "INVALID_ARGUMENT",
		});
		expect(await invoke("browser:profile:get", renderer, "2:other")).toEqual({
			viewId: "",
			profileId: null,
			temporary: true,
		});
		expect(await invoke("browser:profile:menu", renderer, { viewId: "2:other", bounds: {}, labels })).toBeUndefined();
		expect(() => invoke("browser:profile:menu", renderer, { viewId: "1:worker-1", bounds: {}, labels })).toThrow(
			"Browser profile menu bounds are invalid.",
		);
		expect(() => invoke("browser:profile:menu", renderer, {
			viewId: "1:worker-1",
			bounds: { x: 100_001, y: 0, width: 40, height: 20 },
			labels,
		})).toThrow("Browser profile menu bounds are invalid.");
		expect(host.switchProfile).not.toHaveBeenCalled();
	});

	it("builds a native menu with bounded coordinates and switches only through menu actions", async () => {
		const { invoke, renderer, store, menuItems: getMenuItems, menuPopup, host } = await setup();
		const profile = await store.createProfile("Work");
		renderer.getZoomFactor.mockReturnValue(1.5);

		await invoke("browser:profile:menu", renderer, {
			viewId: "1:worker-1",
			bounds: { x: 12.4, y: 34.6, width: 120, height: 28 },
			labels,
		});
		expect(getMenuItems().map((item) => item.label ?? item.type)).toEqual(["Temporary", "Work", "separator", "Manage profiles"]);
		expect(getMenuItems().slice(0, 2)).toEqual([
			expect.objectContaining({ type: "radio", checked: true, enabled: true }),
			expect.objectContaining({ type: "radio", checked: false, enabled: true }),
		]);
		expect(menuPopup).toHaveBeenCalledWith({ window: {}, x: 19, y: 94 });
		await getMenuItems()[1]!.click?.();
		await new Promise<void>((resolve) => setImmediate(resolve));
		expect(host.switchProfile).toHaveBeenCalledWith("1:worker-1", profile.id);
		// The menu is native: opening it only builds/pops the menu and never changes
		// the browser view bounds or paints a renderer overlay over the page.
		expect(host.isRendererOwned).toHaveBeenCalledWith(expect.objectContaining({ sender: renderer }), "1:worker-1");
	});

	it("switches profiles through the renderer-owned AO menu endpoint", async () => {
		const { invoke, renderer, store, host } = await setup();
		const profile = await store.createProfile("Work");

		await invoke("browser:profile:select", renderer, {
			viewId: "1:worker-1",
			profileId: profile.id,
			labels,
		});

		expect(host.switchProfile).toHaveBeenCalledWith("1:worker-1", profile.id);
	});

	it("requires confirmation for a loaded page and refuses switching during agent activity", async () => {
		const confirmSwitch = vi.fn(async () => false);
		const { invoke, renderer, store, host, menuItems: getMenuItems } = await setup(confirmSwitch);
		const profile = await store.createProfile("Work");
		host.getProfileSwitchInfo.mockReturnValue({ hasNavigated: true, agentActive: false });
		await invoke("browser:profile:menu", renderer, {
			viewId: "1:worker-1",
			bounds: { x: 1, y: 1, width: 40, height: 20 },
			labels,
		});
		await getMenuItems().find((item) => item.label === profile.name)!.click?.();
		await new Promise<void>((resolve) => setImmediate(resolve));
		expect(confirmSwitch).toHaveBeenCalledWith(labels);
		expect(host.switchProfile).not.toHaveBeenCalled();

		host.getProfileSwitchInfo.mockReturnValue({ hasNavigated: false, agentActive: true });
		await invoke("browser:profile:menu", renderer, {
			viewId: "1:worker-1",
			bounds: { x: 1, y: 1, width: 40, height: 20 },
			labels,
		});
		expect(getMenuItems().slice(0, 2).every((item) => item.enabled === false)).toBe(true);
		await getMenuItems().find((item) => item.label === profile.name)!.click?.();
		await new Promise<void>((resolve) => setImmediate(resolve));
		expect(host.switchProfile).not.toHaveBeenCalled();
	});

	it("refuses clear/delete while live and cleans storage before deleting an idle profile", async () => {
		const { invoke, shell, store, host, state } = await setup();
		const profile = await store.createProfile("Work");
		state().profileId = profile.id;
		state().temporary = false;

		await expect(invoke("browserProfiles:clear", shell, { id: profile.id })).rejects.toMatchObject({
			code: "BROWSER_PROFILE_ACTIVE",
		});
		await expect(invoke("browserProfiles:delete", shell, { id: profile.id })).rejects.toMatchObject({
			code: "BROWSER_PROFILE_ACTIVE",
		});
		expect(host.clearProfileData).not.toHaveBeenCalled();

		state().profileId = null;
		state().temporary = true;
		await invoke("browserProfiles:delete", shell, { id: profile.id });
		expect(host.clearProfileData).toHaveBeenCalledWith(profile.id);
		expect(store.getProfile(profile.id)).toBeUndefined();
	});

	it("allows import only from the trusted shell and forwards scoped progress", async () => {
		const { importer, invoke, renderer, shell } = await setup();
		const request = {
			requestId: "11111111-1111-4111-8111-111111111111",
			sourceId: "a".repeat(32),
			profileIds: ["b".repeat(32)],
			includeCookies: true,
			includeHistory: true,
			destination: { mode: "merge", name: "Imported" },
		};

		expect(await invoke("browserProfiles:import:discover", renderer)).toEqual({ sources: [] });
		await expect(invoke("browserProfiles:import:start", renderer, request)).rejects.toThrow("Untrusted");
		expect(importer.discover).not.toHaveBeenCalled();
		expect(importer.import).not.toHaveBeenCalled();

		await expect(invoke("browserProfiles:import:discover", shell)).resolves.toEqual({ sources: [] });
		await expect(invoke("browserProfiles:import:start", shell, request)).resolves.toEqual({
			sourceName: "Chrome",
			entries: [],
		});
		expect(importer.import).toHaveBeenCalledWith(request, expect.any(Function));
		expect(shell.send).toHaveBeenCalledWith("browserProfiles:import:progress", {
			requestId: request.requestId,
			phase: "reading",
			completed: 1,
			total: 1,
		});
	});

	it("disposes every registered handler", async () => {
		const { invoke, ipc, renderer } = await setup();
		await invoke("browserProfiles:list", renderer);
		ipc.dispose();
		expect(() => invoke("browserProfiles:list", renderer)).toThrow();
	});
});
