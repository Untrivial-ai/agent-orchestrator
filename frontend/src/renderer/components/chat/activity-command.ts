import type { ConversationActivity } from "../../types/conversation";

/** The user-facing category of a shell command in the chat timeline. */
export type CommandCategory = "read" | "search" | "vcs" | "run";

/** Shared chrome for one command summary and a collapsed run of commands. */
export const ACTIVITY_SUMMARY_BUTTON_CLASS =
	"activity-summary-button group/run flex w-full select-none items-center gap-1.5 py-0.5 pr-1 text-left outline-none focus-visible:outline-none";

/** A completed shell command that reported an ordinary non-zero process exit. */
export function isNonzeroCommandExit(activity: ConversationActivity): boolean {
	const exitCode = activity.detail?.exitCode;
	return (
		activity.activityKind === "command" &&
		activity.status === "failed" &&
		typeof exitCode === "number" &&
		exitCode !== 0
	);
}

const READERS = new Set(["cat", "sed", "nl", "head", "tail", "bat", "less", "more", "wc", "jq"]);
const SEARCHERS = new Set(["rg", "grep", "find", "fd", "ls", "tree", "glob", "ag"]);
const VCS = new Set(["git", "gh"]);

/** Classify mechanics by intent so the timeline says what happened, not which binary ran. */
export function commandCategory(text: string): CommandCategory {
	const binary = firstWord(text);
	if (READERS.has(binary)) return "read";
	if (SEARCHERS.has(binary)) return "search";
	if (VCS.has(binary)) return "vcs";
	return "run";
}

/**
 * Count explicit file arguments for reader commands. Search expressions are too
 * ambiguous to count honestly, so those fall back to the unnumbered label.
 */
export function exploredFileCount(text: string): number | undefined {
	if (commandCategory(text) !== "read") return undefined;

	const files = new Set<string>();
	for (const token of text.match(/"(?:\\.|[^"])*"|'(?:\\.|[^'])*'|[^\s]+/g) ?? []) {
		const value = token
			.replace(/^["']|["']$/g, "")
			.replace(/[;&|]+$/, "");
		if (!value || value.startsWith("-") || /[*?{}]/.test(value)) continue;
		if (["&&", "||", "|", ";"].includes(value)) continue;
		if (READERS.has(binaryName(value))) continue;
		if (/^\d+(?:,\d+)?[a-z]*$/i.test(value)) continue;
		if (/^[a-z]+:\/\//i.test(value)) continue;

		const looksLikeFile =
			value.startsWith("/") ||
			value.startsWith("./") ||
			value.startsWith("../") ||
			value.startsWith("~/") ||
			value.includes("/") ||
			/^[\w@.+-]+\.[A-Za-z][\w-]*$/.test(value);
		if (looksLikeFile) files.add(value);
	}

	return files.size > 0 ? files.size : undefined;
}

/** The command's binary, normalized so `/bin/sed` and `sed` classify alike. */
export function commandBinaryLabel(command: string): string | undefined {
	const trimmed = command.trim();
	if (!trimmed) return undefined;
	const word = firstWord(trimmed);
	return word || undefined;
}

/** The command's binary, normalized so `/bin/sed` and `sed` classify alike. */
function firstWord(text: string): string {
	const trimmed = text.trim();
	const space = trimmed.indexOf(" ");
	const head = space > 0 ? trimmed.slice(0, space) : trimmed;
	return binaryName(head);
}

function binaryName(value: string): string {
	const slash = value.lastIndexOf("/");
	return slash >= 0 ? value.slice(slash + 1) : value;
}
