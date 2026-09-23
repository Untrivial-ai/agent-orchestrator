import { formatVersion, formatUpdate, type BuildInfo } from "./appInfo";

/**
 * The GitHub issue a "Report a problem" tap opens.
 *
 * Pure so the URL and the body can be tested without a native runtime — the
 * same split as appInfo.ts, whose formatters this builds on.
 *
 * Three things make a mobile report findable later:
 *
 *  - the `comp/mobile` label, which the repo already defines, so the issue
 *    lands in the mobile queue rather than in untriaged;
 *  - a marker line in the body, because labels can be dropped (GitHub ignores a
 *    label the reporter cannot apply) while body text always survives, and
 *    `is:issue "reported-from: ao-mobile"` finds every one of them;
 *  - the repo's own bug form, so the report arrives with a title and the same
 *    fields a desktop report has.
 */
export const BUG_REPORT_REPO = "Untrivial-ai/agent-orchestrator";
export const BUG_REPORT_TEMPLATE = "bug_report.yml";
export const BUG_REPORT_LABELS = ["bug", "comp/mobile"] as const;

/** Searchable, and stable: filters are built on this string, so it does not change. */
export const BUG_REPORT_MARKER = "reported-from: ao-mobile";

export type BugReportEnvironment = {
	build: BuildInfo;
	platform: string;
	osVersion: string | number;
	/** expo-device's model, e.g. "iPhone 13" or "SM-M317F". */
	deviceModel?: string | null;
	/** Whether the phone is paired to a daemon, and whether it is answering. */
	paired: boolean;
	connection?: "open" | "connecting" | "closed";
};

function connectionLine(env: BugReportEnvironment): string {
	if (!env.paired) return "Daemon: not paired";
	if (env.connection === "open") return "Daemon: connected";
	if (env.connection === "connecting") return "Daemon: connecting";
	return "Daemon: paired, not reachable";
}

/**
 * What goes in the form's "What went wrong?" field: room to type, then the
 * environment the reporter would otherwise be asked for.
 */
export function bugReportSummary(env: BugReportEnvironment): string {
	const update = formatUpdate(env.build);
	const runtime = env.build.runtimeVersion ? ` (runtime ${env.build.runtimeVersion.slice(0, 8)})` : "";
	return [
		"",
		"",
		"---",
		`AO mobile: ${formatVersion(env.build)}`,
		...(update ? [`Update: ${update}${runtime}`] : []),
		`Platform: ${env.platform} ${env.osVersion}`,
		...(env.deviceModel?.trim() ? [`Device: ${env.deviceModel.trim()}`] : []),
		connectionLine(env),
		`<!-- ${BUG_REPORT_MARKER} -->`,
	].join("\n");
}

/** "[Bug]: mobile — " so the title carries the platform before anyone opens it. */
export function bugReportTitle(platform: string): string {
	return `[Bug]: ${platform === "ios" ? "iOS" : platform === "android" ? "Android" : platform} — `;
}

export function bugReportUrl(env: BugReportEnvironment): string {
	const params = new URLSearchParams({
		template: BUG_REPORT_TEMPLATE,
		labels: BUG_REPORT_LABELS.join(","),
		title: bugReportTitle(env.platform),
		summary: bugReportSummary(env),
	});
	return `https://github.com/${BUG_REPORT_REPO}/issues/new?${params.toString()}`;
}

/**
 * The whole report as text: the title line, then the environment block.
 *
 * Copied on every tap so a reporter can paste the lot into GitHub — or into
 * Discord, or a message — when the prefilled form does not survive the trip.
 * The marker travels with it, so a pasted report is as findable as a prefilled
 * one.
 */
export function bugReportClipboard(env: BugReportEnvironment): string {
	return `${bugReportTitle(env.platform).trimEnd()}\n${bugReportSummary(env).replace(/^\n+/, "\n")}`;
}

/**
 * The same URL, but aimed at a browser rather than the GitHub app.
 *
 * iOS claims github.com for the GitHub app through universal links, so a plain
 * `https://` open lands in the app — which accepts a prefilled title and
 * silently drops the issue form and its fields, filing a report with an empty
 * body. `x-safari-https://` is Safari's own scheme and bypasses that claim.
 * Android has no equivalent problem: an intent chooser respects the query.
 */
export function bugReportOpenUrl(url: string, platform: string): string {
	return platform === "ios" ? url.replace(/^https:\/\//, "x-safari-https://") : url;
}
