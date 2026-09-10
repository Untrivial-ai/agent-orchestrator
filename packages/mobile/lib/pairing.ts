import type { ServerConfig } from "./config";

// Parses the JSON payload encoded in the desktop's pairing QR code. Pure and
// dependency-free (no React/RN imports) so it typechecks trivially and needs
// no test runner in this package. The password is included so a single scan
// autofills everything; it is optional for back-compat with older QR codes
// (host+port only), in which case the user types the password by hand.
export type ParsedPairing = { host: string; port: string; password: string; secure: boolean };

export function parsePairingPayload(raw: string): ParsedPairing | null {
	let parsed: unknown;
	try {
		parsed = JSON.parse(raw);
	} catch {
		return null;
	}

	if (typeof parsed !== "object" || parsed === null) return null;
	const obj = parsed as Record<string, unknown>;

	if (obj.v !== 1) return null;

	const { host, port, password } = obj;
	if (typeof host !== "string" || host.length === 0) return null;
	if (typeof port !== "string" && typeof port !== "number") return null;

	return {
		host,
		port: String(port),
		password: typeof password === "string" ? password : "",
		// Absent means plaintext: every QR issued before secure pairing existed
		// describes the plaintext bridge, and so does every LAN QR. Strict `=== true`
		// (not `Boolean(...)`) so a malformed value degrades to plaintext rather
		// than to a broken TLS attempt.
		secure: obj.secure === true,
	};
}

// Merges a scanned pairing payload into the stored config. Kept here (not
// inline in the pair screen) because `packages/mobile` has no screen tests,
// only pure-module tests under `lib/` — logic left in the screen is
// unreachable by the suite.
export function applyPairingPayload(cfg: ServerConfig, parsed: ParsedPairing): ServerConfig {
	return {
		...cfg,
		host: parsed.host,
		httpPort: parsed.port,
		password: parsed.password || cfg.password,
		// Always taken from the payload, never inherited. This is what makes a flip
		// back to LAN clear the TLS flag a secure Tailscale scan set — without it,
		// a stale `secure: true` survives the scan and breaks the connection with a
		// misleading "reached nothing" error.
		secure: parsed.secure,
	};
}
