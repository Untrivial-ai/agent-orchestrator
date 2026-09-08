import { purgeFileAttachmentsForSession } from "../../hooks/useFileAttachments";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ChatWorkspace } from "./ChatWorkspace";
import { chatFixture } from "../../lib/chat-fixture";
import { typeInLexicalEditor } from "../../test/lexical";
import { TooltipProvider } from "../ui/tooltip";
import type { ConversationContentSummary, ConversationSnapshot } from "../../types/conversation";

beforeEach(() => { window.localStorage.clear(); purgeFileAttachmentsForSession(chatFixture.sessionId); });

const path = ".ao/attachments/attachment-shot.png";
const suffix = `Attached files (read these files in the workspace):\n- ${path}`;

function setup(text = "inspect this", content: ConversationContentSummary[] = []) {
	const edit = vi.fn().mockResolvedValue(undefined);
	const send = vi.fn().mockResolvedValue(undefined);
	const stage = vi.fn().mockResolvedValue([path]);
	const snapshot: ConversationSnapshot = {
		...chatFixture,
		turns: [{ id: "q1", state: "queued" as const, requestedAt: "2026-09-06T10:00:00Z" }],
		items: [
			{
				kind: "message" as const,
				id: "m1",
				turnId: "q1",
				sequence: 1,
				revision: 0,
				role: "user" as const,
				origin: "human" as const,
				text,
				content,
				streaming: false,
				createdAt: "2026-09-06T10:00:00Z",
			},
		],
	};
	const view = render(
		<TooltipProvider>
			<ChatWorkspace
				snapshot={snapshot}
				onSend={send}
				onEditQueuedTurn={edit}
				onStageAttachments={stage}
				nativeImages
			/>
		</TooltipProvider>,
	);
	return {
		edit,
		send,
		stage,
		snapshot,
		rerenderSnapshot: (next: ConversationSnapshot) =>
			view.rerender(
				<TooltipProvider>
					<ChatWorkspace
						snapshot={next}
						onSend={send}
						onEditQueuedTurn={edit}
						onStageAttachments={stage}
						nativeImages
					/>
				</TooltipProvider>,
			),
	};
}

async function beginEdit() {
	await userEvent.click(
		within(screen.getByTestId("queued-message-q1")).getByRole("button", { name: "Edit queued message" }),
	);
}

async function pasteImage(name = "shot.png") {
	fireEvent.paste(screen.getByRole("combobox"), {
		clipboardData: {
			files: [new File([new Uint8Array([137, 80, 78, 71])], name, { type: "image/png" })],
			items: [],
		},
	});
	await screen.findByLabelText(`Remove ${name}`);
}

describe("queued message attachments", () => {
	it("saves newly attached image bytes and their staged reference to the queued turn", async () => {
		const { edit, send, stage } = setup();
		await beginEdit();
		await pasteImage();
		await userEvent.click(screen.getByRole("button", { name: "Send message" }));
		await waitFor(() =>
			expect(edit).toHaveBeenCalledWith("q1", `inspect this\n\n${suffix}`, {
				attachments: [{ mimeType: "image/png", data: expect.any(String) }],
				retainedContent: [],
				expectedRevision: 0,
			}),
		);
		expect(stage).toHaveBeenCalledOnce();
		expect(send).not.toHaveBeenCalled();
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
	});

	it("opens existing images as chips and retains their server-owned bytes when text changes", async () => {
		const { edit } = setup(`inspect this\n\n${suffix}`, [{ type: "image", mimeType: "image/png" }]);
		await beginEdit();
		expect(screen.getByLabelText("Remove attachment-shot.png")).toBeInTheDocument();
		expect(screen.getByRole("combobox")).not.toHaveTextContent(".ao/attachments");
		await typeInLexicalEditor(screen.getByRole("combobox"), " carefully");
		await userEvent.click(screen.getByRole("button", { name: "Send message" }));
		await waitFor(() =>
			expect(edit).toHaveBeenCalledWith("q1", `inspect this carefully\n\n${suffix}`, {
				retainedContent: [0],
				expectedRevision: 0,
			}),
		);
	});

	it.each(["path", "native"])("removes only the selected %s attachment when independent image counts match", async (selected) => {
		const { edit } = setup(`inspect this\n\n${suffix}`, [{ type: "image", mimeType: "image/png" }]);
		await beginEdit();
		await userEvent.click(screen.getByLabelText(selected === "path" ? "Remove attachment-shot.png" : "Remove Image 1"));
		await userEvent.click(screen.getByRole("button", { name: "Send message" }));
		await waitFor(() =>
			expect(edit).toHaveBeenCalledWith("q1", selected === "path" ? "inspect this" : `inspect this\n\n${suffix}`, {
				retainedContent: selected === "path" ? [0] : [],
				expectedRevision: 0,
			}),
		);
	});

	it.each([8, 9])("preserves %i resources when saving a valid queued edit", async (count) => {
		const resources = Array.from({ length: count }, (_, index) => ({
			type: "resource_link", uri: `file:///context-${index}.txt`, name: `Context ${index}`,
		}));
		const { edit } = setup("inspect this", resources);
		await beginEdit();
		await typeInLexicalEditor(screen.getByRole("combobox"), " carefully");
		if (count === 8) await pasteImage();
		await userEvent.click(screen.getByRole("button", { name: "Send message" }));
		await waitFor(() => expect(edit).toHaveBeenCalledWith(
			"q1", count === 8 ? `inspect this carefully\n\n${suffix}` : "inspect this carefully",
			{
				retainedContent: resources.map((_, index) => index), expectedRevision: 0,
				...(count === 8 ? { attachments: [{ mimeType: "image/png", data: expect.any(String) }] } : {}),
			},
		));
	});

	it.each(["image/png", "text/plain"])("counts only native images when adding %s to eight retained images", async (mimeType) => {
		const { edit, stage } = setup("inspect this", Array.from({ length: 8 }, () => ({ type: "image", mimeType: "image/png" })));
		stage.mockResolvedValue([".ao/attachments/attachment-context.txt"]);
		await beginEdit();
		fireEvent.paste(screen.getByRole("combobox"), { clipboardData: { files: [new File(["context"], "new-file", { type: mimeType })], items: [] } });
		await screen.findByLabelText("Remove new-file");
		await userEvent.click(screen.getByRole("button", { name: "Send message" }));
		if (mimeType === "image/png") {
			await screen.findByText("You can attach up to 8 images.");
			expect(edit).not.toHaveBeenCalled();
		} else {
			await waitFor(() => expect(edit).toHaveBeenCalledWith("q1", "inspect this\n\nAttached files (read these files in the workspace):\n- .ao/attachments/attachment-context.txt", {
				retainedContent: [0, 1, 2, 3, 4, 5, 6, 7], expectedRevision: 0,
			}));
		}
	});

	it.each(["cancel", "save"])("isolates the ordinary draft and restores it after %s", async (action) => {
		const { edit } = setup();
		await typeInLexicalEditor(screen.getByRole("combobox"), "unrelated draft");
		await pasteImage("unrelated.png");
		await beginEdit();
		expect(screen.getByRole("combobox")).toHaveTextContent("inspect this");
		expect(screen.queryByLabelText("Remove unrelated.png")).not.toBeInTheDocument();
		if (action === "save") {
			await userEvent.click(screen.getByRole("button", { name: "Send message" }));
			await waitFor(() => expect(edit).toHaveBeenCalledOnce());
		} else {
			await userEvent.click(screen.getByRole("combobox"));
			await userEvent.keyboard("{Escape}");
		}
		await waitFor(() => expect(screen.getByRole("combobox")).toHaveTextContent("unrelated draft"));
		expect(screen.getByLabelText("Remove unrelated.png")).toBeInTheDocument();
	});

	it("keeps a failed edit for retry without staging the same image twice", async () => {
		const { edit, send, stage } = setup();
		edit.mockRejectedValueOnce(new Error("Could not save queued message edit"));
		await beginEdit();
		await pasteImage();
		await userEvent.click(screen.getByRole("button", { name: "Send message" }));
		await screen.findByText("Could not save queued message edit");
		expect(screen.getByRole("combobox")).toHaveTextContent("inspect this");
		expect(screen.getByLabelText("Remove shot.png")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Send message" }));
		await waitFor(() => expect(edit).toHaveBeenCalledTimes(2));
		expect(edit.mock.calls[1]).toEqual(edit.mock.calls[0]);
		expect(stage).toHaveBeenCalledOnce();
		expect(send).not.toHaveBeenCalled();
		await waitFor(() => expect(screen.queryByText("Editing queued message")).not.toBeInTheDocument());
	});

	it("keeps the edit target and draft when the turn dispatches before saving", async () => {
		const { edit, send, snapshot, rerenderSnapshot } = setup();
		edit.mockRejectedValueOnce(new Error("that message is no longer queued"));
		await beginEdit();
		await pasteImage();
		rerenderSnapshot({ ...snapshot, turns: snapshot.turns.map((turn) => ({ ...turn, state: "running" })) });
		await waitFor(() => expect(screen.queryByTestId("queued-message-q1")).not.toBeInTheDocument());
		expect(screen.getByText("Editing queued message")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Send message" }));
		await screen.findByText("that message is no longer queued");
		expect(edit).toHaveBeenCalledWith(
			"q1",
			expect.any(String),
			expect.objectContaining({ expectedRevision: 0 }),
		);
		expect(send).not.toHaveBeenCalled();
		expect(screen.getByRole("combobox")).toHaveTextContent("inspect this");
		expect(screen.getByLabelText("Remove shot.png")).toBeInTheDocument();
	});

	it("reloads retained attachments when reopening a newer revision of the same queued turn", async () => {
		const { edit, snapshot, rerenderSnapshot } = setup(`inspect this\n\n${suffix}`, [
			{ type: "image", mimeType: "image/png" },
		]);
		await beginEdit();
		expect(screen.getByLabelText("Remove attachment-shot.png")).toBeInTheDocument();
		rerenderSnapshot({
			...snapshot,
			items: snapshot.items.map((item) =>
				item.kind === "message" ? { ...item, text: "newer edit", revision: 1, content: [] } : item,
			),
		});
		await beginEdit();
		await waitFor(() => expect(screen.getByRole("combobox")).toHaveTextContent("newer edit"));
		expect(screen.queryByLabelText("Remove attachment-shot.png")).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Send message" }));
		await waitFor(() =>
			expect(edit).toHaveBeenCalledWith("q1", "newer edit", {
				retainedContent: [],
				expectedRevision: 1,
			}),
		);
	});
});
