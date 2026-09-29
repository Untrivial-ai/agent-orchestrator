import { expect, test } from "@playwright/test";
import { installFakeAgent, type FakeWorker } from "./support/fake-bridge";

const cacheState = (page: import("@playwright/test").Page) =>
	page.evaluate(() => {
		const client = (window as unknown as { __qc?: { getQueryData: (key: readonly unknown[]) => unknown } }).__qc;
		const workspaces = client?.getQueryData(["workspaces"]) as
			| Array<{ id: string; sessions: Array<{ id: string; title?: string }> }>
			| undefined;
		const workspace = workspaces?.find((item) => item.id === "fake-proj");
		return {
			workspaceCount: workspaces?.length ?? 0,
			sessionCount: workspace?.sessions.length ?? 0,
			sessionIds: workspace?.sessions.map((session) => session.id) ?? [],
		};
	});

const sidebarRowState = (page: import("@playwright/test").Page, title: string) =>
	page.evaluate((sessionTitle) => {
		const rows = [...document.querySelectorAll('[data-testid="session-list-fake-proj"] [data-session-row]')];
		const row = rows.find((candidate) => candidate.textContent?.includes(sessionTitle));
		if (!row) return { mounted: false, visible: false, opacity: null, display: null, rect: null };
		const style = getComputedStyle(row);
		const rect = row.getBoundingClientRect();
		return {
			mounted: true,
			visible: rect.width > 0 && rect.height > 0 && style.display !== "none" && style.visibility !== "hidden",
			opacity: style.opacity,
			display: style.display,
			rect: { x: rect.x, y: rect.y, width: rect.width, height: rect.height },
		};
	}, title);

async function openLargeExpandedWorkspace(page: import("@playwright/test").Page) {
	const workers: FakeWorker[] = Array.from({ length: 491 }, (_, index) => {
		const number = index + 1;
		return {
			id: `seed-${String(number).padStart(3, "0")}`,
			title: number === 443 ? "Index 443 target worker" : `Seed worker ${number}`,
			createdAt: new Date(Date.UTC(2024, 0, 1) + number * 1_000).toISOString(),
		};
	});
	await installFakeAgent(page, { workers, expandProject: true });
	await page.goto("/#/");
	const projectRow = page.locator('[data-sidebar="menu-button"]').filter({ hasText: "fake-proj" }).first();
	await expect(projectRow).toBeVisible();
	await projectRow.click();
	await expect.poll(async () => (await cacheState(page)).sessionCount, { timeout: 20_000 }).toBe(492);
	await expect(page.locator('[data-project-folder]')).toHaveAttribute("aria-expanded", "true");
}

const queryMetrics = (page: import("@playwright/test").Page) =>
	page.evaluate(() => {
		const client = (window as unknown as {
			__qc?: {
				getQueryState: (key: readonly unknown[]) => { status?: string; fetchStatus?: string; fetchFailureCount?: number } | undefined;
				invalidateQueries: (filters: { queryKey: readonly unknown[] }) => Promise<void>;
			};
		}).__qc;
		const state = client?.getQueryState(["workspaces"]);
		return {
			queryStatus: state?.status ?? "missing",
			fetchStatus: state?.fetchStatus ?? "missing",
			fetchFailureCount: state?.fetchFailureCount ?? 0,
		};
	});

const setDocumentVisibility = (page: import("@playwright/test").Page, visibility: "visible" | "hidden") =>
	page.evaluate((nextVisibility) => {
		Object.defineProperty(document, "visibilityState", {
			configurable: true,
			get: () => nextVisibility,
		});
		// TanStack Query's installed focusManager registers this event on window
		// and reads globalThis.document.visibilityState when it handles it.
		window.dispatchEvent(new Event("visibilitychange"));
	}, visibility);

test("diagnostic: large workspace snapshot, refetch race, and SSE replay keep the worker row in sync @T0 @SIDEBAR_DIAG", async ({ page }) => {
	test.setTimeout(90_000);
	page.on("pageerror", (error) => console.log("SIDEBAR_DIAGNOSTIC pageerror", error.message));
	const workers: FakeWorker[] = Array.from({ length: 491 }, (_, index) => {
		const number = index + 1;
		return {
			id: `seed-${String(number).padStart(3, "0")}`,
			title: number === 443 ? "Index 443 target worker" : `Seed worker ${number}`,
			createdAt: new Date(Date.UTC(2024, 0, 1) + number * 1_000).toISOString(),
		};
	});
	await installFakeAgent(page, { workers, expandProject: true });
	await page.goto("/#/");
	const projectRow = page.locator('[data-sidebar="menu-button"]').filter({ hasText: "fake-proj" }).first();
	await expect(projectRow).toBeVisible();
	await projectRow.click();
	await expect.poll(async () => (await cacheState(page)).sessionCount, { timeout: 20_000 }).toBe(492);
	await expect(page.locator('[data-project-folder]')).toHaveAttribute("aria-expanded", "true");

	const initialCache = await cacheState(page);
	expect(initialCache.sessionCount).toBe(492);
	expect(initialCache.sessionIds[443]).toBe("seed-443");
	await expect(page.locator('[data-testid="session-list-fake-proj"] [data-session-row]')).toHaveCount(491);
	await expect(page.locator('[data-testid="session-list-fake-proj"]').getByText("Index 443 target worker", { exact: true })).toBeVisible();

	// Arrange a new-worker event to arrive from inside the next fetchWorkspaces
	// snapshot. The returned snapshot is deliberately pre-creation, so the event
	// transport sees an event while the workspace query is fetching.
	await page.evaluate(() => {
		window.__aoFakeAgent!.createWorkerDuringNextSnapshot({ id: "race-worker", title: "Race worker" });
		void (window as unknown as { __qc: { invalidateQueries: (filters: { queryKey: string[] }) => Promise<void> } }).__qc.invalidateQueries({ queryKey: ["workspaces"] });
	});
	await expect.poll(async () => (await cacheState(page)).sessionIds.includes("race-worker"), { timeout: 5_000 }).toBe(true);
	await expect(page.locator('[data-testid="session-list-fake-proj"]').getByText("Race worker", { exact: true })).toBeVisible();
	const raceCache = await cacheState(page);
	const raceCapture = {
		cacheSessionCount: raceCache.sessionCount,
		workerIndex: raceCache.sessionIds.indexOf("race-worker"),
		row: await sidebarRowState(page, "Race worker"),
	};
	console.log("SIDEBAR_DIAGNOSTIC race", JSON.stringify(raceCapture));

	// Disconnect the fake stream and arrange a 378k-frame reconnect replay. The
	// new session is durable in the snapshot while its CDC event waits behind the
	// replay backlog, matching the original investigation's reported scale.
	await page.evaluate(() => window.__aoFakeAgent!.disconnectEventsWithReplayBacklog(378_000));
	await expect.poll(async () => page.evaluate(() => window.__aoFakeAgent!.replayBacklogRemaining()), { timeout: 8_000 }).toBeGreaterThan(0);
	await page.evaluate(() => window.__aoFakeAgent!.createWorkerDuringReplay({ id: "replay-worker", title: "Replay worker" }));
	const replayCacheAtCapture = await cacheState(page);
	const duringReplay = {
		cacheSessionCount: replayCacheAtCapture.sessionCount,
		workerIndex: replayCacheAtCapture.sessionIds.indexOf("replay-worker"),
		row: await sidebarRowState(page, "Replay worker"),
		backlogRemaining: await page.evaluate(() => window.__aoFakeAgent!.replayBacklogRemaining()),
	};
	console.log("SIDEBAR_DIAGNOSTIC during-replay", JSON.stringify(duringReplay));
	await expect.poll(async () => page.evaluate(() => window.__aoFakeAgent!.replayBacklogRemaining()), { timeout: 30_000 }).toBe(0);
	await expect.poll(async () => (await cacheState(page)).sessionIds.includes("replay-worker"), { timeout: 5_000 }).toBe(true);
	await expect(page.locator('[data-testid="session-list-fake-proj"]').getByText("Replay worker", { exact: true })).toBeVisible();
	const afterReplayCache = await cacheState(page);
	const afterReplay = {
		cacheSessionCount: afterReplayCache.sessionCount,
		workerIndex: afterReplayCache.sessionIds.indexOf("replay-worker"),
		row: await sidebarRowState(page, "Replay worker"),
	};
	console.log("SIDEBAR_DIAGNOSTIC after-replay", JSON.stringify(afterReplay));

	// Model the event being missed entirely while keeping the renderer mounted.
	// This isolates the documented 15-second poll as recovery evidence.
	await page.evaluate(() => window.__aoFakeAgent!.createWorkerWithoutEvent({ id: "poll-worker", title: "Poll worker" }));
	const pollCacheBefore = await cacheState(page);
	const beforePoll = {
		cacheSessionCount: pollCacheBefore.sessionCount,
		workerIndex: pollCacheBefore.sessionIds.indexOf("poll-worker"),
		row: await sidebarRowState(page, "Poll worker"),
	};
	console.log("SIDEBAR_DIAGNOSTIC before-poll", JSON.stringify(beforePoll));
	expect(beforePoll.workerIndex).toBe(-1);
	expect(beforePoll.row.mounted).toBe(false);
	const pollStartedAt = await page.evaluate(() => Date.now());
	await page.waitForTimeout(15_500);
	const pollElapsedMs = await page.evaluate((startedAt) => Date.now() - startedAt, pollStartedAt);
	expect(pollElapsedMs).toBeGreaterThanOrEqual(15_000);
	await expect(page.locator('[data-testid="session-list-fake-proj"]').getByText("Poll worker", { exact: true })).toBeVisible();
	const pollCacheAfter = await cacheState(page);
	const afterPoll = {
		cacheSessionCount: pollCacheAfter.sessionCount,
		workerIndex: pollCacheAfter.sessionIds.indexOf("poll-worker"),
		row: await sidebarRowState(page, "Poll worker"),
		pollElapsedMs,
	};
	console.log("SIDEBAR_DIAGNOSTIC after-poll", JSON.stringify(afterPoll));
	expect(afterPoll.workerIndex).toBeGreaterThanOrEqual(0);
	expect(afterPoll.row.mounted).toBe(true);
	expect(afterPoll.row.visible).toBe(true);
});

test("diagnostic: continuous SSE pressure does not starve workspace invalidation for two minutes @T0 @SIDEBAR_DIAG", async ({ page }) => {
	test.setTimeout(180_000);
	await openLargeExpandedWorkspace(page);

	const invalidationCount = await page.evaluate(() => {
		const target = window as unknown as {
			__qc: { invalidateQueries: (filters: { queryKey: readonly unknown[] }) => Promise<void> };
			__workspaceInvalidationCount?: number;
		};
		const original = target.__qc.invalidateQueries.bind(target.__qc);
		target.__workspaceInvalidationCount = 0;
		target.__qc.invalidateQueries = (filters) => {
			if (filters.queryKey[0] === "workspaces") target.__workspaceInvalidationCount! += 1;
			return original(filters);
		};
		return target.__workspaceInvalidationCount;
	});
	const snapshotsBefore = await page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount());
	const startTime = await page.evaluate(() => Date.now());
	// EventSource can hand the renderer a deep buffered queue under replay load.
	// Deliver 100 recognized session frames in each 50 ms tick so each debounce
	// window receives many arrivals even if rendering delays the next timer task.
	await page.evaluate(() => window.__aoFakeAgent!.startContinuousWorkspaceEvents(50, 100));
	await page.waitForTimeout(1_000);
	await page.evaluate(() => window.__aoFakeAgent!.createWorker({ id: "continuous-worker", title: "Continuous worker" }));
	await page.waitForTimeout(120_000);
	await page.evaluate(() => window.__aoFakeAgent!.stopContinuousWorkspaceEvents());
	const elapsedMs = await page.evaluate((startedAt) => Date.now() - startedAt, startTime);
	const eventCount = await page.evaluate(() => window.__aoFakeAgent!.continuousWorkspaceEventCount());
	const snapshotsAfter = await page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount());
	const invalidationsAfter = await page.evaluate(() => (window as unknown as { __workspaceInvalidationCount?: number }).__workspaceInvalidationCount ?? 0);
	const capturedCache = await cacheState(page);
	const capture = {
		elapsedMs,
		continuousEvents: eventCount,
		invalidationCount: invalidationsAfter - invalidationCount,
		snapshotCalls: snapshotsAfter - snapshotsBefore,
		cacheSessionCount: capturedCache.sessionCount,
		workerIndex: capturedCache.sessionIds.indexOf("continuous-worker"),
		row: await sidebarRowState(page, "Continuous worker"),
	};
	console.log("SIDEBAR_DIAGNOSTIC continuous-pressure", JSON.stringify(capture));
	expect(elapsedMs).toBeGreaterThanOrEqual(120_000);
	expect(eventCount).toBeGreaterThanOrEqual(5_000);
	expect(capture.invalidationCount).toBeGreaterThan(0);
	expect(capture.snapshotCalls).toBeGreaterThan(0);
	expect(capture.workerIndex).toBeGreaterThanOrEqual(0);
	expect(capture.row.mounted).toBe(true);
	expect(capture.row.visible).toBe(true);
});

test("diagnostic: exhausted snapshot retries do not stop the 15-second workspace poll @T0 @SIDEBAR_DIAG", async ({ page }) => {
	test.setTimeout(60_000);
	await openLargeExpandedWorkspace(page);
	const baseline = await page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount());
	await page.evaluate(() => {
		window.__aoFakeAgent!.createWorkerWithoutEvent({ id: "failed-poll-worker", title: "Failed poll worker" });
		window.__aoFakeAgent!.setSnapshotFailures(2);
	});
	const failureStart = await page.evaluate(() => Date.now());
	await expect.poll(async () => (await page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount())) - baseline, { timeout: 20_000 }).toBeGreaterThanOrEqual(2);
	await expect.poll(async () => (await queryMetrics(page)).queryStatus, { timeout: 5_000 }).toBe("error");
	const failedCapture = {
		elapsedMs: await page.evaluate((startedAt) => Date.now() - startedAt, failureStart),
		snapshotCalls: (await page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount())) - baseline,
		query: await queryMetrics(page),
		cache: await cacheState(page),
		row: await sidebarRowState(page, "Failed poll worker"),
	};
	console.log("SIDEBAR_DIAGNOSTIC failed-poll", JSON.stringify(failedCapture));
	expect(failedCapture.cache.sessionIds).not.toContain("failed-poll-worker");
	expect(failedCapture.row.mounted).toBe(false);

	const nextPollStart = await page.evaluate(() => Date.now());
	await expect.poll(async () => (await page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount())) - baseline, { timeout: 20_000 }).toBeGreaterThan(failedCapture.snapshotCalls);
	await expect.poll(async () => (await cacheState(page)).sessionIds.includes("failed-poll-worker"), { timeout: 5_000 }).toBe(true);
	const recoveredCache = await cacheState(page);
	const recoveryCapture = {
		elapsedMs: await page.evaluate((startedAt) => Date.now() - startedAt, nextPollStart),
		snapshotCalls: (await page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount())) - baseline,
		query: await queryMetrics(page),
		cacheSessionCount: recoveredCache.sessionCount,
		workerIndex: recoveredCache.sessionIds.indexOf("failed-poll-worker"),
		row: await sidebarRowState(page, "Failed poll worker"),
	};
	console.log("SIDEBAR_DIAGNOSTIC poll-recovery", JSON.stringify(recoveryCapture));
	await expect(page.locator('[data-testid="session-list-fake-proj"]').getByText("Failed poll worker", { exact: true })).toBeVisible();
	expect(recoveryCapture.workerIndex).toBeGreaterThanOrEqual(0);
});

test("diagnostic: a delivered SSE event updates the cache while document is hidden @T0 @SIDEBAR_DIAG", async ({ page }) => {
	test.setTimeout(165_000);
	await openLargeExpandedWorkspace(page);
	const beforeHidden = await page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount());
	const hiddenStartedAt = await page.evaluate(() => Date.now());
	await setDocumentVisibility(page, "hidden");
	await page.waitForTimeout(1_000);
	const snapshotsAtHiddenStart = await page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount());
	await page.evaluate(() => window.__aoFakeAgent!.createWorker({ id: "hidden-event-worker", title: "Hidden event worker" }));
	await expect.poll(async () => (await cacheState(page)).sessionIds.includes("hidden-event-worker"), { timeout: 5_000 }).toBe(true);
	const eventCapture = {
		cacheSessionCount: (await cacheState(page)).sessionCount,
		workerIndex: (await cacheState(page)).sessionIds.indexOf("hidden-event-worker"),
		snapshotCalls: (await page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount())) - snapshotsAtHiddenStart,
		row: await sidebarRowState(page, "Hidden event worker"),
		visibility: await page.evaluate(() => document.visibilityState),
	};
	console.log("SIDEBAR_DIAGNOSTIC hidden-sse-event", JSON.stringify(eventCapture));
	await page.waitForTimeout(120_000);
	const hiddenDurationMs = await page.evaluate((startedAt) => Date.now() - startedAt, hiddenStartedAt);
	const endHiddenCache = await cacheState(page);
	const hiddenCompleteCapture = {
		hiddenDurationMs,
		snapshotCallsSinceInitial: (await page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount())) - beforeHidden,
		cacheSessionCount: endHiddenCache.sessionCount,
		workerIndex: endHiddenCache.sessionIds.indexOf("hidden-event-worker"),
		row: await sidebarRowState(page, "Hidden event worker"),
	};
	console.log("SIDEBAR_DIAGNOSTIC hidden-sse-event-after-two-minutes", JSON.stringify(hiddenCompleteCapture));
	expect(hiddenDurationMs).toBeGreaterThanOrEqual(120_000);
	expect(hiddenCompleteCapture.workerIndex).toBeGreaterThanOrEqual(0);
	expect(hiddenCompleteCapture.row.mounted).toBe(true);
	await setDocumentVisibility(page, "visible");
});

test("diagnostic: a missed hidden SSE event recovers on the next interval after visibility returns @T0 @SIDEBAR_DIAG", async ({ page }) => {
	test.setTimeout(165_000);
	await openLargeExpandedWorkspace(page);
	await setDocumentVisibility(page, "hidden");
	await page.waitForTimeout(1_000);
	const snapshotsAtHiddenStart = await page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount());
	const hiddenStartedAt = await page.evaluate(() => Date.now());
	await page.evaluate(() => window.__aoFakeAgent!.createWorkerWithoutEvent({ id: "hidden-missed-worker", title: "Hidden missed worker" }));
	await page.waitForTimeout(120_000);
	const cacheAfterTwoMinutesHidden = await cacheState(page);
	const hiddenCapture = {
		hiddenDurationMs: await page.evaluate((startedAt) => Date.now() - startedAt, hiddenStartedAt),
		snapshotCallsDuringHidden: (await page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount())) - snapshotsAtHiddenStart,
		cacheSessionCount: cacheAfterTwoMinutesHidden.sessionCount,
		workerIndex: cacheAfterTwoMinutesHidden.sessionIds.indexOf("hidden-missed-worker"),
		row: await sidebarRowState(page, "Hidden missed worker"),
		query: await queryMetrics(page),
	};
	console.log("SIDEBAR_DIAGNOSTIC hidden-missed-event", JSON.stringify(hiddenCapture));
	expect(hiddenCapture.hiddenDurationMs).toBeGreaterThanOrEqual(120_000);
	expect(hiddenCapture.snapshotCallsDuringHidden).toBe(0);
	expect(hiddenCapture.workerIndex).toBe(-1);
	expect(hiddenCapture.row.mounted).toBe(false);

	const restoreStartedAt = await page.evaluate(() => Date.now());
	const callsBeforeRestore = await page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount());
	await setDocumentVisibility(page, "visible");
	const callsImmediatelyAfterRestore = await page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount());
	await expect.poll(async () => page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount()), { timeout: 16_000 }).toBeGreaterThan(callsBeforeRestore);
	await expect.poll(async () => (await cacheState(page)).sessionIds.includes("hidden-missed-worker"), { timeout: 5_000 }).toBe(true);
	const recoveredCache = await cacheState(page);
	const recoveryCapture = {
		elapsedAfterRestoreMs: await page.evaluate((startedAt) => Date.now() - startedAt, restoreStartedAt),
		callsImmediatelyAfterRestore: callsImmediatelyAfterRestore - callsBeforeRestore,
		callsAfterRecovery: (await page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount())) - callsBeforeRestore,
		cacheSessionCount: recoveredCache.sessionCount,
		workerIndex: recoveredCache.sessionIds.indexOf("hidden-missed-worker"),
		row: await sidebarRowState(page, "Hidden missed worker"),
	};
	console.log("SIDEBAR_DIAGNOSTIC hidden-missed-event-recovery", JSON.stringify(recoveryCapture));
	await expect(page.locator('[data-testid="session-list-fake-proj"]').getByText("Hidden missed worker", { exact: true })).toBeVisible();
	expect(recoveryCapture.workerIndex).toBeGreaterThanOrEqual(0);
});

test("diagnostic: EventSource reopen performs a workspace catch-up after a hidden missed event @T0 @SIDEBAR_DIAG", async ({ page }) => {
	test.setTimeout(30_000);
	await openLargeExpandedWorkspace(page);
	await setDocumentVisibility(page, "hidden");
	await page.waitForTimeout(1_000);
	await page.evaluate(() => window.__aoFakeAgent!.createWorkerWithoutEvent({ id: "reopen-catchup-worker", title: "Reopen catch-up worker" }));
	expect((await cacheState(page)).sessionIds).not.toContain("reopen-catchup-worker");
	const snapshotsBeforeReopen = await page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount());
	await setDocumentVisibility(page, "visible");
	const restoredAt = await page.evaluate(() => Date.now());
	// This fixture signal invokes the still-connected fake EventSource's `open`
	// callback, modeling the callback native EventSource emits after reconnect.
	await page.evaluate(() => window.__aoFakeAgent!.signalEventsReopened());
	await expect.poll(async () => (await cacheState(page)).sessionIds.includes("reopen-catchup-worker"), { timeout: 5_000 }).toBe(true);
	const caughtUp = await cacheState(page);
	const capture = {
		elapsedAfterReopenMs: await page.evaluate((startedAt) => Date.now() - startedAt, restoredAt),
		snapshotCallsAfterReopen: (await page.evaluate(() => window.__aoFakeAgent!.snapshotCallCount())) - snapshotsBeforeReopen,
		cacheSessionCount: caughtUp.sessionCount,
		workerIndex: caughtUp.sessionIds.indexOf("reopen-catchup-worker"),
		row: await sidebarRowState(page, "Reopen catch-up worker"),
	};
	console.log("SIDEBAR_DIAGNOSTIC hidden-event-reopen-catchup", JSON.stringify(capture));
	await expect(page.locator('[data-testid="session-list-fake-proj"]').getByText("Reopen catch-up worker", { exact: true })).toBeVisible();
	expect(capture.workerIndex).toBeGreaterThanOrEqual(0);
});
