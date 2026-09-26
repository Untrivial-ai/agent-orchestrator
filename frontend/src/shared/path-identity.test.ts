import { mkdirSync, mkdtempSync, symlinkSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { canonicalPathInside, canonicalPathKey, sameCanonicalPath } from "./path-identity";

describe("path identity", () => {
	it("treats filesystem aliases as the same path", () => {
		const root = mkdtempSync(path.join(os.tmpdir(), "ao-path-identity-"));
		const checkout = path.join(root, "checkout");
		const alias = path.join(root, "alias");
		mkdirSync(checkout);
		symlinkSync(checkout, alias, process.platform === "win32" ? "junction" : "dir");

		expect(sameCanonicalPath(checkout, alias)).toBe(true);
	});

	it("canonicalizes the nearest existing ancestor", () => {
		const root = mkdtempSync(path.join(os.tmpdir(), "ao-path-identity-missing-"));
		const checkout = path.join(root, "checkout");
		const alias = path.join(root, "alias");
		mkdirSync(checkout);
		symlinkSync(checkout, alias, process.platform === "win32" ? "junction" : "dir");

		expect(canonicalPathKey(path.join(alias, "missing", "ao"))).toBe(
			path.join(checkout, "missing", "ao"),
		);
		expect(canonicalPathInside(path.join(alias, "backend", "ao"), checkout)).toBe(true);
	});

	it("does not treat sibling prefixes as descendants", () => {
		const root = path.join(os.tmpdir(), "ao-path-prefix");
		expect(canonicalPathInside(`${root}-other`, root)).toBe(false);
	});
});
