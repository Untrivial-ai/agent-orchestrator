import { describe, expect, it } from "vitest";

import { minimalBackButtonStyle } from "./navigationChrome";
import { radius } from "./tokens";

describe("minimal mobile navigation", () => {
	it("uses an icon-only native header target", () => {
		expect(minimalBackButtonStyle).toMatchObject({
			width: 44,
			height: 44,
			aspectRatio: 1,
			borderRadius: radius.pill,
			overflow: "hidden",
			alignItems: "center",
			justifyContent: "center",
		});
	});
});
