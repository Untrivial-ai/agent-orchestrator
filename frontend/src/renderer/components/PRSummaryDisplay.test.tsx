import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { SessionPRSummary } from "../hooks/useSessionScmSummary";
import { appI18n } from "../i18n";
import { useLocaleStore } from "../stores/locale-store";
import { PRCardStatusSummary, PRSummaryMeta, PRSummaryParts } from "./PRSummaryDisplay";

const summary = (overrides: Partial<SessionPRSummary> = {}): SessionPRSummary => ({
	url: "https://github.com/acme/repo/pull/7",
	htmlUrl: "https://github.com/acme/repo/pull/7",
	number: 7,
	title: "Fix dashboard",
	state: "open",
	provider: "github",
	repo: "acme/repo",
	author: "ada",
	sourceBranch: "fix/dashboard",
	targetBranch: "main",
	headSha: "abc123",
	additions: 10,
	deletions: 3,
	changedFiles: 2,
	ci: { autoInjectCI: true, state: "passing", failingChecks: [] },
	review: { decision: "approved", hasUnresolvedHumanComments: false, unresolvedBy: [] },
	mergeability: { state: "mergeable", reasons: [], prUrl: "https://github.com/acme/repo/pull/7" },
	updatedAt: "2026-06-15T00:00:00Z",
	observedAt: "2026-06-15T00:00:00Z",
	ciObservedAt: "2026-06-15T00:00:00Z",
	reviewObservedAt: "2026-06-15T00:00:00Z",
	...overrides,
});

describe("PRSummaryParts", () => {
	it("links GitHub authors and successful checks to their provider pages", () => {
		render(
			<>
				<PRSummaryMeta pr={summary()} />
				<PRCardStatusSummary pr={summary()} />
			</>,
		);

		expect(screen.getByRole("link", { name: "ada" })).toHaveAttribute("href", "https://github.com/ada");
		expect(screen.getByRole("link", { name: "Checks passing" })).toHaveAttribute(
			"href",
			"https://github.com/acme/repo/pull/7/checks",
		);
	});

	it("keeps running checks visible and pulsing beneath a higher-priority review blocker", () => {
		const { container } = render(
			<PRCardStatusSummary
				pr={summary({
					ci: { autoInjectCI: true, state: "pending", failingChecks: [] },
					review: { decision: "review_required", hasUnresolvedHumanComments: false, unresolvedBy: [] },
					mergeability: {
						state: "blocked",
						reasons: ["review_required"],
						prUrl: "https://github.com/acme/repo/pull/7",
					},
				})}
			/>,
		);

		expect(screen.getByText("Review status")).toBeInTheDocument();
		expect(screen.getByText("Required review not submitted")).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "Checks running" })).toHaveAttribute(
			"href",
			"https://github.com/acme/repo/pull/7/checks",
		);
		expect(container.querySelector(".animate-status-pulse")).toBeInTheDocument();
	});

	it("optically centers the primary status marker with its text line", () => {
		const { container } = render(<PRCardStatusSummary pr={summary()} />);

		const marker = container.querySelector(".size-dot-sm");
		expect(marker).toHaveClass("size-dot-sm");
		expect(marker).not.toHaveClass("mt-1");
	});

	it("centers a supplied primary action beside the compact status stack", () => {
		const { container } = render(<PRCardStatusSummary action={<button type="button">Merge</button>} pr={summary()} />);

		const action = screen.getByRole("button", { name: "Merge" });
		const supportingStatus = screen.getByRole("link", { name: "Checks passing" });
		expect(action.parentElement).toHaveClass("shrink-0", "self-center");
		expect(action.parentElement?.parentElement).toHaveClass("items-center");
		expect(container.querySelector(".grid")).toContainElement(supportingStatus);
		expect(screen.getByText("Mergeable")).toBeInTheDocument();
	});

	it("renders failing check links with visible error contrast", () => {
		render(
			<PRCardStatusSummary
				pr={summary({
					ci: {
						autoInjectCI: true,
						state: "failing",
						failingChecks: [
							{ name: "renderer-smoke", status: "failed", conclusion: "failure", url: "https://ci/smoke" },
						],
					},
				})}
			/>,
		);

		expect(screen.getByText("Checks failing")).toBeInTheDocument();
		expect(screen.queryByRole("link", { name: "renderer-smoke" })).not.toBeInTheDocument();
	});

	it("localizes changed-file plurals instead of rebuilding English nouns", async () => {
		await appI18n.changeLanguage("zh-CN");
		useLocaleStore.setState({ locale: "zh-CN" });
		render(<PRSummaryMeta pr={summary({ changedFiles: 2 })} />);
		expect(screen.getByText(/2 个文件/)).toBeInTheDocument();
		await appI18n.changeLanguage("en");
		useLocaleStore.setState({ locale: "en" });
	});

	it("counts overflow from the rendered maxLinks limit", () => {
		render(
			<PRSummaryParts
				interactiveLinks={false}
				maxLinks={2}
				pr={summary({
					ci: {
						autoInjectCI: true,
						state: "failing",
						failingChecks: [
							{ name: "unit", status: "failed", conclusion: "failure", url: "https://checks.example/unit" },
							{ name: "lint", status: "failed", conclusion: "failure", url: "https://checks.example/lint" },
							{ name: "types", status: "failed", conclusion: "failure", url: "https://checks.example/types" },
						],
					},
				})}
			/>,
		);

		expect(screen.getByText("unit")).toBeInTheDocument();
		expect(screen.getByText("lint")).toBeInTheDocument();
		expect(screen.queryByText("types")).not.toBeInTheDocument();
		expect(screen.getByText("+1 check")).toBeInTheDocument();
	});

	it("counts overflow beyond helper-truncated links", () => {
		render(
			<PRSummaryParts
				interactiveLinks={false}
				pr={summary({
					ci: {
						autoInjectCI: true,
						state: "failing",
						failingChecks: [
							{ name: "unit", status: "failed", conclusion: "failure", url: "https://checks.example/unit" },
							{ name: "lint", status: "failed", conclusion: "failure", url: "https://checks.example/lint" },
							{ name: "types", status: "failed", conclusion: "failure", url: "https://checks.example/types" },
							{ name: "build", status: "failed", conclusion: "failure", url: "https://checks.example/build" },
						],
					},
				})}
			/>,
		);

		expect(screen.getByText("unit")).toBeInTheDocument();
		expect(screen.getByText("lint")).toBeInTheDocument();
		expect(screen.getByText("types")).toBeInTheDocument();
		expect(screen.queryByText("build")).not.toBeInTheDocument();
		expect(screen.getByText("+1 check")).toBeInTheDocument();
	});
});
