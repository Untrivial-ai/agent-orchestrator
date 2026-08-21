import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChatComposer } from "./ChatComposer";
import { ChatWorkspace } from "./ChatWorkspace";
import { chatFixture } from "../../lib/chat-fixture";

const png = (name = "shot.png") =>
	new File([new Uint8Array([137, 80, 78, 71])], name, { type: "image/png" });

afterEach(() => vi.unstubAllGlobals());

// Steering sends guidance INTO the running turn instead of queueing behind it. The
// thing these tests protect is that the choice is legible: Enter changing meaning
// silently would be worse than the queueing it replaces.

describe("ChatComposer steering", () => {
	function composer(props: Partial<Parameters<typeof ChatComposer>[0]> = {}) {
		return render(
			<ChatComposer onSend={vi.fn()} willQueue onSteer={vi.fn()} canSteer {...props} />,
		);
	}

	it("names both destinations while a turn is running", () => {
		composer();
		expect(screen.getByRole("button", { name: "Steer this turn" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Queue for next" })).toBeInTheDocument();
	});

	it("defaults Enter to the durable queue path used by ao send", () => {
		composer();
		expect(screen.getByRole("button", { name: "Queue for next" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.queryByText(/Enter to/)).not.toBeInTheDocument();
	});

	it("queues by default while a turn is running", async () => {
		const onSteer = vi.fn().mockResolvedValue(undefined);
		const onSend = vi.fn();
		composer({ onSteer, onSend });

		await userEvent.type(screen.getByRole("combobox"), "use the unit tests only{Enter}");
		expect(onSend).toHaveBeenCalledWith("use the unit tests only");
		expect(onSteer).not.toHaveBeenCalled();
	});

	it("steers once the user explicitly picks that", async () => {
		const onSteer = vi.fn().mockResolvedValue(undefined);
		const onSend = vi.fn();
		composer({ onSteer, onSend });

		await userEvent.click(screen.getByRole("button", { name: "Steer this turn" }));
		expect(screen.getByRole("button", { name: "Steer this turn" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.getByRole("button", { name: "Steer the running turn" })).toBeInTheDocument();
		expect(screen.queryByText(/Enter to/)).not.toBeInTheDocument();

		await userEvent.type(screen.getByRole("combobox"), "and then ship it{Enter}");
		expect(onSteer).toHaveBeenCalledWith("and then ship it");
		expect(onSend).not.toHaveBeenCalled();
	});

	it("marks the armed destination for assistive tech", async () => {
		composer();
		expect(screen.getByRole("button", { name: "Queue for next" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		await userEvent.click(screen.getByRole("button", { name: "Steer this turn" }));
		expect(screen.getByRole("button", { name: "Steer this turn" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
	});

	it("clears the composer only once the provider has taken the guidance", async () => {
		let settle = () => {};
		const onSteer = vi.fn(
			() =>
				new Promise<void>((resolve) => {
					settle = resolve;
				}),
		);
		composer({ onSteer });

		const field = screen.getByRole("combobox");
		await userEvent.click(screen.getByRole("button", { name: "Steer this turn" }));
		await userEvent.type(field, "stop after the tests{Enter}");
		// Still in the box: the turn is already running, so a refusal is a real
		// possibility and clearing early would lose what the user typed.
		expect(field).toHaveValue("stop after the tests");
		settle();
	});

	it("keeps focus on a refused steer so Enter can retry the retained draft", async () => {
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
		await userEvent.click(screen.getByRole("button", { name: "Steer this turn" }));
		await userEvent.type(field, "actually, skip it");
		fireEvent.keyDown(field, { key: "Enter" });
		await waitFor(() => expect(field).toBeDisabled());
		// Chromium blurs a focused textarea when it becomes disabled. Model that
		// browser behavior explicitly because jsdom leaves focus in place.
		const previousTabIndex = document.body.getAttribute("tabindex");
		try {
			document.body.tabIndex = -1;
			document.body.focus();
			expect(field).not.toHaveFocus();
			await act(async () => rejectSteer(new Error("not steerable")));
			await waitFor(() => expect(field).toBeEnabled());
			expect(field).toHaveValue("actually, skip it");
			expect(screen.getByRole("button", { name: "Queue for next" })).toHaveAttribute(
				"aria-pressed",
				"true",
			);
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

		await userEvent.click(screen.getByRole("button", { name: "Steer this turn" }));

		const field = screen.getByRole("combobox");
		await userEvent.click(field);
		fireEvent.paste(field, { clipboardData: { files: [png()], items: [] } });
		await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(1));

		expect(screen.getByRole("button", { name: "Steer the running turn" })).toBeEnabled();
		await userEvent.keyboard("{Enter}");
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

		fireEvent.click(screen.getByRole("button", { name: "Steer this turn" }));
		const field = screen.getByRole("combobox");
		fireEvent.change(field, { target: { value: "inspect this" } });
		fireEvent.paste(field, { clipboardData: { files: [png("slow.png")], items: [] } });
		act(() => {
			fireEvent.keyDown(field, { key: "Enter" });
			fireEvent.keyDown(field, { key: "Enter", repeat: true });
		});

		expect(stage).not.toHaveBeenCalled();
		expect(onSteer).not.toHaveBeenCalled();
		expect(field).toHaveValue("inspect this");
		expect(field).toBeDisabled();
		expect(screen.getByRole("button", { name: "Steer the running turn" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Steer this turn" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Queue for next" })).toBeDisabled();
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

	// Claude answers CHAT_STEER_UNSUPPORTED. A control that only ever fails is worse
	// than none, so the surface withdraws it rather than disabling it.
	it("offers nothing when the harness cannot steer", () => {
		composer({ onSteer: undefined, canSteer: false });
		expect(screen.queryByRole("button", { name: "Steer this turn" })).not.toBeInTheDocument();
		expect(screen.queryByText(/Enter to/)).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Send message" })).toBeInTheDocument();
	});

	it("offers nothing when no turn is in flight to steer", () => {
		composer({ canSteer: false, willQueue: false });
		expect(screen.queryByRole("button", { name: "Steer this turn" })).not.toBeInTheDocument();
		expect(screen.queryByText(/Enter to/)).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Send message" })).toBeInTheDocument();
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

	it("docks queued messages above the composer and steers the selected turn", async () => {
		const onPromoteQueuedTurn = vi.fn().mockResolvedValue(undefined);
		render(
			<ChatWorkspace
				snapshot={withQueuedMessages()}
				onSteer={vi.fn()}
				onPromoteQueuedTurn={onPromoteQueuedTurn}
			/>,
		);
		const dock = screen.getByTestId("queued-message-dock");
		expect(within(dock).getByText("first queued")).toBeVisible();
		expect(within(dock).getByText("second queued")).toBeVisible();
		expect(dock).toHaveClass("rounded-t-[10px]");
		expect(dock.nextElementSibling).toHaveClass("rounded-t-none");
		const actions = within(dock).getAllByRole("button", { name: "Steer" });
		expect(actions).toHaveLength(2);
		await userEvent.click(actions[1]!);
		expect(onPromoteQueuedTurn).toHaveBeenCalledWith("queued-2");
		expect(screen.queryByRole("button", { name: "Steer now" })).not.toBeInTheDocument();
	});

	it("keeps queued messages docked after the conversation branches", () => {
		render(
			<ChatWorkspace
				snapshot={{ ...withQueuedMessages(), branchedFromEarlierMessage: true }}
				onSteer={vi.fn()}
				onPromoteQueuedTurn={vi.fn().mockResolvedValue(undefined)}
			/>,
		);

		const dock = screen.getByTestId("queued-message-dock");
		expect(within(dock).getByText("first queued")).toBeVisible();
		expect(within(dock).getByText("second queued")).toBeVisible();
	});

	it("scopes pending and failure feedback to the selected queued message", async () => {
		let releaseFirst = () => {};
		const onPromoteQueuedTurn = vi
			.fn<(turnId: string) => Promise<void>>()
			.mockImplementationOnce(
				() =>
					new Promise<void>((resolve) => {
						releaseFirst = resolve;
					}),
			)
			.mockRejectedValueOnce(new Error("provider refused steer"));
		render(
			<ChatWorkspace
				snapshot={withQueuedMessages()}
				onSteer={vi.fn()}
				onPromoteQueuedTurn={onPromoteQueuedTurn}
			/>,
		);

		const first = screen.getByTestId("queued-message-queued-1");
		const second = screen.getByTestId("queued-message-queued-2");
		await userEvent.click(within(first).getByRole("button", { name: "Steer" }));
		expect(within(first).getByRole("button", { name: "Steering…" })).toBeDisabled();
		expect(within(second).getByRole("button", { name: "Steer" })).toBeDisabled();
		releaseFirst();
		await screen.findAllByRole("button", { name: "Steer" });

		await userEvent.click(within(second).getByRole("button", { name: "Steer" }));
		expect(await within(second).findByRole("status")).toHaveTextContent(
			"Could not steer this message. It remains queued.",
		);
		expect(within(first).queryByRole("status")).not.toBeInTheDocument();
		expect(within(first).getByText("first queued")).toBeVisible();
		expect(within(second).getByText("second queued")).toBeVisible();
	});

	it("offers steering only into a turn the provider is actually running", () => {
		render(<ChatWorkspace snapshot={chatFixture} onSteer={vi.fn()} />);
		// The live fixture is mid-turn.
		expect(screen.getByRole("button", { name: "Steer this turn" })).toBeInTheDocument();
	});

	it("does not offer steering on a settled conversation", () => {
		render(
			<ChatWorkspace
				snapshot={{ ...chatFixture, turns: chatFixture.turns.map((t) => ({ ...t, state: "completed" as const })) }}
				onSteer={vi.fn()}
			/>,
		);
		expect(screen.queryByRole("button", { name: "Steer this turn" })).not.toBeInTheDocument();
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

	it("does not duplicate a staged steer image that also has native content", () => {
		const snapshot = {
			...chatFixture,
			items: chatFixture.items.map((item) =>
				item.kind === "activity" && item.id === "a-steer-1"
					? {
							...item,
							detail: {
								...item.detail,
								text: "inspect this\n\nAttached files (read these files in the workspace):\n- .ao/attachments/attachment-steer123.png",
								content: [{ type: "image", data: "aGVsbG8=", mimeType: "image/png" }],
							},
						}
					: item,
			),
		};
		render(<ChatWorkspace snapshot={snapshot} />);

		expect(screen.getAllByRole("img", { name: "attachment-steer123.png" })).toHaveLength(1);
		expect(screen.queryByRole("img", { name: "Steered attachment 1" })).not.toBeInTheDocument();
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
