// Mobile Sentry sink (@sentry/react-native), mirroring the desktop renderer sink
// so both classify errors identically. No-op until BOTH hold:
//   1. a DSN is configured via EXPO_PUBLIC_SENTRY_DSN, and
//   2. the @sentry/react-native SDK is installed.
// Ship-time steps (no code change): `npx expo install @sentry/react-native`,
// set EXPO_PUBLIC_SENTRY_DSN, and add source-map upload for OTA builds.

import { classifyError, type ClassifyInput, type Triage } from "./observability";

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
		return process.env.EXPO_PUBLIC_SENTRY_DSN ?? "";
	} catch {
		return "";
	}
}

const LOCAL_URL = /(?:\bhttps?:\/\/(?:localhost|127\.0\.0\.1|\[::1\]|100\.\d+\.\d+\.\d+|[^\s/]+\.ts\.net)(?::\d+)?\S*)/gi;
const HOME_PATH = /\/(?:Users|home|data\/data)\/[^\s"']+/g;
function scrub(value: unknown): unknown {
	if (typeof value === "string") return value.replace(LOCAL_URL, "[redacted-url]").replace(HOME_PATH, "[redacted-path]");
	return value;
}
function scrubEvent(event: Record<string, unknown>): Record<string, unknown> {
	if (typeof event.message === "string") event.message = scrub(event.message) as string;
	const exception = event.exception as { values?: Array<{ value?: unknown }> } | undefined;
	for (const v of exception?.values ?? []) if (typeof v.value === "string") v.value = scrub(v.value);
	return event;
}

export type MobileObservabilityContext = { release?: string; distinctId?: string };
let ctx: MobileObservabilityContext = {};

/** Initialize once. No DSN → stays a no-op forever. */
export async function initMobileSentry(context: MobileObservabilityContext = {}): Promise<void> {
	ctx = context;
	if (initStarted || sentry) return;
	const d = dsn();
	if (!d) return;
	initStarted = true;
	try {
		// Dormant placeholder: Metro only bundles a dynamic import() with a static
		// string literal, so this computed specifier is intentionally NOT bundled
		// today (keeps the package dependency-free). SHIP STEP: install the SDK and
		// replace this line with `const mod = await import("@sentry/react-native")`.
		const spec = ["@sentry", "react-native"].join("/");
		const mod = (await import(spec)) as unknown as SentryLike;
		mod.init({
			dsn: d,
			release: context.release,
			enableAutoSessionTracking: false,
			beforeSend: (event: Record<string, unknown>) => scrubEvent(event),
			beforeBreadcrumb: (crumb: Record<string, unknown> | null) => (crumb ? scrubEvent(crumb) : crumb),
		});
		sentry = mod;
	} catch {
		sentry = null;
	}
}

type CaptureMeta = ClassifyInput & { operation?: string; surface?: string; domain?: string };

function tagsFor(meta: CaptureMeta, triage: Triage) {
	return {
		platform: "mobile",
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

// Collapse id-like path segments to :id so the operation is a low-cardinality
// route template, never a per-request value (no session ids in tags, no tag
// cardinality blow-up). Mirrors the renderer's normalizeApiOperation.
function normalizePath(path: string): string {
	return path
		.split("/")
		.map((seg) => (/^[0-9]+$/.test(seg) || /^[0-9a-fA-F-]{6,}$/.test(seg) ? ":id" : seg))
		.join("/");
}

export function captureMobileException(error: unknown, meta: CaptureMeta = {}): void {
	if (!sentry) return;
	const triage = classifyError(meta);
	const tags = tagsFor(meta, triage);
	if (triage.report) sentry.captureException(error, { level: triage.level, tags });
	else sentry.addBreadcrumb({ category: "handled", level: triage.level, message: String((error as Error)?.message ?? error), data: tags });
}

/** Capture a classified API failure from the mobile request helper. */
export function captureMobileApiError(path: string, category: string, status?: number, code?: string): void {
	if (!sentry) return;
	const template = normalizePath(path.split("?")[0]);
	// domain = first meaningful path segment (…/api/v1/<domain>/…)
	const parts = template.split("/").filter(Boolean);
	const domain = parts.includes("v1") ? parts[parts.indexOf("v1") + 1] : parts[0];
	const meta: CaptureMeta = { operation: template, domain, category, httpStatus: status, code };
	const triage = classifyError(meta);
	const tags = tagsFor(meta, triage);
	if (triage.report) sentry.captureMessage(`${meta.operation} failed: ${category}${status ? ` (${status})` : ""}`, { level: triage.level, tags });
	else sentry.addBreadcrumb({ category: "api", level: triage.level, message: `${meta.operation} ${category}`, data: tags });
}

/** Map an HTTP status to a transport category. */
export function httpCategory(status: number): string {
	return status >= 500 ? "http_5xx" : "http_4xx";
}
