import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { chatFixtureEmpty } from "../lib/chat-fixture";

const { patch } = vi.hoisted(() => ({ patch: vi.fn() }));
vi.mock("../lib/api-client", () => ({
	apiClient: { PATCH: patch },
	apiErrorMessage: () => "Failed", apiErrorCode: () => undefined,
}));
import { conversationQueryKey, useConversationCommands } from "./useConversation";

describe("confirmed conversation permissions", () => {
	it("updates the confirmed policy before enabling controls, without waiting on a slow snapshot refresh", async () => {
		let finish!: (value: unknown) => void;
		patch.mockReturnValue(new Promise((resolve) => { finish = resolve; }));
		const client = new QueryClient();
		const snapshot = { ...chatFixtureEmpty, sessionId: "session-a", settings: { approvalMode: "default" } };
		client.setQueryData(conversationQueryKey("session-a"), { pages: [snapshot], pageParams: [undefined] });
		vi.spyOn(client, "invalidateQueries").mockReturnValue(new Promise(() => {}));
		const wrapper = ({ children }: { children: ReactNode }) =>
			<QueryClientProvider client={client}>{children}</QueryClientProvider>;
		const { result, rerender } = renderHook(({ id }) => useConversationCommands(id), { wrapper, initialProps: { id: "session-a" } });
		act(() => result.current.chooseSettings({ approvalMode: "auto" }));
		await waitFor(() => expect(result.current.choosingSettings).toBe(true));
		// Navigation during the write must not update the newly opened session.
		rerender({ id: "session-b" });
		expect(result.current.choosingSettings).toBe(false);
		rerender({ id: "session-a" });
		expect(result.current.choosingSettings).toBe(true);
		rerender({ id: "session-b" });
		await act(async () => { finish({ data: { approvalMode: "auto" } }); });
		await waitFor(() => expect(result.current.choosingSettings).toBe(false));
		expect(client.getQueryData(conversationQueryKey("session-a"))).toMatchObject({
			pages: [{ settings: { approvalMode: "auto" } }],
		});
		expect(client.getQueryData(conversationQueryKey("session-b"))).toBeUndefined();
	});
});
