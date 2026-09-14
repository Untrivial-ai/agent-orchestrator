import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const mockMutateCreate = vi.fn();
const mockMutateUpdate = vi.fn();

vi.mock("../../hooks/useWorkflowRoles", () => ({
	useCreateAgentRole: () => ({
		mutate: mockMutateCreate,
		isPending: false,
		isError: false,
		error: null,
	}),
	useUpdateAgentRole: () => ({
		mutate: mockMutateUpdate,
		isPending: false,
		isError: false,
		error: null,
	}),
}));

vi.mock("../../hooks/useProviders", () => ({
	useProviders: () => ({ data: [], isLoading: false, isError: false }),
	useProviderModels: () => ({ data: [], isLoading: false, isError: false }),
}));

vi.mock("react-i18next", () => ({
	useTranslation: () => ({
		t: (key: string, opts?: Record<string, unknown>) => opts?.defaultValue ?? key,
	}),
}));

vi.mock("lucide-react", () => ({
	Plus: () => null,
	XIcon: () => null,
	ChevronDownIcon: () => null,
	ChevronUpIcon: () => null,
	CheckIcon: () => null,
}));

import { AgentRoleDialog } from "./AgentRoleDialog";

const mockRole = {
	id: "role-1",
	name: "code-gen",
	displayName: "Code Generator",
	description: "Generates code",
	enabled: true,
	systemPrompt: "Generate clean code",
	defaultProviderId: "prov-1",
	defaultProviderModelId: "model-1",
	createdAt: "2026-01-01T00:00:00Z",
	updatedAt: "2026-01-01T00:00:00Z",
};

function createWrapper() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
	);
}

beforeEach(() => {
	mockMutateCreate.mockReset();
	mockMutateUpdate.mockReset();
});

describe("AgentRoleDialog", () => {
	it("renders create mode — name input enabled", () => {
		render(<AgentRoleDialog open onOpenChange={vi.fn()} />, { wrapper: createWrapper() });
		expect(screen.getByTestId("role-name-input")).toBeEnabled();
	});

	it("renders edit mode — name input disabled", () => {
		render(<AgentRoleDialog open onOpenChange={vi.fn()} role={mockRole} />, { wrapper: createWrapper() });
		expect(screen.getByTestId("role-name-input")).toBeDisabled();
	});

	it("renders edit mode — name shows existing value", () => {
		render(<AgentRoleDialog open onOpenChange={vi.fn()} role={mockRole} />, { wrapper: createWrapper() });
		expect((screen.getByTestId("role-name-input") as HTMLInputElement).value).toBe("code-gen");
	});

	it("renders system prompt textarea with existing value in edit mode", () => {
		render(<AgentRoleDialog open onOpenChange={vi.fn()} role={mockRole} />, { wrapper: createWrapper() });
		expect((screen.getByTestId("role-prompt-input") as HTMLTextAreaElement).value).toBe("Generate clean code");
	});

	it("submit button disabled when name empty in create mode", () => {
		render(<AgentRoleDialog open onOpenChange={vi.fn()} />, { wrapper: createWrapper() });
		expect(screen.getByTestId("role-dialog-submit")).toBeDisabled();
	});

	it("submit button enabled in edit mode with existing data", () => {
		render(<AgentRoleDialog open onOpenChange={vi.fn()} role={mockRole} />, { wrapper: createWrapper() });
		expect(screen.getByTestId("role-dialog-submit")).toBeEnabled();
	});

	it("calls createRole.mutate on submit in create mode", () => {
		render(<AgentRoleDialog open onOpenChange={vi.fn()} />, { wrapper: createWrapper() });
		fireEvent.change(screen.getByTestId("role-name-input"), { target: { value: "new-role" } });
		screen.getByTestId("role-dialog-submit").click();
		expect(mockMutateCreate).toHaveBeenCalledWith(
			expect.objectContaining({ name: "new-role" }),
			expect.anything(),
		);
	});

	it("calls updateRole.mutate on submit in edit mode", () => {
		render(<AgentRoleDialog open onOpenChange={vi.fn()} role={mockRole} />, { wrapper: createWrapper() });
		screen.getByTestId("role-dialog-submit").click();
		expect(mockMutateUpdate).toHaveBeenCalledWith(
			expect.objectContaining({ displayName: "Code Generator" }),
			expect.anything(),
		);
	});

	it("cancel button calls onOpenChange(false)", () => {
		const onOpenChange = vi.fn();
		render(<AgentRoleDialog open onOpenChange={onOpenChange} />, { wrapper: createWrapper() });
		screen.getByText("workflow.agentRole.cancel").click();
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});

	it("renders provider selector", () => {
		render(<AgentRoleDialog open onOpenChange={vi.fn()} />, { wrapper: createWrapper() });
		expect(screen.getByTestId("provider-selector")).toBeInTheDocument();
	});

	it("renders displayName field", () => {
		render(<AgentRoleDialog open onOpenChange={vi.fn()} />, { wrapper: createWrapper() });
		expect(screen.getByTestId("role-display-name-input")).toBeInTheDocument();
	});

	it("renders description field", () => {
		render(<AgentRoleDialog open onOpenChange={vi.fn()} />, { wrapper: createWrapper() });
		expect(screen.getByTestId("role-description-input")).toBeInTheDocument();
	});
});
