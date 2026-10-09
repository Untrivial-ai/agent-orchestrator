import type { AgentModelCatalog } from "./api";

export type ReviewerPickerOption = {
	id: string;
	label: string;
};

export type ReviewerModelChoice = ReviewerPickerOption & {
	selected: boolean;
	/** What picking it saves: "" for the catalog's default, so no override is stored. */
	value: string;
};

export type ReviewerModelView = {
	title: "Model" | "Mode";
	/** The configured model, else the catalog's default, else that the agent picks. */
	text: string;
	/** "Use agent model", offered only when the catalog names no default. */
	followAgent: { label: string; selected: boolean } | null;
	/** Empty when the reviewer has no model/mode catalog. */
	choices: ReviewerModelChoice[];
};

export type ReviewerPickerProps = {
	/** Only selectable reviewers; unavailable agents are never offered. */
	reviewers: readonly ReviewerPickerOption[];
	/** The session override, or "" for the project default. */
	selectedReviewer: string;
	/** The harness that actually runs reviews, shown next to "Project default". */
	effectiveReviewer: string;
	onSelectReviewer: (id: string) => void;
	model: ReviewerModelView;
	/** Receives a choice's `value`: a model/mode id, or "" for no override. */
	onSelectModel: (value: string) => void;
	busy: boolean;
};

export function reviewerLabel(reviewers: readonly ReviewerPickerOption[], id: string): string {
	return reviewers.find((item) => item.id === id)?.label || id;
}

/** "default" names no model: it is how a config or a catalog says the agent picks (desktop's isConcreteModelID). */
function isConcreteModelId(id: string): boolean {
	return id !== "" && id.toLowerCase() !== "default";
}

/**
 * The reviewer's model row, resolved like desktop's ReviewerSelect (#5834). An
 * unset model reads as the catalog's default and that model is checked, with no
 * separate "default" row. Picking the default saves no override, so the reviewer
 * keeps following the agent when its default changes. Only a catalog that names
 * no default (Cursor's, for one) gets a "Use agent model" row.
 */
export function reviewerModelView(catalog: AgentModelCatalog | undefined, configured: string): ReviewerModelView {
	const title = catalog?.selectionMode === "mode" ? "Mode" : "Model";
	const followLabel = title === "Mode" ? "Use agent mode" : "Use agent model";
	const models = (catalog?.models ?? []).filter((model) => isConcreteModelId(model.id));
	const defaultModel = models.find((model) => model.isDefault)?.id ?? "";
	const picked = isConcreteModelId(configured) ? configured : "";
	const effective = picked || defaultModel;
	const choices = models.map((model) => ({
		id: model.id,
		label: model.label || model.id,
		selected: model.id === effective,
		value: model.id === defaultModel ? "" : model.id,
	}));
	return {
		title,
		text: effective ? reviewerLabel(choices, effective) : followLabel,
		followAgent: defaultModel ? null : { label: followLabel, selected: !picked },
		choices,
	};
}
