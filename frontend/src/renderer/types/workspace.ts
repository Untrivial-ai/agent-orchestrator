import { attentionZone as presentationAttentionZone } from "../lib/session-presentation";
import {
	AGENT_OPTIONS,
	toKanbanColumn,
	toSessionActivity,
	toSessionStatus,
	type AgentId,
	type KanbanColumn,
	type SessionActivity,
	type SessionActivityState,
	type SessionStatus,
} from "@aoagents/product-ui";

import type { ReviewerHarnessId } from "../lib/reviewer-harnesses";

export { toKanbanColumn, toSessionActivity, toSessionStatus };
export type { KanbanColumn, SessionActivity, SessionActivityState, SessionStatus };

export type AgentProvider = AgentId | "fake";

/** A file changed in a worker workspace (drives the review rail). */
export type ChangedFile = {
	path: string;
	additions: number;
	deletions: number;
	staged?: boolean;
};

export type SessionKind = "worker" | "orchestrator";

/** Lifecycle state of a single pull request, mirrors the daemon's enum. */
export type PRState = "open" | "draft" | "merged" | "closed";

/**
 * One attributed pull request, mirroring the daemon's SessionPRFacts wire shape.
 * A session can own many (e.g. a stack), so {@link WorkspaceSession.prs} is a
 * list. The wire carries no source/target branch or parent pointer, so the UI
 * renders a flat list of PRs, not a stack tree.
 */
export type PullRequestFacts = {
	url: string;
	number: number;
	state: PRState;
	ci: string;
	review: string;
	mergeability: string;
	reviewComments: boolean;
	updatedAt: string;
};

/** The daemon-committed controller currently responsible for the session. */
export type SessionMode = "chat" | "tui";

export type AgentSwitchSummary = {
	agentHandoffStatus: string;
	errorCode?: string;
	fromHarness: string;
	id: string;
	state: string;
	targetHarness: string;
	updatedAt?: string;
};

export type WorkspaceSession = {
	id: string;
	terminalHandleId?: string;
	/** Opaque controller generation; changes even when a restarted PTY reuses its handle. */
	terminalGeneration?: string;
	workspaceId: string;
	workspaceName: string;
	title: string;
	/** Raw issue/task identifier from the daemon. Intake ids are provider-prefixed. */
	issueId?: string;
	provider: AgentProvider;
	/** Reviewer selected for this session; absent means use the project default. */
	reviewerHarness?: ReviewerHarnessId;
	/** Per-session reviewer override, including hidden fields preserved across saves. */
	reviewerConfig?: {
		model?: string;
		mode?: string;
		permissions?: string;
	};
	/** Whether the daemon may automatically review this session after it becomes idle. */
	autoReviewEnabled?: boolean;
	kind?: SessionKind;
	/**
	 * Which controller is currently committed for this session. The session
	 * surface renders from THIS value, never from the current creation default.
	 * Only the daemon's durable interface-transition coordinator may change it.
	 */
	mode?: SessionMode;
	branch?: string;
	status: SessionStatus;
	/** Stack-aware PR context derived by the daemon independently of runtime activity. */
	scmStatus?: SessionStatus;
	/**
	 * Board lane derived by the daemon from durable delivery facts (PR
	 * lifecycle, review runs, review ownership). `validating` and
	 * `needs_review` are the same review-feedback loop seen from either side:
	 * AO turning it, or a person taking the next turn. The board groups by this
	 * and never re-derives a lane from {@link status}. For a daemon too old to
	 * send one, {@link toKanbanColumn} keeps the placement the status already
	 * implied rather than inventing a new one.
	 */
	kanbanColumn?: KanbanColumn;
	/**
	 * Phrase the daemon derived for what is happening inside
	 * {@link kanbanColumn} — "Reviewing", "Fixing CI failures", "Needs human
	 * review". It arrives renderable, so the UI prints it rather than mapping it.
	 * Absent from a daemon too old to send one, which keeps the label
	 * {@link status} already produced.
	 */
	displayStatus?: string;
	/** Durable runtime fact from the daemon; independent of the derived SCM-aware status. */
	isTerminated?: boolean;
	/** User preference to tear down this session when its PR set completes through a merge. */
	terminateOnPrMerge?: boolean;
	/** Whether SCM review feedback is automatically injected into the worker. */
	autoInjectReview?: boolean;
	/** Whether CI failures are automatically injected into the worker. */
	autoInjectCI?: boolean;
	/** ISO timestamp from the daemon — used for relative time in the inspector. */
	createdAt?: string;
	/** ISO timestamp from the daemon. */
	updatedAt: string;
	/** ISO timestamp of the latest real user-authored message, when known. */
	lastUserMessageAt?: string;
	isPinned?: boolean;
	pinnedAt?: string;
	/** Raw agent lifecycle activity from the daemon. */
	activity?: SessionActivity;
	activeAgentSwitch?: AgentSwitchSummary;
	/**
	 * Live preview target set by the daemon (via `ao preview`) and streamed over
	 * CDC. When non-empty, the browser panel opens and navigates here.
	 */
	previewUrl?: string;
	/**
	 * Monotonic counter the daemon bumps on every `ao preview` call (even when
	 * previewUrl is unchanged), so the browser panel can re-navigate / refresh on
	 * a repeated preview of the same target.
	 */
	previewRevision?: number;
	/** The session's git diff against its base, when known. */
	changedFiles?: ChangedFile[];
	/** Pre-filled commit subject for the Git rail, when known. */
	commitMessage?: string;
	/**
	 * The session's attributed pull requests. One session can own many (a stack
	 * or independent PRs); empty when none are open yet. Status aggregation is
	 * done server-side, so {@link status} already reflects all of these.
	 */
	prs: PullRequestFacts[];
	/**
	 * Present only for sessions that run in a control-plane sandbox. Carries the
	 * org the session is scoped to so its terminal can be opened against the CP;
	 * absent for local sessions, which route through the local daemon.
	 */
	cloud?: { orgId: string };
};

// Tracker providers whose ids the intake daemon stamps sessions with, in
// "<provider>:<native>" form. Adding a provider (Linear, Jira, ...) later is
// just another prefix in this list — no caller of canonicalTrackerIssueId
// needs to change.
const TRACKER_PROVIDER_PREFIXES = ["github:"] as const;

/**
 * The provider-prefixed issue id if `issueId` came from tracker intake, or
 * undefined for manually created sessions (whose issueId, if any, is a plain
 * task title with no provider prefix).
 */
export function canonicalTrackerIssueId(issueId?: string): string | undefined {
	if (!issueId) return undefined;
	return TRACKER_PROVIDER_PREFIXES.some((prefix) => issueId.startsWith(prefix)) ? issueId : undefined;
}

export type ProjectKind = "single_repo" | "workspace" | "scratch";

/** Sentinel `kind` value for projects hosted by the AO cloud control plane. */
export const CLOUD_PROJECT_KIND = "cloud" as const;

const projectKinds = new Set<ProjectKind>(["single_repo", "workspace", "scratch"]);

export function toProjectKind(kind?: string): ProjectKind | undefined {
	return projectKinds.has(kind as ProjectKind) ? (kind as ProjectKind) : undefined;
}

export type WorkspaceRepoSummary = {
	name: string;
	relativePath: string;
	repo: string;
};

// Open PRs (actionable) sort above merged/closed; ties break by number.
const prStateRank: Record<PRState, number> = { open: 0, draft: 1, merged: 2, closed: 3 };

/** A session's PRs ordered actionable-first (open, draft, merged, closed). */
export function sortedPRs(session: WorkspaceSession): PullRequestFacts[] {
	return [...session.prs].sort((a, b) => prStateRank[a.state] - prStateRank[b.state] || a.number - b.number);
}

/** PRs still in flight (open or draft). */
export function openPRs(session: WorkspaceSession): PullRequestFacts[] {
	return session.prs.filter((pr) => pr.state === "open" || pr.state === "draft");
}

export function mergedPRCount(session: WorkspaceSession): number {
	return session.prs.filter((pr) => pr.state === "merged").length;
}

/** The highest-priority PR for compact one-line surfaces (board card, sidebar). */
export function primaryPR(session: WorkspaceSession): PullRequestFacts | undefined {
	return sortedPRs(session)[0];
}

export function isOrchestratorSession(session: WorkspaceSession): boolean {
	return session.kind === "orchestrator" || session.id.endsWith("-orchestrator");
}

/**
 * The project's LIVE orchestrator, if any. Terminated orchestrator rows stay in
 * the session list (the daemon returns all sessions, ordered by spawn number),
 * so an earlier dead orchestrator must not shadow a live one — its zellij
 * session is deleted and attaching to it dead-ends in an instant
 * "[process exited]". No live orchestrator → undefined, so the topbar offers
 * Spawn instead of navigating to a dead session.
 */
export function findProjectOrchestrator(
	workspaces: WorkspaceSummary[],
	projectId: string,
): WorkspaceSession | undefined {
	const workspace = workspaces.find((w) => w.id === projectId);
	return newestActiveOrchestrator(workspace?.sessions ?? []);
}

export function newestOrchestrator(sessions: WorkspaceSession[]): WorkspaceSession | undefined {
	return sessions.filter(isOrchestratorSession).reduce<WorkspaceSession | undefined>(
		(newest, session) => (!newest || sessionNewer(session, newest) ? session : newest),
		undefined,
	);
}

export function newestActiveOrchestrator(sessions: WorkspaceSession[]): WorkspaceSession | undefined {
	return newestOrchestrator(sessions.filter(sessionIsActive));
}

function sessionNewer(a: WorkspaceSession, b: WorkspaceSession): boolean {
	const aCreated = timestamp(a.createdAt);
	const bCreated = timestamp(b.createdAt);
	if (aCreated !== bCreated) return aCreated > bCreated;
	const aUpdated = timestamp(a.updatedAt);
	const bUpdated = timestamp(b.updatedAt);
	if (aUpdated !== bUpdated) return aUpdated > bUpdated;
	return a.id > b.id;
}

function sessionRecentlyUpdatedNewer(a: WorkspaceSession, b: WorkspaceSession): boolean {
	const aUpdated = timestamp(a.updatedAt);
	const bUpdated = timestamp(b.updatedAt);
	if (aUpdated !== bUpdated) return aUpdated > bUpdated;
	const aLastActive = sessionLastActiveTimestamp(a);
	const bLastActive = sessionLastActiveTimestamp(b);
	if (aLastActive !== bLastActive) return aLastActive > bLastActive;
	return a.id > b.id;
}

function sessionLastActiveTimestamp(session: WorkspaceSession): number {
	return (
		validTimestamp(session.activity?.lastActivityAt) ??
		validTimestamp(session.updatedAt) ??
		validTimestamp(session.createdAt) ??
		0
	);
}

function timestamp(value?: string): number {
	return validTimestamp(value) ?? 0;
}

function validTimestamp(value?: string): number | undefined {
	if (!value) return undefined;
	const parsed = Date.parse(value);
	return Number.isNaN(parsed) ? undefined : parsed;
}

export function workerSessions(sessions: WorkspaceSession[]): WorkspaceSession[] {
	return sessions.filter((s) => !isOrchestratorSession(s));
}

/** Worker sessions ordered by session update time, newest first. */
export function sortedWorkerSessions(sessions: WorkspaceSession[]): WorkspaceSession[] {
	return workerSessions(sessions).sort((a, b) =>
		sessionRecentlyUpdatedNewer(b, a) ? 1 : sessionRecentlyUpdatedNewer(a, b) ? -1 : 0,
	);
}

export function sessionIsActive(session: WorkspaceSession): boolean {
	return session.isTerminated !== true && session.status !== "terminated";
}

export function sessionNeedsAttention(session: WorkspaceSession): boolean {
	return presentationAttentionZone(session) === "action";
}

export { attentionZone, attentionZoneLabel, attentionZoneOrder } from "../lib/session-presentation";
export type { AttentionZone } from "../lib/session-presentation";

export type WorkspaceSummary = {
	id: string;
	name: string;
	/**
	 * Discriminator for where the project lives. Local projects carry the
	 * daemon's ProjectKind (or undefined for older daemons); projects hosted by
	 * the AO cloud control plane carry CLOUD_PROJECT_KIND — branch on
	 * `kind === CLOUD_PROJECT_KIND`.
	 */
	kind?: ProjectKind | typeof CLOUD_PROJECT_KIND;
	/** Local checkout path; empty string for cloud projects (no local folder). */
	path: string;
	folderMissing?: boolean;
	workspaceRepos?: WorkspaceRepoSummary[];
	type?: "main" | "worktree";
	orchestratorAgent?: AgentProvider;
	accentColor?: string;
	diff?: {
		additions: number;
		deletions: number;
	};
	sessions: WorkspaceSession[];
};

export function hasConfiguredOrchestratorAgent(
	workspace: Pick<WorkspaceSummary, "orchestratorAgent"> | undefined,
): boolean {
	return Boolean(workspace?.orchestratorAgent);
}

export function orchestratorNeedsRestart(workspace: WorkspaceSummary, orchestrator?: WorkspaceSession): boolean {
	if (!orchestrator || !workspace.orchestratorAgent) return false;
	return orchestrator.provider !== workspace.orchestratorAgent;
}

export type OrchestratorHealth =
	| { state: "ok" }
	| { state: "restarting"; message: string }
	| { state: "restart_needed"; message: string }
	| { state: "missing"; message: string }
	| { state: "duplicates"; message: string };

export function orchestratorHealth(workspace: WorkspaceSummary, restarting = false): OrchestratorHealth {
	if (restarting) {
		return {
			state: "restarting",
			message: "Restarting orchestrator. New tasks wait until the replacement is ready.",
		};
	}
	const active = workspace.sessions.filter((session) => isOrchestratorSession(session) && sessionIsActive(session));
	if (active.length > 1) {
		return {
			state: "duplicates",
			message:
				"Multiple orchestrators are active. The newest one is used; stale ones will be cleaned up on daemon reconcile.",
		};
	}
	const orchestrator = newestActiveOrchestrator(workspace.sessions);
	if (!orchestrator) {
		return { state: "missing", message: "No orchestrator is running for this project." };
	}
	if (orchestratorNeedsRestart(workspace, orchestrator)) {
		return {
			state: "restart_needed",
			message: `Configured orchestrator agent is ${workspace.orchestratorAgent}; running agent is ${orchestrator.provider}.`,
		};
	}
	return { state: "ok" };
}

export function toAgentProvider(provider?: string): AgentProvider {
	if (provider === "fake") return provider;
	return AGENT_OPTIONS.find((candidate) => candidate === provider) ?? "codex";
}
