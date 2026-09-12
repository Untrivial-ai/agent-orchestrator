import createClient from "openapi-fetch";
import type { components, paths } from "../../api/schema";
import type { DaemonStatus } from "../../shared/daemon-status";
import { daemonFailureMessage } from "./daemon-failure";
import { captureRendererEvent } from "./telemetry";
import { captureApiErrorToSentry } from "./sentry";

function devApiBaseUrl(): string {
	return typeof window === "undefined" ? "http://127.0.0.1:3001" : window.location.origin;
}

const explicitApiBaseUrl = import.meta.env.VITE_AO_API_BASE_URL;
const initialApiBaseUrl = explicitApiBaseUrl ?? (import.meta.env.DEV ? devApiBaseUrl() : "http://127.0.0.1:3001");

let runtimeApiBaseUrl: string | null = explicitApiBaseUrl ?? null;
let daemonStatus: DaemonStatus = { state: "stopped" };

const baseUrlListeners = new Set<() => void>();

export function getApiBaseUrl(): string {
	return runtimeApiBaseUrl ?? "";
}

export function hasTrustedApiBaseUrl(): boolean {
	return runtimeApiBaseUrl !== null;
}

/**
 * Subscribe to base-URL changes (useSyncExternalStore-compatible). Long-lived
 * connections bound to a specific port — the terminal mux WebSocket, the SSE
 * stream — use this to rebind when the daemon comes back on a different port.
 */
export function subscribeApiBaseUrl(listener: () => void): () => void {
	baseUrlListeners.add(listener);
	return () => {
		baseUrlListeners.delete(listener);
	};
}

export function setApiBaseUrl(nextBaseUrl: string | null): void {
	const normalized = (nextBaseUrl ?? explicitApiBaseUrl ?? null)?.replace(/\/+$/, "") ?? null;
	if (normalized === runtimeApiBaseUrl) return;
	runtimeApiBaseUrl = normalized;
	baseUrlListeners.forEach((listener) => listener());
}

// The renderer records every supervisor status here so API requests made while
// no daemon URL is trusted can return the actual startup failure, not a generic
// availability message.
export function setApiDaemonStatus(nextStatus: DaemonStatus): void {
	daemonStatus = nextStatus;
}

// Route templates from the generated OpenAPI schema (frontend/src/api/schema.ts).
// Operation strings sent to telemetry must never contain raw IDs (project IDs
// are user-chosen strings), so we match each request path against these
// templates and report the template — collapsing `{param}` to `:id` — rather
// than guessing which segments are identifiers. Matching from the schema keeps
// static child routes (notifications/read-all, sessions/cleanup) intact and
// still normalizes IDs for every resource, including ones a segment heuristic
// would miss (orchestrators/{id}). Keep in sync with schema.ts.
const ROUTE_TEMPLATES = [
	"/api/v1/agents",
	"/api/v1/agents/install-jobs",
	"/api/v1/agents/auth-plans",
	"/api/v1/agents/installers",
	"/api/v1/agents/refresh",
	"/api/v1/agents/readiness",
	"/api/v1/agents/readiness/ensure",
	"/api/v1/agents/{agent}/auth",
	"/api/v1/agents/{agent}/install",
	"/api/v1/agents/codex/accounts",
	"/api/v1/agents/codex/accounts/{accountId}",
	"/api/v1/agents/codex/accounts/ensure",
	"/api/v1/agents/codex/accounts/{accountId}/login-terminal",
	"/api/v1/agents/codex/accounts/{accountId}/logout",
	"/api/v1/agents/codex/accounts/{accountId}/reset-credit/consume",
	"/api/v1/agents/codex/accounts/events",
	"/api/v1/agents/codex/accounts/login-terminal",
	"/api/v1/agents/codex/accounts/login-operations/{operationId}/verify",
	"/api/v1/agents/codex/accounts/login-operations/{operationId}/cancel",
	"/api/v1/agents/codex/account-switches",
	"/api/v1/agents/codex/account-switches/{switchId}/recover",
	"/api/v1/agents/{agent}/models",
	"/api/v1/agents/{agent}/models/refresh",
	"/api/v1/agents/{agent}/probe",
	"/api/v1/agents/{agent}/verify",
	"/api/v1/desktop/sessions/{sessionId}/workspace",
	"/api/v1/events",
	"/api/v1/import",
	"/api/v1/imports/prepare-git",
	"/api/v1/imports/validate",
	"/api/v1/notifications",
	"/api/v1/notifications/{id}",
	"/api/v1/notifications/read-all",
	"/api/v1/notifications/stream",
	"/api/v1/orchestrators",
	"/api/v1/orchestrators/{id}",
	"/api/v1/projects",
"/api/v1/projects/clone",
	"/api/v1/projects/clone/prepare",
	"/api/v1/projects/clone/cleanup",
	"/api/v1/projects/initialize",
	"/api/v1/projects/{id}",
	"/api/v1/projects/{id}/config",
	"/api/v1/prs/{id}/merge",
	"/api/v1/prs/{id}/resolve-comments",
	"/api/v1/sessions",
	"/api/v1/sessions/{sessionId}",
	"/api/v1/sessions/{sessionId}/activity",
	"/api/v1/sessions/{sessionId}/agent-switches",
	"/api/v1/sessions/{sessionId}/agent-switches/{switchId}/handoff",
	"/api/v1/sessions/{sessionId}/agent-switches/{switchId}/recover",
	"/api/v1/sessions/{sessionId}/exit-agent",
	"/api/v1/sessions/{sessionId}/interface-transition",
	"/api/v1/sessions/{sessionId}/kill",
	"/api/v1/sessions/{sessionId}/pr",
	"/api/v1/sessions/{sessionId}/pr/claim",
	"/api/v1/sessions/{sessionId}/preview",
	"/api/v1/sessions/{sessionId}/preview/files/*",
	"/api/v1/sessions/{sessionId}/preview/server",
	"/api/v1/sessions/{sessionId}/resume-agent",
	"/api/v1/sessions/{sessionId}/restore",
	"/api/v1/sessions/{sessionId}/switch-agent",
	"/api/v1/sessions/{sessionId}/reviews",
	"/api/v1/sessions/{sessionId}/reviews/cancel",
	"/api/v1/sessions/{sessionId}/reviews/comments/resolve",
	"/api/v1/sessions/{sessionId}/reviews/submit",
	"/api/v1/sessions/{sessionId}/reviews/trigger",
	"/api/v1/sessions/{sessionId}/rollback",
	"/api/v1/sessions/{sessionId}/send",
	"/api/v1/sessions/{sessionId}/workspace/events",
	"/api/v1/sessions/{sessionId}/workspace/file",
	"/api/v1/sessions/{sessionId}/workspace/files",
	"/api/v1/sessions/cleanup",
] as const;

// Resource collections whose next path segment is an identifier. Only used as a
// defensive fallback for paths not covered by ROUTE_TEMPLATES; keeps IDs out of
// telemetry for known collections even if a route is ever missed above.
const RESOURCE_SEGMENTS = new Set([
	"agents",
	"projects",
	"sessions",
	"notifications",
	"workspaces",
	"prs",
	"orchestrators",
]);

// Match a path against one template. `{param}` matches any single segment
// (reported as `:id`), a trailing `*` matches the remaining path, and every
// other segment must match literally. Returns the normalized template plus a
// score = number of literal segments matched, so the most specific template
// wins when several match (e.g. `read-all` beats `{id}`).
function matchRouteTemplate(pathname: string, template: string): { normalized: string; score: number } | null {
	const pathSegs = pathname.split("/");
	const tmplSegs = template.split("/");
	const out: string[] = [];
	let score = 0;
	for (let i = 0; i < tmplSegs.length; i += 1) {
		const t = tmplSegs[i];
		if (t === "*") {
			out.push("*");
			return { normalized: out.join("/"), score };
		}
		const p = pathSegs[i];
		if (p === undefined) return null;
		if (t.startsWith("{") && t.endsWith("}")) {
			out.push(":id");
		} else if (t === p) {
			out.push(t);
			score += 1;
		} else {
			return null;
		}
	}
	if (pathSegs.length !== tmplSegs.length) return null;
	return { normalized: out.join("/"), score };
}

function fallbackNormalize(pathname: string): string {
	const segments = pathname.split("/");
	for (let i = 0; i < segments.length - 1; i += 1) {
		if (RESOURCE_SEGMENTS.has(segments[i]) && segments[i + 1]) {
			segments[i + 1] = ":id";
			i += 1;
		}
	}
	return segments.join("/");
}

export function normalizeApiOperation(method: string, pathname: string): string {
	let best: { normalized: string; score: number } | null = null;
	for (const template of ROUTE_TEMPLATES) {
		const match = matchRouteTemplate(pathname, template);
		if (match && (best === null || match.score > best.score)) best = match;
	}
	return `${method.toUpperCase()} ${best?.normalized ?? fallbackNormalize(pathname)}`;
}

type ApiErrorCategory = "daemon_unavailable" | "network_error" | "http_4xx" | "http_5xx";
type ReportingOwner = NonNullable<components["schemas"]["APIError"]["reporting_owner"]>;

// One event per (operation, category, status) per window: a daemon outage
// makes every polling query fail at once and on every retry — the failure
// signal matters, the storm does not.
const API_ERROR_DEDUPE_MS = 30_000;
const lastApiErrorAt = new Map<string, number>();

function reportApiError(
	operation: string,
	category: ApiErrorCategory,
	status?: number,
	code?: string,
	requestId?: string,
	reportingOwner?: ReportingOwner,
): void {
	// Treat an omitted owner as HTTP-owned for dedupe purposes. Saga-owned
	// responses need their own bucket so suppressing one cannot hide a later
	// generic HTTP failure from Sentry.
	const ownerBucket = reportingOwner === "agent_switch_saga" ? "agent_switch_saga" : "http";
	const key = `${operation}|${category}|${status ?? ""}|${ownerBucket}`;
	const now = Date.now();
	const last = lastApiErrorAt.get(key);
	if (last !== undefined && now - last < API_ERROR_DEDUPE_MS) return;
	lastApiErrorAt.set(key, now);
	void captureRendererEvent("ao.renderer.api_error", {
		operation,
		error_category: category,
		status,
	});
	// Mirror into Sentry (no-op unless a DSN is configured). The daemon `code`
	// is what drives the fine-grained severity/owner classification; `requestId`
	// (when present) is tagged so a client event pivots to the daemon's own
	// capture of the same request, which carries the matching request_id.
	if (reportingOwner !== "agent_switch_saga") {
		captureApiErrorToSentry(operation, category, status, code, requestId);
	}
}

async function runtimeFetch(input: Request): Promise<Response> {
	const operation = normalizeApiOperation(input.method, new URL(input.url).pathname);
	const visibilityOwned = operation === "GET /api/v1/projects" || operation === "GET /api/v1/sessions" || operation === "GET /api/v1/sessions/:id/agent-switches";
	const baseUrl = runtimeApiBaseUrl;
	if (baseUrl === null) {
		if (!visibilityOwned) reportApiError(operation, "daemon_unavailable", 503);
		return new Response(JSON.stringify({ message: daemonFailureMessage(daemonStatus), code: daemonStatus.code }), {
			status: 503,
			headers: { "Content-Type": "application/json" },
		});
	}

	const send = async (): Promise<Response> => {
		if (!baseUrl) {
			return fetch(input);
		}

		const url = new URL(input.url);
		const target = new URL(url.pathname + url.search + url.hash, baseUrl);
		if (target.href === input.url) {
			return fetch(input);
		}

		// Rebase onto the runtime base URL by copying fields explicitly and
		// buffering the body. `new Request(target, input)` reads the source
		// request's `duplex` getter, which Electron's Chromium lacks — it throws
		// "The duplex member must be specified" for any request with a body, so
		// every POST would fail in the packaged app. API bodies are small JSON;
		// buffering sidesteps streaming-duplex semantics entirely.
		const body = input.method === "GET" || input.method === "HEAD" ? undefined : await input.arrayBuffer();
		return fetch(target, {
			method: input.method,
			headers: input.headers,
			body,
			signal: input.signal,
			credentials: input.credentials,
			cache: input.cache,
			redirect: input.redirect,
			referrerPolicy: input.referrerPolicy,
			integrity: input.integrity,
			keepalive: input.keepalive,
		});
	};

	let response: Response;
	try {
		response = await send();
	} catch (error) {
		// Caller-initiated aborts (unmounted components cancelling queries) are not failures.
		if (!(error instanceof DOMException && error.name === "AbortError")) {
			if (!visibilityOwned) reportApiError(operation, "network_error");
		}
		throw error;
	}
	if (!response.ok) {
		// Best-effort read the daemon error envelope's `code` (via a clone so the
		// caller still gets an unconsumed body) to drive classification.
		let code: string | undefined;
		let requestId: string | undefined;
		let reportingOwner: ReportingOwner | undefined;
		try {
			const body = (await response.clone().json()) as Partial<components["schemas"]["APIError"]>;
			if (typeof body?.code === "string" && body.code !== "") code = body.code;
			if (typeof body?.requestId === "string" && body.requestId !== "") requestId = body.requestId;
			if (body?.reporting_owner === "http" || body?.reporting_owner === "agent_switch_saga") {
				reportingOwner = body.reporting_owner;
			}
		} catch {
			// Non-JSON or empty body: fall back to status-only classification.
		}
		if (!visibilityOwned) reportApiError(
			operation,
			response.status >= 500 ? "http_5xx" : "http_4xx",
			response.status,
			code,
			requestId,
			reportingOwner,
		);
	}
	return response;
}

export const apiClient = createClient<paths>({
	baseUrl: initialApiBaseUrl,
	fetch: runtimeFetch,
});

/**
 * Human-readable message from an openapi-fetch `error` value. The daemon's
 * error body is `{ error, code, message, requestId }` (backend apierr) — a
 * plain object, so `String(error)` renders "[object Object]". Falls back
 * through Error instances and strings.
 */
export function apiErrorCode(error: unknown): string | undefined {
	if (typeof error === "object" && error !== null) {
		const body = error as { code?: unknown };
		if (typeof body.code === "string" && body.code !== "") return body.code;
	}
	return undefined;
}

/** Structured recovery metadata from the daemon's stable error envelope. */
export function apiErrorDetails(error: unknown): Record<string, unknown> | undefined {
	if (typeof error !== "object" || error === null) return undefined;
	const details = (error as { details?: unknown }).details;
	return typeof details === "object" && details !== null && !Array.isArray(details)
		? (details as Record<string, unknown>)
		: undefined;
}

/** Correlation id from the daemon's stable error envelope. */
export function apiErrorRequestId(error: unknown): string | undefined {
	if (typeof error === "object" && error !== null) {
		const body = error as { requestId?: unknown };
		if (typeof body.requestId === "string" && body.requestId !== "") return body.requestId;
	}
	return undefined;
}

export function apiErrorMessage(error: unknown, fallback = "Request failed"): string {
	if (error instanceof Error) return error.message;
	if (typeof error === "string" && error !== "") return error;
	if (typeof error === "object" && error !== null) {
		const body = error as { code?: unknown; message?: unknown; error?: unknown };
		if (typeof body.error === "object" && body.error !== null) {
			return apiErrorMessage(body.error, fallback);
		}
		if (typeof body.message === "string" && body.message !== "") {
			return body.message;
		}
		if (typeof body.error === "string" && body.error !== "") return body.error;
	}
	return fallback;
}
