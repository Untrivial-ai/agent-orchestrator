import { beforeEach, describe, expect, it } from "vitest";
import {
	clearElicitationDraft,
	elicitationDraftKey,
	pruneExpiredElicitationDrafts,
	pruneExpiredElicitationDraftsOnce,
	readElicitationDraft,
	reconcileElicitationDraftsForConversation,
	resetElicitationDraftPruning,
	writeElicitationDraft,
} from "./elicitation-drafts";

describe("elicitation drafts", () => {
	beforeEach(() => {
		window.localStorage.clear();
		resetElicitationDraftPruning();
	});

	it("round-trips an in-progress answer", () => {
		writeElicitationDraft("conversation-1", "request-1", {
			values: { question_0: "Native", question_0_custom: "Hybrid", picks: ["a", "b"] },
			activeQuestion: 1,
		});

		expect(readElicitationDraft("conversation-1", "request-1")).toEqual({
			values: { question_0: "Native", question_0_custom: "Hybrid", picks: ["a", "b"] },
			activeQuestion: 1,
		});
	});

	it("scopes drafts to one conversation and request", () => {
		// The daemon itself identifies a pending input by (conversation_id,
		// request_id), and a reviewer-chat overlay can report the same session id
		// as its underlying worker chat while reading a different conversation —
		// so conversation, not session, has to be the scope.
		writeElicitationDraft("conversation-1", "request-1", { values: { a: "one" }, activeQuestion: 0 });

		expect(readElicitationDraft("conversation-2", "request-1")).toBeUndefined();
		expect(readElicitationDraft("conversation-1", "request-2")).toBeUndefined();
	});

	it("clears a resolved question", () => {
		writeElicitationDraft("conversation-1", "request-1", { values: { a: "one" }, activeQuestion: 0 });
		clearElicitationDraft("conversation-1", "request-1");

		expect(readElicitationDraft("conversation-1", "request-1")).toBeUndefined();
	});

	it("ignores unreadable or foreign payloads", () => {
		window.localStorage.setItem(elicitationDraftKey("conversation-1", "request-1"), "not json");
		expect(readElicitationDraft("conversation-1", "request-1")).toBeUndefined();

		window.localStorage.setItem(
			elicitationDraftKey("conversation-1", "request-2"),
			JSON.stringify({ schemaVersion: 99, values: { a: "one" }, activeQuestion: 0, updatedAt: Date.now() }),
		);
		expect(readElicitationDraft("conversation-1", "request-2")).toBeUndefined();
	});

	it("drops values the schema could never hold", () => {
		window.localStorage.setItem(
			elicitationDraftKey("conversation-1", "request-1"),
			JSON.stringify({
				schemaVersion: 1,
				values: { good: "yes", bad: { nested: true } },
				activeQuestion: 0,
				updatedAt: Date.now(),
			}),
		);

		expect(readElicitationDraft("conversation-1", "request-1")?.values).toEqual({ good: "yes" });
	});

	it("reports success or failure from a write, the same way the other chat drafts do", () => {
		expect(writeElicitationDraft("conversation-1", "request-1", { values: { a: "one" }, activeQuestion: 0 }).ok).toBe(true);

		const throwingStorage = {
			getItem: () => null,
			setItem: () => {
				throw new DOMException("quota exceeded", "QuotaExceededError");
			},
			removeItem: () => undefined,
		};
		expect(
			writeElicitationDraft("conversation-1", "request-1", { values: { a: "one" }, activeQuestion: 0 }, throwingStorage).ok,
		).toBe(false);
	});

	it("removes stale entries from storage when swept, independent of any later read", () => {
		const eightDays = 8 * 24 * 60 * 60 * 1000;
		window.localStorage.setItem(
			elicitationDraftKey("conversation-1", "stale"),
			JSON.stringify({ schemaVersion: 1, values: { a: "one" }, activeQuestion: 0, updatedAt: Date.now() - eightDays }),
		);
		writeElicitationDraft("conversation-1", "fresh", { values: { a: "two" }, activeQuestion: 0 });

		pruneExpiredElicitationDrafts(window.localStorage);

		// Asserted directly on storage, not through readElicitationDraft: a sweep
		// that walks the keys but never calls removeItem would otherwise still
		// pass here, since the read path independently deletes anything stale.
		expect(window.localStorage.getItem(elicitationDraftKey("conversation-1", "stale"))).toBeNull();
		expect(window.localStorage.getItem(elicitationDraftKey("conversation-1", "fresh"))).not.toBeNull();
	});

	it("removes an entry with an unsupported schema version when swept, not only on read", () => {
		// A future schemaVersion bump makes every current entry "unsupported" to a
		// newer build. The key carries no version, so the sweep — not a `startsWith`
		// filter on the key — is what eventually reclaims entries an old build left
		// behind; this stands in for that case using a version this build already
		// rejects.
		window.localStorage.setItem(
			elicitationDraftKey("conversation-1", "old-version"),
			JSON.stringify({ schemaVersion: 99, values: { a: "one" }, activeQuestion: 0, updatedAt: Date.now() }),
		);

		pruneExpiredElicitationDrafts(window.localStorage);

		expect(window.localStorage.getItem(elicitationDraftKey("conversation-1", "old-version"))).toBeNull();
	});

	it("does not keep writing while the human types", () => {
		let reads = 0;
		const counted = {
			getItem: (key: string) => (reads++, window.localStorage.getItem(key)),
			setItem: (key: string, value: string) => window.localStorage.setItem(key, value),
			removeItem: (key: string) => window.localStorage.removeItem(key),
			// Forwarded live so a write that started walking the store — through
			// pruneExpiredElicitationDrafts or reconcileElicitationDraftsForConversation,
			// say — would actually be caught here: without these, both bail out on
			// their own `typeof storage.key`/`length` guard before ever calling
			// getItem, and this test would stay green regardless.
			key: (index: number) => window.localStorage.key(index),
			get length() {
				return window.localStorage.length;
			},
		};

		writeElicitationDraft("conversation-1", "request-1", { values: { a: "o" }, activeQuestion: 0 }, counted);
		writeElicitationDraft("conversation-1", "request-1", { values: { a: "on" }, activeQuestion: 0 }, counted);
		writeElicitationDraft("conversation-1", "request-1", { values: { a: "one" }, activeQuestion: 0 }, counted);

		// Writing must not walk the store; only the explicit sweep reads every key.
		expect(reads).toBe(0);
	});

	it("re-sweeps only after the sweep interval elapses, tracked in storage rather than in memory", () => {
		// Tracked via a timestamp written to storage itself, not an in-memory flag:
		// a renderer left open for days needs a later mount to sweep again, which a
		// once-per-process guard would never do. Proven functionally — by whether a
		// stale entry gets removed — rather than by counting internal calls, since
		// the sweep's own "last swept at" marker is itself one more key in the same
		// store and would throw off a raw key()-call count.
		const hour = 60 * 60 * 1000;
		const eightDays = 8 * 24 * 60 * 60 * 1000;
		let now = Date.now();
		const realNow = Date.now;
		Date.now = () => now;

		try {
			pruneExpiredElicitationDraftsOnce(window.localStorage);

			window.localStorage.setItem(
				elicitationDraftKey("conversation-1", "stale"),
				JSON.stringify({ schemaVersion: 1, values: { a: "one" }, activeQuestion: 0, updatedAt: now - eightDays }),
			);

			// Still inside the interval from the first sweep above: this one is a no-op.
			pruneExpiredElicitationDraftsOnce(window.localStorage);
			expect(window.localStorage.getItem(elicitationDraftKey("conversation-1", "stale"))).not.toBeNull();

			now += hour + 1;
			pruneExpiredElicitationDraftsOnce(window.localStorage);
			expect(window.localStorage.getItem(elicitationDraftKey("conversation-1", "stale"))).toBeNull();
		} finally {
			Date.now = realNow;
		}
	});

	it("refuses to restore a stale draft even without any prior sweep", () => {
		// Nothing sweeps in this test — proves the read path itself enforces the
		// seven-day expiry, rather than depending on a sweep to have already run.
		const eightDays = 8 * 24 * 60 * 60 * 1000;
		window.localStorage.setItem(
			elicitationDraftKey("conversation-1", "stale"),
			JSON.stringify({ schemaVersion: 1, values: { a: "one" }, activeQuestion: 0, updatedAt: Date.now() - eightDays }),
		);

		expect(readElicitationDraft("conversation-1", "stale")).toBeUndefined();
		// The stale entry is also removed as a side effect, so a later sweep has nothing to do.
		expect(window.localStorage.getItem(elicitationDraftKey("conversation-1", "stale"))).toBeNull();
	});

	it("refuses a draft with no, non-numeric, or absurdly-future updatedAt", () => {
		const twoDays = 2 * 24 * 60 * 60 * 1000;
		window.localStorage.setItem(
			elicitationDraftKey("conversation-1", "missing-timestamp"),
			JSON.stringify({ schemaVersion: 1, values: { a: "one" }, activeQuestion: 0 }),
		);
		window.localStorage.setItem(
			elicitationDraftKey("conversation-1", "bad-timestamp"),
			JSON.stringify({ schemaVersion: 1, values: { a: "one" }, activeQuestion: 0, updatedAt: "yesterday" }),
		);
		window.localStorage.setItem(
			elicitationDraftKey("conversation-1", "absurd-future-timestamp"),
			JSON.stringify({ schemaVersion: 1, values: { a: "one" }, activeQuestion: 0, updatedAt: Date.now() + twoDays }),
		);

		expect(readElicitationDraft("conversation-1", "missing-timestamp")).toBeUndefined();
		expect(readElicitationDraft("conversation-1", "bad-timestamp")).toBeUndefined();
		expect(readElicitationDraft("conversation-1", "absurd-future-timestamp")).toBeUndefined();
	});

	it("tolerates a small future skew, for when the local clock is a little ahead of the write", () => {
		// A clock that steps backward after the write (a manual fix, an NTP step)
		// would otherwise make a just-written draft look corrupt and destroy the
		// exact thing this module exists to protect.
		const oneMinute = 60 * 1000;
		window.localStorage.setItem(
			elicitationDraftKey("conversation-1", "slightly-ahead"),
			JSON.stringify({ schemaVersion: 1, values: { a: "one" }, activeQuestion: 0, updatedAt: Date.now() + oneMinute }),
		);

		expect(readElicitationDraft("conversation-1", "slightly-ahead")?.values).toEqual({ a: "one" });
	});

	it("keeps and re-stamps a draft whose clock was hours ahead, rather than deleting it", () => {
		// A dual-boot RTC offset is typically a whole timezone, hours rather than
		// minutes. Written while the clock read an hour ahead, then the clock
		// gets corrected: the draft is still the real, just-typed answer, not a
		// corrupt value, so it survives — but is re-stamped so it isn't still
		// relying on the clock that was wrong when it was written.
		const oneHour = 60 * 60 * 1000;
		const key = elicitationDraftKey("conversation-1", "clock-was-ahead");
		window.localStorage.setItem(
			key,
			JSON.stringify({ schemaVersion: 1, values: { a: "one" }, activeQuestion: 0, updatedAt: Date.now() + oneHour }),
		);

		expect(readElicitationDraft("conversation-1", "clock-was-ahead")?.values).toEqual({ a: "one" });

		const restamped = JSON.parse(window.localStorage.getItem(key) ?? "{}");
		expect(restamped.updatedAt).toBeLessThanOrEqual(Date.now());
		expect(restamped.updatedAt).toBeGreaterThan(Date.now() - 1000);
	});

	it("does not let a corrupt or future sweep marker wedge the sweep shut", () => {
		// A marker written while the clock was far ahead (manual change, bad RTC,
		// an NTP step) makes `now - last` negative for as long as real time is
		// behind it — for a corrupt or absurd value, that could be forever.
		// Drafts in a conversation that is never reopened have no other path to
		// expiry, so a wedged marker would keep them past their advertised seven
		// days indefinitely.
		window.localStorage.setItem("ao.elicitation-draft-sweep:last", String(Date.now() + 30 * 24 * 60 * 60 * 1000));
		const eightDays = 8 * 24 * 60 * 60 * 1000;
		window.localStorage.setItem(
			elicitationDraftKey("conversation-1", "stale"),
			JSON.stringify({ schemaVersion: 1, values: { a: "one" }, activeQuestion: 0, updatedAt: Date.now() - eightDays }),
		);

		pruneExpiredElicitationDraftsOnce(window.localStorage);

		expect(window.localStorage.getItem(elicitationDraftKey("conversation-1", "stale"))).toBeNull();
	});

	it("treats even a slightly future sweep marker as no marker", () => {
		const twoHours = 2 * 60 * 60 * 1000;
		window.localStorage.setItem("ao.elicitation-draft-sweep:last", String(Date.now() + twoHours));
		const eightDays = 8 * 24 * 60 * 60 * 1000;
		window.localStorage.setItem(
			elicitationDraftKey("conversation-1", "stale"),
			JSON.stringify({ schemaVersion: 1, values: { a: "one" }, activeQuestion: 0, updatedAt: Date.now() - eightDays }),
		);

		pruneExpiredElicitationDraftsOnce(window.localStorage);

		expect(window.localStorage.getItem(elicitationDraftKey("conversation-1", "stale"))).toBeNull();
	});

	describe("reconcileElicitationDraftsForConversation", () => {
		it("keeps a draft for every request that is still pending, not only the one shown", () => {
			// The daemon can have more than one user_input open on a conversation
			// at once; the UI only ever displays the newest, but every open
			// question's draft has to survive reconciliation.
			writeElicitationDraft("conversation-1", "q1", { values: { a: "one" }, activeQuestion: 0 });
			writeElicitationDraft("conversation-1", "q2", { values: { a: "two" }, activeQuestion: 0 });
			writeElicitationDraft("conversation-1", "answered", { values: { a: "gone" }, activeQuestion: 0 });

			reconcileElicitationDraftsForConversation("conversation-1", ["q1", "q2"], ["answered"], true);

			expect(readElicitationDraft("conversation-1", "q1")?.values).toEqual({ a: "one" });
			expect(readElicitationDraft("conversation-1", "q2")?.values).toEqual({ a: "two" });
			expect(readElicitationDraft("conversation-1", "answered")).toBeUndefined();
		});

		it("removes every draft for a conversation when nothing is pending there anymore", () => {
			writeElicitationDraft("conversation-1", "answered", { values: { a: "gone" }, activeQuestion: 0 });

			reconcileElicitationDraftsForConversation("conversation-1", [], [], true);

			expect(readElicitationDraft("conversation-1", "answered")).toBeUndefined();
		});

		it("leaves another conversation's drafts untouched", () => {
			// A reviewer-chat overlay and its worker chat can be open on the same
			// session id at once. Reconciling one conversation must never reach
			// into another's keys, even one sharing the storage instance.
			writeElicitationDraft("conversation-1", "q1", { values: { a: "one" }, activeQuestion: 0 });
			writeElicitationDraft("other-conversation", "q1", { values: { a: "unrelated" }, activeQuestion: 0 });

			reconcileElicitationDraftsForConversation("conversation-1", [], [], true);

			expect(readElicitationDraft("other-conversation", "q1")?.values).toEqual({ a: "unrelated" });
		});

		it("on a partial page, deletes only requests actually seen as resolved, leaving unseen ones alone", () => {
			// hasMoreBefore means older items than what's loaded might exist. A
			// request id absent from the loaded page entirely could be a still-open
			// question old enough to sit outside the window — not safe to guess
			// about — but one seen in the page with a non-pending status is known
			// for certain.
			writeElicitationDraft("conversation-1", "seen-resolved", { values: { a: "gone" }, activeQuestion: 0 });
			writeElicitationDraft("conversation-1", "not-in-loaded-page", { values: { a: "still open, presumably" }, activeQuestion: 0 });

			reconcileElicitationDraftsForConversation("conversation-1", [], ["seen-resolved"], false);

			expect(readElicitationDraft("conversation-1", "seen-resolved")).toBeUndefined();
			expect(readElicitationDraft("conversation-1", "not-in-loaded-page")?.values).toEqual({
				a: "still open, presumably",
			});
		});

		it("on a partial page, still keeps a request that is pending", () => {
			writeElicitationDraft("conversation-1", "q1", { values: { a: "one" }, activeQuestion: 0 });

			reconcileElicitationDraftsForConversation("conversation-1", ["q1"], [], false);

			expect(readElicitationDraft("conversation-1", "q1")?.values).toEqual({ a: "one" });
		});
	});
});
