import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { CloudCpClientEvent } from "../../lib/cloud-cp";
import { CloudCpError } from "../../lib/cloud-cp/errors";
import type { WorkspaceSession } from "../../types/workspace";
import { appendCloudEvents, CloudSessionChatSurface, loadCloudChatEvents, toSnapshot } from "./CloudSessionChatSurface";

const cloudMocks = vi.hoisted(() => ({
	listChatEvents: vi.fn(),
	sendSessionMessage: vi.fn(),
	cancelTurn: vi.fn(),
	steerTurn: vi.fn(),
	decideChatApproval: vi.fn(),
	listChatModels: vi.fn(),
	resumeSession: vi.fn(),
	chatProps: vi.fn(),
}));
vi.mock("../../hooks/useCloudCp", () => ({
	useCloudCp: () => ({ ready: true, client: cloudMocks }),
}));
vi.mock("./ChatWorkspace", () => ({
	ChatWorkspace: (props: unknown) => {
		cloudMocks.chatProps(props);
		return <div data-testid="cloud-chat" />;
	},
}));

const session = {
	id: "session-1",
	workspaceId: "project-1",
	workspaceName: "project",
	title: "Cloud session",
	provider: "codex",
	kind: "worker",
	mode: "chat",
	status: "working",
	updatedAt: "2026-09-22T00:00:00Z",
	prs: [],
} satisfies WorkspaceSession;

describe("CloudSessionChatSurface", () => {
	beforeEach(() => localStorage.clear());
	it("wakes a paused worker before loading model choices", async () => {
		cloudMocks.listChatEvents.mockResolvedValue({ events: [], hasMore: false, nextAfter: 0 });
		cloudMocks.listChatModels.mockReset()
			.mockRejectedValueOnce(new CloudCpError("The session worker is not connected.", { status: 409, code: "WORKER_UNAVAILABLE" }))
			.mockResolvedValue({ models: [{ id: "codex-test", displayName: "Codex Test", default: true }] });
		cloudMocks.resumeSession.mockReset().mockResolvedValue({});
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(
			<QueryClientProvider client={queryClient}>
				<CloudSessionChatSurface session={{ ...session, cloud: { orgId: "org-1" } }} />
			</QueryClientProvider>,
		);
		await waitFor(() => expect(cloudMocks.resumeSession).toHaveBeenCalledWith("org-1", session.id, { signal: expect.any(AbortSignal) }));
		await waitFor(() => expect(cloudMocks.chatProps.mock.lastCall?.[0].models).toEqual([
			{ id: "codex-test", displayName: "Codex Test", default: true },
		]));
	});

	it("shows provider models and sends the selected model and effort with the next turn", async () => {
		cloudMocks.listChatEvents.mockResolvedValue({ events: [], hasMore: false, nextAfter: 0 });
		cloudMocks.listChatModels.mockResolvedValue({ models: [{ id: "codex-test", displayName: "Codex Test", default: true, efforts: ["low", "high"] }] });
		cloudMocks.sendSessionMessage.mockResolvedValue({ event: {} });
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
		render(
			<QueryClientProvider client={queryClient}>
				<CloudSessionChatSurface session={{ ...session, cloud: { orgId: "org-1" } }} />
			</QueryClientProvider>,
		);
		await waitFor(() => expect(cloudMocks.chatProps.mock.lastCall?.[0].models).toEqual([
			{ id: "codex-test", displayName: "Codex Test", default: true, efforts: ["low", "high"] },
		]));
		expect(cloudMocks.chatProps.mock.lastCall?.[0].onChooseSettings).toBeTypeOf("function");
		cloudMocks.chatProps.mock.lastCall?.[0].onChooseSettings({ model: "codex-test", reasoningEffort: "high" });
		await cloudMocks.chatProps.mock.lastCall?.[0].onSend("hello", [], "message-2");
		expect(cloudMocks.sendSessionMessage).toHaveBeenCalledWith("org-1", session.id, {
			text: "hello", model: "codex-test", reasoningEffort: "high",
		}, { idempotencyKey: "message-2" });
	});

	it("hides model controls for Cloud harnesses without a provider catalog", async () => {
		cloudMocks.listChatEvents.mockResolvedValue({ events: [], hasMore: false, nextAfter: 0 });
		cloudMocks.listChatModels.mockClear();
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(
			<QueryClientProvider client={queryClient}>
				<CloudSessionChatSurface session={{ ...session, provider: "claude-code", cloud: { orgId: "org-1" } }} />
			</QueryClientProvider>,
		);
		await waitFor(() => expect(cloudMocks.chatProps.mock.lastCall?.[0].snapshot.harness).toBe("claude-code"));
		expect(cloudMocks.chatProps.mock.lastCall?.[0].models).toEqual([]);
		expect(cloudMocks.chatProps.mock.lastCall?.[0].showApprovalMode).toBe(false);
		expect(cloudMocks.listChatModels).not.toHaveBeenCalled();
	});

	it.each([
		["claude-code", "trusted", ["default", "accept-edits", "auto", "bypass-permissions"], "accept-edits"],
		["cursor", "trusted", ["default", "accept-edits", "auto", "bypass-permissions"], "auto"],
		["codex", "trusted", ["default", "accept-edits", "auto", "bypass-permissions"], "accept-edits"],
	] as const)("offers Cloud %s approval modes and sends the selected policy", async (provider, ceiling, modes, chosen) => {
		cloudMocks.listChatEvents.mockResolvedValue({ events: [], hasMore: false, nextAfter: 0 });
		cloudMocks.listChatModels.mockResolvedValue({ models: [] });
		cloudMocks.sendSessionMessage.mockResolvedValue({ event: {} });
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
		render(
			<QueryClientProvider client={queryClient}>
				<CloudSessionChatSurface session={{ ...session, provider, cloud: { orgId: "org-1", permissionMode: ceiling } }} />
			</QueryClientProvider>,
		);
		const props = cloudMocks.chatProps.mock.lastCall?.[0];
		expect(props.configOptions).toBeUndefined();
		expect(props.approvalModes).toEqual(modes);
		act(() => props.onChooseSettings({ approvalMode: chosen }));
		await props.onSend("hello", [], `mode-${provider}`);
		expect(cloudMocks.sendSessionMessage).toHaveBeenLastCalledWith("org-1", session.id, {
			text: "hello", mode: ceiling, approvalMode: chosen,
		}, { idempotencyKey: `mode-${provider}` });
	});

	it("does not offer a mode above the Cloud session ceiling", async () => {
		cloudMocks.listChatEvents.mockResolvedValue({ events: [], hasMore: false, nextAfter: 0 });
		cloudMocks.sendSessionMessage.mockResolvedValue({ event: {} });
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
		render(
			<QueryClientProvider client={queryClient}>
				<CloudSessionChatSurface session={{ ...session, cloud: { orgId: "org-1", permissionMode: "standard" } }} />
			</QueryClientProvider>,
		);
		const props = cloudMocks.chatProps.mock.lastCall?.[0];
		expect(props.approvalModes).toEqual(["accept-edits", "auto"]);
		act(() => props.onChooseSettings({ approvalMode: "bypass-permissions" }));
		await props.onSend("hello", [], "capped-message");
		expect(cloudMocks.sendSessionMessage).toHaveBeenLastCalledWith("org-1", session.id, {
			text: "hello", mode: "standard", approvalMode: "accept-edits",
		}, { idempotencyKey: "capped-message" });
	});

	it("does not carry one Cloud session's model selection into another session", async () => {
		cloudMocks.listChatEvents.mockResolvedValue({ events: [], hasMore: false, nextAfter: 0 });
		cloudMocks.listChatModels.mockResolvedValue({ models: [{ id: "codex-test", displayName: "Codex Test", default: true }] });
		cloudMocks.sendSessionMessage.mockResolvedValue({ event: {} });
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
		const view = render(
			<QueryClientProvider client={queryClient}>
				<CloudSessionChatSurface session={{ ...session, cloud: { orgId: "org-1" } }} />
			</QueryClientProvider>,
		);
		await waitFor(() => expect(cloudMocks.chatProps.mock.lastCall?.[0].onChooseSettings).toBeTypeOf("function"));
		cloudMocks.chatProps.mock.lastCall?.[0].onChooseSettings({ model: "codex-test" });
		view.rerender(
			<QueryClientProvider client={queryClient}>
				<CloudSessionChatSurface session={{ ...session, id: "session-2", cloud: { orgId: "org-1" } }} />
			</QueryClientProvider>,
		);
		await cloudMocks.chatProps.mock.lastCall?.[0].onSend("next", [], "message-3");
		expect(cloudMocks.sendSessionMessage).toHaveBeenLastCalledWith("org-1", "session-2", { text: "next" }, { idempotencyKey: "message-3" });
	});

	it("does not carry Codex selections into another harness in the same session", async () => {
		cloudMocks.listChatEvents.mockResolvedValue({ events: [], hasMore: false, nextAfter: 0 });
		cloudMocks.listChatModels.mockResolvedValue({ models: [{ id: "codex-test", displayName: "Codex Test", default: true }] });
		cloudMocks.sendSessionMessage.mockResolvedValue({ event: {} });
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
		const view = render(
			<QueryClientProvider client={queryClient}>
				<CloudSessionChatSurface session={{ ...session, cloud: { orgId: "org-1", permissionMode: "trusted" } }} />
			</QueryClientProvider>,
		);
		await waitFor(() => expect(cloudMocks.chatProps.mock.lastCall?.[0].onChooseSettings).toBeTypeOf("function"));
		act(() => cloudMocks.chatProps.mock.lastCall?.[0].onChooseSettings({ model: "codex-test" }));
		view.rerender(
			<QueryClientProvider client={queryClient}>
				<CloudSessionChatSurface session={{ ...session, provider: "claude-code", cloud: { orgId: "org-1", permissionMode: "trusted" } }} />
			</QueryClientProvider>,
		);
		await cloudMocks.chatProps.mock.lastCall?.[0].onSend("next", [], "after-handoff");
		expect(cloudMocks.sendSessionMessage).toHaveBeenLastCalledWith("org-1", session.id, {
			text: "next", mode: "trusted", approvalMode: "default",
		}, { idempotencyKey: "after-handoff" });
	});

	it("surfaces send errors and sends cancellation to the active turn", async () => {
		cloudMocks.listChatEvents.mockReset().mockResolvedValue({
			events: [
				{ sessionId: session.id, sequence: 1, type: "chat.user_message", payload: { text: "Run", turnId: "turn-1" }, createdAt: session.updatedAt },
				{ sessionId: session.id, sequence: 2, type: "chat.turn_started", payload: { turnId: "turn-1" }, createdAt: session.updatedAt },
			],
			hasMore: false,
			nextAfter: 2,
		});
		cloudMocks.sendSessionMessage.mockReset().mockRejectedValue(new Error("send failed"));
		cloudMocks.cancelTurn.mockReset().mockRejectedValue(new Error("cancel failed"));
		cloudMocks.chatProps.mockClear();
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
		render(
			<QueryClientProvider client={queryClient}>
				<CloudSessionChatSurface session={{ ...session, cloud: { orgId: "org-1" } }} />
			</QueryClientProvider>,
		);
		await waitFor(() => expect(cloudMocks.chatProps.mock.lastCall?.[0].onInterrupt).toBeTypeOf("function"));
		const props = cloudMocks.chatProps.mock.lastCall?.[0];
		expect(props.onSteer).toBeUndefined();
		await expect(props.onSend("hello", [], "message-1")).rejects.toThrow("send failed");
		await waitFor(() => expect(cloudMocks.chatProps.mock.lastCall?.[0].commandError).toBe("send failed"));
		cloudMocks.chatProps.mock.lastCall?.[0].onInterrupt();
		await waitFor(() => expect(cloudMocks.cancelTurn).toHaveBeenCalledWith("org-1", session.id, "turn-1"));
		await waitFor(() => expect(cloudMocks.chatProps.mock.lastCall?.[0].commandError).toBe("cancel failed"));
	});

	it("appends only newer events without duplicating replayed pages", () => {
		const event = (sequence: number): CloudCpClientEvent => ({
			sessionId: session.id,
			sequence,
			type: "chat.assistant_delta",
			payload: { text: String(sequence) },
			createdAt: session.updatedAt,
		});
		const existing = [event(1), event(500)];
		const merged = appendCloudEvents(existing, [event(500), event(501)]);
		expect(merged.map((item) => item.sequence)).toEqual([1, 500, 501]);
		expect(merged.at(-1)?.sequence).toBe(501);
	});

	it("continues a long history from its last sequence", async () => {
		const event = (sequence: number): CloudCpClientEvent => ({
			sessionId: session.id, sequence, type: "chat.assistant_delta",
			payload: { text: String(sequence) }, createdAt: session.updatedAt,
		});
		const listChatEvents = vi.fn().mockResolvedValue({ events: [event(500), event(501)], hasMore: false, nextAfter: 501 });
		const events = await loadCloudChatEvents({ listChatEvents }, "org-1", session.id, [event(1), event(500)]);
		expect(listChatEvents).toHaveBeenCalledWith("org-1", session.id, { after: 500, limit: 500 });
		expect(events.map((item) => item.sequence)).toEqual([1, 500, 501]);
	});

	it("projects completed and interrupted turns without leaving output streaming", () => {
		const events: CloudCpClientEvent[] = [
			{ sessionId: session.id, sequence: 1, type: "chat.user_message", payload: { text: "First", turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 2, type: "chat.turn_started", payload: { turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 3, type: "chat.assistant_delta", payload: { text: "Done", turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 4, type: "chat.turn_completed", payload: { turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 5, type: "chat.user_message", payload: { text: "Second", turnId: "turn-2" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 6, type: "chat.turn_started", payload: { turnId: "turn-2" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 7, type: "chat.turn_interrupted", payload: { turnId: "turn-2" }, createdAt: session.updatedAt },
		];
		const snapshot = toSnapshot(session, events);
		expect(snapshot.turns.map((turn) => turn.state)).toEqual(["completed", "interrupted"]);
		expect(snapshot.items).toContainEqual(expect.objectContaining({ role: "assistant", text: "Done", streaming: false }));
		expect(snapshot.controller.state).toBe("ready");
	});

	it("omits Codex's already-stored stdin status from the visible reply", () => {
		const events: CloudCpClientEvent[] = [
			{ sessionId: session.id, sequence: 1, type: "chat.user_message", payload: { text: "Hello", turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 2, type: "chat.assistant_delta", payload: { text: "Reading additional input from stdin...\n", turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 3, type: "chat.assistant_delta", payload: { text: "Hello!", turnId: "turn-1" }, createdAt: session.updatedAt },
		];
		const snapshot = toSnapshot(session, events);
		expect(snapshot.items.filter((item) => item.kind === "message" && item.role === "assistant"))
			.toEqual([expect.objectContaining({ text: "Hello!" })]);
	});

	it("shows an idle Cloud send as active while the worker claims it", () => {
		const events: CloudCpClientEvent[] = [
			{ sessionId: session.id, sequence: 1, type: "chat.user_message", payload: { text: "Next", turnId: "turn-next" }, createdAt: session.updatedAt },
		];
		const snapshot = toSnapshot(session, events);
		expect(snapshot.turns).toEqual([expect.objectContaining({ id: "turn-next", state: "running" })]);
		expect(snapshot.controller.state).toBe("busy");
	});

	it("keeps a second Cloud message queued behind an active turn", () => {
		const events: CloudCpClientEvent[] = [
			{ sessionId: session.id, sequence: 1, type: "chat.user_message", payload: { text: "First", turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 2, type: "chat.turn_started", payload: { turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 3, type: "chat.user_message", payload: { text: "Second", turnId: "turn-2" }, createdAt: session.updatedAt },
		];
		const snapshot = toSnapshot(session, events);
		expect(snapshot.turns.map((turn) => turn.state)).toEqual(["running", "queued"]);
	});

	it("projects a durable steer as activity on the active turn", () => {
		const events: CloudCpClientEvent[] = [
			{ sessionId: session.id, sequence: 1, type: "chat.user_message", payload: { text: "Build it", turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 2, type: "chat.turn_started", payload: { turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 3, type: "chat.turn_steered", payload: { turnId: "turn-1", text: "Prefer tests first", clientMessageId: "message-1" }, createdAt: session.updatedAt },
		];

		const snapshot = toSnapshot(session, events);

		expect(snapshot.items).toContainEqual(expect.objectContaining({
			kind: "activity",
			turnId: "turn-1",
			summary: "Steered: Prefer tests first",
			detail: { event: "steer", text: "Prefer tests first", origin: "human", clientMessageId: "message-1" },
		}));
	});

	it("shows the provider's exact pending approval and resolves it through Cloud", async () => {
		cloudMocks.listChatEvents.mockResolvedValue({ events: [
			{ sessionId: session.id, sequence: 1, type: "chat.user_message", payload: { text: "Edit", turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 2, type: "chat.turn_started", payload: { turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 3, type: "chat.approval_requested", payload: {
				turnId: "turn-1", requestId: "approval-1", summary: "Apply file changes",
				decisions: [{ id: "allow-once", label: "Allow once", kind: "allow_once" }, { id: "reject", label: "Reject", kind: "reject_once" }],
			}, createdAt: session.updatedAt },
		], hasMore: false, nextAfter: 3 });
		cloudMocks.decideChatApproval.mockResolvedValue({ ok: true });
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
		render(<QueryClientProvider client={queryClient}>
			<CloudSessionChatSurface session={{ ...session, provider: "cursor", cloud: { orgId: "org-1", permissionMode: "standard" } }} />
		</QueryClientProvider>);
		await waitFor(() => expect(cloudMocks.chatProps.mock.lastCall?.[0].snapshot.items).toContainEqual(expect.objectContaining({
			activityKind: "approval", requestId: "approval-1", status: "pending",
			decisions: [
				{ id: "allow-once", label: "Allow once", kind: "allow_once" },
				{ id: "reject", label: "Reject", kind: "reject_once" },
			],
		})));
		cloudMocks.chatProps.mock.lastCall?.[0].onDecide("approval-1", "allow-once");
		await waitFor(() => expect(cloudMocks.decideChatApproval).toHaveBeenCalledWith("org-1", session.id, "approval-1", "allow-once"));
	});

	it("closes an unresolved approval when its turn ends", () => {
		const events: CloudCpClientEvent[] = [
			{ sessionId: session.id, sequence: 1, type: "chat.turn_started", payload: { turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 2, type: "chat.approval_requested", payload: {
				turnId: "turn-1", requestId: "approval-1", summary: "Run command", decisions: [{ id: "accept", label: "Accept" }],
			}, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 3, type: "chat.turn_aborted", payload: { turnId: "turn-1" }, createdAt: session.updatedAt },
		];
		expect(toSnapshot(session, events).items).toContainEqual(expect.objectContaining({
			requestId: "approval-1", status: "cancelled",
		}));
	});

	it("offers steering only after the active provider advertises it", () => {
		const base: CloudCpClientEvent[] = [
			{ sessionId: session.id, sequence: 1, type: "chat.user_message", payload: { text: "Build", turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 2, type: "chat.turn_started", payload: { turnId: "turn-1" }, createdAt: session.updatedAt },
		];
		expect(toSnapshot(session, base).capabilities).toEqual([]);
		expect(toSnapshot(session, [...base, {
			sessionId: session.id, sequence: 3, type: "chat.turn_capabilities", payload: { turnId: "turn-1", steering: true }, createdAt: session.updatedAt,
		}]).capabilities).toEqual(["steer"]);
	});
});
