import posthog from "posthog-js/dist/module.full.no-external";
import { aoBridge } from "./bridge";
import { isLoopbackHostname } from "./loopback";
import { ORCHESTRATOR_SPAWN_SOURCES } from "./orchestrator-spawn-sources";
import { DEFAULT_POSTHOG_HOST, DEFAULT_POSTHOG_PROJECT_KEY } from "../../shared/posthog-config";

const POSTHOG_KEY = import.meta.env.VITE_AO_POSTHOG_KEY?.trim() || DEFAULT_POSTHOG_PROJECT_KEY;
const POSTHOG_HOST = import.meta.env.VITE_AO_POSTHOG_HOST?.trim() || DEFAULT_POSTHOG_HOST;
const RELEASE_TAG = "2026-01-30";
const TELEMETRY_SCHEMA_VERSION = 2;
const REDACTED_LOCAL_URL = "[redacted-local-url]";
const REDACTED_LOCAL_PATH = "[redacted-local-path]";
const ACTIVE_STORAGE_KEY = "ao.telemetry.activeSlotsByDate";
const ROUTE_VIEW_STORAGE_KEY = "ao.telemetry.routeViewsByDate";
const EMBEDDED_LOCAL_URL_PATTERN =
	/(?:\bfile:\/\/\/\S+|\bapp:\/\/renderer\/\S+|\bhttps?:\/\/(?:localhost|127\.0\.0\.1|\[::1\])(?::\d+)?\S*)/gi;
const POSTHOG_EVENT_NAME_ALIASES: Record<string, string> = {
	"ao.app.active": "ao.v2.app.active",
	"ao.renderer.route_viewed": "ao.v2.renderer.route_viewed",
	"ao.renderer.loaded": "ao.v2.renderer.loaded",
	"ao.renderer.api_error": "ao.v2.renderer.api_error",
	"ao.renderer.daemon_failure": "ao.v2.renderer.daemon_failure",
};

let initPromise: Promise<boolean> | null = null;
let errorHandlersBound = false;
let telemetryContext: TelemetryProperties = {};
let fallbackActiveDate = "";
let fallbackRouteViewDate = "";
let fallbackRouteViewSurfaces = new Set<string>();

// Bounds how many captures of a single event (or exception) name reach
// PostHog. Mirrors the daemon-side RateLimitedSink: a re-render loop or
// repeatedly-thrown exception must not turn into an unbounded PostHog bill.
// A per-minute cap alone doesn't bound the daily total — a loop paced just
// under it would sit under the ceiling forever — so this pairs a small burst
// allowance with a hard daily ceiling per name.
const EVENTS_PER_NAME_PER_MINUTE = 5;
const EVENTS_PER_NAME_PER_DAY = 200;
const MINUTE_MS = 60_000;
const DAY_MS = 24 * 60 * 60_000;
const minuteWindows = new Map<string, { start: number; count: number }>();
const dayWindows = new Map<string, { start: number; count: number }>();

function reserveWindow(
	windows: Map<string, { start: number; count: number }>,
	name: string,
	now: number,
	size: number,
	limit: number,
): boolean {
	const window = windows.get(name);
	if (!window || now - window.start >= size) {
		windows.set(name, { start: now, count: 1 });
		return true;
	}
	if (window.count >= limit) return false;
	window.count += 1;
	return true;
}

export function reserveCapture(name: string, now = Date.now()): boolean {
	if (!reserveWindow(minuteWindows, name, now, MINUTE_MS, EVENTS_PER_NAME_PER_MINUTE)) return false;
	return reserveWindow(dayWindows, name, now, DAY_MS, EVENTS_PER_NAME_PER_DAY);
}

type TelemetryProperties = Record<string, unknown>;
type DailyActiveStorage = Pick<Storage, "getItem" | "setItem">;
type DailyActiveEventTarget = {
	addEventListener: (type: string, listener: EventListener, options?: AddEventListenerOptions) => void;
	removeEventListener: (type: string, listener: EventListener, options?: EventListenerOptions) => void;
};

export type DailyActiveHeartbeatOptions = {
	storage?: DailyActiveStorage;
	now?: () => Date;
	capture: () => boolean | void | Promise<boolean | void>;
	window: DailyActiveEventTarget;
	document: DailyActiveEventTarget & Pick<Document, "visibilityState">;
};

/**
 * Release channel, read from the user's own Updates setting.
 *
 * Previously this had to be inferred by looking for "-nightly." inside the
 * version string, which broke for anyone pinned to a feature build and told you
 * nothing about someone who had switched channels but not yet updated. The
 * setting is the truth; the version is a consequence of it.
 */
export type ReleaseChannel = "stable" | "nightly" | "feature" | "unknown";

export function releaseChannelFrom(settings: { channel?: unknown; feature?: unknown } | null | undefined): ReleaseChannel {
	if (!settings) return "unknown";
	if (settings.feature != null) return "feature";
	if (settings.channel === "nightly") return "nightly";
	if (settings.channel === "latest") return "stable";
	return "unknown";
}

/**
 * Channel of the build actually running, read from the version string.
 *
 * Distinct from release_channel, which is what the user opted into. A nightly
 * build carries "-nightly." in its version (CI stamps 0.11.3-nightly.5); a plain
 * semver is stable. This is "what am I running", release_channel is "what did I
 * choose". The two disagree exactly when someone switched channels but has not
 * updated yet, and that gap is the adoption-lag signal.
 */
export type VersionChannel = "stable" | "nightly" | "unknown";

export function versionChannelFrom(appVersion: string): VersionChannel {
	const v = appVersion.trim();
	if (!v || v === "unknown") return "unknown";
	return /-nightly\./i.test(v) ? "nightly" : "stable";
}

export function buildTelemetryContext(
	appVersion: string,
	platform: string,
	channel: ReleaseChannel = "unknown",
): TelemetryProperties {
	const version = appVersion.trim() || "unknown";
	return {
		// Classifies this install as the desktop app across every event, so a
		// shared event like ao.app.active splits cleanly by `client` alongside
		// the mobile app (client="mobile") and the CLI (client="cli"), rather
		// than by inferring from the platform value set.
		client: "desktop",
		app_version: version,
		ao_version: version,
		platform,
		// What they opted into.
		release_channel: channel,
		// What they are actually running.
		version_channel: versionChannelFrom(version),
		build_mode: import.meta.env.DEV ? "dev" : "packaged",
		telemetry_schema_version: TELEMETRY_SCHEMA_VERSION,
	};
}

/** Refreshes the channel in context after the user changes it, without a restart. */
export function setReleaseChannelContext(channel: ReleaseChannel): void {
	telemetryContext = { ...telemetryContext, release_channel: channel };
}

/**
 * Best-effort read of the Updates setting for the initial context.
 *
 * Never throws and never blocks startup: if the bridge is unavailable the
 * channel reports "unknown" rather than delaying the first heartbeat.
 */
async function readUpdateSettingsForTelemetry(): Promise<{ channel?: unknown; feature?: unknown } | null> {
	try {
		return (await aoBridge.updateSettings.get()) as { channel?: unknown; feature?: unknown };
	} catch {
		return null;
	}
}

export function withTelemetryContext(properties: TelemetryProperties): TelemetryProperties {
	return { ...telemetryContext, ...properties, $process_person_profile: false };
}

export function postHogEventName(event: string): string {
	return POSTHOG_EVENT_NAME_ALIASES[event] ?? event;
}

// Streams the supervisor has silenced, delivered on the telemetry bootstrap.
// The daemon enforces the same list on its own sink, but renderer events go
// straight to PostHog, so without this the kill switch would only cover half
// the producers.
let disabledEventMatchers: string[] = [];

/**
 * Whether a stream is silenced.
 *
 * Mirrors the daemon's DenylistSink: case-insensitive, `*` matches by prefix,
 * and both the internal name and the exported alias are checked so an operator
 * can type whichever one they see.
 */
export function isDeniedEvent(event: string, denied: string[] = disabledEventMatchers): boolean {
	if (denied.length === 0) return false;
	const candidates = [event.trim().toLowerCase(), postHogEventName(event).trim().toLowerCase()];
	return denied.some((raw) => {
		const rule = raw.trim().toLowerCase();
		if (rule === "" || rule === "*") return false;
		if (rule.endsWith("*")) {
			const prefix = rule.slice(0, -1);
			return prefix !== "" && candidates.some((name) => name.startsWith(prefix));
		}
		return candidates.includes(rule);
	});
}

/** Test seam: the real value arrives with the bootstrap in initTelemetry. */
export function setDisabledEventsForTest(denied: string[]): void {
	disabledEventMatchers = denied;
}

export function reserveDailyActiveCapture(storage?: DailyActiveStorage, now = new Date()): boolean {
	const utcDate = now.toISOString().slice(0, 10);
	const reserveFallback = () => {
		if (fallbackActiveDate === utcDate) return false;
		fallbackActiveDate = utcDate;
		return true;
	};

	if (!storage) return reserveFallback();
	try {
		const raw = storage.getItem(ACTIVE_STORAGE_KEY);
		const parsed = raw ? (JSON.parse(raw) as { date?: unknown }) : {};
		// Any record carrying today's date counts as reserved, which includes the
		// older { date, slots } shape written by builds that emitted four times a
		// day. That is what stops an install upgrading mid-day from emitting a
		// second time on a day it has already reported.
		if (parsed.date === utcDate) return false;
		storage.setItem(ACTIVE_STORAGE_KEY, JSON.stringify({ date: utcDate }));
		return true;
	} catch {
		return reserveFallback();
	}
}

function releaseDailyActiveCapture(storage?: DailyActiveStorage, now = new Date()): void {
	const utcDate = now.toISOString().slice(0, 10);
	if (fallbackActiveDate === utcDate) fallbackActiveDate = "";
	if (!storage) return;

	try {
		const raw = storage.getItem(ACTIVE_STORAGE_KEY);
		const parsed = raw ? (JSON.parse(raw) as { date?: unknown }) : {};
		if (parsed.date !== utcDate) return;
		// DailyActiveStorage is deliberately narrow (getItem/setItem only), so the
		// reservation is cleared by blanking the date rather than widening it.
		storage.setItem(ACTIVE_STORAGE_KEY, JSON.stringify({ date: "" }));
	} catch {
		// The fallback reservation was already released above.
	}
}

export function reserveRouteViewCapture(
	storage: DailyActiveStorage | undefined,
	surface: string,
	now = new Date(),
): boolean {
	const normalizedSurface = surface.trim() || "unknown";
	const utcDate = now.toISOString().slice(0, 10);
	const reserveFallback = () => {
		if (fallbackRouteViewDate !== utcDate) {
			fallbackRouteViewDate = utcDate;
			fallbackRouteViewSurfaces = new Set<string>();
		}
		if (fallbackRouteViewSurfaces.has(normalizedSurface)) return false;
		fallbackRouteViewSurfaces.add(normalizedSurface);
		return true;
	};

	if (!storage) return reserveFallback();
	try {
		const raw = storage.getItem(ROUTE_VIEW_STORAGE_KEY);
		const parsed = raw ? (JSON.parse(raw) as { date?: unknown; surfaces?: unknown }) : {};
		const surfaces =
			parsed.date === utcDate && Array.isArray(parsed.surfaces)
				? parsed.surfaces.filter((value): value is string => typeof value === "string")
				: [];
		if (surfaces.includes(normalizedSurface)) return false;
		surfaces.push(normalizedSurface);
		storage.setItem(ROUTE_VIEW_STORAGE_KEY, JSON.stringify({ date: utcDate, surfaces }));
		return true;
	} catch {
		return reserveFallback();
	}
}

function telemetryStorage(): DailyActiveStorage | undefined {
	try {
		return window.localStorage;
	} catch {
		return undefined;
	}
}

export function startDailyActiveHeartbeat({
	storage,
	now = () => new Date(),
	capture,
	window,
	document,
}: DailyActiveHeartbeatOptions): () => void {
	const maybeCapture = () => {
		const captureTime = now();
		if (!reserveDailyActiveCapture(storage, captureTime)) return;

		let result: boolean | void | Promise<boolean | void>;
		try {
			result = capture();
		} catch {
			releaseDailyActiveCapture(storage, captureTime);
			return;
		}
		void Promise.resolve(result).then(
			(captured) => {
				if (captured === false) releaseDailyActiveCapture(storage, captureTime);
			},
			() => releaseDailyActiveCapture(storage, captureTime),
		);
	};
	const onVisibilityChange = () => {
		if (document.visibilityState === "visible") {
			maybeCapture();
		}
	};
	const activityEvents = ["pointerdown", "keydown"] as const;
	const passiveOptions = { passive: true };

	maybeCapture();
	window.addEventListener("focus", maybeCapture);
	document.addEventListener("visibilitychange", onVisibilityChange);
	for (const event of activityEvents) {
		document.addEventListener(event, maybeCapture, passiveOptions);
	}

	return () => {
		window.removeEventListener("focus", maybeCapture);
		document.removeEventListener("visibilitychange", onVisibilityChange);
		for (const event of activityEvents) {
			document.removeEventListener(event, maybeCapture);
		}
	};
}

function normalizeException(reason: unknown): Error {
	if (reason instanceof Error) return reason;
	if (typeof reason === "string") return new Error(reason);
	try {
		return new Error(JSON.stringify(reason));
	} catch {
		return new Error("Unknown renderer exception");
	}
}

function routeSurface(pathname: string): string {
	if (pathname === "/") return "home";
	if (/^\/settings(?:\/|$)/.test(pathname)) return "global_settings";
	if (/^\/projects\/[^/]+\/sessions\/[^/]+$/.test(pathname)) return "session_detail";
	if (/^\/projects\/[^/]+(?:\/|$)/.test(pathname)) {
		if (/\/settings$/.test(pathname)) return "project_settings";
		return "project_board";
	}
	if (/^\/sessions\/[^/]+$/.test(pathname)) return "session_detail";
	return "other";
}

async function sha256Hex(raw: string): Promise<string> {
	const subtle = globalThis.crypto?.subtle;
	if (!subtle) return "redacted";
	const bytes = new TextEncoder().encode(raw);
	const digest = await subtle.digest("SHA-256", bytes);
	return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

async function hashedTelemetryID(value: unknown): Promise<string | undefined> {
	if (typeof value !== "string") return undefined;
	const trimmed = value.trim();
	if (!trimmed) return undefined;
	return sha256Hex(trimmed);
}

function isLocalURL(value: string): boolean {
	try {
		const url = new URL(value);
		return (
			url.protocol === "file:" ||
			(url.protocol === "app:" && url.host === "renderer") ||
			isLoopbackHostname(url.hostname)
		);
	} catch {
		return false;
	}
}

function redactEmbeddedLocalURLs(value: string): string {
	return value.replace(EMBEDDED_LOCAL_URL_PATTERN, REDACTED_LOCAL_URL);
}

function redactEmbeddedAbsolutePaths(value: string): string {
	return value
		.replace(/(?:\/Users\/|\/home\/|\/tmp\/|\/private\/var\/|\/var\/folders\/)\S+/g, REDACTED_LOCAL_PATH)
		.replace(/\b[A-Za-z]:\\[^\s)]+/g, REDACTED_LOCAL_PATH);
}

function sanitizeSensitiveString(value: string): string {
	const trimmed = value.trim();
	if (!trimmed) return trimmed;
	if (isLocalURL(trimmed)) return REDACTED_LOCAL_URL;
	return redactEmbeddedAbsolutePaths(redactEmbeddedLocalURLs(trimmed));
}

function sanitizePostHogValue(value: unknown): unknown {
	if (typeof value === "string") return sanitizeSensitiveString(value);
	if (Array.isArray(value)) return value.map((item) => sanitizePostHogValue(item));
	if (value && typeof value === "object") {
		return Object.fromEntries(Object.entries(value).map(([key, nested]) => [key, sanitizePostHogValue(nested)]));
	}
	return value;
}

export function sanitizePostHogEvent(event: Record<string, unknown>): Record<string, unknown> {
	return sanitizePostHogValue(event) as Record<string, unknown>;
}

export function sanitizeReplayRequestName(name: string): string {
	const withoutQuery = name.split("?")[0] ?? name;
	return sanitizeSensitiveString(withoutQuery);
}

function sanitizePostHogCaptureResult<T>(event: T): T {
	return sanitizePostHogEvent(event as unknown as Record<string, unknown>) as unknown as T;
}

async function sanitizeRendererContextProperties(properties?: TelemetryProperties): Promise<TelemetryProperties> {
	const safe: TelemetryProperties = {};
	if (typeof properties?.source === "string" && properties.source.trim() !== "") {
		safe.source = properties.source;
	}
	if (typeof properties?.operation === "string" && properties.operation.trim() !== "") {
		safe.operation = properties.operation;
	}
	if (typeof properties?.surface === "string" && properties.surface.trim() !== "") {
		safe.surface = properties.surface;
	}
	if (typeof properties?.unhandled === "boolean") {
		safe.unhandled = properties.unhandled;
	}
	const projectIDHash = await hashedTelemetryID(properties?.project_id);
	if (projectIDHash) {
		safe.project_id_hash = projectIDHash;
	}
	return safe;
}

const ORCHESTRATOR_SPAWN_SOURCE_SET = new Set<string>(ORCHESTRATOR_SPAWN_SOURCES);

export async function sanitizeRendererProperties(
	event: string,
	properties?: TelemetryProperties,
): Promise<TelemetryProperties> {
	const safe: TelemetryProperties = {};
	switch (event) {
		case "ao.app.active":
			if (properties?.channel === "renderer") safe.channel = "renderer";
			break;
		case "ao.renderer.route_viewed":
			if (typeof properties?.surface === "string" && properties.surface.trim() !== "") {
				safe.surface = properties.surface;
			}
			break;
		case "ao.renderer.project_add_requested":
		case "ao.renderer.loaded":
			break;
		case "ao.renderer.project_add_succeeded":
		case "ao.renderer.project_removed":
		case "ao.renderer.orchestrator_open_requested":
		case "ao.renderer.task_create_requested":
		case "ao.renderer.task_create_succeeded":
		case "ao.renderer.task_create_failed":
		case "ao.renderer.session_kill_requested":
		case "ao.renderer.session_kill_succeeded":
		case "ao.renderer.session_kill_failed":
		case "ao.renderer.settings_save_requested":
		case "ao.renderer.settings_save_succeeded":
		case "ao.renderer.settings_save_failed": {
			const projectIDHash = await hashedTelemetryID(properties?.project_id);
			if (projectIDHash) safe.project_id_hash = projectIDHash;
			break;
		}
		case "ao.renderer.orchestrator_spawn_requested":
		case "ao.renderer.orchestrator_spawn_succeeded":
		case "ao.renderer.orchestrator_spawn_failed": {
			const projectIDHash = await hashedTelemetryID(properties?.project_id);
			if (projectIDHash) safe.project_id_hash = projectIDHash;
			if (typeof properties?.source === "string" && ORCHESTRATOR_SPAWN_SOURCE_SET.has(properties.source)) {
				safe.source = properties.source;
			}
			break;
		}
		case "ao.renderer.notification_opened":
			if (properties?.target === "pr" || properties?.target === "session") safe.target = properties.target;
			break;
		case "ao.renderer.notification_mark_read_requested":
		case "ao.renderer.notification_mark_read_succeeded":
		case "ao.renderer.notification_mark_read_failed":
			if (properties?.scope === "single" || properties?.scope === "all") safe.scope = properties.scope;
			break;
		case "ao.renderer.daemon_failure":
			if (typeof properties?.daemon_state === "string") safe.daemon_state = properties.daemon_state;
			if (typeof properties?.code === "string") safe.code = properties.code;
			if (typeof properties?.exit_code === "number") safe.exit_code = properties.exit_code;
			if (typeof properties?.signal === "string") safe.signal = properties.signal;
			break;
		case "ao.renderer.api_error":
			if (typeof properties?.operation === "string") safe.operation = properties.operation;
			if (typeof properties?.error_category === "string") safe.error_category = properties.error_category;
			if (typeof properties?.status === "number") safe.status = properties.status;
			break;
		case "ao.renderer.terminal_attach_failed":
			if (properties?.reason === "open_timeout" || properties?.reason === "pane_error") {
				safe.reason = properties.reason;
			}
			break;
		case "ao.renderer.agents_available": {
			// Counts and a fixed-vocabulary id list only. Agent ids come from AO's own
			// registry, never from user input, so they carry no user data.
			for (const key of ["installed_count", "authorized_count", "supported_count"] as const) {
				if (typeof properties?.[key] === "number") safe[key] = properties[key];
			}
			if (typeof properties?.authorized_agents === "string") {
				safe.authorized_agents = properties.authorized_agents;
			}
			break;
		}
		case "ao.renderer.session_state_unknown":
			if (properties?.field === "status" || properties?.field === "activity") safe.field = properties.field;
			if (properties?.reason === "missing" || properties?.reason === "unrecognized") safe.reason = properties.reason;
			break;
		case "ao.renderer.update_failed":
		case "ao.renderer.update_downloaded":
		case "ao.renderer.update_unsupported":
			// Version strings are release identifiers, not user data. The updater's
			// raw error message is deliberately absent: it can carry feed URLs and
			// local paths, so update-telemetry.ts maps it to error_category first.
			if (typeof properties?.to_version === "string") safe.to_version = properties.to_version;
			if (typeof properties?.error_category === "string") safe.error_category = properties.error_category;
			if (properties?.phase === "check" || properties?.phase === "download") safe.phase = properties.phase;
			if (properties?.trigger === "automatic" || properties?.trigger === "manual") safe.trigger = properties.trigger;
			break;
		case "ao.renderer.mobile_connect_opened":
			// Whether the bridge was already on when the modal opened separates
			// "came to set this up" from "came back to re-scan the QR".
			if (typeof properties?.bridge_enabled === "boolean") safe.bridge_enabled = properties.bridge_enabled;
			break;
		case "ao.renderer.update_channel_changed":
			// Closed vocabulary on both ends. A feature build's PR number or branch
			// name is deliberately absent: it names unreleased work.
			for (const key of ["from_channel", "to_channel"] as const) {
				const value = properties?.[key];
				if (value === "stable" || value === "nightly" || value === "feature" || value === "unknown") {
					safe[key] = value;
				}
			}
			break;
		case "ao.renderer.support_opened":
			break;
		case "ao.renderer.support_submitted":
			// The report's summary, details, and diagnostics block are the user's own
			// words and machine state. Only the chosen destination and whether the
			// hand-off worked are reported.
			if (properties?.destination === "github" || properties?.destination === "discord" || properties?.destination === "email") {
				safe.destination = properties.destination;
			}
			if (properties?.outcome === "succeeded" || properties?.outcome === "failed") safe.outcome = properties.outcome;
			break;
		case "ao.renderer.mobile_bridge_toggled":
			// The host, port, and connection password in the QR never leave the
			// machine: only the direction of the switch and whether it worked.
			if (typeof properties?.enabled === "boolean") safe.enabled = properties.enabled;
			if (properties?.outcome === "succeeded" || properties?.outcome === "failed") safe.outcome = properties.outcome;
			break;
	}
	return safe;
}

function exceptionName(error: unknown): string {
	if (error instanceof Error && error.name.trim() !== "") return error.name.trim();
	if (typeof error === "string") return "string";
	return "unknown";
}

export async function sanitizeRendererExceptionProperties(
	error: unknown,
	properties?: TelemetryProperties,
): Promise<TelemetryProperties> {
	const safe: TelemetryProperties = {
		error_name: exceptionName(error),
	};
	return { ...safe, ...(await sanitizeRendererContextProperties(properties)) };
}

function bindErrorHandlers() {
	if (errorHandlersBound) return;
	errorHandlersBound = true;
	window.addEventListener("error", (event) => {
		void captureRendererException(event.error ?? new Error(event.message), {
			source: "window-error",
			unhandled: true,
		});
	});
	window.addEventListener("unhandledrejection", (event) => {
		void captureRendererException(normalizeException(event.reason), {
			source: "unhandledrejection",
			unhandled: true,
		});
	});
}

type PostHogInitOptions = NonNullable<Parameters<typeof posthog.init>[1]>;

export function buildPostHogConfig(distinctId: string): PostHogInitOptions {
	return {
		api_host: POSTHOG_HOST,
		defaults: RELEASE_TAG,
		autocapture: false,
		capture_pageview: false,
		capture_exceptions: false,
		capture_performance: false,
		// Session replay is billed per recording, not per event, so it bypasses
		// every limiter in this file. AO never watches replays, so keep the
		// recorder off in the client instead of relying on the project-side
		// toggle staying off.
		disable_session_recording: true,
		// AO reads no feature flags and ships no surveys. Both of these poll
		// PostHog on init, and /flags requests are billed, so every one of these
		// requests is pure cost for data nothing consumes.
		advanced_disable_flags: true,
		disable_surveys: true,
		// AO owns the stable random installation ID. Memory-only SDK
		// persistence prevents legacy identified state from replacing it after
		// an upgrade; the AO-owned heartbeat and route reservations continue to
		// use window.localStorage independently.
		persistence: "memory",
		person_profiles: "never",
		bootstrap: {
			distinctID: distinctId,
			isIdentifiedID: false,
		},
		before_send: (event) => (event ? sanitizePostHogCaptureResult(event) : event),
		session_recording: {
			maskCapturedNetworkRequestFn: (request) => {
				if (request.name) {
					request.name = sanitizeReplayRequestName(request.name);
				}
				return request;
			},
		},
	};
}

export async function initTelemetry(): Promise<boolean> {
	if (initPromise) return initPromise;
	const attempt = (async () => {
		if (!POSTHOG_KEY) return false;
		const bootstrap = await aoBridge.telemetry.getBootstrap();
		// Null means the supervisor withheld it: no key, no data dir, or an
		// unpackaged build that has not opted in. The client is never created.
		if (!bootstrap) return false;
		disabledEventMatchers = bootstrap.disabledEvents ?? [];
		telemetryContext = buildTelemetryContext(
			bootstrap.appVersion,
			bootstrap.platform,
			releaseChannelFrom(await readUpdateSettingsForTelemetry()),
		);
		posthog.init(POSTHOG_KEY, buildPostHogConfig(bootstrap.distinctId));
		posthog.register({
			...telemetryContext,
			surface: "renderer",
		});
		bindErrorHandlers();
		startDailyActiveHeartbeat({
			storage: telemetryStorage(),
			window,
			document,
			// Ride the batched request queue like every other renderer event
			// (loaded, route_viewed). An earlier `send_instantly: true` here
			// fired an immediate, un-retried send during init that never
			// reached PostHog in packaged builds, so renderer app-active
			// dropped to zero while batched events kept landing.
			capture: async () =>
				isDeniedEvent("ao.app.active")
					? true
					: Boolean(
					posthog.capture(
						postHogEventName("ao.app.active"),
						withTelemetryContext(await sanitizeRendererProperties("ao.app.active", { channel: "renderer" })),
					),
				),
		});
		if (!isDeniedEvent("ao.renderer.loaded")) {
			posthog.capture(
				postHogEventName("ao.renderer.loaded"),
				withTelemetryContext(await sanitizeRendererProperties("ao.renderer.loaded")),
			);
		}
		return true;
	})().catch(() => {
		// A thrown error is transient (the bridge not ready yet, or a hiccup in
		// posthog.init): drop the memoized promise so the next captured event
		// retries init rather than the whole session going dark after one bad
		// moment. A deliberate `return false` above (no key, not opted in) is
		// not an error and stays cached, so an intentionally-withheld client is
		// not retried forever.
		if (initPromise === attempt) initPromise = null;
		return false;
	});
	initPromise = attempt;
	return attempt;
}

export async function captureRendererEvent(event: string, properties?: Record<string, unknown>): Promise<void> {
	// Checked before the reservations so a silenced stream does not consume a
	// rate-limit slot on its way to being discarded, matching the daemon, where
	// the denylist sits outermost.
	if (isDeniedEvent(event)) return;
	const sanitizedProperties = await sanitizeRendererProperties(event, properties);
	if (event === "ao.renderer.route_viewed") {
		const surface = typeof sanitizedProperties.surface === "string" ? sanitizedProperties.surface : "other";
		if (!reserveRouteViewCapture(telemetryStorage(), surface)) return;
	} else if (!reserveCapture(event)) {
		return;
	}
	if (!(await initTelemetry())) return;
	const safeProperties = withTelemetryContext(sanitizedProperties);
	posthog.capture(postHogEventName(event), safeProperties);
}

export async function captureRendererException(error: unknown, properties?: Record<string, unknown>): Promise<void> {
	// "$exception" is the name this lands under in PostHog, so that is what an
	// operator would type to silence a crash loop.
	if (isDeniedEvent("$exception")) return;
	if (!reserveCapture(`exception:${exceptionName(error)}`)) return;
	if (!(await initTelemetry())) return;
	const safeProperties = withTelemetryContext(await sanitizeRendererExceptionProperties(error, properties));
	posthog.captureException(normalizeException(error), safeProperties);
}

export async function addRendererExceptionStep(message: string, properties?: Record<string, unknown>): Promise<void> {
	if (!(await initTelemetry())) return;
	const safeProperties = withTelemetryContext(await sanitizeRendererContextProperties(properties));
	posthog.addExceptionStep(message, safeProperties);
}

export { routeSurface };
