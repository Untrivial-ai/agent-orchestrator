// Pure failure-to-copy mapping for pairing and manual connect. Free of React
// Native / Expo imports (and of `api.ts`, which pulls in AsyncStorage via
// `config.ts`) so it can be unit-tested directly — mirroring `pushStatus.ts`.
//
// The guiding rule, same as `classifyServerFailure`: reaching the server and
// being rejected by it is not the same as never reaching it. Telling someone
// with a rotated password to "check you're on the same Wi-Fi" sends them to
// debug the wrong thing.

export type ConnectionFailure =
	| "not-ao-qr" // the scanned code wasn't an AO pairing payload
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
	return failure !== "auth" && failure !== "rate-limited";
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
		case "not-ao-qr":
			return {
				title: "Not an AO pairing code",
				message: "That QR code isn't an AO pairing code.",
				showLocalNetworkHint: false,
			};
		case "unreachable":
			return {
				title: "Your desktop disconnected",
				message: isTailscaleHost(target.host)
					? `Reached nothing at ${target.host}:${target.port}. ` +
						"Make sure Tailscale is connected on both this phone and your computer, and that your computer is awake."
					: `Reached nothing at ${target.host}:${target.port}. ` +
						"Is Connect Mobile still on, and is your phone on the same Wi-Fi?",
				showLocalNetworkHint,
			};
		case "auth":
			// The connection itself worked, so "disconnected" would be wrong here —
			// and re-scanning is the actual fix, not retrying the same password.
			return {
				title: "Your desktop rejected the password",
				message: "That password was rotated. Re-scan the code on your computer.",
				showLocalNetworkHint: false,
			};
		case "rate-limited":
			// The daemon locks a device out for one minute after 5 failed auths, and
			// clears it automatically. Saying so stops people from power-cycling
			// things that were never the problem.
			return {
				title: "Too many attempts",
				message:
					"Your computer locked this device out after too many failed attempts. " +
					"It clears on its own in about a minute — check the password, then try again.",
				showLocalNetworkHint: false,
			};
		case "server-error":
			return {
				title: "Your desktop returned an error",
				message: `${target.host}:${target.port} answered, but with an error. Check the AO logs on your computer.`,
				showLocalNetworkHint: false,
			};
	}
}

/** The extra line shown when {@link ConnectionErrorCopy.showLocalNetworkHint} is set. */
export const LOCAL_NETWORK_HINT =
	"If you denied the Local Network prompt, enable it in Settings › Privacy & Security › Local Network › AO.";
