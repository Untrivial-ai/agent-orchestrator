import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Fragment } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { aoBridge } from "../../lib/bridge";
import { getChatDraftBoundary, setChatDraftBoundary } from "../../lib/chat-draft-boundary";
import {
	elicitationDraftKey,
	readElicitationDraft,
	resetElicitationDraftPruning,
	writeElicitationDraft,
} from "../../lib/elicitation-drafts";
import type { ConversationActivity } from "../../types/conversation";
import { ElicitationDock, elicitationBoundarySource, resetUnsavedElicitationDraftMemory } from "./ElicitationDock";

/** Test hygiene: chat-draft-boundary is a module-level store with no reset, so any test that sets one clears its own slot when it's done. */
function clearElicitationBoundary(sessionId: string, requestId: string) {
	setChatDraftBoundary(sessionId, elicitationBoundarySource(requestId), undefined);
}

function activity(detail: ConversationActivity["detail"]): ConversationActivity {
	return {
		kind: "activity",
		id: "question-1",
		sequence: 1,
		revision: 0,
		activityKind: "user_input",
		status: "pending",
		summary: "Choose a direction",
		requestId: "request-1",
		detail,
		createdAt: "2026-08-04T00:00:00Z",
	};
}

describe("ElicitationDock", () => {
	beforeEach(() => {
		window.localStorage.clear();
		resetElicitationDraftPruning();
		resetUnsavedElicitationDraftMemory();
	});

	const claudeQuestions = {
		type: "object" as const,
		required: ["question_0", "question_1"],
		properties: {
			question_0: {
				type: "string",
				title: "Approach",
				oneOf: [
					{ const: "Native", title: "Native", description: "Use ACP directly" },
					{ const: "Bridge", title: "Bridge" },
				],
			},
			question_0_custom: { type: "string", title: "Other approach" },
			question_1: {
				type: "string",
				title: "Language",
				oneOf: [
					{ const: "Go", title: "Go" },
					{ const: "TypeScript", title: "TypeScript" },
				],
			},
			question_1_custom: { type: "string", title: "Other language" },
		},
	};

	it("shows one Claude question and its Other field at a time", () => {
		render(
			<ElicitationDock
				activity={activity({ inputMode: "form", schema: claudeQuestions })}
				onResolve={vi.fn()}
			/>,
		);

		expect(screen.getByRole("group", { name: /Approach/ })).toBeInTheDocument();
		expect(screen.getByLabelText("Other approach")).toBeInTheDocument();
		expect(screen.queryByRole("group", { name: /Language/ })).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Other language")).not.toBeInTheDocument();
	});

	it("validates the active Claude question before moving forward", async () => {
		const user = userEvent.setup();
		render(
			<ElicitationDock
				activity={activity({ inputMode: "form", schema: claudeQuestions })}
				onResolve={vi.fn()}
			/>,
		);

		await user.click(screen.getByRole("button", { name: "Next" }));

		expect(screen.getByText("Choose an answer.")).toBeInTheDocument();
		expect(screen.queryByRole("group", { name: /Language/ })).not.toBeInTheDocument();
	});

	it("navigates Claude questions and preserves answers when going back", async () => {
		const user = userEvent.setup();
		render(
			<ElicitationDock
				activity={activity({ inputMode: "form", schema: claudeQuestions })}
				onResolve={vi.fn()}
			/>,
		);

		await user.click(screen.getByRole("radio", { name: /Native/ }));
		await user.type(screen.getByLabelText("Other approach"), "Hybrid");
		await user.click(screen.getByRole("button", { name: "Next" }));
		expect(screen.getByRole("group", { name: /Language/ })).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Back" }));
		expect(screen.getByRole("radio", { name: /Native/ })).toBeChecked();
		expect(screen.getByLabelText("Other approach")).toHaveValue("Hybrid");
	});

	it("submits all Claude answers together from the final question", async () => {
		const user = userEvent.setup();
		const onResolve = vi.fn().mockResolvedValue(undefined);
		render(
			<ElicitationDock
				activity={activity({
					inputMode: "form",
					message: "Which implementation should we use?",
					schema: claudeQuestions,
				})}
				onResolve={onResolve}
			/>,
		);

		await user.click(screen.getByRole("radio", { name: /Native/ }));
		await user.type(screen.getByLabelText("Other approach"), "Hybrid");
		await user.click(screen.getByRole("button", { name: "Next" }));
		await user.click(screen.getByRole("radio", { name: "Go" }));
		await user.type(screen.getByLabelText("Other language"), "Rust");
		await user.click(screen.getByRole("button", { name: "Continue" }));
		expect(onResolve).toHaveBeenCalledWith("request-1", "accept", {
			question_0: "Native",
			question_0_custom: "Hybrid",
			question_1: "Go",
			question_1_custom: "Rust",
		});
	});

	it("restores a typed custom answer after the dock is unmounted and remounted", async () => {
		const user = userEvent.setup();
		const dock = (
			<ElicitationDock
				activity={activity({ inputMode: "form", schema: claudeQuestions })}
				conversationId="conversation-1"
				onResolve={vi.fn()}
			/>
		);
		const first = render(dock);

		await user.click(screen.getByRole("radio", { name: /Native/ }));
		await user.type(screen.getByLabelText("Other approach"), "Hybrid");
		// Switching sessions unmounts the whole Chat surface.
		first.unmount();

		render(dock);
		expect(screen.getByRole("radio", { name: /Native/ })).toBeChecked();
		expect(screen.getByLabelText("Other approach")).toHaveValue("Hybrid");
	});

	it("restores the question the human had reached", async () => {
		const user = userEvent.setup();
		const dock = (
			<ElicitationDock
				activity={activity({ inputMode: "form", schema: claudeQuestions })}
				conversationId="conversation-1"
				onResolve={vi.fn()}
			/>
		);
		const first = render(dock);

		await user.click(screen.getByRole("radio", { name: /Native/ }));
		await user.click(screen.getByRole("button", { name: "Next" }));
		first.unmount();

		render(dock);
		expect(screen.getByRole("group", { name: /Language/ })).toBeInTheDocument();
	});

	it("drops the draft once the question is answered", async () => {
		const user = userEvent.setup();
		const dock = (
			<ElicitationDock
				activity={activity({ inputMode: "form", schema: claudeQuestions })}
				conversationId="conversation-1"
				onResolve={vi.fn().mockResolvedValue(undefined)}
			/>
		);
		const first = render(dock);

		await user.click(screen.getByRole("radio", { name: /Native/ }));
		await user.type(screen.getByLabelText("Other approach"), "Hybrid");
		await user.click(screen.getByRole("button", { name: "Next" }));
		await user.click(screen.getByRole("radio", { name: "Go" }));
		await user.click(screen.getByRole("button", { name: "Continue" }));
		first.unmount();

		render(dock);
		expect(screen.getByLabelText("Other approach")).toHaveValue("");
		expect(screen.getByRole("radio", { name: /Native/ })).not.toBeChecked();
	});

	it("starts a replacing question from scratch instead of inheriting answers", async () => {
		const user = userEvent.setup();
		const first = activity({ inputMode: "form", schema: claudeQuestions });
		const view = render(<ElicitationDock activity={first} conversationId="conversation-1" onResolve={vi.fn()} />);

		await user.click(screen.getByRole("radio", { name: /Native/ }));
		await user.type(screen.getByLabelText("Other approach"), "Hybrid");

		// A second question arrives in the same dock, without it unmounting.
		view.rerender(
			<ElicitationDock
				activity={{ ...first, id: "question-2", requestId: "request-2" }}
				conversationId="conversation-1"
				onResolve={vi.fn()}
			/>,
		);

		expect(screen.getByLabelText("Other approach")).toHaveValue("");
		expect(screen.getByRole("radio", { name: /Native/ })).not.toBeChecked();
		expect(readElicitationDraft("conversation-1", "request-2")?.values.question_0_custom).toBeUndefined();
	});

	it("stores nothing for a question that was only shown", () => {
		const shown = activity({ inputMode: "form", schema: claudeQuestions });
		const view = render(<ElicitationDock activity={shown} conversationId="conversation-1" onResolve={vi.fn()} />);
		view.unmount();

		expect(readElicitationDraft("conversation-1", "request-1")).toBeUndefined();
	});

	it("saves going Back from a restored draft with no other edits", async () => {
		const user = userEvent.setup();
		const shown = activity({ inputMode: "form", schema: claudeQuestions });
		writeElicitationDraft("conversation-1", "request-1", { values: { question_0: "Native" }, activeQuestion: 1 });
		const dock = <ElicitationDock activity={shown} conversationId="conversation-1" onResolve={vi.fn()} />;
		const first = render(dock);

		await user.click(screen.getByRole("button", { name: "Back" }));
		first.unmount();

		render(dock);
		expect(screen.getByRole("group", { name: /Approach/ })).toBeInTheDocument();
	});

	it("saves advancing with Next from a restored draft with no other edits", async () => {
		const user = userEvent.setup();
		const shown = activity({ inputMode: "form", schema: claudeQuestions });
		writeElicitationDraft("conversation-1", "request-1", { values: { question_0: "Native" }, activeQuestion: 0 });
		const dock = <ElicitationDock activity={shown} conversationId="conversation-1" onResolve={vi.fn()} />;
		const first = render(dock);

		await user.click(screen.getByRole("button", { name: "Next" }));
		first.unmount();

		render(dock);
		expect(screen.getByRole("group", { name: /Language/ })).toBeInTheDocument();
	});

	it("keeps a rejected answer in the draft so it comes back after a remount", async () => {
		const user = userEvent.setup();
		const dock = (
			<ElicitationDock
				activity={activity({ inputMode: "form", schema: claudeQuestions })}
				conversationId="conversation-1"
				onResolve={vi.fn().mockRejectedValue(new Error("network down"))}
			/>
		);
		const first = render(dock);

		await user.click(screen.getByRole("radio", { name: /Native/ }));
		await user.type(screen.getByLabelText("Other approach"), "Hybrid");
		await user.click(screen.getByRole("button", { name: "Next" }));
		await user.click(screen.getByRole("radio", { name: "Go" }));
		await user.click(screen.getByRole("button", { name: "Continue" }));
		await screen.findByRole("alert");
		first.unmount();

		render(dock);
		// The draft was saved at the second question, so that's what comes back.
		expect(screen.getByRole("radio", { name: "Go" })).toBeChecked();
		await user.click(screen.getByRole("button", { name: "Back" }));
		expect(screen.getByRole("radio", { name: /Native/ })).toBeChecked();
		expect(screen.getByLabelText("Other approach")).toHaveValue("Hybrid");
	});

	it("retries a failed draft write until storage recovers, without needing another edit", () => {
		vi.useFakeTimers();
		try {
			// A failed write must not be mistaken for an already-saved one: nothing
			// else would prompt a retry if the human never touches the form again.
			// Only the draft write itself fails once — not the unrelated sweep
			// marker the dock also writes on mount.
			let failNextDraftWrite = true;
			const originalSetItem = window.localStorage.setItem.bind(window.localStorage);
			vi.spyOn(window.localStorage, "setItem").mockImplementation((key: string, value: string) => {
				if (failNextDraftWrite && key === elicitationDraftKey("conversation-1", "request-1")) {
					failNextDraftWrite = false;
					throw new DOMException("quota exceeded", "QuotaExceededError");
				}
				originalSetItem(key, value);
			});

			render(
				<ElicitationDock
					activity={activity({ inputMode: "form", schema: claudeQuestions })}
					conversationId="conversation-1"
					onResolve={vi.fn()}
				/>,
			);

			fireEvent.click(screen.getByRole("radio", { name: /Native/ }));
			expect(readElicitationDraft("conversation-1", "request-1")).toBeUndefined();

			act(() => {
				vi.advanceTimersByTime(3000);
			});

			expect(readElicitationDraft("conversation-1", "request-1")?.values.question_0).toBe("Native");
		} finally {
			vi.useRealTimers();
			vi.restoreAllMocks();
		}
	});

	it("reports a failed write through the leave/quit draft boundary, and keeps it set through an unmount that still can't save", () => {
		// Dropping the sessionId prop, always passing undefined to
		// setChatDraftBoundary, or clearing it unconditionally on unmount would
		// each keep every other test in this file green — none of them look at
		// the boundary. Left unreported, a failed save gives no warning before
		// the human navigates away or quits with an unsent answer. Clearing it
		// on every unmount regardless of outcome is just as wrong the other
		// way: plenty of unmounts (ending a queued-message edit, closing the
		// reviewer overlay, a refetch error swapping the view) never go through
		// the leave/quit guards at all, so an unconditional clear would drop
		// the warning while the answer is still genuinely unsaved.
		vi.spyOn(window.localStorage, "setItem").mockImplementation((key: string) => {
			if (key === elicitationDraftKey("conversation-1", "request-1")) {
				throw new DOMException("quota exceeded", "QuotaExceededError");
			}
		});

		try {
			const view = render(
				<ElicitationDock
					activity={activity({ inputMode: "form", schema: claudeQuestions })}
					sessionId="session-1"
					conversationId="conversation-1"
					onResolve={vi.fn()}
				/>,
			);
			expect(getChatDraftBoundary("session-1")).toBeUndefined();

			fireEvent.click(screen.getByRole("radio", { name: /Native/ }));
			expect(getChatDraftBoundary("session-1")).toBe("elicitation-persistence-failed");
			expect(screen.getByRole("alert")).toHaveTextContent(/couldn.?t be saved/i);

			view.unmount();
			// Storage is still failing: the warning must not have quietly gone away.
			expect(getChatDraftBoundary("session-1")).toBe("elicitation-persistence-failed");
		} finally {
			vi.restoreAllMocks();
			clearElicitationBoundary("session-1", "request-1");
		}
	});

	it("saves and clears the boundary on unmount if storage recovers, even without another edit", () => {
		let failing = true;
		const originalSetItem = window.localStorage.setItem.bind(window.localStorage);
		vi.spyOn(window.localStorage, "setItem").mockImplementation((key: string, value: string) => {
			if (failing && key === elicitationDraftKey("conversation-1", "request-1")) {
				throw new DOMException("quota exceeded", "QuotaExceededError");
			}
			originalSetItem(key, value);
		});

		try {
			const view = render(
				<ElicitationDock
					activity={activity({ inputMode: "form", schema: claudeQuestions })}
					sessionId="session-1"
					conversationId="conversation-1"
					onResolve={vi.fn()}
				/>,
			);

			fireEvent.click(screen.getByRole("radio", { name: /Native/ }));
			expect(getChatDraftBoundary("session-1")).toBe("elicitation-persistence-failed");

			// The failure clears (quota freed elsewhere) before the first retry
			// fires and before any further edit — an unmount here is the only
			// remaining chance to save it.
			failing = false;
			view.unmount();

			expect(getChatDraftBoundary("session-1")).toBeUndefined();
			expect(readElicitationDraft("conversation-1", "request-1")?.values.question_0).toBe("Native");
		} finally {
			vi.restoreAllMocks();
		}
	});

	it("retries once the form re-enables after a rejected resolve, without another edit", async () => {
		// A rejected resolve leaves values/activeQuestion unchanged, so
		// re-enabling the form has to count as its own reason to retry — nothing
		// about the content itself changed to prompt one.
		const user = userEvent.setup();
		let failing = true;
		const originalSetItem = window.localStorage.setItem.bind(window.localStorage);
		vi.spyOn(window.localStorage, "setItem").mockImplementation((key: string, value: string) => {
			if (failing && key === elicitationDraftKey("conversation-1", "request-1")) {
				throw new DOMException("quota exceeded", "QuotaExceededError");
			}
			originalSetItem(key, value);
		});

		try {
			render(
				<ElicitationDock
					activity={activity({ inputMode: "form", schema: claudeQuestions })}
					sessionId="session-1"
					conversationId="conversation-1"
					onResolve={vi.fn().mockRejectedValue(new Error("network down"))}
				/>,
			);

			await user.click(screen.getByRole("radio", { name: /Native/ }));
			expect(getChatDraftBoundary("session-1")).toBe("elicitation-persistence-failed");

			// Storage recovers, but nothing edits the form again — only the
			// disable/re-enable cycle around the rejected Skip should retry.
			failing = false;
			await user.click(screen.getByRole("button", { name: "Skip" }));

			await waitFor(() => {
				expect(readElicitationDraft("conversation-1", "request-1")?.values.question_0).toBe("Native");
			});
			expect(getChatDraftBoundary("session-1")).toBeUndefined();
		} finally {
			vi.restoreAllMocks();
		}
	});

	it("clears the boundary and cancels the pending retry when the answer reverts to what storage already has", async () => {
		// Picking Native saves; Bridge then fails to save and leaves a retry
		// pending; picking Native again matches what storage already holds. A
		// write effect that only compares against the last *successful* write
		// would return early on that match, leaving the failure flagged and the
		// retry still armed to rewrite the same answer.
		const user = userEvent.setup();
		const originalSetItem = window.localStorage.setItem.bind(window.localStorage);
		vi.spyOn(window.localStorage, "setItem").mockImplementation((key: string, value: string) => {
			if (key === elicitationDraftKey("conversation-1", "request-1") && JSON.parse(value).values.question_0 === "Bridge") {
				throw new DOMException("quota exceeded", "QuotaExceededError");
			}
			originalSetItem(key, value);
		});

		try {
			render(
				<ElicitationDock
					activity={activity({ inputMode: "form", schema: claudeQuestions })}
					sessionId="session-1"
					conversationId="conversation-1"
					onResolve={vi.fn()}
				/>,
			);

			await user.click(screen.getByRole("radio", { name: /Native/ }));
			expect(getChatDraftBoundary("session-1")).toBeUndefined();

			await user.click(screen.getByRole("radio", { name: /Bridge/ }));
			expect(getChatDraftBoundary("session-1")).toBe("elicitation-persistence-failed");

			await user.click(screen.getByRole("radio", { name: /Native/ }));
			expect(getChatDraftBoundary("session-1")).toBeUndefined();
			expect(readElicitationDraft("conversation-1", "request-1")?.values.question_0).toBe("Native");
		} finally {
			vi.restoreAllMocks();
		}
	});

	it("does not let one dock on a session clear another's still-failing boundary", async () => {
		// The reviewer overlay reports the same session id as its underlying
		// worker chat while running its own conversation, so two docks can be
		// mounted under one sessionId at once. Each request gets its own
		// leave/quit-guard slot for exactly this: either dock succeeding,
		// resolving, or unmounting must not silently clear a warning that still
		// belongs to the other one's unsaved answer.
		const user = userEvent.setup();
		const originalSetItem = window.localStorage.setItem.bind(window.localStorage);
		vi.spyOn(window.localStorage, "setItem").mockImplementation((key: string, value: string) => {
			if (key === elicitationDraftKey("worker-conversation", "request-1")) {
				throw new DOMException("quota exceeded", "QuotaExceededError");
			}
			originalSetItem(key, value);
		});

		try {
			const worker = render(
				<ElicitationDock
					activity={activity({ inputMode: "form", schema: claudeQuestions })}
					sessionId="session-1"
					conversationId="worker-conversation"
					onResolve={vi.fn()}
				/>,
			);
			await user.click(screen.getByRole("radio", { name: /Native/ }));
			expect(getChatDraftBoundary("session-1")).toBe("elicitation-persistence-failed");

			const overlay = render(
				<ElicitationDock
					activity={{
						...activity({
							inputMode: "form",
							schema: { type: "object", properties: { name: { type: "string", title: "Name" } } },
						}),
						id: "overlay-question",
						requestId: "overlay-request",
					}}
					sessionId="session-1"
					conversationId="overlay-conversation"
					onResolve={vi.fn().mockResolvedValue(undefined)}
				/>,
			);
			const overlayScreen = within(overlay.container);
			await user.type(overlayScreen.getByLabelText("Name"), "Alice");
			// The overlay's own answer actually saved — checked directly, not just
			// inferred later from a click succeeding — before Continue clears it.
			expect(readElicitationDraft("overlay-conversation", "overlay-request")?.values.name).toBe("Alice");

			await user.click(overlayScreen.getByRole("button", { name: "Continue" }));
			await waitFor(() => expect(overlayScreen.getByRole("button", { name: "Sending answer" })).toBeInTheDocument());

			// The worker's is still unsaved.
			expect(getChatDraftBoundary("session-1")).toBe("elicitation-persistence-failed");

			overlay.unmount();
			expect(getChatDraftBoundary("session-1")).toBe("elicitation-persistence-failed");

			worker.unmount();
		} finally {
			vi.restoreAllMocks();
			clearElicitationBoundary("session-1", "request-1");
		}
	});

	it("restores a not-yet-saved answer across a same-tick remount, not a stale value from storage", async () => {
		// Starting or ending a queued-message edit re-keys the composer that
		// contains this dock, so it unmounts and remounts in the same commit for
		// a reason unrelated to the question itself. React renders the new
		// instance — including this lazy read — before the old instance's own
		// unmount effect runs, so storage can still hold the previous, already-
		// superseded answer at the moment this reads it. The in-memory cache
		// holds the newer one instead.
		let failing = false;
		const originalSetItem = window.localStorage.setItem.bind(window.localStorage);
		vi.spyOn(window.localStorage, "setItem").mockImplementation((key: string, value: string) => {
			if (failing && key === elicitationDraftKey("conversation-1", "request-1")) {
				throw new DOMException("quota exceeded", "QuotaExceededError");
			}
			originalSetItem(key, value);
		});

		try {
			const dock = (
				<ElicitationDock
					activity={activity({ inputMode: "form", schema: claudeQuestions })}
					sessionId="session-1"
					conversationId="conversation-1"
					onResolve={vi.fn()}
				/>
			);
			// Re-keying the host remounts the dock in a single commit, the way a
			// queued edit re-keys the composer.
			const Host = ({ generation }: { generation: number }) => <Fragment key={generation}>{dock}</Fragment>;
			const view = render(<Host generation={1} />);
			fireEvent.click(screen.getByRole("radio", { name: /Native/ }));
			expect(readElicitationDraft("conversation-1", "request-1")?.values.question_0).toBe("Native");

			failing = true;
			fireEvent.click(screen.getByRole("radio", { name: /Bridge/ }));

			view.rerender(<Host generation={2} />);

			// Storage still holds the older Native; the newer, unsaved Bridge comes
			// from the in-memory cache, along with its still-failing state.
			expect(screen.getByRole("radio", { name: /Bridge/ })).toBeChecked();
			expect(screen.getByRole("alert")).toHaveTextContent(/couldn.?t be saved/i);
			expect(getChatDraftBoundary("session-1")).toBe("elicitation-persistence-failed");
		} finally {
			vi.restoreAllMocks();
			clearElicitationBoundary("session-1", "request-1");
		}
	});

	it("backs off between retries instead of hammering storage every few seconds forever", () => {
		vi.useFakeTimers();
		try {
			vi.spyOn(window.localStorage, "setItem").mockImplementation((key: string) => {
				if (key === elicitationDraftKey("conversation-1", "request-1")) {
					throw new DOMException("quota exceeded", "QuotaExceededError");
				}
			});

			render(
				<ElicitationDock
					activity={activity({ inputMode: "form", schema: claudeQuestions })}
					sessionId="session-1"
					conversationId="conversation-1"
					onResolve={vi.fn()}
				/>,
			);

			fireEvent.click(screen.getByRole("radio", { name: /Native/ }));
			const setItemCallsAfter = (ms: number) => {
				const calls = vi.mocked(window.localStorage.setItem).mock.calls.length;
				act(() => {
					vi.advanceTimersByTime(ms);
				});
				return vi.mocked(window.localStorage.setItem).mock.calls.length - calls;
			};

			// First retry at the base delay (3s); a second one right after that
			// same delay must not have fired yet — the delay has grown.
			expect(setItemCallsAfter(3000)).toBe(1);
			expect(setItemCallsAfter(3000)).toBe(0);
			expect(setItemCallsAfter(3000)).toBe(1);
		} finally {
			vi.useRealTimers();
			vi.restoreAllMocks();
			clearElicitationBoundary("session-1", "request-1");
		}
	});

	it("does not resurrect the draft on unmount after a successful resolve", async () => {
		const user = userEvent.setup();
		let failing = true;
		const originalSetItem = window.localStorage.setItem.bind(window.localStorage);
		vi.spyOn(window.localStorage, "setItem").mockImplementation((key: string, value: string) => {
			if (failing && key === elicitationDraftKey("conversation-1", "request-1")) {
				throw new DOMException("quota exceeded", "QuotaExceededError");
			}
			originalSetItem(key, value);
		});

		try {
			const view = render(
				<ElicitationDock
					activity={activity({ inputMode: "form", schema: claudeQuestions })}
					sessionId="session-1"
					conversationId="conversation-1"
					onResolve={vi.fn().mockResolvedValue(undefined)}
				/>,
			);
			await user.click(screen.getByRole("radio", { name: /Native/ }));
			await user.click(screen.getByRole("button", { name: "Next" }));
			await user.click(screen.getByRole("radio", { name: "Go" }));
			await user.click(screen.getByRole("button", { name: "Continue" }));
			await waitFor(() => expect(getChatDraftBoundary("session-1")).toBeUndefined());

			failing = false;
			view.unmount();

			expect(readElicitationDraft("conversation-1", "request-1")).toBeUndefined();
			expect(getChatDraftBoundary("session-1")).toBeUndefined();
		} finally {
			vi.restoreAllMocks();
			clearElicitationBoundary("session-1", "request-1");
		}
	});

	it("hides the failed-save alert once the form disables for a resolve, successful or not", async () => {
		// The write effect bails out entirely while disabled, so nothing else
		// would clear the underlying failure state in that window — the alert
		// itself is hidden instead, since it's stale either way once a resolve
		// is in flight or just delivered.
		const user = userEvent.setup();
		vi.spyOn(window.localStorage, "setItem").mockImplementation((key: string) => {
			if (key === elicitationDraftKey("conversation-1", "request-1")) {
				throw new DOMException("quota exceeded", "QuotaExceededError");
			}
		});

		try {
			render(
				<ElicitationDock
					activity={activity({ inputMode: "form", schema: claudeQuestions })}
					sessionId="session-1"
					conversationId="conversation-1"
					onResolve={vi.fn().mockResolvedValue(undefined)}
				/>,
			);

			await user.click(screen.getByRole("radio", { name: /Native/ }));
			await user.click(screen.getByRole("button", { name: "Next" }));
			await user.click(screen.getByRole("radio", { name: "Go" }));
			expect(screen.getByRole("alert")).toHaveTextContent(/couldn.?t be saved/i);

			await user.click(screen.getByRole("button", { name: "Continue" }));

			expect(screen.queryByRole("alert")).not.toBeInTheDocument();
		} finally {
			vi.restoreAllMocks();
			clearElicitationBoundary("session-1", "request-1");
		}
	});

	it.each(["decline", "cancel"] as const)("drops the draft on %s the same as on accept", async (action) => {
		const user = userEvent.setup();
		const dock = (
			<ElicitationDock
				activity={activity({ inputMode: "form", schema: claudeQuestions })}
				conversationId="conversation-1"
				onResolve={vi.fn().mockResolvedValue(undefined)}
			/>
		);
		const first = render(dock);

		await user.click(screen.getByRole("radio", { name: /Native/ }));
		await user.click(screen.getByRole("button", { name: action === "decline" ? "Skip" : "Cancel" }));
		first.unmount();

		render(dock);
		expect(screen.getByRole("radio", { name: /Native/ })).not.toBeChecked();
	});

	it("keeps drafts for two conversations separate even when they share a session id", () => {
		// A reviewer-chat overlay reports the same sessionId as its underlying
		// worker chat while reading a different conversation. If the draft were
		// scoped by session, one chat's answer could resurface pre-filled in the
		// other's identically-shaped question.
		const shown = activity({ inputMode: "form", schema: claudeQuestions });
		writeElicitationDraft("other-conversation", "request-1", {
			values: { question_0: "Bridge", question_0_custom: "answer from the other chat" },
			activeQuestion: 1,
		});

		render(<ElicitationDock activity={shown} conversationId="conversation-1" onResolve={vi.fn()} />);

		expect(screen.getByRole("radio", { name: /Native/ })).not.toBeChecked();
		expect(screen.getByRole("radio", { name: /Bridge/ })).not.toBeChecked();
		expect(screen.getByRole("group", { name: /Approach/ })).toBeInTheDocument();
		// The other conversation's entry is left untouched, not consumed or deleted.
		expect(readElicitationDraft("other-conversation", "request-1")?.values.question_0_custom).toBe(
			"answer from the other chat",
		);
	});

	it("clamps a restored question index that no longer fits the question set", () => {
		const shown = activity({ inputMode: "form", schema: claudeQuestions });
		writeElicitationDraft("conversation-1", "request-1", { values: { question_0: "Native" }, activeQuestion: 99 });

		render(<ElicitationDock activity={shown} conversationId="conversation-1" onResolve={vi.fn()} />);

		// Falls back to the last real question, not to every field shown at once.
		expect(screen.getByRole("group", { name: /Language/ })).toBeInTheDocument();
	});

	it("truncates a non-integer restored question index instead of showing every field at once", () => {
		// Only reachable from corrupted storage; a fractional index would otherwise
		// miss `questionGroups[activeQuestion]` entirely and fall back to the
		// all-fields layout, with a pager reading something like "1.5 of 2".
		const shown = activity({ inputMode: "form", schema: claudeQuestions });
		writeElicitationDraft("conversation-1", "request-1", { values: { question_0: "Native" }, activeQuestion: 0.5 });

		render(<ElicitationDock activity={shown} conversationId="conversation-1" onResolve={vi.fn()} />);

		expect(screen.getByRole("group", { name: /Approach/ })).toBeInTheDocument();
		expect(screen.queryByRole("group", { name: /Language/ })).not.toBeInTheDocument();
	});

	it("keeps generic MCP forms in the all-fields layout", () => {
		render(
			<ElicitationDock
				activity={activity({
					inputMode: "form",
					schema: {
						type: "object",
						properties: {
							name: { type: "string", title: "Name" },
							team: { type: "string", title: "Team" },
						},
					},
				})}
				onResolve={vi.fn()}
			/>,
		);

		expect(screen.getByLabelText("Name")).toBeInTheDocument();
		expect(screen.getByLabelText("Team")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Next" })).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Continue" })).toBeInTheDocument();
	});

	it("keeps required fields actionable instead of sending an invalid form", async () => {
		const user = userEvent.setup();
		const onResolve = vi.fn();
		render(
			<ElicitationDock
				activity={activity({
					inputMode: "form",
					schema: {
						type: "object",
						required: ["name"],
						properties: { name: { type: "string", title: "Name" } },
					},
				})}
				onResolve={onResolve}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Continue" }));
		expect(screen.getByText("This field is required.")).toBeInTheDocument();
		expect(onResolve).not.toHaveBeenCalled();
	});

	it("gives an invalid boolean the error node its aria-describedby names", async () => {
		const user = userEvent.setup();
		const onResolve = vi.fn();
		render(
			<ElicitationDock
				activity={activity({
					inputMode: "form",
					schema: {
						type: "object",
						required: ["diagnostics"],
						properties: { diagnostics: { type: "boolean", title: "Share diagnostics" } },
					},
				})}
				onResolve={onResolve}
			/>,
		);

		const checkbox = screen.getByRole("checkbox", { name: /Share diagnostics/ });
		expect(checkbox).not.toHaveAttribute("aria-describedby");

		await user.click(screen.getByRole("button", { name: "Continue" }));
		expect(onResolve).not.toHaveBeenCalled();

		// A description that points at nothing reads as an unlabelled error to a
		// screen reader, so the target has to exist and carry the wording.
		const describedBy = checkbox.getAttribute("aria-describedby") ?? "";
		expect(describedBy).not.toBe("");
		expect(document.getElementById(describedBy)).toHaveTextContent("This field is required.");
	});

	it("names the Other row with a visible label rather than a placeholder", () => {
		render(
			<ElicitationDock
				activity={activity({ inputMode: "form", schema: claudeQuestions })}
				onResolve={vi.fn()}
			/>,
		);

		// DESIGN.md §9: a placeholder is an example, never the only name a field has.
		const other = screen.getByLabelText("Other approach");
		expect(other).not.toHaveAttribute("placeholder");
		expect(screen.getByText("Other approach")).toBeVisible();
	});

	it("opens an external URL only after the user explicitly consents", async () => {
		const user = userEvent.setup();
		const openExternal = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		const onResolve = vi.fn().mockResolvedValue(undefined);
		render(
			<ElicitationDock
				activity={activity({ inputMode: "url", url: "https://console.anthropic.com/oauth", message: "Sign in" })}
				onResolve={onResolve}
			/>,
		);
		expect(openExternal).not.toHaveBeenCalled();
		expect(screen.getByText("https://console.anthropic.com/oauth")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Open console.anthropic.com" }));
		expect(openExternal).toHaveBeenCalledWith("https://console.anthropic.com/oauth");
		expect(onResolve).toHaveBeenCalledWith("request-1", "accept", undefined);
	});

	it("refuses unsafe URL schemes", () => {
		render(
			<ElicitationDock
				activity={activity({ inputMode: "url", url: "file:///Users/alice/.ssh/id_rsa" })}
				onResolve={vi.fn()}
			/>,
		);
		expect(screen.getByRole("alert")).toHaveTextContent(/unsafe or invalid URL/i);
		expect(screen.getByRole("button", { name: "Open link" })).toBeDisabled();
	});
});
