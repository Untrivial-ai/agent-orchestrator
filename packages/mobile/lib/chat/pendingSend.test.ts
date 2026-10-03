import { beforeEach, describe, expect, it, vi } from "vitest";

const saved = new Map<string, string>();
vi.mock("@react-native-async-storage/async-storage", () => ({
	default: {
		getItem: vi.fn(async (key: string) => saved.get(key) ?? null),
		setItem: vi.fn(async (key: string, value: string) => { saved.set(key, value); }),
		removeItem: vi.fn(async (key: string) => { saved.delete(key); }),
	},
}));

import { afterDraftWrites, clearPendingSend, needsAttachmentRecovery, pendingSendKey, readPendingSend, reservePendingSend } from "./pendingSend";

beforeEach(() => saved.clear());

describe("pending Chat send", () => {
	it("reuses a host-scoped steer ID after acceptance, response loss, and app relaunch", async () => {
		const key = pendingSendKey("host-A", "session-1");
		const accepted = new Set<string>();
		const postSteer = async (candidateId: string) => {
			const pending = await reservePendingSend(key, { id: candidateId, draftText: "Change course", text: "Change course", hasAttachments: false, kind: "steer" });
			if (!accepted.has(pending.id)) {
				accepted.add(pending.id);
				throw new Error("Response lost after the provider accepted guidance");
			}
			return pending;
		};
		await expect(postSteer("first-id")).rejects.toThrow("Response lost");
		const afterRelaunch = await postSteer("new-id");
		expect(afterRelaunch.id).toBe("first-id");
		expect(accepted.size).toBe(1);
		expect(await readPendingSend(key)).toEqual(afterRelaunch);
		expect(await readPendingSend(pendingSendKey("host-B", "session-1"))).toBeNull();
		await expect(reservePendingSend(key, { id: "send-id", draftText: "Change course", text: "Change course", hasAttachments: false, kind: "send" })).rejects.toThrow("previous message");
	});

	it("reuses the accepted message ID after the response was lost and the screen remounted", async () => {
		const key = "ao.chat.pending.host-A.session-1";
		const accepted = new Set<string>();
		const post = async (id: string) => {
			const pending = await reservePendingSend(key, { id, draftText: "Fix it", text: "Fix it", hasAttachments: false });
			if (!accepted.has(pending.id)) {
				accepted.add(pending.id);
				throw new Error("Response lost after the daemon accepted the turn");
			}
			return pending;
		};
		await expect(post("accepted")).rejects.toThrow("Response lost");
		// A new hook instance generates a new candidate ID after app relaunch.
		const afterRelaunch = await post("new-id");
		expect(afterRelaunch.id).toBe("accepted");
		expect(accepted.size).toBe(1);
		expect(await readPendingSend(key)).toEqual(afterRelaunch);
	});

	it("blocks a different draft until the uncertain message is resolved", async () => {
		const key = "ao.chat.pending.host-A.session-1";
		await reservePendingSend(key, { id: "first", draftText: "Fix A", text: "Fix A", hasAttachments: false });
		await expect(reservePendingSend(key, { id: "second", draftText: "Fix B", text: "Fix B", hasAttachments: false })).rejects.toThrow("previous message");
		await clearPendingSend(key, "first");
		expect((await reservePendingSend(key, { id: "second", draftText: "Fix B", text: "Fix B", hasAttachments: false })).id).toBe("second");
	});

	it("never reuses an uncertain ID with newly selected attachments", async () => {
		const key = "ao.chat.pending.host-A.session-1";
		await reservePendingSend(key, { id: "first", draftText: "Fix it", text: "Fix it", hasAttachments: false });
		await expect(reservePendingSend(key, { id: "second", draftText: "Fix it", text: "Fix it with photo B", hasAttachments: true })).rejects.toThrow("new attachments");
		await clearPendingSend(key, "first");
		await reservePendingSend(key, { id: "photo-A", draftText: "Fix it", text: "Fix it with photo A", hasAttachments: true });
		await expect(reservePendingSend(key, { id: "photo-B", draftText: "Fix it", text: "Fix it with photo B", hasAttachments: true })).rejects.toThrow("new attachments");
		// Without the original bytes, only a read-only recovery of photo A is safe.
		expect((await reservePendingSend(key, { id: "recover", draftText: "Fix it", text: "Fix it", hasAttachments: false })).id).toBe("photo-A");
	});

	it("scopes the retry record to both host and conversation", () => {
		expect(pendingSendKey("host-A", "session-1")).not.toBe(pendingSendKey("host-B", "session-1"));
		expect(pendingSendKey("host-A", "session-1")).not.toBe(pendingSendKey("host-A", "session-2"));
		expect(pendingSendKey("a.b", "c")).not.toBe(pendingSendKey("a", "b.c"));
	});

	it("checks a restored attachment send before any text-only POST", () => {
		expect(needsAttachmentRecovery(true, true, false)).toBe(true);
		expect(needsAttachmentRecovery(true, true, true)).toBe(false);
		expect(needsAttachmentRecovery(true, false, false)).toBe(false);
	});

	it("waits for an in-flight draft save before clearing the accepted draft", async () => {
		let draft: string | null = null;
		let completeOld!: () => void;
		void afterDraftWrites("draft-cross-mount", () => new Promise<void>((resolve) => {
			completeOld = () => { draft = "already accepted"; resolve(); };
		}));
		const clearAfterAcceptance = afterDraftWrites("draft-cross-mount", async () => { draft = null; });
		await vi.waitFor(() => expect(completeOld).toBeTypeOf("function"));
		completeOld();
		await clearAfterAcceptance;
		expect(draft).toBeNull();
	});
});
