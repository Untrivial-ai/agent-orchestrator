// State + capture for the in-product survey.
//
// Pure and injectable (storage, clock, capture are all passed in) so the rules
// are unit-testable without a browser or PostHog. It does NOT use PostHog's
// native survey feature: the renderer disables /surveys and /flags polling to
// avoid per-request billing, so answers are captured as ordinary events.
//
// State is held in memory and persisted best-effort. If storage is blocked
// (private mode, quota), the survey still works for the session and a completed
// response still emits every answer; only cross-session memory is lost.

const STATE_KEY = "ao.survey.state.v1";
/** How long the sidebar invite stays hidden after the user crosses it. */
export const INVITE_SNOOZE_MS = 48 * 60 * 60 * 1000;

type State = {
	answers: Record<string, string>; // questionId -> answer (multi joined with ", ")
	completed: boolean; // finished once -> never invite again
	inviteDismissedAt: number; // ms epoch the invite was crossed -> 48h quiet
	optedOut: boolean; // chose "don't show again" -> never invite again
};

// A fresh, unshared state every time — never hand out a reference to a module
// constant, or a later answers[id] = ... would mutate it for everyone.
function emptyState(): State {
	return { answers: {}, completed: false, inviteDismissedAt: 0, optedOut: false };
}

export type Storage = Pick<globalThis.Storage, "getItem" | "setItem">;
export type Capture = (event: string, properties?: Record<string, unknown>) => void;

export type SurveyDeps = {
	storage?: Storage;
	now?: () => number;
	capture?: Capture;
};

export class SurveyController {
	private storage?: Storage;
	private now: () => number;
	private capture: Capture;
	private state: State;

	constructor(deps: SurveyDeps = {}) {
		this.storage = deps.storage;
		this.now = deps.now ?? (() => Date.now());
		this.capture = deps.capture ?? (() => {});
		this.state = this.load();
	}

	private load(): State {
		const base = emptyState();
		if (!this.storage) return base;
		try {
			const raw = this.storage.getItem(STATE_KEY);
			if (!raw) return base;
			const parsed = JSON.parse(raw) as Partial<State>;
			// Merge onto a fresh base; force a fresh answers object so we never
			// alias a shared reference when the stored blob omits the key.
			return { ...base, ...parsed, answers: { ...(parsed.answers ?? {}) } };
		} catch {
			return base;
		}
	}

	private persist(): void {
		try {
			this.storage?.setItem(STATE_KEY, JSON.stringify(this.state));
		} catch {
			// Storage blocked (private mode): state stays in memory for the session.
		}
	}

	/** Record one answer (a single choice, a multi-select list, or free text)
	 * and emit the per-question event for step-level analysis. */
	answer(id: string, value: string | string[]): void {
		const choice = Array.isArray(value) ? value.join(", ") : value;
		this.state.answers[id] = choice;
		this.persist();
		this.capture("ao.renderer.survey_answered", {
			survey: id,
			choice,
			...(Array.isArray(value) ? { choices: value } : {}),
		});
	}

	/** User finished the survey: record completion (never invite again) and emit
	 * one event carrying every answer as answer_<id>, so a whole response reads
	 * as a single row for metrics without joining per-question events. Reads from
	 * in-memory state, so the row is complete even when storage never persisted. */
	markCompleted(): void {
		this.state.completed = true;
		this.persist();
		const props: Record<string, unknown> = {};
		for (const [id, value] of Object.entries(this.state.answers)) {
			props[`answer_${id.replace(/-/g, "_")}`] = value;
		}
		this.capture("ao.renderer.survey_completed", props);
	}

	/** The sidebar invite is eligible when the user has not completed the survey,
	 * has not opted out for good, and is not within the 48h quiet window after
	 * crossing it. */
	inviteEligible(): boolean {
		if (this.state.completed || this.state.optedOut) return false;
		return this.now() - this.state.inviteDismissedAt >= INVITE_SNOOZE_MS;
	}

	/** User crossed the invite: hush it for 48 hours. */
	dismissInvite(): void {
		this.state.inviteDismissedAt = this.now();
		this.persist();
		this.capture("ao.renderer.survey_invite_dismissed", {});
	}

	/** User chose "don't show again": retire the invite for good. */
	optOut(): void {
		this.state.optedOut = true;
		this.persist();
		this.capture("ao.renderer.survey_invite_opted_out", {});
	}
}
