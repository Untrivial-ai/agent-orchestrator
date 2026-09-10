import { describe, expect, it } from "vitest";
import { formatTimeCompact, formatTokenCount } from "./formatting";

describe("formatting", () => {
	it.each([
		[0, "0 tok"],
		[999, "999 tok"],
		[1_000, "1K tok"],
		[12_400, "12.4K tok"],
		[1_250_000, "1.3M tok"],
	])("formats %d tokens as %s", (tokens, expected) => {
		expect(formatTokenCount(tokens)).toBe(expected);
	});

	it("formats relative time with an injected clock and labels", () => {
		const result = formatTimeCompact("2026-01-01T00:00:00Z", {
			now: new Date("2026-01-01T00:42:00Z"),
			translate: (key, values) => `${key}:${values?.n ?? ""}`,
		});

		expect(result).toBe("time.minutesAgo:42");
	});
});
