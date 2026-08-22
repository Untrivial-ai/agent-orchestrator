export const SESSION_STATUSES = [
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
	"unknown",
] as const;

export type SessionStatus = (typeof SESSION_STATUSES)[number];

export const SESSION_ACTIVITY_STATES = [
	"active",
	"idle",
	"waiting_input",
	"blocked",
	"exited",
	"unknown",
] as const;

export type SessionActivityState = (typeof SESSION_ACTIVITY_STATES)[number];

export type SessionActivity = {
	state: SessionActivityState;
	lastActivityAt: string;
};

export const KANBAN_COLUMNS = [
	"building",
	"validating",
	"needs_review",
	"ready",
	"archive",
] as const;

/**
 * Where the daemon placed a session in its delivery lifecycle, and who owns the
 * next step. Derived server-side from durable facts, independently of
 * {@link SessionStatus}.
 */
export type KanbanColumn = (typeof KANBAN_COLUMNS)[number];

export function isKanbanColumn(value: string): value is KanbanColumn {
	return KANBAN_COLUMNS.some((column) => column === value);
}

export type SessionStatusModel = {
	status: SessionStatus;
};

export function toSessionStatus(status?: string, isTerminated = false): SessionStatus {
	if (status && isSessionStatus(status)) return status;
	return isTerminated ? "terminated" : "unknown";
}

export function toSessionActivity(
	activity?: { state?: string; lastActivityAt?: string } | null,
): SessionActivity | undefined {
	if (!activity) {
		return undefined;
	}
	return {
		state: activity.state && isSessionActivityState(activity.state) ? activity.state : "unknown",
		lastActivityAt: activity.lastActivityAt ?? "",
	};
}

function isSessionStatus(value: string): value is SessionStatus {
	return SESSION_STATUSES.some((status) => status === value);
}

function isSessionActivityState(value: string): value is SessionActivityState {
	return SESSION_ACTIVITY_STATES.some((state) => state === value);
}
