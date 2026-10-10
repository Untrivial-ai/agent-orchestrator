import { describe, expect, it } from "vitest";
import { accountChatModels, claudeRowRunsAccountModel, foldClaudeAliasDefault, splitClaudeModels } from "./agent-model-choices";

type TestModel = { id: string; label: string; isDefault?: boolean };
const model = (label: string, id = label.toLowerCase().replace(/[ .]/g, "-"), extra: { isDefault?: boolean } = {}): TestModel => ({
	id,
	label,
	...extra,
});

describe("splitClaudeModels", () => {
	it("leads with the newest model of each family in Fable, Opus, Sonnet, Haiku order", () => {
		const { current, other } = splitClaudeModels(
			["Opus 4.9", "Haiku 4.5", "Opus 4.10", "Sonnet 5", "Opus 5.5", "Fable 5.1", "Fable 5"].map((label) => model(label)),
		);
		expect(current.map((item) => item.label)).toEqual(["Fable 5.1", "Opus 5.5", "Sonnet 5", "Haiku 4.5"]);
		expect(other.map((item) => item.label)).toEqual(["Fable 5", "Opus 4.10", "Opus 4.9"]);
	});

	it("reads versions from provider IDs, ignoring snapshot dates and suffixes", () => {
		const ids = [
			"us.anthropic.claude-opus-4-5-v1:0",
			"claude-opus-4-8-20260101",
			"us.anthropic.claude-opus-5-5-v1:0",
		];
		const { current, other } = splitClaudeModels(ids.map((id) => model(id, id)));
		expect(current.map((item) => item.id)).toEqual(["us.anthropic.claude-opus-5-5-v1:0"]);
		expect(other.map((item) => item.id)).toEqual(["claude-opus-4-8-20260101", "us.anthropic.claude-opus-4-5-v1:0"]);
	});

	it("keeps models without a family version in the current list", () => {
		const { current, other } = splitClaudeModels([model("Opus (1M context)", "opus[1m]"), model("Opus 5.5"), model("My gateway model", "gw")]);
		expect(current.map((item) => item.label)).toEqual(["Opus 5.5", "Opus (1M context)", "My gateway model"]);
		expect(other).toEqual([]);
	});

	// Fallback aliases now carry the version discovery resolved, so a context
	// variant of the newest Opus must stay a current choice, not an older one.
	it("keeps a versioned context variant alongside its family's newest model", () => {
		const aliases = [model("Sonnet 5.5", "sonnet"), model("Fable 5.1", "fable"), model("Opus 5.5", "opus"), model("Haiku 5.5", "haiku"), model("Opus 5.5 (1M context)", "opus[1m]")];
		const { current, other } = splitClaudeModels(aliases);
		expect(current.map((item) => item.id)).toEqual(["fable", "opus", "opus[1m]", "sonnet", "haiku"]);
		expect(other).toEqual([]);
	});
});

describe("foldClaudeAliasDefault", () => {
	it("marks the newest model of the configured alias family as the default instead of listing the alias", () => {
		const alias = model("Sonnet", "sonnet", { isDefault: true });
		const folded = foldClaudeAliasDefault([model("Sonnet 4.6"), model("Sonnet 5.5"), model("Opus 5.5"), alias]);
		expect(folded.map((item) => item.id)).toEqual(["sonnet-4-6", "sonnet-5-5", "opus-5-5"]);
		expect(folded.find((item) => item.isDefault)?.label).toBe("Sonnet 5.5");
		const noSibling = [alias, model("Opus 5.5")];
		expect(foldClaudeAliasDefault(noSibling)).toBe(noSibling);
	});

	it("never folds a configured alias into a context variant", () => {
		const models = [model("Opus 5.5", "opus", { isDefault: true }), model("Opus 5.5 (1M context)", "opus[1m]"), model("Sonnet 5.5", "sonnet")];
		expect(foldClaudeAliasDefault(models)).toBe(models);
	});
});

describe("claudeRowRunsAccountModel", () => {
	// What Claude Code reported when it was started with the account's models.
	const rows = [
		{ value: "default", name: "Default (recommended)", description: "Opus" },
		{ value: "opus", name: "Opus", description: "Opus 5.5 · Best for everyday, complex tasks · $4/$20 per Mtok" },
		{ value: "opus[1m]", name: "Opus (1M context)", description: "Opus 5.5 with 1M context" },
		{ value: "claude-fable-5-1", name: "Fable", description: "Fable 5.1 · Most capable for your hardest and longest-running tasks" },
		{ value: "claude-fable-5[1m]", name: "Fable", description: "" },
		{ value: "sonnet", name: "Sonnet", description: "Sonnet 5.5 · Efficient for routine tasks · $2/$10 per Mtok" },
		{ value: "haiku", name: "Haiku", description: "Haiku 4.5 · Fastest for quick answers · $1/$5 per Mtok" },
		{ value: "claude-opus-4-7", name: "Opus 4.7", description: "Newer version available · select Opus for Opus 5.5" },
		{ value: "claude-opus-5", name: "Opus 5", description: "Newer version available · select Opus for Opus 5.5" },
		{ value: "claude-fable-5-dd-5.5-tpg", name: "claude-fable-5-dd-5.5-tpg", description: "" },
	];
	const account = [
		{ id: "claude-haiku-4-5-20251001", label: "Haiku 4.5" },
		{ id: "claude-opus-4-7", label: "Opus 4.7" },
		{ id: "claude-opus-5", label: "Opus 5" },
		{ id: "claude-fable-5", label: "Fable 5" },
		{ id: "claude-fable-5-1", label: "Fable 5.1" },
		{ id: "claude-opus-5-5", label: "Opus 5.5" },
		{ id: "claude-sonnet-5-5", label: "Sonnet 5.5" },
		{ id: "claude-haiku-5-5", label: "Haiku 5.5" },
	];
	const kept = (models: typeof account) => rows.filter((row) => claudeRowRunsAccountModel(row, models)).map((row) => row.value);

	it("keeps a row only when the release it runs is one of the account's models", () => {
		expect(kept(account)).toEqual(["opus", "opus[1m]", "claude-fable-5-1", "claude-fable-5[1m]", "sonnet", "haiku", "claude-opus-4-7", "claude-opus-5"]);
		// An account without the newest Opus and Sonnet: "opus" would ask for Opus 5.5, which it does not list.
		const older = account.filter((model) => !/5-5$/.test(model.id) || model.id.includes("haiku"));
		expect(kept(older)).toEqual(["claude-fable-5-1", "claude-fable-5[1m]", "haiku", "claude-opus-4-7", "claude-opus-5"]);
	});
});

describe("accountChatModels", () => {
	const live = [
		{ id: "gpt-b", displayName: "B, as the agent names it", default: true, efforts: ["low"], defaultEffort: "low" },
		{ id: "agent-only", displayName: "Agent only", default: false },
	];

	it("offers the account's models under the account's names, marking the one the chat is on", () => {
		expect(accountChatModels([{ id: "gpt-a", label: "GPT A", efforts: ["low", "high"] }, { id: "gpt-b", label: "GPT B" }], live)).toEqual([
			{ id: "gpt-a", displayName: "GPT A", default: false, efforts: ["low", "high"], defaultEffort: undefined },
			// The account names no efforts for it, so the running chat's are kept.
			{ id: "gpt-b", displayName: "GPT B", default: true, efforts: ["low"], defaultEffort: "low" },
		]);
	});

	it("keeps the model the chat is on listed when the account's list lacks it", () => {
		const models = accountChatModels([{ id: "gpt-a", label: "GPT A" }], live);
		expect(models.map((model) => model.id)).toEqual(["gpt-b", "gpt-a"]);
	});

	it("falls back to the agent's own list until the account's models are known", () => {
		expect(accountChatModels([], live)).toBe(live);
		expect(accountChatModels([{ id: "default", label: "Default" }], live)).toBe(live);
	});
});
