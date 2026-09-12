import type { WorkspaceFileSummary } from "../hooks/useSessionWorkspaceFiles";

export type WorkspaceFileStatus = WorkspaceFileSummary["status"];

// Shared between the changed-files diff view and the file tree's row
// decoration so a status always reads the same badge letter and color
// wherever it appears.
export const statusLabel: Record<WorkspaceFileStatus, string> = {
	added: "A",
	deleted: "D",
	modified: "M",
	renamed: "R",
	unmodified: "",
};

export const statusTone: Record<WorkspaceFileStatus, string> = {
	added: "text-success",
	deleted: "text-error",
	modified: "text-warning",
	renamed: "text-accent",
	unmodified: "text-passive",
};
