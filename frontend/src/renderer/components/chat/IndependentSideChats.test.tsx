import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SideComposer } from "./IndependentSideChats";
import { emptySideChatDraft } from "./sideChatDraft";
import { typeInLexicalEditor } from "../../test/lexical";

const side = { id: "side-1", sessionId: "session-1", mainConversationId: "main-1", contextMode: "native",
	createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z", label: "Side", state: "ready" };

function renderSideComposer(draft = emptySideChatDraft(), flags: { sending?: boolean; ready?: boolean; deliveryPending?: boolean } = {}) {
	const onSend = vi.fn();
	const onFocusSide = vi.fn();
	const onCompact = vi.fn();
	render(<SideComposer draft={draft} onChange={vi.fn()} onSend={onSend} onInterrupt={vi.fn()}
		onAttach={vi.fn()} files={[]} onRemoveFile={vi.fn()} ready running={false} sending={false} {...flags}
		models={[]} skills={[{ name: "review", displayName: "review", source: "repo" }]}
		side={side} onSettings={vi.fn()} onRemoveReference={vi.fn()} onFocusSide={onFocusSide} onCompact={onCompact} focusKey={0} />);
	return { onSend, onFocusSide, onCompact, field: screen.getByLabelText("Side chat question") as HTMLElement };
}

describe("side composer slash menu", () => {
	it("shows provider skills and opens /btw without sending", async () => {
		const { onSend, onFocusSide, field } = renderSideComposer();
		await typeInLexicalEditor(field, "/");
		expect(screen.getByRole("option", { name: /\/review/ })).toBeInTheDocument();
		expect(screen.getByRole("option", { name: /\/compact/ })).toBeInTheDocument();
		await userEvent.click(screen.getByRole("option", { name: /\/btw/ }));
		await waitFor(() => expect(onFocusSide).toHaveBeenCalled());
		expect(onSend).not.toHaveBeenCalled();
	});

	it("keeps multiple reference chips removable", () => {
		const draft = { ...emptySideChatDraft(), references: [
			{ id: "one", conversationId: "main", messageId: "m1", revision: 1, text: "sun", role: "assistant" as const },
			{ id: "two", conversationId: "side", messageId: "m2", revision: 1, text: "moon", role: "user" as const },
		] };
		renderSideComposer(draft);
		expect(screen.getAllByRole("button", { name: "Remove referenced message" })).toHaveLength(2);
	});

	it("keeps a typed bare /btw in this side without sending", async () => {
		const { onSend, onFocusSide, field } = renderSideComposer();
		await typeInLexicalEditor(field, "/btw");
		fireEvent.keyDown(field, { key: "Enter" });
		await waitFor(() => expect(onFocusSide).toHaveBeenCalled());
		expect(onSend).not.toHaveBeenCalled();
	});

	it("routes /compact to this side instead of sending it as a question", async () => {
		const { onSend, onCompact, field } = renderSideComposer();
		await typeInLexicalEditor(field, "/compact");
		fireEvent.keyDown(field, { key: "Enter" });
		await waitFor(() => expect(onCompact).toHaveBeenCalledTimes(1));
		expect(onSend).not.toHaveBeenCalled();
	});
	it("keeps editing enabled during delivery and blocks uncertain sends", async () => {
		const { field, onSend } = renderSideComposer(emptySideChatDraft(), { sending: true, deliveryPending: true });
		expect(field).toHaveAttribute("contenteditable", "true");
		await typeInLexicalEditor(field, "next question");
		fireEvent.keyDown(field, { key: "Enter" });
		expect(onSend).not.toHaveBeenCalled();
	});

});
