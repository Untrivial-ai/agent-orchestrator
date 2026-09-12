import type {
	AgentSwitchVisibilityOperation,
	AgentSwitchVisibilitySignalBody,
} from "../../shared/agent-switch-observability";
import { aoBridge } from "./bridge";

type HealthKind = "transport" | "query";
type ExpectedPresentationSignal = Extract<AgentSwitchVisibilitySignalBody, { kind: "expected_presentation" }>;

export class RendererAgentSwitchVisibility {
	private readonly routeRefs = new Map<string, { operation: AgentSwitchVisibilityOperation; count: number }>();
	private readonly health: Record<HealthKind, Record<AgentSwitchVisibilityOperation, boolean>> = {
		transport: { active: true, history: true },
		query: { active: true, history: true },
	};
	private focused = false;
	private online = true;
	private readonly querySources: Record<AgentSwitchVisibilityOperation, Map<string, boolean>> = { active: new Map(), history: new Map() };

	constructor(private readonly send: (signal: AgentSwitchVisibilitySignalBody) => unknown) {}

	registerRoute(localRouteKey: string, operation: AgentSwitchVisibilityOperation): () => void {
		const key = `${operation}\u0000${localRouteKey}`;
		const current = this.routeRefs.get(key);
		this.routeRefs.set(key, { operation, count: (current?.count ?? 0) + 1 });
		if (!current) { this.publishHealth("active"); this.publishHealth("history"); }
		let disposed = false;
		return () => {
			if (disposed) return;
			disposed = true;
			const entry = this.routeRefs.get(key);
			if (!entry || entry.count <= 1) this.routeRefs.delete(key);
			else this.routeRefs.set(key, { ...entry, count: entry.count - 1 });
			this.publishHealth("active"); this.publishHealth("history");
		};
	}

	setTransportHealthy(operation: AgentSwitchVisibilityOperation, healthy: boolean): void {
		this.setHealth("transport", operation, healthy);
	}

	setQueryHealthy(operation: AgentSwitchVisibilityOperation, healthy: boolean, localSourceKey = "default"): void {
		this.querySources[operation].set(localSourceKey, healthy);
		this.setHealth("query", operation, [...this.querySources[operation].values()].every(Boolean));
	}

	clearQuerySource(localSourceKey: string): void {
		for (const operation of ["active", "history"] as const) {
			this.querySources[operation].delete(localSourceKey);
			this.setHealth("query", operation, [...this.querySources[operation].values()].every(Boolean));
		}
	}

	transportHealthy(operation: AgentSwitchVisibilityOperation): boolean { return this.health.transport[operation]; }

	setFocused(value: boolean): void { this.focused = value; this.send({ kind: "focus", value }); }
	setOnline(value: boolean): void { this.online = value; this.send({ kind: "online", value }); }
	replay(): void {
		this.send({ kind: "focus", value: this.focused });
		this.send({ kind: "online", value: this.online });
		this.publishHealth("active"); this.publishHealth("history");
	}
	expectPresentation(signal: Omit<ExpectedPresentationSignal, "kind">): void { this.send({ kind: "expected_presentation", ...signal }); }
	presented(token: string): void { this.send({ kind: "presented", token }); }
	cancel(token: string): void { this.send({ kind: "cancel", token }); }

	private setHealth(kind: HealthKind, operation: AgentSwitchVisibilityOperation, healthy: boolean): void {
		if (this.health[kind][operation] === healthy) return;
		this.health[kind][operation] = healthy;
		this.send({ kind, operation, healthy, active: this.operationActive(operation) });
	}

	private publishHealth(operation: AgentSwitchVisibilityOperation): void {
		const active = this.operationActive(operation);
		this.send({ kind: "transport", operation, healthy: this.health.transport[operation], active });
		this.send({ kind: "query", operation, healthy: this.health.query[operation], active });
	}

	private operationActive(operation: AgentSwitchVisibilityOperation): boolean {
		if (operation === "history") {
			for (const entry of this.routeRefs.values()) if (entry.operation === "active" && entry.count > 0) return false;
		}
		for (const entry of this.routeRefs.values()) if (entry.operation === operation && entry.count > 0) return true;
		return false;
	}
}

export const agentSwitchVisibility = new RendererAgentSwitchVisibility((signal) => aoBridge.telemetry?.signalAgentSwitchVisibility(signal) ?? false);
aoBridge.telemetry?.onPolicy(() => agentSwitchVisibility.replay());
void aoBridge.telemetry?.getPolicy().then(() => agentSwitchVisibility.replay()).catch(() => undefined);
