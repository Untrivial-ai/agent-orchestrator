import { act, fireEvent, render as rtlRender, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChatComposer } from "./ChatComposer";
import { ChatWorkspace } from "./ChatWorkspace";
import { QueuedMessageDock } from "./QueuedMessageDock";
import { chatFixture } from "../../lib/chat-fixture";
import { typeInLexicalEditor } from "../../test/lexical";
import { TooltipProvider } from "../ui/tooltip";

function render(ui: ReactElement) {
	const result = rtlRender(<TooltipProvider>{ui}</TooltipProvider>);
	return {
		...result,
		rerender: (nextUi: ReactElement) => result.rerender(<TooltipProvider>{nextUi}</TooltipProvider>),
	};
}

const png = (name = "shot.png") =>
	new File([new Uint8Array([137, 80, 78, 71])], name, { type: "image/png" });

afterEach(() => vi.unstubAllGlobals());

// Steering sends guidance INTO the running turn instead of queueing behind it. The
// thing these tests protect is that the choice is legible: Enter changing meaning
// silently would be worse than the queueing it replaces.

describe("ChatComposer steering", () => {
	const queuedMessage = (turnId: string, text: string) => ({
		turnId,
		message: {
			kind: "message" as const,
			id: `message-${turnId}`,
			turnId,
			sequence: 1,
			revision: 0,
			role: "user" as const,
			origin: "human" as const,
			text,
			streaming: false,
			createdAt: "2026-08-11T10:01:00Z",
		},
	});

	function composer(props: Partial<Parameters<typeof ChatComposer>[0]> = {}) {
		return render(
			<ChatComposer onSend={vi.fn()} willQueue onSteer={vi.fn()} canSteer {...props} />,
		);
	}

	it("does not show delivery indicators in the composer while a turn is running", () => {
		composer();
		expect(screen.queryByRole("group", { name: /where this message goes/i })).not.toBeInTheDocument();
		expect(screen.queryByText("Queue")).not.toBeInTheDocument();
		expect(screen.queryByText("Steer")).not.toBeInTheDocument();
	});

	it("queues by default while a turn is running", async () => {
		const onSteer = vi.fn().mockResolvedValue(undefined);
		const onSend = vi.fn();
		composer({ onSteer, onSend });

		await typeInLexicalEditor(screen.getByRole("combobox"), "use the unit tests only");
		await userEvent.keyboard("{Enter}");
		expect(onSend).toHaveBeenCalledWith("use the unit tests only");
		expect(onSteer).not.toHaveBeenCalled();
	});

	it("keeps Cmd/Ctrl+Enter steering available without advertising it", async () => {
		const onSteer = vi.fn().mockResolvedValue(undefined);
		composer({ onSteer });

		await typeInLexicalEditor(screen.getByRole("combobox"), "steer this draft");
		await userEvent.keyboard("{Control>}{Enter}{/Control}");

		expect(onSteer).toHaveBeenCalledWith("steer this draft");
		expect(screen.queryByText(/steer/i)).not.toBeInTheDocument();
	});

	it("steers the next queued message on empty Enter and removes it immediately", async () => {
		const onPromoteQueuedTurn = vi.fn().mockResolvedValue(undefined);
		render(
			<ChatComposer
				onSend={vi.fn()}
				willQueue
				onSteer={vi.fn()}
				canSteer
				queuedDock={
					<QueuedMessageDock
						messages={[queuedMessage("queued-1", "first"), queuedMessage("queued-2", "next")]}
						canSteer
						onPromoteQueuedTurn={onPromoteQueuedTurn}
					/>
				}
			/>,
		);

		await userEvent.click(screen.getByRole("combobox"));
		await userEvent.keyboard("{Enter}");

		await waitFor(() => expect(onPromoteQueuedTurn).toHaveBeenCalledWith("queued-1"));
		expect(screen.queryByTestId("queued-message-queued-1")).not.toBeInTheDocument();
	});

	it("drains every queued steer request that arrives while a promotion is pending", async () => {
		let resolveFirstPromotion: (() => void) | undefined;
		const firstPromotion = new Promise<void>((resolve) => {
			resolveFirstPromotion = resolve;
		});
		const onPromoteQueuedTurn = vi
			.fn()
			.mockImplementationOnce(() => firstPromotion)
			.mockResolvedValueOnce(undefined);
		const messages = [queuedMessage("queued-1", "first"), queuedMessage("queued-2", "next")];
		const { rerender } = render(
			<QueuedMessageDock
				messages={messages}
				canSteer
				canSteerNext
				steerNextRequest={0}
				onPromoteQueuedTurn={onPromoteQueuedTurn}
			/>,
		);

		rerender(
			<QueuedMessageDock
				messages={messages}
				canSteer
				canSteerNext
				steerNextRequest={2}
				onPromoteQueuedTurn={onPromoteQueuedTurn}
			/>,
		);

		await waitFor(() => expect(onPromoteQueuedTurn).toHaveBeenCalledWith("queued-1"));
		resolveFirstPromotion?.();
		await waitFor(() => expect(onPromoteQueuedTurn).toHaveBeenNthCalledWith(2, "queued-2"));
	});

	it("keeps focus on a refused steer so Enter can queue the retained draft", async () => {
		let rejectSteer!: (reason?: unknown) => void;
		const onSteer = vi.fn(
			() =>
				new Promise<void>((_resolve, reject) => {
					rejectSteer = reject;
				}),
		);
		const onSend = vi.fn().mockResolvedValue(undefined);
		composer({ onSteer, onSend });

		const field = screen.getByRole("combobox");
		await typeInLexicalEditor(field, "actually, skip it");
		await userEvent.keyboard("{Control>}{Enter}{/Control}");
		await waitFor(() => expect(field).toHaveAttribute("aria-disabled", "true"));

		// Chromium can move focus to the document when the editor becomes non-editable.
		// Model that explicitly because jsdom leaves focus in place.
		const previousTabIndex = document.body.getAttribute("tabindex");
		try {
			document.body.tabIndex = -1;
			document.body.focus();
			expect(field).not.toHaveFocus();
			await act(async () => rejectSteer(new Error("not steerable")));
			await waitFor(() => expect(field).not.toHaveAttribute("aria-disabled"));
			expect(field).toHaveTextContent("actually, skip it");
			expect(field).toHaveFocus();

			await userEvent.keyboard("{Enter}");
			await waitFor(() => expect(onSend).toHaveBeenCalledWith("actually, skip it"));
		} finally {
			if (previousTabIndex === null) document.body.removeAttribute("tabindex");
			else document.body.setAttribute("tabindex", previousTabIndex);
		}
	});

	it("stages attachment-only drafts and includes them in steer", async () => {
		const onSteer = vi.fn().mockResolvedValue(undefined);
		const onSend = vi.fn();
		const stage = vi.fn().mockResolvedValue([".ao/attachments/shot.png"]);
		composer({ onSteer, onSend, onStageAttachments: stage, nativeImages: true });

		const field = screen.getByRole("combobox");
		await userEvent.click(field);
		fireEvent.paste(field, { clipboardData: { files: [png()], items: [] } });
		await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(1));

		await userEvent.keyboard("{Control>}{Enter}{/Control}");
		await waitFor(() => expect(stage).toHaveBeenCalledOnce());
		expect(onSteer).toHaveBeenCalledWith(
			"Attached files (read these files in the workspace):\n- .ao/attachments/shot.png",
			[{ mimeType: "image/png", data: expect.any(String) }],
		);
		expect(onSend).not.toHaveBeenCalled();
		await waitFor(() => expect(screen.queryAllByRole("listitem")).toHaveLength(0));
	});

	it("waits for a pasted image read and ignores repeated Enter while steering", async () => {
		let finishRead!: () => void;
		class SlowFileReader {
			error: Error | null = null;
			result: string | ArrayBuffer | null = null;
			onerror: (() => void) | null = null;
			onload: (() => void) | null = null;

			readAsDataURL(file: File) {
				finishRead = () => {
					this.result = `data:${file.type};base64,iVBORw==`;
					this.onload?.();
				};
			}
		}
		vi.stubGlobal("FileReader", SlowFileReader);

		let finishSteer!: () => void;
		const steerPromise = new Promise<void>((resolve) => {
			finishSteer = resolve;
		});
		const onSteer = vi.fn(() => steerPromise);
		const stage = vi.fn().mockResolvedValue([".ao/attachments/slow.png"]);
		composer({ onSteer, onStageAttachments: stage, nativeImages: true });

		const field = screen.getByRole("combobox");
		await typeInLexicalEditor(field, "inspect this");
		fireEvent.paste(field, { clipboardData: { files: [png("slow.png")], items: [] } });
		act(() => {
			fireEvent.keyDown(field, { key: "Enter", ctrlKey: true });
			fireEvent.keyDown(field, { key: "Enter", ctrlKey: true, repeat: true });
		});

		expect(stage).not.toHaveBeenCalled();
		expect(onSteer).not.toHaveBeenCalled();
		expect(field).toHaveTextContent("inspect this");
		expect(field).toHaveAttribute("aria-disabled", "true");
		expect(screen.getByRole("button", { name: "Send message" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Attach a file" })).toBeDisabled();

		await act(async () => finishRead());
		await waitFor(() => expect(stage).toHaveBeenCalledOnce());
		await waitFor(() =>
			expect(onSteer).toHaveBeenCalledWith(
				"inspect this\n\nAttached files (read these files in the workspace):\n- .ao/attachments/slow.png",
				[{ mimeType: "image/png", data: "iVBORw==" }],
			),
		);
		expect(onSteer).toHaveBeenCalledOnce();
		await act(async () => finishSteer());
	});

	it("reports the daemon's refusal without a second message of its own", () => {
		composer({ steerRefusal: "A compaction turn is running. Try again once it finishes." });
		expect(screen.getByRole("status")).toHaveTextContent(/compaction turn is running/);
	});

	it("hides delivery indicators in the composer when the harness cannot steer", () => {
		composer({ onSteer: undefined, canSteer: false });
		expect(screen.queryByText("Steer")).not.toBeInTheDocument();
	});

	it("hides delivery indicators in the composer when no turn is in flight", () => {
		composer({ canSteer: false, willQueue: false });
		expect(screen.queryByText("Steer")).not.toBeInTheDocument();
	});
});

describe("ChatWorkspace steering", () => {
	function withQueuedMessages() {
		return {
			...chatFixture,
			turns: [
				...chatFixture.turns,
				{ id: "queued-1", state: "queued" as const, requestedAt: "2026-08-11T10:01:00Z" },
				{ id: "queued-2", state: "queued" as const, requestedAt: "2026-08-11T10:02:00Z" },
			],
			items: [
				...chatFixture.items,
				{
					kind: "message" as const,
					id: "queued-message-1",
					turnId: "queued-1",
					sequence: 100,
					revision: 0,
					role: "user" as const,
					origin: "human" as const,
					text: "first queued",
					streaming: false,
					createdAt: "2026-08-11T10:01:00Z",
				},
				{
					kind: "message" as const,
					id: "queued-message-2",
					turnId: "queued-2",
					sequence: 101,
					revision: 0,
					role: "user" as const,
					origin: "human" as const,
					text: "second queued",
					streaming: false,
					createdAt: "2026-08-11T10:02:00Z",
				},
			],
		};
	}

	it("docks queued messages above the composer", () => {
		render(
			<ChatWorkspace
				snapshot={withQueuedMessages()}
				onSteer={vi.fn()}
			/>,
		);
		const dock = screen.getByTestId("queued-message-dock");
		expect(within(dock).getByText("2 Queued Messages")).toBeVisible();
		expect(within(dock).getByText("first queued")).toBeVisible();
		expect(within(dock).getByText("second queued")).toBeVisible();
		expect(screen.queryByText("Queued · sends when the agent finishes")).not.toBeInTheDocument();
	});

	it("shows drag handles when queue reordering is available", () => {
		render(
			<QueuedMessageDock
				messages={[
					{
						turnId: "queued-1",
						message: {
							kind: "message",
							id: "queued-message-1",
							turnId: "queued-1",
							sequence: 100,
							revision: 0,
							role: "user",
							origin: "human",
							text: "first queued",
							streaming: false,
							createdAt: "2026-08-11T10:01:00Z",
						},
					},
					{
						turnId: "queued-2",
						message: {
							kind: "message",
							id: "queued-message-2",
							turnId: "queued-2",
							sequence: 101,
							revision: 0,
							role: "user",
							origin: "human",
							text: "second queued",
							streaming: false,
							createdAt: "2026-08-11T10:02:00Z",
						},
					},
				]}
				onReorderQueuedTurns={vi.fn().mockResolvedValue(undefined)}
			/>,
		);
		expect(screen.getAllByRole("button", { name: "Drag to reorder queued message" })).toHaveLength(2);
	});

	it("steers, edits, and deletes queued messages from the dock", async () => {
		const onPromoteQueuedTurn = vi.fn().mockResolvedValue(undefined);
		const onBeginQueuedEdit = vi.fn();
		const onCancelQueuedTurn = vi.fn().mockResolvedValue(undefined);
		render(
			<QueuedMessageDock
				messages={[
					{
						turnId: "queued-1",
						message: {
							kind: "message",
							id: "queued-message-1",
							turnId: "queued-1",
							sequence: 100,
							revision: 0,
							role: "user",
							origin: "human",
							text: "first queued",
							streaming: false,
							createdAt: "2026-08-11T10:01:00Z",
						},
					},
					{
						turnId: "queued-2",
						message: {
							kind: "message",
							id: "queued-message-2",
							turnId: "queued-2",
							sequence: 101,
							revision: 0,
							role: "user",
							origin: "human",
							text: "second queued",
							streaming: false,
							createdAt: "2026-08-11T10:02:00Z",
						},
					},
				]}
				canSteer
				onPromoteQueuedTurn={onPromoteQueuedTurn}
				onBeginQueuedEdit={onBeginQueuedEdit}
				onCancelQueuedTurn={onCancelQueuedTurn}
			/>,
		);

		expect(screen.queryByText("Queue")).not.toBeInTheDocument();
		expect(
			within(screen.getByTestId("queued-message-queued-1")).getAllByRole("button", {
				name: "Steer this queued message into the running turn",
			}),
		).toHaveLength(1);

		await userEvent.click(
			within(screen.getByTestId("queued-message-queued-2")).getByRole("button", {
				name: "Steer this queued message into the running turn",
			}),
		);
		expect(onPromoteQueuedTurn).toHaveBeenCalledWith("queued-2");
		expect(screen.queryByTestId("queued-message-queued-2")).not.toBeInTheDocument();

		await userEvent.click(within(screen.getByTestId("queued-message-queued-1")).getByRole("button", { name: "Edit queued message" }));
		expect(onBeginQueuedEdit).toHaveBeenCalledWith("queued-1", "first queued");

		await userEvent.click(within(screen.getByTestId("queued-message-queued-1")).getByRole("button", { name: "Delete queued message" }));
		expect(onCancelQueuedTurn).toHaveBeenCalledWith("queued-1");
	});

	it("keeps the next queued message visible when the dock is collapsed", async () => {
		render(
			<QueuedMessageDock
				messages={[
					{
						turnId: "queued-1",
						message: {
							kind: "message",
							id: "queued-message-1",
							turnId: "queued-1",
							sequence: 100,
							revision: 0,
							role: "user",
							origin: "human",
							text: "first queued",
							streaming: false,
							createdAt: "2026-08-11T10:01:00Z",
						},
					},
					{
						turnId: "queued-2",
						message: {
							kind: "message",
							id: "queued-message-2",
							turnId: "queued-2",
							sequence: 101,
							revision: 0,
							role: "user",
							origin: "human",
							text: "second queued",
							streaming: false,
							createdAt: "2026-08-11T10:02:00Z",
						},
					},
				]}
			/>,
		);

		const dock = screen.getByTestId("queued-message-dock");
		expect(within(dock).getByText("first queued")).toBeVisible();
		expect(within(dock).getByText("second queued")).toBeVisible();

		await userEvent.click(screen.getByRole("button", { expanded: true }));
		expect(screen.getByRole("button", { expanded: false })).toBeInTheDocument();
		expect(within(dock).getByText("first queued")).toBeVisible();
		const hiddenRow = within(dock).getByTestId("queued-message-queued-2");
		expect(hiddenRow).toHaveAttribute("aria-hidden", "true");
		expect(hiddenRow).toHaveAttribute("inert");
	});

	it("hides a cancelled queued message from the timeline", () => {
		const base = withQueuedMessages();
		const snapshot = {
			...base,
			turns: base.turns.map((turn) =>
				turn.id === "queued-1"
					? {
							...turn,
							state: "cancelled" as const,
							completedAt: "2026-08-11T10:03:00Z",
						}
					: turn,
			),
			items: base.items.filter((item) => item.kind !== "message" || item.turnId !== "queued-1"),
		};
		render(<ChatWorkspace snapshot={snapshot} onSteer={vi.fn()} />);

		expect(screen.queryByText("first queued")).not.toBeInTheDocument();
		expect(within(screen.getByTestId("queued-message-dock")).getByText("second queued")).toBeVisible();
	});

	it("clears a queued edit when that message is deleted from the dock", async () => {
		const onCancelQueuedTurn = vi.fn().mockResolvedValue(undefined);
		render(
			<ChatWorkspace
				snapshot={withQueuedMessages()}
				onSteer={vi.fn()}
				onEditQueuedTurn={vi.fn()}
				onCancelQueuedTurn={onCancelQueuedTurn}
			/>,
		);

		await userEvent.click(within(screen.getByTestId("queued-message-queued-1")).getByRole("button", { name: "Edit queued message" }));
		await waitFor(() => expect(screen.getByText(/editing/i)).toBeInTheDocument());

		await userEvent.click(within(screen.getByTestId("queued-message-queued-1")).getByRole("button", { name: "Delete queued message" }));
		await waitFor(() => expect(onCancelQueuedTurn).toHaveBeenCalledWith("queued-1"));
		await waitFor(() => expect(screen.queryByText(/editing/i)).not.toBeInTheDocument());
	});

	it("shows the queued dock while messages are still queued between turns", () => {
		const snapshot = {
			...withQueuedMessages(),
			turns: withQueuedMessages().turns.map((turn) =>
				turn.state === "running" ? { ...turn, state: "completed" as const } : turn,
			),
		};
		render(<ChatWorkspace snapshot={snapshot} onSteer={vi.fn()} />);
		expect(screen.getByTestId("queued-message-dock")).toBeInTheDocument();
		expect(within(screen.getByTestId("queued-message-dock")).getByText("first queued")).toBeVisible();
	});

	it("keeps queued messages docked after the conversation branches", () => {
		render(
			<ChatWorkspace
				snapshot={{ ...withQueuedMessages(), branchedFromEarlierMessage: true }}
				onSteer={vi.fn()}
			/>,
		);

		const dock = screen.getByTestId("queued-message-dock");
		expect(within(dock).getByText("first queued")).toBeVisible();
		expect(within(dock).getByText("second queued")).toBeVisible();
	});

	it("shows the standard steer action on the next row and hover steer on later rows", () => {
		render(
			<ChatWorkspace
				snapshot={withQueuedMessages()}
				onSteer={vi.fn()}
				onPromoteQueuedTurn={vi.fn()}
			/>,
		);

		const dock = screen.getByTestId("queued-message-dock");
		expect(within(dock).queryByText("Queue")).not.toBeInTheDocument();
		expect(
			within(screen.getByTestId("queued-message-queued-1")).getByRole("button", {
				name: "Steer this queued message into the running turn",
			}),
		).toBeInTheDocument();
		expect(
			within(screen.getByTestId("queued-message-queued-2")).getByRole("button", {
				name: "Steer this queued message into the running turn",
			}),
		).toBeInTheDocument();
	});

	it("does not show delivery indicators in the composer for a running turn without a pending approval", () => {
		const snapshot = {
			...chatFixture,
			items: chatFixture.items.filter(
				(item) =>
					!(item.kind === "activity" && item.activityKind === "approval" && item.status === "pending"),
			),
		};
		render(<ChatWorkspace snapshot={snapshot} onSteer={vi.fn()} />);
		expect(screen.queryByText("Queue")).not.toBeInTheDocument();
		expect(screen.queryByText("Steer")).not.toBeInTheDocument();
	});

	it("does not show delivery indicator on a settled conversation", () => {
		render(
			<ChatWorkspace
				snapshot={{ ...chatFixture, turns: chatFixture.turns.map((t) => ({ ...t, state: "completed" as const })) }}
				onSteer={vi.fn()}
			/>,
		);
		expect(screen.queryByText("Steer")).not.toBeInTheDocument();
		expect(screen.queryByTestId("queued-message-dock")).not.toBeInTheDocument();
	});

	// A queued turn has not reached the provider, so there is nothing to steer.
	it("does not offer steering into a turn that is only queued", () => {
		render(
			<ChatWorkspace
				snapshot={{
					...chatFixture,
					turns: chatFixture.turns.map((t) =>
						t.state === "running" ? { ...t, state: "queued" as const } : t,
					),
				}}
				onSteer={vi.fn()}
			/>,
		);
		expect(screen.queryByRole("button", { name: "Steer this turn" })).not.toBeInTheDocument();
	});

	it("renders a landed steer as the user's own words", () => {
		render(<ChatWorkspace snapshot={chatFixture} />);
		expect(screen.getByText(/Steered into the running turn/)).toBeInTheDocument();
	});

	it("renders a staged image reference on a landed steer in chat history", () => {
		const snapshot = {
			...chatFixture,
			items: chatFixture.items.map((item) =>
				item.kind === "activity" && item.id === "a-steer-1"
					? {
							...item,
							detail: {
								...item.detail,
								text: "inspect this\n\nAttached files (read these files in the workspace):\n- .ao/attachments/attachment-steer123.png",
								content: undefined,
							},
						}
					: item,
			),
		};
		render(<ChatWorkspace snapshot={snapshot} />);

		const image = screen.getByRole("img", { name: "attachment-steer123.png" });
		expect(image).toBeInTheDocument();
		expect(image).toHaveAttribute(
			"src",
			expect.stringContaining(
				"/api/v1/sessions/ao-14/preview/files/.ao/attachments/attachment-steer123.png",
			),
		);
		expect(screen.getByText("inspect this")).toBeInTheDocument();
		expect(screen.queryByText(/Attached files \(read these files/)).not.toBeInTheDocument();
	});

	it("suppresses only native images matched by staged steer images", () => {
		const snapshot = {
			...chatFixture,
			items: chatFixture.items.map((item) =>
				item.kind === "activity" && item.id === "a-steer-1"
					? {
							...item,
							detail: {
								...item.detail,
								text: "inspect this\n\nAttached files (read these files in the workspace):\n- .ao/attachments/attachment-steer123.png",
								content: [
									{ type: "image", data: "Zmlyc3Q=", mimeType: "image/png" },
									{ type: "image", data: "c2Vjb25k", mimeType: "image/png" },
								],
							},
						}
					: item,
			),
		};
		render(<ChatWorkspace snapshot={snapshot} />);

		expect(screen.getAllByRole("img", { name: "attachment-steer123.png" })).toHaveLength(1);
		expect(screen.getByRole("img", { name: "Steered attachment 1" })).toHaveAttribute(
			"src",
			"data:image/png;base64,c2Vjb25k",
		);
	});

	it("renders every promoted steer content block on the running turn", () => {
		const snapshot = {
			...chatFixture,
			items: chatFixture.items.map((item) =>
				item.kind === "activity" && item.id === "a-steer-1"
					? {
							...item,
							detail: {
								...item.detail,
								content: [
									{ type: "image", data: "aGVsbG8=", mimeType: "image/png" },
									{ type: "resource_link", name: "reference.md", uri: "file:///reference.md" },
									{ type: "resource", name: "notes.md", uri: "file:///notes.md", text: "details" },
								],
							},
						}
					: item,
			),
		};
		render(<ChatWorkspace snapshot={snapshot} />);
		const image = screen.getByRole("img", { name: "Steered attachment 1" });
		expect(image).toHaveAttribute("src", "data:image/png;base64,aGVsbG8=");
		const attachments = screen.getByRole("list", { name: "Steered attachments" });
		expect(within(attachments).getByTitle("file:///reference.md")).toHaveTextContent("reference.md");
		expect(within(attachments).getByTitle("file:///notes.md")).toHaveTextContent("notes.md");
	});
});
