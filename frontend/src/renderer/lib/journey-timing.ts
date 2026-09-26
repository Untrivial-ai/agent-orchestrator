import { captureRendererEvent } from "./telemetry";

type Surface = "chat" | "tui";
type Scope = "local" | "standalone" | "cloud";
type Outcome = "ready" | "failed" | "timeout" | "cancelled";

const MAX_DURATION_MS = 300_000;
const SESSION_TIMEOUT_MS = 120_000;
const SAMPLE_RATE = 0.05;
const TIMING_BUDGET_KEY = "ao.telemetry.journeyTimingBudget";
const TIMING_BUDGET_MS = 24 * 60 * 60_000;
const MAX_TIMING_EVENTS_PER_DAY = 20;

type PendingSession = {
	sessionId: string;
	startedAt: number;
	timer: ReturnType<typeof setTimeout>;
};

type PendingTask = PendingSession & { attempt: number; scope: Scope };

let pendingSession: PendingSession | undefined;
let pendingTask: PendingTask | undefined;
let nextTaskAttempt = 0;

function durationSince(startedAt: number): number {
	return Math.min(MAX_DURATION_MS, Math.max(0, Math.round(performance.now() - startedAt)));
}

// Shared across the three timing names and persisted across renderer restarts.
// If storage is unavailable, skip optional timing telemetry rather than reset
// the budget on every crash/relaunch.
function reserveTimingBudget(now = Date.now()): boolean {
	try {
		const raw = window.localStorage.getItem(TIMING_BUDGET_KEY);
		const stored = raw ? (JSON.parse(raw) as { start: number; count: number }) : undefined;
		if (raw && (!stored || !Number.isFinite(stored.start)
			|| !Number.isInteger(stored.count) || stored.count < 0)) return false;
		let start = now;
		let count = 0;
		if (stored) {
			if (now < stored.start) return false;
			if (now - stored.start < TIMING_BUDGET_MS) {
				start = stored.start;
				count = stored.count;
			}
		}
		if (count >= MAX_TIMING_EVENTS_PER_DAY) return false;
		window.localStorage.setItem(TIMING_BUDGET_KEY, JSON.stringify({ start, count: count + 1 }));
		return true;
	} catch {
		return false;
	}
}

function captureTiming(event: string, durationMs: number, outcome: Outcome, surface?: Surface, scope?: Scope): void {
	if (Math.random() >= SAMPLE_RATE || !reserveTimingBudget()) return;
	void captureRendererEvent(event, {
		duration_ms: durationMs,
		outcome,
		...(surface ? { surface } : {}),
		...(scope ? { scope } : {}),
	});
}

function finishSession(outcome: Outcome, surface?: Surface): void {
	if (!pendingSession) return;
	const pending = pendingSession;
	pendingSession = undefined;
	clearTimeout(pending.timer);
	captureTiming("ao.renderer.session_open_timing", durationSince(pending.startedAt), outcome, surface);
}

function finishTask(outcome: Outcome, surface?: Surface): void {
	if (!pendingTask) return;
	const pending = pendingTask;
	pendingTask = undefined;
	clearTimeout(pending.timer);
	captureTiming("ao.renderer.task_create_timing", durationSince(pending.startedAt), outcome, surface, pending.scope);
}

/** Called at the router's navigation boundary, before route loaders run. */
export function startSessionOpen(sessionId: string | undefined): void {
	if (pendingTask?.sessionId && pendingTask.sessionId !== sessionId) finishTask("cancelled");
	if (pendingSession?.sessionId === sessionId) return;
	finishSession("cancelled");
	if (!sessionId || pendingTask?.sessionId === sessionId) return;
	const startedAt = performance.now();
	const pending: PendingSession = {
		sessionId,
		startedAt,
		timer: setTimeout(() => {
			if (pendingSession === pending) finishSession("timeout");
		}, SESSION_TIMEOUT_MS),
	};
	pendingSession = pending;
}

/** Begins at a validated submit, including readiness checks and attachment staging. */
export function startTaskCreate(scope: Scope): number {
	finishSession("cancelled");
	finishTask("cancelled");
	const attempt = ++nextTaskAttempt;
	const startedAt = performance.now();
	const pending: PendingTask = {
		attempt,
		scope,
		sessionId: "",
		startedAt,
		timer: setTimeout(() => {
			if (pendingTask === pending) finishTask("timeout");
		}, MAX_DURATION_MS),
	};
	pendingTask = pending;
	return attempt;
}

export function taskCreateReturned(attempt: number, sessionId: string): void {
	if (pendingTask?.attempt === attempt) pendingTask.sessionId = sessionId;
}

export function taskCreateFailed(attempt: number): void {
	if (pendingTask?.attempt === attempt) finishTask("failed");
}

/** Called after the conversation or terminal replay is painted and visible. */
export function sessionUsable(sessionId: string, surface: Surface): void {
	if (pendingSession?.sessionId === sessionId) finishSession("ready", surface);
	if (pendingTask?.sessionId === sessionId) finishTask("ready", surface);
}

/** A selected file tab is outside the chat and terminal journey. */
export function skipHiddenSession(sessionId: string): void {
	if (pendingSession?.sessionId === sessionId) {
		clearTimeout(pendingSession.timer);
		pendingSession = undefined;
	}
	if (pendingTask?.sessionId === sessionId) {
		clearTimeout(pendingTask.timer);
		pendingTask = undefined;
	}
}

/** Main measures its own monotonic clock from native window creation. */
export function recordStartupTiming(durationMs: number, outcome: "ready" | "failed" | "timeout"): void {
	if (!Number.isFinite(durationMs) || durationMs < 0) return;
	captureTiming("ao.renderer.startup_timing", Math.min(MAX_DURATION_MS, Math.round(durationMs)), outcome);
}
