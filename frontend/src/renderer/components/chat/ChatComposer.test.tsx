import { act, fireEvent, render as rtlRender, screen, waitFor, within } from "@testing-library/react";
import type { ReactElement } from "react";
import userEvent from "@testing-library/user-event";
import { Profiler } from "react";
import { describe, expect, it, vi } from "vitest";
import { ChatComposer } from "./ChatComposer";
import { TooltipProvider } from "../ui/tooltip";
import type { ChatSkill } from "../../types/conversation";
import {
	lexicalEditorText,
	placeLexicalCaret,
	typeAndPressInLexicalEditor,
	typeInLexicalEditor,
} from "../../test/lexical";

// Every button in the composer relies on the shared styled Tooltip, which needs
// a TooltipProvider ancestor. `wrapper` survives `rerender`, so every render call
// in this file — direct or through renderComposer/renderSteerable — gets one.
function render(ui: ReactElement, options?: Parameters<typeof rtlRender>[1]) {
	return rtlRender(ui, { wrapper: TooltipProvider, ...options });
}

const SKILLS: ChatSkill[] = [
	{ name: "code-review", displayName: "code-review", description: "Review the diff", source: "user" },
	{ name: "review", displayName: "review", description: "Look it over", source: "repo" },
	{ name: "ship", displayName: "ship", description: "Open a PR", source: "user" },
];

const FILES = [
	"AGENTS.md",
	"backend/internal/ports/chat.go",
	"frontend/src/renderer/components/chat/ChatComposer.tsx",
];

function renderComposer(props: Partial<Parameters<typeof ChatComposer>[0]> = {}) {
	const onSend = vi.fn();
	render(<ChatComposer onSend={onSend} {...props} />);
	return { onSend, field: screen.getByLabelText("Message the agent") as HTMLElement };
}

function deferred<T>() {
	let resolve!: (value: T) => void;
	const promise = new Promise<T>((resolvePromise) => {
		resolve = resolvePromise;
	});
	return { promise, resolve };
}

async function typeInComposer(field: HTMLElement, text: string) {
	await typeInLexicalEditor(field, text);
}

function composerWireText(field: HTMLElement): string {
	return lexicalEditorText(field);
}

function clipboardData(files: File[]) {
	return {
		files,
		items: [],
		getData: () => "",
		setData: () => undefined,
	};
}

const png = (name = "shot.png") =>
	new File([new Uint8Array([137, 80, 78, 71])], name, { type: "image/png" });
const textFile = (name = "notes.txt") => new File(["hello"], name, { type: "text/plain" });

/* ---- the keyboard contract the composer already had ---------------------- */

describe("send keys", () => {
	it("focuses the message field when the chat composer opens", () => {
		const { field } = renderComposer({ autoFocusKey: "session-1" });
		expect(document.activeElement).toBe(field);
	});

	it("refocuses the message field when the active chat session changes", () => {
		const { rerender } = render(
			<>
				<button type="button">Outside</button>
				<ChatComposer onSend={vi.fn()} autoFocusKey="session-1" />
			</>,
		);
		const field = screen.getByLabelText("Message the agent");
		expect(document.activeElement).toBe(field);

		screen.getByRole("button", { name: "Outside" }).focus();
		expect(document.activeElement).not.toBe(field);

		rerender(
			<>
				<button type="button">Outside</button>
				<ChatComposer onSend={vi.fn()} autoFocusKey="session-2" />
			</>,
		);
		expect(document.activeElement).toBe(field);
	});

	it("refocuses the message field when returning to the chat window", () => {
		const { field } = renderComposer({ autoFocusKey: "session-1" });
		field.blur();
		expect(document.activeElement).not.toBe(field);

		act(() => {
			window.dispatchEvent(new Event("focus"));
		});

		expect(document.activeElement).toBe(field);
	});

	it("does not focus the hidden or inactive chat composer", () => {
		const { field } = renderComposer({ autoFocus: false, autoFocusKey: "session-1" });
		expect(document.activeElement).not.toBe(field);
	});

	it("applies the natural-growth and seven-line scroll-cap styles", () => {
		const { field } = renderComposer();
		expect(field).toHaveClass(
			"chat-composer-scrollbar",
			"min-h-[4.5rem]",
			"max-h-40",
			"overflow-y-auto",
		);
	});

	it("separates secondary message tools from the primary send action", async () => {
		const user = userEvent.setup();
		render(
			<ChatComposer
				onSend={vi.fn()}
				onStageAttachments={vi.fn().mockResolvedValue([])}
				settings={<button type="button">Model</button>}
			/>,
		);

		const tools = screen.getByRole("group", { name: "Message tools" });
		expect(within(tools).getByRole("button", { name: "Attach a file" })).toBeInTheDocument();
		expect(within(tools).getByRole("button", { name: "Model" })).toBeInTheDocument();

		const actions = screen.getByRole("group", { name: "Send message controls" });
		// The destination Enter is armed with rides on the send control's tooltip
		// rather than as a line of prose beside it.
		const send = within(actions).getByRole("button", { name: "Send message" });
		await user.hover(send);
		expect(await screen.findByRole("tooltip")).toHaveTextContent("Enter to send");
	});


	it("keeps a taller resting field for the redesigned composer", () => {
		const { field } = renderComposer();
		expect(field).toHaveAttribute("contenteditable", "true");
		expect(field).toHaveClass("min-h-[4.5rem]");
	});

	it("uses a muted circular send control that lights up when armed", async () => {
		const { field } = renderComposer();
		const send = screen.getByRole("button", { name: "Send message" });
		expect(send).toHaveClass("rounded-full", "bg-primary", "text-primary-foreground");
		expect(send).toBeDisabled();

		await typeInComposer(field, "hello");
		expect(send).toBeEnabled();
		expect(send).toHaveClass("bg-foreground", "text-background");
	});

	it("keeps ordinary typing local after the draft becomes nonempty", async () => {
		const onRender = vi.fn();
		render(
			<Profiler id="composer" onRender={onRender}>
				<ChatComposer onSend={vi.fn()} />
			</Profiler>,
		);
		const field = screen.getByLabelText("Message the agent");

		await typeInComposer(field, "a");
		const commitsAfterFirstCharacter = onRender.mock.calls.length;
		await typeInComposer(field, "b");
		await typeInComposer(field, "c");

		expect(field.textContent).toBe("abc");
		expect(onRender).toHaveBeenCalledTimes(commitsAfterFirstCharacter);
	});

	it("turns the empty send action into Stop while the agent is working", async () => {
		const onInterrupt = vi.fn();
		const { field } = renderComposer({ willQueue: true, onInterrupt });

		const stop = screen.getByRole("button", { name: "Stop turn" });
		expect(screen.queryByRole("button", { name: "Send message" })).not.toBeInTheDocument();
		await userEvent.click(stop);
		expect(onInterrupt).toHaveBeenCalledOnce();

		await typeInComposer(field, "queue this next");
		expect(screen.queryByRole("button", { name: "Stop turn" })).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Send message" })).toBeEnabled();
	});

	it("sends on Enter", async () => {
		const { onSend, field } = renderComposer();
		await typeInComposer(field, "hello");
		expect(field).toHaveTextContent("hello");
		await userEvent.keyboard("{Enter}");
		expect(onSend).toHaveBeenCalledWith("hello");
	});

	it("joins rapid duplicate Enter submissions without showing a false retry error", async () => {
		const provider = deferred<void>();
		const onSend = vi
			.fn()
			.mockImplementationOnce(() => provider.promise)
			.mockRejectedValueOnce(new Error("A message is already being sent for this session."));
		render(<ChatComposer onSend={onSend} />);
		const field = screen.getByLabelText("Message the agent") as HTMLElement;
		await typeInComposer(field, "only send this once");

		fireEvent.keyDown(field, { key: "Enter" });
		fireEvent.keyDown(field, { key: "Enter" });

		await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
		provider.resolve();
		await act(async () => {
			await provider.promise;
		});
		await waitFor(() => expect(field).toHaveTextContent(""));
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
		expect(screen.queryByText(/draft.*kept|retry/i)).not.toBeInTheDocument();
	});

	it("makes a newline on Shift+Enter and does not send", async () => {
		const { onSend, field } = renderComposer();
		await typeInComposer(field, "one");
		await userEvent.keyboard("{Shift>}{Enter}{/Shift}");
		await typeInComposer(field, "two");
		expect(onSend).not.toHaveBeenCalled();
		expect(composerWireText(field)).toBe("one\ntwo");
	});

	it("refuses to send an empty message: there is no keystroke concept here", async () => {
		const { onSend, field } = renderComposer();
		await typeInComposer(field, "   ");
		await userEvent.keyboard("{Enter}");
		expect(onSend).not.toHaveBeenCalled();
	});

	it("clears the field after a send", async () => {
		const { field } = renderComposer();
		await typeInComposer(field, "hello");
		await userEvent.keyboard("{Enter}");
		expect(field.textContent).toBe("");
	});

	it("does not restore a sent draft through undo", async () => {
		const { field } = renderComposer();
		await typeInComposer(field, "already sent");
		await userEvent.keyboard("{Enter}");
		await userEvent.keyboard("{Meta>}z{/Meta}");

		expect(field.textContent).toBe("");
	});

	it("does not undo an external draft seed into the previous draft", async () => {
		const onSend = vi.fn();
		const view = render(
			<ChatComposer onSend={onSend} draftSeed={{ id: "first", text: "first draft" }} />,
		);
		const field = screen.getByLabelText("Message the agent");
		await waitFor(() => expect(field).toHaveTextContent("first draft"));

		view.rerender(
			<ChatComposer onSend={onSend} draftSeed={{ id: "second", text: "replacement draft" }} />,
		);
		await waitFor(() => expect(field).toHaveTextContent("replacement draft"));
		field.focus();
		await userEvent.keyboard("{Meta>}z{/Meta}");

		expect(field).toHaveTextContent("replacement draft");
		expect(field).not.toHaveTextContent("first draft");
	});

	it("keeps the draft and reports the error when sending fails", async () => {
		const onSend = vi.fn().mockRejectedValue(new Error("daemon unavailable"));
		render(<ChatComposer onSend={onSend} />);
		const field = screen.getByLabelText("Message the agent") as HTMLElement;

		await typeInComposer(field, "do not lose this task");
		await userEvent.keyboard("{Enter}");

		expect(await screen.findByRole("alert")).toHaveTextContent("Your draft was kept");
		expect(field.textContent).toBe("do not lose this task");
	});

	it("renders command failures from the live surface", () => {
		render(<ChatComposer onSend={vi.fn()} commandError="The approval could not be submitted" />);
		expect(screen.getByRole("alert")).toHaveTextContent("The approval could not be submitted");
	});
});

/* ---- steering -------------------------------------------------------------
   Queueing and steering are different promises to the user: a queued message
   waits for a cold start, a steer lands in the turn already running. The chord
   is the only way to pick the second one now, so each path that can reach it —
   Cmd, Ctrl, and the send control while a modifier is held — is pinned here.
--------------------------------------------------------------------------- */

describe("queued message edit", () => {
	it("saves a queued edit while the composer is busy", async () => {
		const onSend = vi.fn().mockResolvedValue(undefined);
		render(
			<ChatComposer
				onSend={onSend}
				busy
				willQueue
				editingQueuedTurnId="queued-1"
				draftSeed={{ id: "queued-1", text: "hi" }}
			/>,
		);
		await waitFor(() => expect(screen.getByLabelText("Message the agent")).toHaveTextContent("hi"));
		await userEvent.keyboard("{Enter}");
			await waitFor(() => expect(onSend).toHaveBeenCalledWith("hi", undefined, []));
	});
});

describe("steering", () => {
	function renderSteerable(props: Partial<Parameters<typeof ChatComposer>[0]> = {}) {
		const onSend = vi.fn();
		const onSteer = vi.fn().mockResolvedValue(undefined);
		render(<ChatComposer onSend={onSend} onSteer={onSteer} canSteer willQueue {...props} />);
		return {
			onSend,
			onSteer,
			field: screen.getByLabelText("Message the agent") as HTMLElement,
		};
	}

	it("steers on Cmd+Enter rather than queueing, and trims the body first", async () => {
		const { onSend, onSteer, field } = renderSteerable();

		await typeInComposer(field, "  change course  ");
		await userEvent.keyboard("{Meta>}{Enter}{/Meta}");

		await waitFor(() => expect(onSteer).toHaveBeenCalledWith("change course"));
		expect(onSend).not.toHaveBeenCalled();
	});

	it("steers on Ctrl+Enter, so the chord exists off macOS too", async () => {
		const { onSend, onSteer, field } = renderSteerable();

		await typeInComposer(field, "change course");
		await userEvent.keyboard("{Control>}{Enter}{/Control}");

		await waitFor(() => expect(onSteer).toHaveBeenCalledWith("change course"));
		expect(onSend).not.toHaveBeenCalled();
	});

	it("queues on a bare Enter even while a turn is running", async () => {
		const { onSend, onSteer, field } = renderSteerable();

		await typeInComposer(field, "wait your turn");
		await userEvent.keyboard("{Enter}");

		await waitFor(() => expect(onSend).toHaveBeenCalledWith("wait your turn"));
		expect(onSteer).not.toHaveBeenCalled();
	});

	// The modifier is a request, not a guarantee: a harness that cannot steer must
	// still deliver the message rather than swallow the keystroke.
	it("falls back to queueing on Cmd+Enter when the harness cannot steer", async () => {
		const onSend = vi.fn();
		render(<ChatComposer onSend={onSend} willQueue />);
		const field = screen.getByLabelText("Message the agent") as HTMLElement;

		await typeInComposer(field, "no steering here");
		await userEvent.keyboard("{Meta>}{Enter}{/Meta}");

		await waitFor(() => expect(onSend).toHaveBeenCalledWith("no steering here"));
	});

	// The indicator beside the send control is painted from a window-level view of
	// the modifier, so clicking has to read that same state or the button would
	// contradict the label sitting next to it.
	it("steers when the send control is clicked with a modifier held", async () => {
		const { onSend, onSteer, field } = renderSteerable();

		await typeInComposer(field, "pointer agrees with the chip");
		act(() => {
			window.dispatchEvent(new KeyboardEvent("keydown", { key: "Meta", metaKey: true }));
		});

		await userEvent.click(screen.getByRole("button", { name: "Send message" }));

		await waitFor(() => expect(onSteer).toHaveBeenCalledWith("pointer agrees with the chip"));
		expect(onSend).not.toHaveBeenCalled();
	});

	it("releasing the modifier returns the send control to queueing", async () => {
		const { onSend, onSteer, field } = renderSteerable();

		await typeInComposer(field, "back to the queue");
		act(() => {
			window.dispatchEvent(new KeyboardEvent("keydown", { key: "Meta", metaKey: true }));
		});
		act(() => {
			window.dispatchEvent(new KeyboardEvent("keyup", { key: "Meta", metaKey: false }));
		});

		await userEvent.click(screen.getByRole("button", { name: "Send message" }));

		await waitFor(() => expect(onSend).toHaveBeenCalledWith("back to the queue"));
		expect(onSteer).not.toHaveBeenCalled();
	});

	// A refused steer is an ordinary outcome — the turn may have ended mid-keystroke —
	// so the draft has to survive for the user to send it the other way.
	it("keeps the draft when the provider refuses the steer", async () => {
		const onSend = vi.fn();
		const onSteer = vi.fn().mockRejectedValue(new Error("the turn already finished"));
		render(<ChatComposer onSend={onSend} onSteer={onSteer} canSteer willQueue />);
		const field = screen.getByLabelText("Message the agent") as HTMLElement;

		await typeInComposer(field, "do not lose this");
		await userEvent.keyboard("{Meta>}{Enter}{/Meta}");

		await waitFor(() => expect(onSteer).toHaveBeenCalledWith("do not lose this"));
		expect(field.textContent).toBe("do not lose this");
		expect(onSend).not.toHaveBeenCalled();
	});
});

/* ---- slash commands ------------------------------------------------------ */

describe("slash commands", () => {
	it("opens the skill menu on a leading slash", async () => {
		const { field } = renderComposer({ skills: SKILLS });
		await typeInComposer(field, "/");
		expect(screen.getByRole("listbox")).toBeTruthy();
		expect(screen.getAllByRole("option")).toHaveLength(3);
	});

	it("hides the generic agent source and keeps the AO source label", async () => {
		const { field } = renderComposer({
			skills: [
				{ name: "built-in", displayName: "built-in", source: "agent" },
				{ name: "compact", displayName: "compact", source: "ao" },
			],
		});
		await typeInComposer(field, "/");

		expect(screen.queryByText("agent", { exact: true })).toBeNull();
		expect(screen.getByText("AO", { exact: true })).toBeInTheDocument();
	});

	it("filters as the user types", async () => {
		const { field } = renderComposer({ skills: SKILLS });
		await typeInComposer(field, "/rev");
		const options = screen.getAllByRole("option");
		expect(options).toHaveLength(2);
		// The prefix match leads; the mid-name match follows.
		expect(options[0]?.textContent).toContain("/review");
	});

	// The whole point of gating on the provider's answer: with nothing to offer,
	// the menu must not appear and the slash must behave like a character.
	it("leaves the slash an ordinary character when the provider reported no skills", async () => {
		const { onSend, field } = renderComposer({ skills: [] });
		await typeInComposer(field, "/rev");
		expect(screen.queryByRole("listbox")).toBeNull();
		expect(field.textContent).toBe("/rev");
		// And Enter still sends, rather than being consumed by a menu that is not there.
		await userEvent.keyboard("{Enter}");
		expect(onSend).toHaveBeenCalledWith("/rev");
	});

	it("opens on a slash after existing text", async () => {
		const { field } = renderComposer({ skills: SKILLS });
		await typeInComposer(field, "look here /rev");
		expect(screen.getByRole("listbox")).toBeInTheDocument();
		expect(screen.getByRole("option", { name: /\/review/ })).toBeInTheDocument();
	});

	it("moves the highlight with the arrow keys and wraps", async () => {
		const { field } = renderComposer({ skills: SKILLS });
		await typeInComposer(field, "/");

		const selected = () =>
			screen.getAllByRole("option").findIndex((node) => node.getAttribute("aria-selected") === "true");
		expect(selected()).toBe(0);
		await userEvent.keyboard("{ArrowDown}");
		expect(selected()).toBe(1);
		await userEvent.keyboard("{ArrowUp}{ArrowUp}");
		expect(selected()).toBe(2);
	});

	it("does not let mouse movement change keyboard selection", async () => {
		const { field } = renderComposer({ skills: SKILLS });
		await typeInComposer(field, "/");
		await userEvent.keyboard("{ArrowDown}");
		fireEvent.mouseEnter(screen.getAllByRole("option")[0]!);

		const selected = screen
			.getAllByRole("option")
			.findIndex((node) => node.getAttribute("aria-selected") === "true");
		expect(selected).toBe(1);
	});

	it("returns to the first result when the search changes", async () => {
		const { field } = renderComposer({ skills: SKILLS });
		await typeInComposer(field, "/");
		await userEvent.keyboard("{ArrowDown}{ArrowDown}");
		await typeInComposer(field, "r");

		const selected = screen
			.getAllByRole("option")
			.findIndex((node) => node.getAttribute("aria-selected") === "true");
		expect(selected).toBe(0);
	});

	it("does not force-scroll when filtering keeps the visible first result selected", async () => {
		const scrollIntoView = vi.spyOn(Element.prototype, "scrollIntoView");
		const { field } = renderComposer({ skills: SKILLS });
		await typeInComposer(field, "/");
		scrollIntoView.mockClear();
		await typeInComposer(field, "r");

		expect(scrollIntoView).not.toHaveBeenCalled();
		scrollIntoView.mockRestore();
	});

	it("inserts the highlighted skill on Enter instead of sending", async () => {
		const { onSend, field } = renderComposer({ skills: SKILLS });
		await typeInComposer(field, "/rev");
		await userEvent.keyboard("{Enter}");

		expect(onSend).not.toHaveBeenCalled();
		const token = field.querySelector('[data-composer-token="skill"]');
		expect(token).toHaveTextContent("/review");
		expect(token).toHaveClass("text-logo-accent");
		expect(token).not.toHaveClass("text-accent");
		expect(field.textContent).toBe("/review ");
		// The trigger is finished, so the menu closes rather than re-filtering what was
		// just inserted.
		expect(screen.queryByRole("listbox")).toBeNull();
	});

	it("inserts instead of sending when Enter follows typing before React rerenders", async () => {
		const { onSend, field } = renderComposer({ skills: SKILLS });
		await typeAndPressInLexicalEditor(field, "/rev", "Enter");

		expect(onSend).not.toHaveBeenCalled();
		await waitFor(() => {
			expect(field.querySelector('[data-composer-token="skill"]')).toHaveTextContent("/review");
		});
		expect(field.querySelectorAll('[data-composer-token="skill"]')).toHaveLength(1);
	});

	it("does not complete or send when Enter confirms IME composition", async () => {
		const { onSend, field } = renderComposer({ skills: SKILLS });
		await typeInComposer(field, "/rev");
		fireEvent.keyDown(field, { key: "Enter", isComposing: true });

		expect(onSend).not.toHaveBeenCalled();
		expect(field.querySelector('[data-composer-token="skill"]')).toBeNull();
	});

	it("does not auto-complete an exact skill during IME composition", async () => {
		const onSend = vi.fn();
		const view = render(<ChatComposer onSend={onSend} skills={[]} />);
		const field = screen.getByLabelText("Message the agent");
		await typeInComposer(field, "/ship");
		fireEvent.compositionStart(field);
		view.rerender(<ChatComposer onSend={onSend} skills={SKILLS} />);

		expect(field.querySelector('[data-composer-token="skill"]')).toBeNull();
		fireEvent.compositionEnd(field);

		await waitFor(() => {
			expect(field.querySelector('[data-composer-token="skill"]')).toHaveTextContent("/ship");
		});
	});

	it("keeps Shift+Enter as a newline while the skill menu is open", async () => {
		const { onSend, field } = renderComposer({ skills: SKILLS });
		await typeInComposer(field, "/rev");
		await userEvent.keyboard("{Shift>}{Enter}{/Shift}");

		expect(onSend).not.toHaveBeenCalled();
		expect(field.querySelector('[data-composer-token="skill"]')).toBeNull();
		expect(composerWireText(field)).toBe("/rev\n");
	});

	it("turns a fully typed unambiguous skill into a chip and closes the menu", async () => {
		const { field } = renderComposer({ skills: SKILLS });
		await typeInComposer(field, "/ship");

		await waitFor(() => {
			expect(field.querySelector('[data-composer-token="skill"]')).toHaveTextContent("/ship");
		});
		expect(screen.queryByRole("listbox")).toBeNull();
	});

	it("keeps an exact skill editable when it prefixes another skill", async () => {
		const { field } = renderComposer({
			skills: [
				{ name: "review", displayName: "review", source: "user" },
				{ name: "review-pr", displayName: "review-pr", source: "user" },
			],
		});
		await typeInComposer(field, "/review");

		expect(field.querySelector('[data-composer-token="skill"]')).toBeNull();
		expect(screen.getByRole("listbox")).toBeInTheDocument();
		await typeInComposer(field, "-pr");
		await waitFor(() => {
			expect(field.querySelector('[data-composer-token="skill"]')).toHaveTextContent("/review-pr");
		});
	});

	it("does not duplicate existing whitespace after an accepted completion", async () => {
		const { field } = renderComposer({ skills: SKILLS });
		await typeInComposer(field, "/rev x");
		await placeLexicalCaret(field, 4);
		await userEvent.keyboard("{Enter}");

		expect(composerWireText(field)).toBe("/review x");
	});

	it("sends a skill chip as its plain slash command", async () => {
		const { onSend, field } = renderComposer({ skills: SKILLS });
		await typeInComposer(field, "/rev");
		await userEvent.keyboard("{Enter}{Enter}");

		expect(onSend).toHaveBeenCalledWith("/review");
	});

	it("inserts on Tab as well", async () => {
		const { field } = renderComposer({ skills: SKILLS });
		await typeInComposer(field, "/ship");
		await userEvent.keyboard("{Tab}");
		expect(field.querySelector('[data-composer-token="skill"]')).toHaveTextContent("/ship");
		expect(field.textContent).toBe("/ship ");
	});

	it("closes on Escape and leaves the typed text alone", async () => {
		const { field } = renderComposer({ skills: SKILLS });
		await typeInComposer(field, "/rev");
		await userEvent.keyboard("{Escape}");
		expect(screen.queryByRole("listbox")).toBeNull();
		expect(field.textContent).toBe("/rev");
	});

	// After a dismissal the composer must behave like a plain field again, or Enter
	// would appear to do nothing.
	it("sends on Enter after the menu was dismissed", async () => {
		const { onSend, field } = renderComposer({ skills: SKILLS });
		await typeInComposer(field, "/rev");
		await userEvent.keyboard("{Escape}");
		await userEvent.keyboard("{Enter}");
		expect(onSend).toHaveBeenCalledWith("/rev");
	});

	it("selects with the mouse", async () => {
		const { field } = renderComposer({ skills: SKILLS });
		await typeInComposer(field, "/");
		await userEvent.click(screen.getByText("/ship"));
		expect(field.querySelector('[data-composer-token="skill"]')).toHaveTextContent("/ship");
	});
});

/* ---- file mentions ------------------------------------------------------- */

describe("file mentions", () => {
	it("opens the file menu on an at-sign", async () => {
		const { field } = renderComposer({ filePaths: FILES });
		await typeInComposer(field, "@chat");
		const options = screen.getAllByRole("option");
		// Both files whose name starts with "chat" match; neither AGENTS.md does.
		expect(options).toHaveLength(2);
		// The row reads as a file name plus where it lives, not as one long path.
		expect(options[0]?.textContent).toContain("chat.go");
		expect(options[0]?.textContent).toContain("backend/internal/ports");
	});

	// The label is a name; what the agent has to resolve is the whole path.
	it("inserts the full path, without the sigil", async () => {
		const { field } = renderComposer({ filePaths: FILES });
		await typeInComposer(field, "look at @ChatComposer");
		await userEvent.keyboard("{Enter}");
		const token = field.querySelector('[data-composer-token="file"]');
		expect(token).toHaveTextContent("ChatComposer.tsx");
		expect(token).toHaveAttribute(
			"data-value",
			"frontend/src/renderer/components/chat/ChatComposer.tsx",
		);
	});

	it("inserts a file instead of sending when Enter follows typing before React rerenders", async () => {
		const { onSend, field } = renderComposer({ filePaths: FILES });
		await typeAndPressInLexicalEditor(field, "@chat", "Enter");

		expect(onSend).not.toHaveBeenCalled();
		await waitFor(() => {
			expect(field.querySelector('[data-composer-token="file"]')).toHaveAttribute(
				"data-value",
				"backend/internal/ports/chat.go",
			);
		});
		expect(field.querySelectorAll('[data-composer-token="file"]')).toHaveLength(1);
	});

	it("keeps Shift+Enter as a newline while the file menu is open", async () => {
		const { onSend, field } = renderComposer({ filePaths: FILES });
		await typeInComposer(field, "@chat");
		await userEvent.keyboard("{Shift>}{Enter}{/Shift}");

		expect(onSend).not.toHaveBeenCalled();
		expect(field.querySelector('[data-composer-token="file"]')).toBeNull();
		expect(composerWireText(field)).toBe("@chat\n");
	});

	it("sends the inserted path verbatim", async () => {
		const { onSend, field } = renderComposer({ filePaths: FILES });
		await typeInComposer(field, "@chat.go");
		await userEvent.keyboard("{Enter}");
		await userEvent.keyboard("{Enter}");
		expect(onSend).toHaveBeenCalledWith("backend/internal/ports/chat.go");
	});

	it("quotes a completed path containing spaces in the agent-facing text", async () => {
		const { onSend, field } = renderComposer({ filePaths: ["docs/product notes.md"] });
		await typeInComposer(field, "read @notes");
		await userEvent.keyboard("{Enter}{Enter}");

		expect(field.querySelector('[data-composer-token="file"]')).not.toBeInTheDocument();
		expect(onSend).toHaveBeenCalledWith('read "docs/product notes.md"');
	});

	it("leaves the at-sign ordinary when there are no paths", async () => {
		const { field } = renderComposer({ filePaths: [] });
		await typeInComposer(field, "@chat");
		expect(screen.queryByRole("listbox")).toBeNull();
		expect(field.textContent).toBe("@chat");
	});

	it("says so when the worktree list was capped", async () => {
		const { field } = renderComposer({ filePaths: FILES, filePathsTruncated: true });
		await typeInComposer(field, "@chat");
		expect(screen.getByText(/Showing part of a large worktree/)).toBeTruthy();
	});
});

/* ---- attachments --------------------------------------------------------- */

describe("attachments", () => {
	// A control that cannot deliver must not be drawn: the fixture preview has no
	// worktree to write into.
	it("offers no attach control when there is nowhere to put the bytes", () => {
		renderComposer();
		expect(screen.queryByLabelText("Attach a file")).toBeNull();
	});

	it("offers the attach control when staging is wired", () => {
		renderComposer({ onStageAttachments: vi.fn() });
		expect(screen.getByLabelText("Attach a file")).toBeTruthy();
	});

	it("shows a removable chip per pasted image", async () => {
		const { field } = renderComposer({ onStageAttachments: vi.fn() });
		fireEvent.paste(field, { clipboardData: clipboardData([png("a.png"), png("b.png")]) });

		await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(2));
		expect(screen.getByLabelText("Remove a.png")).toBeTruthy();

		await userEvent.click(screen.getByLabelText("Remove a.png"));
		await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(1));
	});

	it("ignores a paste that carries no file", async () => {
		const { field } = renderComposer({ onStageAttachments: vi.fn() });
		fireEvent.paste(field, { clipboardData: clipboardData([]) });
		await waitFor(() => expect(screen.queryByRole("listitem")).toBeNull());
	});

	// The chip has to mean something: the bytes get written and the message names
	// the path the agent can open.
	it("stages the image and names the returned path in the message", async () => {
		const stage = vi.fn().mockResolvedValue([".ao/attachments/attachment-ab12cd34ef.png"]);
		const { onSend, field } = renderComposer({ onStageAttachments: stage });

		fireEvent.paste(field, { clipboardData: clipboardData([png()]) });
		await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(1));

		await typeInComposer(field, "what is wrong here");
		await userEvent.keyboard("{Enter}");

		await waitFor(() => expect(stage).toHaveBeenCalledTimes(1));
		expect(stage.mock.calls[0]?.[0]).toEqual([
			{ mimeType: "image/png", data: expect.any(String) },
		]);
		await waitFor(() =>
			expect(onSend).toHaveBeenCalledWith(
				"what is wrong here\n\nAttached files (read these files in the workspace):\n- .ao/attachments/attachment-ab12cd34ef.png",
			),
		);
		// Consumed, so the next message does not silently resend them.
		await waitFor(() => expect(screen.queryByRole("listitem")).toBeNull());
	});

	it("sends an image with no words, since the reference block carries the request", async () => {
		const stage = vi.fn().mockResolvedValue([".ao/attachments/attachment-1.png"]);
		const { onSend, field } = renderComposer({ onStageAttachments: stage });

		fireEvent.paste(field, { clipboardData: clipboardData([png()]) });
		await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(1));
		// Sent from the field with no text typed at all.
		await userEvent.keyboard("{Enter}");

		await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
		expect(onSend.mock.calls[0]?.[0]).toBe(
			"Attached files (read these files in the workspace):\n- .ao/attachments/attachment-1.png",
		);
	});

	it("also sends native image bytes when the provider negotiated image prompts", async () => {
		const stage = vi.fn().mockResolvedValue([".ao/attachments/attachment-native.png"]);
		const { onSend, field } = renderComposer({ onStageAttachments: stage, nativeImages: true });

		fireEvent.paste(field, { clipboardData: clipboardData([png()]) });
		await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(1));
		await typeInComposer(field, "inspect this");
		await userEvent.keyboard("{Enter}");

		await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
		expect(onSend.mock.calls[0]?.[0]).toContain(".ao/attachments/attachment-native.png");
		expect(onSend.mock.calls[0]?.[1]).toEqual([
			{ mimeType: "image/png", data: expect.any(String) },
		]);
	});

	it("stages non-images by path without sending them as native image blocks", async () => {
		const stage = vi.fn().mockResolvedValue([
			".ao/attachments/attachment-native.png",
			".ao/attachments/notes.txt",
		]);
		const { onSend, field } = renderComposer({ onStageAttachments: stage, nativeImages: true });

		fireEvent.drop(field, {
			dataTransfer: {
				files: [png(), textFile()],
				items: [{ kind: "file" }, { kind: "file" }],
				getData: () => "",
			},
		});
		await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(2));
		await typeInComposer(field, "inspect these");
		await userEvent.keyboard("{Enter}");

		await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
		expect(onSend.mock.calls[0]?.[0]).toContain(".ao/attachments/notes.txt");
		expect(onSend.mock.calls[0]?.[1]).toEqual([
			{ mimeType: "image/png", data: expect.any(String) },
		]);
	});

	// A message claiming an attachment the agent cannot open is worse than a refusal.
	it("sends nothing when staging fails, and says so", async () => {
		const stage = vi.fn().mockRejectedValue(new Error("disk full"));
		const { onSend, field } = renderComposer({ onStageAttachments: stage });

		fireEvent.paste(field, { clipboardData: clipboardData([png()]) });
		await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(1));
		await typeInComposer(field, "look");
		await userEvent.keyboard("{Enter}");

		await waitFor(() => expect(screen.getByRole("alert")).toBeTruthy());
		expect(onSend).not.toHaveBeenCalled();
		// The images are still staged, so the user can retry rather than re-paste.
		expect(screen.getAllByRole("listitem")).toHaveLength(1);
		expect(field.textContent).toBe("look");
	});

	it("keeps attachments after a failed send and reuses their staged paths on retry", async () => {
		const stage = vi.fn().mockResolvedValue([".ao/attachments/attachment-retry.png"]);
		const onSend = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce(undefined);
		render(<ChatComposer onSend={onSend} onStageAttachments={stage} />);
		const field = screen.getByLabelText("Message the agent") as HTMLElement;

		fireEvent.paste(field, { clipboardData: clipboardData([png()]) });
		await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(1));
		await typeInComposer(field, "inspect this");
		await userEvent.keyboard("{Enter}");

		expect(await screen.findByRole("alert")).toHaveTextContent("attachments were kept");
		expect(field.textContent).toBe("inspect this");
		expect(screen.getAllByRole("listitem")).toHaveLength(1);

		await userEvent.keyboard("{Enter}");
		await waitFor(() => expect(onSend).toHaveBeenCalledTimes(2));
		expect(stage).toHaveBeenCalledTimes(1);
		await waitFor(() => expect(screen.queryByRole("listitem")).not.toBeInTheDocument());
		expect(field.textContent).toBe("");
	});
});

/* ---- disabled and busy states ------------------------------------------- */

describe("unavailable states", () => {
	it("explains a stopped controller and refuses to send", async () => {
		const { onSend, field } = renderComposer({ disabled: true, skills: SKILLS });
		expect(field).toHaveAttribute("aria-placeholder", expect.stringContaining("not connected"));
		expect(field).toHaveAttribute("contenteditable", "false");
		expect(field).toHaveAttribute("aria-readonly", "true");
		expect(field).toHaveAttribute("tabindex", "-1");
		await userEvent.keyboard("{Enter}");
		expect(onSend).not.toHaveBeenCalled();
	});

	it("does not show a loading spinner when no queued edit is saving", () => {
		renderComposer();
		expect(
			screen.getByRole("button", { name: "Send message" }).querySelector(".animate-spin"),
		).not.toBeInTheDocument();
	});

	it("shows a loading spinner only while the queued edit being edited is saving", () => {
		renderComposer({ editingQueuedTurnId: "turn-1", savingQueuedEditPending: true });
		expect(
			screen.getByRole("button", { name: "Send message" }).querySelector(".animate-spin"),
		).toBeInTheDocument();
	});

	it("says a mid-turn message will be held", () => {
		const { field } = renderComposer({ willQueue: true });
		expect(field).toHaveAttribute(
			"aria-placeholder",
			expect.stringContaining("sends when it finishes"),
		);
	});

	it("turns the primary composer action into stop while the agent is working and the draft is empty", async () => {
		const onSend = vi.fn();
		const onInterrupt = vi.fn();
		render(<ChatComposer onSend={onSend} willQueue onInterrupt={onInterrupt} />);

		await userEvent.click(screen.getByRole("button", { name: "Stop turn" }));

		expect(onInterrupt).toHaveBeenCalledTimes(1);
		expect(onSend).not.toHaveBeenCalled();
	});

	it("keeps the primary action as queue while the agent is working and a draft exists", async () => {
		const onSend = vi.fn();
		const onInterrupt = vi.fn();
		render(<ChatComposer onSend={onSend} willQueue onInterrupt={onInterrupt} />);

		await typeInComposer(screen.getByLabelText("Message the agent"), "follow up");
		await userEvent.click(screen.getByRole("button", { name: "Send message" }));

		expect(onSend).toHaveBeenCalledWith("follow up");
		expect(onInterrupt).not.toHaveBeenCalled();
	});

	it("explains when a send is blocked by the previous in-flight message", async () => {
		const onSend = vi.fn().mockImplementation(
			() => new Promise<void>(() => {
				/* keep pending */
			}),
		);
		render(<ChatComposer busy onSend={onSend} willQueue />);
		const field = screen.getByLabelText("Message the agent");

		await typeInComposer(field, "follow up");
		await userEvent.keyboard("{Enter}");

		expect(onSend).not.toHaveBeenCalled();
		expect(screen.getByRole("alert")).toHaveTextContent(/still sending the previous message/i);
	});
});
