import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AgentModelCombobox, buildModelSearchIndex, searchModelIndex } from "./AgentModelCombobox";

function renderCombobox(
	models: Array<{ id: string; label: string; provider?: string; isDefault?: boolean }>,
	overrides: Partial<React.ComponentProps<typeof AgentModelCombobox>> = {},
) {
	const onChange = vi.fn();
	const onCustom = vi.fn();
	const view = render(
		<AgentModelCombobox
			aria-label="Worker model"
			value=""
			models={models}
			allowCustom
			onChange={onChange}
			onCustom={onCustom}
			{...overrides}
		/>,
	);
	return { ...view, onChange, onCustom };
}

describe("AgentModelCombobox", () => {
	beforeEach(() => window.localStorage.clear());

	it("keeps the model menu closed while its owning operation is pending", async () => {
		renderCombobox([{ id: "gpt-5.6-sol", label: "GPT-5.6 Sol" }], { disabled: true });

		const trigger = screen.getByRole("button", { name: "Worker model" });
		expect(trigger).toBeDisabled();
		await userEvent.click(trigger);
		expect(screen.queryByRole("menuitem")).not.toBeInTheDocument();
	});

	it("uses direct lookup and provider buckets instead of scanning the complete catalog", () => {
		const models = Array.from({ length: 1_400 }, (_, index) => ({
			id: `provider-${index % 4}/model-${index}`,
			label: `Model ${index}`,
			provider: `provider-${index % 4}`,
		}));
		const index = buildModelSearchIndex(models);

		const direct = searchModelIndex(index, "provider-3/model-1399");
		expect(direct.strategy).toBe("direct");
		expect(direct.candidateCount).toBe(1);
		expect(direct.models.map((model) => model.id)).toEqual(["provider-3/model-1399"]);

		const providerSearch = searchModelIndex(index, "provider-3/");
		expect(providerSearch.strategy).toBe("provider-index");
		expect(providerSearch.candidateCount).toBe(350);
		expect(providerSearch.models).toHaveLength(350);
		expect(providerSearch.models.every((model) => model.provider === "provider-3")).toBe(true);
	});

	it("renders only the first 50 models from a large cached catalog", async () => {
		const models = Array.from({ length: 1_397 }, (_, index) => ({
			id: `provider-${index % 4}/model-${index}`,
			label: `Model ${index}`,
			provider: `provider-${index % 4}`,
			isDefault: index === 0,
		}));

		renderCombobox(models);
		await userEvent.click(screen.getByRole("button", { name: "Worker model" }));

		// Agent default and 50 catalog models. The custom-model action appears
		// only after the user types a value that does not match the catalog.
		expect(screen.getAllByRole("menuitem")).toHaveLength(51);
		expect(screen.getByText("Showing 50 of 1,397 matching models — type to narrow")).toBeInTheDocument();
		expect(screen.queryByRole("menuitem", { name: /Model 1000/ })).not.toBeInTheDocument();
	});

	it("keeps compact catalogs free of search and result-count chrome", async () => {
		renderCombobox(
			Array.from({ length: 9 }, (_, index) => ({
				id: `gpt-${index}`,
				label: index === 8 ? "GPT Luna" : `GPT ${index}`,
				provider: "OpenAI",
			})),
			{ allowCustom: false, customModelEntry: "none" },
		);

		await userEvent.click(screen.getByRole("button", { name: "Worker model" }));

		expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
		expect(screen.queryByText(/matching models/)).not.toBeInTheDocument();
	});

	it("uses the search field to enter a direct model ID for a compact catalog", async () => {
		const { onCustom } = renderCombobox([
			{ id: "gpt-5.6-sol", label: "Sol" },
			{ id: "gpt-5.6-luna", label: "Luna" },
		]);

		await userEvent.click(screen.getByRole("button", { name: "Worker model" }));
		const search = screen.getByRole("searchbox", { name: "Search worker model" });
		expect(screen.queryByRole("menuitem", { name: "Enter model ID…" })).not.toBeInTheDocument();
		await userEvent.type(search, "private/model-id");
		await userEvent.click(screen.getByRole("menuitem", { name: "Use “private/model-id” as a custom model" }));

		expect(onCustom).toHaveBeenCalledWith("private/model-id");
	});

	it("adds simple model search at ten models", async () => {
		renderCombobox(
			Array.from({ length: 10 }, (_, index) => ({
				id: index === 8 ? "gpt-luna" : index === 9 ? "claude-fable" : `model-${index}`,
				label: index === 8 ? "Luna" : index === 9 ? "Fable" : `Model ${index}`,
				provider: "OpenAI",
			})),
		);

		await userEvent.click(screen.getByRole("button", { name: "Worker model" }));
		const search = screen.getByRole("searchbox", { name: "Search worker model" });
		expect(search).toHaveAttribute("placeholder", "Search models…");
		await userEvent.type(search, "fab");
		expect(screen.getByRole("menuitem", { name: "Fable" })).toBeInTheDocument();
		expect(screen.queryByRole("menuitem", { name: "Luna" })).not.toBeInTheDocument();
	});

	it("does not offer typed custom models when direct entry is disabled", async () => {
		renderCombobox(
			Array.from({ length: 10 }, (_, index) => ({ id: `model-${index}`, label: `Model ${index}` })),
			{ allowCustom: false },
		);

		await userEvent.click(screen.getByRole("button", { name: "Worker model" }));
		await userEvent.type(screen.getByRole("searchbox", { name: "Search worker model" }), "wrong-model");

		expect(screen.getByText("No matching models.")).toBeInTheDocument();
		expect(screen.queryByText(/Use .* as a custom model/)).not.toBeInTheDocument();
	});

	it("explains how configured models become available and refreshes the catalog", async () => {
		const onRefresh = vi.fn();
		renderCombobox([{ id: "configured/model", label: "Configured model" }], {
			allowCustom: false,
			customModelEntry: "configured",
			agentLabel: "OpenCode",
			onRefresh,
		});

		await userEvent.click(screen.getByRole("button", { name: "Worker model" }));
		expect(screen.getByText("Can’t find your model?")).toBeInTheDocument();
		expect(screen.getByText("Configure the model in OpenCode, then refresh.")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Refresh models" }));
		expect(onRefresh).toHaveBeenCalledOnce();
	});

	it("does not expose free text for fixed model catalogs", async () => {
		renderCombobox([{ id: "account/model", label: "Account model" }], {
			allowCustom: false,
			customModelEntry: "none",
			onRefresh: vi.fn(),
		});

		await userEvent.click(screen.getByRole("button", { name: "Worker model" }));
		expect(screen.getByText("This model isn’t available for this account or agent version.")).toBeInTheDocument();
		expect(screen.queryByRole("menuitem", { name: "Enter model ID…" })).not.toBeInTheDocument();
	});

	it("groups a recent explicit choice immediately after current and default models", async () => {
		const models = [
			{ id: "gpt-5.6-sol", label: "GPT-5.6 Sol", provider: "OpenAI", isDefault: true },
			{ id: "gpt-5.6-terra", label: "GPT-5.6 Terra", provider: "OpenAI" },
			{ id: "gpt-5.6-luna", label: "GPT-5.6 Luna", provider: "OpenAI" },
		];
		const first = renderCombobox(models, { recentScope: "codex" });
		await userEvent.click(screen.getByRole("button", { name: "Worker model" }));
		await userEvent.click(screen.getByRole("menuitem", { name: "GPT-5.6 Luna" }));
		first.unmount();

		renderCombobox(models, { recentScope: "codex" });
		await userEvent.click(screen.getByRole("button", { name: "Worker model" }));

		const groupLabels = screen.getAllByText(/Current & defaults|Recent/).map((node) => node.textContent);
		expect(groupLabels).toEqual(["Current & defaults", "Recent"]);
	});

	it("shows machine IDs only when they disambiguate duplicate model names", async () => {
		renderCombobox([
			{ id: "gpt-5.6-sol", label: "GPT-5.6 Sol", provider: "OpenAI" },
			{ id: "anthropic/opus-standard", label: "Opus", provider: "Anthropic" },
			{ id: "anthropic/opus-long", label: "Opus", provider: "Anthropic" },
		]);

		await userEvent.click(screen.getByRole("button", { name: "Worker model" }));

		expect(screen.queryByText("gpt-5.6-sol")).not.toBeInTheDocument();
		expect(screen.getByText("anthropic/opus-standard")).toBeInTheDocument();
		expect(screen.getByText("anthropic/opus-long")).toBeInTheDocument();
	});

	it("searches the full catalog and groups matching models by provider", async () => {
		const models = Array.from({ length: 100 }, (_, index) => ({
			id: `provider-${index % 2}/model-${index}`,
			label: `Model ${index}`,
			provider: `provider-${index % 2}`,
		}));
		const { onChange } = renderCombobox(models);
		await userEvent.click(screen.getByRole("button", { name: "Worker model" }));
		const search = screen.getByRole("searchbox", { name: "Search worker model" });
		expect(search).toHaveAttribute("placeholder", "Search models or providers…");
		await userEvent.type(search, "provider-1/model-99");

		expect(screen.getByText("provider-1", { selector: "div" })).toBeInTheDocument();
		expect(screen.getByText("Showing 1 of 1 matching models")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("menuitem", { name: /Model 99/ }));
		expect(onChange).toHaveBeenCalledWith("provider-1/model-99");
	});

	it("offers the typed value as a custom model when no catalog model matches", async () => {
		const { onCustom } = renderCombobox(
			Array.from({ length: 10 }, (_, index) => ({
				id: `openai/gpt-${index}`,
				label: `GPT ${index}`,
				provider: "OpenAI",
			})),
		);
		await userEvent.click(screen.getByRole("button", { name: "Worker model" }));
		await userEvent.type(
			screen.getByRole("searchbox", { name: "Search worker model" }),
			"private/custom-model",
		);

		await userEvent.click(screen.getByRole("menuitem", { name: "Use “private/custom-model” as a custom model" }));
		expect(onCustom).toHaveBeenCalledWith("private/custom-model");
	});
});
