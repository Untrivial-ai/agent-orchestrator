export const MAX_DIFF_SELECTION_MESSAGE_LENGTH = 8192;

const MAX_INSTRUCTION_LENGTH = 1000;
const MAX_FILEPATH_LENGTH = 500;
const MAX_LINE_TEXT_LENGTH = 1000;

export type DiffSelectionLineKind = "context" | "add" | "del";

export type DiffSelectionLine = {
	kind: DiffSelectionLineKind;
	oldNo: number | null;
	newNo: number | null;
	text: string;
};

export type DiffSelectionPayload = {
	instruction: string;
	filePath: string;
	lines: DiffSelectionLine[];
	scope?: string;
	workspaceVersion?: string;
	fileFingerprint?: string;
};

export function formatDiffSelectionMessage(payload: DiffSelectionPayload): string {
	const instruction = compactText(payload.instruction, MAX_INSTRUCTION_LENGTH) || "(empty)";
	const filePath = compactText(payload.filePath, MAX_FILEPATH_LENGTH);
	const lineRange = deriveLineRange(payload.lines);

	const formattedLines = payload.lines.map((line) => {
		const marker = getMarkerForKind(line.kind);
		const text = truncateLineText(line.text, MAX_LINE_TEXT_LENGTH);
		// Marker is either a space (context), '+' (add), or '-' (del).
		// For readability in message format, add a space after + and - markers to match context format.
		const separator = marker === " " ? "" : " ";
		return `${marker}${separator}${text}`;
	});

	const lines = [
		"The user selected lines in the diff viewer and asked for a change.",
		"",
		"Instruction:",
		instruction,
		"",
		`File: ${filePath}`,
		payload.scope ? `Comparison scope: ${compactText(payload.scope, 40)}` : null,
		payload.workspaceVersion ? `Workspace version: ${compactText(payload.workspaceVersion, 160)}` : null,
		payload.fileFingerprint ? `File fingerprint: ${compactText(payload.fileFingerprint, 160)}` : null,
		`Selected ${lineRange}:`,
		"",
		...formattedLines,
	];

	return limitMessage(lines.filter((line): line is string => line !== null).join("\n"), MAX_DIFF_SELECTION_MESSAGE_LENGTH);
}

function deriveLineRange(lines: DiffSelectionLine[]): string {
	if (lines.length === 0) return "lines (empty selection)";

	const firstLine = lines[0];
	const lastLine = lines[lines.length - 1];

	// Determine if we have any new numbers
	const hasAnyNewNo = lines.some((line) => line.newNo !== null);

	if (hasAnyNewNo) {
		// Prefer new-line range when any line has a new number
		const startNo = firstLine.newNo ?? firstLine.oldNo ?? "?";
		const endNo = lastLine.newNo ?? lastLine.oldNo ?? "?";
		return `lines ${startNo}-${endNo}`;
	} else {
		// Fall back to old-line range (for pure deletions)
		const startNo = firstLine.oldNo ?? "?";
		const endNo = lastLine.oldNo ?? "?";
		return `lines ${startNo}-${endNo}`;
	}
}

function getMarkerForKind(kind: DiffSelectionLineKind): string {
	switch (kind) {
		case "add":
			return "+";
		case "del":
			return "-";
		case "context":
			return " ";
	}
}

function compactText(value: string, maxLength: number): string {
	const compact = value.replace(/\s+/g, " ").trim();
	if (compact.length <= maxLength) return compact;
	const suffix = " [truncated]";
	return `${compact.slice(0, Math.max(0, maxLength - suffix.length)).trimEnd()}${suffix}`;
}

function truncateLineText(value: string, maxLength: number): string {
	// Preserve whitespace in diff lines (meaningful indentation, etc.)
	// Only truncate if line exceeds maxLength.
	if (value.length <= maxLength) return value;
	const suffix = "[truncated]";
	return `${value.slice(0, Math.max(0, maxLength - suffix.length))}${suffix}`;
}

function limitMessage(message: string, maxLength: number): string {
	if (message.length <= maxLength) return message;
	const suffix = "\n[truncated]";
	return `${message.slice(0, Math.max(0, maxLength - suffix.length)).trimEnd()}${suffix}`;
}
