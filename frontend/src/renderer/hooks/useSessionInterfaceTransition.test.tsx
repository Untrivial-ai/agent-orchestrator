import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { getMock, putMock } = vi.hoisted(() => ({ getMock: vi.fn(), putMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: vi.fn(), PUT: putMock, DELETE: vi.fn() },
	apiErrorMessage: () => "request failed",
	hasTrustedApiBaseUrl: () => true,
}));

import { useSessionInterfaceTransition } from "./useSessionInterfaceTransition";

function wrapper({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

beforeEach(() => {
	getMock.mockReset();
	putMock.mockReset();
});

describe("interface switch readiness", () => {
	it.each(["NATIVE_SESSION_MISSING", "NATIVE_SESSION_UNVERIFIED"])(
		"rechecks transient native-session readiness (%s) until the switch becomes supported",
		async (reasonCode) => {
			getMock
				.mockResolvedValueOnce({
					data: {
						supported: false,
						targetMode: "chat",
						reasonCode,
						reason: "no native conversation found for codex",
					},
					error: undefined,
				})
				.mockResolvedValue({
					data: { supported: true, targetMode: "chat" },
					error: undefined,
				});

			const { result } = renderHook(() => useSessionInterfaceTransition("session-1"), {
				wrapper,
			});

			await waitFor(() => expect(result.current.status?.supported).toBe(false));
			await waitFor(() => expect(result.current.status?.supported).toBe(true), {
				timeout: 2_500,
			});
			expect(getMock).toHaveBeenCalledTimes(2);
		},
	);

	it("does not poll a permanently unsupported interface handoff", async () => {
		getMock.mockResolvedValue({
			data: {
				supported: false,
				targetMode: "chat",
				reasonCode: "INTERFACE_HANDOFF_UNSUPPORTED",
				reason: "cursor has not declared compatible TUI and Chat identities",
			},
			error: undefined,
		});

		const { result } = renderHook(() => useSessionInterfaceTransition("session-1"), {
			wrapper,
		});

		await waitFor(() => expect(result.current.status?.supported).toBe(false));
		await new Promise((resolve) => setTimeout(resolve, 1_100));
		expect(getMock).toHaveBeenCalledTimes(1);
	});

	it("acknowledges the exact transition and replaces the cached notice with the durable response", async () => {
		const transition = {
			id: "transition-1",
			sessionId: "session-1",
			sourceMode: "chat" as const,
			targetMode: "tui" as const,
			policy: "drain" as const,
			phase: "recovery_required" as const,
			createdAt: "2026-08-12T10:00:00Z",
			updatedAt: "2026-08-12T10:01:00Z",
		};
		const acknowledged = { ...transition, noticeAcknowledgedAt: "2026-08-13T08:00:00Z" };
		getMock
			.mockResolvedValueOnce({
				data: { supported: true, targetMode: "chat", transition },
				error: undefined,
			})
			.mockResolvedValue({
				data: { supported: true, targetMode: "chat", transition: acknowledged },
				error: undefined,
			});
		putMock.mockResolvedValue({
			data: { ok: true, sessionId: "session-1", transition: acknowledged },
			error: undefined,
		});

		const { result } = renderHook(() => useSessionInterfaceTransition("session-1"), {
			wrapper,
		});
		await waitFor(() => expect(result.current.transition?.id).toBe("transition-1"));
		await act(async () => {
			await result.current.acknowledgeNotice("transition-1");
		});

		expect(putMock).toHaveBeenCalledWith(
			"/api/v1/sessions/{sessionId}/interface-transition/{transitionId}/notice-acknowledgement",
			{
				params: {
					path: { sessionId: "session-1", transitionId: "transition-1" },
				},
			},
		);
		await waitFor(() =>
			expect(result.current.transition?.noticeAcknowledgedAt).toBe("2026-08-13T08:00:00Z"),
		);
	});
});
