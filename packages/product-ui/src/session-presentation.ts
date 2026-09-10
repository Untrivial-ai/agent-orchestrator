import { isDisplayStatus, isKanbanColumn } from "./session-models";
import type {
	DisplayStatus,
	KanbanColumn,
	SessionActivity,
	SessionActivityState,
	SessionStatus,
	SessionStatusModel,
} from "./session-models";

export type SessionPresentationMessageKey =
	| `activity.${SessionActivityState}`
	| `status.${SessionStatus}`
	| `zone.${AttentionZone}`
	| `column.${KanbanColumn}`
	| `timeline.${SessionTimelinePillStatus}`
	| (typeof displayStatusLabelKeys)[DisplayStatus];

export type ProductUITranslator = (
	key: SessionPresentationMessageKey,
	values?: Readonly<Record<string, string | number>>,
) => string;

const englishLabels: Record<SessionPresentationMessageKey, string> = {
	"activity.active": "Working",
	"activity.idle": "Idle",
	"activity.waiting_input": "Input Needed",
	"activity.blocked": "Awaiting Decision",
	"activity.exited": "Exited",
	"activity.unknown": "Unknown",
	"status.working": "Working",
	"status.idle": "Idle",
	"status.needs_input": "Input needed",
	"status.exited": "Exited",
	"status.no_signal": "No signal",
	"status.ci_failed": "CI failed",
	"status.changes_requested": "Changes requested",
	"status.review_pending": "Review pending",
	"status.draft": "Draft PR",
	"status.pr_open": "PR open",
	"status.approved": "Approved",
	"status.mergeable": "Ready",
	"status.merged": "Merged",
	"status.terminated": "Terminated",
	"status.unknown": "Unknown status",
	"zone.merge": "Ready to merge",
	"zone.action": "Needs you",
	"zone.pending": "In review",
	"zone.working": "Working",
	"zone.done": "Terminated",
	"column.building": "Building",
	"column.validating": "Validating",
	// Deliberate: this lane is the review-feedback loop, not a queue of PRs
	// awaiting a first human review. It is the fallthrough of
	// derivePRKanbanColumn, so a card lands here whenever the PR is in its
	// review cycle and no AO loop is turning it -- awaiting review, carrying
	// feedback someone has to answer, or holding a failing check someone has to
	// decide about. The next turn is a person's, but the loop is the same one
	// "validating" holds while AO turns it. The enum stays needs_review, so
	// different wording later is one string per locale.
	"column.needs_review": "In review",
	"column.ready": "Ready",
	"column.archive": "Archive",
	"timeline.no_signal": "No Signal",
	"timeline.ci_failed": "CI Failed",
	"timeline.changes_requested": "Changes Requested",
	"displayStatus.working": "Working",
	"displayStatus.blocked": "Blocked",
	"displayStatus.exited": "Exited",
	"displayStatus.noSignal": "No signal",
	"displayStatus.awaitingPr": "Awaiting PR",
	"displayStatus.fixingCiFailures": "Fixing CI failures",
	"displayStatus.addressingComments": "Addressing comments",
	"displayStatus.needsReview": "Needs review",
	"displayStatus.reviewScheduled": "Review scheduled",
	"displayStatus.reviewing": "Reviewing",
	"displayStatus.reviewPending": "Review pending",
	"displayStatus.draft": "Draft",
	"displayStatus.ciFailing": "CI failing",
	"displayStatus.commented": "Commented",
	"displayStatus.changesRequested": "Changes requested",
	"displayStatus.needsHumanReview": "Needs human review",
	"displayStatus.mergeable": "Mergeable",
	"displayStatus.approved": "Approved",
	"displayStatus.merged": "Merged",
	"displayStatus.closedWithoutMerge": "Closed without merge",
	"displayStatus.terminated": "Terminated",
};

/**
 * Exhaustive mapping from the daemon's wire phrase to a stable, namespaced
 * translation key -- decoupled from the exact English wording so a future
 * phrase edit on the daemon does not silently break every locale's lookup.
 */
export const displayStatusLabelKeys: Record<DisplayStatus, `displayStatus.${string}`> = {
	Working: "displayStatus.working",
	Blocked: "displayStatus.blocked",
	Exited: "displayStatus.exited",
	"No signal": "displayStatus.noSignal",
	"Awaiting PR": "displayStatus.awaitingPr",
	"Fixing CI failures": "displayStatus.fixingCiFailures",
	"Addressing comments": "displayStatus.addressingComments",
	"Needs review": "displayStatus.needsReview",
	"Review scheduled": "displayStatus.reviewScheduled",
	Reviewing: "displayStatus.reviewing",
	"Review pending": "displayStatus.reviewPending",
	Draft: "displayStatus.draft",
	"CI failing": "displayStatus.ciFailing",
	Commented: "displayStatus.commented",
	"Changes requested": "displayStatus.changesRequested",
	"Needs human review": "displayStatus.needsHumanReview",
	Mergeable: "displayStatus.mergeable",
	Approved: "displayStatus.approved",
	Merged: "displayStatus.merged",
	"Closed without merge": "displayStatus.closedWithoutMerge",
	Terminated: "displayStatus.terminated",
};

/**
 * Translates the daemon's `displayStatus` phrase for the current locale. A
 * phrase this build does not recognize yet (a newer daemon added one before
 * the frontend shipped a translation for it) falls back to the raw English
 * text the API guarantees is already renderable, exactly as an older client
 * without this lookup would show it.
 */
export function getDisplayStatusLabel(
	displayStatus: string,
	translate: ProductUITranslator = defaultProductUITranslator,
): string {
	if (!isDisplayStatus(displayStatus)) return displayStatus;
	return translate(displayStatusLabelKeys[displayStatus]);
}

export const defaultProductUITranslator: ProductUITranslator = (key) => englishLabels[key];

export type AgentActivityView = {
	state: SessionActivityState;
	label: string;
	tone: string;
	dotClassName: string;
	indicatorClassName: string;
	breathe: boolean;
};

type AgentActivityBase = Omit<AgentActivityView, "label" | "indicatorClassName"> & {
	labelKey: SessionPresentationMessageKey;
};

const agentActivityBases: Record<SessionActivityState, AgentActivityBase> = {
	active: {
		state: "active",
		labelKey: "activity.active",
		tone: "var(--color-status-working)",
		dotClassName: "bg-status-working",
		breathe: true,
	},
	idle: {
		state: "idle",
		labelKey: "activity.idle",
		tone: "var(--color-status-idle)",
		dotClassName: "bg-status-idle",
		breathe: false,
	},
	waiting_input: {
		state: "waiting_input",
		labelKey: "activity.waiting_input",
		tone: "var(--color-status-needs-you)",
		dotClassName: "bg-status-needs-you",
		breathe: false,
	},
	blocked: {
		state: "blocked",
		labelKey: "activity.blocked",
		tone: "var(--color-status-needs-you)",
		dotClassName: "bg-status-needs-you",
		breathe: false,
	},
	exited: {
		state: "exited",
		labelKey: "activity.exited",
		tone: "var(--color-status-exited)",
		dotClassName: "bg-status-exited",
		breathe: false,
	},
	unknown: {
		state: "unknown",
		labelKey: "activity.unknown",
		tone: "var(--color-status-unknown)",
		dotClassName: "bg-status-unknown",
		breathe: false,
	},
};

export function getAgentActivityView(
	activity?: SessionActivity | null,
	translate: ProductUITranslator = defaultProductUITranslator,
): AgentActivityView {
	const state = activity?.state ?? "unknown";
	const base = agentActivityBases[state] ?? agentActivityBases.unknown;
	const { labelKey, ...view } = base;
	return {
		...view,
		label: translate(labelKey),
		indicatorClassName: `${view.dotClassName}${view.breathe ? " animate-status-pulse" : ""}`,
	};
}

export function isAgentActivityWorking(activity?: SessionActivity | null): boolean {
	return activity?.state === "active";
}

export type SessionStatusView = {
	label: string;
	className: string;
	/** Same tone as `className`, for surfaces that paint a dot instead of text. */
	dotClassName: string;
	cardClassName?: string;
};

const sessionStatusStyles: Record<SessionStatus, Omit<SessionStatusView, "label">> = {
	working: { className: "text-status-working", dotClassName: "bg-status-working" },
	idle: { className: "text-status-idle", dotClassName: "bg-status-idle" },
	needs_input: { className: "text-status-needs-you", dotClassName: "bg-status-needs-you" },
	exited: { className: "text-status-exited", dotClassName: "bg-status-exited" },
	no_signal: { className: "text-status-unknown", dotClassName: "bg-status-unknown" },
	ci_failed: { className: "text-status-exited", dotClassName: "bg-status-exited" },
	changes_requested: { className: "text-status-needs-you", dotClassName: "bg-status-needs-you" },
	review_pending: { className: "text-status-in-review", dotClassName: "bg-status-in-review" },
	draft: { className: "text-status-in-review", dotClassName: "bg-status-in-review" },
	pr_open: { className: "text-status-in-review", dotClassName: "bg-status-in-review" },
	approved: { className: "text-status-ready", dotClassName: "bg-status-ready" },
	mergeable: { className: "text-status-ready", dotClassName: "bg-status-ready" },
	merged: { className: "text-status-merged", dotClassName: "bg-status-merged" },
	terminated: {
		className: "text-status-terminated-foreground",
		dotClassName: "bg-status-terminated",
		cardClassName: "session-card-terminated",
	},
	unknown: { className: "text-status-unknown", dotClassName: "bg-status-unknown" },
};

export function getSessionStatusView(
	status: SessionStatus,
	translate: ProductUITranslator = defaultProductUITranslator,
): SessionStatusView {
	const normalizedStatus = sessionStatusStyles[status] ? status : "unknown";
	return {
		...sessionStatusStyles[normalizedStatus],
		label: translate(`status.${normalizedStatus}`),
	};
}

export type AttentionZone = "merge" | "action" | "pending" | "working" | "done";

export type AttentionZoneView = {
	zone: AttentionZone;
	label: string;
	glow: string;
	dot: string;
	dotGlow: boolean;
	titleClassName: string;
	dotClassName: string;
};

type AttentionZoneBase = Omit<AttentionZoneView, "label"> & {
	labelKey: SessionPresentationMessageKey;
};

const attentionZoneBases: Record<AttentionZone, AttentionZoneBase> = {
	working: {
		zone: "working",
		labelKey: "zone.working",
		glow: "color-mix(in srgb, var(--color-status-working) 7%, transparent)",
		dot: "var(--color-status-working)",
		dotGlow: true,
		titleClassName: "text-status-working",
		dotClassName: "bg-status-working",
	},
	action: {
		zone: "action",
		labelKey: "zone.action",
		glow: "color-mix(in srgb, var(--color-status-needs-you) 6%, transparent)",
		dot: "var(--color-status-needs-you)",
		dotGlow: true,
		titleClassName: "text-status-needs-you",
		dotClassName: "bg-status-needs-you",
	},
	pending: {
		zone: "pending",
		labelKey: "zone.pending",
		glow: "color-mix(in srgb, var(--color-status-in-review) 5%, transparent)",
		dot: "var(--color-status-in-review)",
		dotGlow: false,
		titleClassName: "text-status-in-review",
		dotClassName: "bg-status-in-review",
	},
	merge: {
		zone: "merge",
		labelKey: "zone.merge",
		glow: "color-mix(in srgb, var(--color-status-ready) 7%, transparent)",
		dot: "var(--color-status-ready)",
		dotGlow: true,
		titleClassName: "text-status-ready",
		dotClassName: "bg-status-ready",
	},
	done: {
		zone: "done",
		labelKey: "zone.done",
		glow: "var(--color-overlay-faint)",
		dot: "var(--color-status-terminated)",
		dotGlow: false,
		titleClassName: "text-status-terminated-foreground",
		dotClassName: "bg-status-terminated",
	},
};

export const attentionZoneOrder: AttentionZone[] = ["merge", "action", "pending", "working", "done"];
export const boardAttentionZoneOrder: AttentionZone[] = ["working", "action", "pending", "merge"];

/**
 * Board lanes in delivery order: building -> validating -> in review ->
 * ready. The middle two are the same review-feedback loop seen from either
 * side: validating while AO turns it, in review while a person does.
 * `archive` is deliberately absent — terminated sessions render in the archive
 * sheet, not as a lane.
 */
export const boardKanbanColumnOrder: KanbanColumn[] = [
	"building",
	"validating",
	"needs_review",
	"ready",
];

/**
 * Resolve the lane a session belongs in. The daemon derives the column from
 * durable delivery facts and clients render what it sends; this only fills the
 * gap when the field is absent or unrecognized.
 *
 * The fallback keeps the placement the session already had. A daemon that
 * predates `kanbanColumn` still reports `status`, and mapping that through the
 * attention zone reproduces exactly the lane the board used before the column
 * existed — so a mixed-version upgrade leaves cards where the user last saw
 * them instead of collapsing every live session into the leftmost lane.
 */
export function toKanbanColumn(column: string | undefined, status: SessionStatus): KanbanColumn {
	if (column && isKanbanColumn(column)) return column;
	switch (attentionZone(status)) {
		case "merge":
			return "ready";
		case "action":
			return "needs_review";
		case "pending":
			return "validating";
		case "done":
			return "archive";
		case "working":
			return "building";
	}
}

export type KanbanColumnView = {
	column: KanbanColumn;
	label: string;
	glow: string;
	dot: string;
	dotGlow: boolean;
	titleClassName: string;
	dotClassName: string;
};

type KanbanColumnBase = Omit<KanbanColumnView, "label"> & {
	labelKey: SessionPresentationMessageKey;
};

const kanbanColumnBases: Record<KanbanColumn, KanbanColumnBase> = {
	building: {
		column: "building",
		labelKey: "column.building",
		glow: "color-mix(in srgb, var(--color-status-working) 7%, transparent)",
		dot: "var(--color-status-working)",
		dotGlow: true,
		titleClassName: "text-status-working",
		dotClassName: "bg-status-working",
	},
	validating: {
		column: "validating",
		labelKey: "column.validating",
		glow: "color-mix(in srgb, var(--color-status-validating) 5%, transparent)",
		dot: "var(--color-status-validating)",
		dotGlow: false,
		titleClassName: "text-status-validating",
		dotClassName: "bg-status-validating",
	},
	needs_review: {
		column: "needs_review",
		labelKey: "column.needs_review",
		glow: "color-mix(in srgb, var(--color-status-in-review) 5%, transparent)",
		dot: "var(--color-status-in-review)",
		dotGlow: false,
		titleClassName: "text-status-in-review",
		dotClassName: "bg-status-in-review",
	},
	ready: {
		column: "ready",
		labelKey: "column.ready",
		glow: "color-mix(in srgb, var(--color-status-ready) 7%, transparent)",
		dot: "var(--color-status-ready)",
		dotGlow: true,
		titleClassName: "text-status-ready",
		dotClassName: "bg-status-ready",
	},
	archive: {
		column: "archive",
		labelKey: "column.archive",
		glow: "var(--color-overlay-faint)",
		dot: "var(--color-status-terminated)",
		dotGlow: false,
		titleClassName: "text-status-terminated-foreground",
		dotClassName: "bg-status-terminated",
	},
};

export function getKanbanColumnView(
	column: KanbanColumn,
	translate: ProductUITranslator = defaultProductUITranslator,
): KanbanColumnView {
	const { labelKey, ...view } = kanbanColumnBases[column];
	return { ...view, label: translate(labelKey) };
}

export function attentionZone(input: SessionStatus | SessionStatusModel): AttentionZone {
	const status = typeof input === "string" ? input : input.status;
	switch (status) {
		case "merged":
		case "approved":
		case "mergeable":
			return "merge";
		case "terminated":
			return "done";
		case "needs_input":
		case "exited":
		case "no_signal":
		case "ci_failed":
		case "changes_requested":
		case "unknown":
			return "action";
		case "review_pending":
		case "pr_open":
		case "draft":
			return "pending";
		case "working":
		case "idle":
			return "working";
	}
}

export function getAttentionZoneView(
	status: SessionStatus,
	translate: ProductUITranslator = defaultProductUITranslator,
): AttentionZoneView {
	return getAttentionZoneViewForZone(attentionZone(status), translate);
}

export function getAttentionZoneViewForZone(
	zone: AttentionZone,
	translate: ProductUITranslator = defaultProductUITranslator,
): AttentionZoneView {
	const { labelKey, ...view } = attentionZoneBases[zone];
	return { ...view, label: translate(labelKey) };
}

export type SessionTimelinePillStatus = Extract<
	SessionStatus,
	"no_signal" | "ci_failed" | "changes_requested"
>;

export type SessionTimelinePillView = {
	label: string;
	tone: string;
	breathe: boolean;
};

const sessionTimelinePillBases: Record<
	SessionTimelinePillStatus,
	{ labelKey: SessionPresentationMessageKey; tone: string; breathe: boolean }
> = {
	no_signal: {
		labelKey: "timeline.no_signal",
		tone: "var(--color-status-unknown)",
		breathe: false,
	},
	ci_failed: {
		labelKey: "timeline.ci_failed",
		tone: "var(--color-status-exited)",
		breathe: false,
	},
	changes_requested: {
		labelKey: "timeline.changes_requested",
		tone: "var(--color-status-needs-you)",
		breathe: false,
	},
};

export function getSessionTimelinePillView(
	status: SessionTimelinePillStatus,
	translate: ProductUITranslator = defaultProductUITranslator,
): SessionTimelinePillView {
	const base = sessionTimelinePillBases[status];
	return {
		label: translate(base.labelKey),
		tone: base.tone,
		breathe: base.breathe,
	};
}

export function isSessionIdle(session: SessionStatusModel): boolean {
	return session.status === "idle";
}
