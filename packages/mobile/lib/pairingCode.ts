import type { Endpoint, EndpointKind } from "./endpoints";

/**
 * What a pairing code carries.
 *
 * Endpoints are a list because the phone races them; a code that named one
 * address would go stale the moment the machine changed networks.
 */
export type PairingOffer = {
	v: 2;
	/** Empty for a legacy code — v1 predates host ids, so the machine adopts
	 * one on its first connect. */
	hostId: string;
	name: string;
	platform: string;
	endpoints: Endpoint[];
	token: string;
};

const KINDS: readonly EndpointKind[] = ["lan", "tailscale", "tunnel", "relay"];

/** base64url, unpadded — shorter, and safe inside a URL fragment. */
function toBase64Url(json: string): string {
	return btoa(json).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function fromBase64Url(code: string): string {
	const b64 = code.replace(/-/g, "+").replace(/_/g, "/");
	// Some JS runtimes reject unpadded input, and the desktop strips padding to
	// keep the QR small, so it has to be restored here.
	const pad = b64.length % 4 === 0 ? "" : "=".repeat(4 - (b64.length % 4));
	return atob(b64 + pad);
}

/** Encodes an offer for a QR or a copyable code. */
export function encodePairingCode(offer: PairingOffer): string {
	return toBase64Url(JSON.stringify(offer));
}

/**
 * Wraps an offer in a link.
 *
 * The payload goes in the fragment, never the query: fragments are not sent to
 * servers, so the token stays out of web logs and referrer headers even when
 * the https form is scanned by a browser.
 */
export function pairingUrl(offer: PairingOffer, base: string): string {
	return `${base}#${encodePairingCode(offer)}`;
}

function isEndpoint(v: unknown): v is Endpoint {
	if (typeof v !== "object" || v === null) return false;
	const e = v as Record<string, unknown>;
	return (
		typeof e.kind === "string" &&
		KINDS.includes(e.kind as EndpointKind) &&
		typeof e.host === "string" &&
		e.host.length > 0 &&
		typeof e.port === "number" &&
		typeof e.secure === "boolean"
	);
}

/** Pulls the encoded payload out of whatever the user actually handed us. */
function extractCode(input: string): string | null {
	const trimmed = input.trim();
	if (!trimmed) return null;
	const hash = trimmed.indexOf("#");
	if (hash !== -1) {
		// Only our own links. Another app's deep link with a fragment must not
		// be treated as a pairing code.
		if (!/^(aomobile:\/\/pair|https?:\/\/[^/]+\/pair)/i.test(trimmed)) return null;
		return trimmed.slice(hash + 1) || null;
	}
	if (/^[a-z][a-z0-9+.-]*:\/\//i.test(trimmed)) return null; // a URL with no payload
	return trimmed;
}

/**
 * Whether this is a v1 code from a desktop that has not been updated.
 *
 * v1 is no longer accepted. Those codes predate the identity probe the race
 * uses to verify a machine before sending it a credential, so a daemon old
 * enough to emit one answers 404 to every probe and the race can never
 * complete — the compatibility was advertised but unreachable in practice.
 *
 * Recognised, though, so the app can say "update AO on your computer" instead
 * of "this is not a pairing code".
 */
export function isLegacyPairingCode(raw: string): boolean {
	let parsed: unknown;
	try {
		parsed = JSON.parse(raw.trim());
	} catch {
		return false;
	}
	if (typeof parsed !== "object" || parsed === null) return false;
	const o = parsed as Record<string, unknown>;
	return o.v === 1 && typeof o.host === "string" && o.host.length > 0;
}

/**
 * Reads a pairing code from a deep link, an https universal link, or a pasted
 * bare code. v1 payloads are not accepted — see isLegacyPairingCode.
 *
 * Returns null rather than throwing: the input is whatever a camera happened to
 * see, so anything unrecognised is simply not a pairing code.
 */
export function parsePairingCode(input: string): PairingOffer | null {
	const code = extractCode(input);
	if (!code) return null;

	let json: string;
	try {
		json = fromBase64Url(code);
	} catch {
		return null;
	}

	let parsed: unknown;
	try {
		parsed = JSON.parse(json);
	} catch {
		return null;
	}
	if (typeof parsed !== "object" || parsed === null) return null;
	const o = parsed as Record<string, unknown>;
	if (o.v !== 2) return null;
	if (typeof o.hostId !== "string") return null;
	if (!Array.isArray(o.endpoints)) return null;

	const endpoints = o.endpoints.filter(isEndpoint);
	// A code with nothing to connect to is not usable.
	if (endpoints.length === 0) return null;

	return {
		v: 2,
		hostId: o.hostId,
		name: typeof o.name === "string" ? o.name : "",
		platform: typeof o.platform === "string" ? o.platform : "",
		endpoints,
		token: typeof o.token === "string" ? o.token : "",
	};
}
