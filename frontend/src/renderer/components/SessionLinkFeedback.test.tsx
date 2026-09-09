import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useUiStore } from "../stores/ui-store";
import { SessionLinkFeedback } from "./SessionLinkFeedback";

describe("SessionLinkFeedback", () => {
	beforeEach(() => {
		vi.useFakeTimers();
		useUiStore.setState({
			sessionLinkError: null,
			sessionLinkNotices: [],
			sessionLinkNoticeSequence: 0,
		});
	});

	afterEach(() => {
		vi.useRealTimers();
	});

	it("expires a terminated-session notice after five seconds", () => {
		useUiStore.getState().showSessionLinkNotice("Session hosted-ao-109 is terminated");
		render(<SessionLinkFeedback />);

		expect(screen.getByText("Session hosted-ao-109 is terminated")).toBeInTheDocument();
		act(() => vi.advanceTimersByTime(4_999));
		expect(screen.getByText("Session hosted-ao-109 is terminated")).toBeInTheDocument();
		act(() => vi.advanceTimersByTime(1));
		expect(screen.queryByText("Session hosted-ao-109 is terminated")).not.toBeInTheDocument();
	});

	it("dismisses one notice without removing the rest", () => {
		const store = useUiStore.getState();
		store.showSessionLinkNotice("Session first is terminated");
		store.showSessionLinkNotice("Session second is terminated");
		render(<SessionLinkFeedback />);

		fireEvent.click(screen.getAllByRole("button", { name: "Dismiss session link error" })[0]!);
		expect(screen.queryByText("Session second is terminated")).not.toBeInTheDocument();
		expect(screen.getByText("Session first is terminated")).toBeInTheDocument();
	});

	it("stacks repeated notices in a four-item viewport with scroll overflow", () => {
		for (let index = 1; index <= 5; index += 1) {
			useUiStore.getState().showSessionLinkNotice(`Session session-${index} is terminated`);
		}
		render(<SessionLinkFeedback />);

		expect(screen.getAllByRole("status")).toHaveLength(5);
		const stack = screen.getByTestId("session-link-notifications");
		expect(stack).toHaveClass("max-h-[184px]", "overflow-y-auto");
		expect(screen.getAllByRole("status")[0]).toHaveTextContent("Session session-5 is terminated");
	});
});
