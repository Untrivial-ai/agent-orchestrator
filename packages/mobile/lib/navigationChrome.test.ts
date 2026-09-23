import { describe, expect, it } from "vitest";

import { minimalBackButtonStyle } from "./navigationChrome";

describe("minimal mobile navigation", () => {
	it("uses an icon-only native header target", () => {
		expect(minimalBackButtonStyle).toMatchObject({
			width: 38,
			height: 44,
			aspectRatio: 1,
			borderRadius: 22,
			overflow: "hidden",
			alignItems: "center",
			justifyContent: "center",
		});
	});
});
