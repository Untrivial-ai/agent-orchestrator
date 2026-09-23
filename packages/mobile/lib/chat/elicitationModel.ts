import type { InputProperty } from "./types";

export type InputPropertyEntry = readonly [string, InputProperty];

export function elicitationPromptCopy(value?: string): string | undefined {
	const copy = value?.trim();
	if (!copy || /^please answer the following questions\.?$/i.test(copy)) return undefined;
	return copy;
}

export function elicitationStepPresentation(index: number, total: number) {
	const current = Math.min(Math.max(index, 0), Math.max(total - 1, 0));
	return {
		status: total > 1 ? `Needs input · Question ${current + 1} of ${total}` : "Needs input",
		showBack: current > 0,
		primaryLabel: current < total - 1 ? "Next" : "Continue",
	};
}

export function initialInputValue(property: InputProperty): unknown {
	if (property.default !== undefined) return property.default;
	if (property.type === "array") return [];
	if (property.type === "boolean") return false;
	return "";
}

export function inputOptions(property: InputProperty): Array<{ value: string; label: string; description?: string }> {
	const candidates = property.oneOf ?? property.items?.anyOf;
	if (candidates?.length) {
		return candidates.flatMap((candidate) => typeof candidate.const === "string"
			? [{ value: candidate.const, label: candidate.title || candidate.const, description: candidate.description }]
			: []);
	}
	return (property.enum ?? []).flatMap((value) => typeof value === "string"
		? [{ value, label: value }]
		: []);
}

export function groupedQuestionInputs(properties: readonly InputPropertyEntry[]): InputPropertyEntry[][] | undefined {
	if (!properties.length) return undefined;
	const groups = new Map<number, { question?: InputPropertyEntry; custom?: InputPropertyEntry }>();
	for (const entry of properties) {
		const match = /^question_(\d+)(_custom)?$/.exec(entry[0]);
		if (!match) return undefined;
		const index = Number(match[1]);
		const group = groups.get(index) ?? {};
		if (match[2]) group.custom = entry;
		else group.question = entry;
		groups.set(index, group);
	}
	const ordered: InputPropertyEntry[][] = [];
	for (const [, group] of [...groups.entries()].sort(([left], [right]) => left - right)) {
		if (!group.question) return undefined;
		ordered.push(group.custom ? [group.question, group.custom] : [group.question]);
	}
	return ordered;
}

export function toggleInputValue(values: unknown[], value: string): string[] {
	const strings = values.filter((item): item is string => typeof item === "string");
	return strings.includes(value) ? strings.filter((item) => item !== value) : [...strings, value];
}

export function missingRequiredInputs(required: string[] | undefined, values: Record<string, unknown>): string[] {
	return (required ?? []).filter((name) => {
		const value = values[name];
		return value === undefined || value === null || value === "" || (Array.isArray(value) && value.length === 0);
	});
}

export function validateInput(property: InputProperty, value: unknown): string | undefined {
	if (typeof value === "string") {
		if (property.minLength !== undefined && value.length < property.minLength) return `must be at least ${property.minLength} characters`;
		if (property.maxLength !== undefined && value.length > property.maxLength) return `must be at most ${property.maxLength} characters`;
	}
	if ((property.type === "number" || property.type === "integer") && typeof value === "number") {
		if (!Number.isFinite(value)) return "must be a number";
		if (property.type === "integer" && !Number.isInteger(value)) return "must be a whole number";
		if (property.minimum !== undefined && value < property.minimum) return `must be at least ${property.minimum}`;
		if (property.maximum !== undefined && value > property.maximum) return `must be at most ${property.maximum}`;
	}
	return undefined;
}

export function humanizeInputName(value: string): string {
	return value.replace(/_/g, " ").replace(/^./, (letter) => letter.toUpperCase());
}

export function safeHttpURL(value: unknown): URL | undefined {
	if (typeof value !== "string") return undefined;
	try {
		const url = new URL(value);
		return url.protocol === "https:" || url.protocol === "http:" ? url : undefined;
	} catch {
		return undefined;
	}
}
