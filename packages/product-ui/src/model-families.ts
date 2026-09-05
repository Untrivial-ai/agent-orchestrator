/** Presentation grouping only: selectors remain opaque, provider-owned values. */
export type FamilyModel = { id: string; label: string; provider?: string; description?: string };
export type ModelFamily<T extends FamilyModel> = { key: string; label: string; nested: boolean; models: T[] };

const families = ["Opus", "Fable", "Sonnet", "Haiku", "Mythos", "Astra", "Sol", "Terra", "Luna"];

export function groupModelFamilies<T extends FamilyModel>(models: readonly T[]): ModelFamily<T>[] {
	const groups = new Map<string, ModelFamily<T>>();
	for (const model of models) {
		const selector = model.id.split("/").at(-1) ?? model.id;
		const family = families.find((name) => new RegExp(`(?:^|[-_\\s])${name}(?:$|[-_\\s\\d\\[(])`, "i").test(selector))
			?? families.find((name) => new RegExp(`(?:^|[-_\\s])${name}(?:$|[-_\\s\\d\\[(])`, "i").test(model.label));
		const provider = model.provider ?? (model.id.includes("/") ? model.id.slice(0, model.id.lastIndexOf("/")) : "");
		const key = family ? `${provider.toLowerCase()}:${family}` : `model:${model.id}`;
		const group = groups.get(key) ?? { key, label: family ?? model.label, nested: false, models: [] };
		group.models.push(model);
		groups.set(key, group);
	}
	const result = [...groups.values()];
	const counts = new Map<string, number>();
 for (const group of result) counts.set(group.label, (counts.get(group.label) ?? 0) + 1);
	for (const group of result) {
		group.nested = group.models.length > 1;
		if ((counts.get(group.label) ?? 0) > 1) {
			const model = group.models[0];
			const provider = model.provider ?? (model.id.includes("/") ? model.id.slice(0, model.id.lastIndexOf("/")) : "");
			group.label = `${group.label} (${provider || "local"})`;
		}
	}
	return result;
}

export function modelVersionLabel(model: FamilyModel): string {
	const selector = model.id.split("/").at(-1) ?? model.id;
 const version = model.description?.match(/^(Opus|Fable|Sonnet|Haiku|Mythos) [0-9]+(?:\.[0-9]+)*(?: with 1M context)?/i)?.[0];
 const label = version && model.label.toLowerCase().includes(version.split(" ")[0].toLowerCase()) ? version : model.label;
	// An unversioned alias may have a friendly label naming today's version.
	// Preserve that label but do not imply the selector pins that version.
	return /^(?:claude-)?(?:opus|fable|sonnet|haiku|mythos)(?:\[[^\]]+\])?$/i.test(selector)
		? `${label} (provider alias)` : label;
}
