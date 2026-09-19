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

/**
 * Board lanes are a presentation grouping over the daemon's {@link KanbanColumn}.
 * `planning` and `building` split the daemon's pre-PR `building` column by the
 * session's user-controlled workflow mode; `review` groups the two
 * review-feedback columns (`validating` + `needs_review`) the user asked to see
 * as one. `archive` is kept so terminated cards still resolve. The daemon never
 * sends these values — {@link toBoardLane} derives them client-side.
 */
export const BOARD_LANES = ["planning", "building", "review", "ready", "archive"] as const;

export type BoardLane = (typeof BOARD_LANES)[number];

export function isBoardLane(value: string): value is BoardLane {
	return BOARD_LANES.some((lane) => lane === value);
}

/** User-controlled delivery stage persisted on the session row. */
export const WORKFLOW_MODES = ["planning", "building"] as const;

export type WorkflowMode = (typeof WORKFLOW_MODES)[number];

export const DISPLAY_STATUSES = [
	"Working",
	"Blocked",
	"Exited",
	"No signal",
	"Awaiting PR",
	"Fixing CI failures",
	"Addressing comments",
	"Needs review",
	"Review scheduled",
	"Reviewing",
	"Review pending",
	"Draft",
	"CI failing",
	"Commented",
	"Changes requested",
	"Needs human review",
	"Mergeable",
	"Approved",
	"Merged",
	"Closed without merge",
	"Terminated",
] as const;

/**
 * The daemon's phrase for what is happening inside a session's
 * {@link KanbanColumn} right now. The wire value is already an English phrase
 * (the API's deliberate shape, so an old client can print it with no mapping
 * table); {@link isDisplayStatus} narrows it to this known set so
 * `getDisplayStatusLabel` can look up a locale string instead of printing that
 * English text unconditionally.
 */
export type DisplayStatus = (typeof DISPLAY_STATUSES)[number];

export function isDisplayStatus(value: string): value is DisplayStatus {
	return DISPLAY_STATUSES.some((status) => status === value);
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
