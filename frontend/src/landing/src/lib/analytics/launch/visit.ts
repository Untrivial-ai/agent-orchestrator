/**
 * Deciding and applying the once-per-visit launch events.
 *
 * Everything with a failure mode lives here so it is unit-testable: the dedupe
 * semantics, the consent wait, the guarded storage reads/writes, and the order
 * events fire before their markers are written. The browser dependencies are
 * injectable, and `LaunchAnalytics` only wires them up — the same seam shape as
 * `marketing-consent.ts` (client injected) and `campaign.ts` (storage
 * injected), which also each have a single production caller.
 */

import { MARKETING_ACCEPTED_CONSENT } from "../marketing-consent";
import { ANALYTICS_CONSENT_KEY } from "../../constants";
import type { LaunchSource } from "./context";
import { LAUNCH_EVENTS } from "./events";

export type VisitState = {
	source: LaunchSource;
	/** The browser has visited before (persistent marker present). */
	seenBefore: boolean;
	/** This tab session's initial visit has been counted. */
	sessionCounted: boolean;
	/** ph_referral_visit already fired in this tab session. */
	phReferralFired: boolean;
	sessionStorageAvailable: boolean;
	localStorageAvailable: boolean;
};

export type VisitPlan = {
	/** A seen browser in a NEW tab session (not a reload of its first visit). */
	fireReturnVisit: boolean;
	/** Product Hunt traffic, once per tab session. */
	firePhReferralVisit: boolean;
	/** Count this session's initial visit, even when nothing fired: it is what
	 * stops a reload of a first-ever visit from counting as a return. */
	markSessionCounted: boolean;
	/** Persist the seen marker for future sessions. */
	markSeen: boolean;
	/** Record ph_referral_visit so it does not re-fire in this session. */
	markPhReferralFired: boolean;
};

export function planLaunchEvents(state: VisitState): VisitPlan {
	// Without session storage a reload is indistinguishable from a new tab
	// session. ph_referral_visit keeps its documented per-load degradation (the
	// launch's primary metric is worth a duplicate), but the softer return
	// signal is suppressed rather than guessed at: firing it per load would
	// count a first-ever visit's reload as a return.
	const canDedupe = state.sessionStorageAvailable;
	const firePhReferralVisit =
		state.source === "product_hunt" && !state.phReferralFired;
	return {
		fireReturnVisit:
			state.seenBefore && !state.sessionCounted && canDedupe,
		firePhReferralVisit,
		markSessionCounted: canDedupe && !state.sessionCounted,
		markSeen: state.localStorageAvailable && !state.seenBefore,
		markPhReferralFired: canDedupe && firePhReferralVisit,
	};
}

/** localStorage: this browser has visited before (return-visit signal). */
export const SEEN_KEY = "ao.launch.seen";
/** sessionStorage: this tab session's initial visit has been counted. */
export const SESSION_COUNTED_KEY = "ao.launch.return_fired";
/** sessionStorage: ph_referral_visit already fired in this tab session. */
export const PH_REFERRAL_KEY = "ao.launch.ph_referral";

/** The storage the visit markers live in; injectable for tests. */
export type VisitStorage = {
	local: Pick<Storage, "getItem" | "setItem">;
	session: Pick<Storage, "getItem" | "setItem">;
};

/** Reads `get`, treating an unavailable/blocked store as absent. */
function available<T>(get: () => T | undefined): T | undefined {
	try {
		return get();
	} catch {
		return undefined;
	}
}

/** Reads the dedupe markers; a blocked store reads as absent and unavailable. */
export function readVisitState(
	source: LaunchSource,
	storage?: VisitStorage,
): VisitState {
	const local = available(
		() => storage?.local ?? (typeof window === "undefined" ? undefined : window.localStorage),
	);
	const session = available(
		() => storage?.session ?? (typeof window === "undefined" ? undefined : window.sessionStorage),
	);
	// A store is "available" only if a probe read succeeds: a blocked store
	// can throw on access (private mode) or on the operation itself
	// (enterprise policies), and either way it reads as absent — never as a
	// crash — and is reported unavailable so no marker write is planned.
	const probe = (
		store: typeof local,
		key: string,
	): { available: boolean; value: string | null } => {
		if (!store) return { available: false, value: null };
		try {
			return { available: true, value: store.getItem(key) };
		} catch {
			return { available: false, value: null };
		}
	};
	const seen = probe(local, SEEN_KEY);
	const counted = probe(session, SESSION_COUNTED_KEY);
	const phFired = probe(session, PH_REFERRAL_KEY);
	return {
		source,
		seenBefore: seen.value === "1",
		sessionCounted: counted.value === "1",
		phReferralFired: phFired.value === "1",
		localStorageAvailable: seen.available,
		sessionStorageAvailable: counted.available && phFired.available,
	};
}

/**
 * Fires the plan's events and writes its markers, in that order: a marker
 * written before its event would suppress the event entirely if the write
 * succeeded and the event did not; the reverse order risks only a duplicate,
 * which is the cheap failure.
 */
export function applyVisitPlan(
	plan: VisitPlan,
	track: (event: string) => void,
	storage?: VisitStorage,
): void {
	if (plan.fireReturnVisit) track(LAUNCH_EVENTS.returnVisit);
	if (plan.firePhReferralVisit) track(LAUNCH_EVENTS.phReferralVisit);

	const local = available(
		() => storage?.local ?? (typeof window === "undefined" ? undefined : window.localStorage),
	);
	const session = available(
		() => storage?.session ?? (typeof window === "undefined" ? undefined : window.sessionStorage),
	);
	// Each write is guarded independently: quota errors must not abort the rest.
	try {
		if (local && plan.markSeen) local.setItem(SEEN_KEY, "1");
	} catch {
		// Storage blocked.
	}
	try {
		if (session && plan.markSessionCounted) {
			session.setItem(SESSION_COUNTED_KEY, "1");
		}
		if (session && plan.markPhReferralFired) {
			session.setItem(PH_REFERRAL_KEY, "1");
		}
	} catch {
		// Storage blocked.
	}
}

/** Poll for consent while the banner is up, so the events are not lost. */
const CONSENT_POLL_MS = 1_000;
const CONSENT_POLL_TRIES = 120;

/** True once the visitor has accepted analytics. */
export function consentAccepted(
	storage?: Pick<Storage, "getItem">,
): boolean {
	return available(
		() =>
			storage ??
			(typeof window === "undefined" ? undefined : window.localStorage),
	)?.getItem(ANALYTICS_CONSENT_KEY) === MARKETING_ACCEPTED_CONSENT;
}

/**
 * Runs `fire` once consent is accepted — immediately if it already is,
 * otherwise on a bounded poll while the cookie banner is up. Returns a
 * cleanup that stops waiting (component unmount / strict-mode remount).
 */
export function whenConsented(
	fire: () => void,
	isAccepted: () => boolean = consentAccepted,
): () => void {
	if (isAccepted()) {
		fire();
		return () => {};
	}
	let tries = 0;
	const timer = setInterval(() => {
		tries += 1;
		if (isAccepted()) {
			clearInterval(timer);
			fire();
		} else if (tries >= CONSENT_POLL_TRIES) {
			clearInterval(timer);
		}
	}, CONSENT_POLL_MS);
	return () => clearInterval(timer);
}
