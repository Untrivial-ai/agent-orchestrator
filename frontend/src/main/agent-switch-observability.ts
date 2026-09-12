import { randomBytes } from "node:crypto";
import type {
	AgentSwitchDurableState,
	AgentSwitchPresentationKind,
	AgentSwitchVisibilityIncident,
	AgentSwitchVisibilityMetadata,
	AgentSwitchVisibilityOperation,
} from "../shared/agent-switch-observability";
import {
	buildVisibilityEvent,
	encodeAgentSwitchEnvelopeV1,
	parseAgentSwitchDSN,
	parseAgentSwitchVisibilitySignal,
} from "../shared/agent-switch-observability";

const activeGraceMilliseconds = 15_000;
const historyGraceMilliseconds = 60_000;
const presentationGraceMilliseconds = 2_000;
const recurrenceRecoveryMilliseconds = 5 * 60_000;

type TimeoutHandle = ReturnType<typeof setTimeout>;
type Health = { active: boolean; healthy: boolean };
type WindowState = {
	focused: boolean;
	online: boolean;
	focusSequence: number;
	health: Map<string, Health>;
	expectations: Map<string, PresentationExpectation>;
};
type PresentationExpectation = {
	token: string;
	localKey: string;
	presentationKind: AgentSwitchPresentationKind;
	durableState: AgentSwitchDurableState;
};
type Incident = {
	state: "suspect" | "reported";
	timer?: TimeoutHandle;
	healTimer?: TimeoutHandle;
	remote: AgentSwitchVisibilityIncident;
};

export class AgentSwitchVisibilityController {
	private enabled = false;
	private generation = "";
	private focusSequence = 0;
	private owner: number | null = null;
	private readonly windows = new Map<number, WindowState>();
	private readonly incidents = new Map<string, Incident>();
	private readonly deliveries = new Map<AbortController, Promise<void>>();

	constructor(private readonly options: {
		send: (incident: AgentSwitchVisibilityIncident, consentGeneration: string, signal: AbortSignal) => void | Promise<void>;
		diagnostic?: (code: "visibility_delivery_failed") => void;
		killSwitched?: boolean;
	}) {}

	setPolicy(enabled: boolean, consentGeneration: string): void {
		if (!enabled || consentGeneration !== this.generation) { this.cancelAll(); this.abortDeliveries(); }
		this.enabled = enabled && !this.options.killSwitched;
		this.generation = consentGeneration;
		if (this.enabled) this.reselectOwner();
	}

	registerWindow(senderId: number): void {
		if (!Number.isSafeInteger(senderId) || senderId < 1 || this.windows.has(senderId)) return;
		this.windows.set(senderId, { focused: false, online: false, focusSequence: 0, health: new Map(), expectations: new Map() });
	}

	destroyWindow(senderId: number): void {
		const window = this.windows.get(senderId);
		if (!window) return;
		this.windows.delete(senderId);
		if (this.owner === senderId) {
			this.cancelSuspectTimers();
			this.owner = null;
			this.reselectOwner();
		}
	}

	signal(senderId: number, raw: unknown): boolean {
		const window = this.windows.get(senderId);
		const parsed = parseAgentSwitchVisibilitySignal(raw);
		if (!window || !parsed || !this.enabled || parsed.consentGeneration !== this.generation) return false;
		const signal = parsed.signal;
		switch (signal.kind) {
			case "focus":
				window.focused = signal.value;
				if (signal.value) window.focusSequence = ++this.focusSequence;
				this.reselectOwner();
				break;
			case "online":
				window.online = signal.value;
				this.reselectOwner();
				break;
			case "transport": case "query":
				window.health.set(`${signal.kind}:${signal.operation}`, { active: signal.active, healthy: signal.healthy });
				if (signal.active && !signal.healthy) this.interruptRecovery(`${signal.kind}:${signal.operation}`);
				if (this.owner === senderId) this.reconcileHealth(signal.operation);
				break;
			case "expected_presentation": {
				const localKey = `presentation:${signal.localRouteKey}\u0000${signal.switchId}\u0000${signal.updatedAt}`;
				window.expectations.set(signal.token, { token: signal.token, localKey, presentationKind: signal.presentationKind, durableState: signal.durableState });
				this.interruptRecovery(localKey);
				if (this.owner === senderId) this.reconcilePresentation(localKey);
				break;
			}
			case "presented": this.finishExpectation(window, signal.token, true); break;
			case "cancel": this.finishExpectation(window, signal.token, false); break;
		}
		return true;
	}

	async disableAndDrain(): Promise<void> {
		this.enabled = false;
		this.cancelAll();
		const pending = this.abortDeliveries();
		await Promise.allSettled(pending);
	}

	async closeAndDrain(): Promise<void> {
		await this.disableAndDrain();
		this.windows.clear();
		this.owner = null;
	}

	private reselectOwner(): void {
		let selected: [number, WindowState] | undefined;
		for (const entry of this.windows) {
			if (!entry[1].focused || !entry[1].online) continue;
			if (!selected || entry[1].focusSequence > selected[1].focusSequence) selected = entry;
		}
		const next = selected?.[0] ?? null;
		if (next === this.owner) {
			this.reconcileOwner();
			return;
		}
		this.cancelSuspectTimers();
		this.owner = next;
		this.reconcileOwner();
	}

	private reconcileOwner(): void {
		if (!this.ownerEligible()) { this.cancelSuspectTimers(); return; }
		this.reconcileHealth("active"); this.reconcileHealth("history");
		const window = this.windows.get(this.owner!);
		for (const expectation of window?.expectations.values() ?? []) this.reconcilePresentation(expectation.localKey);
	}

	private reconcileHealth(operation: AgentSwitchVisibilityOperation): void {
		const window = this.owner === null ? undefined : this.windows.get(this.owner);
		if (!window || !this.ownerEligible()) return;
		const transport = window.health.get(`transport:${operation}`);
		const query = window.health.get(`query:${operation}`);
		const transportFailed = Boolean(transport?.active && !transport.healthy);
		this.setIncident(`transport:${operation}`, transportFailed, {
			failurePoint: "visibility_transport", operation,
			elapsedTimeBucket: operation === "active" ? "under_30s" : "under_2m",
		}, operation === "active" ? activeGraceMilliseconds : historyGraceMilliseconds);
		const queryFailed = Boolean(query?.active && !query.healthy) && !transportFailed;
		this.setIncident(`query:${operation}`, queryFailed, {
			failurePoint: "visibility_query", operation,
			elapsedTimeBucket: operation === "active" ? "under_30s" : "under_2m",
		}, operation === "active" ? activeGraceMilliseconds : historyGraceMilliseconds);
	}

	private reconcilePresentation(localKey: string): void {
		if (!this.ownerEligible() || this.owner === null) return;
		const window = this.windows.get(this.owner);
		const expectation = [...(window?.expectations.values() ?? [])].find((candidate) => candidate.localKey === localKey);
		if (!expectation) { this.recoverIncident(localKey); return; }
		this.setIncident(localKey, true, {
			failurePoint: "visibility_presentation", operation: expectation.presentationKind,
			presentationKind: expectation.presentationKind, durableState: expectation.durableState,
			elapsedTimeBucket: "under_5s",
		}, presentationGraceMilliseconds);
	}

	private finishExpectation(window: WindowState, token: string, presented: boolean): void {
		const expectation = window.expectations.get(token);
		if (!expectation) return;
		const ownerWindow = this.owner === null ? undefined : this.windows.get(this.owner);
		if (presented && window === ownerWindow) {
			for (const candidateWindow of this.windows.values()) {
				for (const [candidateToken, candidate] of candidateWindow.expectations) if (candidate.localKey === expectation.localKey) candidateWindow.expectations.delete(candidateToken);
			}
			this.recoverIncident(expectation.localKey);
			return;
		}
		window.expectations.delete(token);
		if (![...(ownerWindow?.expectations.values() ?? [])].some((candidate) => candidate.localKey === expectation.localKey)) {
			this.recoverIncident(expectation.localKey);
		}
	}

	private setIncident(key: string, failed: boolean, remote: AgentSwitchVisibilityIncident, grace: number): void {
		if (!failed) { this.recoverIncident(key); return; }
		const existing = this.incidents.get(key);
		if (existing?.healTimer) { clearTimeout(existing.healTimer); existing.healTimer = undefined; }
		if (existing?.state === "reported" || existing?.timer || !this.ownerEligible()) return;
		const incident: Incident = existing ?? { state: "suspect", remote };
		incident.state = "suspect"; incident.remote = remote;
		incident.timer = setTimeout(() => {
			incident.timer = undefined;
			if (!this.enabled || !this.ownerEligible() || !this.incidentStillFailed(key)) return;
			incident.state = "reported";
			const abort = new AbortController();
			const delivery = Promise.resolve(this.options.send({ ...incident.remote }, this.generation, abort.signal))
				.catch(() => { this.options.diagnostic?.("visibility_delivery_failed"); })
				.finally(() => this.deliveries.delete(abort));
			this.deliveries.set(abort, delivery);
		}, grace);
		this.incidents.set(key, incident);
	}

	private incidentStillFailed(key: string): boolean {
		if (this.owner === null) return false;
		const window = this.windows.get(this.owner);
		if (!window) return false;
		if (key.startsWith("presentation:")) return [...window.expectations.values()].some((candidate) => candidate.localKey === key);
		const [kind, operation] = key.split(":") as ["transport" | "query", AgentSwitchVisibilityOperation];
		const state = window.health.get(key);
		if (!state?.active || state.healthy) return false;
		if (kind === "query") {
			const transport = window.health.get(`transport:${operation}`);
			return !(transport?.active && !transport.healthy);
		}
		return true;
	}

	private recoverIncident(key: string): void {
		const incident = this.incidents.get(key);
		if (!incident) return;
		if (incident.timer) { clearTimeout(incident.timer); incident.timer = undefined; }
		if (incident.state === "suspect") { this.incidents.delete(key); return; }
		if (!incident.healTimer) incident.healTimer = setTimeout(() => this.incidents.delete(key), recurrenceRecoveryMilliseconds);
	}

	private interruptRecovery(key: string): void {
		const incident = this.incidents.get(key);
		if (incident?.healTimer) { clearTimeout(incident.healTimer); incident.healTimer = undefined; }
	}

	private ownerEligible(): boolean {
		if (!this.enabled || this.owner === null) return false;
		const window = this.windows.get(this.owner);
		return Boolean(window?.focused && window.online);
	}

	private cancelSuspectTimers(): void {
		for (const [key, incident] of this.incidents) {
			if (incident.timer) clearTimeout(incident.timer);
			if (incident.state === "suspect") this.incidents.delete(key);
			else incident.timer = undefined;
		}
	}

	private cancelAll(): void {
		for (const incident of this.incidents.values()) {
			if (incident.timer) clearTimeout(incident.timer);
			if (incident.healTimer) clearTimeout(incident.healTimer);
		}
		this.incidents.clear();
		for (const window of this.windows.values()) { window.health.clear(); window.expectations.clear(); }
	}

	private abortDeliveries(): Promise<void>[] {
		const pending = [...this.deliveries.values()];
		for (const abort of this.deliveries.keys()) abort.abort();
		return pending;
	}
}

type FetchLike = (input: string, init: RequestInit) => Promise<{ ok: boolean; status: number; body?: { cancel(): void | Promise<void> } | null }>;

export function createAgentSwitchVisibilitySender(options: {
	dsn: string;
	production: boolean;
	fetch: FetchLike;
	metadata: AgentSwitchVisibilityMetadata;
	now?: () => Date;
	eventId?: () => string;
}): (incident: AgentSwitchVisibilityIncident, consentGeneration: string, signal: AbortSignal) => Promise<void> {
	const destination = parseAgentSwitchDSN(options.dsn, options.production);
	return async (incident, _consentGeneration, cancellation) => {
		const eventId = options.eventId?.() ?? randomBytes(16).toString("hex");
		const event = buildVisibilityEvent({ ...incident, eventId, occurredAt: (options.now?.() ?? new Date()).toISOString() }, options.metadata);
		const envelope = encodeAgentSwitchEnvelopeV1(eventId, event);
		const body = new ArrayBuffer(envelope.byteLength);
		new Uint8Array(body).set(envelope);
		const abort = new AbortController();
		const cancel = () => abort.abort();
		cancellation.addEventListener("abort", cancel, { once: true });
		const timer = setTimeout(() => abort.abort(), 5_000);
		try {
			const response = await options.fetch(destination.endpoint, {
				method: "POST", body, signal: abort.signal,
				headers: { "Content-Type": "application/x-sentry-envelope", "X-Sentry-Auth": `Sentry sentry_version=7, sentry_key=${destination.publicKey}, sentry_client=ao-agent-switch/1` },
				redirect: "manual", credentials: "omit", cache: "no-store", referrerPolicy: "no-referrer",
			});
			await response.body?.cancel();
			if (response.status < 200 || response.status >= 300) throw new Error("visibility delivery was not accepted");
		} finally { clearTimeout(timer); cancellation.removeEventListener("abort", cancel); }
	};
}
