import { describe, expect, it } from "vitest";
import { PAIRING_LINK_BASE_FOR_SCRAMBLE, scramblePairingCodes } from "./qrScramble";
import { buildPairingOffer, pairingCodeUrl } from "../../lib/pairing-payload";

describe("scramble payloads for the pairing placeholder", () => {
	it("produces the requested number of codes", () => {
		expect(scramblePairingCodes(5)).toHaveLength(5);
	});

	// Deterministic: React re-renders must not reshuffle the stack, or the
	// crossfade turns into flicker.
	it("is stable across calls", () => {
		expect(scramblePairingCodes(4)).toEqual(scramblePairingCodes(4));
	});

	it("gives every layer a different pattern", () => {
		expect(new Set(scramblePairingCodes(6)).size).toBe(6);
	});

	// The whole point: the placeholder has to encode a payload of the same
	// shape and length as a real offer, so qr-code-styling picks the same QR
	// version. A different version means a different module count, and the
	// resolve would visibly jump from a coarse grid to a fine one.
	it("matches the length of a real pairing code closely enough to share a QR version", () => {
		const real = pairingCodeUrl(
			buildPairingOffer({
				endpoints: [
					{ kind: "lan", host: "192.168.29.189", port: 3011, secure: false },
					{ kind: "tailscale", host: "100.72.46.7", port: 3011, secure: false },
					{ kind: "tunnel", host: "stem-til-house-doing.trycloudflare.com", port: 443, secure: true },
				],
				password: "3f9a2b7c1d4e5f60",
				hostId: "h_70884810-088d-4b10-b3ab-0b19c47bb17c",
				name: "Prasads-MacBook-Pro",
				platform: "darwin",
			}),
			PAIRING_LINK_BASE_FOR_SCRAMBLE,
		);
		for (const code of scramblePairingCodes(6)) {
			expect(Math.abs(code.length - real.length) / real.length).toBeLessThan(0.12);
		}
	});

	it("carries no real credential material", () => {
		for (const code of scramblePairingCodes(4)) {
			expect(code.startsWith(`${PAIRING_LINK_BASE_FOR_SCRAMBLE}#`)).toBe(true);
			expect(code).not.toContain("trycloudflare.com");
		}
	});
});
