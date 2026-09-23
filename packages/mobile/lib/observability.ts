// Error triage classification, shared across renderer, main, and (later) the
// daemon bridge. Pure and dependency-free so it is trivially unit-testable and
// identical everywhere.
//
// It answers the two questions a person watching Sentry actually has:
//   severity — how bad is it?      (drives alerting + priority)
//   owner    — whose problem is it? (drives routing)
// on top of the technical `category`/`code` we already produce. It is additive:
// an unforeseen error still classifies via its category (or the safe default),
// so new failures never go unlabelled.

/** How bad, worst to least. P0 pages, P3 is a breadcrumb. */
export type Severity = "P0" | "P1" | "P2" | "P3";

/** Where a fix (if any) belongs. */
export type FaultOwner = "ours" | "user" | "external" | "environment";

/** Sentry event level per severity. */
export type SentryLevel = "fatal" | "error" | "warning" | "info";

export type Triage = {
	severity: Severity;
	owner: FaultOwner;
	level: SentryLevel;
	/** Transient connectivity-ish failures that self-recover; kept as breadcrumbs. */
	transient: boolean;
	/** Whether to send as an issue (true) or only record as a breadcrumb (false). */
	report: boolean;
};

export type ClassifyInput = {
	/** Transport/type category, e.g. from api-client.ts or a boundary. */
	category?: string;
	/** Exact daemon error code, e.g. WORKSPACE_LOCKED, PR_NOT_MERGEABLE. */
	code?: string;
	/** HTTP status when known. */
	httpStatus?: number;
	/** apierr.Kind name if surfaced (invalid/not_found/conflict/forbidden/internal). */
	kind?: string;
	/** True for window.onerror / unhandledrejection / native crashes. */
	unhandled?: boolean;
};

const LEVEL: Record<Severity, SentryLevel> = { P0: "fatal", P1: "error", P2: "warning", P3: "info" };

// Categories that are connectivity/environment blips: real, but they self-recover
// and would drown the signal, so they ride as breadcrumbs unless they spike
// (spike detection is a server-side alert rule, not a client decision).
const TRANSIENT = new Set(["offline", "network_error", "timeout"]);

type Base = { severity: Severity; owner: FaultOwner };

// Default triage per technical category. The safe fallback for anything unknown
// is P2/ours — visible, and pointed at us to investigate, never silently dropped.
const CATEGORY: Record<string, Base> = {
	native_crash: { severity: "P0", owner: "ours" },
	render_crash: { severity: "P0", owner: "ours" },
	daemon_unavailable: { severity: "P1", owner: "environment" },
	http_5xx: { severity: "P1", owner: "ours" },
	agent_runtime: { severity: "P1", owner: "ours" },
	update: { severity: "P1", owner: "ours" },
	integration: { severity: "P2", owner: "external" },
	rate_limited: { severity: "P2", owner: "external" },
	auth: { severity: "P2", owner: "external" },
	conflict: { severity: "P2", owner: "ours" },
	timeout: { severity: "P2", owner: "environment" },
	network_error: { severity: "P2", owner: "environment" },
	version_skew: { severity: "P2", owner: "ours" },
	push: { severity: "P3", owner: "environment" },
	http_4xx: { severity: "P3", owner: "user" },
	not_found: { severity: "P3", owner: "ours" },
	validation: { severity: "P3", owner: "user" },
	permission: { severity: "P3", owner: "environment" },
	offline: { severity: "P3", owner: "environment" },
};

// Code-specific overrides where the category alone gets owner/severity wrong.
// e.g. an `auth` failure is `external` by default (a GitHub token), but a wrong
// pairing password is squarely the user.
const CODE: Record<string, Partial<Base>> = {
	// spawn / runtime setup — blocks the user, and it's their environment to fix
	AGENT_BINARY_NOT_FOUND: { severity: "P1", owner: "user" },
	RUNTIME_PREREQUISITE_MISSING: { severity: "P1", owner: "user" },
	// spawn / worktree contention and drift — our state machine
	WORKSPACE_LOCKED: { severity: "P1", owner: "ours" },
	BRANCH_CHECKED_OUT_ELSEWHERE: { severity: "P1", owner: "ours" },
	WORKSPACE_CWD_MISMATCH: { severity: "P1", owner: "ours" },
	// git / project setup — user
	NOT_A_GIT_REPO: { severity: "P2", owner: "user" },
	BRANCH_NOT_FETCHED: { severity: "P2", owner: "user" },
	INVALID_BRANCH: { severity: "P2", owner: "user" },
	PROJECT_NOT_RESOLVABLE: { severity: "P2", owner: "user" },
	// pairing auth — user
	BAD_PASSWORD: { severity: "P2", owner: "user" },
	LOCKED_OUT: { severity: "P2", owner: "user" },
	// external SCM
	SCM_UNAVAILABLE: { severity: "P1", owner: "external" },
	PR_OPERATION_FAILED: { severity: "P2", owner: "external" },
	REVIEW_OPERATION_FAILED: { severity: "P2", owner: "external" },
	// unmapped/internal server error — ours, high
	INTERNAL_ERROR: { severity: "P1", owner: "ours" },
};

// apierr.Kind is a coarse safety net when no category/code override applies.
const KIND: Record<string, Base> = {
	internal: { severity: "P1", owner: "ours" },
	conflict: { severity: "P2", owner: "ours" },
	forbidden: { severity: "P2", owner: "external" },
	not_found: { severity: "P3", owner: "ours" },
	invalid: { severity: "P3", owner: "user" },
};

/** Classify a raw error into severity + owner + reporting policy. */
export function classifyError(input: ClassifyInput): Triage {
	const category = input.category?.trim();
	const code = input.code?.trim();

	// Base from category, then kind, then the safe default.
	let base: Base =
		(category && CATEGORY[category]) || (input.kind && KIND[input.kind]) || { severity: "P2", owner: "ours" };

	// Code override wins where present.
	if (code && CODE[code]) base = { ...base, ...CODE[code] };

	// A daemon 503 is deliberate backpressure, not a fault: the daemon returns a
	// typed SERVICE_UNAVAILABLE for retryable contention (#4325/#4334) and skips it
	// in its own Sentry capture. Mirror that here so it rides as a breadcrumb and
	// never becomes a paged issue — otherwise every contention spike, the exact
	// thing 503 exists to absorb, would flood Sentry. Scoped to the daemon's own
	// backpressure signal; the synthetic `daemon_unavailable` category (couldn't
	// reach the daemon at all) is distinct and keeps its own P1 routing.
	const unavailable =
		code === "SERVICE_UNAVAILABLE" || (input.httpStatus === 503 && category !== "daemon_unavailable");

	// An unhandled crash is always at least P1 (the app hit something it didn't expect).
	let severity = base.severity;
	if (input.unhandled && (severity === "P2" || severity === "P3")) severity = "P1";
	// A 5xx status is never below P1 even if the category was coarse — but 503 is
	// backpressure, handled above, so it is excluded from the escalation.
	if (input.httpStatus && input.httpStatus >= 500 && input.httpStatus !== 503 && severity === "P2")
		severity = "P1";
	// Cap backpressure at P2 so it can be suppressed as a breadcrumb below.
	if (unavailable) severity = "P2";

	const transient = (category ? TRANSIENT.has(category) : false) || unavailable;
	// Report as an issue for P0/P1 always; for P2 unless it's transient; never for P3.
	const report = severity === "P0" || severity === "P1" || (severity === "P2" && !transient);

	return { severity, owner: base.owner, level: LEVEL[severity], transient, report };
}
