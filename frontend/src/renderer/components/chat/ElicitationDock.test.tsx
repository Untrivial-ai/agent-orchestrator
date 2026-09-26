import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { aoBridge } from "../../lib/bridge";
import {
	elicitationDraftKey,
	readElicitationDraft,
	resetElicitationDraftPruning,
	writeElicitationDraft,
} from "../../lib/elicitation-drafts";
import type { ConversationActivity } from "../../types/conversation";
import { ElicitationDock } from "./ElicitationDock";

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
