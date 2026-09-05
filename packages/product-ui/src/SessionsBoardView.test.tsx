import { fireEvent, render, screen, within } from "@testing-library/react";
import { createElement, type ComponentProps } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
	ARCHIVE_TOGGLE_HEIGHT_PX,
	SessionCardView,
	SessionsArchiveView,
	SessionsBoardGridView,
	archiveToggleHeightClassName,
	archiveToggleOffsetClassName,
	type BoardSessionPresentation,
	type BoardColumnLabels,
} from "./SessionsBoardView";
import {
	boardKanbanColumnOrder,
	getKanbanColumnView,
	getSessionStatusView,
} from "./session-presentation";
import type { ExternalLinkProps } from "./external-link";

const useReducedMotionMock = vi.hoisted(() => vi.fn(() => false));
const lastArchiveMotionTransition = vi.hoisted(() => ({
	current: undefined as { duration?: number } | undefined,
}));

vi.mock("motion/react", async (importOriginal) => {
	const actual = await importOriginal<typeof import("motion/react")>();
	function MotionDiv(props: ComponentProps<typeof actual.motion.div>) {
		lastArchiveMotionTransition.current = props.transition as { duration?: number } | undefined;
		return createElement(actual.motion.div, props);
	}
	return {
		...actual,
		useReducedMotion: useReducedMotionMock,
		motion: { ...actual.motion, div: MotionDiv },
	};
});

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

const columnLabels: BoardColumnLabels = {
	columnAria: (label) => `${label} sessions`,
};

const baseSession: BoardSessionPresentation = {
	id: "session-1",
	kanbanColumn: "building",
	provider: "codex",
	status: "idle",
	title: "portable task",
	updatedAt: "2026-08-09T10:00:00Z",
};

describe("SessionsBoardView", () => {
	beforeEach(() => {
		useReducedMotionMock.mockReturnValue(false);
		lastArchiveMotionTransition.current = undefined;
	});

	it("renders one lane per Kanban column, newest first, with one scroller each", () => {
		const sessions: BoardSessionPresentation[] = [
			baseSession,
			{ ...baseSession, id: "later", title: "later task", updatedAt: "2026-08-09T12:00:00Z" },
			{
				...baseSession,
				id: "ready",
				kanbanColumn: "ready",
				status: "mergeable",
				title: "ready task",
			},
		];
		render(
			<SessionsBoardGridView
				columns={boardKanbanColumnOrder.map((column) => getKanbanColumnView(column))}
				labels={columnLabels}
				renderSessionCard={(session) => <div data-testid={`card-${session.id}`}>{session.title}</div>}
				sessions={sessions}
			/>,
		);

		const buildingLane = screen.getByRole("region", { name: "Building sessions" });
		const buildingHeader = buildingLane.firstElementChild as HTMLElement;
		expect(buildingHeader).not.toHaveAttribute("style");
		const swatch = within(buildingLane).getByTestId("board-column-swatch");
		expect(swatch).toHaveClass("size-[var(--size-swatch)]", "rounded-full");
		expect(swatch.style.boxShadow).toBe("");
		const title = within(buildingLane).getByText("Building");
		expect(title).toHaveClass("text-xs", "font-medium");
		expect(title).not.toHaveClass("font-mono", "uppercase", "tracking-wide-sm");
		const count = within(buildingLane).getByText("2");
		expect(count).toHaveClass("tabular-nums", "text-xs");
		expect(count).not.toHaveClass("font-mono");
		expect(
			within(buildingLane)
				.getAllByTestId(/^card-/)
				.map((card) => card.textContent),
		).toEqual(["later task", "portable task"]);
		expect(buildingLane.querySelectorAll(".overflow-y-auto")).toHaveLength(1);

		expect(within(screen.getByRole("region", { name: "Ready sessions" })).getByTestId("card-ready")).toBeInTheDocument();
		// Empty lanes still render, so the four-column grid never collapses.
		expect(screen.getByRole("region", { name: "Validating sessions" })).toBeInTheDocument();
		expect(screen.getByRole("region", { name: "In review sessions" })).toBeInTheDocument();
		expect(screen.getByTestId("board-horizontal-scroll")).toHaveClass("board-horizontal-scrollbar");
	});

	it("pins attention-required sessions first inside every lane without changing lanes", () => {
		const columns = boardKanbanColumnOrder.map((column) => getKanbanColumnView(column));
		const sessions: BoardSessionPresentation[] = columns.flatMap(({ column }, index) => [
			{
				...baseSession,
				id: `${column}-newer`,
				kanbanColumn: column,
				status: column === "ready" ? "mergeable" : "idle",
				title: `${column} newer`,
				updatedAt: `2026-08-09T1${index}:00:00Z`,
			},
			{
				...baseSession,
				id: `${column}-attention`,
				kanbanColumn: column,
				status: "needs_input",
				title: `${column} attention`,
				updatedAt: "2026-08-08T09:00:00Z",
			},
		]);

		render(
			<SessionsBoardGridView
				columns={columns}
				labels={columnLabels}
				renderSessionCard={(session) => <div data-testid={`card-${session.id}`}>{session.title}</div>}
				sessions={sessions}
			/>,
		);

		for (const column of columns) {
			const lane = screen.getByRole("region", { name: `${column.label} sessions` });
			expect(
				within(lane)
					.getAllByTestId(/^card-/)
					.map((card) => card.textContent),
			).toEqual([`${column.column} attention`, `${column.column} newer`]);
		}
	});

	it("pins display-status attention cards first inside the lane", () => {
		render(
			<SessionsBoardGridView
				columns={boardKanbanColumnOrder.map((column) => getKanbanColumnView(column))}
				labels={columnLabels}
				renderSessionCard={(session) => <div data-testid={`card-${session.id}`}>{session.title}</div>}
				sessions={[
					{
						...baseSession,
						id: "newer-neutral",
						kanbanColumn: "needs_review",
						status: "idle",
						title: "newer neutral",
						updatedAt: "2026-08-09T12:00:00Z",
					},
					{
						...baseSession,
						id: "older-attention",
						displayStatus: "Changes requested",
						kanbanColumn: "needs_review",
						status: "idle",
						title: "older attention",
						updatedAt: "2026-08-08T09:00:00Z",
					},
				]}
			/>,
		);

		const lane = screen.getByRole("region", { name: "In review sessions" });
		expect(
			within(lane)
				.getAllByTestId(/^card-/)
				.map((card) => card.textContent),
		).toEqual(["older attention", "newer neutral"]);
	});

	it.each([
		{ displayStatus: "Blocked", status: "idle" as const },
		{ displayStatus: "CI failing", status: "idle" as const },
		{ displayStatus: "Changes requested", status: "idle" as const },
		{ displayStatus: undefined, status: "ci_failed" as const },
		{ displayStatus: undefined, status: "changes_requested" as const },
	] as const)(
		"gives %s cards the persistent orange attention treatment",
		({ displayStatus, status }) => {
			render(
				<SessionCardView
					externalLink={ExternalLink}
					labels={{
						formatTime: () => "5m ago",
						intakeIssue: (id) => `Issue ${id}`,
						pr: {
							short: "PR",
							states: { closed: "closed", draft: "draft", merged: "merged", open: "open" },
						},
						updatedAt: (timestamp) => `Updated ${timestamp}`,
					}}
					renderAvatar={(provider) => <span role="img" aria-label={provider}>C</span>}
					session={{ ...baseSession, status, displayStatus }}
				/>,
			);

			const card = screen.getByTestId("board-session-card");
			expect(card).toHaveClass(
				"animate-attention-card-pulse",
				"border-status-needs-you",
				"bg-[color-mix(in_srgb,var(--color-status-needs-you)_8%,var(--color-surface))]",
			);
		},
	);

	it("leaves ordinary cards on the neutral surface", () => {
		render(
			<SessionCardView
				externalLink={ExternalLink}
				labels={{
					formatTime: () => "5m ago",
					intakeIssue: (id) => `Issue ${id}`,
					pr: {
						short: "PR",
						states: { closed: "closed", draft: "draft", merged: "merged", open: "open" },
					},
					updatedAt: (timestamp) => `Updated ${timestamp}`,
				}}
				renderAvatar={(provider) => <span role="img" aria-label={provider}>C</span>}
				session={baseSession}
			/>,
		);

		const card = screen.getByTestId("board-session-card");
		expect(card).toHaveClass("border", "border-border", "bg-surface");
		expect(card).toHaveClass("rounded-lg");
		expect(card).not.toHaveClass("animate-attention-card-pulse", "border-status-needs-you");
	});

	it("does not show the attention border for non-attention display statuses", () => {
		render(
			<SessionCardView
				externalLink={ExternalLink}
				labels={{
					formatTime: () => "5m ago",
					intakeIssue: (id) => `Issue ${id}`,
					pr: {
						short: "PR",
						states: { closed: "closed", draft: "draft", merged: "merged", open: "open" },
					},
					updatedAt: (timestamp) => `Updated ${timestamp}`,
				}}
				renderAvatar={(provider) => <span role="img" aria-label={provider}>C</span>}
				session={{ ...baseSession, status: "changes_requested", displayStatus: "Needs human review" }}
			/>,
		);

		expect(screen.getByTestId("board-session-card")).not.toHaveClass(
			"animate-attention-card-pulse",
			"border-status-needs-you",
		);
	});

	it("does not show attention styling when a custom status presentation overrides the card", () => {
		render(
			<SessionCardView
				externalLink={ExternalLink}
				labels={{
					formatTime: () => "5m ago",
					intakeIssue: (id) => `Issue ${id}`,
					pr: {
						short: "PR",
						states: { closed: "closed", draft: "draft", merged: "merged", open: "open" },
					},
					updatedAt: (timestamp) => `Updated ${timestamp}`,
				}}
				renderAvatar={(provider) => <span role="img" aria-label={provider}>C</span>}
				session={{
					...baseSession,
					status: "changes_requested",
					displayStatus: "Changes requested",
					statusPresentation: {
						className: "text-status-working",
						indicatorClassName: "bg-status-working",
						label: "Switching to Codex",
					},
				}}
			/>,
		);

		expect(screen.getByTestId("board-session-card")).not.toHaveClass(
			"animate-attention-card-pulse",
			"border-status-needs-you",
		);
	});

	it("renders a neutral card with grouped multi-PR, usage, and action presentation", () => {
		const onOpen = vi.fn();
		const { container } = render(
			<SessionCardView
				action={<button type="button">Restore</button>}
				branchAction={<button type="button">Copy branch</button>}
				externalLink={ExternalLink}
				labels={{
					formatTime: () => "5m ago",
					intakeIssue: (id) => `Issue ${id}`,
					pr: {
						short: "PR",
						states: { closed: "closed", draft: "draft", merged: "merged", open: "open" },
					},
					updatedAt: (timestamp) => `Updated ${timestamp}`,
				}}
				onOpen={onOpen}
				prs={[
					{
						commentCount: 1,
						number: 10,
						reviewers: [
							{
								id: "ada-lovelace",
								avatarUrl: "https://avatars.githubusercontent.com/u/1?v=4",
							},
						],
						state: "open",
						url: "https://example.com/pull/10",
					},
					{
						commentCount: 1,
						number: 11,
						reviewers: [{ id: "grace-hopper" }],
						state: "open",
						url: "https://example.com/pull/11",
					},
					{ number: 12, state: "merged", url: "https://example.com/pull/12" },
				]}
				renderAvatar={(provider) => <span role="img" aria-label={provider}>C</span>}
				session={{ ...baseSession, branch: "feat/portable", trackerIssueId: "github:42" }}
				usage={{ accessibleLabel: "12,400 tokens", compactLabel: "12.4K tok" }}
			/>,
		);

		expect(screen.getByRole("link", { name: "PR #10 open" })).toHaveAttribute(
			"href",
			"https://example.com/pull/10",
		);
		expect(screen.getByRole("link", { name: "PR #11 open" })).toHaveAttribute(
			"href",
			"https://example.com/pull/11",
		);
		expect(screen.getByRole("link", { name: "PR #12 merged" })).toHaveAttribute(
			"href",
			"https://example.com/pull/12",
		);
		const reviewerAvatar = container.querySelector(
			'img[src="https://avatars.githubusercontent.com/u/1?v=4"]',
		);
		expect(reviewerAvatar).not.toBeNull();
		expect(reviewerAvatar).toHaveAttribute("src", "https://avatars.githubusercontent.com/u/1?v=4");
		expect(reviewerAvatar).toHaveAttribute("referrerpolicy", "no-referrer");
		const fallback = screen.getByText("GH");
		expect(fallback).toHaveAttribute("aria-hidden", "true");
		// The full label is real text, not an aria-label on a generic span, and
		// the compact form is hidden so it is not read out alongside it.
		expect(screen.getByText("12,400 tokens")).toHaveClass("sr-only");
		expect(screen.getByText("12.4K tok")).toHaveAttribute("aria-hidden", "true");
		expect(screen.getByText("5m ago")).toHaveAttribute("title", "Updated 2026-08-09T10:00:00Z");
		expect(screen.getByText("feat/portable")).toHaveClass("text-muted-foreground");
		expect(screen.getByText("5m ago")).toHaveClass("tabular-nums", "text-muted-foreground");
		expect(screen.queryByText("github:42")).not.toBeInTheDocument();

		fireEvent.click(screen.getByRole("button", { name: "portable task" }));
		expect(onOpen).toHaveBeenCalledOnce();
	});

	it("uses the shared loading and error fallback for reviewer avatars", () => {
		const avatarUrl = "https://avatars.githubusercontent.com/ada?size=64";
		render(
			<SessionCardView
				externalLink={ExternalLink}
				labels={{
					formatTime: () => "5m ago",
					intakeIssue: (id) => `Issue ${id}`,
					pr: {
						short: "PR",
						states: { closed: "closed", draft: "draft", merged: "merged", open: "open" },
					},
					updatedAt: (timestamp) => `Updated ${timestamp}`,
				}}
				prs={[{
					commentCount: 1,
					number: 10,
					reviewers: [{ id: "ada-lovelace", avatarUrl }],
					state: "open",
					url: "https://example.com/pull/10",
				}]}
				renderAvatar={(provider) => <span role="img" aria-label={provider}>C</span>}
				session={baseSession}
			/>,
		);

		const prLink = screen.getByRole("link", { name: "PR #10 open" });
		const image = prLink.querySelector("img");
		expect(prLink).toHaveTextContent("AL");
		expect(image).toHaveAttribute("src", avatarUrl);
		if (image) fireEvent.load(image);
		expect(prLink).not.toHaveTextContent("AL");
		if (image) fireEvent.error(image);
		expect(prLink).toHaveTextContent("AL");
		expect(prLink.querySelector("img")).not.toBeInTheDocument();
	});

	it("truncates the status before card metrics can collide", () => {
		render(
			<SessionCardView
				externalLink={ExternalLink}
				labels={{
					formatTime: () => "1h ago",
					intakeIssue: (id) => `Issue ${id}`,
					pr: {
						short: "PR",
						states: { closed: "closed", draft: "draft", merged: "merged", open: "open" },
					},
					updatedAt: (timestamp) => `Updated ${timestamp}`,
				}}
				renderAvatar={(provider) => <span role="img" aria-label={provider}>C</span>}
				session={{ ...baseSession, status: "review_pending" }}
				usage={{ accessibleLabel: "24,600,000 tokens", compactLabel: "24.6M tok" }}
			/>,
		);

		const statusLabel = screen.getByText("Review pending");
		const status = statusLabel.parentElement;
		const statusSlot = status?.parentElement;
		const metadataRow = statusSlot?.parentElement;
		expect(statusLabel).toHaveClass("min-w-0", "truncate");
		expect(status).toHaveClass("min-w-0", "max-w-full");
		expect(statusSlot).toHaveClass("min-w-0", "flex-1");
		expect(metadataRow).toHaveClass("grid", "grid-cols-[minmax(0,1fr)_auto]", "items-center");
		expect(metadataRow).not.toHaveClass("flex-wrap");
		expect(screen.getByText("24.6M tok").parentElement).toHaveClass("shrink-0", "whitespace-nowrap");
	});

	it("prints the daemon's display status in place of the derived status label", () => {
		const labels = {
			formatTime: () => "1h ago",
			intakeIssue: (id: string) => `Issue ${id}`,
			pr: {
				short: "PR",
				states: { closed: "closed", draft: "draft", merged: "merged", open: "open" },
			},
			updatedAt: (timestamp: string) => `Updated ${timestamp}`,
		};
		const { rerender } = render(
			<SessionCardView
				externalLink={ExternalLink}
				labels={labels}
				renderAvatar={(provider) => <span role="img" aria-label={provider}>C</span>}
				session={{
					...baseSession,
					displayStatus: "Fixing CI failures",
					kanbanColumn: "validating",
					status: "ci_failed",
				}}
			/>,
		);
		expect(screen.getByText("Fixing CI failures")).toBeInTheDocument();

		// A daemon too old to derive one leaves the status badge in charge.
		rerender(
			<SessionCardView
				externalLink={ExternalLink}
				labels={labels}
				renderAvatar={(provider) => <span role="img" aria-label={provider}>C</span>}
				session={{ ...baseSession, status: "ci_failed" }}
			/>,
		);
		expect(screen.queryByText("Fixing CI failures")).not.toBeInTheDocument();
		expect(screen.getByText(getSessionStatusView("ci_failed").label)).toBeInTheDocument();
	});

	it("translates the daemon's display status instead of printing raw English", () => {
		render(
			<SessionCardView
				externalLink={ExternalLink}
				labels={{
					formatTime: () => "1h ago",
					intakeIssue: (id) => `Issue ${id}`,
					pr: {
						short: "PR",
						states: { closed: "closed", draft: "draft", merged: "merged", open: "open" },
					},
					updatedAt: (timestamp) => `Updated ${timestamp}`,
				}}
				renderAvatar={(provider) => <span role="img" aria-label={provider}>C</span>}
				session={{
					...baseSession,
					displayStatus: "Fixing CI failures",
					kanbanColumn: "validating",
					status: "ci_failed",
				}}
				translate={(key) => (key === "displayStatus.fixingCiFailures" ? "CI-Fehler werden behoben" : key)}
			/>,
		);

		expect(screen.getByText("CI-Fehler werden behoben")).toBeInTheDocument();
	});

	it("shows a display status this build does not recognize as raw English rather than a key", () => {
		render(
			<SessionCardView
				externalLink={ExternalLink}
				labels={{
					formatTime: () => "1h ago",
					intakeIssue: (id) => `Issue ${id}`,
					pr: {
						short: "PR",
						states: { closed: "closed", draft: "draft", merged: "merged", open: "open" },
					},
					updatedAt: (timestamp) => `Updated ${timestamp}`,
				}}
				renderAvatar={(provider) => <span role="img" aria-label={provider}>C</span>}
				session={{ ...baseSession, displayStatus: "Rebasing onto main", status: "pr_open" }}
				translate={(key) => `translated:${key}`}
			/>,
		);

		expect(screen.getByText("Rebasing onto main")).toBeInTheDocument();
	});

	it("styles the display status with the daemon-owned Kanban column", () => {
		render(
			<SessionCardView
				externalLink={ExternalLink}
				labels={{
					formatTime: () => "1h ago",
					intakeIssue: (id) => `Issue ${id}`,
					pr: {
						short: "PR",
						states: { closed: "closed", draft: "draft", merged: "merged", open: "open" },
					},
					updatedAt: (timestamp) => `Updated ${timestamp}`,
				}}
				renderAvatar={(provider) => <span role="img" aria-label={provider}>C</span>}
				session={{
					...baseSession,
					displayStatus: "Fixing CI failures",
					kanbanColumn: "validating",
					status: "ci_failed",
				}}
			/>,
		);

		const label = screen.getByText("Fixing CI failures");
		const status = label.parentElement;
		expect(status).toHaveAttribute("data-kanban-column", "validating");
		expect(status).toHaveClass("text-status-validating");
		expect(status).not.toHaveClass("rounded-sm", "border");
		expect(status?.style.getPropertyValue("--session-status-tone")).toBe("");
		expect(status?.querySelector(".rounded-full")).toBeNull();
	});

	it("keeps archive toggle height and board offset classes in lockstep", () => {
		expect(archiveToggleHeightClassName).toBe(`h-[${ARCHIVE_TOGGLE_HEIGHT_PX}px]`);
		expect(archiveToggleOffsetClassName).toBe(`pb-[${ARCHIVE_TOGGLE_HEIGHT_PX}px]`);
	});

	it("overlays the archive, keeps cards mounted after collapse, and resets on resetKey", async () => {
		const { rerender } = render(
			<SessionsArchiveView
				labels={{ archive: "Archive", archiveAria: "Archive, 1 session", archivedSessions: "Archived sessions" }}
				renderSessionCard={(session) => <div role="listitem">{session.title}</div>}
				resetKey="p1"
				sessions={[baseSession]}
			/>,
		);

		const archiveButton = screen.getByRole("button", { name: "Archive, 1 session" });
		expect(archiveButton).toHaveClass(archiveToggleHeightClassName, "w-full", "py-0");
		expect(archiveButton.parentElement).toHaveClass("absolute", "inset-x-0", "bottom-0", "bg-background");
		expect(within(archiveButton).getByText("Archive")).toHaveClass("text-2xs", "font-medium");
		expect(within(archiveButton).getByText("Archive")).not.toHaveClass("font-mono", "uppercase");

		fireEvent.click(archiveButton);
		const archive = await screen.findByRole("list", { name: "Archived sessions" });
		expect(archive).toHaveClass("scrollbar-none", "grid", "overflow-y-auto", "max-h-[28vh]");
		const card = within(archive).getByText("portable task");

		fireEvent.click(archiveButton);
		expect(archiveButton).toHaveAttribute("aria-expanded", "false");
		expect(archive).toBeInTheDocument();
		expect(archive).toHaveAttribute("aria-hidden", "true");
		expect(archive).toHaveAttribute("inert");
		expect(archive).toHaveClass("pointer-events-none");
		expect(screen.queryByRole("list", { name: "Archived sessions" })).not.toBeInTheDocument();

		fireEvent.click(archiveButton);
		const reopened = screen.getByRole("list", { name: "Archived sessions" });
		expect(reopened).toBe(archive);
		expect(within(reopened).getByText("portable task")).toBe(card);

		rerender(
			<SessionsArchiveView
				labels={{ archive: "Archive", archiveAria: "Archive, 1 session", archivedSessions: "Archived sessions" }}
				renderSessionCard={(session) => <div role="listitem">{session.title}</div>}
				resetKey="p2"
				sessions={[baseSession]}
			/>,
		);
		expect(screen.getByRole("button", { name: "Archive, 1 session" })).toHaveAttribute("aria-expanded", "false");
		expect(screen.queryByRole("list", { name: "Archived sessions" })).not.toBeInTheDocument();
	});

	it("skips archive motion when the user prefers reduced motion", async () => {
		useReducedMotionMock.mockReturnValue(true);
		render(
			<SessionsArchiveView
				labels={{ archive: "Archive", archiveAria: "Archive, 1 session", archivedSessions: "Archived sessions" }}
				renderSessionCard={(session) => <div role="listitem">{session.title}</div>}
				sessions={[baseSession]}
			/>,
		);

		const archiveButton = screen.getByRole("button", { name: "Archive, 1 session" });
		expect(archiveButton.querySelector("svg")).toHaveClass("transition-none");

		fireEvent.click(archiveButton);
		await screen.findByRole("list", { name: "Archived sessions" });
		expect(lastArchiveMotionTransition.current).toEqual({ duration: 0 });
		expect(archiveButton.querySelector("svg")).toHaveClass("transition-none");
	});
});
