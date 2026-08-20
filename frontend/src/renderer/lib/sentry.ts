// Renderer-side Sentry sink. Fed from the existing telemetry seams
// (captureRendererException, reportApiError) so there is one capture path, not
// two. Everything here is a no-op until BOTH conditions hold:
//   1. telemetry consent is granted (the caller already gates on this), and
//   2. a DSN is configured via VITE_AO_SENTRY_DSN.
//
// Ship-time steps (no code change): `npm i @sentry/electron`, then set
// VITE_AO_SENTRY_DSN (renderer) / AO_SENTRY_DSN (main). Until then this file
// compiles and runs with no dependency on the SDK — the import is lazy and only
// happens once a DSN is present.

import { classifyError, type ClassifyInput, type Triage } from "../../shared/observability";

type SentryLike = {
	init: (opts: Record<string, unknown>) => void;
	captureException: (err: unknown, hint?: Record<string, unknown>) => void;
	captureMessage: (msg: string, hint?: Record<string, unknown>) => void;
	addBreadcrumb: (crumb: Record<string, unknown>) => void;
};

let sentry: SentryLike | null = null;
let initStarted = false;

function dsn(): string {
	try {
		return (import.meta as unknown as { env?: Record<string, string> }).env?.VITE_AO_SENTRY_DSN ?? "";
	} catch {
		return "";
	}
}

// Strip embedded local URLs/paths from any free text before it leaves the
// machine. Mirrors the redaction the PostHog path already applies; tags/contexts
// we attach are safe enums/ids, so this guards the message + stack strings.
const LOCAL_URL = /(?:\bfile:\/\/\/\S+|\bapp:\/\/renderer\/\S+|\bhttps?:\/\/(?:localhost|127\.0\.0\.1|\[::1\])(?::\d+)?\S*)/gi;
const HOME_PATH = /\/(?:Users|home)\/[^\s"']+/g;
function scrub(value: unknown): unknown {
	if (typeof value === "string") return value.replace(LOCAL_URL, "[redacted-url]").replace(HOME_PATH, "[redacted-path]");
	return value;
}

export type ObservabilityContext = {
	release?: string;
	channel?: string;
	platform?: string;
	distinctId?: string;
};

let ctx: ObservabilityContext = {};

/** Initialize once, after telemetry consent. No DSN → stays a no-op forever. */
export async function initSentry(context: ObservabilityContext): Promise<void> {
	ctx = context;
	if (initStarted || sentry) return;
	const d = dsn();
	if (!d) return; // not configured yet — nothing to do
	initStarted = true;
	try {
		// Runtime-computed specifier + @vite-ignore so the bundler does not try to
		// resolve @sentry/electron at build time — this file compiles and runs with
		// no such dependency present. SHIP STEP: `npm i @sentry/electron` and set the
		// DSN; then this import resolves (or convert it to a static import).
		const spec = ["@sentry", "electron", "renderer"].join("/");
		const mod = (await import(/* @vite-ignore */ spec)) as unknown as SentryLike;
		mod.init({
			dsn: d,
			release: context.release,
			environment: context.channel ?? "unknown",
			autoSessionTracking: false,
			// We never want content in events — enums/ids only via tags, and scrub free text.
			beforeSend: (event: Record<string, unknown>) => scrubEvent(event),
			beforeBreadcrumb: (crumb: Record<string, unknown> | null) => (crumb ? scrubEvent(crumb) : crumb),
		});
		sentry = mod;
	} catch {
		// SDK not installed yet, or init failed — remain a silent no-op.
		sentry = null;
	}
}

function scrubEvent(event: Record<string, unknown>): Record<string, unknown> {
	if (typeof event.message === "string") event.message = scrub(event.message) as string;
	const exception = event.exception as { values?: Array<{ value?: unknown }> } | undefined;
	for (const v of exception?.values ?? []) if (typeof v.value === "string") v.value = scrub(v.value);
	return event;
}

function tagsFor(meta: ClassifyInput & { operation?: string; surface?: string; domain?: string }, triage: Triage) {
	return {
		platform: ctx.platform ?? "desktop",
		surface: meta.surface,
		domain: meta.domain,
		operation: meta.operation,
		category: meta.category,
		code: meta.code,
		http_status: meta.httpStatus,
		apierr_kind: meta.kind,
		severity: triage.severity,
		owner: triage.owner,
	};
}

export type CaptureMeta = ClassifyInput & { operation?: string; surface?: string; domain?: string };

/** Capture an exception (from a boundary or unhandled handler). */
export function captureExceptionToSentry(error: unknown, meta: CaptureMeta = {}): void {
	if (!sentry) return;
	const triage = classifyError(meta);
	const payload = { level: triage.level, tags: tagsFor(meta, triage) };
	if (triage.report) sentry.captureException(error, payload);
	else sentry.addBreadcrumb({ category: "handled", level: triage.level, message: String((error as Error)?.message ?? error), data: payload.tags });
}

/** Capture a classified API error (from reportApiError). */
export function captureApiErrorToSentry(
	operation: string,
	category: string,
	status?: number,
	code?: string,
): void {
	if (!sentry) return;
	const meta: CaptureMeta = { operation, category, httpStatus: status, code, domain: operation.split(".")[0] };
	const triage = classifyError(meta);
	const tags = tagsFor(meta, triage);
	if (triage.report) sentry.captureMessage(`${operation} failed: ${category}${status ? ` (${status})` : ""}`, { level: triage.level, tags });
	else sentry.addBreadcrumb({ category: "api", level: triage.level, message: `${operation} ${category}`, data: tags });
}
