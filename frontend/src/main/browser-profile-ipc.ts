import type { IpcMain, IpcMainInvokeEvent, WebContents } from "electron";
import {
	normalizeBrowserProfileId,
	type BrowserMenuBounds,
	type BrowserProfile,
	type BrowserProfileListState,
	type BrowserProfileMenuInput,
	type BrowserProfileSelectInput,
	type BrowserProfileViewState,
} from "../shared/browser-profiles";
import type { BrowserImportRequest } from "../shared/browser-profile-import";
import type { BrowserProfileImportService } from "./browser-profile-import";
import { BrowserProfileStore, BrowserProfileStoreError as StoreError } from "./browser-profile-store";

export type BrowserProfileMenuItem = {
	label?: string;
	type?: "separator" | "radio";
	checked?: boolean;
	enabled?: boolean;
	click?: () => void;
};

export type BrowserProfileMenu = {
	popup: (options: { window: unknown; x: number; y: number }) => void;
};

export type BrowserProfileIpcHost = {
	isRendererOwned: (event: IpcMainInvokeEvent, viewId: string) => boolean;
	getProfileState: (viewId: string) => BrowserProfileViewState | null;
	refreshProfileState?: (profileId: string) => void;
	getProfileSwitchInfo: (viewId: string) => { hasNavigated: boolean; agentActive: boolean } | null;
	switchProfile: (viewId: string, profileId: string | null) => Promise<BrowserProfileViewState>;
	isProfileLive: (profileId: string) => boolean;
	clearProfileData: (profileId: string) => Promise<void>;
};

export type BrowserProfileIpcOptions = {
	ipcMain: Pick<IpcMain, "handle" | "removeHandler">;
	shellWebContents: WebContents;
	mainWindow: unknown;
	store: BrowserProfileStore;
	host: BrowserProfileIpcHost;
	importer: BrowserProfileImportService;
	buildMenu: (items: BrowserProfileMenuItem[]) => BrowserProfileMenu;
	confirmSwitch: (labels: BrowserProfileMenuInput["labels"]) => Promise<boolean>;
};

export type BrowserProfileIpc = {
	dispose: () => void;
};

const EMPTY_PROFILE_STATE: BrowserProfileViewState = {
	viewId: "",
	profileId: null,
	temporary: true,
};

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function invalid(message: string): StoreError {
	return new StoreError("INVALID_ARGUMENT", message);
}

function profileIdFromInput(value: unknown): string {
	const id = normalizeBrowserProfileId(value);
	if (!id) throw invalid("Profile ID is invalid.");
	return id;
}

function viewIdFromInput(value: unknown): string {
	if (typeof value !== "string" || value.length === 0 || value.length > 300 || /[\u0000-\u001f\u007f]/u.test(value)) {
		throw invalid("Browser view ID is invalid.");
	}
	return value;
}

function menuBoundsFromInput(value: unknown): BrowserMenuBounds {
	if (!isRecord(value)) throw invalid("Browser profile menu bounds are invalid.");
	const x = value.x;
	const y = value.y;
	const width = value.width;
	const height = value.height;
	if (
		typeof x !== "number" ||
		typeof y !== "number" ||
		typeof width !== "number" ||
		typeof height !== "number" ||
		!Number.isFinite(x) ||
		!Number.isFinite(y) ||
		!Number.isFinite(width) ||
		!Number.isFinite(height) ||
		x < 0 ||
		y < 0 ||
		x > 100_000 ||
		y > 100_000 ||
		width <= 0 ||
		height <= 0 ||
		width > 2_000 ||
		height > 2_000
	) {
		throw invalid("Browser profile menu bounds are invalid.");
	}
	return { x, y, width, height };
}

function labelsFromInput(value: unknown): BrowserProfileMenuInput["labels"] {
	if (!isRecord(value)) throw invalid("Browser profile menu labels are invalid.");
	const keys = ["temporary", "manage", "switchTitle", "switchMessage", "switchDetail", "cancel", "confirm"] as const;
	const labels = {} as BrowserProfileMenuInput["labels"];
	for (const key of keys) {
		const label = value[key];
		if (typeof label !== "string" || label.length === 0 || label.length > 300 || /[\u0000-\u001f\u007f]/u.test(label)) {
			throw invalid("Browser profile menu labels are invalid.");
		}
		labels[key] = label;
	}
	return labels;
}

function trustedShellSender(event: IpcMainInvokeEvent, shellWebContents: WebContents): boolean {
	return event.sender === shellWebContents;
}

function listStateWithStoreError(store: BrowserProfileStore): BrowserProfileListState {
	return { profiles: store.profiles, ...(store.error ? { error: store.error } : {}) };
}

function profileLabel(profile: BrowserProfile): string {
	return profile.name;
}

export function registerBrowserProfileIpc(options: BrowserProfileIpcOptions): BrowserProfileIpc {
	const disposers: Array<() => void> = [];
	const handle = <Args extends unknown[], Result>(
		channel: string,
		fn: (event: IpcMainInvokeEvent, ...args: Args) => Result,
	): void => {
		options.ipcMain.handle(channel, fn);
		disposers.push(() => options.ipcMain.removeHandler(channel));
	};

	handle("browserProfiles:list", async (event) => {
		if (!trustedShellSender(event, options.shellWebContents)) return { profiles: [] } satisfies BrowserProfileListState;
		await options.store.load();
		return listStateWithStoreError(options.store);
	});
	handle("browserProfiles:create", async (event, input: unknown) => {
		if (!trustedShellSender(event, options.shellWebContents)) throw invalid("Untrusted browser profile sender.");
		if (!isRecord(input)) throw invalid("Browser profile input is invalid.");
		return options.store.createProfile(input.name);
	});
	handle("browserProfiles:rename", async (event, input: unknown) => {
		if (!trustedShellSender(event, options.shellWebContents)) throw invalid("Untrusted browser profile sender.");
		if (!isRecord(input)) throw invalid("Browser profile input is invalid.");
		const profile = await options.store.renameProfile(profileIdFromInput(input.id), input.name);
		options.host.refreshProfileState?.(profile.id);
		return profile;
	});
	handle("browserProfiles:clear", async (event, input: unknown) => {
		if (!trustedShellSender(event, options.shellWebContents)) throw invalid("Untrusted browser profile sender.");
		const profileId = profileIdFromInput(isRecord(input) ? input.id : undefined);
		return options.store.runProfileOperation(profileId, () => options.host.isProfileLive(profileId), () => options.host.clearProfileData(profileId));
	});
	handle("browserProfiles:delete", async (event, input: unknown) => {
		if (!trustedShellSender(event, options.shellWebContents)) throw invalid("Untrusted browser profile sender.");
		const profileId = profileIdFromInput(isRecord(input) ? input.id : undefined);
		return options.store.runProfileOperation(profileId, () => options.host.isProfileLive(profileId), async () => {
			await options.host.clearProfileData(profileId);
			await options.store.deleteProfile(profileId);
		});
	});
	handle("browserProfiles:import:discover", async (event) => {
		if (!trustedShellSender(event, options.shellWebContents)) return { sources: [] };
		return options.importer.discover();
	});
	handle("browserProfiles:import:start", async (event, input: unknown) => {
		if (!trustedShellSender(event, options.shellWebContents)) throw invalid("Untrusted browser profile sender.");
		return options.importer.import(input as BrowserImportRequest, (progress) => {
			options.shellWebContents.send("browserProfiles:import:progress", progress);
		});
	});
	handle("browser:profile:get", (event, rawViewId: unknown) => {
		const viewId = viewIdFromInput(rawViewId);
		if (!options.host.isRendererOwned(event, viewId)) return { ...EMPTY_PROFILE_STATE };
		return options.host.getProfileState(viewId) ?? { ...EMPTY_PROFILE_STATE, viewId };
	});
	handle("browser:profile:select", async (event, input: unknown) => {
		if (!isRecord(input)) throw invalid("Browser profile selection is invalid.");
		const viewId = viewIdFromInput(input.viewId);
		if (!options.host.isRendererOwned(event, viewId)) return undefined;
		const profileId = input.profileId === null ? null : profileIdFromInput(input.profileId);
		const labels = labelsFromInput(input.labels) satisfies BrowserProfileSelectInput["labels"];
		await selectFromMenu(viewId, profileId, labels, options);
		return undefined;
	});
	handle("browser:profile:menu", (event, input: unknown) => {
		if (!isRecord(input)) throw invalid("Browser profile menu input is invalid.");
		const viewId = viewIdFromInput(input.viewId);
		if (!options.host.isRendererOwned(event, viewId)) return undefined;
		const bounds = menuBoundsFromInput(input.bounds);
		const labels = labelsFromInput(input.labels);
		const current = options.host.getProfileState(viewId);
		if (!current) return undefined;
		const switchInfo = options.host.getProfileSwitchInfo(viewId);
		const switchBlocked = !switchInfo || switchInfo.agentActive;
		const profiles = options.store.profiles;
		const items: BrowserProfileMenuItem[] = [
			{
				label: labels.temporary,
				type: "radio",
				checked: current.profileId === null,
				enabled: !switchBlocked,
				click: () => void selectFromMenu(viewId, null, labels, options),
			},
			...profiles.map((profile) => ({
				label: profileLabel(profile),
				type: "radio" as const,
				checked: current.profileId === profile.id,
				enabled: !switchBlocked && !options.store.isProfileOperationInProgress(profile.id),
				click: () => void selectFromMenu(viewId, profile.id, labels, options),
			})),
			{ type: "separator" },
			{
				label: labels.manage,
				click: () => options.shellWebContents.send("browser:profileManage", { viewId }),
			},
		];
		const rawZoomFactor = (event.sender as Partial<Pick<WebContents, "getZoomFactor">>).getZoomFactor?.() ?? 1;
		const zoomFactor = Number.isFinite(rawZoomFactor) && rawZoomFactor > 0 ? rawZoomFactor : 1;
		options.buildMenu(items).popup({
			window: options.mainWindow,
			x: Math.round(bounds.x * zoomFactor),
			y: Math.round((bounds.y + bounds.height) * zoomFactor),
		});
		return undefined;
	});

	return {
		dispose: () => disposers.splice(0).forEach((dispose) => dispose()),
	};
}

async function selectFromMenu(
	viewId: string,
	profileId: string | null,
	labels: BrowserProfileMenuInput["labels"],
	options: BrowserProfileIpcOptions,
): Promise<void> {
	const current = options.host.getProfileState(viewId);
	if (!current || current.profileId === profileId) return;
	const info = options.host.getProfileSwitchInfo(viewId);
	if (!info || info.agentActive) return;
	if (info.hasNavigated && !(await options.confirmSwitch(labels))) return;
	try {
		await options.host.switchProfile(viewId, profileId);
	} catch (error) {
		console.error("browser profile switch failed:", error);
	}
}
