import { useQueries, type QueryClient } from "@tanstack/react-query";
import { useSyncExternalStore } from "react";
import { apiErrorMessage } from "../lib/api-client";
import { baseUrlForHost, clientForHost, connectedHosts, labelForHost, subscribeConnectedHosts } from "../lib/host-clients";
import { probeRemoteSse } from "../lib/remote-sse-probe";
import { useConnectedHosts } from "./useHostConnection";
import { NOTIFICATION_PAGE_SIZE, type NotificationListStatus, type NotificationsPage } from "../lib/notifications";

export const remoteNotificationsQueryKey = (hostId: string, status: NotificationListStatus) =>
	["remote-notifications", hostId, status] as const;

let streamingHosts: string[] = [];
const streamListeners = new Set<() => void>();
const subscribeStreams = (listener: () => void) => {
	streamListeners.add(listener);
	return () => streamListeners.delete(listener);
};
const getStreamingHosts = () => streamingHosts;
function setStreaming(hostId: string, streaming: boolean) {
	if (streamingHosts.includes(hostId) === streaming) return;
	streamingHosts = streaming ? [...streamingHosts, hostId] : streamingHosts.filter((id) => id !== hostId);
	for (const listener of streamListeners) listener();
}

/** One stream per connected host; the existing REST query polls until a frame arrives. */
export function connectRemoteNotificationStreams(queryClient: QueryClient): () => void {
	const connections = new Map<string, { base: string; close: () => void }>();
	const invalidate = (hostId: string) => {
		void queryClient.invalidateQueries({ queryKey: remoteNotificationsQueryKey(hostId, "unread") });
		void queryClient.invalidateQueries({ queryKey: remoteNotificationsQueryKey(hostId, "all") });
	};
	const sync = () => {
		const active = new Set(connectedHosts());
		for (const [hostId, connection] of connections) {
			if (active.has(hostId) && baseUrlForHost(hostId) === connection.base) continue;
			connection.close();
			connections.delete(hostId);
			setStreaming(hostId, false);
		}
		for (const hostId of active) {
			const base = baseUrlForHost(hostId);
			if (!base || connections.has(hostId)) continue;
			const close = probeRemoteSse(
				`${base.replace(/\/+$/, "")}/api/v1/notifications/stream`,
				["notification_created", "notification_resolved", "notification_deleted", "notification_cleared"],
				() => invalidate(hostId),
				() => { setStreaming(hostId, true); invalidate(hostId); },
				() => { setStreaming(hostId, false); invalidate(hostId); },
			);
			connections.set(hostId, { base, close });
		}
	};
	const unsubscribe = subscribeConnectedHosts(sync);
	sync();
	return () => {
		unsubscribe();
		for (const [hostId, connection] of connections) {
			connection.close();
			setStreaming(hostId, false);
		}
		connections.clear();
	};
}

export async function fetchRemoteNotificationsPage(hostId: string, status: NotificationListStatus, cursor = ""): Promise<NotificationsPage> {
	const { data, error } = await clientForHost(hostId).GET("/api/v1/notifications", {
		params: { query: { status, limit: NOTIFICATION_PAGE_SIZE, cursor: cursor || undefined } },
	});
	if (error || !data) throw new Error(apiErrorMessage(error, "Could not load notifications"));
	return data;
}

export async function markRemoteNotificationsRead(hostId: string, ids: string[], queryClient: QueryClient): Promise<void> {
	const { error } = await clientForHost(hostId).POST("/api/v1/notifications/read-all", { body: { ids } });
	if (error) throw new Error(apiErrorMessage(error, "Could not mark notifications read"));
	await queryClient.invalidateQueries({ queryKey: remoteNotificationsQueryKey(hostId, "unread") });
	await queryClient.invalidateQueries({ queryKey: remoteNotificationsQueryKey(hostId, "all") });
}

export async function clearRemoteNotifications(hostId: string, queryClient: QueryClient): Promise<void> {
	const { error } = await clientForHost(hostId).DELETE("/api/v1/notifications");
	if (error) throw new Error(apiErrorMessage(error));
	await queryClient.invalidateQueries({ queryKey: ["remote-notifications", hostId] });
}

export async function clearRemoteNotification(hostId: string, id: string, queryClient: QueryClient): Promise<void> {
	const { error, response } = await clientForHost(hostId).DELETE("/api/v1/notifications/{id}", {
		params: { path: { id } },
	});
	if (error && response.status !== 404) throw new Error(apiErrorMessage(error));
	await queryClient.invalidateQueries({ queryKey: ["remote-notifications", hostId] });
}

export function useRemoteNotificationHosts(status: NotificationListStatus, enabled = true) {
	const connected = useConnectedHosts();
	const streaming = useSyncExternalStore(subscribeStreams, getStreamingHosts, getStreamingHosts);
	const queries = useQueries({
		queries: connected.map((hostId) => ({
			queryKey: remoteNotificationsQueryKey(hostId, status),
			queryFn: () => fetchRemoteNotificationsPage(hostId, status),
			enabled,
			retry: 1,
			refetchInterval: streaming.includes(hostId) ? false : 2_000,
		})),
	});
	return {
		hosts: connected.map((hostId, index) => ({
			hostId,
			label: labelForHost(hostId) ?? hostId,
			data: queries[index]?.isError ? undefined : queries[index]?.data,
			isError: queries[index]?.isError ?? false,
			isLoading: queries[index]?.isLoading ?? false,
		})),
		totalUnreadCount: status === "unread"
			? queries.reduce((count, query) => count + (query.isError ? 0 : query.data?.unreadCount ?? 0), 0)
			: 0,
	};
}
