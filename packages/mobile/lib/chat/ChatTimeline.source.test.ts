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

describe("empty conversation", () => {
	it("uses the AO mascot instead of a generic chat icon", () => {
		const emptyConversation = source.slice(source.indexOf("function EmptyConversation"), source.indexOf("function Action"));
		expect(emptyConversation).toContain("<MascotLamp size={48} />");
		expect(emptyConversation).not.toContain('name="message-circle"');
	});
});

describe("elicitation typography", () => {
	it("matches the question to regular assistant copy", () => {
		// The family is required next to every weight now, so this pins the parts
		// that carry the intent: the question reads at assistant-copy size and weight.
		expect(source).toContain('fontSize: type.callout.fontSize, lineHeight: type.callout.lineHeight, fontWeight: "500" }');
		expect(source).toContain("inputRequest: { marginVertical: space.md, paddingHorizontal: space.hair, paddingTop: space.xs, paddingBottom: space.xl, gap: space.md }");
		expect(source).toContain('inputActions: { minHeight: 36, flexDirection: "row"');
	});
});
