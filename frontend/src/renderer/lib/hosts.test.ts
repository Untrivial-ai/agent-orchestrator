import { describe, expect, it } from "vitest";
import { isLocal, LOCAL_HOST, parseRefKey, refKey, type Ref } from "./hosts";

describe("refKey", () => {
	it("round-trips a local ref", () => {
		const ref: Ref = { host: LOCAL_HOST, id: "skyvern-cloud" };
		expect(refKey(ref)).toBe("local:skyvern-cloud");
		expect(parseRefKey(refKey(ref))).toEqual(ref);
	});

	it("round-trips a remote ref whose host url contains colons and slashes", () => {
		const ref: Ref = { host: "http://192.0.2.1:3011", id: "skyvern-cloud" };
		expect(parseRefKey(refKey(ref))).toEqual(ref);
	});

	it("round-trips an id that itself contains a colon", () => {
		// Ids come from another machine; nothing guarantees they are colon-free.
		const ref: Ref = { host: "http://192.0.2.1:3011", id: "weird:id" };
		expect(parseRefKey(refKey(ref))).toEqual(ref);
	});

	it("distinguishes the same id on two hosts", () => {
		expect(refKey({ host: LOCAL_HOST, id: "skyvern-cloud" })).not.toBe(
			refKey({ host: "http://192.0.2.1:3011", id: "skyvern-cloud" }),
		);
	});
});

describe("isLocal", () => {
	it("is true only for the local host", () => {
		expect(isLocal(LOCAL_HOST)).toBe(true);
		expect(isLocal("http://192.0.2.1:3011")).toBe(false);
	});
});
