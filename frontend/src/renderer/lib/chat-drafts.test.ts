import { describe, expect, it, vi } from "vitest";

import {
	acknowledgeChatComposerMutation,
	acknowledgeChatInlineEditMutation,
	activateChatDraftScope,
	beginChatComposerMutation,
	beginChatInlineEditMutation,
	cancelChatComposerMutation,
	cancelChatInlineEditMutation,
	clearAcceptedChatComposer,
	clearAcceptedChatInlineEdit,
	clearRejectedChatComposerDelivery,
	clearRejectedChatInlineEditDelivery,
	clearUncertainChatInlineEditDelivery,
	clearUncertainChatComposerDelivery,
	finishChatComposerMutation,
	finishChatInlineEditMutation,
	getChatComposerMutation,
	getChatInlineEditMutation,
	markChatComposerDeliveryAccepted,
	markChatInlineEditDeliveryAccepted,
	prepareChatComposerDelivery,
	prepareChatInlineEditDelivery,
	readChatSessionDraft,
	subscribeChatDraftRuntime,
	writeChatAttachments,
	writeChatComposerText,
	writeChatInlineEdit,
	type DraftStorage,
	type ChatDraftScope,
} from "./chat-drafts";

class MemoryStorage implements DraftStorage {
	readonly values = new Map<string, string>();

	getItem(key: string) {
		return this.values.get(key) ?? null;
	}

	setItem(key: string, value: string) {
		this.values.set(key, value);
	}

	removeItem(key: string) {
		this.values.delete(key);
	}
}

describe("Chat draft storage", () => {
	it("lets authoritative recreation purge once while every late obsolete callback fails closed", () => {
		const storage = new MemoryStorage();
		const first: ChatDraftScope = {
			sessionId: "session-recreated",
			incarnation: "2026-08-25T09:00:00.000Z",
		};
		const replacement: ChatDraftScope = {
			sessionId: first.sessionId,
			incarnation: "2026-08-26T09:00:00.000Z",
		};
		expect(activateChatDraftScope(first, storage)).toMatchObject({ ok: true });
		writeChatComposerText(first, "belongs to deleted session", storage);
		writeChatAttachments(
			first,
			[
				{
					id: "old-attachment",
					path: ".ao/attachments/old.png",
					name: "old.png",
					mimeType: "image/png",
					bytes: 10,
				},
			],
			storage,
		);
		const oldSend = prepareChatComposerDelivery(
			first,
			{
				kind: "steer",
				composerText: "belongs to deleted session",
				attachments: [],
				requestText: "belongs to deleted session",
				clientMessageId: "old-unresolved-delivery",
			},
			storage,
		);
		const oldEdit = prepareChatInlineEditDelivery(
			first,
			{
				turnId: "old-turn",
				text: "old edit",
				content: [],
				clientMessageId: "old-edit-delivery",
			},
			storage,
		);
		expect(oldSend.ok).toBe(true);
		expect(oldEdit.ok).toBe(true);
		expect(beginChatComposerMutation(first)).toBeTypeOf("symbol");
		expect(beginChatInlineEditMutation(first)).toBeTypeOf("symbol");

		expect(activateChatDraftScope(replacement, storage)).toMatchObject({
			ok: true,
			replaced: true,
		});
		expect(writeChatComposerText(replacement, "replacement draft", storage).ok).toBe(true);
		expect(
			writeChatAttachments(
				replacement,
				[
					{
						id: "new-attachment",
						path: ".ao/attachments/new.png",
						name: "new.png",
						mimeType: "image/png",
						bytes: 20,
					},
				],
				storage,
			).ok,
		).toBe(true);
		expect(
			writeChatInlineEdit(
				replacement,
				{ turnId: "new-turn", text: "replacement edit", content: [] },
				storage,
			).ok,
		).toBe(true);

		// These model late FileReader/staging, send, edit, and stale-render callbacks
		// from the deleted incarnation after the replacement already owns storage.
		expect(writeChatComposerText(first, "late old text", storage).ok).toBe(false);
		expect(
			writeChatAttachments(
				first,
				[
					{
						id: "late-old-attachment",
						path: ".ao/attachments/late-old.png",
						name: "late-old.png",
						mimeType: "image/png",
						bytes: 30,
					},
				],
				storage,
			).ok,
		).toBe(false);
		expect(
			markChatComposerDeliveryAccepted(
				first,
				"old-unresolved-delivery",
				oldSend.mutation!.revision,
				storage,
			).ok,
		).toBe(false);
		expect(
			markChatInlineEditDeliveryAccepted(
				first,
				"old-edit-delivery",
				oldEdit.mutation!.revision,
				storage,
			).ok,
		).toBe(false);
		expect(activateChatDraftScope(first, storage)).toMatchObject({
			ok: false,
			reason: "obsolete",
		});

		const restored = readChatSessionDraft(replacement, storage);
		expect(restored).toMatchObject({
			sessionId: replacement.sessionId,
			incarnation: replacement.incarnation,
			composer: {
				text: "replacement draft",
				attachments: [{ id: "new-attachment", path: ".ao/attachments/new.png" }],
			},
			inlineEdit: { turnId: "new-turn", text: "replacement edit" },
		});
		expect(restored.composer.delivery).toBeUndefined();
		expect(getChatComposerMutation(first)).toEqual({ pending: false });
		expect(getChatInlineEditMutation(first)).toEqual({ pending: false });
	});

	it("keeps both incarnations fail closed until an interrupted authoritative purge finishes", () => {
		const backing = new MemoryStorage();
		const first: ChatDraftScope = {
			sessionId: "session-purge",
			incarnation: "2026-08-25T09:00:00.000Z",
		};
		const replacement: ChatDraftScope = {
			sessionId: first.sessionId,
			incarnation: "2026-08-26T09:00:00.000Z",
		};
		expect(activateChatDraftScope(first, backing)).toMatchObject({ ok: true });
		writeChatComposerText(first, "obsolete", backing);
		const blockedRemoval: DraftStorage = {
			getItem: backing.getItem.bind(backing),
			setItem: backing.setItem.bind(backing),
			removeItem: () => {
				throw new DOMException("blocked", "SecurityError");
			},
		};

		expect(activateChatDraftScope(replacement, blockedRemoval)).toMatchObject({
			ok: false,
			reason: "storage",
		});
		expect(writeChatComposerText(first, "late obsolete write", backing).ok).toBe(false);
		expect(writeChatComposerText(replacement, "too early", backing).ok).toBe(false);
		expect(backing.values.get(`ao.chat.draft:${encodeURIComponent(first.sessionId)}`)).toContain(
			"obsolete",
		);

		expect(activateChatDraftScope(replacement, backing)).toMatchObject({
			ok: true,
			replaced: true,
		});
		expect(writeChatComposerText(replacement, "replacement draft", backing).ok).toBe(true);
		expect(readChatSessionDraft(replacement, backing).composer.text).toBe("replacement draft");
		expect(writeChatComposerText(first, "still obsolete", backing).ok).toBe(false);
	});

	it("purges retained receipts when an authoritative incarnation replaces their scope", () => {
		const storage = new MemoryStorage();
		const first: ChatDraftScope = {
			sessionId: "session-retained-receipts",
			incarnation: "2026-08-25T09:00:00.000Z",
		};
		const replacement: ChatDraftScope = {
			sessionId: first.sessionId,
			incarnation: "2026-08-26T09:00:00.000Z",
		};
		expect(activateChatDraftScope(first, storage)).toMatchObject({ ok: true });

		const composerRevision = writeChatComposerText(first, "accepted", storage).draft.composer
			.revision;
		const composerToken = beginChatComposerMutation(first)!;
		finishChatComposerMutation(
			first,
			composerToken,
			composerRevision,
			clearAcceptedChatComposer(first, composerRevision, storage),
		);
		const inlineRevision = writeChatInlineEdit(
			first,
			{ turnId: "turn-1", text: "accepted edit", content: [] },
			storage,
		).draft.inlineEdit!.revision;
		const inlineToken = beginChatInlineEditMutation(first)!;
		finishChatInlineEditMutation(
			first,
			inlineToken,
			inlineRevision,
			clearAcceptedChatInlineEdit(first, inlineRevision, storage),
		);
		expect(getChatComposerMutation(first).accepted).toBeDefined();
		expect(getChatInlineEditMutation(first).accepted).toBeDefined();

		expect(activateChatDraftScope(replacement, storage)).toMatchObject({
			ok: true,
			replaced: true,
			previousIncarnation: first.incarnation,
		});
		expect(getChatComposerMutation(first)).toEqual({ pending: false });
		expect(getChatInlineEditMutation(first)).toEqual({ pending: false });
	});

	it("warned abandon clears only an uncertain steer journal", () => {
		const storage = new MemoryStorage();
		const scope: ChatDraftScope = { sessionId: "session-abandon", incarnation: "one" };
		const attachment = {
			id: "staged",
			path: ".ao/attachments/staged.png",
			name: "staged.png",
			mimeType: "image/png",
			bytes: 10,
		};
		const prepared = prepareChatComposerDelivery(
			scope,
			{
				kind: "steer",
				composerText: "possibly delivered",
				attachments: [attachment],
				requestText: "possibly delivered",
				clientMessageId: "uncertain-steer",
			},
			storage,
		);
		expect(prepared.ok).toBe(true);

		expect(
			clearUncertainChatComposerDelivery(
				scope,
				"uncertain-steer",
				prepared.mutation!.revision,
				storage,
			),
		).toMatchObject({ ok: true });
		expect(readChatSessionDraft(scope, storage).composer).toMatchObject({
			text: "possibly delivered",
			attachments: [attachment],
		});
		expect(readChatSessionDraft(scope, storage).composer.delivery).toBeUndefined();
	});
	it("round-trips independent composer, attachment, and inline-edit state per session", () => {
		const storage = new MemoryStorage();
		writeChatComposerText("session/a", "composer A", storage);
		writeChatAttachments(
			"session/a",
			[
				{
					id: "attachment-a",
					path: ".ao/attachments/attachment-a.png",
					name: "a.png",
					mimeType: "image/png",
					bytes: 4,
				},
			],
			storage,
		);
		writeChatInlineEdit(
			"session/a",
			{
				turnId: "turn-a",
				text: "edit A",
				content: [{ type: "image", name: "a.png" }],
			},
			storage,
		);
		writeChatComposerText("session/b", "composer B", storage);

		const sessionA = readChatSessionDraft("session/a", storage);
		expect(sessionA.composer.text).toBe("composer A");
		expect(sessionA.composer.attachments).toEqual([
			expect.objectContaining({
				id: "attachment-a",
				path: ".ao/attachments/attachment-a.png",
			}),
		]);
		expect(sessionA.inlineEdit).toEqual(expect.objectContaining({ turnId: "turn-a", text: "edit A" }));
		expect(readChatSessionDraft("session/b", storage).composer.text).toBe("composer B");
	});

	it("does not clear a composer revision changed while an accepted send was pending", () => {
		const storage = new MemoryStorage();
		const accepted = writeChatComposerText("session-a", "submitted", storage).draft.composer.revision;
		writeChatComposerText("session-a", "newer input", storage);

		const staleClear = clearAcceptedChatComposer("session-a", accepted, storage);
		expect(staleClear).toMatchObject({ ok: true, cleared: false });
		expect(readChatSessionDraft("session-a", storage).composer.text).toBe("newer input");

		const whitespaceRevision = writeChatComposerText("session-a", "submitted ", storage).draft.composer.revision;
		expect(clearAcceptedChatComposer("session-a", accepted, storage)).toMatchObject({
			ok: true,
			cleared: false,
		});
		expect(readChatSessionDraft("session-a", storage).composer.text).toBe("submitted ");

		expect(clearAcceptedChatComposer("session-a", whitespaceRevision, storage)).toMatchObject({
			ok: true,
			cleared: true,
		});
		expect(readChatSessionDraft("session-a", storage).composer.text).toBe("");
	});

	it("clears inline-edit metadata without touching the independent composer", () => {
		const storage = new MemoryStorage();
		writeChatComposerText("session-a", "keep composer", storage);
		writeChatInlineEdit("session-a", { turnId: "turn-a", text: "discard edit", content: [] }, storage);

		expect(writeChatInlineEdit("session-a", undefined, storage).ok).toBe(true);
		const restored = readChatSessionDraft("session-a", storage);
		expect(restored.inlineEdit).toBeUndefined();
		expect(restored.composer.text).toBe("keep composer");
	});

	it("does not clear a newer inline edit when an older accepted edit completes", () => {
		const storage = new MemoryStorage();
		const accepted = writeChatInlineEdit(
			"session-a",
			{ turnId: "turn-a", text: "submitted edit", content: [] },
			storage,
		).draft.inlineEdit?.revision;
		expect(accepted).toBeTypeOf("string");
		writeChatInlineEdit("session-a", { turnId: "turn-a", text: "newer remounted edit", content: [] }, storage);

		expect(clearAcceptedChatInlineEdit("session-a", accepted!, storage)).toMatchObject({
			ok: true,
			cleared: false,
		});
		expect(readChatSessionDraft("session-a", storage).inlineEdit?.text).toBe("newer remounted edit");
	});

	it("rejects corrupt and foreign-session records", () => {
		const storage = new MemoryStorage();
		const key = `ao.chat.draft:${encodeURIComponent("session-a")}`;
		storage.setItem(key, "not json");
		expect(readChatSessionDraft("session-a", storage).composer.text).toBe("");
		expect(writeChatComposerText("session-a", "recovered", storage).ok).toBe(true);
		expect(readChatSessionDraft("session-a", storage).composer.text).toBe("recovered");

		storage.setItem(
			key,
			JSON.stringify({
				schemaVersion: 1,
				sessionId: "session-b",
				composer: { revision: 1, text: "leak", attachments: [] },
			}),
		);
		expect(readChatSessionDraft("session-a", storage).composer.text).toBe("");
	});

	it("reports quota and removal failures instead of silently claiming durability", () => {
		const unreadableStorage: DraftStorage = {
			getItem: () => {
				throw new DOMException("blocked", "SecurityError");
			},
			setItem: () => undefined,
			removeItem: () => undefined,
		};
		expect(writeChatAttachments("session-a", [], unreadableStorage).ok).toBe(false);
		expect(clearAcceptedChatComposer("session-a", 0, unreadableStorage)).toMatchObject({
			ok: false,
			cleared: false,
		});

		const quotaStorage: DraftStorage = {
			getItem: () => null,
			setItem: () => {
				throw new DOMException("full", "QuotaExceededError");
			},
			removeItem: () => undefined,
		};
		expect(writeChatComposerText("session-a", "not durable", quotaStorage).ok).toBe(false);

		const backing = new MemoryStorage();
		const accepted = writeChatComposerText("session-a", "accepted", backing).draft.composer.revision;
		const removalFailure: DraftStorage = {
			getItem: (key) => backing.getItem(key),
			setItem: (key, value) => backing.setItem(key, value),
			removeItem: () => {
				throw new DOMException("blocked", "SecurityError");
			},
		};
		expect(clearAcceptedChatComposer("session-a", accepted, removalFailure)).toMatchObject({
			ok: false,
			cleared: false,
		});
		expect(readChatSessionDraft("session-a", backing).composer.text).toBe("accepted");
	});

	it("proves the exact composer and attachment draft before creating a durable delivery", () => {
		const storage = new MemoryStorage();
		const attachments = [
			{
				id: "attachment-a",
				path: ".ao/attachments/attachment-a.png",
				name: "a.png",
				mimeType: "image/png",
				bytes: 4,
			},
		];
		const prepared = prepareChatComposerDelivery(
			"session-proof",
			{
				kind: "send",
				composerText: "  durable prompt  ",
				attachments,
				requestText: "durable prompt\n\nAttached files:\n- .ao/attachments/attachment-a.png",
				clientMessageId: "delivery-proof-1",
			},
			storage,
		);

		expect(prepared).toMatchObject({
			ok: true,
			mutation: {
				kind: "send",
				state: "dispatching",
				clientMessageId: "delivery-proof-1",
				requestText: "durable prompt\n\nAttached files:\n- .ao/attachments/attachment-a.png",
			},
		});
		const restored = readChatSessionDraft("session-proof", storage);
		expect(restored.composer.text).toBe("  durable prompt  ");
		expect(restored.composer.attachments).toEqual(attachments);
		expect(restored.composer.delivery?.revision).toBe(restored.composer.revision);

		const retry = prepareChatComposerDelivery(
			"session-proof",
			{
				kind: "send",
				composerText: "ignored after reload",
				attachments: [],
				requestText: "ignored after reload",
				clientMessageId: "must-not-replace-the-durable-id",
			},
			storage,
		);
		expect(retry).toMatchObject({
			ok: true,
			recovered: true,
			mutation: { clientMessageId: "delivery-proof-1" },
		});
	});

	it("re-proves a restored delivery immediately before allowing a retry", () => {
		const backing = new MemoryStorage();
		prepareChatComposerDelivery(
			"session-restored-proof",
			{
				kind: "send",
				composerText: "restore me",
				attachments: [],
				requestText: "restore me",
				clientMessageId: "restored-proof-1",
			},
			backing,
		);
		const writeFailure: DraftStorage = {
			getItem: (key) => backing.getItem(key),
			setItem: () => {
				throw new DOMException("full", "QuotaExceededError");
			},
			removeItem: (key) => backing.removeItem(key),
		};

		expect(
			prepareChatComposerDelivery(
				"session-restored-proof",
				{
					kind: "send",
					composerText: "restore me",
					attachments: [],
					requestText: "restore me",
					clientMessageId: "must-still-reuse-restored-proof-1",
				},
				writeFailure,
			),
		).toMatchObject({ ok: false, recovered: true });
	});

	it("refuses dispatch when storage cannot prove the exact composer write", () => {
		const silentlyDroppingStorage: DraftStorage = {
			getItem: () => null,
			setItem: () => undefined,
			removeItem: () => undefined,
		};

		expect(
			prepareChatComposerDelivery(
				"session-unproven",
				{
					kind: "steer",
					composerText: "do not dispatch",
					attachments: [],
					requestText: "do not dispatch",
					clientMessageId: "unproven-1",
				},
				silentlyDroppingStorage,
			),
		).toMatchObject({ ok: false });
	});

	it("keeps an accepted delivery durable when clearing local storage fails", () => {
		const backing = new MemoryStorage();
		const prepared = prepareChatComposerDelivery(
			"session-accepted",
			{
				kind: "send",
				composerText: "accepted once",
				attachments: [],
				requestText: "accepted once",
				clientMessageId: "accepted-1",
			},
			backing,
		);
		expect(prepared.ok).toBe(true);
		expect(
			markChatComposerDeliveryAccepted(
				"session-accepted",
				"accepted-1",
				prepared.mutation!.revision,
				backing,
			).ok,
		).toBe(true);
		const removalFailure: DraftStorage = {
			getItem: (key) => backing.getItem(key),
			setItem: (key, value) => backing.setItem(key, value),
			removeItem: () => {
				throw new DOMException("blocked", "SecurityError");
			},
		};

		expect(
			clearAcceptedChatComposer(
				"session-accepted",
				prepared.mutation!.revision,
				removalFailure,
			),
		).toMatchObject({ ok: false, cleared: false });
		expect(readChatSessionDraft("session-accepted", backing).composer.delivery).toMatchObject({
			state: "accepted",
			clientMessageId: "accepted-1",
		});
	});

	it("clears only the accepted delivery journal when a later composer revision exists", () => {
		const storage = new MemoryStorage();
		const prepared = prepareChatComposerDelivery(
			"session-later-composer",
			{
				kind: "send",
				composerText: "submitted",
				attachments: [],
				requestText: "submitted",
				clientMessageId: "submitted-1",
			},
			storage,
		);
		expect(prepared.ok).toBe(true);
		writeChatComposerText("session-later-composer", "newer draft", storage);

		expect(
			clearAcceptedChatComposer(
				"session-later-composer",
				prepared.mutation!.revision,
				storage,
			),
		).toMatchObject({ ok: true, cleared: false });
		const restored = readChatSessionDraft("session-later-composer", storage);
		expect(restored.composer.text).toBe("newer draft");
		expect(restored.composer.delivery).toBeUndefined();
	});

	it("clears only a refused steer journal while preserving a later composer revision", () => {
		const storage = new MemoryStorage();
		const prepared = prepareChatComposerDelivery(
			"session-refused-later-composer",
			{
				kind: "steer",
				composerText: "submitted steer",
				attachments: [],
				requestText: "submitted steer",
				clientMessageId: "refused-steer-1",
			},
			storage,
		);
		expect(prepared.ok).toBe(true);
		writeChatComposerText("session-refused-later-composer", "newer unsent draft", storage);

		expect(
			clearRejectedChatComposerDelivery(
				"session-refused-later-composer",
				"refused-steer-1",
				prepared.mutation!.revision,
				storage,
			),
		).toMatchObject({ ok: true });
		const restored = readChatSessionDraft("session-refused-later-composer", storage);
		expect(restored.composer.text).toBe("newer unsent draft");
		expect(restored.composer.delivery).toBeUndefined();
	});

	it("persists and accepts an inline-edit delivery with one stable client id", () => {
		const storage = new MemoryStorage();
		const prepared = prepareChatInlineEditDelivery(
			"session-edit-proof",
			{
				turnId: "turn-1",
				text: "durable edit",
				content: [],
				clientMessageId: "edit-proof-1",
			},
			storage,
		);

		expect(prepared).toMatchObject({
			ok: true,
			mutation: {
				state: "dispatching",
				turnId: "turn-1",
				requestText: "durable edit",
				clientMessageId: "edit-proof-1",
			},
		});
		expect(
			markChatInlineEditDeliveryAccepted(
				"session-edit-proof",
				"edit-proof-1",
				prepared.mutation!.revision,
				storage,
			),
		).toMatchObject({ ok: true });
		expect(readChatSessionDraft("session-edit-proof", storage).inlineEditDelivery?.state).toBe(
			"accepted",
		);
	});

	it("clears only a definitively rejected inline-edit journal", () => {
		const storage = new MemoryStorage();
		const prepared = prepareChatInlineEditDelivery(
			"session-edit-rejected",
			{
				turnId: "turn-1",
				text: "keep rejected edit",
				content: [],
				clientMessageId: "edit-rejected-1",
			},
			storage,
		);
		expect(prepared.ok).toBe(true);

		expect(
			clearRejectedChatInlineEditDelivery(
				"session-edit-rejected",
				"edit-rejected-1",
				prepared.mutation!.revision,
				storage,
			),
		).toMatchObject({ ok: true });
		const restored = readChatSessionDraft("session-edit-rejected", storage);
		expect(restored.inlineEdit?.text).toBe("keep rejected edit");
		expect(restored.inlineEditDelivery).toBeUndefined();
	});

	it("explicitly abandons only an uncertain inline-edit journal", () => {
		const storage = new MemoryStorage();
		const prepared = prepareChatInlineEditDelivery(
			"session-edit-uncertain",
			{
				turnId: "turn-1",
				text: "possibly delivered edit",
				content: [{ type: "image", name: "preserved.png" }],
				clientMessageId: "edit-uncertain-1",
			},
			storage,
		);
		expect(prepared.ok).toBe(true);

		expect(
			clearUncertainChatInlineEditDelivery(
				"session-edit-uncertain",
				"edit-uncertain-1",
				prepared.mutation!.revision,
				storage,
			),
		).toMatchObject({ ok: true });
		const restored = readChatSessionDraft("session-edit-uncertain", storage);
		expect(restored.inlineEdit).toMatchObject({
			turnId: "turn-1",
			text: "possibly delivered edit",
			content: [{ type: "image", name: "preserved.png" }],
		});
		expect(restored.inlineEditDelivery).toBeUndefined();
	});

	it("clears only the accepted delivery journal when a later inline edit exists", () => {
		const storage = new MemoryStorage();
		const prepared = prepareChatInlineEditDelivery(
			"session-later-edit",
			{
				turnId: "turn-1",
				text: "submitted edit",
				content: [],
				clientMessageId: "submitted-edit-1",
			},
			storage,
		);
		expect(prepared.ok).toBe(true);
		writeChatInlineEdit(
			"session-later-edit",
			{ turnId: "turn-1", text: "newer edit", content: [] },
			storage,
		);

		expect(
			clearAcceptedChatInlineEdit(
				"session-later-edit",
				prepared.mutation!.revision,
				storage,
			),
		).toMatchObject({ ok: true, cleared: false });
		const restored = readChatSessionDraft("session-later-edit", storage);
		expect(restored.inlineEdit?.text).toBe("newer edit");
		expect(restored.inlineEditDelivery).toBeUndefined();
	});

	it("keeps only in-flight mutation ownership across remounts and evicts it once settled", () => {
		const storage = new MemoryStorage();
		const sessionId = "runtime-session-a";
		const revision = writeChatComposerText(sessionId, "send once", storage).draft.composer.revision;
		const unsubscribeFirstMount = subscribeChatDraftRuntime(sessionId, () => undefined);
		const token = beginChatComposerMutation(sessionId);
		expect(token).toBeTypeOf("symbol");
		expect(beginChatComposerMutation(sessionId)).toBeUndefined();
		expect(getChatComposerMutation(sessionId).pending).toBe(true);
		expect(getChatComposerMutation("runtime-session-b").pending).toBe(false);

		unsubscribeFirstMount();
		expect(getChatComposerMutation(sessionId).pending).toBe(true);
		const unsubscribeRemount = subscribeChatDraftRuntime(sessionId, () => undefined);
		cancelChatComposerMutation(sessionId, Symbol("stale"));
		expect(getChatComposerMutation(sessionId).pending).toBe(true);
		const result = clearAcceptedChatComposer(sessionId, revision, storage);
		finishChatComposerMutation(sessionId, token!, revision, result);
		expect(getChatComposerMutation(sessionId)).toMatchObject({
			pending: false,
			accepted: { revision, result: { ok: true, cleared: true } },
		});

		const inlineToken = beginChatInlineEditMutation(sessionId);
		expect(inlineToken).toBeTypeOf("symbol");
		expect(getChatInlineEditMutation(sessionId).pending).toBe(true);
		cancelChatInlineEditMutation(sessionId, inlineToken!);
		expect(getChatInlineEditMutation(sessionId)).toEqual({ pending: false });

		acknowledgeChatComposerMutation(
			sessionId,
			getChatComposerMutation(sessionId).accepted!.sequence,
		);
		unsubscribeRemount();
		expect(getChatComposerMutation(sessionId)).toEqual({ pending: false });
		expect(getChatInlineEditMutation(sessionId)).toEqual({ pending: false });
	});

	it("keeps an accepted send when the old passive cleanup runs before the replacement subscribes", () => {
		const storage = new MemoryStorage();
		const sessionId = "runtime-old-cleanup-before-new-subscribe";
		const revision = writeChatComposerText(sessionId, "send exactly once", storage).draft
			.composer.revision;
		const unsubscribeFirstMount = subscribeChatDraftRuntime(sessionId, () => undefined);
		const token = beginChatComposerMutation(sessionId)!;

		// React has rendered the replacement from this in-flight snapshot, but its
		// passive subscription is not installed yet. The request settles while the
		// old subscription still owns the runtime.
		expect(getChatComposerMutation(sessionId)).toEqual({ pending: true });
		const result = clearAcceptedChatComposer(sessionId, revision, storage);
		finishChatComposerMutation(sessionId, token, revision, result);
		expect(getChatComposerMutation(sessionId).accepted?.result).toBe(result);

		// Passive unmount cleanup precedes passive mount subscription. It must not
		// discard the receipt that reconciles the replacement's stale render.
		unsubscribeFirstMount();
		const unsubscribeRemount = subscribeChatDraftRuntime(sessionId, () => undefined);
		expect(getChatComposerMutation(sessionId).accepted?.result).toBe(result);
		acknowledgeChatComposerMutation(
			sessionId,
			getChatComposerMutation(sessionId).accepted!.sequence,
		);
		expect(getChatComposerMutation(sessionId)).toEqual({ pending: false });
		unsubscribeRemount();
	});

	it("keeps an accepted send when a task elapses between replacement render and subscribe", () => {
		vi.useFakeTimers();
		try {
			const storage = new MemoryStorage();
			const sessionId = "runtime-task-between-render-and-subscribe";
			const revision = writeChatComposerText(sessionId, "accepted", storage).draft.composer
				.revision;
			const token = beginChatComposerMutation(sessionId)!;
			const result = clearAcceptedChatComposer(sessionId, revision, storage);
			finishChatComposerMutation(sessionId, token, revision, result);

			// useSyncExternalStore reads during render, then React may yield to other
			// tasks for an arbitrary duration before installing the subscription.
			expect(getChatComposerMutation(sessionId).accepted?.result).toBe(result);
			vi.runOnlyPendingTimers();
			const unsubscribeRemount = subscribeChatDraftRuntime(sessionId, () => undefined);
			expect(getChatComposerMutation(sessionId).accepted?.result).toBe(result);
			acknowledgeChatComposerMutation(
				sessionId,
				getChatComposerMutation(sessionId).accepted!.sequence,
			);
			expect(getChatComposerMutation(sessionId)).toEqual({ pending: false });
			unsubscribeRemount();
		} finally {
			vi.useRealTimers();
		}
	});

	it("does not let a stale acknowledgement clear a newer accepted send", () => {
		const storage = new MemoryStorage();
		const sessionId = "runtime-stale-acknowledgement";
		const firstRevision = writeChatComposerText(sessionId, "first send", storage).draft.composer
			.revision;
		const firstToken = beginChatComposerMutation(sessionId)!;
		finishChatComposerMutation(
			sessionId,
			firstToken,
			firstRevision,
			clearAcceptedChatComposer(sessionId, firstRevision, storage),
		);
		const firstSequence = getChatComposerMutation(sessionId).accepted!.sequence;
		expect(beginChatComposerMutation(sessionId)).toBeUndefined();

		const secondRevision = writeChatComposerText(sessionId, "second send", storage).draft.composer
			.revision;
		const secondToken = beginChatComposerMutation(sessionId)!;
		finishChatComposerMutation(
			sessionId,
			secondToken,
			secondRevision,
			clearAcceptedChatComposer(sessionId, secondRevision, storage),
		);
		const secondReceipt = getChatComposerMutation(sessionId).accepted!;
		expect(secondReceipt.sequence).toBeGreaterThan(firstSequence);

		acknowledgeChatComposerMutation(sessionId, firstSequence);
		expect(getChatComposerMutation(sessionId).accepted).toBe(secondReceipt);
		acknowledgeChatComposerMutation(sessionId, secondReceipt.sequence);
		expect(getChatComposerMutation(sessionId)).toEqual({ pending: false });
	});

	it("keeps more than 64 delayed composer and inline handoffs until each replacement acknowledges", () => {
		const storage = new MemoryStorage();
		const sessionIds = Array.from(
			{ length: 65 },
			(_, index) => `runtime-delayed-handoff-${index}`,
		);
		for (const sessionId of sessionIds) {
			const composerRevision = writeChatComposerText(sessionId, "accepted", storage).draft.composer
				.revision;
			const inlineRevision = writeChatInlineEdit(
				sessionId,
				{ turnId: "turn-1", text: "accepted edit", content: [] },
				storage,
			).draft.inlineEdit!.revision;
			const unsubscribeOldSurface = subscribeChatDraftRuntime(sessionId, () => undefined);
			const composerToken = beginChatComposerMutation(sessionId)!;
			const inlineToken = beginChatInlineEditMutation(sessionId)!;
			// The replacement has rendered from the in-flight snapshot, but its passive
			// subscription is deliberately delayed until every request below settles.
			expect(getChatComposerMutation(sessionId)).toEqual({ pending: true });
			expect(getChatInlineEditMutation(sessionId)).toEqual({ pending: true });
			finishChatComposerMutation(
				sessionId,
				composerToken,
				composerRevision,
				clearAcceptedChatComposer(sessionId, composerRevision, storage),
			);
			finishChatInlineEditMutation(
				sessionId,
				inlineToken,
				inlineRevision,
				clearAcceptedChatInlineEdit(sessionId, inlineRevision, storage),
			);
			unsubscribeOldSurface();
		}

		for (const sessionId of sessionIds) {
			const unsubscribeReplacement = subscribeChatDraftRuntime(sessionId, () => undefined);
			const acceptedComposer = getChatComposerMutation(sessionId).accepted;
			const acceptedInline = getChatInlineEditMutation(sessionId).accepted;
			expect(acceptedComposer).toBeDefined();
			expect(acceptedInline).toBeDefined();
			acknowledgeChatComposerMutation(sessionId, acceptedComposer!.sequence);
			expect(getChatComposerMutation(sessionId)).toEqual({ pending: false });
			expect(getChatInlineEditMutation(sessionId).accepted).toBe(acceptedInline);
			acknowledgeChatInlineEditMutation(sessionId, acceptedInline!.sequence);
			expect(getChatInlineEditMutation(sessionId)).toEqual({ pending: false });
			unsubscribeReplacement();

			const unsubscribeLaterMount = subscribeChatDraftRuntime(sessionId, () => undefined);
			expect(getChatComposerMutation(sessionId)).toEqual({ pending: false });
			expect(getChatInlineEditMutation(sessionId)).toEqual({ pending: false });
			unsubscribeLaterMount();
		}
	});

	it("evicts an acknowledged failed-clear receipt after the final subscriber leaves", () => {
		const backing = new MemoryStorage();
		const sessionId = "runtime-failed-clear";
		const revision = writeChatComposerText(sessionId, "accepted but still durable", backing).draft
			.composer.revision;
		const removalFailure: DraftStorage = {
			getItem: backing.getItem.bind(backing),
			setItem: backing.setItem.bind(backing),
			removeItem: () => {
				throw new DOMException("blocked", "SecurityError");
			},
		};
		const unsubscribe = subscribeChatDraftRuntime(sessionId, () => undefined);
		const token = beginChatComposerMutation(sessionId);
		const result = clearAcceptedChatComposer(sessionId, revision, removalFailure);
		expect(result).toMatchObject({
			ok: false,
			cleared: false,
			draft: { composer: { text: "accepted but still durable" } },
		});
		finishChatComposerMutation(sessionId, token!, revision, result);
		expect(getChatComposerMutation(sessionId).accepted?.result).toBe(result);

		acknowledgeChatComposerMutation(
			sessionId,
			getChatComposerMutation(sessionId).accepted!.sequence,
		);
		unsubscribe();
		expect(getChatComposerMutation(sessionId)).toEqual({ pending: false });
	});

	it("waits for every mutation receipt to be acknowledged before evicting an unmounted runtime", () => {
		const storage = new MemoryStorage();
		const sessionId = "runtime-two-pending-mutations";
		const composerRevision = writeChatComposerText(sessionId, "send once", storage).draft
			.composer.revision;
		const inlineRevision = writeChatInlineEdit(
			sessionId,
			{ turnId: "turn-1", text: "edit once", content: [] },
			storage,
		).draft.inlineEdit!.revision;
		const unsubscribe = subscribeChatDraftRuntime(sessionId, () => undefined);
		const composerToken = beginChatComposerMutation(sessionId)!;
		const inlineToken = beginChatInlineEditMutation(sessionId)!;
		unsubscribe();

		finishChatComposerMutation(
			sessionId,
			composerToken,
			composerRevision,
			clearAcceptedChatComposer(sessionId, composerRevision, storage),
		);
		expect(getChatComposerMutation(sessionId).accepted?.revision).toBe(composerRevision);
		expect(getChatInlineEditMutation(sessionId).pending).toBe(true);

		finishChatInlineEditMutation(
			sessionId,
			inlineToken,
			inlineRevision,
			clearAcceptedChatInlineEdit(sessionId, inlineRevision, storage),
		);
		expect(getChatComposerMutation(sessionId).accepted?.revision).toBe(composerRevision);
		expect(getChatInlineEditMutation(sessionId).accepted?.revision).toBe(inlineRevision);

		acknowledgeChatComposerMutation(
			sessionId,
			getChatComposerMutation(sessionId).accepted!.sequence,
		);
		expect(getChatComposerMutation(sessionId)).toEqual({ pending: false });
		expect(getChatInlineEditMutation(sessionId).accepted?.revision).toBe(inlineRevision);
		acknowledgeChatInlineEditMutation(
			sessionId,
			getChatInlineEditMutation(sessionId).accepted!.sequence,
		);
		expect(getChatComposerMutation(sessionId)).toEqual({ pending: false });
		expect(getChatInlineEditMutation(sessionId)).toEqual({ pending: false });
	});

	it("does not let a deleted runtime's late unsubscribe evict a later in-flight owner", () => {
		const storage = new MemoryStorage();
		const deleted: ChatDraftScope = {
			sessionId: "runtime-recreated-session",
			incarnation: "2026-08-25T09:00:00.000Z",
		};
		const replacement: ChatDraftScope = {
			sessionId: deleted.sessionId,
			incarnation: "2026-08-26T09:00:00.000Z",
		};
		expect(activateChatDraftScope(deleted, storage)).toMatchObject({ ok: true });
		const unsubscribeDeletedRuntime = subscribeChatDraftRuntime(deleted, () => undefined);
		expect(activateChatDraftScope(replacement, storage)).toMatchObject({
			ok: true,
			replaced: true,
		});

		const laterToken = beginChatComposerMutation(deleted);
		expect(laterToken).toBeTypeOf("symbol");
		unsubscribeDeletedRuntime();
		expect(getChatComposerMutation(deleted).pending).toBe(true);

		cancelChatComposerMutation(deleted, laterToken!);
		expect(getChatComposerMutation(deleted)).toEqual({ pending: false });
	});

	it("does not revise a draft for equivalent attachment metadata", () => {
		const storage = new MemoryStorage();
		const attachment = {
			id: "attachment-equivalent",
			path: ".ao/attachments/equivalent.png",
			name: "equivalent.png",
			mimeType: "image/png",
			bytes: 42,
		};
		const first = writeChatAttachments("attachment-equivalence", [attachment], storage);
		const equivalent = writeChatAttachments(
			"attachment-equivalence",
			[{ ...attachment }],
			storage,
		);

		expect(equivalent.ok).toBe(true);
		expect(equivalent.draft.composer.revision).toBe(first.draft.composer.revision);
	});
});
