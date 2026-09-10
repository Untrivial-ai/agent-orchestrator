import { describe, expect, it } from "vitest";
import { buildPairingOffer, pairingCodeUrl } from "./pairing-payload";

const base = {
	endpoints: [
		{ kind: "lan" as const, host: "192.168.1.42", port: 3011, secure: false },
		{ kind: "tunnel" as const, host: "abc.trycloudflare.com", port: 443, secure: true },
	],
	password: "pw-123",
	hostId: "h_x",
	name: "mbp",
	platform: "darwin",
};

function decodeFragment(url: string) {
	const code = url.slice(url.indexOf("#") + 1);
	const b64 = code.replace(/-/g, "+").replace(/_/g, "/");
	const pad = b64.length % 4 === 0 ? "" : "=".repeat(4 - (b64.length % 4));
	return JSON.parse(atob(b64 + pad));
}

describe("buildPairingOffer", () => {
	// Every reachable address goes into the code, because the phone races them.
	// A code naming a single address goes stale as soon as the machine moves.
	it("carries every advertised endpoint", () => {
		const offer = buildPairingOffer(base);

		expect(offer.v).toBe(2);
		expect(offer.endpoints).toHaveLength(2);
		expect(offer.hostId).toBe("h_x");
		expect(offer.token).toBe("pw-123");
	});

	it("refuses to build an offer with nothing to connect to", () => {
		expect(() => buildPairingOffer({ ...base, endpoints: [] })).toThrow();
	});
});

describe("pairingCodeUrl", () => {
	// The payload rides in the fragment so it is never sent to a server: a
	// scanned https link must not put the connection token into web logs or a
	// referrer header.
	it("puts the payload in the fragment, not the query", () => {
		const url = pairingCodeUrl(buildPairingOffer(base), "https://aoagents.dev/pair");

		expect(url.split("#")[0]).toBe("https://aoagents.dev/pair");
		expect(url.split("#")[0]).not.toContain("pw-123");
	});

	it("survives a round trip back to the original offer", () => {
		const offer = buildPairingOffer(base);
		expect(decodeFragment(pairingCodeUrl(offer, "aomobile://pair"))).toEqual(offer);
	});

	// Padding is stripped to keep the QR small; the phone restores it.
	it("emits unpadded base64url so the QR stays small", () => {
		const code = pairingCodeUrl(buildPairingOffer(base), "aomobile://pair").split("#")[1];

		expect(code).not.toContain("=");
		expect(code).not.toContain("+");
		expect(code).not.toContain("/");
	});
});
