import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo } from "react";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";
import type { CloudCpPullRequestSummary } from "../lib/cloud-cp";
import { createRendererCloudCpClient } from "../lib/cloud-cp/renderer-client";
import { subscribeSessionEventsBridged } from "../lib/cloud-cp/stream-bridge";
import { useSettings } from "./useSettings";

export type SessionPRSummary = components["schemas"]["SessionPRSummary"];

export const sessionScmSummaryQueryKey = (sessionId?: string) =>
	sessionId ? (["session-scm-summary", sessionId] as const) : (["session-scm-summary"] as const);

export async function fetchSessionScmSummary(sessionId: string): Promise<SessionPRSummary[]> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/pr", {
		params: { path: { sessionId } },
	});
	if (error) throw error;
	return data?.prs ?? [];
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

export function sessionScmSummaryQueryOptions(sessionId: string) {
	return {
		queryKey: sessionScmSummaryQueryKey(sessionId),
		enabled: Boolean(sessionId),
		queryFn: () => fetchSessionScmSummary(sessionId),
		retry: 1,
	};
}


export function useSessionScmSummary(
	sessionId?: string,
	enabled = true,
	cloudOrgId?: string,
	cloudAutoInjectCI = false,
) {
	const { settings } = useSettings();
	const baseUrl = settings?.cloudControlPlaneUrl ?? "";
	const cloudClient = useMemo(() => createRendererCloudCpClient(baseUrl), [baseUrl]);
	const cloud = Boolean(cloudOrgId);
	const queryClient = useQueryClient();
	const queryKey = useMemo(
		() => cloud
			? ["cloud-session-scm-summary", baseUrl, cloudOrgId, sessionId] as const
			: sessionScmSummaryQueryKey(sessionId),
		[baseUrl, cloud, cloudOrgId, sessionId],
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
				if (event.type === "scm.updated") {
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
			if (!cloudOrgId) return fetchSessionScmSummary(sessionId!);
			const response = await cloudClient.listSessionPullRequests(cloudOrgId, sessionId!);
			return response.pullRequests.map((pr) => cloudPRSummaryToSessionPRSummary(pr, cloudAutoInjectCI));
		},
		retry: 1,
	});
}
