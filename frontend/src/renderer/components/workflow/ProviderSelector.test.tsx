import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

vi.mock("react-i18next", () => ({
	useTranslation: () => ({
		t: (key: string, opts?: Record<string, unknown>) => opts?.defaultValue ?? key,
	}),
}));

vi.mock("lucide-react", () => ({
	ChevronDownIcon: () => null,
	ChevronUpIcon: () => null,
	CheckIcon: () => null,
}));

vi.mock("../../hooks/useProviders", () => ({
	useProviders: () => ({
		data: [
			{ id: "prov-1", displayName: "OpenAI", enabled: true },
			{ id: "prov-2", displayName: "Anthropic", enabled: true },
			{ id: "prov-3", displayName: "Disabled Provider", enabled: false },
		],
		isLoading: false,
		isError: false,
	}),
	useProviderModels: (providerId: string) => ({
		data: providerId === "prov-1"
			? [
					{ id: "model-1", displayName: "GPT-4", modelName: "gpt-4", enabled: true },
					{ id: "model-2", displayName: "GPT-3.5", modelName: "gpt-3.5", enabled: true },
				]
			: [],
		isLoading: false,
		isError: false,
	}),
}));

import { ProviderSelector } from "./ProviderSelector";

function createWrapper() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
	);
}

describe("ProviderSelector", () => {
	it("renders provider and model selects", () => {
		render(
			<ProviderSelector
				providerId=""
				modelId=""
				onProviderChange={vi.fn()}
				onModelChange={vi.fn()}
			/>,
			{ wrapper: createWrapper() },
		);
		expect(screen.getByTestId("provider-select")).toBeInTheDocument();
		expect(screen.getByTestId("model-select")).toBeInTheDocument();
	});

	it("model select disabled when no provider selected", () => {
		render(
			<ProviderSelector
				providerId=""
				modelId=""
				onProviderChange={vi.fn()}
				onModelChange={vi.fn()}
			/>,
			{ wrapper: createWrapper() },
		);
		expect(screen.getByTestId("model-select")).toBeDisabled();
	});

	it("renders without validation error when both empty", () => {
		render(
			<ProviderSelector
				providerId=""
				modelId=""
				onProviderChange={vi.fn()}
				onModelChange={vi.fn()}
			/>,
			{ wrapper: createWrapper() },
		);
		expect(screen.queryByText("workflow.agentRole.pairRequired")).not.toBeInTheDocument();
	});

	it("shows validation error when only provider set", () => {
		render(
			<ProviderSelector
				providerId="prov-1"
				modelId=""
				onProviderChange={vi.fn()}
				onModelChange={vi.fn()}
			/>,
			{ wrapper: createWrapper() },
		);
		expect(screen.getByText("workflow.agentRole.pairRequired")).toBeInTheDocument();
	});

	it("shows validation error when only model set", () => {
		render(
			<ProviderSelector
				providerId=""
				modelId="model-1"
				onProviderChange={vi.fn()}
				onModelChange={vi.fn()}
			/>,
			{ wrapper: createWrapper() },
		);
		expect(screen.getByText("workflow.agentRole.pairRequired")).toBeInTheDocument();
	});

	it("no validation error when both set", () => {
		render(
			<ProviderSelector
				providerId="prov-1"
				modelId="model-1"
				onProviderChange={vi.fn()}
				onModelChange={vi.fn()}
			/>,
			{ wrapper: createWrapper() },
		);
		expect(screen.queryByText("workflow.agentRole.pairRequired")).not.toBeInTheDocument();
	});

	it("both selects disabled when disabled prop is true", () => {
		render(
			<ProviderSelector
				providerId="prov-1"
				modelId="model-1"
				onProviderChange={vi.fn()}
				onModelChange={vi.fn()}
				disabled
			/>,
			{ wrapper: createWrapper() },
		);
		expect(screen.getByTestId("provider-select")).toBeDisabled();
		expect(screen.getByTestId("model-select")).toBeDisabled();
	});
});
