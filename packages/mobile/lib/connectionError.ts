// Pure failure-to-copy mapping for pairing and manual connect. Free of React
// Native / Expo imports (and of `api.ts`, which pulls in AsyncStorage via
// `config.ts`) so it can be unit-tested directly — mirroring `pushStatus.ts`.
//
// The guiding rule, same as `classifyServerFailure`: reaching the server and
// being rejected by it is not the same as never reaching it. Telling someone
// with a rotated password to "check you're on the same Wi-Fi" sends them to
// debug the wrong thing.

// Type-only, so erased at runtime: this module stays free of native imports.
import type { FeatherIconName } from "./icons";

export type ConnectionFailure =
	| "not-ao-qr" // the scanned code wasn't an AO pairing payload
	| "outdated-desktop" // a v1 code: AO on the computer is too old to pair with
	| "incompatible-host" // the host reported an API version this phone cannot use
	| "tunnel-rotated" // nothing answered, and the only remote path was a tunnel
	| "unreachable" // nothing answered (DNS failure, refused, timeout)
	| "auth" // 401/403 — the password is wrong or was rotated
	| "rate-limited" // 429 — the daemon's failed-attempt lockout
	| "server-error"; // answered, but with some other error status

/**
 * Maps the HTTP status the daemon answered with to a failure reason.
 * `undefined` means the request never got an answer.
 */
export function classifyConnectionFailure(status: number | undefined): ConnectionFailure {
	if (status === undefined) return "unreachable";
	if (status === 426) return "incompatible-host";
	if (status === 401 || status === 403) return "auth";
	if (status === 429) return "rate-limited";
	return "server-error";
}

/**
 * Whether the board poll should keep running after a failed tick.
 *
 * Rejection is not the same as failure. A wrong or rotated password will be
 * wrong on the next tick too, and the daemon locks a device out for a minute
 * after five failed auths — so polling into a 401 walks the phone into a
 * lockout that then blocks the pairing scan meant to fix it. A 429 says that
 * has already started. Everything else (unreachable, 5xx) is transient by
 * nature, so the poll keeps going and recovers on its own.
 *
 * Keyed on the status the daemon actually returned rather than on the text of
 * the error, which was the previous approach: it matched `message` against
 * "401"/"429" prefixes, so it silently stopped working whenever the wire copy
 * was reworded, and never fired at all for a 403.
 */
export function shouldKeepPolling(status: number | undefined): boolean {
	const failure = classifyConnectionFailure(status);
	return failure !== "auth" && failure !== "rate-limited" && failure !== "incompatible-host";
}

/**
 * Whether the board's last poll failed because nothing answered, the only
 * failure that clears on its own when the desktop comes back.
 *
 * `connection` alone can't say this: the store closes it on every failed tick,
 * including a 401/403/429 that stops the poll for good (see shouldKeepPolling)
 * and a 5xx that did answer. Screens that promise "loads once the app
 * reconnects" must only say so when a reconnect can actually happen, and must
 * leave rejections to their own copy (re-scan the pairing code).
 */
export function isDesktopUnreachable(poll: {
	connection: string;
	error: string | null;
	errorStatus: number | null;
}): boolean {
	return poll.connection === "closed" && poll.error !== null && classifyConnectionFailure(poll.errorStatus ?? undefined) === "unreachable";
}

/**
 * Whether a failed request is the daemon's word that the session no longer
 * exists. Only a 404 or 410 says that. A timeout, a refused connection or a 5xx
 * is a fact about the link, not the session, and a link comes back — so none of
 * those may be shown or acted on as "not found".
 */
export function isSessionGone(status: number | undefined): boolean {
	return status === 404 || status === 410;
}

/**
 * True for addresses on the phone's own LAN — the ones iOS gates behind the
 * Local Network permission prompt.
 *
 * Tailscale's 100.64/10 CGNAT range is deliberately excluded: it rides a VPN
 * interface, so a failure there is never the Local Network prompt and pointing
 * the user at that setting would be a dead end.
 */
export function isLocalNetworkHost(host: string): boolean {
	const h = host.trim().toLowerCase();
	if (h.length === 0) return false;
	if (h === "localhost" || h.endsWith(".local")) return true;
	const m = h.match(/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/);
	if (!m) return false;
	const [a, b] = [Number(m[1]), Number(m[2])];
	if (a === 10) return true;
	if (a === 192 && b === 168) return true;
	if (a === 172 && b >= 16 && b <= 31) return true;
	if (a === 169 && b === 254) return true; // link-local
	if (a === 127) return true;
	return false;
}

/**
 * True for Tailscale's 100.64.0.0/10 CGNAT range. Deliberately NOT part of
 * `isLocalNetworkHost`: a failure here is never the iOS Local Network prompt, and
 * it is never a Wi-Fi problem either.
 */
export function isTailscaleHost(host: string): boolean {
	const h = host.trim().toLowerCase();
	const m = h.match(/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/);
	if (!m) return false;
	const [a, b] = [Number(m[1]), Number(m[2])];
	return a === 100 && b >= 64 && b <= 127;
}

export type ConnectionErrorCopy = {
	// Short heading. Used where the failure owns the screen (the board's empty
	// state); the inline error boxes on the pairing screens show `message` alone.
	title: string;
	message: string;
	// The empty-state glyph for this cause. Distinct per cause so a rejected
	// password no longer wears the same "no Wi-Fi" icon as a desktop that is
	// simply out of range.
	icon: FeatherIconName;
	// One short line under the title on the board's empty state. Most causes
	// leave it out and let the title and buttons speak; a disconnect keeps one
	// because the fix is on the user's side and not obvious from the title.
	hint?: string;
	// When true the screen appends the Local Network hint and offers a button
	// that opens the OS settings page for AO.
	showLocalNetworkHint: boolean;
};

/**
 * Human-facing copy for a failed connect attempt. Kept separate from the network
 * code so the wording is testable.
 */
export function describeConnectionFailure(
	reason: ConnectionFailure,
	target: { host: string; port: string; platform: "ios" | "android" | string },
): ConnectionErrorCopy {
	// Only surfaced when the phone never reached the host at all, and only on
	// iOS, where a denied Local Network prompt produces exactly this symptom.
	const showLocalNetworkHint =
		reason === "unreachable" && target.platform === "ios" && isLocalNetworkHost(target.host);

	switch (reason) {
		case "incompatible-host":
			return {
				title: "AO versions are incompatible",
				icon: "download-cloud",
				message: "Update AO on this phone and the machine, then reconnect.",
				showLocalNetworkHint: false,
			};
		case "tunnel-rotated":
			return {
				title: "Your machine's address changed",
				icon: "route-off",
				message:
					"AO restarted on that machine, so it has a new address. Open AO \u2192 Settings \u2192 Connect Mobile there and scan the new code.",
				showLocalNetworkHint: false,
			};
		case "outdated-desktop":
			return {
				title: "Update AO on your machine",
				icon: "download-cloud",
				message:
					"That code came from an older version of AO. Update AO on that machine, then generate a new code.",
				showLocalNetworkHint: false,
			};
		case "not-ao-qr":
			return {
				title: "Not an AO pairing code",
				icon: "alert-circle",
				message: "That QR code isn't an AO pairing code.",
				showLocalNetworkHint: false,
			};
		case "unreachable": {
			// Build the address string only when we have both host and port
			const address = target.host && target.port ? `${target.host}:${target.port}` : target.host || "";
			const messagePrefix = address ? `Reached nothing at ${address}. ` : "Couldn't reach your machine. ";
			return {
				title: "Your machine is offline",
				icon: "unplug",
				hint: isTailscaleHost(target.host)
					? "Check Tailscale is on for both devices."
					: "Check you're on the same Wi-Fi.",
				message: isTailscaleHost(target.host)
					? messagePrefix +
						"Make sure Tailscale is connected on this phone and that machine, and that AO is running there."
					: messagePrefix +
						"Is Connect Mobile still on, and is your phone on the same Wi-Fi?",
				showLocalNetworkHint,
			};
		}
		case "auth":
			// The connection itself worked, so "disconnected" would be wrong here —
			// and re-scanning is the actual fix, not retrying the same password.
			return {
				title: "Your machine rejected the password",
				icon: "monitor-off",
				message: "That password was rotated. Re-scan the code on that machine.",
				showLocalNetworkHint: false,
			};
		case "rate-limited":
			// The daemon locks a device out for one minute after 5 failed auths, and
			// clears it automatically. Saying so stops people from power-cycling
			// things that were never the problem.
			return {
				title: "Too many attempts",
				icon: "timer",
				message:
					"Your machine locked this device out after too many failed attempts. " +
					"It clears on its own in about a minute — check the password, then try again.",
				showLocalNetworkHint: false,
			};
		case "server-error":
			return {
				title: "Your machine returned an error",
				icon: "monitor-cog",
				message: `${target.host}:${target.port} answered, but with an error. Check the AO logs on that machine.`,
				showLocalNetworkHint: false,
			};
	}
}

/** The extra line shown when {@link ConnectionErrorCopy.showLocalNetworkHint} is set. */
export const LOCAL_NETWORK_HINT =
	"If you denied the Local Network prompt, enable it in Settings › Privacy & Security › Local Network › AO.";

// ---- Failed actions ----------------------------------------------------------

/**
 * Thrown by the request layer when the desktop never answered: a timeout, a
 * refused connection, DNS failure or no network. Typed so screens can tell
 * "your desktop is unreachable" apart from a rejection without matching on
 * fetch's platform-specific wording ("Network request failed").
 */
export class UnreachableError extends Error {
	constructor(
		readonly reason: "timeout" | "offline",
		options?: { cause?: unknown },
	) {
		super(reason === "timeout" ? "Your machine didn't respond in time." : "Couldn't reach your machine.", options);
		this.name = "UnreachableError";
	}
}

/** Shown when a request never reached the desktop. */
export const UNREACHABLE_ACTION_COPY =
	"Couldn't reach your machine. Make sure AO is running there, then try again.";

/** Shown in place of a missing server config ("No AO server configured"). */
export const NOT_PAIRED_ACTION_COPY =
	"This phone isn't paired with a machine. Scan its AO pairing code.";

// The fields an ApiError carries, read structurally so this module stays free of
// `api.ts` (and with it AsyncStorage) and remains unit-testable.
type AnsweredFailure = { status: number; code?: unknown; detail?: unknown; requestId?: unknown; message?: unknown };

function answeredFailure(error: unknown): AnsweredFailure | undefined {
	if (typeof error !== "object" || error === null || !("status" in error)) return undefined;
	const status = (error as { status: unknown }).status;
	return typeof status === "number" && status >= 400 ? (error as AnsweredFailure) : undefined;
}

/** True when a request failed without the desktop ever answering. */
export function isUnreachableError(error: unknown): boolean {
	if (error instanceof UnreachableError) return true;
	// `req()` wraps its own fetch failures, so this only catches fetch used
	// directly (event streams, the identity probe). Anchored on fetch's exact
	// messages: a looser match also caught code defects such as
	// "undefined is not a function (evaluating 'x.fetchPage()')".
	return error instanceof TypeError && FETCH_FAILURE_MESSAGES.test(error.message.trim());
}

// React Native's fetch ("Network request failed"/"timed out") and the WebKit /
// Chromium wording, in case a WebView-backed path surfaces one.
const FETCH_FAILURE_MESSAGES = /^(network request (failed|timed out)|failed to fetch|load failed)$/i;

// Errors the JS engine throws for code defects. Their text ("undefined is not a
// function (evaluating …)") is for developers, never for the person holding the
// phone, so they get the generic line instead.
function isEngineError(error: Error): boolean {
	return error instanceof TypeError || error instanceof ReferenceError || error instanceof RangeError || error instanceof SyntaxError;
}

/**
 * The daemon's own explanation, without the `404 Not Found - ` envelope prefix
 * that `ApiError.message` keeps for logs.
 */
export function daemonDetail(error: unknown): string | undefined {
	const failure = answeredFailure(error);
	if (!failure) return undefined;
	if (typeof failure.detail === "string") return failure.detail.trim() || undefined;
	if (typeof failure.message !== "string") return undefined;
	const stripped = failure.message.replace(/^\d{3}\b[^-]*(?:-\s*)?/, "").trim();
	return stripped || undefined;
}

/**
 * One sentence a person can act on, for any failed request or action. Never a
 * status code, an HTTP reason phrase or fetch's own wording.
 *
 * - no answer → "couldn't reach your desktop"
 * - 401/403/429 → the pairing copy the board uses
 * - other 4xx → the daemon's message, which it writes for people
 * - 5xx → a generic line; the daemon's text there is internal, so only its
 *   request ID is kept, for matching against the desktop's logs
 * - an error the app threw itself → its message, which is already user copy
 * - a JS engine error (a code defect) → the fallback, never its text
 */
export function userFacingError(error: unknown, fallback = "Something went wrong. Try again."): string {
	if (isUnreachableError(error)) return UNREACHABLE_ACTION_COPY;
	const failure = answeredFailure(error);
	if (failure) {
		const { status } = failure;
		if (status === 401 || status === 403) return "Your machine rejected this phone's password. Re-scan the pairing code on that machine.";
		if (status === 429) return "Your machine locked this device out after too many failed attempts. It clears on its own in about a minute.";
		if (status >= 500) {
			if (status === 503) return "Your machine is busy or still starting up. Try again in a moment.";
			const ref = typeof failure.requestId === "string" && failure.requestId ? ` (Reference: ${failure.requestId})` : "";
			return `Something went wrong on your machine. Try again, or check the AO logs there.${ref}`;
		}
		const detail = daemonDetail(error);
		if (detail) return sentence(detail);
		if (status === 404 || status === 410) return "That's no longer on your machine. Refresh and try again.";
		if (status === 409) return "That changed on your machine in the meantime. Refresh and try again.";
		return "Your machine couldn't complete that. Refresh and try again.";
	}
	if (error instanceof Error && !isEngineError(error) && error.message.trim()) return error.message.trim();
	return fallback;
}

function sentence(text: string): string {
	const trimmed = text.trim();
	const capitalized = trimmed.charAt(0).toUpperCase() + trimmed.slice(1);
	return /[.!?…)]$/.test(capitalized) ? capitalized : `${capitalized}.`;
}
