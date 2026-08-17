import type {
	SessionActivity,
	SessionActivityState,
	SessionStatus,
	SessionStatusModel,
} from "./session-models";

export type SessionPresentationMessageKey =
	| `activity.${SessionActivityState}`
	| `status.${SessionStatus}`
	| `zone.${AttentionZone}`
	| `timeline.${SessionTimelinePillStatus}`;

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
	"timeline.no_signal": "No Signal",
	"timeline.ci_failed": "CI Failed",
	"timeline.changes_requested": "Changes Requested",
};

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
	cardClassName?: string;
};

const sessionStatusStyles: Record<SessionStatus, Omit<SessionStatusView, "label">> = {
	working: { className: "text-status-working" },
	idle: { className: "text-status-idle" },
	needs_input: { className: "text-status-needs-you" },
	exited: { className: "text-status-exited" },
	no_signal: { className: "text-status-unknown" },
	ci_failed: { className: "text-status-exited" },
	changes_requested: { className: "text-status-needs-you" },
	review_pending: { className: "text-status-in-review" },
	draft: { className: "text-status-in-review" },
	pr_open: { className: "text-status-in-review" },
	approved: { className: "text-status-ready" },
	mergeable: { className: "text-status-ready" },
	merged: { className: "text-status-merged" },
	terminated: {
		className: "text-status-terminated-foreground",
		cardClassName: "session-card-terminated",
	},
	unknown: { className: "text-status-unknown" },
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
