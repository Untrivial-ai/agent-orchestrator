import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const { getMock, postMock, apiErrorCodeMock, apiErrorMessageMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	postMock: vi.fn(),
	apiErrorCodeMock: vi.fn(),
	apiErrorMessageMock: vi.fn(),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock, PATCH: vi.fn() },
	apiErrorCode: apiErrorCodeMock,
	apiErrorMessage: apiErrorMessageMock,
}));

import { useConversation, useConversationCommands } from "./useConversation";
import { workspaceQueryKey } from "./useWorkspaceQuery";

function wrapper({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
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
	postMock.mockReset();
	apiErrorCodeMock.mockReset().mockReturnValue(undefined);
	apiErrorMessageMock.mockReset().mockReturnValue("failed");
});

describe("useConversation snapshot mapping", () => {
	it("maps branch metadata and lightweight prompt content", async () => {
		getMock.mockResolvedValue({
			data: {
				...WIRE,
				activeBranchId: "branch-child",
				branchedFromEarlierMessage: true,
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
