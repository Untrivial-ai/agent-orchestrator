import { expect, test } from "vitest";
import { isNetErrorMessage } from "./update-support";

test("detects Chromium network-stack errors", () => {
	expect(isNetErrorMessage("net::ERR_FAILED")).toBe(true);
	expect(isNetErrorMessage("net::ERR_CONNECTION_RESET")).toBe(true);
	// Anchored at the start — a net:: substring elsewhere is not the wedge signature.
	expect(isNetErrorMessage("Error: net::ERR_FAILED")).toBe(false);
	expect(isNetErrorMessage("HttpError: 404")).toBe(false);
	expect(isNetErrorMessage(undefined)).toBe(false);
});
