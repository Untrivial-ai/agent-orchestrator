import { buildPairingOffer, pairingCodeUrl } from "../../lib/pairing-payload";

/**
 * Decoy payloads for the placeholder shown while remote access comes up.
 *
 * The placeholder is a real QR, not a drawing of one. Standing the tunnel up
 * takes ~30s, and the earlier attempt — a hand-drawn grid of squares — could
 * never resolve into the finished code: it had its own module size, its own
 * corner markers and no logo, so the moment the real QR arrived one image was
 * swapped for a visibly different one.
 *
 * Encoding a payload of the same *shape and length* as a genuine offer makes
 * qr-code-styling choose the same QR version, so the placeholder and the real
 * code share a module count, a dot size, corner markers and a logo cutout.
 * They differ only in which modules are dark, which is exactly what lets one
 * dissolve into the other.
 *
 * Nothing here is scannable in any useful sense: the hosts are reserved
 * documentation addresses and the token is decoy bytes.
 */

/** Matches the base ConnectMobileContent builds real codes on. */
export const PAIRING_LINK_BASE_FOR_SCRAMBLE = "aomobile://pair";

/** Deterministic, so a re-render reproduces the stack instead of reshuffling. */
function pseudoRandom(seed: number): () => number {
	let s = seed * 2654435761;
	return () => {
		s = (s * 1664525 + 1013904223) % 4294967296;
		return s / 4294967296;
	};
}

function hex(next: () => number, length: number): string {
	let out = "";
	while (out.length < length) out += Math.floor(next() * 16).toString(16);
	return out.slice(0, length);
}

/**
 * One decoy offer. The endpoint shapes mirror a real machine — a LAN address,
 * a tailnet address, a tunnel hostname — because their combined length is what
 * sets the QR version.
 */
function scrambleCode(index: number): string {
	const next = pseudoRandom(index + 1);
	const label = hex(next, 12);
	const offer = buildPairingOffer({
		// 192.0.2.0/24 and 198.51.100.0/24 are reserved for documentation, so
		// these cannot collide with anything real on a user's network.
		endpoints: [
			{ kind: "lan", host: `192.0.2.${1 + Math.floor(next() * 250)}`, port: 3011, secure: false },
			{ kind: "tailscale", host: `198.51.100.${1 + Math.floor(next() * 250)}`, port: 3011, secure: false },
			{ kind: "tunnel", host: `${label}-${hex(next, 10)}.invalid`, port: 443, secure: true },
		],
		password: hex(next, 16),
		hostId: `h_${hex(next, 8)}-${hex(next, 4)}-${hex(next, 4)}-${hex(next, 4)}-${hex(next, 12)}`,
		name: `machine-${hex(next, 6)}`,
		platform: "darwin",
	});
	return pairingCodeUrl(offer, PAIRING_LINK_BASE_FOR_SCRAMBLE);
}

export function scramblePairingCodes(count: number): string[] {
	return Array.from({ length: count }, (_, i) => scrambleCode(i));
}
