import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ChatComposer } from "./ChatComposer";
import type { ChatSkill } from "../../types/conversation";

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
	return { onSend, field: screen.getByLabelText("Message the agent") as HTMLTextAreaElement };
}

const png = (name = "shot.png") =>
	new File([new Uint8Array([137, 80, 78, 71])], name, { type: "image/png" });
const textFile = (name = "notes.txt") => new File(["hello"], name, { type: "text/plain" });

/* ---- the keyboard contract the composer already had ---------------------- */

describe("send keys", () => {
	it("grows with the draft, then scrolls after the seven-line cap", () => {
		const { field } = renderComposer();
		let scrollHeight = 112;
		Object.defineProperty(field, "scrollHeight", {
			configurable: true,
			get: () => scrollHeight,
		});
		// JSDOM does not resolve Tailwind's generated max-height, so mirror the
		// max-h-40 value inline while exercising the browser measurement path.
		field.style.maxHeight = "160px";

		fireEvent.change(field, { target: { value: "A prompt that wraps to a few lines" } });
		expect(field.style.height).toBe("112px");
		expect(field.style.overflowY).toBe("hidden");

		scrollHeight = 240;
		fireEvent.change(field, { target: { value: "A much longer prompt that exceeds seven lines" } });
		expect(field.style.height).toBe("160px");
		expect(field.style.overflowY).toBe("auto");

		scrollHeight = 72;
		fireEvent.change(field, { target: { value: "Short again" } });
		expect(field.style.height).toBe("72px");
		expect(field.style.overflowY).toBe("hidden");
		expect(field).toHaveClass("chat-composer-scrollbar", "max-h-40");
	});

	it("separates secondary message tools from the primary send action", () => {
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
		expect(within(actions).getByRole("button", { name: "Send message" })).toBeInTheDocument();
		expect(within(actions).queryByText(/Enter to/)).not.toBeInTheDocument();
	});

	it("keeps optional footer actions with message tools, away from send controls", () => {
		render(
			<ChatComposer
				onSend={vi.fn()}
				onStageAttachments={vi.fn().mockResolvedValue([])}
				settings={<button type="button">Model</button>}
				footerAction={<button type="button">Compact</button>}
			/>,
		);
		const tools = screen.getByRole("group", { name: "Message tools" });
		expect(within(tools).getByRole("button", { name: "Compact" })).toBeInTheDocument();
		const actions = screen.getByRole("group", { name: "Send message controls" });
		expect(within(actions).queryByRole("button", { name: "Compact" })).not.toBeInTheDocument();
	});

	it("starts as a single-line field and grows only when the draft needs it", () => {
		const { field } = renderComposer();
		expect(field).toHaveAttribute("rows", "1");
		expect(field).toHaveClass("min-h-9");
		expect(field).not.toHaveClass("min-h-[3.25rem]");
	});

	it("uses the AO logo palette for the send control", async () => {
		const { field } = renderComposer();
		const send = screen.getByRole("button", { name: "Send message" });
		expect(send).toHaveClass("bg-logo-accent", "text-logo-accent-foreground");

		await userEvent.type(field, "hello");
		expect(send).toBeEnabled();
		expect(send).toHaveClass("hover:bg-logo-accent-bright", "focus-visible:ring-logo-accent/45");
	});

	it("turns the empty send action into Stop while the agent is working", async () => {
		const onInterrupt = vi.fn();
		const { field } = renderComposer({ willQueue: true, onInterrupt });

		const stop = screen.getByRole("button", { name: "Stop turn" });
		expect(screen.queryByRole("button", { name: "Send message" })).not.toBeInTheDocument();
		await userEvent.click(stop);
		expect(onInterrupt).toHaveBeenCalledOnce();

		await userEvent.type(field, "queue this next");
		expect(screen.queryByRole("button", { name: "Stop turn" })).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Send message" })).toBeEnabled();
	});

	it("sends on Enter", async () => {
		const { onSend, field } = renderComposer();
		await userEvent.type(field, "hello");
		await userEvent.keyboard("{Enter}");
		expect(onSend).toHaveBeenCalledWith("hello");
	});

	it("makes a newline on Shift+Enter and does not send", async () => {
		const { onSend, field } = renderComposer();
		await userEvent.type(field, "one");
		await userEvent.keyboard("{Shift>}{Enter}{/Shift}");
		await userEvent.type(field, "two");
		expect(onSend).not.toHaveBeenCalled();
		expect(field.value).toBe("one\ntwo");
	});

	it("refuses to send an empty message: there is no keystroke concept here", async () => {
		const { onSend, field } = renderComposer();
		await userEvent.type(field, "   ");
		await userEvent.keyboard("{Enter}");
		expect(onSend).not.toHaveBeenCalled();
	});

	it("clears the field after a send", async () => {
		const { field } = renderComposer();
		await userEvent.type(field, "hello");
		await userEvent.keyboard("{Enter}");
		expect(field.value).toBe("");
	});

	it("keeps the draft and reports the error when sending fails", async () => {
		const onSend = vi.fn().mockRejectedValue(new Error("daemon unavailable"));
		render(<ChatComposer onSend={onSend} />);
		const field = screen.getByLabelText("Message the agent") as HTMLTextAreaElement;

		await userEvent.type(field, "do not lose this task");
		await userEvent.keyboard("{Enter}");

		expect(await screen.findByRole("alert")).toHaveTextContent("Your draft was kept");
		expect(field.value).toBe("do not lose this task");
	});

	it("renders command failures from the live surface", () => {
		render(<ChatComposer onSend={vi.fn()} commandError="The approval could not be submitted" />);
		expect(screen.getByRole("alert")).toHaveTextContent("The approval could not be submitted");
	});
});

/* ---- slash commands ------------------------------------------------------ */

describe("slash commands", () => {
	it("opens the skill menu on a leading slash", async () => {
		const { field } = renderComposer({ skills: SKILLS });
		await userEvent.type(field, "/");
		expect(screen.getByRole("listbox")).toBeTruthy();
		expect(screen.getAllByRole("option")).toHaveLength(3);
	});

	it("filters as the user types", async () => {
		const { field } = renderComposer({ skills: SKILLS });
		await userEvent.type(field, "/rev");
		const options = screen.getAllByRole("option");
		expect(options).toHaveLength(2);
		// The prefix match leads; the mid-name match follows.
		expect(options[0]?.textContent).toContain("/review");
	});

	// The whole point of gating on the provider's answer: with nothing to offer,
	// the menu must not appear and the slash must behave like a character.
	it("leaves the slash an ordinary character when the provider reported no skills", async () => {
		const { onSend, field } = renderComposer({ skills: [] });
		await userEvent.type(field, "/rev");
		expect(screen.queryByRole("listbox")).toBeNull();
		expect(field.value).toBe("/rev");
		// And Enter still sends, rather than being consumed by a menu that is not there.
		await userEvent.keyboard("{Enter}");
		expect(onSend).toHaveBeenCalledWith("/rev");
	});

	it("does not open on a slash that is not the start of the message", async () => {
		const { field } = renderComposer({ skills: SKILLS });
		await userEvent.type(field, "look in src/app");
		expect(screen.queryByRole("listbox")).toBeNull();
	});

	it("moves the highlight with the arrow keys and wraps", async () => {
		const { field } = renderComposer({ skills: SKILLS });
		await userEvent.type(field, "/");

		const selected = () =>
			screen.getAllByRole("option").findIndex((node) => node.getAttribute("aria-selected") === "true");
		expect(selected()).toBe(0);
		await userEvent.keyboard("{ArrowDown}");
		expect(selected()).toBe(1);
		await userEvent.keyboard("{ArrowUp}{ArrowUp}");
		expect(selected()).toBe(2);
	});

	it("inserts the highlighted skill on Enter instead of sending", async () => {
		const { onSend, field } = renderComposer({ skills: SKILLS });
		await userEvent.type(field, "/rev");
		await userEvent.keyboard("{Enter}");

		expect(onSend).not.toHaveBeenCalled();
		expect(field.value).toBe("/review ");
		// The trigger is finished, so the menu closes rather than re-filtering what was
		// just inserted.
		expect(screen.queryByRole("listbox")).toBeNull();
	});

	it("inserts on Tab as well", async () => {
		const { field } = renderComposer({ skills: SKILLS });
		await userEvent.type(field, "/ship");
		await userEvent.keyboard("{Tab}");
		expect(field.value).toBe("/ship ");
	});

	it("closes on Escape and leaves the typed text alone", async () => {
		const { field } = renderComposer({ skills: SKILLS });
		await userEvent.type(field, "/rev");
		await userEvent.keyboard("{Escape}");
		expect(screen.queryByRole("listbox")).toBeNull();
		expect(field.value).toBe("/rev");
	});

	// After a dismissal the composer must behave like a plain field again, or Enter
	// would appear to do nothing.
	it("sends on Enter after the menu was dismissed", async () => {
		const { onSend, field } = renderComposer({ skills: SKILLS });
		await userEvent.type(field, "/rev");
		await userEvent.keyboard("{Escape}");
		await userEvent.keyboard("{Enter}");
		expect(onSend).toHaveBeenCalledWith("/rev");
	});

	it("selects with the mouse", async () => {
		const { field } = renderComposer({ skills: SKILLS });
		await userEvent.type(field, "/");
		await userEvent.click(screen.getByText("/ship"));
		expect(field.value).toBe("/ship ");
	});
});

/* ---- file mentions ------------------------------------------------------- */

describe("file mentions", () => {
	it("opens the file menu on an at-sign", async () => {
		const { field } = renderComposer({ filePaths: FILES });
		await userEvent.type(field, "@chat");
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
		await userEvent.type(field, "look at @ChatComposer");
		await userEvent.keyboard("{Enter}");
		expect(field.value).toBe(
			"look at frontend/src/renderer/components/chat/ChatComposer.tsx ",
		);
	});

	it("sends the inserted path verbatim", async () => {
		const { onSend, field } = renderComposer({ filePaths: FILES });
		await userEvent.type(field, "@chat.go");
		await userEvent.keyboard("{Enter}");
		await userEvent.keyboard("{Enter}");
		expect(onSend).toHaveBeenCalledWith("backend/internal/ports/chat.go");
	});

	it("leaves the at-sign ordinary when there are no paths", async () => {
		const { field } = renderComposer({ filePaths: [] });
		await userEvent.type(field, "@chat");
		expect(screen.queryByRole("listbox")).toBeNull();
		expect(field.value).toBe("@chat");
	});

	it("says so when the worktree list was capped", async () => {
		const { field } = renderComposer({ filePaths: FILES, filePathsTruncated: true });
		await userEvent.type(field, "@chat");
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
		fireEvent.paste(field, { clipboardData: { files: [png("a.png"), png("b.png")], items: [] } });

		await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(2));
		expect(screen.getByLabelText("Remove a.png")).toBeTruthy();

		await userEvent.click(screen.getByLabelText("Remove a.png"));
		await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(1));
	});

	it("ignores a paste that carries no file", async () => {
		const { field } = renderComposer({ onStageAttachments: vi.fn() });
		fireEvent.paste(field, { clipboardData: { files: [], items: [] } });
		await waitFor(() => expect(screen.queryByRole("listitem")).toBeNull());
	});

	// The chip has to mean something: the bytes get written and the message names
	// the path the agent can open.
	it("stages the image and names the returned path in the message", async () => {
		const stage = vi.fn().mockResolvedValue([".ao/attachments/attachment-ab12cd34ef.png"]);
		const { onSend, field } = renderComposer({ onStageAttachments: stage });

		fireEvent.paste(field, { clipboardData: { files: [png()], items: [] } });
		await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(1));

		await userEvent.type(field, "what is wrong here");
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

		fireEvent.paste(field, { clipboardData: { files: [png()], items: [] } });
		await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(1));
		// Sent from the field with no text typed at all.
		await userEvent.type(field, "{Enter}");

		await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
		expect(onSend.mock.calls[0]?.[0]).toBe(
			"Attached files (read these files in the workspace):\n- .ao/attachments/attachment-1.png",
		);
	});

	it("also sends native image bytes when the provider negotiated image prompts", async () => {
		const stage = vi.fn().mockResolvedValue([".ao/attachments/attachment-native.png"]);
		const { onSend, field } = renderComposer({ onStageAttachments: stage, nativeImages: true });

		fireEvent.paste(field, { clipboardData: { files: [png()], items: [] } });
		await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(1));
		await userEvent.type(field, "inspect this{Enter}");

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
			dataTransfer: { files: [png(), textFile()], items: [{ kind: "file" }, { kind: "file" }] },
		});
		await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(2));
		await userEvent.type(field, "inspect these{Enter}");

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

		fireEvent.paste(field, { clipboardData: { files: [png()], items: [] } });
		await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(1));
		await userEvent.type(field, "look");
		await userEvent.keyboard("{Enter}");

		await waitFor(() => expect(screen.getByRole("alert")).toBeTruthy());
		expect(onSend).not.toHaveBeenCalled();
		// The images are still staged, so the user can retry rather than re-paste.
		expect(screen.getAllByRole("listitem")).toHaveLength(1);
		expect(field.value).toBe("look");
	});

	it("keeps attachments after a failed send and reuses their staged paths on retry", async () => {
		const stage = vi.fn().mockResolvedValue([".ao/attachments/attachment-retry.png"]);
		const onSend = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce(undefined);
		render(<ChatComposer onSend={onSend} onStageAttachments={stage} />);
		const field = screen.getByLabelText("Message the agent") as HTMLTextAreaElement;

		fireEvent.paste(field, { clipboardData: { files: [png()], items: [] } });
		await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(1));
		await userEvent.type(field, "inspect this");
		await userEvent.keyboard("{Enter}");

		expect(await screen.findByRole("alert")).toHaveTextContent("attachments were kept");
		expect(field.value).toBe("inspect this");
		expect(screen.getAllByRole("listitem")).toHaveLength(1);

		await userEvent.keyboard("{Enter}");
		await waitFor(() => expect(onSend).toHaveBeenCalledTimes(2));
		expect(stage).toHaveBeenCalledTimes(1);
		await waitFor(() => expect(screen.queryByRole("listitem")).not.toBeInTheDocument());
		expect(field.value).toBe("");
	});
});

/* ---- disabled and busy states ------------------------------------------- */

describe("unavailable states", () => {
	it("explains a stopped controller and refuses to send", async () => {
		const { onSend, field } = renderComposer({ disabled: true, skills: SKILLS });
		expect(field.placeholder).toContain("not connected");
		await userEvent.keyboard("{Enter}");
		expect(onSend).not.toHaveBeenCalled();
	});

	it("says a mid-turn message will be held", () => {
		const { field } = renderComposer({ willQueue: true });
		expect(field.placeholder).toContain("sends when it finishes");
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

		await userEvent.type(screen.getByLabelText("Message the agent"), "follow up");
		await userEvent.click(screen.getByRole("button", { name: "Send message" }));

		expect(onSend).toHaveBeenCalledWith("follow up");
		expect(onInterrupt).not.toHaveBeenCalled();
	});
});
