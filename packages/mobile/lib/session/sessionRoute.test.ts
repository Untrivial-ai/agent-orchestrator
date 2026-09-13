import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@react-native-async-storage/async-storage", () => ({ default: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() } }));
vi.mock("expo-secure-store", () => ({ getItemAsync: vi.fn(), setItemAsync: vi.fn(), deleteItemAsync: vi.fn() }));

import { getSessions, type DashboardSession, type OrchestratorLink } from "../api";
import type { ServerConfig } from "../config";
import { lookUpSession } from "./sessionLookup";
import {
	currentSessionLookup,
	sessionLookupDue,
	sessionLookupKey,
	sessionLookupSettled,
	sessionRouteView,
	type SessionLookup,
} from "./sessionRoute";

const worker = (over: Partial<DashboardSession> = {}): DashboardSession =>
	({ id: "proj-1", projectId: "proj", mode: "chat", isTerminated: false, ...over }) as DashboardSession;

const orchestrator = (over: Partial<OrchestratorLink> = {}): OrchestratorLink => ({
	id: "proj-orchestrator",
	projectId: "proj",
	projectName: "proj",
	mode: "chat",
	...over,
});

const found = (session: DashboardSession): SessionLookup => ({ state: "found", session });
const failed = (status: number | undefined): SessionLookup => ({ state: "failed", status });
const pending: SessionLookup = { state: "pending" };

type ViewArgs = Parameters<typeof sessionRouteView>[0];
const view = (over: Partial<ViewArgs> = {}) =>
	sessionRouteView({ listed: undefined, configured: true, connection: "open", loading: false, lookup: pending, ...over });

type DueArgs = Parameters<typeof sessionLookupDue>[0];
const due = (over: Partial<DueArgs> = {}) =>
	sessionLookupDue({ listed: false, configured: true, connection: "open", appActive: true, machineChanged: false, lookup: pending, ...over });

describe("sessionRouteView", () => {
	it("waits for the saved config before judging anything", () => {
		expect(view({ configured: null, listed: orchestrator(), lookup: failed(404) })).toEqual({ kind: "loading" });
	});

	// The store keeps the last machine's lists after Settings → forget, so a
	// cached id must not open a session screen with nothing paired.
	it("shows the unpaired state before consulting the lists", () => {
		expect(view({ configured: false, listed: orchestrator() })).toEqual({ kind: "unpaired" });
	});

	it("opens a listed session whatever the lookup or the link says", () => {
		const listed = orchestrator();
		for (const lookup of [pending, failed(404), failed(undefined), found(worker({ isTerminated: true }))]) {
			for (const connection of ["open", "connecting", "closed"] as const) {
				expect(view({ listed, lookup, connection })).toEqual({ kind: "screen", session: listed });
			}
		}
	});

	it("reports an unlisted session that has ended as ended, not as not found", () => {
		expect(view({ lookup: found(worker({ isTerminated: true })) })).toEqual({ kind: "ended" });
	});

	it("opens an unlisted session that is still live", () => {
		const live = worker({ id: "proj-9" });
		expect(view({ lookup: found(live) })).toEqual({ kind: "screen", session: live });
	});

	it.each([404, 410])("reports not found only on a %s", (status) => {
		expect(view({ lookup: failed(status) })).toEqual({ kind: "missing" });
	});

	// An answer the daemon gave is not withdrawn because the link dropped after.
	it("keeps a settled answer while the board is disconnected", () => {
		expect(view({ connection: "closed", lookup: found(worker({ isTerminated: true })) })).toEqual({ kind: "ended" });
		expect(view({ connection: "closed", lookup: failed(404) })).toEqual({ kind: "missing" });
	});

	// The bug this replaces: "Session not found" was the fallback for anything the
	// cache did not hold, including a request that never reached the daemon.
	it.each([undefined, 401, 403, 429, 500])("leaves a %s failure to the board while it is disconnected", (status) => {
		expect(view({ connection: "closed", lookup: failed(status) })).toEqual({ kind: "offline" });
	});

	it("reports the board disconnected before anything was asked", () => {
		expect(view({ connection: "closed" })).toEqual({ kind: "offline" });
	});

	it("waits while the board is still reaching the machine", () => {
		expect(view({ connection: "connecting" })).toEqual({ kind: "loading" });
		// The store starts "closed" and only flips to "connecting" in its own effect,
		// which runs after this route's; `loading` covers that first render.
		expect(view({ connection: "closed", loading: true })).toEqual({ kind: "loading" });
	});

	it("waits while the lookup is in flight", () => {
		expect(view({ lookup: pending })).toEqual({ kind: "loading" });
	});

	it.each([undefined, 500, 503])("offers a retry when the board is connected but a %s lookup failed", (status) => {
		expect(view({ lookup: failed(status) })).toEqual({ kind: "failed" });
	});

	// "open" can be a poll interval behind a regenerated password. A Retry on a 401
	// would spend one failed auth per tap, and five lock the phone out for a minute,
	// pairing scan included (`lan_listener.go`, `newLockout(5, time.Minute, ...)`).
	// Under a 429 a retry spends nothing but cannot succeed either; 403 travels with
	// them because `classifyConnectionFailure` groups it as "auth".
	it.each([401, 403, 429])("hands a %s lookup to the board instead of offering a retry", (status) => {
		expect(view({ lookup: failed(status) })).toEqual({ kind: "offline" });
	});
});

describe("sessionLookupDue", () => {
	it("asks about an unlisted id once the board is connected", () => {
		expect(due()).toBe(true);
	});

	// Asking on "closed" too cost an extra 401 per app switch under a rotated password.
	it.each(["closed", "connecting"] as const)("never asks while the board is %s, even to retry a failure", (connection) => {
		for (const lookup of [pending, failed(undefined), failed(401), failed(429), failed(500)]) {
			expect(due({ connection, lookup })).toBe(false);
		}
	});

	// A tick in flight when the app was backgrounded still lands and sets "open".
	it("never asks from the background", () => {
		for (const lookup of [pending, failed(undefined), failed(500)]) {
			expect(due({ appActive: false, lookup })).toBe(false);
		}
	});

	it("asks again for a failure once the board has reconnected", () => {
		expect(due({ lookup: failed(undefined) })).toBe(true);
		expect(due({ lookup: failed(500) })).toBe(true);
	});

	// On a re-pair the route's effect runs before the store restarts its poll, so
	// "open" in that render is the previous machine's.
	it("never asks in the render where the machine changed", () => {
		expect(due({ machineChanged: true })).toBe(false);
	});

	it("never asks for a listed id, or without a paired machine", () => {
		expect(due({ listed: true })).toBe(false);
		expect(due({ configured: false })).toBe(false);
		expect(due({ configured: null })).toBe(false);
	});

	it("never asks again once the daemon has answered", () => {
		expect(due({ lookup: found(worker({ isTerminated: true })) })).toBe(false);
		expect(due({ lookup: found(worker()) })).toBe(false);
		expect(due({ lookup: failed(404) })).toBe(false);
		expect(due({ lookup: failed(410) })).toBe(false);
	});
});

describe("sessionLookupSettled", () => {
	it("keeps an answer the daemon gave", () => {
		expect(sessionLookupSettled(found(worker({ isTerminated: true })))).toBe(true);
		expect(sessionLookupSettled(found(worker()))).toBe(true);
		expect(sessionLookupSettled(failed(404))).toBe(true);
		expect(sessionLookupSettled(failed(410))).toBe(true);
	});

	it.each([undefined, 401, 429, 500])("leaves a %s failure open", (status) => {
		expect(sessionLookupSettled(failed(status))).toBe(false);
	});

	it("leaves a lookup that has not answered open", () => {
		expect(sessionLookupSettled(pending)).toBe(false);
	});
});

describe("currentSessionLookup", () => {
	const answer = { key: sessionLookupKey("host.h_a", "proj-1"), lookup: failed(404) };

	it("returns the answer for the machine and id it was asked about", () => {
		expect(currentSessionLookup(answer, sessionLookupKey("host.h_a", "proj-1"))).toBe(answer.lookup);
	});

	it("ignores an answer about the id the screen showed before", () => {
		expect(currentSessionLookup(answer, sessionLookupKey("host.h_a", "proj-2"))).toEqual(pending);
	});

	it("ignores an answer from the machine the phone was paired with before", () => {
		expect(currentSessionLookup(answer, sessionLookupKey("host.h_b", "proj-1"))).toEqual(pending);
	});

	it("starts pending when nothing was asked yet", () => {
		expect(currentSessionLookup(null, sessionLookupKey("host.h_a", "proj-1"))).toEqual(pending);
	});
});

// Orchestrator records copied from a daemon built from origin/main (dev data,
// 2026-09-13): project `scratch` has two, the first terminated. The /sessions
// mock is trimmed to those two; the 404 body is the daemon's, requestId renamed.
const scratch1 = {
	id: "scratch-1", projectId: "scratch", kind: "orchestrator", harness: "claude-code", reviewerConfig: {},
	autoReviewEnabled: false, mode: "chat", activity: { state: "exited", lastActivityAt: "2026-08-26T14:55:38.347578Z" },
	isTerminated: true, terminateOnPrMerge: false, autoInjectReview: true, autoInjectCI: true,
	createdAt: "2026-08-25T10:00:51.945665Z", updatedAt: "2026-08-26T14:55:38.347578Z", isPinned: false,
	chatProviderPreserved: false, status: "terminated", kanbanColumn: "archive", displayStatus: "Terminated", prs: [],
};
const scratch2 = {
	id: "scratch-2", projectId: "scratch", kind: "orchestrator", harness: "claude-code", reviewerConfig: {},
	autoReviewEnabled: false, mode: "chat", activity: { state: "exited", lastActivityAt: "2026-09-01T17:25:46.152243Z" },
	isTerminated: false, terminateOnPrMerge: false, autoInjectReview: true, autoInjectCI: true,
	createdAt: "2026-09-01T15:27:57.219478Z", updatedAt: "2026-09-13T16:11:05.51102Z", isPinned: false,
	chatProviderPreserved: false, status: "exited", kanbanColumn: "building", displayStatus: "Exited", prs: [],
};
const notFoundBody = { error: "not_found", code: "SESSION_NOT_FOUND", message: "Unknown session", requestId: "req-6" };

const cfg: ServerConfig = { host: "ao.test", httpPort: "3011", muxPort: "3011", secure: false, password: "secret12" };

function response(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

describe("lookUpSession, the request the route sends", () => {
	beforeEach(() => vi.stubGlobal("fetch", vi.fn()));
	afterEach(() => vi.unstubAllGlobals());

	it("finds the orchestrator the board's lists dropped, and reports it ended", async () => {
		vi.mocked(fetch)
			.mockResolvedValueOnce(response({ sessions: [scratch1, scratch2] }))
			.mockResolvedValueOnce(response({ sessions: [scratch1, scratch2] }))
			.mockResolvedValueOnce(response({ projects: [{ id: "scratch", name: "Scratch", kind: "scratch", sessionPrefix: "scratch" }] }))
			.mockResolvedValueOnce(response({ session: scratch1 }));

		const board = await getSessions(cfg);
		const listed = board.sessions.find((s) => s.id === "scratch-1") ?? board.orchestrators.find((s) => s.id === "scratch-1");
		// The precondition #4844 is about: one orchestrator per project, so the
		// terminated first one is not on the phone at all.
		expect(board.orchestrators.map((o) => o.id)).toEqual(["scratch-2"]);
		expect(listed).toBeUndefined();

		const lookup = await lookUpSession(cfg, "scratch-1");
		expect(vi.mocked(fetch).mock.calls[3]?.[0]).toBe("http://ao.test:3011/api/v1/sessions/scratch-1");
		expect(view({ listed, lookup })).toEqual({ kind: "ended" });
	});

	it("carries the daemon's 404 through as not found", async () => {
		vi.mocked(fetch).mockResolvedValueOnce(response(notFoundBody, 404));
		const lookup = await lookUpSession(cfg, "no-such-session");
		expect(lookup).toEqual(failed(404));
		expect(view({ lookup })).toEqual({ kind: "missing" });
	});

	it("resolves a request that never reached the daemon to a failure without a status", async () => {
		vi.mocked(fetch).mockRejectedValueOnce(new TypeError("Network request failed"));
		const lookup = await lookUpSession(cfg, "scratch-1");
		expect(lookup).toEqual(failed(undefined));
		expect(view({ lookup })).toEqual({ kind: "failed" });
	});
});
