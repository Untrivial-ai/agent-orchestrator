import { describe, expect, it, vi } from "vitest";

import {
	applyVisitPlan,
	consentAccepted,
	planLaunchEvents,
	PH_REFERRAL_KEY,
	readVisitState,
	SEEN_KEY,
	SESSION_COUNTED_KEY,
	whenConsented,
	type VisitStorage,
} from "./visit";

const base = {
	sessionStorageAvailable: true,
	localStorageAvailable: true,
};

describe("planLaunchEvents", () => {
	it("fires ph_referral_visit but not return_visit on a first-ever PH visit", () => {
		expect(
			planLaunchEvents({
				...base,
				source: "product_hunt",
				seenBefore: false,
				sessionCounted: false,
				phReferralFired: false,
			}),
		).toEqual({
			fireReturnVisit: false,
			firePhReferralVisit: true,
			markSessionCounted: true,
			markSeen: true,
			markPhReferralFired: true,
		});
	});

	it("does not treat a reload of a first-ever visit as a return visit", () => {
		// Regression: the session marker was only written when return_visit
		// fired, so the first reload of a brand-new visitor counted as a return.
		const plan = planLaunchEvents({
			...base,
			source: "direct",
			seenBefore: true,
			sessionCounted: true,
			phReferralFired: true,
		});
		expect(plan.fireReturnVisit).toBe(false);
		// Already recorded; do not rewrite.
		expect(plan.markSessionCounted).toBe(false);
		expect(plan.markSeen).toBe(false);
	});

	it("fires return_visit for a seen browser in a new tab session", () => {
		const plan = planLaunchEvents({
			...base,
			source: "direct",
			seenBefore: true,
			sessionCounted: false,
			phReferralFired: true,
		});
		expect(plan.fireReturnVisit).toBe(true);
		expect(plan.markSessionCounted).toBe(true);
	});

	it("fires ph_referral_visit once per tab session only", () => {
		const plan = planLaunchEvents({
			...base,
			source: "product_hunt",
			seenBefore: true,
			sessionCounted: true,
			phReferralFired: true,
		});
		expect(plan.firePhReferralVisit).toBe(false);
		expect(plan.markPhReferralFired).toBe(false);
	});

	it("never fires ph_referral_visit for other sources", () => {
		expect(
			planLaunchEvents({
				...base,
				source: "x",
				seenBefore: false,
				sessionCounted: false,
				phReferralFired: false,
			}).firePhReferralVisit,
		).toBe(false);
	});

	it("degrades to per-load ph_referral_visit when sessionStorage is unavailable", () => {
		const plan = planLaunchEvents({
			...base,
			sessionStorageAvailable: false,
			source: "product_hunt",
			seenBefore: false,
			sessionCounted: false,
			phReferralFired: false,
		});
		expect(plan.firePhReferralVisit).toBe(true);
		expect(plan.markSessionCounted).toBe(false);
		expect(plan.markPhReferralFired).toBe(false);
	});

	it("suppresses return_visit when sessionStorage is unavailable", () => {
		// Regression: with localStorage alive but sessionStorage blocked, the
		// session marker can never be written, so every reload of a seen
		// browser looked like a new tab session and fired a false return_visit.
		// Without a dedupe store the signal is suppressed, not guessed at.
		const plan = planLaunchEvents({
			...base,
			sessionStorageAvailable: false,
			source: "direct",
			seenBefore: true,
			sessionCounted: false,
			phReferralFired: true,
		});
		expect(plan.fireReturnVisit).toBe(false);
		expect(plan.markSessionCounted).toBe(false);
	});

	it("skips the return signal entirely when localStorage is unavailable", () => {
		// Without persistence there is no honest "seen before"; never guess.
		const plan = planLaunchEvents({
			...base,
			localStorageAvailable: false,
			source: "product_hunt",
			seenBefore: false,
			sessionCounted: false,
			phReferralFired: false,
		});
		expect(plan.fireReturnVisit).toBe(false);
		expect(plan.markSeen).toBe(false);
	});
});

/** In-memory Storage fake recording the write order alongside event calls. */
function fakeStorage(log?: string[]): VisitStorage {
	const local = new Map<string, string>();
	const session = new Map<string, string>();
	const record = (store: Map<string, string>, key: string) => {
		store.set(key, "1");
		log?.push(`set:${key}`);
	};
	return {
		local: {
			getItem: (k: string) => local.get(k) ?? null,
			setItem: (k: string, _v: string) => record(local, k),
		},
		session: {
			getItem: (k: string) => session.get(k) ?? null,
			setItem: (k: string, _v: string) => record(session, k),
		},
	};
}

describe("readVisitState", () => {
	it("reads markers and reports store availability", () => {
		const storage = fakeStorage();
		storage.local.setItem(SEEN_KEY, "1");
		storage.session.setItem(SESSION_COUNTED_KEY, "1");
		expect(readVisitState("direct", storage)).toEqual({
			source: "direct",
			seenBefore: true,
			sessionCounted: true,
			phReferralFired: false,
			localStorageAvailable: true,
			sessionStorageAvailable: true,
		});
	});

	it("treats a throwing store as unavailable, not as a crash", () => {
		const throwing = {
			local: {
				getItem: () => {
					throw new Error("blocked");
				},
				setItem: () => {
					throw new Error("blocked");
				},
			},
			session: {
				getItem: () => null,
				setItem: () => {},
			},
		} as unknown as VisitStorage;
		const state = readVisitState("product_hunt", throwing);
		expect(state.localStorageAvailable).toBe(false);
		expect(state.seenBefore).toBe(false);
		expect(state.sessionStorageAvailable).toBe(true);
	});
});

describe("applyVisitPlan", () => {
	it("fires events before writing their markers", () => {
		const order: string[] = [];
		const storage = fakeStorage(order);
		const track = (event: string) => order.push(`event:${event}`);
		applyVisitPlan(
			planLaunchEvents({
				...base,
				source: "product_hunt",
				seenBefore: true,
				sessionCounted: false,
				phReferralFired: false,
			}),
			track,
			storage,
		);
		// A marker written before its event would suppress the event if the
		// event then failed to send; the reverse risks only a duplicate.
		expect(order).toEqual([
			"event:return_visit",
			"event:ph_referral_visit",
			"set:ao.launch.return_fired",
			"set:ao.launch.ph_referral",
			// seen already recorded on the first visit, so not rewritten
		]);
	});

	it("writes only the markers the plan asks for", () => {
		const storage = fakeStorage();
		applyVisitPlan(
			planLaunchEvents({
				...base,
				source: "direct",
				seenBefore: true,
				sessionCounted: true,
				phReferralFired: true,
			}),
			vi.fn(),
			storage,
		);
		expect(storage.local.getItem(SEEN_KEY)).toBeNull();
		expect(storage.session.getItem(SESSION_COUNTED_KEY)).toBeNull();
		expect(storage.session.getItem(PH_REFERRAL_KEY)).toBeNull();
	});

	it("survives a throwing store (quota/blocked) without throwing", () => {
		const throwing = {
			local: {
				getItem: () => null,
				setItem: () => {
					throw new Error("quota");
				},
			},
			session: {
				getItem: () => null,
				setItem: () => {
					throw new Error("quota");
				},
			},
		} as unknown as VisitStorage;
		expect(() =>
			applyVisitPlan(
				planLaunchEvents({
					...base,
					source: "product_hunt",
					seenBefore: false,
					sessionCounted: false,
					phReferralFired: false,
				}),
				vi.fn(),
				throwing,
			),
		).not.toThrow();
	});
});

describe("consentAccepted", () => {
	it("is true only for the accepted value under the consent key", () => {
		const make = (value: string | null) => ({
			getItem: (key: string) => (key === "ao-analytics-consent" ? value : null),
		});
		expect(consentAccepted(make("accepted"))).toBe(true);
		expect(consentAccepted(make("declined"))).toBe(false);
		expect(consentAccepted(make(null))).toBe(false);
	});
});

describe("whenConsented", () => {
	it("fires immediately when consent is already accepted", () => {
		const fire = vi.fn();
		const stop = whenConsented(fire, () => true);
		expect(fire).toHaveBeenCalledTimes(1);
		stop();
	});

	it("waits for acceptance instead of dropping the events", () => {
		vi.useFakeTimers();
		try {
			const fire = vi.fn();
			let accepted = false;
			const stop = whenConsented(fire, () => accepted);
			// A first-time visitor accepts the banner a minute into the visit.
			vi.advanceTimersByTime(60_000);
			expect(fire).not.toHaveBeenCalled();
			accepted = true;
			vi.advanceTimersByTime(1_000);
			expect(fire).toHaveBeenCalledTimes(1);
			// And exactly once: the wait stops after firing.
			vi.advanceTimersByTime(10_000);
			expect(fire).toHaveBeenCalledTimes(1);
			stop();
		} finally {
			vi.useRealTimers();
		}
	});

	it("stops polling after the bound, and cleanup cancels the wait", () => {
		vi.useFakeTimers();
		try {
			const bounded = vi.fn();
			whenConsented(bounded, () => false);
			vi.advanceTimersByTime(121_000);
			expect(bounded).not.toHaveBeenCalled();

			const cancelled = vi.fn();
			const stop = whenConsented(cancelled, () => false);
			stop();
			vi.advanceTimersByTime(121_000);
			expect(cancelled).not.toHaveBeenCalled();
		} finally {
			vi.useRealTimers();
		}
	});
});
