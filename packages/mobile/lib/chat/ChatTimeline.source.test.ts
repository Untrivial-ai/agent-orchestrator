import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const source = readFileSync(new URL("./ChatTimeline.tsx", import.meta.url), "utf8");
const turnSummarySource = source.slice(
	source.indexOf("function TurnSummary"),
	source.indexOf("function TurnPlan"),
);

describe("chat turn summary styling", () => {
	it("shows the duration without a trailing divider line", () => {
		expect(turnSummarySource).toContain("Worked for ${duration}");
		expect(turnSummarySource).not.toContain("<View style={styles.ruleHalf} />");
	});
});

describe("streaming response layout", () => {
	it("keeps the live cursor inside the response instead of adding a new block", () => {
		expect(source).toContain('item.streaming ? `${item.text || ""} ▍`');
		expect(source).not.toContain("styles.streamingDot");
	});
});

describe("elicitation typography", () => {
	it("matches the question to regular assistant copy", () => {
		expect(source).toContain('inputQuestion: { color: t.textPrimary, fontSize: 16, lineHeight: 24, fontWeight: "500" }');
		expect(source).toContain("inputRequest: { marginVertical: 12, paddingHorizontal: 2, paddingTop: 6, paddingBottom: 20, gap: 12 }");
		expect(source).toContain('inputActions: { minHeight: 36, flexDirection: "row"');
	});
});
