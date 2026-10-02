export type ReviewerPickerOption = {
	id: string;
	label: string;
};

export type ReviewerPickerProps = {
	/** Only selectable reviewers; unavailable agents are never offered. */
	reviewers: readonly ReviewerPickerOption[];
	/** The session override, or "" for the project default. */
	selectedReviewer: string;
	/** The harness that actually runs reviews, shown next to "Project default". */
	effectiveReviewer: string;
	onSelectReviewer: (id: string) => void;
	/** Empty when the reviewer has no model/mode catalog. */
	models: readonly ReviewerPickerOption[];
	modelTitle: "Model" | "Mode";
	/** The configured model/mode id, or "" for the provider default. */
	selectedModel: string;
	onSelectModel: (id: string) => void;
	busy: boolean;
};

export function reviewerLabel(reviewers: readonly ReviewerPickerOption[], id: string): string {
	return reviewers.find((item) => item.id === id)?.label || id;
}
