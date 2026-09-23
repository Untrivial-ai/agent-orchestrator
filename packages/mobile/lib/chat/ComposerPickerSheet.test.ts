import { describe, expect, it } from "vitest";
import { sheetInset } from "../tokens";
import { composerSheetContentStyle } from "./chatLayout";

describe("composer picker native sheet layout", () => {
	it("uses the same safe top inset as the project picker", () => {
		// Compare against the shared sheet inset rather than a literal, so the two
		// sheets cannot drift apart the next time the ladder moves.
		expect(composerSheetContentStyle).toMatchObject({
			paddingHorizontal: sheetInset.horizontal,
			paddingTop: sheetInset.top,
			paddingBottom: sheetInset.bottom,
		});
	});
});
