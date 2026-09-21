import { describe, expect, it } from "vitest";
import { encodePairingCode, isLegacyPairingCode, parsePairingCode, pairingUrl } from "./pairingCode";

const offer = {
	v: 2 as const,
	hostId: "h_b3e07f31",
	name: "prasad-mbp",
	platform: "darwin",
	endpoints: [
		{ kind: "lan" as const, host: "192.168.1.42", port: 3011, secure: false },
		{ kind: "tunnel" as const, host: "abc.trycloudflare.com", port: 443, secure: true },
	],
	token: "pw-123",
};

describe("parsePairingCode", () => {
	it("round-trips an offer through the encoded form", () => {
		const got = parsePairingCode(encodePairingCode(offer));

		expect(got).not.toBeNull();
		expect(got?.hostId).toBe("h_b3e07f31");
		expect(got?.endpoints).toHaveLength(2);
		expect(got?.token).toBe("pw-123");
	});

	// The QR encodes a URL so the system camera can open the app. The payload
	// rides in the fragment, which browsers never send to a server, keeping the
	// token out of web logs and referrer headers.
	it("reads the payload out of a deep link fragment", () => {
		const got = parsePairingCode(pairingUrl(offer, "aomobile://pair"));

		expect(got?.hostId).toBe("h_b3e07f31");
	});

	it("reads the payload out of an https universal link", () => {
		const got = parsePairingCode(pairingUrl(offer, "https://aoagents.dev/pair"));

		expect(got?.hostId).toBe("h_b3e07f31");
	});

	// Someone who cannot scan copies the code from the desktop and pastes it.
	it("accepts a bare code with no URL around it", () => {
		const got = parsePairingCode(`   ${encodePairingCode(offer)}   `);

		expect(got?.hostId).toBe("h_b3e07f31");
	});

	// v1 support was removed rather than repaired: those codes predate the
	// identity probe the race uses to verify a machine before sending it a
	// credential, so a daemon old enough to emit one answers 404 to every probe
	// and the race can never complete. See isLegacyPairingCode.
	it("rejects a legacy v1 payload", () => {
		expect(
			parsePairingCode(JSON.stringify({ v: 1, host: "192.168.1.5", port: 3011, password: "old-pw" })),
		).toBeNull();
	});

	it.each([
		["empty", ""],
		["not a code", "hello world"],
		["a different app's link", "otherapp://pair#abc"],
		["valid base64 that is not an offer", btoa(JSON.stringify({ hello: "world" }))],
		["an offer from a newer major version", btoa(JSON.stringify({ v: 99, hostId: "x" }))],
		["an offer with no endpoints", btoa(JSON.stringify({ v: 2, hostId: "h_x", endpoints: [] }))],
	])("rejects %s", (_name, input) => {
		expect(parsePairingCode(input)).toBeNull();
	});

	// Desktop strips base64 padding for a smaller QR; some JS runtimes reject
	// unpadded input, so it has to be restored before decoding.
	it("decodes a payload whose base64 padding was stripped", () => {
		const padded = encodePairingCode(offer);
		expect(parsePairingCode(padded.replace(/=+$/, ""))?.hostId).toBe("h_b3e07f31");
	});
});

// A scheme mismatch between the desktop's QR and the app's registered scheme
// fails silently: the camera opens nothing and there is no error anywhere. Pin
// the parser to what app.json actually registers.
describe("deep link scheme", () => {
	it("matches the scheme the app registers in app.json", async () => {
		const appConfig = (await import("../app.json")) as unknown as {
			default: { expo: { scheme: string } };
		};
		const scheme = appConfig.default.expo.scheme;

		const got = parsePairingCode(pairingUrl(offer, `${scheme}://pair`));

		expect(got?.hostId).toBe("h_b3e07f31");
	});
});

// v1 support was dropped rather than repaired. The probe that verifies a
// machine's identity before sending it a credential does not exist on a daemon
// old enough to emit v1, so those codes could never complete a race — the
// compatibility was advertised but unreachable. Rejecting them outright is
// honest; what matters is that the user is told why.
describe("v1 pairing codes", () => {
	const v1 = JSON.stringify({ v: 1, host: "192.168.1.42", port: "3011", password: "pw" });

	it("no longer accepts a v1 payload", () => {
		expect(parsePairingCode(v1)).toBeNull();
	});

	it("recognises one well enough to explain the failure", () => {
		expect(isLegacyPairingCode(v1)).toBe(true);
	});

	it("does not mistake a v2 code for a legacy one", () => {
		const v2 = encodePairingCode({
			v: 2,
			hostId: "h_1",
			name: "mbp",
			platform: "darwin",
			endpoints: [{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false }],
			token: "pw",
		});
		expect(isLegacyPairingCode(v2)).toBe(false);
		expect(parsePairingCode(v2)).not.toBeNull();
	});

	it("does not mistake arbitrary text for a legacy one", () => {
		expect(isLegacyPairingCode("https://example.com")).toBe(false);
		expect(isLegacyPairingCode("{}")).toBe(false);
	});
});
