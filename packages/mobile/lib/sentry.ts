// Mobile Sentry sink (@sentry/react-native), mirroring the desktop renderer sink
// so both classify errors identically. No-op until BOTH hold:
//   1. a DSN is configured via EXPO_PUBLIC_SENTRY_DSN, and
//   2. the @sentry/react-native SDK is installed.
//
// The SDK is intentionally NOT added to package.json yet: @sentry/react-native
// is a native module, so installing it correctly means `npx expo install
// @sentry/react-native` (to pick the SDK-54-compatible version) plus a prebuild
// cycle to regenerate the committed native projects, and the config plugin for
// source maps. Adding it without that would leave the committed ios/ prebuild
// inconsistent. Ship steps, in order:
//   1. `npx expo install @sentry/react-native`  (do not hand-pin the version),
//      then point loadSdk() below at the real import
//   2. add the @sentry/react-native Expo config plugin, then `npx expo prebuild`
//   3. wire source-map upload at EAS build time AND on every `eas update`
//      (the Sentry Expo plugin does both), and set `dist` to the running
//      expo-updates id so an OTA bundle's maps resolve
//   4. set EXPO_PUBLIC_SENTRY_DSN and update the store Data-Safety / App-Privacy
//      declarations (a second processor gates the mobile release)
// Until then this file compiles and runs as a no-op with no native dependency.

import { classifyError, type ClassifyInput, type Triage } from "./observability";
import { DEFAULT_SENTRY_DSN } from "./sentry-config";

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
		return process.env.EXPO_PUBLIC_SENTRY_DSN?.trim() || DEFAULT_SENTRY_DSN;
	} catch {
		return DEFAULT_SENTRY_DSN;
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

export type MobileObservabilityContext = { release?: string; distinctId?: string; daemonVersion?: string };
let ctx: MobileObservabilityContext = {};

// A nightly/edge/pr release is not stable; keep those out of stable release
// health. release on mobile is version@build.
function channelOf(release: string): string {
	const v = release.toLowerCase();
	if (v.includes("nightly")) return "nightly";
	if (v.includes("edge") || v.includes("pr")) return "development";
	return "stable";
}

/**
 * The mobile app talks to the user's own daemon over Tailscale, which can be
 * older than the app. A mobile issue is uninterpretable without knowing which
 * daemon produced the failure, so it rides every event as a tag. Set it once
 * the paired daemon's version is known (desktop bundles its daemon, so this is
 * mobile-only).
 */
export function setMobileDaemonVersion(version: string): void {
	ctx = { ...ctx, daemonVersion: version };
}

// Stub until @sentry/react-native is installed (ship steps above). Not a
// computed import(): Metro rejects those in app code at bundle time.
async function loadSdk(): Promise<SentryLike | null> {
	return null;
}

/** Initialize once. No DSN or no SDK → stays a no-op forever. */
export async function initMobileSentry(context: MobileObservabilityContext = {}): Promise<void> {
	ctx = context;
	if (initStarted || sentry) return;
	const d = dsn();
	if (!d) return;
	initStarted = true;
	try {
		const mod = await loadSdk();
		if (!mod) return;
		mod.init({
			dsn: d,
			release: context.release,
			environment: channelOf(context.release ?? ""),
			enableAutoSessionTracking: false,
			// No PII (no device name, no IP-derived fields we can avoid).
			sendDefaultPii: false,
			// Deny-by-default. @sentry/react-native auto-captures fetch/XHR
			// breadcrumbs with FULL URLs — on mobile that is the user's Tailscale
			// host, exactly what must never leave the device. Cap breadcrumbs to
			// zero (none retained or sent) and turn off auto performance/HTTP
			// tracing rather than relying on scrubbing. Events reach Sentry only
			// through our capture seams with safe enum/id tags.
			maxBreadcrumbs: 0,
			enableAutoPerformanceTracing: false,
			tracesSampleRate: 0,
			beforeSend: (event: Record<string, unknown>) => scrubEvent(event),
			beforeBreadcrumb: (crumb: Record<string, unknown> | null) => (crumb ? scrubEvent(crumb) : crumb),
		});
		sentry = mod;
	} catch {
		sentry = null;
	}
}

type CaptureMeta = ClassifyInput & { operation?: string; surface?: string; domain?: string; requestId?: string };

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
		// Correlates with the daemon's own request_id tag on the matching capture.
		request_id: meta.requestId,
		severity: triage.severity,
		owner: triage.owner,
		daemon_version: ctx.daemonVersion,
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
export function captureMobileApiError(
	path: string,
	category: string,
	status?: number,
	code?: string,
	requestId?: string,
): void {
	if (!sentry) return;
	const template = normalizePath(path.split("?")[0]);
	// domain = first meaningful path segment (…/api/v1/<domain>/…)
	const parts = template.split("/").filter(Boolean);
	const domain = parts.includes("v1") ? parts[parts.indexOf("v1") + 1] : parts[0];
	const meta: CaptureMeta = { operation: template, domain, category, httpStatus: status, code, requestId };
	const triage = classifyError(meta);
	const tags = tagsFor(meta, triage);
	if (triage.report) sentry.captureMessage(`${meta.operation} failed: ${category}${status ? ` (${status})` : ""}`, { level: triage.level, tags });
	else sentry.addBreadcrumb({ category: "api", level: triage.level, message: `${meta.operation} ${category}`, data: tags });
}

/** Map an HTTP status to a transport category. */
export function httpCategory(status: number): string {
	return status >= 500 ? "http_5xx" : "http_4xx";
}
