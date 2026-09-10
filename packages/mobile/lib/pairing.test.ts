import { describe, expect, it } from "vitest";
import { applyPairingPayload, parsePairingPayload } from "./pairing";
import type { ServerConfig } from "./config";

const cfg = (over: Partial<ServerConfig> = {}): ServerConfig => ({
	host: "",
	httpPort: "3011",
	muxPort: "14801",
	secure: false,
	password: "",
	...over,
});

describe("parsePairingPayload", () => {
	it("reads secure: true from the payload", () => {
		const parsed = parsePairingPayload(
			JSON.stringify({ v: 1, host: "my-pc.tail1234.ts.net", port: 443, password: "pw", secure: true }),
		);
		expect(parsed?.secure).toBe(true);
	});

	it("defaults secure to false when absent", () => {
		const parsed = parsePairingPayload(JSON.stringify({ v: 1, host: "192.168.1.5", port: 3011 }));
		expect(parsed?.secure).toBe(false);
	});

	// Absent means plaintext, and so does malformed: a truthy-but-not-`true` value
	// must degrade to plaintext rather than attempt a broken TLS connection.
	it.each(["yes", 1, null])("treats malformed secure (%j) as false", (bad) => {
		const parsed = parsePairingPayload(JSON.stringify({ v: 1, host: "192.168.1.5", port: 3011, secure: bad }));
		expect(parsed?.secure).toBe(false);
	});

	it("still rejects a v other than 1", () => {
		expect(parsePairingPayload(JSON.stringify({ v: 2, host: "h", port: 1 }))).toBeNull();
	});

	it("still rejects an empty host", () => {
		expect(parsePairingPayload(JSON.stringify({ v: 1, host: "", port: 1 }))).toBeNull();
	});

	it("still accepts numeric and string ports, returned as a string", () => {
		expect(parsePairingPayload(JSON.stringify({ v: 1, host: "h", port: 3011 }))?.port).toBe("3011");
		expect(parsePairingPayload(JSON.stringify({ v: 1, host: "h", port: "3011" }))?.port).toBe("3011");
	});

	it("still defaults a missing password to an empty string", () => {
		expect(parsePairingPayload(JSON.stringify({ v: 1, host: "h", port: 1 }))?.password).toBe("");
	});
});

describe("applyPairingPayload", () => {
	// The flip-back-to-LAN trap: without `secure` always taken from the payload
	// (never inherited), scanning a plaintext LAN QR after enabling secure
	// Tailscale pairing would silently keep `secure: true` and break the
	// connection with a misleading "reached nothing" error.
	it("clears a stored secure: true when the scanned payload is plaintext", () => {
		const stored = cfg({ secure: true, host: "old-host", httpPort: "443" });
		const parsed = { host: "192.168.1.5", port: "3011", password: "", secure: false };
		expect(applyPairingPayload(stored, parsed).secure).toBe(false);
	});

	it("sets secure: true from a Tailscale payload", () => {
		const stored = cfg({ secure: false });
		const parsed = { host: "my-pc.tail1234.ts.net", port: "443", password: "", secure: true };
		expect(applyPairingPayload(stored, parsed).secure).toBe(true);
	});

	it("keeps the stored password when the payload's is empty", () => {
		const stored = cfg({ password: "stored-pw" });
		const parsed = { host: "h", port: "3011", password: "", secure: false };
		expect(applyPairingPayload(stored, parsed).password).toBe("stored-pw");
	});

	it("overrides the stored password when the payload has one", () => {
		const stored = cfg({ password: "stored-pw" });
		const parsed = { host: "h", port: "3011", password: "new-pw", secure: false };
		expect(applyPairingPayload(stored, parsed).password).toBe("new-pw");
	});
});
