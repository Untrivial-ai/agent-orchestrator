import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const { getMock, patchMock, postMock, apiErrorCodeMock, apiErrorMessageMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	patchMock: vi.fn(),
	postMock: vi.fn(),
	apiErrorCodeMock: vi.fn(),
	apiErrorMessageMock: vi.fn(),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock, PATCH: patchMock },
	apiErrorCode: apiErrorCodeMock,
	apiErrorMessage: apiErrorMessageMock,
}));

import {
	clearConversationProviderCatalogs,
	conversationConfigOptionsQueryKey,
	useConversation,
	useConversationCommands,
	useConversationConfigOptions,
} from "./useConversation";
import { workspaceQueryKey } from "./useWorkspaceQuery";

function wrapper({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

function deferred<T>() {
	let resolve!: (value: T) => void;
	const promise = new Promise<T>((resolvePromise) => {
		resolve = resolvePromise;
	});
	return { promise, resolve };
}

/** The provider state the daemon now serves, in wire shape. */
const WIRE = {
	conversationId: "conv-1",
	sessionId: "ao-1",
	harness: "codex",
	mode: "chat",
	controller: "ready",
	latestSequence: 3,
	settings: { model: "gpt-5.6-terra" },
	turns: [
		{
			id: "turn-1",
			state: "running",
			requestedAt: "2026-08-03T00:00:00Z",
			plan: {
				explanation: "why",
				steps: [
					{ text: "one", status: "completed" },
					{ text: "two", status: "in_progress" },
				],
			},
		},
	],
	messages: [],
	activities: [],
	modelReroute: {
		fromModel: "gpt-5.6-terra",
		toModel: "gpt-5.6-terra-mini",
		reason: "at capacity",
		providerTurnId: "prov-1",
		at: "2026-08-03T00:00:01Z",
	},
	account: {
		authMode: "chatgpt",
		planLabel: "Pro",
		reauthRequiredAt: "2026-08-03T00:00:02Z",
		reauthReason: "expired",
	},
	threadState: { status: "system_error", waitingOn: ["user_input"] },
	mcpServers: [
		{ name: "github", status: "ready" },
		{ name: "playwright", status: "failed", error: "boom", failureReason: "startup_timeout" },
	],
};

beforeEach(() => {
	getMock.mockReset();
	patchMock.mockReset();
	postMock.mockReset();
	apiErrorCodeMock.mockReset().mockReturnValue(undefined);
	apiErrorMessageMock.mockReset().mockReturnValue("failed");
});

describe("accepted conversation sends", () => {
	it("keeps each accepted turn attached to the session that initiated it", async () => {
		const firstResponse = deferred<{
			data: { turnId: string };
			error: undefined;
		}>();
		postMock.mockImplementation(
			(_path: string, request: { params: { path: { sessionId: string } } }) =>
				request.params.path.sessionId === "ao-1"
					? firstResponse.promise
					: Promise.resolve({ data: { turnId: "turn-2" }, error: undefined }),
		);
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const { result, rerender } = renderHook(
			({ sessionId }) => useConversationCommands(sessionId),
			{ initialProps: { sessionId: "ao-1" }, wrapper: HookWrapper },
		);

		let firstSend!: Promise<unknown>;
		act(() => {
			firstSend = result.current.send("first session work");
		});
		rerender({ sessionId: "ao-2" });
		await act(async () => {
			await result.current.send("second session work");
		});
		firstResponse.resolve({ data: { turnId: "turn-1" }, error: undefined });
		await act(async () => {
			await firstSend;
		});

		expect(result.current.pendingAcceptedTurnId).toBe("turn-2");
		rerender({ sessionId: "ao-1" });
		expect(result.current.pendingAcceptedTurnId).toBe("turn-1");
	});

	it("retains an in-flight send when Chat unmounts before the response", async () => {
		const response = deferred<{
			data: { turnId: string };
			error: undefined;
		}>();
		postMock.mockReturnValue(response.promise);
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const firstMount = renderHook(() => useConversationCommands("ao-in-flight-remount"), {
			wrapper: HookWrapper,
		});

		let sendRequest!: Promise<unknown>;
		act(() => {
			sendRequest = firstMount.result.current.send("still posting");
		});
		await waitFor(() => {
			expect(firstMount.result.current.busy).toBe(true);
		});
		firstMount.unmount();

		const secondMount = renderHook(() => useConversationCommands("ao-in-flight-remount"), {
			wrapper: HookWrapper,
		});
		expect(secondMount.result.current.busy).toBe(true);
		expect(secondMount.result.current.pendingAcceptedTurnId).toBeUndefined();

		response.resolve({ data: { turnId: "turn-after-deferred-response" }, error: undefined });
		await act(async () => {
			await sendRequest;
		});
		await waitFor(() => {
			expect(secondMount.result.current.pendingAcceptedTurnId).toBe(
				"turn-after-deferred-response",
			);
		});
	});

	it("clears an in-flight send sentinel when the request fails", async () => {
		postMock.mockResolvedValue({
			data: undefined,
			error: { code: "CHAT_SEND_FAILED" },
		});
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const { result } = renderHook(() => useConversationCommands("ao-send-failure"), {
			wrapper: HookWrapper,
		});

		await act(async () => {
			await result.current.send("this will fail").catch(() => {});
		});

		await waitFor(() => {
			expect(result.current.pendingAcceptedTurnId).toBeUndefined();
			expect(result.current.busy).toBe(false);
		});
	});

	it("clears an in-flight sentinel when the daemon confirms a duplicate without a turn id", async () => {
		postMock.mockResolvedValue({
			data: { duplicate: true },
			error: undefined,
		});
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const firstMount = renderHook(() => useConversationCommands("ao-duplicate-send"), {
			wrapper: HookWrapper,
		});

		await act(async () => {
			await firstMount.result.current.send("idempotent retry");
		});
		firstMount.unmount();
		const secondMount = renderHook(() => useConversationCommands("ao-duplicate-send"), {
			wrapper: HookWrapper,
		});

		expect(secondMount.result.current.pendingAcceptedTurnId).toBeUndefined();
		expect(secondMount.result.current.busy).toBe(false);
	});

	it("retains an accepted turn when its follow-up conversation refresh fails", async () => {
		postMock.mockResolvedValue({
			data: { duplicate: false, turnId: "turn-refresh-failed" },
			error: undefined,
		});
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		vi.spyOn(queryClient, "invalidateQueries").mockRejectedValue(new Error("refresh failed"));
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const firstMount = renderHook(() => useConversationCommands("ao-refresh-failure"), {
			wrapper: HookWrapper,
		});

		let sendError: unknown;
		await act(async () => {
			try {
				await firstMount.result.current.send("accepted before refresh failed");
			} catch (error) {
				sendError = error;
			}
		});
		expect(sendError).toBeUndefined();

		firstMount.unmount();
		const secondMount = renderHook(() => useConversationCommands("ao-refresh-failure"), {
			wrapper: HookWrapper,
		});
		expect(secondMount.result.current.pendingAcceptedTurnId).toBe("turn-refresh-failed");
		expect(secondMount.result.current.busy).toBe(false);
	});

	it("releases the dispatch sentinel immediately when the daemon queues mid-turn", async () => {
		postMock
			.mockResolvedValueOnce({
				data: { duplicate: false, turnId: "turn-queued-1", state: "queued" as const },
				error: undefined,
			})
			.mockResolvedValueOnce({
				data: { duplicate: false, turnId: "turn-queued-2", state: "queued" as const },
				error: undefined,
			});
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const { result } = renderHook(() => useConversationCommands("ao-queue-chain"), {
			wrapper: HookWrapper,
		});

		await act(async () => {
			await result.current.send("first queued");
		});
		await waitFor(() => expect(result.current.busy).toBe(false));
		expect(result.current.pendingAcceptedTurnId).toBeUndefined();

		await act(async () => {
			await result.current.send("second queued");
		});

		expect(postMock).toHaveBeenCalledTimes(2);
	});

	it("admits only one same-session send before React can publish busy state", async () => {
		const firstResponse = deferred<{
			data: { duplicate: false; turnId: string };
			error: undefined;
		}>();
		postMock
			.mockImplementationOnce(() => firstResponse.promise)
			.mockResolvedValueOnce({
				data: { duplicate: false, turnId: "turn-overlap-second" },
				error: undefined,
			});
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const { result } = renderHook(() => useConversationCommands("ao-overlap"), {
			wrapper: HookWrapper,
		});

		let firstSend!: Promise<unknown>;
		let secondSend!: Promise<unknown>;
		act(() => {
			firstSend = result.current.send("first");
			secondSend = result.current.send("second").catch((error) => error);
		});
		await waitFor(() => expect(postMock).toHaveBeenCalled());
		firstResponse.resolve({
			data: { duplicate: false, turnId: "turn-overlap-first" },
			error: undefined,
		});
		let secondOutcome: unknown;
		await act(async () => {
			await firstSend;
			secondOutcome = await secondSend;
		});

		expect(postMock).toHaveBeenCalledTimes(1);
		expect(secondOutcome).toBeInstanceOf(Error);
		await waitFor(() => {
			expect(result.current.pendingAcceptedTurnId).toBe("turn-overlap-first");
		});
	});

	it("retains accepted work across a full chat surface unmount and remount", async () => {
		postMock.mockResolvedValue({
			data: { turnId: "turn-after-remount" },
			error: undefined,
		});
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const firstMount = renderHook(() => useConversationCommands("ao-remount"), {
			wrapper: HookWrapper,
		});

		await act(async () => {
			await firstMount.result.current.send("keep this work visible");
		});
		await waitFor(() => {
			expect(firstMount.result.current.pendingAcceptedTurnId).toBe("turn-after-remount");
		});
		firstMount.unmount();

		const secondMount = renderHook(() => useConversationCommands("ao-remount"), {
			wrapper: HookWrapper,
		});
		expect(secondMount.result.current.pendingAcceptedTurnId).toBe("turn-after-remount");

		act(() => {
			secondMount.result.current.acknowledgeAcceptedTurn("turn-after-remount");
		});
		await waitFor(() => {
			expect(secondMount.result.current.pendingAcceptedTurnId).toBeUndefined();
		});
	});

});

describe("session-scoped conversation commands", () => {
	it("keeps send mutation state and completion scoped to its initiating session", async () => {
		const response = deferred<{
			data: { turnId: string };
			error: undefined;
		}>();
		postMock.mockReturnValue(response.promise);
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const { result, rerender } = renderHook(
			({ sessionId }) => useConversationCommands(sessionId),
			{ initialProps: { sessionId: "ao-send-a" }, wrapper: HookWrapper },
		);

		let request!: Promise<unknown>;
		act(() => {
			request = result.current.send("work for A");
		});
		await waitFor(() => expect(result.current.busy).toBe(true));

		rerender({ sessionId: "ao-send-b" });
		expect(result.current.busy).toBe(false);
		expect(result.current.error).toBeUndefined();

		response.resolve({ data: { turnId: "turn-send-a" }, error: undefined });
		await act(async () => {
			await request;
		});
		expect(invalidate).toHaveBeenCalledWith({ queryKey: ["conversation", "ao-send-a"] });
		expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["conversation", "ao-send-b"] });
		expect(result.current.pendingAcceptedTurnId).toBeUndefined();
	});

	it("does not publish an initiating session's pending state, error, or refresh after navigation", async () => {
		const response = deferred<{
			data: undefined;
			error: { code: string };
		}>();
		postMock.mockReturnValue(response.promise);
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const { result, rerender } = renderHook(
			({ sessionId }) => useConversationCommands(sessionId),
			{ initialProps: { sessionId: "ao-command-a" }, wrapper: HookWrapper },
		);

		act(() => {
			result.current.interrupt();
		});
		await waitFor(() => expect(result.current.busy).toBe(true));

		rerender({ sessionId: "ao-command-b" });
		expect(result.current.busy).toBe(false);
		expect(result.current.error).toBeUndefined();

		response.resolve({ data: undefined, error: { code: "CHAT_NO_ACTIVE_TURN" } });
		await waitFor(() => {
			expect(invalidate).toHaveBeenCalledWith({ queryKey: ["conversation", "ao-command-a"] });
		});
		expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["conversation", "ao-command-b"] });
		expect(result.current.busy).toBe(false);
		expect(result.current.error).toBeUndefined();
	});

	it.each(["retry", "edit"] as const)(
		"keeps pending and accepted %s work attached to its initiating session",
		async (operation) => {
			const response = deferred<{
				data: {
					activeBranchId?: string;
					sourceBranchId?: string;
					turnId: string;
				};
				error: undefined;
			}>();
			postMock.mockReturnValue(response.promise);
			const queryClient = new QueryClient({
				defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
			});
			const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);
			const HookWrapper = ({ children }: { children: ReactNode }) => (
				<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
			);
			const { result, rerender } = renderHook(
				({ sessionId }) => useConversationCommands(sessionId),
				{ initialProps: { sessionId: "ao-turn-a" }, wrapper: HookWrapper },
			);

			let request!: Promise<unknown>;
			act(() => {
				request =
					operation === "retry"
						? result.current.retryControl.retry("turn-source")
						: result.current.editMessage("turn-source", "edited prompt");
			});
			await waitFor(() => {
				expect(result.current.busy).toBe(true);
				expect(result.current.pendingAcceptedTurnId).toBeUndefined();
			});

			rerender({ sessionId: "ao-turn-b" });
			expect(result.current.busy).toBe(false);
			expect(result.current.pendingAcceptedTurnId).toBeUndefined();

			const acceptedTurnId = `turn-${operation}-accepted`;
			response.resolve({
				data: {
					...(operation === "edit"
						? { activeBranchId: "branch-edit", sourceBranchId: "branch-root" }
						: {}),
					turnId: acceptedTurnId,
				},
				error: undefined,
			});
			await act(async () => {
				await request;
			});

			expect(invalidate).toHaveBeenCalledWith({ queryKey: ["conversation", "ao-turn-a"] });
			expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["conversation", "ao-turn-b"] });
			expect(result.current.busy).toBe(false);
			expect(result.current.pendingAcceptedTurnId).toBeUndefined();

			act(() => result.current.acknowledgeAcceptedTurn(acceptedTurnId));
			expect(result.current.pendingAcceptedTurnId).toBeUndefined();

			rerender({ sessionId: "ao-turn-a" });
			expect(result.current.busy).toBe(false);
			expect(result.current.pendingAcceptedTurnId).toBe(acceptedTurnId);

			act(() => result.current.acknowledgeAcceptedTurn("turn-from-another-session"));
			expect(result.current.busy).toBe(false);
			expect(result.current.pendingAcceptedTurnId).toBe(acceptedTurnId);

			act(() => result.current.acknowledgeAcceptedTurn(acceptedTurnId));
			await waitFor(() => {
				expect(result.current.busy).toBe(false);
				expect(result.current.pendingAcceptedTurnId).toBeUndefined();
			});
		},
	);

	it.each(["retry", "edit"] as const)(
		"clears pending %s work after the initiating request fails",
		async (operation) => {
			const response = deferred<{
				data: undefined;
				error: { code: string };
			}>();
			postMock.mockReturnValue(response.promise);
			const queryClient = new QueryClient({
				defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
			});
			const HookWrapper = ({ children }: { children: ReactNode }) => (
				<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
			);
			const { result } = renderHook(() => useConversationCommands("ao-turn-failure"), {
				wrapper: HookWrapper,
			});

			let request!: Promise<unknown>;
			act(() => {
				request = (
					operation === "retry"
						? result.current.retryControl.retry("turn-source")
						: result.current.editMessage("turn-source", "edited prompt")
				).catch(() => {});
			});
			await waitFor(() => expect(result.current.busy).toBe(true));

			response.resolve({ data: undefined, error: { code: "CHAT_TURN_FAILED" } });
			await act(async () => {
				await request;
			});
			await waitFor(() => {
				expect(result.current.busy).toBe(false);
				expect(result.current.pendingAcceptedTurnId).toBeUndefined();
			});
		},
	);
});

describe("provider catalog controller epochs", () => {
	it("discards a config mutation response from before switch admission", async () => {
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const queryKey = conversationConfigOptionsQueryKey("ao-1");
		queryClient.setQueryData(queryKey, [{ id: "model", currentValue: "source" }]);
		let resolvePatch!: (value: {
			data: { options: Array<{ id: string; currentValue: string }> };
			error: undefined;
		}) => void;
		patchMock.mockReturnValue(
			new Promise((resolve) => {
				resolvePatch = resolve;
			}),
		);
		const Wrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const { result } = renderHook(() => useConversationConfigOptions("ao-1", false), {
			wrapper: Wrapper,
		});

		let mutation!: Promise<unknown>;
		act(() => {
			mutation = result.current.setOption("model", { value: "source-next" });
		});
		await waitFor(() => expect(patchMock).toHaveBeenCalledOnce());
		act(() => clearConversationProviderCatalogs(queryClient, "ao-1"));
		expect(queryClient.getQueryData(queryKey)).toBeUndefined();

		resolvePatch({
			data: { options: [{ id: "model", currentValue: "source-next" }] },
			error: undefined,
		});
		await act(async () => mutation);

		expect(queryClient.getQueryData(queryKey)).toBeUndefined();
	});
});

describe("useConversation snapshot mapping", () => {
	it("maps branch metadata and lightweight prompt content", async () => {
		getMock.mockResolvedValue({
			data: {
				...WIRE,
				activeBranchId: "branch-child",
				branchedFromEarlierMessage: true,
				branchMaterialization: {
					strategy: "approximate_context",
					replayTruncated: true,
				},
				branchPoints: [
					{
						turnId: "turn-1",
						position: 2,
						total: 3,
						previousBranchId: "branch-previous",
						nextBranchId: "branch-next",
					},
				],
				messages: [
					{
						id: "message-1",
						turnId: "turn-1",
						sequence: 1,
						revision: 1,
						role: "user",
						origin: "human",
						text: "inspect this",
						streaming: false,
						createdAt: "2026-08-03T00:00:00Z",
						editAvailable: true,
						content: [
							{ type: "image", mimeType: "image/png" },
							{ type: "resource", name: "notes.md", uri: "file:///notes.md" },
						],
					},
				],
			},
			error: undefined,
		});

		const { result } = renderHook(() => useConversation("ao-1"), { wrapper });
		await waitFor(() => expect(result.current.snapshot).toBeDefined());

		expect(result.current.snapshot).toMatchObject({
			activeBranchId: "branch-child",
			branchedFromEarlierMessage: true,
			branchMaterialization: {
				strategy: "approximate_context",
				replayTruncated: true,
			},
			branchPoints: [{ turnId: "turn-1", position: 2, total: 3 }],
		});
		expect(result.current.snapshot!.items[0]).toMatchObject({
			editAvailable: true,
			content: [
				{ type: "image", mimeType: "image/png" },
				{ type: "resource", name: "notes.md", uri: "file:///notes.md" },
			],
		});
	});

	it("maps the provider state the timeline cannot express", async () => {
		getMock.mockResolvedValue({ data: WIRE, error: undefined });

		const { result } = renderHook(() => useConversation("ao-1"), { wrapper });
		await waitFor(() => expect(result.current.snapshot).toBeDefined());
		const snapshot = result.current.snapshot!;

		expect(snapshot.modelReroute).toEqual({
			fromModel: "gpt-5.6-terra",
			toModel: "gpt-5.6-terra-mini",
			reason: "at capacity",
			providerTurnId: "prov-1",
			at: "2026-08-03T00:00:01Z",
		});
		expect(snapshot.account?.reauthRequiredAt).toBe("2026-08-03T00:00:02Z");
		expect(snapshot.threadState).toEqual({
			status: "system_error",
			waitingOn: ["user_input"],
			archivedAt: undefined,
			closedAt: undefined,
		});
		expect(snapshot.mcpServers).toHaveLength(2);
		expect(snapshot.turns[0]!.plan?.steps).toEqual([
			{ text: "one", status: "completed" },
			{ text: "two", status: "in_progress" },
		]);
	});

	it("maps retry lineage and consumed-source facts from the daemon", async () => {
		getMock.mockResolvedValue({
			data: {
				...WIRE,
				turns: [
					{
						...WIRE.turns[0],
						retryOfTurnId: "turn-source",
						hasRetryAttempt: true,
					},
				],
			},
			error: undefined,
		});

		const { result } = renderHook(() => useConversation("ao-1"), { wrapper });
		await waitFor(() => expect(result.current.snapshot).toBeDefined());

		expect(result.current.snapshot!.turns[0]).toMatchObject({
			retryOfTurnId: "turn-source",
			hasRetryAttempt: true,
		});
	});

	// Absent must stay absent: a client has to tell "the provider said nothing" from
	// "the provider said everything is fine".
	it("leaves the new fields undefined when the daemon omits them", async () => {
		getMock.mockResolvedValue({
			data: { ...WIRE, modelReroute: undefined, account: undefined, threadState: undefined, mcpServers: [] },
			error: undefined,
		});

		const { result } = renderHook(() => useConversation("ao-1"), { wrapper });
		await waitFor(() => expect(result.current.snapshot).toBeDefined());

		expect(result.current.snapshot!.modelReroute).toBeUndefined();
		expect(result.current.snapshot!.account).toBeUndefined();
		expect(result.current.snapshot!.threadState).toBeUndefined();
		expect(result.current.snapshot!.mcpServers).toBeUndefined();
	});
});

describe("conversation branching commands", () => {
	it("edits through the dedicated endpoint without rolling back", async () => {
		postMock.mockResolvedValue({ data: {}, error: undefined });
		const { result } = renderHook(() => useConversationCommands("ao-1"), { wrapper });

		await act(async () => {
			await result.current.editMessage("turn-2", "edited prompt");
		});

		expect(postMock).toHaveBeenCalledWith(
			"/api/v1/sessions/{sessionId}/conversation/turns/{turnId}/edit",
			expect.objectContaining({
				params: { path: { sessionId: "ao-1", turnId: "turn-2" } },
				body: expect.objectContaining({ text: "edited prompt" }),
			}),
		);
		expect(
			postMock.mock.calls.some(([path]) => String(path).endsWith("/rollback")),
		).toBe(false);
	});

	it("activates an existing branch", async () => {
		postMock.mockResolvedValue({ data: {}, error: undefined });
		const { result } = renderHook(() => useConversationCommands("ao-1"), { wrapper });

		await act(async () => {
			await result.current.activateBranch("branch-previous");
		});

		expect(postMock).toHaveBeenCalledWith(
			"/api/v1/sessions/{sessionId}/conversation/branches/{branchId}/activate",
			{ params: { path: { sessionId: "ao-1", branchId: "branch-previous" } } },
		);
	});
});

describe("steering refusals", () => {
	it("posts native image attachments with steer guidance", async () => {
		postMock.mockResolvedValue({
			data: { providerTurnId: "provider-1", activityId: "activity-1" },
			error: undefined,
		});
		const { result } = renderHook(() => useConversationCommands("ao-1"), { wrapper });

		await act(async () => {
			await result.current.steer("inspect this", [
				{ mimeType: "image/png", data: "aW1hZ2U=" },
			]);
		});

		expect(postMock).toHaveBeenCalledWith(
			"/api/v1/sessions/{sessionId}/conversation/steer",
			{
				params: { path: { sessionId: "ao-1" } },
				body: {
					text: "inspect this",
					attachments: [{ mimeType: "image/png", data: "aW1hZ2U=" }],
					clientMessageId: expect.any(String),
				},
			},
		);
	});

	it("clears steer pending before a slow conversation refresh finishes", async () => {
		const refresh = deferred<void>();
		const steerResponse = deferred<{
			data: { sourceTurnId: string; providerTurnId: string; activityId: string };
			error: undefined;
		}>();
		postMock.mockImplementationOnce(() => steerResponse.promise);
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		vi.spyOn(queryClient, "invalidateQueries").mockImplementation(() => refresh.promise);
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const { result } = renderHook(() => useConversationCommands("ao-1"), { wrapper: HookWrapper });

		let steerDone!: Promise<unknown>;
		act(() => {
			steerDone = result.current.steer("go left");
		});
		await waitFor(() => expect(result.current.steerPending).toBe(true));

		steerResponse.resolve({
			data: { sourceTurnId: "turn-1", providerTurnId: "provider-1", activityId: "activity-1" },
			error: undefined,
		});
		await act(async () => {
			await steerDone;
		});

		await waitFor(() => expect(result.current.steerPending).toBe(false));
		refresh.resolve();
	});

	it("promotes the selected durable queued turn through the turn-scoped route", async () => {
		postMock.mockResolvedValue({
			data: { sourceTurnId: "queued-2", providerTurnId: "provider-1", activityId: "activity-1" },
			error: undefined,
		});
		const { result } = renderHook(() => useConversationCommands("ao-1"), { wrapper });
		await act(async () => {
			await result.current.promoteQueuedTurn("queued-2");
		});
		expect(postMock).toHaveBeenCalledWith(
			"/api/v1/sessions/{sessionId}/conversation/turns/{turnId}/steer",
			{ params: { path: { sessionId: "ao-1", turnId: "queued-2" } } },
		);
	});

	async function steerFailingWith(code: string) {
		apiErrorCodeMock.mockReturnValue(code);
		apiErrorMessageMock.mockReturnValue("a compaction turn is running.");
		postMock.mockResolvedValue({ data: undefined, error: { code } });

		const { result } = renderHook(() => useConversationCommands("ao-1"), { wrapper });
		await act(async () => {
			await result.current.steer("go left").catch(() => {});
		});
		return result;
	}

	it("tells the user to send it as a message when nothing is in flight", async () => {
		const result = await steerFailingWith("CHAT_NO_ACTIVE_TURN");
		await waitFor(() =>
			expect(result.current.steerRefusal).toMatch(/Send it as a message instead/),
		);
	});

	// The daemon's own message names which kind of turn refused, which is the part the
	// user needs; "cannot be steered" alone leaves them nothing to do.
	it("keeps the daemon's wording for a turn that cannot absorb guidance", async () => {
		const result = await steerFailingWith("CHAT_TURN_NOT_STEERABLE");
		await waitFor(() => {
			expect(result.current.steerRefusal).toMatch(/a compaction turn is running/);
			expect(result.current.steerRefusal).toMatch(/Try again once it finishes/);
		});
	});

	// The control is withdrawn instead, so there is nothing to say.
	it("says nothing when the harness cannot steer at all", async () => {
		const result = await steerFailingWith("CHAT_STEER_UNSUPPORTED");
		await waitFor(() => {
			expect(result.current.steerUnsupported).toBe(true);
			expect(result.current.steerRefusal).toBeUndefined();
		});
	});
});

describe("tool server reload refusals", () => {
	it("withdraws the control when the harness cannot reload", async () => {
		apiErrorCodeMock.mockReturnValue("CHAT_MCP_RELOAD_UNSUPPORTED");
		postMock.mockResolvedValue({ data: undefined, error: { code: "CHAT_MCP_RELOAD_UNSUPPORTED" } });

		const { result } = renderHook(() => useConversationCommands("ao-1"), { wrapper });
		await act(async () => {
			await result.current.reloadMcpServers().catch(() => {});
		});

		await waitFor(() => {
			expect(result.current.mcpReloadUnsupported).toBe(true);
			// Not also an error message: the control disappearing is the whole answer.
			expect(result.current.mcpReloadError).toBeUndefined();
		});
	});

	it("surfaces a refusal the user can act on", async () => {
		apiErrorCodeMock.mockReturnValue("CHAT_TURN_RUNNING");
		apiErrorMessageMock.mockReturnValue("a turn is running");
		postMock.mockResolvedValue({ data: undefined, error: { code: "CHAT_TURN_RUNNING" } });

		const { result } = renderHook(() => useConversationCommands("ao-1"), { wrapper });
		await act(async () => {
			await result.current.reloadMcpServers().catch(() => {});
		});

		await waitFor(() => {
			expect(result.current.mcpReloadUnsupported).toBe(false);
			expect(result.current.mcpReloadError).toBe("a turn is running");
		});
	});
});

describe("controller recovery", () => {
	it("refreshes the conversation after Stop reports stale turn state", async () => {
		postMock.mockResolvedValue({
			data: undefined,
			error: { code: "CHAT_NO_ACTIVE_TURN" },
			response: { status: 409 },
		});
		const invalidateSpy = vi.spyOn(QueryClient.prototype, "invalidateQueries");

		const { result } = renderHook(() => useConversationCommands("ao-1"), { wrapper });
		act(() => {
			result.current.interrupt();
		});

		await waitFor(() => {
			expect(postMock).toHaveBeenCalledWith(
				"/api/v1/sessions/{sessionId}/conversation/interrupt",
				{ params: { path: { sessionId: "ao-1" } } },
			);
			expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["conversation", "ao-1"] });
		});
		invalidateSpy.mockRestore();
	});

	it("resumes the agent and refreshes both chat and task state", async () => {
		postMock.mockResolvedValue({ data: {}, error: undefined, response: { status: 200 } });
		const invalidateSpy = vi.spyOn(QueryClient.prototype, "invalidateQueries");

		const { result } = renderHook(() => useConversationCommands("ao-1"), { wrapper });
		await act(async () => {
			await result.current.resumeAgent();
		});

		expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/resume-agent", {
			params: { path: { sessionId: "ao-1" } },
		});
		expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["conversation", "ao-1"] });
		expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: workspaceQueryKey });
		invalidateSpy.mockRestore();
	});
});
