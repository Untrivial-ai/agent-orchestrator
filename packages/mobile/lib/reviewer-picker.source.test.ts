import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const actions = readFileSync(new URL("../app/sheets/review-actions.tsx", import.meta.url), "utf8");
const pickers = {
	"reviewer-picker.tsx": readFileSync(new URL("./reviewer-picker.tsx", import.meta.url), "utf8"),
	"reviewer-picker.ios.tsx": readFileSync(new URL("./reviewer-picker.ios.tsx", import.meta.url), "utf8"),
};

// #5834: the model row's decisions live in reviewerModelView; both pickers only
// render what it returns, so they cannot drift from it or from each other.
describe("reviewer model row wiring", () => {
	it.each(Object.entries(pickers))("%s renders the resolved view", (_name, source) => {
		expect(source).not.toContain("Provider default");
		expect(source).toContain("{model.text}");
		expect(source).toMatch(/\{model\.followAgent \?/);
		// "Use agent model" clears the override.
		expect(source).toMatch(/(onSelectModel|chooseModel)\(""\)/);
		expect(source).toContain("model.choices.map((choice) =>");
		expect(source).toMatch(/choice\.selected/);
		// Picking the catalog's default must save its value (""), not its id.
		expect(source).toMatch(/\(choice\.value\)/);
		expect(source).not.toMatch(/\(choice\.id\)/);
	});

	it("builds the view from the reviewer's catalog and its configured model or mode", () => {
		expect(actions).toContain("model={reviewerModelView(effectiveReviewer ? models : undefined, (models?.selectionMode === \"mode\" ? reviewerConfig.mode : reviewerConfig.model) ?? \"\")}");
	});
});
