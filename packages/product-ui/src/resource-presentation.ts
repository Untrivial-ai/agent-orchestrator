/**
 * Every rule behind the memory monitor, as pure functions: which pressure
 * state the machine is in, which single suggestion to make, what colour a
 * card chip gets, how bytes read. Components hold no thresholds of their
 * own, so the product logic is testable without rendering anything, the
 * same way session-presentation.ts works for status.
 *
 * The one idea underneath: colour means "do something", not "here is a
 * number". When nothing needs doing everything is grey.
 */

/** Fine: ignore it. Tight soon: worth a glance. Tight: the machine is struggling. */
export type PressureState = "fine" | "tight_soon" | "tight";

export type MachineReading = {
	totalBytes: number;
	availableBytes: number;
	/** PSI some avg10 on Linux (percent of the last 10 s a task stalled on memory); 100 minus available percent elsewhere. */
	pressureRaw: number;
	pressureSource: string;
};

/**
 * Thresholds are a first guess, to be tuned against real readings. On the
 * PSI path a few percent of stall time already feels sluggish; the
 * available-percent fallback maps the classic 25% / 10% free cut-offs.
 */
export function pressureState(machine: MachineReading): PressureState {
	return pressureStateFromRaw(machine.pressureRaw, machine.pressureSource);
}

/** The same rule from a bare reading, for colouring one bar of the graph. */
export function pressureStateFromRaw(pressureRaw: number, pressureSource: string): PressureState {
	if (pressureSource === "psi") {
		if (pressureRaw > 20) return "tight";
		if (pressureRaw >= 5) return "tight_soon";
		return "fine";
	}
	if (pressureSource === "memorystatus") {
		// macOS's own kernel verdict: 1 normal, 2 warn, 4 critical. A direct
		// kernel signal like PSI, not a derived free-memory percentage.
		if (pressureRaw >= 4) return "tight";
		if (pressureRaw >= 2) return "tight_soon";
		return "fine";
	}
	// The fallback is 100 minus the available percent, so the classic
	// 25% / 10% free cut-offs sit at 75 and 90.
	if (pressureRaw > 90) return "tight";
	if (pressureRaw > 75) return "tight_soon";
	return "fine";
}

/** What the monitor knows about one session when it decides what to suggest. */
export type ResourceSessionFacts = {
	id: string;
	title: string;
	rssBytes: number;
	/** True while the agent is mid-turn. */
	working: boolean;
	/** Seconds since the agent last did anything; undefined when unknown. */
	idleSeconds?: number;
};

export type ResourceSuggestion =
	| { kind: "none" }
	| { kind: "largest"; sessionId: string; title: string; rssBytes: number };

/**
 * One line at most, and only about AO: while the machine is tight, name the
 * session holding the most. Never a verdict on other applications — the bar
 * already shows AO's size against what is free, and the user can read the
 * rest of the machine in their own monitor.
 */
export function resourceSuggestion(
	state: PressureState,
	_machine: { totalBytes: number; availableBytes: number },
	_aoBytes: number,
	sessions: ResourceSessionFacts[],
): ResourceSuggestion {
	if (state === "fine") return { kind: "none" };
	const largest = [...sessions].sort((a, b) => b.rssBytes - a.rssBytes)[0];
	if (!largest) return { kind: "none" };
	return { kind: "largest", sessionId: largest.id, title: largest.title, rssBytes: largest.rssBytes };
}

export type ChipTone = "neutral" | "warning" | "critical";

/**
 * A card chip is grey unless this card is part of the fix: yellow when the
 * machine is tight-ish and the session sits idle, red when it is tight and
 * this is the single largest session.
 */
export function chipTone(state: PressureState, session: ResourceSessionFacts, largestSessionId: string | undefined): ChipTone {
	if (state === "tight" && session.id === largestSessionId) return "critical";
	if (state !== "fine" && !session.working) return "warning";
	return "neutral";
}

/** The biggest live session by memory, for the red-chip rule. */
export function largestSession(sessions: ResourceSessionFacts[]): string | undefined {
	let best: ResourceSessionFacts | undefined;
	for (const s of sessions) {
		if (!best || s.rssBytes > best.rssBytes) best = s;
	}
	return best?.id;
}

const MB = 1000 ** 2;
const GB = 1000 ** 3;

/**
 * Whole megabytes under a gigabyte, one decimal GB above, decimal units like
 * Activity Monitor. Whole MB rather than 10 MB steps: rows are summed into
 * the AO total, and coarse rounding on each row made 123 + 157 read as
 * 120 + 160 against a 280 total.
 */
export function formatResourceBytes(bytes: number): string {
	if (bytes >= GB) return `${(bytes / GB).toFixed(1)} GB`;
	const mb = Math.round(bytes / MB);
	if (mb >= 1000) return `${(mb / 1000).toFixed(1)} GB`;
	return `${Math.max(1, mb)} MB`;
}

/** Whole percent of one core; CPU is only shown while working, so zero never appears. */
export function formatResourceCPU(percent: number): string {
	return `${Math.round(percent)}%`;
}

/** Sort by memory, descending, but only move rows that changed by a real margin. */
const RESORT_MARGIN = 0.1;

/**
 * Keeps the previous order unless a session's size changed enough to
 * justify moving it, so two near-equal sessions do not swap every sample.
 */
export function stableResourceOrder<T extends { id: string; rssBytes: number }>(previous: string[], rows: T[]): T[] {
	const sorted = [...rows].sort((a, b) => b.rssBytes - a.rssBytes);
	if (previous.length === 0) return sorted;
	const byId = new Map(rows.map((r) => [r.id, r] as const));
	const kept = previous.map((id) => byId.get(id)).filter((r): r is T => r !== undefined);
	const known = new Set(kept.map((r) => r.id));
	const fresh = sorted.filter((r) => !known.has(r.id));
	const candidate = [...kept, ...fresh];
	for (let i = 1; i < candidate.length; i++) {
		const above = candidate[i - 1].rssBytes;
		const here = candidate[i].rssBytes;
		if (here > above * (1 + RESORT_MARGIN)) return sorted;
	}
	return candidate;
}
