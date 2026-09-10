export type DaemonTelemetryPolicyAcknowledgement = {
	status: "applied";
	consentGeneration: string;
	eventsEnabled: boolean;
	gateDrained: boolean;
	purgeConfirmed: boolean;
};

type Fetcher = (input: string, init: RequestInit) => Promise<Response>;

export class DaemonTelemetryPolicyClient {
	constructor(private readonly origin: () => string | null, private readonly fetcher: Fetcher = fetch) {}

	prepareDisable(): Promise<DaemonTelemetryPolicyAcknowledgement> {
		return this.request("/internal/agent-switch-observability/prepare-disable", undefined).then((acknowledgement) => {
			if (acknowledgement.eventsEnabled || !acknowledgement.gateDrained || acknowledgement.purgeConfirmed) throw new Error("daemon prepare-disable acknowledgement lacks drain proof");
			return acknowledgement;
		});
	}

	async applyPolicy(consentGeneration: string, eventsEnabled: boolean): Promise<DaemonTelemetryPolicyAcknowledgement> {
		const acknowledgement = await this.request("/internal/agent-switch-observability/apply-policy", { consentGeneration, eventsEnabled }, consentGeneration);
		if (acknowledgement.eventsEnabled !== eventsEnabled) throw new Error("daemon telemetry acknowledgement policy mismatch");
		if (!eventsEnabled && (!acknowledgement.gateDrained || !acknowledgement.purgeConfirmed)) throw new Error("daemon telemetry acknowledgement lacks cleanup proof");
		return acknowledgement;
	}

	private async request(pathname: string, body?: object, expectedGeneration?: string): Promise<DaemonTelemetryPolicyAcknowledgement> {
		const base = this.origin();
		if (!base) throw new Error("daemon telemetry control is unavailable");
		const parsed = new URL(base);
		if (parsed.protocol !== "http:" || parsed.hostname !== "127.0.0.1" || parsed.username || parsed.password || parsed.pathname !== "/" || parsed.search || parsed.hash) {
			throw new Error("daemon telemetry control origin must be exact loopback HTTP");
		}
		const controller = new AbortController();
		const timer = setTimeout(() => controller.abort(), 2_000);
		try {
			const response = await this.fetcher(`${parsed.origin}${pathname}`, {
				method: "POST", signal: controller.signal,
				headers: body ? { "content-type": "application/json" } : undefined,
				body: body ? JSON.stringify(body) : undefined,
			});
			if (!response.ok) throw new Error(`daemon telemetry control returned HTTP ${response.status}`);
			const acknowledgement = parseAcknowledgement(await response.json());
			if (expectedGeneration && acknowledgement.consentGeneration !== expectedGeneration) throw new Error("daemon telemetry acknowledgement generation mismatch");
			return acknowledgement;
		} finally { clearTimeout(timer); }
	}
}

function parseAcknowledgement(value: unknown): DaemonTelemetryPolicyAcknowledgement {
	if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("invalid daemon telemetry acknowledgement");
	const record = value as Record<string, unknown>;
	const keys = Object.keys(record).sort();
	const expected = ["consentGeneration", "eventsEnabled", "gateDrained", "purgeConfirmed", "status"];
	if (keys.length !== expected.length || keys.some((key, index) => key !== expected[index]) || record.status !== "applied" || typeof record.consentGeneration !== "string" || typeof record.eventsEnabled !== "boolean" || typeof record.gateDrained !== "boolean" || typeof record.purgeConfirmed !== "boolean") {
		throw new Error("invalid daemon telemetry acknowledgement");
	}
	return record as DaemonTelemetryPolicyAcknowledgement;
}
