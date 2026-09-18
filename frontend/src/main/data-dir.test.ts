import { describe, expect, it } from "vitest";
import { resolveDesktopDataDir } from "./data-dir";

describe("resolveDesktopDataDir", () => {
	it("resolves one absolute data directory against the daemon launch cwd", () => {
		expect(resolveDesktopDataDir({ AO_DATA_DIR: "relative-data" }, "/home/ao", "/work/checkout", false)).toBe(
			"/work/checkout/relative-data",
		);
		expect(resolveDesktopDataDir({}, "/home/ao", "/work/checkout", true)).toBe("/home/ao/.ao/data");
		expect(resolveDesktopDataDir({}, "/home/ao", "/work/checkout", false)).toBe("/home/ao/.ao/dev/data");
	});
});
