import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { readChatSessionDraft } from "../lib/chat-drafts";
import { StartupSessionDraft } from "./StartupSessionDraft";

const session = {
	sessionId: "startup-draft-session",
	projectId: "mer",
	incarnation: "2026-09-22T00:00:00.000Z",
	title: "Last session",
};

afterEach(() => {
	window.localStorage.clear();
	vi.restoreAllMocks();
});

describe("StartupSessionDraft", () => {
	it("keeps typed text in the local draft and does not send it", async () => {
		const user = userEvent.setup();
		const fetchSpy = vi.spyOn(globalThis, "fetch");
		render(<StartupSessionDraft session={session} />);

		const composer = await screen.findByTestId("startup-session-composer");
		await waitFor(() => expect(composer).toBeEnabled());
		await user.type(composer, "hold this");

		expect(readChatSessionDraft({ sessionId: session.sessionId, incarnation: session.incarnation }).composer.text).toBe(
			"hold this",
		);
		const send = screen.getByTestId("startup-session-send");
		expect(send).toBeDisabled();
		fireEvent.submit(send.closest("form") as HTMLFormElement);
		expect(fetchSpy).not.toHaveBeenCalled();
	});
});
