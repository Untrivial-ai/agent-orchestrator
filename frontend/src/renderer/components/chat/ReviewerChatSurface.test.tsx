import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { chatFixtureSettled } from "../../lib/chat-fixture";

const { loadOlderMock, reviewerQuery } = vi.hoisted(() => ({
	loadOlderMock: vi.fn(),
	reviewerQuery: {
		snapshot: undefined as unknown,
		isLoading: false,
		error: undefined,
		hasOlder: true,
		isLoadingOlder: false,
		loadOlder: vi.fn(),
	},
}));

vi.mock("../../hooks/useReviewerConversation", () => ({
	useReviewerConversation: () => reviewerQuery,
	useReviewerConversationCommands: () => ({
		send: vi.fn(),
		resolve: vi.fn(),
		resolveInput: vi.fn(),
		interrupt: vi.fn(),
		busy: false,
		error: undefined,
	}),
}));

import { ReviewerChatSurface } from "./ReviewerChatSurface";

beforeEach(() => {
	loadOlderMock.mockReset();
	reviewerQuery.snapshot = chatFixtureSettled;
	reviewerQuery.loadOlder = loadOlderMock;
});

describe("ReviewerChatSurface", () => {
	it("offers the reviewer history page loader", () => {
		render(<ReviewerChatSurface hideHeader reviewId="review-1" />);

		fireEvent.click(screen.getByRole("button", { name: "Load earlier messages" }));

		expect(loadOlderMock).toHaveBeenCalledOnce();
	});
});
