export const AGENT_SWITCH_VISIBILITY_IPC_CHANNEL = "agent-switch:visibility";
export const AGENT_SWITCH_CANONICAL_EVENT_MAX_BYTES = 60 << 10;
export const AGENT_SWITCH_ENVELOPE_MAX_BYTES = 64 << 10;

export type AgentSwitchVisibilityOperation = "active" | "history";
export type AgentSwitchPresentationKind = "terminal_failure" | "recovery_required";
export type AgentSwitchDurableState = "failed" | "stopping_source" | "source_stopped" | "starting_target";

export type AgentSwitchVisibilitySignalBody =
	| { kind: "transport" | "query"; operation: AgentSwitchVisibilityOperation; healthy: boolean; active: boolean }
	| { kind: "expected_presentation"; token: string; switchId: string; updatedAt: string; localRouteKey: string; presentationKind: AgentSwitchPresentationKind; durableState: AgentSwitchDurableState }
	| { kind: "presented" | "cancel"; token: string }
	| { kind: "focus" | "online"; value: boolean };

export type AgentSwitchVisibilitySignal = {
	consentGeneration: string;
	signal: AgentSwitchVisibilitySignalBody;
};

export type AgentSwitchVisibilityFailurePoint = "visibility_transport" | "visibility_query" | "visibility_presentation";
export type AgentSwitchVisibilityIncident = {
	failurePoint: AgentSwitchVisibilityFailurePoint;
	operation: AgentSwitchVisibilityOperation | AgentSwitchPresentationKind;
	presentationKind?: AgentSwitchPresentationKind;
	durableState?: AgentSwitchDurableState;
	elapsedTimeBucket: "under_5s" | "under_30s" | "under_2m";
};

export type AgentSwitchVisibilityMetadata = {
	release: string;
	environment: "stable" | "nightly" | "development";
	channel: "stable" | "nightly" | "preview";
	os: "darwin" | "linux" | "windows";
};

const eventIdPattern = /^[0-9a-f]{32}$/;
const tokenPattern = /^[A-Za-z0-9][A-Za-z0-9._+:-]{0,255}$/;
const releasePattern = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$/;

export function parseAgentSwitchVisibilitySignal(value: unknown): AgentSwitchVisibilitySignal | null {
	if (!isRecord(value) || !exactKeys(value, ["consentGeneration", "signal"]) || !boundedToken(value.consentGeneration)) return null;
	const signal = value.signal;
	if (!isRecord(signal) || typeof signal.kind !== "string") return null;
	switch (signal.kind) {
		case "transport": case "query":
			if (!exactKeys(signal, ["kind", "operation", "healthy", "active"]) || !isOperation(signal.operation) || typeof signal.healthy !== "boolean" || typeof signal.active !== "boolean") return null;
			break;
		case "expected_presentation":
			if (!exactKeys(signal, ["kind", "token", "switchId", "updatedAt", "localRouteKey", "presentationKind", "durableState"]) ||
				!boundedToken(signal.token) || !boundedLocal(signal.switchId) || !boundedLocal(signal.updatedAt) || !boundedLocal(signal.localRouteKey) ||
				!isPresentationKind(signal.presentationKind) || !isDurableState(signal.durableState)) return null;
			break;
		case "presented": case "cancel":
			if (!exactKeys(signal, ["kind", "token"]) || !boundedToken(signal.token)) return null;
			break;
		case "focus": case "online":
			if (!exactKeys(signal, ["kind", "value"]) || typeof signal.value !== "boolean") return null;
			break;
		default: return null;
	}
	return value as AgentSwitchVisibilitySignal;
}

export function buildVisibilityEvent(input: AgentSwitchVisibilityIncident & { eventId: string; occurredAt: string }, metadata: AgentSwitchVisibilityMetadata): Uint8Array {
	if (!eventIdPattern.test(input.eventId)) throw new Error("invalid agent switch event ID");
	validateMetadata(metadata);
	const point = input.failurePoint;
	const event = {
		event_id: input.eventId,
		timestamp: input.occurredAt,
		message: `Agent switch failed: not_applicable / ${input.durableState ?? "not_applicable"} / ${point}`,
		level: "warning",
		platform: "renderer",
		environment: metadata.environment,
		release: metadata.release,
		exception: { values: [{ type: "AgentSwitchFailure", value: `agent switch failure: not_applicable at ${point}`, stacktrace: { frames: [] } }] },
		fingerprint: ["agent-switch-visibility", "v1", point, input.operation],
		tags: {
			feature: "agent_switching", platform: "renderer", report_kind: "visibility_failure", subsystem: "visibility",
			mode: "not_applicable", phase: input.durableState ?? "not_applicable", failure_point: point,
			error_code: "not_applicable", fault_code: "not_applicable", execution: "live",
			from_harness: "not_applicable", target_harness: "not_applicable", target_start_mode: "not_applicable",
			runtime_backend: "not_applicable", call_outcome: "timed_out", ownership: "not_applicable",
			compensation: "not_applicable", user_impact: "visibility_impaired", release: metadata.release,
			classifier_callsite: "electron_main.agent_switch_visibility", channel: metadata.channel, os: metadata.os,
			visibility_failure_type: point, frontend_operation: input.operation,
			presentation_kind: input.presentationKind ?? "not_applicable", durable_state: input.durableState ?? "not_applicable",
		},
		contexts: { agent_switch: { source_stop_confirmed: "not_applicable", target_owner_committed: "not_applicable", gate_retained: "not_applicable", elapsed_time_bucket: input.elapsedTimeBucket } },
	};
	const bytes = new TextEncoder().encode(JSON.stringify(event));
	if (bytes.length > AGENT_SWITCH_CANONICAL_EVENT_MAX_BYTES) throw new Error("canonical agent switch event exceeds 60 KiB");
	return bytes;
}

export function encodeAgentSwitchEnvelopeV1(eventId: string, canonicalEvent: Uint8Array): Uint8Array {
	if (!eventIdPattern.test(eventId)) throw new Error("invalid agent switch event ID");
	if (canonicalEvent.length > AGENT_SWITCH_CANONICAL_EVENT_MAX_BYTES) throw new Error("canonical agent switch event exceeds 60 KiB");
	const decoded = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(canonicalEvent)) as unknown;
	if (!isRecord(decoded) || decoded.event_id !== eventId) throw new Error("agent switch EventID mismatch");
	const prefix = new TextEncoder().encode(`{"event_id":"${eventId}"}\n{"type":"event","length":${canonicalEvent.length}}\n`);
	if (prefix.length + canonicalEvent.length > AGENT_SWITCH_ENVELOPE_MAX_BYTES) throw new Error("agent switch envelope exceeds 64 KiB");
	const envelope = new Uint8Array(prefix.length + canonicalEvent.length);
	envelope.set(prefix); envelope.set(canonicalEvent, prefix.length);
	return envelope;
}

export type AgentSwitchDestination = { endpoint: string; publicKey: string; projectId: string };

export function parseAgentSwitchDSN(raw: string, production: boolean): AgentSwitchDestination {
	if (!raw || raw.length > 4 << 10 || raw.trim() !== raw || !/^[\x21-\x7e]+$/.test(raw) || /[?#]/.test(raw) || /%(?:2e|2f|5c)/i.test(raw)) throw new Error("invalid Sentry DSN");
	let parsed: URL;
	try { parsed = new URL(raw); } catch { throw new Error("invalid Sentry DSN"); }
	if ((parsed.protocol !== "https:" && parsed.protocol !== "http:") || (production && parsed.protocol !== "https:")) throw new Error("invalid Sentry DSN scheme");
	if (!parsed.username || parsed.password || !/^[A-Za-z0-9_-]{1,256}$/.test(parsed.username)) throw new Error("invalid Sentry DSN public key");
	if (!parsed.hostname || parsed.search || parsed.hash) throw new Error("invalid Sentry DSN");
	if (parsed.protocol === "http:" && !isLoopback(parsed.hostname)) throw new Error("development HTTP Sentry DSN must use loopback");
	const authorityStart = raw.indexOf("://") + 3;
	const pathStart = raw.indexOf("/", authorityStart);
	const rawPath = pathStart >= 0 ? raw.slice(pathStart).split(/[?#]/, 1)[0] : "";
	if (rawPath.includes("//") || rawPath.split("/").some((segment) => segment === "." || segment === "..")) throw new Error("invalid Sentry DSN path");
	const rawSegments = rawPath.split("/").slice(1);
	if (rawSegments.some((segment) => segment === "")) throw new Error("invalid Sentry DSN path");
	const segments = rawSegments.map((segment) => {
		let decoded: string;
		try { decoded = decodeURIComponent(segment); } catch { throw new Error("invalid Sentry DSN path escaping"); }
		if (!decoded || decoded === "." || decoded === ".." || decoded.includes("/") || decoded.includes("\\")) throw new Error("invalid Sentry DSN path escaping");
		return decoded;
	});
	const projectRaw = segments.pop();
	if (!projectRaw || !/^[0-9]{1,20}$/.test(projectRaw) || BigInt(projectRaw) === 0n) throw new Error("invalid Sentry DSN project ID");
	const projectId = BigInt(projectRaw).toString();
	const base = segments.length ? `/${segments.join("/")}` : "";
	const port = parsed.port ? `:${Number(parsed.port)}` : "";
	const endpoint = new URL(`${parsed.protocol}//${parsed.hostname.toLowerCase()}${port}`);
	endpoint.pathname = `${base}/api/${projectId}/envelope/`;
	return { endpoint: endpoint.toString(), publicKey: parsed.username, projectId };
}

function validateMetadata(metadata: AgentSwitchVisibilityMetadata): void {
	if (!releasePattern.test(metadata.release) || metadata.release.length > 96) throw new Error("invalid release");
	const prerelease = releasePattern.exec(metadata.release)?.[4] ?? "";
	if (prerelease.split(".").some((part) => /^0[0-9]+$/.test(part))) throw new Error("invalid release");
}
function isRecord(value: unknown): value is Record<string, unknown> { return typeof value === "object" && value !== null && !Array.isArray(value); }
function exactKeys(value: Record<string, unknown>, expected: string[]): boolean { const actual = Object.keys(value).sort(); return actual.length === expected.length && actual.every((key, index) => key === [...expected].sort()[index]); }
function boundedToken(value: unknown): value is string { return typeof value === "string" && tokenPattern.test(value); }
function boundedLocal(value: unknown): value is string { return typeof value === "string" && value.length > 0 && value.length <= 512; }
function isOperation(value: unknown): value is AgentSwitchVisibilityOperation { return value === "active" || value === "history"; }
function isPresentationKind(value: unknown): value is AgentSwitchPresentationKind { return value === "terminal_failure" || value === "recovery_required"; }
function isDurableState(value: unknown): value is AgentSwitchDurableState { return value === "failed" || value === "stopping_source" || value === "source_stopped" || value === "starting_target"; }
function isLoopback(host: string): boolean { return host === "localhost" || host === "127.0.0.1" || host === "[::1]" || host === "::1"; }
