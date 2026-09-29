import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { subscribeCloudNotificationHints } from "../lib/cloud-notification-hints";
import { cloudNotificationsQueryKey } from "../lib/cloud-notifications";
import { subscribeNotificationEventsBridged } from "../lib/cloud-cp/stream-bridge";
import { useCloudCp } from "./useCloudCp";
import { useCloudOrg } from "./useCloudOrg";
import { cloudSessionsQueryKey } from "./useWorkspaceQuery";
import { orchestratorChildrenQueryKey } from "./useOrchestratorChildren";

export function useCloudNotifications(status: "all" | "unread" | "read" = "all") {
	const { client, ready, baseUrl } = useCloudCp();
	const { org } = useCloudOrg();
	const orgId = org?.id ?? "";
	// Normalize the base URL once so the query key and every invalidation agree.
	// A trailing slash otherwise makes the invalidation key miss the query key,
	// so live updates would only land on mount/focus.
	const base = baseUrl.replace(/\/+$/, "");
	const key = cloudNotificationsQueryKey(base, orgId, status);
	const queryClient = useQueryClient();
	const query = useQuery({ queryKey: key, enabled: ready && orgId !== "", queryFn: async () => client.listNotifications(orgId, { status }) });
	useEffect(() => {
		if (!ready || orgId === "") return;
		const notificationsKey = cloudNotificationsQueryKey(base, orgId, status);
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
	}, [base, orgId, queryClient, ready, status]);
	return { ...query, markAllRead: () => client.markNotificationsRead(orgId) };
}
