import { attentionZone as presentationAttentionZone } from "../lib/session-presentation";

import type { ReviewerHarnessId } from "../lib/reviewer-harnesses";

export type SessionStatus =
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
	| "no_signal"
	| "idle"
	| "terminated"
	| "unknown";

const sessionStatuses = new Set<SessionStatus>([
	"working",
	"pr_open",
	"draft",
	"ci_failed",
	"review_pending",
	"changes_requested",
	"approved",
	"mergeable",
	"merged",
	"needs_input",
	"exited",
	"no_signal",
	"idle",
	"terminated",
]);

export function toSessionStatus(status?: string, isTerminated = false): SessionStatus {
	if (status && sessionStatuses.has(status as SessionStatus)) return status as SessionStatus;
	return isTerminated ? "terminated" : "unknown";
}

export type SessionActivityState = "active" | "idle" | "waiting_input" | "blocked" | "exited" | "unknown";

const sessionActivityStates = new Set<SessionActivityState>(["active", "idle", "waiting_input", "blocked", "exited"]);

export type SessionActivity = {
	state: SessionActivityState;
	lastActivityAt: string;
};

export function toSessionActivity(
	activity?: { state?: string; lastActivityAt?: string } | null,
): SessionActivity | undefined {
	if (!activity) return undefined;
	const state = sessionActivityStates.has(activity.state as SessionActivityState)
		? (activity.state as SessionActivityState)
		: "unknown";
	return {
		state,
		lastActivityAt: activity.lastActivityAt ?? "",
	};
}

export type AgentProvider =
	| "codex"
	| "claude-code"
	| "opencode"
	| "aider"
	| "grok"
	| "droid"
	| "amp"
	| "agy"
	| "crush"
	| "cursor"
	| "qwen"
	| "copilot"
	| "goose"
	| "auggie"
	| "continue"
	| "devin"
	| "cline"
	| "kimi"
	| "muse"
	| "kiro"
	| "kilocode"
	| "vibe"
	| "pi"
	| "kimchi"
	| "autohand"
	| "fake";

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

export type WorkspaceSession = {
	id: string;
	terminalHandleId?: string;
	workspaceId: string;
	workspaceName: string;
	title: string;
	/** Raw issue/task identifier from the daemon. Intake ids are provider-prefixed. */
	issueId?: string;
	/**
	 * The orchestrator session that spawned this session, when known. Set by the
	 * daemon at spawn time only when the spawn request came from an
	 * orchestrator's own agent process; absent for sessions spawned directly by
	 * the user/CLI, or spawned before this field existed. Prefer this over
	 * {@link findProjectOrchestrator}'s recency heuristic when resolving which
	 * orchestrator a worker's "Orchestrator" button should link to — see
	 * {@link resolveSessionOrchestrator}.
	 */
	parentOrchestratorId?: string;
	provider: AgentProvider;
	/** Reviewer selected for this session; absent means use the project default. */
	reviewerHarness?: ReviewerHarnessId;
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
	/** Durable runtime fact from the daemon; independent of the derived SCM-aware status. */
	isTerminated?: boolean;
	/** User preference to tear down this session when its PR set completes through a merge. */
	terminateOnPrMerge?: boolean;
	/** Whether SCM review feedback is automatically injected into the worker. */
	autoInjectReview?: boolean;
	/** ISO timestamp from the daemon — used for relative time in the inspector. */
	createdAt?: string;
	/** ISO timestamp from the daemon. */
	updatedAt: string;
	isPinned?: boolean;
	pinnedAt?: string;
	/** Raw agent lifecycle activity from the daemon. */
	activity?: SessionActivity;
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

/**
 * The orchestrator a worker session's "Orchestrator" button should link to.
 * Prefers the session's recorded {@link WorkspaceSession.parentOrchestratorId}
 * — the orchestrator that actually spawned it — resolved against the
 * project's current session list. Falls back to
 * {@link findProjectOrchestrator}'s recency heuristic when no parent is
 * recorded (workers spawned before this field existed, or spawned directly
 * without an orchestrator parent) or the recorded parent can no longer be
 * found (e.g. cleaned up). This is what makes the button correct with 2+
 * concurrent orchestrators in one project: the newest active orchestrator is
 * not necessarily the one that spawned this particular worker.
 */
export function resolveSessionOrchestrator(
	workspaces: WorkspaceSummary[],
	projectId: string,
	session?: WorkspaceSession,
): WorkspaceSession | undefined {
	if (session?.parentOrchestratorId) {
		const workspace = workspaces.find((w) => w.id === projectId);
		const parent = workspace?.sessions.find((s) => s.id === session.parentOrchestratorId);
		if (parent) return parent;
	}
	return findProjectOrchestrator(workspaces, projectId);
}

export function newestActiveOrchestrator(sessions: WorkspaceSession[]): WorkspaceSession | undefined {
	const active = sessions.filter((session) => isOrchestratorSession(session) && sessionIsActive(session));
	return active.reduce<WorkspaceSession | undefined>(
		(newest, session) => (!newest || sessionNewer(session, newest) ? session : newest),
		undefined,
	);
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

function timestamp(value?: string): number {
	if (!value) return 0;
	const parsed = Date.parse(value);
	return Number.isNaN(parsed) ? 0 : parsed;
}

export function workerSessions(sessions: WorkspaceSession[]): WorkspaceSession[] {
	return sessions.filter((s) => !isOrchestratorSession(s));
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
	kind?: ProjectKind;
	path: string;
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
	switch (provider) {
		case "claude-code":
		case "opencode":
		case "aider":
		case "grok":
		case "droid":
		case "amp":
		case "agy":
		case "crush":
		case "cursor":
		case "qwen":
		case "copilot":
		case "goose":
		case "auggie":
		case "continue":
		case "devin":
		case "cline":
		case "kimi":
		case "muse":
		case "kiro":
		case "kilocode":
		case "vibe":
		case "pi":
		case "kimchi":
		case "autohand":
		case "fake":
			return provider;
		default:
			return "codex";
	}
}
