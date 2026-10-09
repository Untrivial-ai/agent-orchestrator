import { describe, expect, it } from "vitest";
import type { AgentModelCatalog, AgentModelInfo } from "./api";
import { reviewerModelView } from "./reviewer-picker.types";

// Trimmed from GET /api/v1/agents/{id}/models on a live daemon.
function catalog(models: AgentModelInfo[], selectionMode: AgentModelCatalog["selectionMode"] = "catalog"): AgentModelCatalog {
	return { agentId: "agent", selectionMode, allowCustom: false, models, source: "live", stale: false, fetchedAt: "2026-10-08T00:00:00Z" };
}
const claude = catalog([
	{ id: "fable", label: "Fable 5.1" },
	{ id: "opus", label: "Opus" },
	{ id: "opus[1m]", label: "Opus (1M context)", isDefault: true },
]);
const codex = catalog([
	{ id: "gpt-6.1-sol", label: "GPT-6.1-Sol", isDefault: true },
	{ id: "gpt-5.6-sol", label: "GPT-5.6-Sol" },
]);
// Cursor's catalog marks no model as its default.
const cursor = catalog([
	{ id: "auto", label: "Auto" },
	{ id: "claude-fable-5-high", label: "Claude Fable 5 1M" },
]);

const selected = (view: ReturnType<typeof reviewerModelView>) => view.choices.filter((choice) => choice.selected).map((choice) => choice.id);

describe("reviewer model row (#5834)", () => {
	it("shows the catalog's default model when none is configured, with no separate default row", () => {
		const view = reviewerModelView(claude, "");
		expect(view.text).toBe("Opus (1M context)");
		expect(view.followAgent).toBeNull();
		expect(selected(view)).toEqual(["opus[1m]"]);
	});

	it("saves no override when the default model is picked, so the reviewer follows the agent's default", () => {
		expect(reviewerModelView(claude, "").choices.map((choice) => choice.value)).toEqual(["fable", "opus", ""]);
		// Picking the default clears a pinned model rather than pinning the default.
		const pinned = reviewerModelView(codex, "gpt-5.6-sol");
		expect(pinned.text).toBe("GPT-5.6-Sol");
		expect(selected(pinned)).toEqual(["gpt-5.6-sol"]);
		expect(pinned.choices.find((choice) => choice.id === "gpt-6.1-sol")?.value).toBe("");
	});

	it("offers \"Use agent model\" only when the catalog names no default", () => {
		const unset = reviewerModelView(cursor, "");
		expect(unset.text).toBe("Use agent model");
		expect(unset.followAgent).toEqual({ label: "Use agent model", selected: true });
		expect(selected(unset)).toEqual([]);
		const pinned = reviewerModelView(cursor, "auto");
		expect(pinned.text).toBe("Auto");
		expect(pinned.followAgent).toEqual({ label: "Use agent model", selected: false });
		expect(pinned.choices.map((choice) => choice.value)).toEqual(["auto", "claude-fable-5-high"]);
	});

	it("reads a configured \"default\" as unset", () => {
		expect(reviewerModelView(codex, "default").text).toBe("GPT-6.1-Sol");
		expect(selected(reviewerModelView(codex, "Default"))).toEqual(["gpt-6.1-sol"]);
		expect(reviewerModelView(cursor, "default").followAgent?.selected).toBe(true);
	});

	it("drops a \"default\" placeholder from the choices and does not treat it as the default model", () => {
		// The catalog normalizes a "Default (recommended)" entry to this shape (modelcatalog normalize).
		const view = reviewerModelView(catalog([{ id: "default", label: "default", isDefault: true }, ...cursor.models]), "");
		expect(view.choices.map((choice) => choice.id)).toEqual(["auto", "claude-fable-5-high"]);
		expect(view.text).toBe("Use agent model");
		expect(view.followAgent?.selected).toBe(true);
	});

	it("names a configured model the catalog does not list by its id, and checks nothing", () => {
		const view = reviewerModelView(codex, "gpt-7");
		expect(view.text).toBe("gpt-7");
		expect(selected(view)).toEqual([]);
	});

	it("speaks of modes for a mode catalog", () => {
		const view = reviewerModelView(catalog(cursor.models, "mode"), "");
		expect(view.title).toBe("Mode");
		expect(view.text).toBe("Use agent mode");
		expect(view.followAgent?.label).toBe("Use agent mode");
	});

	it("has no choices without a catalog, which hides the row", () => {
		expect(reviewerModelView(undefined, "gpt-5.6-sol").choices).toEqual([]);
	});
});
