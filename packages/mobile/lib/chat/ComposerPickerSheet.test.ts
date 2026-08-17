import { describe, expect, it } from "vitest";
import { composerSheetContentStyle } from "./chatLayout";

describe("composer picker native sheet layout", () => {
	it("uses the same safe top inset as the project picker", () => {
		expect(composerSheetContentStyle).toMatchObject({ paddingHorizontal: 20, paddingTop: 22, paddingBottom: 24 });
	});
});
