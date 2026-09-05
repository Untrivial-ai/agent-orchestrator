import { describe, expect, it } from "vitest";
import { groupModelFamilies, modelVersionLabel } from "./model-families";

describe("model families", () => {
	it("groups aliases and pinned versions without changing selectors", () => {
		const models = [{id:"opus",label:"Opus"},{id:"claude-opus-4-8",label:"Opus 4.8"},{id:"claude-opus-5[1m]",label:"Opus 5 (1M)"},{id:"claude-fable-5",label:"Fable 5"},{id:"claude-fable-5-1",label:"Fable 5.1"}];
		const groups = groupModelFamilies(models);
		expect(groups.map((g) => g.label)).toEqual(["Opus", "Fable"]);
		expect(groups[0].models.map((m) => m.id)).toEqual(models.slice(0,3).map((m)=>m.id));
		expect(groups[1].models).toEqual(models.slice(3));
	});
	it("keeps distinct providers and unfamiliar models selectable", () => {
		const groups=groupModelFamilies([{id:"a/claude-opus-5",label:"Opus 5"},{id:"b/claude-opus-5",label:"Opus 5"},{id:"my-custom-model",label:"Custom"}]);
		expect(groups.map((g)=>g.label)).toEqual(["Opus (a)","Opus (b)","Custom"]);
		expect(groups[2].nested).toBe(false);
	});
	it("renders a family directly when only one version is advertised", () => {
		const groups = groupModelFamilies([
			{ id: "gpt-6-astra", label: "GPT-6 Astra" },
			{ id: "claude-fable-5-1[1m]", label: "Fable 5.1" },
		]);
		expect(groups.map((group) => ({ label: group.label, nested: group.nested }))).toEqual([
			{ label: "Astra", nested: false },
			{ label: "Fable", nested: false },
		]);
	});
	it("labels an unprefixed family without truncating its selector", () => {
 expect(groupModelFamilies([{ id:"opus",label:"Opus" },{id:"anthropic/claude-opus-5",label:"Opus 5"}]).map(g=>g.label)).toEqual(["Opus (local)","Opus (anthropic)"]);
 });
 it("does not mistake an alias label for a pinned version", () => {
		expect(modelVersionLabel({id:"fable",label:"Fable 5.1"})).toBe("Fable 5.1 (provider alias)");
		expect(modelVersionLabel({id:"claude-fable-5-1",label:"Fable 5.1"})).toBe("Fable 5.1");
	});
});
