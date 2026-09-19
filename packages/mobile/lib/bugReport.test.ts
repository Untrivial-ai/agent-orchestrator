import { describe, expect, it } from "vitest";
import { BUG_REPORT_MARKER, bugReportClipboard, bugReportOpenUrl, bugReportSummary, bugReportTitle, bugReportUrl } from "./bugReport";

const build = { version: "1.3.1", build: "42", updateId: "abcdef1234", channel: "production", runtimeVersion: "fingerprint-1234", embedded: false };

describe("bug report", () => {
	it("goes to the repo's own bug form, labelled for the mobile queue", () => {
		const url = bugReportUrl({ build, platform: "ios", osVersion: "26.6", paired: true, connection: "open" });
		expect(url.startsWith("https://github.com/Untrivial-ai/agent-orchestrator/issues/new?")).toBe(true);
		expect(url).toContain("template=bug_report.yml");
		expect(url).toContain(`labels=${encodeURIComponent("bug,comp/mobile")}`);
		expect(url).toContain("title=");
		expect(url).toContain("summary=");
	});

	// Labels can be dropped for a reporter who cannot apply them; the body cannot.
	it("marks every report so it stays findable without the label", () => {
		const summary = bugReportSummary({ build, platform: "android", osVersion: 34, paired: true, connection: "open" });
		expect(summary).toContain(`<!-- ${BUG_REPORT_MARKER} -->`);
	});

	it("carries the environment a triager would otherwise have to ask for", () => {
		const summary = bugReportSummary({ build, platform: "ios", osVersion: "26.6", deviceModel: "iPhone 13", paired: true, connection: "closed" });
		expect(summary).toContain("AO mobile: 1.3.1 (42)");
		expect(summary).toContain("Update: abcdef12 on production (runtime fingerpr)");
		expect(summary).toContain("Platform: ios 26.6");
		expect(summary).toContain("Device: iPhone 13");
		expect(summary).toContain("Daemon: paired, not reachable");
		// Two blank lines first, so the reporter types above the details.
		expect(summary.startsWith("\n\n---")).toBe(true);
	});

	it("says which platform in the title and the daemon state in the body", () => {
		expect(bugReportTitle("android")).toBe("[Bug]: Android — ");
		expect(bugReportSummary({ build, platform: "ios", osVersion: "26.6", paired: false })).toContain("Daemon: not paired");
		expect(bugReportSummary({ build, platform: "ios", osVersion: "26.6", paired: true, connection: "connecting" })).toContain("Daemon: connecting");
	});

	// The GitHub app takes the title and drops the form, so reports arrived empty.
	it("opens in the browser on iOS, where the GitHub app would eat the form", () => {
		const url = bugReportUrl({ build, platform: "ios", osVersion: "26.6", paired: true, connection: "open" });
		expect(bugReportOpenUrl(url, "ios").startsWith("x-safari-https://github.com/")).toBe(true);
		expect(bugReportOpenUrl(url, "android")).toBe(url);
	});

	it("copies the title and the details together", () => {
		const text = bugReportClipboard({ build, platform: "ios", osVersion: "26.6", deviceModel: "iPhone 13", paired: true, connection: "open" });
		expect(text.startsWith("[Bug]: iOS —\n")).toBe(true);
		expect(text).toContain("AO mobile: 1.3.1 (42)");
		expect(text).toContain("Device: iPhone 13");
		expect(text).toContain("Daemon: connected");
		expect(text).toContain(BUG_REPORT_MARKER);
	});
});
