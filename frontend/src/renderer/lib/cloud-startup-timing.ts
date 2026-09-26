export type CloudStartupAttempt = {
	attemptId: string;
	startedAtMs: number;
};

export type CloudStartupMeasurement = {
	attemptId: string;
	elapsedMs: number;
};

const MAX_PENDING_ATTEMPTS = 100;
const MAX_ATTEMPT_AGE_MS = 30 * 60_000;

const pendingBySession = new Map<string, CloudStartupAttempt>();

function monotonicNow(): number {
	return globalThis.performance?.now() ?? Date.now();
}

function discardExpired(nowMs: number): void {
	for (const [sessionId, attempt] of pendingBySession) {
		if (nowMs - attempt.startedAtMs > MAX_ATTEMPT_AGE_MS) pendingBySession.delete(sessionId);
	}
}

export function beginCloudStartupAttempt(
	attemptId: string = globalThis.crypto.randomUUID(),
	startedAtMs = monotonicNow(),
): CloudStartupAttempt {
	return { attemptId, startedAtMs };
}

export function bindCloudStartupAttempt(
	sessionId: string,
	attempt: CloudStartupAttempt,
	nowMs = monotonicNow(),
): void {
	discardExpired(nowMs);
	if (!pendingBySession.has(sessionId) && pendingBySession.size >= MAX_PENDING_ATTEMPTS) {
		const oldestSessionId = pendingBySession.keys().next().value;
		if (oldestSessionId !== undefined) pendingBySession.delete(oldestSessionId);
	}
	pendingBySession.set(sessionId, attempt);
}

export function completeCloudStartupAttempt(
	sessionId: string,
	nowMs = monotonicNow(),
): CloudStartupMeasurement | undefined {
	discardExpired(nowMs);
	const attempt = pendingBySession.get(sessionId);
	if (!attempt) return undefined;
	pendingBySession.delete(sessionId);
	return {
		attemptId: attempt.attemptId,
		elapsedMs: Math.max(0, Math.round(nowMs - attempt.startedAtMs)),
	};
}
