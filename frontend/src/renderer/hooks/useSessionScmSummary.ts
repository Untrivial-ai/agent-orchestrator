import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo } from "react";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";
import { clientForHost } from "../lib/host-clients";
import { LOCAL_HOST } from "../lib/hosts";
import type { CloudCpPullRequestSummary } from "../lib/cloud-cp";
import { createRendererCloudCpClient } from "../lib/cloud-cp/renderer-client";
import { subscribeSessionEventsBridged } from "../lib/cloud-cp/stream-bridge";
import { useSettings } from "./useSettings";

export type SessionPRSummary = components["schemas"]["SessionPRSummary"];
export type SessionPRReference = components["schemas"]["SessionPRReference"];

export const sessionScmSummaryQueryKey = (sessionId?: string, hostId?: string) =>
	sessionId ? (["session-scm-summary", hostId ?? LOCAL_HOST, sessionId] as const) : (["session-scm-summary"] as const);

export async function fetchSessionScmSummary(sessionId: string, hostId?: string) {
	const { data, error } = await (hostId ? clientForHost(hostId) : apiClient).GET("/api/v1/sessions/{sessionId}/pr", {
		params: { path: { sessionId } },
	});
	if (error) throw error;
	return { prs: data?.prs ?? [], linkedPrs: data?.linkedPrs ?? [] };
}

export function cloudPRSummaryToSessionPRSummary(
	pr: CloudCpPullRequestSummary,
	autoInjectCI: boolean,
): SessionPRSummary {
	return {
		...pr,
		provider: pr.provider === "gitlab" ? "gitlab" : "github",
		repo: pr.repository,
		ci: { ...pr.ci, autoInjectCI },
		review: pr.review,
		mergeability: {
			...pr.mergeability,
			prUrl: pr.mergeability.pullRequestUrl,
		},
	};
}

export function sessionScmSummaryQueryOptions(sessionId: string, hostId?: string) {
	return {
		queryKey: sessionScmSummaryQueryKey(sessionId, hostId),
		enabled: Boolean(sessionId),
		queryFn: () => fetchSessionScmSummary(sessionId, hostId),
		retry: 1,
		...(hostId ? { refetchInterval: 15_000 } : {}),
	};
}

export function useSessionScmSummary(
	sessionId?: string,
	enabled = true,
	cloudOrgId?: string,
	cloudAutoInjectCI = false,
	hostId?: string,
) {
	const { settings } = useSettings(undefined, Boolean(cloudOrgId));
	const baseUrl = settings?.cloudControlPlaneUrl ?? "";
	const cloudClient = useMemo(() => createRendererCloudCpClient(baseUrl), [baseUrl]);
	const cloud = Boolean(cloudOrgId);
	const queryClient = useQueryClient();
	const queryKey = useMemo(
		() => cloud
			? ["cloud-session-scm-summary", baseUrl, cloudOrgId, sessionId] as const
			: sessionScmSummaryQueryKey(sessionId, hostId),
		[baseUrl, cloud, cloudOrgId, hostId, sessionId],
	);
	useEffect(() => {
		if (!enabled || !cloudOrgId || !sessionId || baseUrl === "") return;
		const controller = new AbortController();
		void subscribeSessionEventsBridged({
			baseUrl,
			orgId: cloudOrgId,
			sessionId,
			signal: controller.signal,
			onEvent: (event) => {
				if (
					event.type === "scm.updated" ||
					event.type === "pull_request.created" ||
					event.type === "pull_request.claimed"
				) {
					void queryClient.invalidateQueries({ queryKey });
				}
			},
		});
		return () => controller.abort();
	}, [baseUrl, cloudOrgId, enabled, queryClient, queryKey, sessionId]);
	return useQuery({
		queryKey,
		enabled: enabled && Boolean(sessionId) && (!cloud || baseUrl !== ""),
		queryFn: async () => {
			if (!cloudOrgId) return fetchSessionScmSummary(sessionId!, hostId);
			const response = await cloudClient.listSessionPullRequests(cloudOrgId, sessionId!);
			return {
				prs: response.pullRequests.map((pr) => cloudPRSummaryToSessionPRSummary(pr, cloudAutoInjectCI)),
				linkedPrs: [],
			};
		},
		retry: 1,
		...(hostId ? { refetchInterval: 15_000 } : {}),
	});
}
