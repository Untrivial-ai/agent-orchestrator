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
	type BoardSplitLaneLabels,
} from "./SessionsBoardView";
import {
	boardAttentionZoneOrder,
	getAttentionZoneViewForZone,
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

const splitLabels: BoardSplitLaneLabels = {
	columnAria: (label) => `${label} sessions`,
	countSessions: (count, label) => `${count} ${label} session${count === 1 ? "" : "s"}`,
	idleWorkingAria: "Idle / Working sessions",
	laneSummary: (primary, secondary) => `${primary} / ${secondary} lane summary`,
	readyMergedAria: "Ready / Merged sessions",
	tones: {
		idle: { countLabel: "idle", label: "Idle", regionLabel: "Idle sessions" },
		working: { countLabel: "working", label: "Working", regionLabel: "Working sessions" },
		ready: { countLabel: "ready", label: "Ready", regionLabel: "Ready sessions" },
		merged: { countLabel: "merged", label: "Merged", regionLabel: "Merged sessions" },
	},
};

const baseSession: BoardSessionPresentation = {
	id: "session-1",
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

	it("renders portable split lanes with one shared scroller", () => {
		const sessions: BoardSessionPresentation[] = [
			baseSession,
			{ ...baseSession, id: "working", status: "working", title: "working task" },
			{ ...baseSession, id: "ready", status: "mergeable", title: "ready task" },
			{ ...baseSession, id: "merged", status: "merged", title: "merged task" },
		];
		render(
			<SessionsBoardGridView
				columns={boardAttentionZoneOrder.map((zone) => getAttentionZoneViewForZone(zone))}
				labels={splitLabels}
				renderSessionCard={(session) => <div data-testid={`card-${session.id}`}>{session.title}</div>}
				sessions={sessions}
			/>,
		);

		const workLane = screen.getByRole("region", { name: "Idle / Working sessions" });
		expect(within(workLane).getByRole("region", { name: "Idle sessions" })).toHaveTextContent("portable task");
		expect(within(workLane).getByRole("region", { name: "Working sessions" })).toHaveTextContent("working task");
		expect(workLane.querySelectorAll(".overflow-y-auto")).toHaveLength(1);

		const mergeLane = screen.getByRole("region", { name: "Ready / Merged sessions" });
		expect(within(mergeLane).getByLabelText("1 ready session")).toHaveTextContent("1");
		expect(within(mergeLane).getByLabelText("1 merged session")).toHaveTextContent("1");
		expect(screen.getByTestId("board-horizontal-scroll")).toHaveClass("board-horizontal-scrollbar");
	});

	it("renders a neutral card with grouped multi-PR, usage, and action presentation", () => {
		const onOpen = vi.fn();
		render(
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
					{ number: 10, state: "open", url: "https://example.com/pull/10" },
					{ number: 11, state: "open", url: "https://example.com/pull/11" },
					{ number: 12, state: "merged", url: "https://example.com/pull/12" },
				]}
				renderAvatar={(provider) => <span role="img" aria-label={provider}>C</span>}
				session={{ ...baseSession, branch: "feat/portable", trackerIssueId: "github:42" }}
				usage={{ accessibleLabel: "12,400 tokens", compactLabel: "12.4K tok" }}
			/>,
		);

		expect(screen.getByLabelText("#10, #11 open")).toHaveTextContent("PR#10,#11open");
		expect(screen.getByLabelText("#12 merged")).toHaveTextContent("PR#12merged");
		expect(screen.getByText("12.4K tok")).toHaveAccessibleName("12,400 tokens");
		expect(screen.getByText("5m ago")).toHaveAttribute("title", "Updated 2026-08-09T10:00:00Z");
		expect(screen.getByText("github:42")).toHaveAttribute("title", "Issue github:42");

		fireEvent.click(screen.getByRole("button", { name: "portable task" }));
		expect(onOpen).toHaveBeenCalledOnce();
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
		const metadataRow = status?.parentElement;
		expect(statusLabel).toHaveClass("min-w-0", "truncate");
		expect(status).toHaveClass("min-w-0", "flex-1");
		expect(metadataRow).toHaveClass("flex", "items-center", "gap-2");
		expect(metadataRow).not.toHaveClass("flex-wrap");
		expect(screen.getByText("24.6M tok").parentElement).toHaveClass("shrink-0", "whitespace-nowrap");
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
