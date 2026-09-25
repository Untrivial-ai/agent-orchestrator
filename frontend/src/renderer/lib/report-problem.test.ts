import { afterEach, describe, expect, it, vi } from "vitest";
import {
	collectReportProblemDiagnostics,
	formatReportProblemDraft,
	reportProblemDestinationUrl,
	type ReportProblemDiagnostics,
	type ReportProblemInput,
	type ReportProblemOutput,
} from "./report-problem";

const diagnostics: ReportProblemDiagnostics = {
	appVersion: "1.2.3-test",
	buildMode: "dev",
	daemonState: "ready",
	generatedAt: "2026-07-02T00:00:00.000Z",
	platform: "darwin-arm64",
	routeSurface: "session_detail",
};

const completeInput: ReportProblemInput = {
	summary: "Terminal keeps reconnecting after daemon restart",
	details:
		"Open /Users/alice/work/secret-app and visit http://127.0.0.1:5173/?token=secret-token. The app should reconnect without losing the current route.",
};

describe("report problem drafts", () => {
	afterEach(() => {
		vi.restoreAllMocks();
		window.location.hash = "";
	});

	it("formats GitHub, Discord, and email drafts with user text plus safe diagnostics", () => {
		const outputs: ReportProblemOutput[] = ["github", "discord", "email"];

		for (const output of outputs) {
			const draft = formatReportProblemDraft(completeInput, diagnostics, output);

			expect(draft).toContain("Terminal keeps reconnecting after daemon restart");
			expect(draft).toContain("The app should reconnect without losing the current route.");
			expect(draft).toContain("AO version: 1.2.3-test");
			expect(draft).toContain("Daemon: ready");
			expect(draft).toContain("Route surface: session_detail");
		}
	});

	it("redacts local paths, local URLs, and token-like values from drafts", () => {
		const draft = formatReportProblemDraft(
			{
				summary: "Setup fails with OPENAI_API_KEY=sk-proj-secret and password=hunter2",
				details:
					"Repo is C:\\Users\\alice\\repo and file:///Users/alice/private/index.html?api_key=abc failed. Tell me what prerequisite is missing.",
			},
			{
				...diagnostics,
				daemonMessage: "Serving http://localhost:31001/api/v1/sessions?access_token=local-secret",
			},
			"github",
		);

		expect(draft).toContain("[redacted-local-path]");
		expect(draft).toContain("[redacted-local-url]");
		expect(draft).toContain("[redacted-secret]");
		expect(draft).not.toContain("/Users/alice");
		expect(draft).not.toContain("C:\\Users\\alice");
		expect(draft).not.toContain("localhost:31001");
		expect(draft).not.toContain("sk-proj-secret");
		expect(draft).not.toContain("hunter2");
	});

	it("redacts JSON secrets, authorization headers, and GitHub token forms", () => {
		const githubToken = `ghp_${"abcdefghijklmnopqrstuvwxyz"}${"1234567890AB"}`;
		const githubOauthToken = `gho_${"abcdefghijklmnopqrstuvwxyz"}${"1234567890AB"}`;
		const fineGrainedGithubToken = `github_pat_11${"AAAAAAAAAAAAAAAAAAAA"}_${"B".repeat(74)}`;

		const draft = formatReportProblemDraft(
			{
				summary: `GitHub token leaked: ${githubToken}`,
				details: [
					'{"token": "json-token-secret", "api_key": "json-api-key-secret"}',
					`Authorization: token ${githubOauthToken}`,
					"authorization: Bearer header-token-secret",
					fineGrainedGithubToken,
				].join("\n"),
			},
			diagnostics,
			"github",
		);

		expect(draft).toContain("[redacted-secret]");
		expect(draft).not.toContain("json-token-secret");
		expect(draft).not.toContain("json-api-key-secret");
		expect(draft).not.toContain(githubToken);
		expect(draft).not.toContain(githubOauthToken);
		expect(draft).not.toContain("header-token-secret");
		expect(draft).not.toContain(fineGrainedGithubToken);
	});

	it("produces a useful draft when user input is partial", () => {
		const draft = formatReportProblemDraft({ summary: "", details: "" }, diagnostics, "email");

		expect(draft).toContain("AO feedback");
		expect(draft).toContain("To: prasad@untrivial.ai");
		expect(draft).toContain("Cc: prateek@untrivial.ai");
		expect(draft).toContain("Not provided");
		expect(draft).toContain("Safe diagnostics");
		expect(draft).toContain("AO version: 1.2.3-test");
	});

	it("omits report type and footer copy from generated drafts", () => {
		const outputs: ReportProblemOutput[] = ["github", "discord", "email"];

		for (const output of outputs) {
			const draft = formatReportProblemDraft(completeInput, diagnostics, output);

			expect(draft).toContain("Summary");
			expect(draft).toContain("Details");
			expect(draft).not.toContain("## Type");
			expect(draft).not.toContain("Bug report");
			expect(draft).not.toContain("Generated locally by AO");
			expect(draft).not.toContain("No logs, repo contents");
		}
	});

	it("builds copy handoff destinations for GitHub, Discord, and support email", () => {
		const github = new URL(reportProblemDestinationUrl(completeInput, diagnostics, "github")!);
		expect(`${github.origin}${github.pathname}`).toBe("https://github.com/Untrivial-ai/agent-orchestrator/issues/new");
		expect(github.searchParams.get("title")).toBe("Terminal keeps reconnecting after daemon restart");
		expect(github.searchParams.get("body")).toContain("[redacted-local-path]");
		expect(github.searchParams.get("body")).toContain("[redacted-local-url]");

		expect(reportProblemDestinationUrl(completeInput, diagnostics, "discord")).toBe(
			"https://discord.gg/WjKNa7EbB8",
		);

		const email = new URL(reportProblemDestinationUrl(completeInput, diagnostics, "email")!);
		expect(email.protocol).toBe("mailto:");
		expect(email.pathname).toBe("prasad@untrivial.ai");
		expect(email.searchParams.get("cc")).toBe("prateek@untrivial.ai");
		expect(email.searchParams.get("subject")).toBe("AO feedback: Terminal keeps reconnecting after daemon restart");
		expect(email.searchParams.get("body")).toContain("AO feedback");
		expect(email.searchParams.get("body")).toContain("AO version: 1.2.3-test");
	});

	it("builds provider-specific web compose URLs for Windows email choices", () => {
		const gmail = new URL(reportProblemDestinationUrl(completeInput, diagnostics, "email", "gmail")!);
		expect(gmail.origin).toBe("https://mail.google.com");
		expect(gmail.pathname).toBe("/mail/");
		expect(gmail.searchParams.get("view")).toBe("cm");
		expect(gmail.searchParams.get("to")).toBe("prasad@untrivial.ai");
		expect(gmail.searchParams.get("cc")).toBe("prateek@untrivial.ai");
		expect(gmail.searchParams.get("su")).toContain("Terminal keeps reconnecting");
		expect(gmail.searchParams.get("body")).toContain("AO version: 1.2.3-test");

		const outlook = new URL(reportProblemDestinationUrl(completeInput, diagnostics, "email", "outlook")!);
		expect(outlook.origin).toBe("https://outlook.office.com");
		expect(outlook.pathname).toBe("/mail/deeplink/compose");
		expect(outlook.searchParams.get("to")).toBe("prasad@untrivial.ai");
		expect(outlook.searchParams.get("cc")).toBe("prateek@untrivial.ai");
		expect(outlook.searchParams.get("subject")).toContain("Terminal keeps reconnecting");
		expect(outlook.searchParams.get("body")).toContain("AO version: 1.2.3-test");
	});

	it("percent-encodes mailto spaces instead of serializing them as plus signs", () => {
		const email = reportProblemDestinationUrl(
			{
				summary: "Switch Codex accounts bug",
				details: "Keep literal + signs safe.",
			},
			diagnostics,
			"email",
		)!;

		expect(email.startsWith("mailto:prasad@untrivial.ai?")).toBe(true);
		expect(email).toContain("subject=AO%20feedback%3A%20Switch%20Codex%20accounts%20bug");
		expect(email).toContain("body=AO%20feedback%0A%0ASummary%3A%20Switch%20Codex%20accounts%20bug");
		expect(email).toContain("Keep%20literal%20%2B%20signs%20safe.");
		expect(email).not.toContain("+");
	});

	it("keeps mailto drafts sendable when pasted text contains malformed UTF-16", () => {
		const email = new URL(
			reportProblemDestinationUrl(
				{
					summary: "Broken \uD800 text",
					details: "The pasted value should still open email.",
				},
				diagnostics,
				"email",
			)!,
		);

		expect(email.searchParams.get("subject")).toBe("AO feedback: Broken � text");
	});

	it("derives route surface from the hash-history route", async () => {
		window.ao!.app.getVersion = vi.fn().mockResolvedValue("1.2.3-test");
		window.ao!.daemon.getStatus = vi.fn().mockResolvedValue({ state: "ready" });
		window.location.hash = "#/projects/demo/sessions/demo-1";

		const nextDiagnostics = await collectReportProblemDiagnostics(new Date("2026-07-02T00:00:00.000Z"));

		expect(nextDiagnostics.routeSurface).toBe("session_detail");
	});
});
