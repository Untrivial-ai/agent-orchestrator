import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, hasTrustedApiBaseUrl } from "../lib/api-client";
import { mockWorkspaces } from "../lib/mock-data";
import { usesPreviewWorkspaceData } from "../lib/preview-mode";
import { toReviewerHarnessId } from "../lib/reviewer-harnesses";
import { captureRendererEvent } from "../lib/telemetry";
import {
	type PRState,
	type PullRequestFacts,
	toAgentProvider,
	toProjectKind,
	toSessionActivity,
	toSessionStatus,
	type WorkspaceSummary,
} from "../types/workspace";

function toPullRequestFacts(pr: components["schemas"]["SessionPRFacts"]): PullRequestFacts {
	return {
		url: pr.url,
		number: pr.number,
		state: pr.state as PRState,
		ci: pr.ci,
		review: pr.review,
		mergeability: pr.mergeability,
		reviewComments: pr.reviewComments,
		updatedAt: pr.updatedAt,
	};
}

export const workspaceQueryKey = ["workspaces"] as const;
const reportedUnknownSessionFields = new Set<string>();

function reportUnknownSessionField(field: "status" | "activity", value?: string): void {
	const reason = value ? "unrecognized" : "missing";
	const key = `${field}:${reason}`;
	if (reportedUnknownSessionFields.has(key)) return;
	reportedUnknownSessionFields.add(key);
	void captureRendererEvent("ao.renderer.session_state_unknown", { field, reason });
}

// e2e seam (dev:web only): the Playwright fake-agent harness injects
// `window.__aoFakeAgent` (see e2e/support/fake-bridge.ts) to drive a
// deterministic, mutable session timeline off the SSE refetch path. Compiled
// out of the packaged build — the packaged renderer never sets VITE_NO_ELECTRON
// and always hits the real daemon.
type FakeAgentSeam = { snapshot: () => WorkspaceSummary[] };

async function fetchWorkspaces(): Promise<WorkspaceSummary[]> {
	if (usesPreviewWorkspaceData) {
		const fake =
			typeof window !== "undefined"
				? (window as unknown as { __aoFakeAgent?: FakeAgentSeam }).__aoFakeAgent
				: undefined;
		return fake ? fake.snapshot() : mockWorkspaces;
	}
	if (!hasTrustedApiBaseUrl()) {
		throw new Error("AO daemon API is not ready");
	}

	const [{ data: projectsData, error: projectsError }, { data: sessionsData, error: sessionsError }] =
		await Promise.all([apiClient.GET("/api/v1/projects"), apiClient.GET("/api/v1/sessions")]);

	if (projectsError || sessionsError) throw projectsError ?? sessionsError;

	return (projectsData?.projects ?? []).map((project) => {
		const kind = toProjectKind(project.kind);
		return {
			id: project.id,
			name: project.name,
			kind,
			path: project.path,
			orchestratorAgent: project.orchestratorAgent ? toAgentProvider(project.orchestratorAgent) : undefined,
			sessions: (sessionsData?.sessions ?? [])
				.filter((session) => session.projectId === project.id)
				.map((session) => {
					const status = toSessionStatus(session.status, session.isTerminated);
					const scmStatus = session.scmStatus ? toSessionStatus(session.scmStatus) : undefined;
					const activity = toSessionActivity(session.activity);
					if (status === "unknown") reportUnknownSessionField("status", session.status);
					if (!activity || activity.state === "unknown") {
						reportUnknownSessionField("activity", session.activity?.state);
					}
					return {
						id: session.id,
						terminalHandleId: session.terminalHandleId,
						workspaceId: project.id,
						workspaceName: project.name,
						title: session.displayName ?? session.issueId ?? session.id,
						issueId: session.issueId,
						provider: toAgentProvider(session.harness),
						reviewerHarness: toReviewerHarnessId(session.reviewerHarness),
						kind: session.kind === "orchestrator" ? "orchestrator" : session.kind === "worker" ? "worker" : undefined,
						// Carried through verbatim: the session surface must render from
						// the mode this session was created with, not from whatever the
						// current default happens to be.
						mode: session.mode === "chat" ? "chat" : "tui",
						branch: session.branch || undefined,
						status,
						scmStatus,
						isTerminated: session.isTerminated,
						terminateOnPrMerge: session.terminateOnPrMerge ?? false,
						autoInjectReview: session.autoInjectReview ?? true,
						createdAt: session.createdAt,
						updatedAt: session.updatedAt,
						activity,
						previewUrl: session.previewUrl,
						previewRevision: session.previewRevision,
						isPinned: session.isPinned ?? false,
						pinnedAt: session.pinnedAt ?? undefined,
						prs: (session.prs ?? []).map(toPullRequestFacts),
					};
				}),
		};
	});
}

// Shared so route loaders can prefetch via queryClient.ensureQueryData (paired
// with the router's defaultPreload: "intent") and the hook reads the same cache.
export const workspaceQueryOptions = {
	queryKey: workspaceQueryKey,
	queryFn: fetchWorkspaces,
	retry: 1,
	refetchInterval: 15_000,
};

export function useWorkspaceQuery() {
	return useQuery(workspaceQueryOptions);
}
