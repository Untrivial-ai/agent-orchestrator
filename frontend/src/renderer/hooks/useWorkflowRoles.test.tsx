import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

const { getMock, postMock, patchMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	postMock: vi.fn(),
	patchMock: vi.fn(),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock, PATCH: patchMock },
	hasTrustedApiBaseUrl: vi.fn(() => true),
	apiErrorMessage: (e: unknown) => (e instanceof Error ? e.message : "error"),
}));

import {
	useAgentRoles,
	useAgentRole,
	useCreateAgentRole,
	useUpdateAgentRole,
	useSetAgentRoleEnabled,
} from "./useWorkflowRoles";
import { useProviders, useProviderModels } from "./useProviders";
import { useAssignTask } from "./useAssignTask";
import { workflowQueryKeys } from "./useWorkflowPlans";

const mockRole = {
	id: "role-1",
	name: "code-gen",
	displayName: "Code Generator",
	description: "Generates code",
	enabled: true,
	createdAt: "2026-01-01T00:00:00Z",
	updatedAt: "2026-01-01T00:00:00Z",
};

const mockProvider = {
	id: "prov-1",
	displayName: "OpenAI",
	baseUrl: "https://api.openai.com",
	apiProtocol: "openai",
	enabled: true,
	secretConfigured: true,
	createdAt: "2026-01-01T00:00:00Z",
	updatedAt: "2026-01-01T00:00:00Z",
};

const mockModel = {
	id: "model-1",
	providerId: "prov-1",
	modelName: "gpt-4",
	displayName: "GPT-4",
	enabled: true,
	sortOrder: 0,
	createdAt: "2026-01-01T00:00:00Z",
	updatedAt: "2026-01-01T00:00:00Z",
};

function createWrapper() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false, retryDelay: 0 } },
	});
	return {
		queryClient,
		wrapper: ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		),
	};
}

beforeEach(() => {
	getMock.mockReset();
	postMock.mockReset();
	patchMock.mockReset();
});

// ══════════════════════════════════════════════════════════════════════
// useAgentRoles
// ══════════════════════════════════════════════════════════════════════
describe("useAgentRoles", () => {
	it("fetches all roles", async () => {
		getMock.mockResolvedValueOnce({ data: { roles: [mockRole] }, error: undefined });
		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useAgentRoles(), { wrapper });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(getMock).toHaveBeenCalledWith("/api/v1/workflow/roles");
		expect(result.current.data).toHaveLength(1);
	});

	it("handles empty list", async () => {
		getMock.mockResolvedValueOnce({ data: { roles: [] }, error: undefined });
		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useAgentRoles(), { wrapper });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toEqual([]);
	});

	it("handles error", async () => {
		getMock.mockResolvedValueOnce({ data: undefined, error: { message: "server error" } });
		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useAgentRoles(), { wrapper });
		await waitFor(() => expect(result.current.isError).toBe(true));
		expect(result.current.error?.message).toBe("error");
	});
});

// ══════════════════════════════════════════════════════════════════════
// useAgentRole
// ══════════════════════════════════════════════════════════════════════
describe("useAgentRole", () => {
	it("fetches single role", async () => {
		getMock.mockResolvedValueOnce({ data: mockRole, error: undefined });
		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useAgentRole("role-1"), { wrapper });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(getMock).toHaveBeenCalledWith("/api/v1/workflow/roles/{id}", {
			params: { path: { id: "role-1" } },
		});
	});

	it("disabled when roleId is null", () => {
		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useAgentRole(null), { wrapper });
		expect(result.current.fetchStatus).toBe("idle");
	});
});

// ══════════════════════════════════════════════════════════════════════
// useCreateAgentRole
// ══════════════════════════════════════════════════════════════════════
describe("useCreateAgentRole", () => {
	it("sends POST with correct body", async () => {
		postMock.mockResolvedValueOnce({ data: mockRole, error: undefined });
		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCreateAgentRole(), { wrapper });
		await result.current.mutateAsync({ name: "code-gen" });
		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/roles", {
			body: { name: "code-gen" },
		});
	});

	it("invalidates roles on success", async () => {
		postMock.mockResolvedValue({ data: mockRole, error: undefined });
		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCreateAgentRole(), { wrapper });
		await result.current.mutateAsync({ name: "code-gen" });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
	});

	it("handles error", async () => {
		postMock.mockResolvedValueOnce({ data: undefined, error: { message: "conflict" } });
		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCreateAgentRole(), { wrapper });
		await expect(result.current.mutateAsync({ name: "dup" })).rejects.toThrow();
	});
});

// ══════════════════════════════════════════════════════════════════════
// useUpdateAgentRole
// ══════════════════════════════════════════════════════════════════════
describe("useUpdateAgentRole", () => {
	it("sends PATCH with correct body", async () => {
		patchMock.mockResolvedValueOnce({ data: mockRole, error: undefined });
		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useUpdateAgentRole("role-1"), { wrapper });
		await result.current.mutateAsync({ displayName: "Updated" });
		expect(patchMock).toHaveBeenCalledWith("/api/v1/workflow/roles/{id}", {
			params: { path: { id: "role-1" } },
			body: { displayName: "Updated" },
		});
	});

	it("handles error", async () => {
		patchMock.mockResolvedValueOnce({ data: undefined, error: { message: "not found" } });
		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useUpdateAgentRole("role-1"), { wrapper });
		await expect(result.current.mutateAsync({ displayName: "X" })).rejects.toThrow();
	});
});

// ══════════════════════════════════════════════════════════════════════
// useSetAgentRoleEnabled
// ══════════════════════════════════════════════════════════════════════
describe("useSetAgentRoleEnabled", () => {
	it("sends PATCH with enabled flag", async () => {
		patchMock.mockResolvedValueOnce({ data: { ...mockRole, enabled: false }, error: undefined });
		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useSetAgentRoleEnabled("role-1"), { wrapper });
		await result.current.mutateAsync(false);
		expect(patchMock).toHaveBeenCalledWith("/api/v1/workflow/roles/{id}/enabled", {
			params: { path: { id: "role-1" } },
			body: { enabled: false },
		});
	});
});

// ══════════════════════════════════════════════════════════════════════
// useProviders
// ══════════════════════════════════════════════════════════════════════
describe("useProviders", () => {
	it("fetches providers", async () => {
		getMock.mockResolvedValueOnce({ data: { providers: [mockProvider] }, error: undefined });
		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useProviders(), { wrapper });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(getMock).toHaveBeenCalledWith("/api/v1/providers");
		expect(result.current.data).toHaveLength(1);
	});

	it("handles empty list", async () => {
		getMock.mockResolvedValueOnce({ data: { providers: [] }, error: undefined });
		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useProviders(), { wrapper });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toEqual([]);
	});
});

// ══════════════════════════════════════════════════════════════════════
// useProviderModels
// ══════════════════════════════════════════════════════════════════════
describe("useProviderModels", () => {
	it("fetches models for provider", async () => {
		const { queryClient, wrapper } = createWrapper();
		queryClient.setQueryData(workflowQueryKeys.providerModels("prov-1"), [mockModel]);
		const { result } = renderHook(() => useProviderModels("prov-1"), { wrapper });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toHaveLength(1);
		expect(result.current.data![0].id).toBe("model-1");
	});

	it("disabled when providerId is empty", () => {
		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useProviderModels(""), { wrapper });
		expect(result.current.fetchStatus).toBe("idle");
	});
});

// ══════════════════════════════════════════════════════════════════════
// useAssignTask
// ══════════════════════════════════════════════════════════════════════
describe("useAssignTask", () => {
	it("sends PATCH with assignment body", async () => {
		patchMock.mockResolvedValueOnce({ data: {}, error: undefined });
		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useAssignTask("task-1", "stage-1"), { wrapper });
		await result.current.mutateAsync({
			agentRoleId: "role-1",
			providerId: "prov-1",
			providerModelId: "model-1",
		});
		expect(patchMock).toHaveBeenCalledWith("/api/v1/workflow/tasks/{id}/assignment", {
			params: { path: { id: "task-1" } },
			body: { agentRoleId: "role-1", providerId: "prov-1", providerModelId: "model-1" },
		});
	});

	it("handles error", async () => {
		patchMock.mockResolvedValueOnce({ data: undefined, error: { message: "fail" } });
		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useAssignTask("task-1", "stage-1"), { wrapper });
		await expect(
			result.current.mutateAsync({ agentRoleId: "", providerId: "", providerModelId: "" }),
		).rejects.toThrow();
	});
});
