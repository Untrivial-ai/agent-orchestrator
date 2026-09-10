import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { TraySessionEntry } from "../../shared/tray";
import { useEffect, useMemo } from "react";
import type { components } from "../../api/schema";
import { apiClient, apiErrorCode, hasTrustedApiBaseUrl } from "../lib/api-client";
import type { CloudCpProject, CloudCpSession } from "../lib/cloud-cp";
import { useCloudCp } from "./useCloudCp";
import { useCloudOrg } from "./useCloudOrg";
import { mockWorkspaces } from "../lib/mock-data";
import { usesPreviewWorkspaceData } from "../lib/preview-mode";
import { toReviewerHarnessId } from "../lib/reviewer-harnesses";
import { captureRendererEvent } from "../lib/telemetry";
import { agentSwitchVisibility } from "../lib/agent-switch-visibility";
import {
	type AgentSwitchSummary,
	type PRState,
	type PullRequestFacts,
	toAgentProvider,
	toKanbanColumn,
	toProjectKind,
	toSessionActivity,
	toSessionStatus,
	newestActiveOrchestrator,
	attentionZone,
	workerSessions,
	type WorkspaceSession,
	type WorkspaceSummary,
} from "../types/workspace";

function toAgentSwitchSummary(
	agentSwitch: components["schemas"]["AgentSwitch"],
): AgentSwitchSummary {
	return {
		agentHandoffStatus: agentSwitch.agentHandoffStatus,
		errorCode: agentSwitch.errorCode,
		fromHarness: agentSwitch.fromHarness,
		id: agentSwitch.id,
		state: agentSwitch.state,
		targetHarness: agentSwitch.targetHarness,
		updatedAt: agentSwitch.updatedAt,
	};
}

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

function toWorkspaceSession(
	session: components["schemas"]["ControllersSessionView"],
	project: Pick<WorkspaceSummary, "id" | "name">,
): WorkspaceSession {
	const status = toSessionStatus(session.status, session.isTerminated);
	const scmStatus = session.scmStatus ? toSessionStatus(session.scmStatus) : undefined;
	const kanbanColumn = toKanbanColumn(session.kanbanColumn, status);
	const activity = toSessionActivity(session.activity);
	if (status === "unknown") reportUnknownSessionField("status", session.status);
	if (!activity || activity.state === "unknown") {
		reportUnknownSessionField("activity", session.activity?.state);
	}
	return {
		id: session.id,
		terminalHandleId: session.terminalHandleId,
		terminalGeneration: session.terminalGeneration,
		workspaceId: project.id,
		workspaceName: project.name,
		title: session.displayName ?? session.issueId ?? session.id,
		issueId: session.issueId,
		provider: toAgentProvider(session.harness),
		reviewerHarness: toReviewerHarnessId(session.reviewerHarness),
		reviewerConfig: session.reviewerConfig
			? {
				model: session.reviewerConfig.model ?? undefined,
				mode: session.reviewerConfig.mode ?? undefined,
				permissions: session.reviewerConfig.permissions ?? undefined,
			}
			: undefined,
		autoReviewEnabled: session.autoReviewEnabled ?? false,
		kind: session.kind === "orchestrator" ? "orchestrator" : session.kind === "worker" ? "worker" : undefined,
		mode: session.mode === "chat" ? "chat" : "tui",
		branch: session.branch || undefined,
		status,
		scmStatus,
		kanbanColumn,
		displayStatus: session.displayStatus || undefined,
		isTerminated: session.isTerminated,
		chatProviderPreserved: session.chatProviderPreserved,
		terminateOnPrMerge: session.terminateOnPrMerge ?? false,
		autoInjectReview: session.autoInjectReview ?? true,
		autoInjectCI: session.autoInjectCI ?? true,
		createdAt: session.createdAt,
		updatedAt: session.updatedAt,
		lastUserMessageAt: session.lastUserMessageAt ?? undefined,
		activity,
		activeAgentSwitch: session.activeAgentSwitch ? toAgentSwitchSummary(session.activeAgentSwitch) : undefined,
		previewUrl: session.previewUrl,
		previewRevision: session.previewRevision,
		isPinned: session.isPinned ?? false,
		pinnedAt: session.pinnedAt ?? undefined,
		prs: (session.prs ?? []).map(toPullRequestFacts),
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

	if (projectsError || sessionsError) {
		agentSwitchVisibility.setQueryHealthy("active", false, "workspaces");
		agentSwitchVisibility.setQueryHealthy("history", false, "workspaces");
		throw projectsError ?? sessionsError;
	}
	agentSwitchVisibility.setQueryHealthy("active", true, "workspaces");
	agentSwitchVisibility.setQueryHealthy("history", true, "workspaces");

	return (projectsData?.projects ?? []).map((project) => {
		const kind = toProjectKind(project.kind);
		return {
		id: project.id,
		name: project.name,
		kind,
		path: project.path,
		folderMissing: project.folderMissing,
		orchestratorAgent: project.orchestratorAgent ? toAgentProvider(project.orchestratorAgent) : undefined,
			sessions: (sessionsData?.sessions ?? [])
				.filter((session) => session.projectId === project.id)
				.map((session) => toWorkspaceSession(session, project)),
		};
	});
}

// Shared so route loaders can prefetch via queryClient.ensureQueryData (paired
// with the router's defaultPreload: "intent") and the hook reads the same cache.
export const workspaceQueryOptions = {
	queryKey: workspaceQueryKey,
	queryFn: fetchWorkspaces,
	retry: 1,
	staleTime: 10_000,
	refetchInterval: 15_000,
};

// Cloud projects are a separate query so a control-plane failure can never
// break the local list: on error TanStack keeps this query's last known data,
// and until the first successful fetch the merge below simply omits cloud
// items. Invalidated by the cloud create flow (CreateProjectFlow).
export const cloudProjectsQueryKey = ["cloud-projects"] as const;
export const cloudSessionsQueryKey = ["cloud-sessions"] as const;

// Maps one control-plane session onto the board's session shape. Cloud sessions
// carry the same status/activity/harness vocabulary as local ones, so the same
// product-ui mappers apply; fields with no cloud analogue take safe defaults.
function toCloudWorkspaceSession(
	session: CloudCpSession,
	project: CloudCpProject,
	orgId: string,
): WorkspaceSession {
	return {
		id: session.id,
		// The terminal pane only mounts for a session that has a terminal handle.
		// A cloud session's PTY is addressed by the session id over its ticketed
		// CP WebSocket, so the session id is its handle.
		terminalHandleId: session.id,
		workspaceId: project.id,
		workspaceName: project.displayName,
		title: session.displayName || session.id,
		provider: toAgentProvider(session.harness),
		kind: session.kind === "orchestrator" ? "orchestrator" : "worker",
		branch: session.branch || undefined,
		status: toSessionStatus(session.status, session.isTerminated),
		isTerminated: session.isTerminated,
		createdAt: session.createdAt,
		updatedAt: session.updatedAt,
		activity: toSessionActivity({ state: session.activityState }),
		prs: [],
		// Marks this as a control-plane session so the terminal opens against the
		// CP (ticket + sandbox WebSocket) instead of the local daemon mux.
		cloud: { orgId },
	};
}

function toCloudWorkspace(
	project: CloudCpProject,
	sessions: CloudCpSession[],
	orgId: string,
): WorkspaceSummary {
	return {
		id: project.id,
		name: project.displayName,
		kind: "cloud",
		// Cloud projects run in control-plane sandboxes; there is no local folder.
		path: "",
		sessions: sessions
			.filter((session) => session.projectId === project.id)
			.map((session) => toCloudWorkspaceSession(session, project, orgId)),
	};
}

type WorkspaceSubscriptionOptions = {
	subscribed?: boolean;
};

export function useCloudProjectsQuery(options: WorkspaceSubscriptionOptions = {}) {
	const { client, ready, baseUrl } = useCloudCp();
	const { org } = useCloudOrg();
	const orgId = org?.id;
	return useQuery({
		queryKey: [...cloudProjectsQueryKey, baseUrl, orgId ?? ""],
		enabled: ready && orgId !== undefined,
		subscribed: options.subscribed,
		retry: 1,
		queryFn: async (): Promise<CloudCpProject[]> => {
			if (orgId === undefined) return [];
			// First page only (control-plane max page size); pagination UI is a
			// later phase alongside cloud sessions.
			const response = await client.listProjects(orgId, { limit: 100 });
			return response.items;
		},
	});
}

export function useCloudSessionsQuery(options: WorkspaceSubscriptionOptions = {}) {
	const { client, ready, baseUrl } = useCloudCp();
	const { org } = useCloudOrg();
	const orgId = org?.id;
	return useQuery({
		queryKey: [...cloudSessionsQueryKey, baseUrl, orgId ?? ""],
		enabled: ready && orgId !== undefined,
		subscribed: options.subscribed,
		retry: 1,
		// A provisioning sandbox changes state without a client action, so poll to
		// reflect requested -> running -> ready the same way local sessions stream.
		refetchInterval: 5000,
		queryFn: async (): Promise<CloudCpSession[]> => {
			if (orgId === undefined) return [];
			const response = await client.listSessions(orgId, { limit: 100 });
			return response.items;
		},
	});
}

export function useWorkspaceQuery(options: WorkspaceSubscriptionOptions = {}) {
	const local = useQuery({ ...workspaceQueryOptions, subscribed: options.subscribed });
	const cloud = useCloudProjectsQuery(options);
	const cloudSessions = useCloudSessionsQuery(options);
	const { org, ready } = useCloudOrg();
	const orgId = org?.id;
	const localData = local.data;
	const cloudData = cloud.data;
	const cloudSessionData = cloudSessions.data;
	const data = useMemo(() => {
		// Local stays authoritative for loading/error semantics: cloud items only
		// render once the local list exists, and never replace it.
		if (localData === undefined || cloudData === undefined || cloudData.length === 0) return localData;
		// Signing out (or turning the offering off) disables the cloud queries,
		// but react-query keeps their last data; without this gate the stale
		// cloud projects would keep rendering for a signed-out user.
		if (!ready || orgId === undefined) return localData;
		const sessions = cloudSessionData ?? [];
		return [...localData, ...cloudData.map((project) => toCloudWorkspace(project, sessions, orgId))];
	}, [localData, cloudData, cloudSessionData, orgId, ready]);
	return { ...local, data };
}

/**
 * Subscribe a detail surface to one session instead of the complete workspace
 * tree. TanStack Query applies structural sharing to the selected value, so an
 * activity update elsewhere no longer redraws the open session workspace.
 */
export function useWorkspaceSession(sessionId: string) {
	const queryClient = useQueryClient();
	const selectLocalSession = useMemo(
		() => (workspaces: WorkspaceSummary[]) =>
			workspaces.flatMap((workspace) => workspace.sessions).find((session) => session.id === sessionId),
		[sessionId],
	);
	const local = useQuery({ ...workspaceQueryOptions, select: selectLocalSession });
	const localWorkspaces = useQuery({ ...workspaceQueryOptions, subscribed: false, enabled: Boolean(sessionId) });
	const direct = useQuery({
		queryKey: ["session", sessionId],
		enabled: Boolean(sessionId) && local.data === undefined,
		retry: (attempt, error) => apiErrorCode(error) === "SESSION_NOT_FOUND" && attempt < 4,
		retryDelay: 250,
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}", {
				params: { path: { sessionId } },
			});
			if (error) throw error;
			const session = data?.session;
			if (!session) return undefined;
			const project =
				localWorkspaces.data?.find((workspace) => workspace.id === session.projectId) ??
				({ id: session.projectId, name: "" } satisfies Pick<WorkspaceSummary, "id" | "name">);
			return toWorkspaceSession(session, project);
		},
	});
	const cloud = useCloudProjectsQuery();
	const cloudSessions = useCloudSessionsQuery();
	const { org, ready } = useCloudOrg();
	const resolvedDirectSession = useMemo(() => {
		if (!direct.data) return undefined;
		const workspace = localWorkspaces.data?.find((candidate) => candidate.id === direct.data?.workspaceId);
		if (!workspace || direct.data.workspaceName === workspace.name) return direct.data;
		return { ...direct.data, workspaceName: workspace.name };
	}, [direct.data, localWorkspaces.data]);
	const cloudSession = useMemo(() => {
		if (!ready || !org?.id || !cloud.data || !cloudSessions.data) return undefined;
		const session = cloudSessions.data.find((candidate) => candidate.id === sessionId);
		if (!session) return undefined;
		const project = cloud.data.find((candidate) => candidate.id === session.projectId);
		return project ? toCloudWorkspaceSession(session, project, org.id) : undefined;
	}, [cloud.data, cloudSessions.data, org?.id, ready, sessionId]);
	useEffect(() => {
		if (!resolvedDirectSession) return;
		queryClient.setQueryData<WorkspaceSummary[]>(workspaceQueryKey, (current) => {
			if (!current) return current;
			let changed = false;
			const next = current.map((workspace) => {
				if (workspace.id !== resolvedDirectSession.workspaceId) return workspace;
				if (workspace.sessions.some((session) => session.id === resolvedDirectSession.id)) return workspace;
				changed = true;
				return { ...workspace, sessions: [...workspace.sessions, resolvedDirectSession] };
			});
			return changed ? next : current;
		});
	}, [queryClient, resolvedDirectSession]);
	return {
		...local,
		data: local.data ?? resolvedDirectSession ?? cloudSession,
		isLoading: local.isLoading || direct.isLoading,
	};
}

export type WorkspaceScope = {
	project?: Pick<WorkspaceSummary, "id" | "kind" | "name" | "orchestratorAgent">;
	session?: WorkspaceSession;
	orchestrator?: WorkspaceSession;
};

function selectWorkspaceScope(
	workspaces: WorkspaceSummary[],
	projectId: string | undefined,
	sessionId: string | undefined,
): WorkspaceScope {
	const session = sessionId
		? workspaces.flatMap((workspace) => workspace.sessions).find((candidate) => candidate.id === sessionId)
		: undefined;
	const resolvedProjectId = session?.workspaceId ?? projectId;
	const workspace = resolvedProjectId ? workspaces.find((candidate) => candidate.id === resolvedProjectId) : undefined;
	// Do not carry the project's complete sessions array into shell chrome. With
	// React Query's structural sharing, this small metadata projection retains
	// its identity when another session in the same project streams an update.
	const project = workspace
		? {
				id: workspace.id,
				kind: workspace.kind,
				name: workspace.name,
				orchestratorAgent: workspace.orchestratorAgent,
			}
		: undefined;
	return { project, session, orchestrator: workspace ? newestActiveOrchestrator(workspace.sessions) : undefined };
}

/**
 * Subscribe shell chrome to just the routed project and session. This avoids
 * redrawing the topbar for streamed activity from every other project.
 */
export function useWorkspaceScope(projectId?: string, sessionId?: string) {
	const selectLocalScope = useMemo(
		() => (workspaces: WorkspaceSummary[]) => selectWorkspaceScope(workspaces, projectId, sessionId),
		[projectId, sessionId],
	);
	const local = useQuery({ ...workspaceQueryOptions, select: selectLocalScope });
	const cloud = useCloudProjectsQuery();
	const cloudSessions = useCloudSessionsQuery();
	const { org, ready } = useCloudOrg();
	const cloudScope = useMemo(() => {
		if (!ready || !org?.id || !cloud.data) return undefined;
		const workspaces = cloud.data.map((project) => toCloudWorkspace(project, cloudSessions.data ?? [], org.id));
		return selectWorkspaceScope(workspaces, projectId, sessionId);
	}, [cloud.data, cloudSessions.data, org?.id, projectId, ready, sessionId]);
	// Match useWorkspaceQuery's local-first semantics: do not reveal cloud
	// records before the local workspace query has resolved successfully.
	return { ...local, data: local.data ?? (local.isSuccess ? cloudScope : undefined) };
}

function selectTraySessions(workspaces: WorkspaceSummary[]): TraySessionEntry[] {
	const entries: TraySessionEntry[] = [];
	for (const workspace of workspaces) {
		for (const session of workerSessions(workspace.sessions)) {
			const zone = attentionZone(session);
			if ((zone === "merge" && session.status === "merged") || (zone !== "action" && zone !== "merge")) continue;
			entries.push({
				projectId: workspace.id,
				projectName: workspace.name,
				sessionId: session.id,
				title: session.title,
				zone,
			});
		}
	}
	return entries;
}

/**
 * The tray lives for the whole app lifetime, but only attention-worthy worker
 * sessions affect its native payload. Select that compact projection at the
 * query boundary so ordinary streamed activity does not wake the runtime.
 */
export function useWorkspaceTraySessions() {
	const local = useQuery({ ...workspaceQueryOptions, select: selectTraySessions });
	const cloud = useCloudProjectsQuery();
	const cloudSessions = useCloudSessionsQuery();
	const { org, ready } = useCloudOrg();
	const cloudEntries = useMemo(() => {
		if (!ready || !org?.id || !cloud.data) return [];
		return selectTraySessions(cloud.data.map((project) => toCloudWorkspace(project, cloudSessions.data ?? [], org.id)));
	}, [cloud.data, cloudSessions.data, org?.id, ready]);
	const data = useMemo(() => {
		if (local.data === undefined) return undefined;
		return cloudEntries.length === 0 ? local.data : [...local.data, ...cloudEntries];
	}, [cloudEntries, local.data]);
	return { ...local, data };
}
