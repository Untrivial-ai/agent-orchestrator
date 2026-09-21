import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { subscribeCloudNotificationHints } from "../lib/cloud-notification-hints";
import { addCloudNotificationHint, applyCloudNotificationEvent, cloudNotificationsQueryKey, emptyCloudNotificationState, type CloudNotificationState } from "../lib/cloud-notifications";
import { subscribeNotificationEventsBridged } from "../lib/cloud-cp/stream-bridge";
import { useCloudCp } from "./useCloudCp";
import { useCloudOrg } from "./useCloudOrg";

export function useCloudNotifications(status: "all" | "unread" | "read" = "all") {
	const { client, ready, baseUrl } = useCloudCp();
	const { org } = useCloudOrg();
	const orgId = org?.id ?? "";
	const key = cloudNotificationsQueryKey(baseUrl, orgId, status);
	const queryClient = useQueryClient();
	const query = useQuery({ queryKey: key, enabled: ready && orgId !== "", queryFn: async () => client.listNotifications(orgId, { status }) });
	useEffect(() => {
		if (!ready || orgId === "") return;
		const controller = new AbortController();
		const update = (change: (state: CloudNotificationState) => CloudNotificationState) => queryClient.setQueryData<CloudNotificationState>(["cloud-notification-state", baseUrl, orgId], (current) => change(current ?? emptyCloudNotificationState()));
		const stopHints = subscribeCloudNotificationHints((hint) => update((state) => addCloudNotificationHint(state, hint)));
		void subscribeNotificationEventsBridged({ baseUrl, orgId, signal: controller.signal, onEvent: (event) => {
			update((state) => applyCloudNotificationEvent(state, event));
			void queryClient.invalidateQueries({ queryKey: ["cloud-notifications", baseUrl, orgId] });
		}, onError: () => { void queryClient.invalidateQueries({ queryKey: ["cloud-notifications", baseUrl, orgId] }); } });
		return () => { stopHints(); controller.abort(); };
	}, [baseUrl, orgId, queryClient, ready]);
	return { ...query, markAllRead: () => client.markNotificationsRead(orgId) };
}
