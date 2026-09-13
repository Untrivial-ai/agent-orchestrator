import { describe, expect, it, vi } from "vitest";

// mux.ts imports ./config, which reaches React Native's Flow-typed sources
// through these two. Same preamble as lib/chat/eventCursor.test.ts.
vi.mock("@react-native-async-storage/async-storage", () => ({
	default: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() },
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(), setItemAsync: vi.fn(), deleteItemAsync: vi.fn(),
}));

import { base64ToBytes, bytesToBase64 } from "./mux";

/**
 * Every literal below was produced by Go's `base64.StdEncoding`, the daemon's
 * only encode site for terminal payloads (backend/internal/terminal/manager.go:449),
 * rather than by bytesToBase64. Pinning the wire contract to the encoder we ship
 * would let both sides drift together without a test noticing.
 */
const WIRE: { name: string; bytes: number[]; b64: string }[] = [
	// Reachable today: mux.ts decodes String(msg.data ?? "").
	{ name: "an empty frame", bytes: [], b64: "" },
	{ name: "a one-byte tail (==)", bytes: [0x41], b64: "QQ==" },
	{ name: "a two-byte tail (=)", bytes: [0x41, 0x42], b64: "QUI=" },
	{ name: "an exact three-byte group", bytes: [0x41, 0x42, 0x43], b64: "QUJD" },
	{
		// A real PTY chunk: NUL, CSI 31m, and bytes above 0x7f that are not valid
		// UTF-8 on their own - the reason the wire is base64 at all.
		name: "an ANSI chunk carrying high bytes",
		bytes: [0x00, 0x1b, 0x5b, 0x33, 0x31, 0x6d, 0xff, 0xfe, 0x7f, 0x80, 0x0a],
		b64: "ABtbMzFt//5/gAo=",
	},
];

describe("base64ToBytes", () => {
	for (const { name, bytes, b64 } of WIRE) {
		it(`decodes ${name} exactly as the daemon encoded it`, () => {
			expect(Array.from(base64ToBytes(b64))).toEqual(bytes);
		});
	}

	it("preserves every byte value 0x00-0xff", () => {
		// Go: base64.StdEncoding.EncodeToString(0..255). A decoder that masked or
		// sign-extended the high half would show up here and nowhere else.
		const all256 =
			"AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8gISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0+P0BB" +
			"QkNERUZHSElKS0xNTk9QUVJTVFVWV1hZWltcXV5fYGFiY2RlZmdoaWprbG1ub3BxcnN0dXZ3eHl6e3x9fn+AgYKD" +
			"hIWGh4iJiouMjY6PkJGSk5SVlpeYmZqbnJ2en6ChoqOkpaanqKmqq6ytrq+wsbKztLW2t7i5uru8vb6/wMHCw8TF" +
			"xsfIycrLzM3Oz9DR0tPU1dbX2Nna29zd3t/g4eLj5OXm5+jp6uvs7e7v8PHy8/T19vf4+fr7/P3+/w==";
		const got = base64ToBytes(all256);
		expect(got).toHaveLength(256);
		expect(Array.from(got)).toEqual(Array.from({ length: 256 }, (_, i) => i));
	});

	it("decodes a full 32 KiB frame", () => {
		// The daemon reads PTY output into a 32 KiB buffer (attachment.go:196), so
		// this is the largest single frame the phone has to decode.
		const bytes = Uint8Array.from({ length: 32 * 1024 }, (_, i) => i & 0xff);
		const b64 = bytesToBase64(bytes);
		expect(b64).toHaveLength(43692);
		expect(b64.slice(0, 32)).toBe("AAECAwQFBgcICQoLDA0ODxAREhMUFRYX");
		expect(b64.slice(-32)).toBe("6err7O3u7/Dx8vP09fb3+Pn6+/z9/v8=");
		expect(base64ToBytes(b64)).toEqual(bytes);
	});

	it("yields an empty frame for a payload it cannot decode, rather than throwing", () => {
		// atob rejects input the previous decoder silently filtered out. `handle` is
		// called outside the JSON.parse guard in ws.onmessage, so a throw would escape
		// the socket callback; drop the frame instead, as that guard already does.
		expect(() => base64ToBytes("not base64!")).not.toThrow();
		expect(base64ToBytes("not base64!")).toEqual(new Uint8Array(0));
		expect(base64ToBytes("QQ=")).toEqual(new Uint8Array(0)); // wrong padding
	});

	it("still decodes a payload carrying whitespace, as the prefilter it replaced did", () => {
		// atob strips ASCII whitespace itself, so dropping the prefilter loses nothing.
		// vitest runs on Node, so every atob case here was re-checked on the
		// simulator's Hermes (250829098.0.17) and behaves identically.
		expect(base64ToBytes("QU\nJD")).toEqual(new Uint8Array([0x41, 0x42, 0x43]));
		expect(base64ToBytes("QU JD")).toEqual(new Uint8Array([0x41, 0x42, 0x43]));
	});
});

describe("bytesToBase64", () => {
	for (const { name, bytes, b64 } of WIRE) {
		it(`encodes ${name} the way Go's StdEncoding decodes it`, () => {
			expect(bytesToBase64(Uint8Array.from(bytes))).toBe(b64);
		});
	}

	it("round-trips arbitrary non-UTF8 bytes", () => {
		const bytes = new Uint8Array([0x00, 0x1b, 0x5b, 0xff, 0xfe, 0x7f, 0x41]);
		expect(base64ToBytes(bytesToBase64(bytes))).toEqual(bytes);
	});
});
