import { useCallback, useEffect, useMemo, useSyncExternalStore } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { subscribeCloudNotificationHints } from "../lib/cloud-notification-hints";
import { cloudNotificationsQueryKey } from "../lib/cloud-notifications";
import { CloudCpError } from "../lib/cloud-cp/errors";
import { subscribeNotificationEventsBridged } from "../lib/cloud-cp/stream-bridge";
import { useCloudCp } from "./useCloudCp";
import { useCloudOrg } from "./useCloudOrg";
import { cloudSessionsQueryKey } from "./useWorkspaceQuery";
import { orchestratorChildrenQueryKey } from "./useOrchestratorChildren";

type UseCloudNotificationsOptions = {
	/**
	 * Open this instance's own live stream. The shell's CloudNotificationRuntime
	 * already keeps the "all" list live, so readers of that list pass false
	 * rather than holding a duplicate stream.
	 */
	live?: boolean;
};

export function useCloudNotifications(
	status: "all" | "unread" | "read" = "all",
	{ live = true }: UseCloudNotificationsOptions = {},
) {
	const { client, ready, baseUrl, userId } = useCloudCp();
	const { org } = useCloudOrg();
	const orgId = org?.id ?? "";
	// Normalize the base URL once so the query key and every invalidation agree.
	// A trailing slash otherwise makes the invalidation key miss the query key,
	// so live updates would only land on mount/focus.
	const base = baseUrl.replace(/\/+$/, "");
	const key = cloudNotificationsQueryKey(base, orgId, userId, status);
	const queryClient = useQueryClient();
	const query = useQuery({ queryKey: key, enabled: ready && orgId !== "" && userId !== "", queryFn: async () => client.listNotifications(orgId, { status }) });
	useEffect(() => {
		if (!live || !ready || orgId === "" || userId === "") return;
		const notificationsKey = cloudNotificationsQueryKey(base, orgId, userId, status);
		const controller = new AbortController();
		// The control plane owns the authoritative list, unread count, and sequence
		// (all paginated/server-computed), so a durable event or a pre-durable hint
		// refreshes from the source rather than being patched into the cache — which
		// would drift the unread badge and can't render a hint that carries no
		// title/body. A hint arrives over the terminal WebSocket before the durable
		// inbox row commits, so it acts as a low-latency refetch trigger; REST/SSE
		// remain the recovery source after a reconnect.
		const refresh = () => { void queryClient.invalidateQueries({ queryKey: notificationsKey }); };
		const stopHints = subscribeCloudNotificationHints(refresh);
		void subscribeNotificationEventsBridged({ baseUrl: base, orgId, signal: controller.signal, onEvent: () => {
			refresh();
			void queryClient.invalidateQueries({ queryKey: cloudSessionsQueryKey });
			void queryClient.invalidateQueries({ queryKey: orchestratorChildrenQueryKey });
		}, onError: refresh });
		return () => { stopHints(); controller.abort(); };
	}, [base, live, orgId, queryClient, ready, status, userId]);

	// Cleared rows are hidden per signed-in user: notifications belong to one
	// recipient, and an org can be shared by several accounts on this device.
	const storageKey = clearStorageKey(base, orgId, userId);
	const clears = useSyncExternalStore(subscribeClears, () => readClearsRaw(storageKey));
	const items = useMemo(() => {
		const state = parseClears(clears);
		return (query.data?.items ?? []).filter((item) => !isCleared(item, state));
	}, [clears, query.data?.items]);

	// A cleared row the control plane re-raises (in place: status back to unread,
	// updated_at bumped) is revived for good, so marking it read later cannot
	// hide it again.
	useEffect(() => {
		const state = parseClears(readClearsRaw(storageKey));
		const revived = (query.data?.items ?? []).filter((item) => isRevived(item, state));
		if (revived.length === 0) return;
		const ids = { ...state.ids };
		for (const item of revived) ids[item.id] = null;
		writeClears(storageKey, { ...state, ids });
	}, [query.data?.items, storageKey]);

	const invalidateAll = useCallback(
		() => queryClient.invalidateQueries({ queryKey: ["cloud-notifications", base, orgId, userId] }),
		[base, orgId, queryClient, userId],
	);
	const markAllRead = useCallback(async () => {
		if (!ready || orgId === "") return;
		await client.markNotificationsRead(orgId);
		await invalidateAll();
	}, [client, invalidateAll, orgId, ready]);
	// Acknowledge concrete ids only. The control plane answers 404 for a row that
	// is already read, which is the goal, so that is not a failure here.
	const markRead = useCallback(async (notificationIds: string[]) => {
		if (!ready || orgId === "" || notificationIds.length === 0) return;
		try {
			await Promise.all(notificationIds.map((id) => client.markNotificationsRead(orgId, [id]).catch(ignoreAlreadyRead)));
		} finally {
			await invalidateAll();
		}
	}, [client, invalidateAll, orgId, ready]);
	// The control plane has no delete endpoint, so clearing marks rows read
	// server-side and hides them on this device. Markers are the server's own
	// timestamps, so local clock skew cannot hide a newer notification.
	const clearAll = useCallback(async () => {
		if (!ready || orgId === "") return;
		const loaded = query.data?.items ?? [];
		const previous = readClearsRaw(storageKey);
		const state = parseClears(previous);
		const newest = loaded.reduce((max, item) => Math.max(max, timestamp(item.createdAt)), state.before);
		const ids: CloudClears["ids"] = {};
		for (const [id, marker] of Object.entries(state.ids)) if (marker !== null) ids[id] = marker;
		for (const item of loaded) ids[item.id] = timestamp(item.updatedAt);
		writeClears(storageKey, { before: newest, ids });
		try {
			await markAllRead();
		} catch (error) {
			restoreClears(storageKey, previous);
			throw error;
		}
	}, [markAllRead, orgId, query.data?.items, ready, storageKey]);
	const clearOne = useCallback(async (notification: CloudNotificationRow) => {
		if (!ready || orgId === "") return;
		const previous = readClearsRaw(storageKey);
		const state = parseClears(previous);
		writeClears(storageKey, { ...state, ids: { ...state.ids, [notification.id]: timestamp(notification.updatedAt) } });
		try {
			if (notification.status === "unread") await markRead([notification.id]);
		} catch (error) {
			// Put the row back so the list agrees with the error it reports.
			restoreClears(storageKey, previous);
			throw error;
		}
	}, [markRead, orgId, ready, storageKey]);
	return { ...query, items, markAllRead, markRead, clearAll, clearOne };
}

/**
 * `before` hides every row created at or before it (clear all). `ids` holds
 * per-row markers: the row's server `updatedAt` when it was cleared, or null
 * for a row that was re-raised after a clear and must stay visible.
 */
export type CloudClears = { before: number; ids: Record<string, number | null> };
type CloudNotificationRow = { id: string; createdAt: string; updatedAt: string; status: "unread" | "read" };

// Keep the most recent per-row markers; older rows fall under the clear-all
// cutoff anyway. Capped so the stored map cannot grow without bound.
const MAX_CLEARED_IDS = 200;
const clearListeners = new Set<() => void>();
const clearStorageKey = (base: string, orgId: string, userId: string) =>
	`ao.cloudNotifications.cleared:${base}:${orgId}:${userId}`;

const timestamp = (value: string) => Date.parse(value) || 0;

function clearMarker(item: CloudNotificationRow, clears: CloudClears): number | null | undefined {
	if (item.id in clears.ids) return clears.ids[item.id];
	return timestamp(item.createdAt) <= clears.before ? clears.before : undefined;
}

function isRevived(item: CloudNotificationRow, clears: CloudClears): boolean {
	const marker = clearMarker(item, clears);
	return typeof marker === "number" && item.status === "unread" && timestamp(item.updatedAt) > marker;
}

export function isCleared(item: CloudNotificationRow, clears: CloudClears): boolean {
	const marker = clearMarker(item, clears);
	if (marker === undefined || marker === null) return false;
	return !isRevived(item, clears);
}

function ignoreAlreadyRead(error: unknown) {
	if (error instanceof CloudCpError && error.status === 404) return;
	throw error;
}

function subscribeClears(listener: () => void) {
	clearListeners.add(listener);
	return () => { clearListeners.delete(listener); };
}

function readClearsRaw(storageKey: string): string {
	try {
		return window.localStorage.getItem(storageKey) ?? "";
	} catch {
		return "";
	}
}

function parseClears(raw: string): CloudClears {
	try {
		const parsed = JSON.parse(raw) as Partial<CloudClears>;
		return {
			before: typeof parsed.before === "number" ? parsed.before : 0,
			ids: parsed.ids && typeof parsed.ids === "object" ? parsed.ids : {},
		};
	} catch {
		return { before: 0, ids: {} };
	}
}

function writeClears(storageKey: string, clears: CloudClears) {
	const rank = (marker: number | null) => (marker === null ? Number.POSITIVE_INFINITY : marker);
	const ids = Object.entries(clears.ids)
		.sort(([, a], [, b]) => rank(b) - rank(a))
		.slice(0, MAX_CLEARED_IDS);
	restoreClears(storageKey, JSON.stringify({ before: clears.before, ids: Object.fromEntries(ids) }));
}

function restoreClears(storageKey: string, raw: string) {
	try {
		if (raw === "") window.localStorage.removeItem(storageKey);
		else window.localStorage.setItem(storageKey, raw);
	} catch {
		// Storage unavailable: rows stay visible but are still marked read.
	}
	for (const listener of clearListeners) listener();
}
