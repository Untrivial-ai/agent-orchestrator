/**
 * Discord Rich Presence types and IPC channel names, shared by the Electron
 * main process (which owns the Discord IPC client and the settings file) and
 * the preload bridge (which types the IPC surface). The renderer picks it up
 * through the preload's AoBridge type.
 */

export type RpcSettings = {
	enabled: boolean;
};

export type RpcConnectionState =
	| "disconnected"
	| "connecting"
	| "connected"
	| "error";

export type RpcStatus = {
	state: RpcConnectionState;
	message?: string;
	activeSessions?: number;
};

export const RPC_SETTINGS_FILE_NAME = "rpc-settings.json";

export const RPC_GET_SETTINGS_CHANNEL = "rpc:getSettings";
export const RPC_SET_SETTINGS_CHANNEL = "rpc:setSettings";
export const RPC_GET_STATUS_CHANNEL = "rpc:getStatus";
export const RPC_STATUS_CHANNEL = "rpc:status";

export const DISCORD_CLIENT_ID = "1537150477735690352";

export const RPC_PRESENCE_REFRESH_INTERVAL_MS = 15_000;
export const RPC_LARGE_IMAGE_KEY = "ao-logo";
export const RPC_LARGE_IMAGE_TEXT = "Agent Orchestrator";

/**
 * Normalizes a raw parsed JSON value into RpcSettings. Missing or corrupt
 * files default to disabled, matching the behavior of `readUpdateSettings`
 * and `coerceLocale` for their respective settings files.
 */
export function coerceRpcSettings(raw: unknown): RpcSettings {
	if (typeof raw !== "object" || raw === null) {
		return { enabled: false };
	}
	const r = raw as Partial<RpcSettings>;
	return { enabled: r.enabled === true };
}
