import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { AgentAvatar } from "./AgentAvatar";
import { GithubAvatar } from "./GithubAvatar";
import { scmUserAvatarUrl } from "./scm-avatar";
import { UserAvatar } from "./UserAvatar";
import type { ExternalLinkProps } from "./external-link";
import { PRCardStatusSummary, PRSummaryMeta, PRSummaryParts } from "./PRSummaryDisplay";
import type { PRCardPresentation, PRSummaryPart } from "./pull-request-models";

function ExternalLink({ ariaLabel, children, stopPropagation, ...props }: ExternalLinkProps) {
	return (
		<a
			{...props}
			aria-label={ariaLabel}
			onClick={stopPropagation ? (event) => event.stopPropagation() : undefined}
		>
			{children}
		</a>
	);
}

describe("portable leaf components", () => {
	it("keeps user initials visible until the provider avatar loads and after errors", () => {
		const avatarUrl = "https://avatars.githubusercontent.com/u/123?v=4";
		const { container } = render(<UserAvatar imageUrl={avatarUrl} name="ada-lovelace" />);
		const image = container.querySelector("img");

		expect(container).toHaveTextContent("AL");
		expect(image).toHaveAttribute("src", avatarUrl);
		expect(image).toHaveClass("opacity-0");
		if (image) fireEvent.load(image);
		expect(image).toHaveClass("opacity-100");
		expect(container).not.toHaveTextContent("AL");
		if (image) fireEvent.error(image);
		expect(container.querySelector("img")).not.toBeInTheDocument();
		expect(container).toHaveTextContent("AL");
	});

	it("renders only initials when no provider avatar URL exists", () => {
		const { container } = render(<UserAvatar name="ada-lovelace" />);

		expect(container).toHaveTextContent("AL");
		expect(container.querySelector("img")).not.toBeInTheDocument();
	});

	it("derives provider avatar endpoints only when an observation has no URL", () => {
		expect(
			scmUserAvatarUrl("github", "https://github.com/acme/repo/pull/7", "ada-lovelace"),
		).toBe("https://avatars.githubusercontent.com/ada-lovelace?size=64");
		expect(
			scmUserAvatarUrl("github", "https://git.example.com/acme/repo/pull/7", "ada-lovelace"),
		).toBe("https://git.example.com/ada-lovelace.png?size=64");
		expect(scmUserAvatarUrl("unknown", "https://example.com/pull/7", "ada-lovelace")).toBeUndefined();
	});

	it("keeps the GitHub-specific compatibility component login-derived", () => {
		const { container } = render(<GithubAvatar login="ada-lovelace" />);

		expect(container.querySelector("img")).toHaveAttribute(
			"src",
			"https://avatars.githubusercontent.com/ada-lovelace?size=64",
		);
	});

	it("renders an injected agent logo without owning app assets", () => {
		render(<AgentAvatar logoSources={{ "claude-code": "/logos/claude.svg" }} provider="claude-code" />);

		expect(screen.getByRole("img", { name: "claude-code" })).toHaveAttribute(
			"src",
			"/logos/claude.svg",
		);
	});

	it("renders fallback identity metadata for unknown agents", () => {
		render(<AgentAvatar provider="new-agent" />);

		expect(screen.getByRole("img", { name: "new-agent" })).toHaveTextContent("N");
	});

	it("renders neutral PR metadata with injected plural labels", () => {
		render(
			<PRSummaryMeta
				countNounLabel={(count, noun) => `${count} localized-${noun}`}
				externalLink={ExternalLink}
				leading={<span>PR #7</span>}
				pr={{
					provider: "github",
					author: "ada",
					authorAvatarUrl: "https://avatars.githubusercontent.com/u/123?v=4",
					sourceBranch: "feature",
					targetBranch: "main",
					changedFiles: 2,
					additions: 4,
					deletions: 1,
				}}
			/>,
		);

		expect(screen.getByText("feature → main")).toBeInTheDocument();
		expect(screen.getByText("2 localized-file")).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "ada" })).toHaveAttribute("href", "https://github.com/ada");
		expect(screen.getByRole("link", { name: "ada" }).querySelector("img")).toHaveAttribute(
			"src",
			"https://avatars.githubusercontent.com/u/123?v=4",
		);
		expect(screen.getByText("feature → main")).toHaveClass("break-words");
		const metadata = screen.getByText("feature → main").closest("div.font-mono");
		const identity = screen.getByText("feature → main").closest("div");
		const details = screen.getByText("2 localized-file").closest("div");
		expect(metadata).toHaveClass("space-y-0.5");
		expect(metadata).toContainElement(screen.getByText("PR #7"));
		expect(identity).toContainElement(screen.getByText("PR #7"));
		expect(details).toContainElement(screen.getByRole("link", { name: "ada" }));
		expect(identity).not.toBe(details);
		expect(screen.queryByText("·")).not.toBeInTheDocument();
	});

	it("renders precomputed PR presentation without controller dependencies", () => {
		const presentation: PRCardPresentation = {
			primary: {
				key: "review",
				label: "Changes requested",
				tone: "warning",
				links: [{ label: "ada", href: "https://example.com/review" }],
			},
			supporting: [
				{
					key: "ci",
					label: "Checks running",
					href: "https://example.com/checks",
					breathe: true,
					tone: "neutral",
					links: [],
				},
			],
			readiness: {
				label: "Merge pending",
				detail: "",
				href: "https://example.com/pull",
				tone: "passive",
			},
		};
		const { container } = render(
			<PRCardStatusSummary externalLink={ExternalLink} presentation={presentation} />,
		);

		const statusLayout = screen.getByText("Changes requested").closest('[data-slot="pr-status-layout"]');
		const secondaryStatuses = screen.getByRole("link", { name: "Merge pending" }).closest('[data-slot="pr-secondary-statuses"]');
		expect(statusLayout).toContainElement(screen.getByRole("link", { name: "ada" }));
		expect(secondaryStatuses).toContainElement(screen.getByRole("link", { name: "Checks running" }));
		expect(secondaryStatuses).not.toContainElement(screen.getByRole("link", { name: "ada" }));
		expect(secondaryStatuses?.querySelector(".lucide-loader-circle")).not.toBeInTheDocument();
		expect(container.querySelector(".animate-spin")).not.toBeInTheDocument();
	});

	it("keeps review details compact and inline with the review detail", () => {
		const reviewDetailsAction = <button type="button">View review details ↗</button>;
		render(
			<PRCardStatusSummary
				externalLink={ExternalLink}
				presentation={{
					primary: { key: "ci", label: "Checks passing", tone: "success", links: [] },
					supporting: [],
					statusRows: [
						{ key: "ci", label: "Checks passing", tone: "success", links: [] },
						{ key: "review", label: "Review status", detail: "Required review not submitted", tone: "review", links: [] },
					],
					readiness: { label: "Not mergeable yet", detail: "A required review is pending.", tone: "error" },
				}}
				reviewDetailsAction={reviewDetailsAction}
			/>,
		);

		const detail = screen.getByText("Required review not submitted");
		expect(detail.parentElement).toHaveClass("flex", "items-baseline");
		expect(detail.parentElement).toContainElement(screen.getByRole("button", { name: "View review details ↗" }));
		expect(detail.closest(".grid")).toHaveClass("grid-cols-1");
	});

	it("computes overflow against the requested link limit", () => {
		const parts: PRSummaryPart[] = [
			{
				key: "ci",
				label: "CI",
				status: "Failing",
				links: [{ label: "unit" }, { label: "lint" }, { label: "types" }],
				linkTotal: 3,
				overflowNoun: "check",
				tone: "error",
			},
		];
		render(
			<PRSummaryParts
				externalLink={ExternalLink}
				interactiveLinks={false}
				maxLinks={2}
				parts={parts}
			/>,
		);

		expect(screen.getByText("unit")).toBeInTheDocument();
		expect(screen.getByText("lint")).toBeInTheDocument();
		expect(screen.queryByText("types")).not.toBeInTheDocument();
		expect(screen.getByText("+1 check")).toBeInTheDocument();
	});
});
