import { captureRendererEvent, isDeniedEvent } from "./telemetry";

const STORAGE_KEY = "ao.telemetry.sessionManagement.v1";
const CHECKPOINT_MS = 5 * 60_000;
const IDLE_MS = 5 * 60_000;
const FLUSH_MIN_MS = 45 * 60_000;
const FLUSH_JITTER_MS = 15 * 60_000;
const TERMINAL_BURST_MS = 30_000;

export type SessionSurface = { kind: "orchestrator" | "worker"; sessionId: string } | null;
type Interaction = "chat" | "terminal" | "lifecycle";
type TransitionKey =
	| "orchestrator_to_worker"
	| "orchestrator_same"
	| "orchestrator_switch"
	| "worker_to_orchestrator"
	| "worker_same"
	| "worker_switch";

type Summary = Record<TransitionKey, number> & {
	orchestratorActiveMs: number;
	workerActiveMs: number;
	manualWorkerOpens: number;
	directWorkerChatSteer: number;
	directWorkerTerminalBursts: number;
	directWorkerLifecycleActions: number;
	patternOrchestratorWorkerOrchestrator: number;
	patternOrchestratorMultipleWorkers: number;
};

type PersistedState = {
	version: 1;
	windowId: string;
	windowStartedAt: number;
	summary: Summary;
	previousSurface: SessionSurface;
	path: Array<{ kind: "orchestrator" | "worker"; sessionId: string }>;
	pendingManualWorkerId?: string;
	pendingManualWorkerAt?: number;
	lastTerminalBurstAt: Record<string, number>;
};

const zeroSummary = (): Summary => ({
	orchestratorActiveMs: 0,
	workerActiveMs: 0,
	manualWorkerOpens: 0,
	directWorkerChatSteer: 0,
	directWorkerTerminalBursts: 0,
	directWorkerLifecycleActions: 0,
	patternOrchestratorWorkerOrchestrator: 0,
	patternOrchestratorMultipleWorkers: 0,
	orchestrator_to_worker: 0,
	orchestrator_same: 0,
	orchestrator_switch: 0,
	worker_to_orchestrator: 0,
	worker_same: 0,
	worker_switch: 0,
});

function freshState(now: number): PersistedState {
	return {
		version: 1,
		windowId: crypto.randomUUID(),
		windowStartedAt: now,
		summary: zeroSummary(),
		previousSurface: null,
		path: [],
		lastTerminalBurstAt: {},
	};
}

function loadState(storage: Storage, now: number): PersistedState {
	try {
		const parsed = JSON.parse(storage.getItem(STORAGE_KEY) ?? "null") as Partial<PersistedState> | null;
		if (parsed?.version === 1 && parsed.summary && typeof parsed.windowStartedAt === "number") {
			return { ...freshState(now), ...parsed, summary: { ...zeroSummary(), ...parsed.summary } };
		}
	} catch {
		// Replace malformed local state.
	}
	return freshState(now);
}

function transitionKey(from: NonNullable<SessionSurface>, to: NonNullable<SessionSurface>): TransitionKey {
	if (from.kind !== to.kind) return `${from.kind}_to_${to.kind}` as TransitionKey;
	return `${to.kind}_${from.sessionId === to.sessionId ? "same" : "switch"}` as TransitionKey;
}

export class SessionManagementAccumulator {
	private state: PersistedState;
	private surface: SessionSurface = null;
	private segmentStartedAt: number | null = null;
	private lastActivityAt: number;
	private trackingActive = true;

	constructor(
		private readonly storage: Storage,
		private readonly now: () => number = Date.now,
		private readonly capture: (event: string, properties: Record<string, unknown>) => Promise<void> = captureRendererEvent,
	) {
		const current = now();
		this.state = loadState(storage, current);
		this.lastActivityAt = current;
	}

	setSurface(surface: SessionSurface): void {
		const now = this.now();
		this.checkpoint(now, false);
		this.surface = surface;
		if (this.isActive(now) && surface) this.segmentStartedAt = now;
		const previous = this.state.previousSurface;
		if (previous && surface) this.state.summary[transitionKey(previous, surface)] += 1;
		if (surface) {
			this.recordPath(surface);
			if (
				surface.kind === "worker" &&
				this.state.pendingManualWorkerId === surface.sessionId &&
				now - (this.state.pendingManualWorkerAt ?? 0) <= 10_000
			) {
				this.state.summary.manualWorkerOpens += 1;
			}
		}
		this.state.pendingManualWorkerId = undefined;
		this.state.pendingManualWorkerAt = undefined;
		this.state.previousSurface = surface;
		this.persist();
	}

	markManualWorkerOpen(sessionId: string): void {
		this.state.pendingManualWorkerId = sessionId;
		this.state.pendingManualWorkerAt = this.now();
		this.persist();
	}

	recordInteraction(
		sessionId: string,
		kind: Interaction,
		role: "orchestrator" | "worker" | undefined = this.surface?.sessionId === sessionId ? this.surface.kind : undefined,
	): void {
		if (role !== "worker") return;
		const now = this.now();
		if (kind === "terminal") {
			const last = this.state.lastTerminalBurstAt[sessionId];
			if (last !== undefined && now - last < TERMINAL_BURST_MS) return;
			this.state.lastTerminalBurstAt[sessionId] = now;
			this.state.summary.directWorkerTerminalBursts += 1;
		} else if (kind === "chat") {
			this.state.summary.directWorkerChatSteer += 1;
		} else {
			this.state.summary.directWorkerLifecycleActions += 1;
		}
		this.persist();
	}

	activity(): void {
		const now = this.now();
		this.trackingActive = true;
		this.lastActivityAt = now;
		if (this.surface && this.segmentStartedAt === null) this.segmentStartedAt = now;
	}

	pause(): void {
		this.checkpoint(this.now(), false);
		this.trackingActive = false;
		this.segmentStartedAt = null;
	}

	checkpoint(now = this.now(), countSameSurface = true): void {
		if (this.segmentStartedAt !== null && this.surface) {
			const end = Math.min(now, this.lastActivityAt + IDLE_MS);
			const elapsed = Math.max(0, end - this.segmentStartedAt);
			if (this.surface.kind === "orchestrator") this.state.summary.orchestratorActiveMs += elapsed;
			else this.state.summary.workerActiveMs += elapsed;
			this.segmentStartedAt = this.isActive(now) ? now : null;
		}
		if (countSameSurface && this.surface && this.state.previousSurface) {
			this.state.summary[transitionKey(this.state.previousSurface, this.surface)] += 1;
			this.state.previousSurface = this.surface;
		}
		this.persist();
	}

	async flush(reason = "interval"): Promise<void> {
		const now = this.now();
		this.checkpoint(now, false);
		const { summary } = this.state;
		await this.capture("ao.renderer.session_management_summary", {
			measurement_schema_version: 1,
			window_id: this.state.windowId,
			window_duration_seconds: Math.round((now - this.state.windowStartedAt) / 1000),
			flush_reason: reason,
			orchestrator_active_seconds: Math.round(summary.orchestratorActiveMs / 1000),
			worker_active_seconds: Math.round(summary.workerActiveMs / 1000),
			transition_orchestrator_to_worker_count: summary.orchestrator_to_worker,
			transition_orchestrator_same_count: summary.orchestrator_same,
			transition_orchestrator_switch_count: summary.orchestrator_switch,
			transition_worker_to_orchestrator_count: summary.worker_to_orchestrator,
			transition_worker_same_count: summary.worker_same,
			transition_worker_switch_count: summary.worker_switch,
			manual_worker_open_count: summary.manualWorkerOpens,
			direct_worker_chat_steer_count: summary.directWorkerChatSteer,
			direct_worker_terminal_input_burst_count: summary.directWorkerTerminalBursts,
			direct_worker_lifecycle_action_count: summary.directWorkerLifecycleActions,
			pattern_orchestrator_worker_orchestrator_count: summary.patternOrchestratorWorkerOrchestrator,
			pattern_orchestrator_multiple_workers_count: summary.patternOrchestratorMultipleWorkers,
		});
		const currentSurface = this.surface;
		const openPath = this.state.path;
		this.state = freshState(now);
		this.state.previousSurface = currentSurface;
		this.state.path = openPath;
		this.persist();
	}

	isFlushOverdue(): boolean {
		return this.now() - this.state.windowStartedAt >= FLUSH_MIN_MS + FLUSH_JITTER_MS;
	}

	private isActive(now: number): boolean {
		return this.trackingActive && now - this.lastActivityAt <= IDLE_MS;
	}

	private recordPath(surface: NonNullable<SessionSurface>): void {
		const path = this.state.path;
		const last = path.at(-1);
		if (last?.sessionId === surface.sessionId) return;
		path.push(surface);
		if (path.length > 8) path.shift();
		const orchestratorIndexes = path.flatMap((entry, index) => entry.kind === "orchestrator" ? [index] : []);
		if (orchestratorIndexes.length < 2) return;
		const start = orchestratorIndexes.at(-2)!;
		const end = orchestratorIndexes.at(-1)!;
		const workers = path.slice(start + 1, end).filter((entry) => entry.kind === "worker");
		const distinctWorkers = new Set(workers.map((entry) => entry.sessionId)).size;
		if (distinctWorkers === 1) this.state.summary.patternOrchestratorWorkerOrchestrator += 1;
		if (distinctWorkers > 1) this.state.summary.patternOrchestratorMultipleWorkers += 1;
		this.state.path = path.slice(end);
	}

	private persist(): void {
		try {
			this.storage.setItem(STORAGE_KEY, JSON.stringify(this.state));
		} catch {
			// Telemetry must never affect the product action.
		}
	}
}

let runtime: SessionManagementAccumulator | null = null;

export function startSessionManagementTelemetry(): () => void {
	if (runtime || isDeniedEvent("ao.renderer.session_management_summary")) return () => undefined;
	runtime = new SessionManagementAccumulator(window.localStorage);
	const activity = () => runtime?.activity();
	const pause = () => runtime?.pause();
	const visibility = () => document.visibilityState === "visible" ? activity() : pause();
	window.addEventListener("focus", activity);
	window.addEventListener("blur", pause);
	window.addEventListener("pagehide", pause);
	document.addEventListener("pointerdown", activity, { passive: true });
	document.addEventListener("keydown", activity);
	document.addEventListener("visibilitychange", visibility);
	if (document.visibilityState !== "visible" || !document.hasFocus()) pause();
	const checkpoint = window.setInterval(() => runtime?.checkpoint(), CHECKPOINT_MS);
	let flushTimer = 0;
	const scheduleFlush = () => {
		flushTimer = window.setTimeout(() => {
			void runtime?.flush().finally(scheduleFlush);
		}, FLUSH_MIN_MS + Math.random() * FLUSH_JITTER_MS);
	};
	if (runtime.isFlushOverdue()) void runtime.flush("startup").finally(scheduleFlush);
	else scheduleFlush();
	return () => {
		window.clearInterval(checkpoint);
		window.clearTimeout(flushTimer);
		window.removeEventListener("focus", activity);
		window.removeEventListener("blur", pause);
		window.removeEventListener("pagehide", pause);
		document.removeEventListener("pointerdown", activity);
		document.removeEventListener("keydown", activity);
		document.removeEventListener("visibilitychange", visibility);
		pause();
		runtime = null;
	};
}

export function recordSessionSurface(surface: SessionSurface): void {
	runtime?.setSurface(surface);
}

export function recordManualWorkerOpen(sessionId: string): void {
	runtime?.markManualWorkerOpen(sessionId);
}

export function recordDirectWorkerInteraction(sessionId: string, kind: Interaction, role?: "orchestrator" | "worker"): void {
	runtime?.recordInteraction(sessionId, kind, role);
}
