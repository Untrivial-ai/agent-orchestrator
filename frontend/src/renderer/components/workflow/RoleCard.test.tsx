import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

vi.mock("react-i18next", () => ({
	useTranslation: () => ({
		t: (key: string, opts?: Record<string, unknown>) => opts?.defaultValue ?? key,
	}),
}));

vi.mock("../../hooks/useWorkflowRoles", () => ({
	useSetAgentRoleEnabled: vi.fn(() => ({
		mutate: vi.fn(),
		isPending: false,
	})),
}));

vi.mock("lucide-react", () => ({
	Plus: () => null,
}));

import { RoleCard } from "./RoleCard";

function createWrapper() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
	);
}

const baseRole = {
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

describe("RoleCard", () => {
	it("renders role displayName", () => {
		render(<RoleCard role={baseRole} onEdit={vi.fn()} />, { wrapper: createWrapper() });
		expect(screen.getByText("Code Generator")).toBeInTheDocument();
	});

	it("shows enabled badge when enabled", () => {
		render(<RoleCard role={baseRole} onEdit={vi.fn()} />, { wrapper: createWrapper() });
		expect(screen.getByText("status.role.enabled")).toBeInTheDocument();
	});

	it("shows disabled badge when disabled", () => {
		render(<RoleCard role={{ ...baseRole, enabled: false }} onEdit={vi.fn()} />, { wrapper: createWrapper() });
		expect(screen.getByText("status.role.disabled")).toBeInTheDocument();
	});

	it("shows provider/model when configured", () => {
		render(<RoleCard role={baseRole} onEdit={vi.fn()} />, { wrapper: createWrapper() });
		expect(screen.getByText("prov-1 / model-1")).toBeInTheDocument();
	});

	it("shows noDefault when no provider configured", () => {
		render(
			<RoleCard
				role={{ ...baseRole, defaultProviderId: "", defaultProviderModelId: "" }}
				onEdit={vi.fn()}
			/>,
			{ wrapper: createWrapper() },
		);
		expect(screen.getByText("workflow.agentRole.noDefault")).toBeInTheDocument();
	});

	it("calls onEdit when edit button clicked", () => {
		const onEdit = vi.fn();
		render(<RoleCard role={baseRole} onEdit={onEdit} />, { wrapper: createWrapper() });
		screen.getByTestId("role-edit-role-1").click();
		expect(onEdit).toHaveBeenCalled();
	});

	it("renders description", () => {
		render(<RoleCard role={baseRole} onEdit={vi.fn()} />, { wrapper: createWrapper() });
		expect(screen.getByText("Generates code")).toBeInTheDocument();
	});

	it("renders name when displayName differs", () => {
		render(<RoleCard role={baseRole} onEdit={vi.fn()} />, { wrapper: createWrapper() });
		expect(screen.getByText("code-gen")).toBeInTheDocument();
	});
});
