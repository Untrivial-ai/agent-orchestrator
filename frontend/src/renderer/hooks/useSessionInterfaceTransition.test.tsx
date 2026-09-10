import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { deleteMock, getMock, postMock, putMock } = vi.hoisted(() => ({
	deleteMock: vi.fn(),
	getMock: vi.fn(),
	postMock: vi.fn(),
	putMock: vi.fn(),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock, PUT: putMock, DELETE: deleteMock },
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

function deferred<T>() {
	let resolve!: (value: T) => void;
	const promise = new Promise<T>((resolvePromise) => {
		resolve = resolvePromise;
	});
	return { promise, resolve };
}

beforeEach(() => {
	deleteMock.mockReset();
	getMock.mockReset();
	postMock.mockReset();
	putMock.mockReset();
});

describe("session-scoped interface transition mutations", () => {
	it("keeps a deferred start attached to its initiating session after navigation", async () => {
		const response = deferred<{
			data: { ok: boolean };
			error: undefined;
		}>();
		getMock.mockResolvedValue({
			data: { supported: true, targetMode: "tui" },
			error: undefined,
		});
		postMock.mockReturnValue(response.promise);
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const { result, rerender } = renderHook(
			({ sessionId }) => useSessionInterfaceTransition(sessionId),
			{ initialProps: { sessionId: "session-a" }, wrapper: HookWrapper },
		);

		let startRequest!: Promise<unknown>;
		act(() => {
			startRequest = result.current.start({ targetMode: "tui", policy: "drain" });
		});
		await waitFor(() => expect(result.current.starting).toBe(true));

		rerender({ sessionId: "session-b" });
		expect(result.current.starting).toBe(false);
		expect(result.current.startError).toBeUndefined();

		response.resolve({ data: { ok: true }, error: undefined });
		await act(async () => {
			await startRequest;
		});
		expect(postMock).toHaveBeenCalledWith(
			"/api/v1/sessions/{sessionId}/interface-transition",
			{
				params: { path: { sessionId: "session-a" } },
				body: { targetMode: "tui", policy: "drain" },
			},
		);
		expect(invalidate).toHaveBeenCalledWith({
			queryKey: ["session-interface-transition", "session-a"],
		});
		expect(invalidate).not.toHaveBeenCalledWith({
			queryKey: ["session-interface-transition", "session-b"],
		});
	});

	it("keeps start pending until the accepted transition refetch is visible", async () => {
		const invalidation = deferred<void>();
		getMock.mockResolvedValue({
			data: { supported: true, targetMode: "tui" },
			error: undefined,
		});
		postMock.mockResolvedValue({ data: { ok: true }, error: undefined });
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const invalidate = vi
			.spyOn(queryClient, "invalidateQueries")
			.mockReturnValue(invalidation.promise);
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const { result } = renderHook(() => useSessionInterfaceTransition("session-a"), {
			wrapper: HookWrapper,
		});

		let startRequest!: Promise<unknown>;
		act(() => {
			startRequest = result.current.start({ targetMode: "tui", policy: "drain" });
		});
		await waitFor(() => {
			expect(invalidate).toHaveBeenCalledWith({
				queryKey: ["session-interface-transition", "session-a"],
			});
		});
		expect(result.current.starting).toBe(true);

		invalidation.resolve();
		await act(async () => {
			await startRequest;
		});
		await waitFor(() => expect(result.current.starting).toBe(false));
	});

	it.each([
		["does not refetch an inactive query", false],
		["fails to refetch the active query", true],
	] as const)(
		"keeps the accepted transition cached when invalidation %s",
		async (_scenario, invalidationFails) => {
			const accepted = {
				id: "accepted-transition",
				sessionId: "session-a",
				sourceMode: "chat" as const,
				targetMode: "tui" as const,
				policy: "drain" as const,
				phase: "draining" as const,
				createdAt: "2026-08-28T10:00:00Z",
				updatedAt: "2026-08-28T10:00:01Z",
			};
			getMock.mockResolvedValue({
				data: { supported: true, targetMode: "tui" },
				error: undefined,
			});
			postMock.mockResolvedValue({
				data: { ok: true, sessionId: "session-a", transition: accepted },
				error: undefined,
			});
			const queryClient = new QueryClient({
				defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
			});
			vi.spyOn(queryClient, "invalidateQueries").mockImplementation(() =>
				invalidationFails
					? Promise.reject(new Error("refetch failed"))
					: Promise.resolve(),
			);
			const HookWrapper = ({ children }: { children: ReactNode }) => (
				<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
			);
			const { result } = renderHook(
				() => useSessionInterfaceTransition("session-a"),
				{ wrapper: HookWrapper },
			);

			await waitFor(() => expect(result.current.status?.supported).toBe(true));
			let startRequest!: Promise<unknown>;
			act(() => {
				startRequest = result.current.start({ targetMode: "tui", policy: "drain" });
			});
			await act(async () => {
				await startRequest.catch(() => undefined);
			});

			expect(result.current.starting).toBe(false);
			expect(result.current.startError).toBeUndefined();
			await waitFor(() =>
				expect(result.current.transition?.id).toBe("accepted-transition"),
			);
		},
	);

	it("keeps each session busy while overlapping starts are pending", async () => {
		const responseA = deferred<{
			data: { ok: boolean };
			error: undefined;
		}>();
		const responseB = deferred<{
			data: { ok: boolean };
			error: undefined;
		}>();
		getMock.mockResolvedValue({
			data: { supported: true, targetMode: "tui" },
			error: undefined,
		});
		postMock.mockImplementation(
			(_path: string, request: { params: { path: { sessionId: string } } }) =>
				request.params.path.sessionId === "session-a"
					? responseA.promise
					: responseB.promise,
		);
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const { result, rerender } = renderHook(
			({ sessionId }) => useSessionInterfaceTransition(sessionId),
			{ initialProps: { sessionId: "session-a" }, wrapper: HookWrapper },
		);

		let startA!: Promise<unknown>;
		act(() => {
			startA = result.current.start({ targetMode: "tui", policy: "drain" });
		});
		await waitFor(() => expect(result.current.starting).toBe(true));

		rerender({ sessionId: "session-b" });
		let startB!: Promise<unknown>;
		act(() => {
			startB = result.current.start({ targetMode: "tui", policy: "drain" });
		});
		await waitFor(() => expect(result.current.starting).toBe(true));

		rerender({ sessionId: "session-a" });
		expect(result.current.starting).toBe(true);

		responseA.resolve({ data: { ok: true }, error: undefined });
		responseB.resolve({ data: { ok: true }, error: undefined });
		await act(async () => {
			await Promise.all([startA, startB]);
		});
	});

	it("keeps a deferred cancellation attached to its initiating session after navigation", async () => {
		const response = deferred<{
			data: undefined;
			error: undefined;
		}>();
		getMock.mockResolvedValue({
			data: { supported: true, targetMode: "tui" },
			error: undefined,
		});
		deleteMock.mockReturnValue(response.promise);
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const { result, rerender } = renderHook(
			({ sessionId }) => useSessionInterfaceTransition(sessionId),
			{ initialProps: { sessionId: "session-a" }, wrapper: HookWrapper },
		);

		let cancelRequest!: Promise<unknown>;
		act(() => {
			cancelRequest = result.current.cancel();
		});
		await waitFor(() => expect(result.current.cancelling).toBe(true));

		rerender({ sessionId: "session-b" });
		expect(result.current.cancelling).toBe(false);
		expect(result.current.cancelError).toBeUndefined();

		response.resolve({ data: undefined, error: undefined });
		await act(async () => {
			await cancelRequest;
		});
		expect(deleteMock).toHaveBeenCalledWith(
			"/api/v1/sessions/{sessionId}/interface-transition",
			{ params: { path: { sessionId: "session-a" } } },
		);
		expect(invalidate).toHaveBeenCalledWith({
			queryKey: ["session-interface-transition", "session-a"],
		});
		expect(invalidate).not.toHaveBeenCalledWith({
			queryKey: ["session-interface-transition", "session-b"],
		});
	});

	it("keeps a deferred notice acknowledgement attached to its initiating session", async () => {
		const transitionA = {
			id: "transition-a",
			sessionId: "session-a",
			sourceMode: "chat" as const,
			targetMode: "tui" as const,
			policy: "drain" as const,
			phase: "recovery_required" as const,
			createdAt: "2026-08-12T10:00:00Z",
			updatedAt: "2026-08-12T10:01:00Z",
		};
		const transitionB = {
			...transitionA,
			id: "transition-b",
			sessionId: "session-b",
		};
		const acknowledgedA = {
			...transitionA,
			noticeAcknowledgedAt: "2026-08-13T08:00:00Z",
		};
		const response = deferred<{
			data: { ok: boolean; sessionId: string; transition: typeof acknowledgedA };
			error: undefined;
		}>();
		getMock.mockImplementation(
			(_path: string, request: { params: { path: { sessionId: string } } }) =>
				Promise.resolve({
					data: {
						supported: true,
						targetMode: "tui",
						transition:
							request.params.path.sessionId === "session-a" ? transitionA : transitionB,
					},
					error: undefined,
				}),
		);
		putMock.mockReturnValue(response.promise);
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const { result, rerender } = renderHook(
			({ sessionId }) => useSessionInterfaceTransition(sessionId),
			{ initialProps: { sessionId: "session-a" }, wrapper: HookWrapper },
		);
		await waitFor(() => expect(result.current.transition?.id).toBe("transition-a"));

		let acknowledgeRequest!: Promise<unknown>;
		act(() => {
			acknowledgeRequest = result.current.acknowledgeNotice("transition-a");
		});
		await waitFor(() => expect(result.current.acknowledgingNotice).toBe(true));

		rerender({ sessionId: "session-b" });
		await waitFor(() => expect(result.current.transition?.id).toBe("transition-b"));
		expect(result.current.acknowledgingNotice).toBe(false);
		expect(result.current.acknowledgeNoticeError).toBeUndefined();

		response.resolve({
			data: { ok: true, sessionId: "session-a", transition: acknowledgedA },
			error: undefined,
		});
		await act(async () => {
			await acknowledgeRequest;
		});
		expect(putMock).toHaveBeenCalledWith(
			"/api/v1/sessions/{sessionId}/interface-transition/{transitionId}/notice-acknowledgement",
			{
				params: {
					path: { sessionId: "session-a", transitionId: "transition-a" },
				},
			},
		);
		expect(
			queryClient.getQueryData<{ transition?: typeof acknowledgedA }>([
				"session-interface-transition",
				"session-a",
			])?.transition?.noticeAcknowledgedAt,
		).toBe("2026-08-13T08:00:00Z");
		expect(result.current.transition?.id).toBe("transition-b");
		expect(result.current.transition?.noticeAcknowledgedAt).toBeUndefined();
		expect(invalidate).toHaveBeenCalledWith({
			queryKey: ["session-interface-transition", "session-a"],
		});
		expect(invalidate).not.toHaveBeenCalledWith({
			queryKey: ["session-interface-transition", "session-b"],
		});
	});

	it.each(["start", "cancel", "acknowledge"] as const)(
		"does not expose session A's deferred %s failure while session B is selected",
		async (operation) => {
			const response = deferred<{
				data: undefined;
				error: { code: string };
			}>();
			getMock.mockResolvedValue({
				data: { supported: true, targetMode: "tui" },
				error: undefined,
			});
			if (operation === "start") postMock.mockReturnValue(response.promise);
			if (operation === "cancel") deleteMock.mockReturnValue(response.promise);
			if (operation === "acknowledge") putMock.mockReturnValue(response.promise);
			const queryClient = new QueryClient({
				defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
			});
			const HookWrapper = ({ children }: { children: ReactNode }) => (
				<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
			);
			const { result, rerender } = renderHook(
				({ sessionId }) => useSessionInterfaceTransition(sessionId),
				{ initialProps: { sessionId: "session-a" }, wrapper: HookWrapper },
			);

			let request!: Promise<unknown>;
			act(() => {
				const mutation =
					operation === "start"
						? result.current.start({ targetMode: "tui", policy: "drain" })
						: operation === "cancel"
							? result.current.cancel()
							: result.current.acknowledgeNotice("transition-a");
				request = mutation.catch((error) => error);
			});
			await waitFor(() => {
				const pending =
					operation === "start"
						? result.current.starting
						: operation === "cancel"
							? result.current.cancelling
							: result.current.acknowledgingNotice;
				expect(pending).toBe(true);
			});

			rerender({ sessionId: "session-b" });
			response.resolve({ data: undefined, error: { code: "TRANSITION_FAILED" } });
			await act(async () => {
				await request;
			});
			const destinationError =
				operation === "start"
					? result.current.startError
					: operation === "cancel"
						? result.current.cancelError
						: result.current.acknowledgeNoticeError;
			expect(destinationError).toBeUndefined();

			rerender({ sessionId: "session-a" });
			await waitFor(() => {
				const initiatingError =
					operation === "start"
						? result.current.startError
						: operation === "cancel"
							? result.current.cancelError
							: result.current.acknowledgeNoticeError;
				expect(initiatingError).toBe("request failed");
			});
		},
	);

	it("clears a local start refusal once another client opens a newer transition", async () => {
		getMock.mockResolvedValue({
			data: { supported: false, targetMode: "tui", reasonCode: "NATIVE_SESSION_MISSING" },
			error: undefined,
		});
		postMock.mockResolvedValue({ data: undefined, error: { code: "NATIVE_SESSION_MISSING" } });
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const { result } = renderHook(() => useSessionInterfaceTransition("session-a"), {
			wrapper: HookWrapper,
		});

		await act(async () => {
			await result.current.start({ targetMode: "tui", policy: "drain" }).catch(() => {});
		});
		await waitFor(() => expect(result.current.startError).toBe("request failed"));

		// Another client starts and progresses the switch; this client's poll picks
		// up the durable row, whose createdAt is newer than the refused attempt.
		getMock.mockResolvedValue({
			data: {
				supported: true,
				targetMode: "tui",
				transition: {
					id: "transition-remote",
					sessionId: "session-a",
					phase: "draining",
					policy: "drain",
					sourceMode: "chat",
					targetMode: "tui",
					createdAt: "2099-01-01T00:00:00Z",
					updatedAt: "2099-01-01T00:00:00Z",
				},
			},
			error: undefined,
		});
		await act(async () => {
			await queryClient.invalidateQueries({
				queryKey: ["session-interface-transition", "session-a"],
			});
		});

		await waitFor(() => expect(result.current.transition?.id).toBe("transition-remote"));
		expect(result.current.startError).toBeUndefined();
		// The stale mutation state is dropped, so the refusal cannot reappear.
		expect(
			queryClient
				.getMutationCache()
				.findAll({ mutationKey: ["start-session-interface-transition"] }),
		).toHaveLength(0);
	});
});

describe("interface switch readiness", () => {
	it("clears settling after navigating away and back during refresh", async () => {
		const refresh = deferred<void>();
		const settled = {
			id: "settled-session-a",
			sessionId: "session-a",
			sourceMode: "tui" as const,
			targetMode: "chat" as const,
			policy: "drain" as const,
			phase: "completed" as const,
			createdAt: "2026-08-28T10:00:00Z",
			updatedAt: "2026-08-28T10:00:01Z",
		};
		getMock.mockImplementation(
			(_path: string, request: { params: { path: { sessionId: string } } }) =>
				Promise.resolve({
					data:
						request.params.path.sessionId === "session-a"
							? { supported: true, targetMode: "tui", transition: settled }
							: { supported: true, targetMode: "chat" },
					error: undefined,
				}),
		);
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		vi.spyOn(queryClient, "invalidateQueries").mockReturnValue(refresh.promise);
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const { result, rerender } = renderHook(
			({ sessionId }) => useSessionInterfaceTransition(sessionId),
			{ initialProps: { sessionId: "session-a" }, wrapper: HookWrapper },
		);

		await waitFor(() => expect(result.current.settling).toBe(true));
		rerender({ sessionId: "session-b" });
		await waitFor(() => expect(result.current.transition).toBeUndefined());
		rerender({ sessionId: "session-a" });
		await waitFor(() =>
			expect(result.current.transition?.id).toBe("settled-session-a"),
		);
		expect(result.current.settling).toBe(true);

		await act(async () => {
			refresh.resolve();
			await refresh.promise;
		});
		await waitFor(() => expect(result.current.settling).toBe(false));
	});

	it("does not let an older refresh clear a restarted session settlement", async () => {
		const firstARefresh = deferred<void>();
		const bRefresh = deferred<void>();
		const secondARefresh = deferred<void>();
		getMock.mockImplementation(
			(_path: string, request: { params: { path: { sessionId: string } } }) => {
				const sessionId = request.params.path.sessionId;
				return Promise.resolve({
					data: {
						supported: true,
						targetMode: "chat",
						transition: {
							id: `settled-${sessionId}`,
							sessionId,
							sourceMode: "tui",
							targetMode: "chat",
							policy: "drain",
							phase: "completed",
							createdAt: "2026-08-28T10:00:00Z",
							updatedAt: "2026-08-28T10:00:01Z",
						},
					},
					error: undefined,
				});
			},
		);
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const invalidate = vi
			.spyOn(queryClient, "invalidateQueries")
			.mockReturnValueOnce(firstARefresh.promise)
			.mockReturnValueOnce(firstARefresh.promise)
			.mockReturnValueOnce(bRefresh.promise)
			.mockReturnValueOnce(bRefresh.promise)
			.mockReturnValueOnce(secondARefresh.promise)
			.mockReturnValueOnce(secondARefresh.promise);
		const HookWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const { result, rerender } = renderHook(
			({ sessionId }) => useSessionInterfaceTransition(sessionId),
			{ initialProps: { sessionId: "session-a" }, wrapper: HookWrapper },
		);

		await waitFor(() => expect(invalidate).toHaveBeenCalledTimes(2));
		rerender({ sessionId: "session-b" });
		await waitFor(() => expect(invalidate).toHaveBeenCalledTimes(4));
		rerender({ sessionId: "session-a" });
		await waitFor(() => expect(invalidate).toHaveBeenCalledTimes(6));
		expect(result.current.settling).toBe(true);

		await act(async () => {
			firstARefresh.resolve();
			await firstARefresh.promise;
		});
		expect(result.current.settling).toBe(true);

		await act(async () => {
			secondARefresh.resolve();
			await secondARefresh.promise;
		});
		await waitFor(() => expect(result.current.settling).toBe(false));
		bRefresh.resolve();
	});

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
