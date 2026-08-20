import { Client } from "@xhayper/discord-rpc";
import createClient from "openapi-fetch";
import type { paths } from "../api/schema";
import {
	DISCORD_CLIENT_ID,
	RPC_LARGE_IMAGE_KEY,
	RPC_LARGE_IMAGE_TEXT,
	RPC_PRESENCE_REFRESH_INTERVAL_MS,
	type RpcConnectionState,
	type RpcSettings,
	type RpcStatus,
} from "../shared/rpc";

type SessionStatus =
	| "working"
	| "pr_open"
	| "draft"
	| "ci_failed"
	| "review_pending"
	| "changes_requested"
	| "approved"
	| "mergeable"
	| "merged"
	| "needs_input"
	| "exited"
	| "idle"
	| "terminated"
	| "no_signal";

const STATUS_PRIORITY: { status: SessionStatus; label: string }[] = [
	{ status: "needs_input", label: "Waiting on you" },
	{ status: "ci_failed", label: "Fixing CI" },
	{ status: "changes_requested", label: "Addressing review" },
	{ status: "merge_conflict" as SessionStatus, label: "Resolving conflicts" },
	{ status: "draft", label: "Drafting PR" },
	{ status: "review_pending", label: "In review" },
	{ status: "pr_open", label: "In review" },
	{ status: "mergeable", label: "Ready to merge" },
	{ status: "approved", label: "Ready to merge" },
	{ status: "working", label: "Working" },
	{ status: "no_signal", label: "Idle" },
	{ status: "idle", label: "Idle" },
];

const EXCLUDED_STATUSES: SessionStatus[] = ["exited", "terminated", "merged"];
const INACTIVE_STATUSES: SessionStatus[] = ["idle", "no_signal"];
const ACTIVE_AGENT_STATUSES: SessionStatus[] = ["working", "needs_input"];

interface ActivityPayload {
	details: string;
	state: string;
	startTimestamp: number;
	largeImageKey: string;
	largeImageText: string;
}

let client: Client | null = null;
let refreshTimer: ReturnType<typeof setInterval> | null = null;
let apiClient: ReturnType<typeof createClient<paths>> | null = null;
let connectionState: RpcConnectionState = "disconnected";
let statusListeners: ((status: RpcStatus) => void)[] = [];
let rpcStartTimestamp: number | null = null;
let reconnectAttempts = 0;
const MAX_RECONNECT_ATTEMPTS = 5;
let settingsOperation: Promise<void> = Promise.resolve();

function makeApiClient(port: number): ReturnType<typeof createClient<paths>> {
	return createClient<paths>({ baseUrl: `http://127.0.0.1:${port}` });
}

export function setDaemonPort(port: number): void {
	apiClient = makeApiClient(port);
}

function broadcastStatus(): void {
	const status: RpcStatus = { state: connectionState };
	for (const listener of statusListeners) {
		listener(status);
	}
}

export function onRpcStatus(listener: (status: RpcStatus) => void): () => void {
	statusListeners.push(listener);
	listener({ state: connectionState });
	return () => {
		statusListeners = statusListeners.filter((l) => l !== listener);
	};
}

export function getRpcStatus(): RpcStatus {
	return { state: connectionState };
}

export async function startDiscordRpc(): Promise<void> {
	if (client) return;
	rpcStartTimestamp = Date.now();
	reconnectAttempts = 0;
	client = new Client({ clientId: DISCORD_CLIENT_ID });
	connectionState = "connecting";
	broadcastStatus();
	try {
		await client.login();
		connectionState = "connected";
	} catch {
		connectionState = "disconnected";
		try {
			await client.destroy();
		} catch {
		}
		client = null;
	}
	broadcastStatus();
	refreshTimer = setInterval(() => {
		void refreshPresence();
	}, RPC_PRESENCE_REFRESH_INTERVAL_MS);
	void refreshPresence();
}

async function reconnect(): Promise<void> {
	if (!client && reconnectAttempts < MAX_RECONNECT_ATTEMPTS) {
		reconnectAttempts++;
		connectionState = "connecting";
		broadcastStatus();
		client = new Client({ clientId: DISCORD_CLIENT_ID });
		try {
			await client.login();
			connectionState = "connected";
			reconnectAttempts = 0;
		} catch {
			connectionState = "disconnected";
			try {
				await client.destroy();
			} catch {
			}
			client = null;
		}
		broadcastStatus();
	}
}

export async function setRpcSettings(settings: RpcSettings): Promise<void> {
	const operation = settingsOperation.then(async () => {
		if (settings.enabled) {
			await startDiscordRpc();
		} else {
			await disposeDiscordRpc();
		}
	});
	settingsOperation = operation.catch(() => undefined);
	await operation;
}

export function pickRepresentativeStatus(sessions: { status: string; isTerminated: boolean }[]): { label: string; count: number } | null {
	const active = sessions.filter(
		(s) =>
			!s.isTerminated &&
			!EXCLUDED_STATUSES.includes(s.status as SessionStatus) &&
			!INACTIVE_STATUSES.includes(s.status as SessionStatus),
	);
	const activeAgentCount = active.filter((s) => ACTIVE_AGENT_STATUSES.includes(s.status as SessionStatus)).length;
	if (active.length === 0) return { label: "Idle", count: 0 };
	for (const entry of STATUS_PRIORITY) {
		const matching = active.filter((s) => s.status === entry.status);
		if (matching.length > 0) {
			return { label: entry.label, count: activeAgentCount };
		}
	}
	return { label: "Idle", count: activeAgentCount };
}

export function buildActivityPayload(
	sessions: { status: string; isTerminated: boolean; createdAt?: string }[],
	startTime: number = Date.now(),
): ActivityPayload | null {
	const rep = pickRepresentativeStatus(sessions);
	if (!rep) return null;
	if (rep.count === 0) {
		return {
			details: "",
			state: "Idle",
			startTimestamp: startTime,
			largeImageKey: RPC_LARGE_IMAGE_KEY,
			largeImageText: RPC_LARGE_IMAGE_TEXT,
		};
	}
	return {
		details: `Orchestrating ${rep.count} ${rep.count === 1 ? "agent" : "agents"}`,
		state: rep.label,
		startTimestamp: startTime,
		largeImageKey: RPC_LARGE_IMAGE_KEY,
		largeImageText: RPC_LARGE_IMAGE_TEXT,
	};
}

async function refreshPresence(): Promise<void> {
	if (!apiClient) return;
	if (connectionState === "disconnected" && !client) {
		await reconnect();
		return;
	}
	if (!client || connectionState !== "connected") return;
	const { data: sessionsResp, error: sessionsErr } = await apiClient.GET("/api/v1/sessions", {
		params: { query: { orchestratorOnly: true } },
	});
	if (sessionsErr || !sessionsResp) return;
	const sessions = (sessionsResp as { sessions?: { status: string; isTerminated: boolean; createdAt?: string }[] }).sessions ?? [];
	const payload = buildActivityPayload(sessions, rpcStartTimestamp ?? undefined);
	if (!payload) {
		await clearActivity();
		return;
	}
	try {
		await client.request("SET_ACTIVITY", {
			pid: process.pid,
			activity: {
				...(payload.details ? { details: payload.details } : {}),
				state: payload.state,
				timestamps: { start: payload.startTimestamp },
				assets: {
					large_image: payload.largeImageKey,
					large_text: payload.largeImageText,
				},
			},
		});
	} catch {
		connectionState = "disconnected";
		broadcastStatus();
		try {
			await client.destroy();
		} catch {
		}
		client = null;
	}
}

async function clearActivity(): Promise<void> {
	if (!client || connectionState !== "connected") return;
	try {
		await client.request("SET_ACTIVITY", { pid: process.pid, activity: null });
	} catch {
		connectionState = "disconnected";
		broadcastStatus();
		try {
			await client.destroy();
		} catch {
		}
		client = null;
	}
}

export async function disposeDiscordRpc(): Promise<void> {
	if (refreshTimer) {
		clearInterval(refreshTimer);
		refreshTimer = null;
	}
	await clearActivity();
	if (client) {
		try {
			await client.destroy();
		} catch {
		}
		client = null;
	}
	reconnectAttempts = 0;
	connectionState = "disconnected";
	broadcastStatus();
}
