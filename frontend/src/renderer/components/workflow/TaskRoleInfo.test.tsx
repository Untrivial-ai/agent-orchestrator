import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

const mockRole = {
	id: "role-1",
	name: "code-gen",
	displayName: "Code Generator",
	description: "Generates code",
	enabled: true,
	systemPrompt: "Write clean, well-documented code following best practices",
	defaultProviderId: "prov-1",
	defaultProviderModelId: "model-1",
	createdAt: "2026-01-01T00:00:00Z",
	updatedAt: "2026-01-01T00:00:00Z",
};

const mockRoleDisabled = { ...mockRole, id: "role-2", enabled: false };
const mockRoleNoProvider = { ...mockRole, id: "role-3", defaultProviderId: undefined, defaultProviderModelId: undefined, systemPrompt: undefined };

vi.mock("../../hooks/useWorkflowRoles", () => ({
	useAgentRole: (roleId: string | null) => ({
		data: roleId === "role-1" ? mockRole : roleId === "role-2" ? mockRoleDisabled : roleId === "role-3" ? mockRoleNoProvider : undefined,
		isLoading: false,
		isError: false,
	}),
}));

vi.mock("../../hooks/useProviders", () => ({
	useProviders: () => ({
		data: [
			{ id: "prov-1", displayName: "OpenAI", enabled: true },
			{ id: "prov-2", displayName: "Anthropic", enabled: true },
		],
		isLoading: false,
		isError: false,
	}),
}));

vi.mock("react-i18next", () => ({
	useTranslation: () => ({
		t: (key: string, opts?: Record<string, unknown>) => opts?.defaultValue ?? key,
	}),
}));

import { TaskRoleInfo } from "./TaskRoleInfo";

function createWrapper() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
	);
}

describe("TaskRoleInfo", () => {
	it("renders nothing when no data provided", () => {
		const { container } = render(<TaskRoleInfo />, { wrapper: createWrapper() });
		expect(container.firstChild).toBeNull();
	});

	it("renders role display name from role query", () => {
		render(<TaskRoleInfo agentRoleId="role-1" />, { wrapper: createWrapper() });
		expect(screen.getByTestId("role-info-name")).toHaveTextContent("Code Generator");
	});

	it("falls back to role name when displayName is missing", () => {
		render(<TaskRoleInfo agentRoleId="role-1" />, { wrapper: createWrapper() });
		expect(screen.getByTestId("role-info-name")).toHaveTextContent("Code Generator");
	});

	it("falls back to agentRoleId when role not found", () => {
		render(<TaskRoleInfo agentRoleId="nonexistent" />, { wrapper: createWrapper() });
		expect(screen.getByTestId("role-info-name")).toHaveTextContent("nonexistent");
	});

	it("shows disabled badge when role is disabled", () => {
		render(<TaskRoleInfo agentRoleId="role-2" />, { wrapper: createWrapper() });
		expect(screen.getByTestId("role-info-disabled-badge")).toBeInTheDocument();
	});

	it("does not show disabled badge when role is enabled", () => {
		render(<TaskRoleInfo agentRoleId="role-1" />, { wrapper: createWrapper() });
		expect(screen.queryByTestId("role-info-disabled-badge")).not.toBeInTheDocument();
	});

	it("renders provider display name from providers list", () => {
		render(<TaskRoleInfo providerId="prov-1" />, { wrapper: createWrapper() });
		expect(screen.getByTestId("role-info-provider")).toHaveTextContent("OpenAI");
	});

	it("falls back to providerId when provider not found", () => {
		render(<TaskRoleInfo providerId="unknown-prov" />, { wrapper: createWrapper() });
		expect(screen.getByTestId("role-info-provider")).toHaveTextContent("unknown-prov");
	});

	it("shows taskOverride source when providerId is set", () => {
		render(<TaskRoleInfo agentRoleId="role-1" providerId="prov-1" providerModelId="model-1" />, { wrapper: createWrapper() });
		expect(screen.getByTestId("provider-source-badge")).toHaveTextContent("workflow.task.providerSource.taskOverride");
	});

	it("shows roleDefault source when no task provider but role has default", () => {
		render(<TaskRoleInfo agentRoleId="role-1" />, { wrapper: createWrapper() });
		expect(screen.getByTestId("provider-source-badge")).toHaveTextContent("workflow.task.providerSource.roleDefault");
	});

	it("shows systemDefault source when no task provider and role has no default", () => {
		render(<TaskRoleInfo agentRoleId="role-3" />, { wrapper: createWrapper() });
		expect(screen.getByTestId("provider-source-badge")).toHaveTextContent("workflow.task.providerSource.systemDefault");
	});

	it("shows systemDefault source when no role assigned and no provider", () => {
		render(<TaskRoleInfo />, { wrapper: createWrapper() });
		expect(screen.queryByTestId("provider-source-badge")).not.toBeInTheDocument();
	});

	it("renders system prompt summary", () => {
		render(<TaskRoleInfo agentRoleId="role-1" />, { wrapper: createWrapper() });
		expect(screen.getByTestId("role-info-prompt")).toHaveTextContent(/Write clean/);
	});

	it("does not render prompt when role has no systemPrompt", () => {
		render(<TaskRoleInfo agentRoleId="role-3" />, { wrapper: createWrapper() });
		expect(screen.queryByTestId("role-info-prompt")).not.toBeInTheDocument();
	});

	it("renders model ID", () => {
		render(<TaskRoleInfo providerModelId="gpt-4o" />, { wrapper: createWrapper() });
		expect(screen.getByTestId("role-info-model")).toHaveTextContent("gpt-4o");
	});
});
