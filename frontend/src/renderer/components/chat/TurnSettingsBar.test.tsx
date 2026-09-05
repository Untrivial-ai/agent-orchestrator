import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { ChatConfigOption } from "../../types/conversation";
import { TurnSettingsBar } from "./TurnSettingsBar";

const OPTIONS: ChatConfigOption[] = [
	{
		id: "model",
		name: "Model",
		category: "model",
		type: "select",
		currentValue: "opus",
		choices: [
			{ value: "opus", name: "Opus 5" },
			{ value: "sonnet", name: "Sonnet 5" },
		],
	},
	{
		id: "effort",
		name: "Effort",
		category: "thought_level",
		type: "select",
		currentValue: "high",
		choices: [{ value: "high", name: "High" }],
	},
	{
		id: "mode",
		name: "Permission mode",
		category: "mode",
		type: "select",
		currentValue: "bypass",
		choices: [
			{ value: "plan", name: "Plan Mode" },
			{ value: "manual", name: "Manual" },
			{ value: "bypass", name: "Bypass Permissions" },
		],
	},
	{
		id: "fast",
		name: "Fast mode",
		type: "boolean",
		currentBoolean: false,
		choices: [],
	},
	{
		id: "agent",
		name: "Agent",
		type: "select",
		currentValue: "reviewer",
		choices: [{ value: "reviewer", name: "Code reviewer" }],
	},
];

describe("ACP session config options", () => {
	it("keeps model, effort, and provider mode explicit while hiding ACP agent internals", async () => {
		const user = userEvent.setup();
		render(
			<TurnSettingsBar
				models={[]}
				settings={{}}
				configOptions={OPTIONS}
				onChangeConfigOption={vi.fn()}
			/>,
		);

		const tools = screen.getByRole("group", { name: "Turn settings" });
		expect(
			within(tools).getByRole("button", { name: "Model and reasoning effort for the next turn" }),
		).toHaveTextContent("Opus 5 High");
		expect(within(tools).getByRole("button", { name: "Permission mode" })).toHaveTextContent(
			"Bypass Permissions",
		);
		expect(within(tools).queryByRole("button", { name: "Fast mode" })).not.toBeInTheDocument();
		expect(within(tools).queryByRole("button", { name: "Agent" })).not.toBeInTheDocument();
		expect(screen.queryByText("Default")).not.toBeInTheDocument();
		expect(screen.queryByText("Provider default")).not.toBeInTheDocument();

		await user.click(
			screen.getByRole("button", { name: "Model and reasoning effort for the next turn" }),
		);
		expect(screen.getByText("Model")).toBeInTheDocument();
		expect(screen.getByText("Effort")).toBeInTheDocument();
		expect(screen.getByRole("switch", { name: "Plan Mode" })).toBeInTheDocument();
		expect(screen.getByRole("switch", { name: "Fast mode" })).toBeInTheDocument();
		expect(screen.queryByText("Agent")).not.toBeInTheDocument();
		expect(screen.queryByText("More")).not.toBeInTheDocument();
	});

	it("maps Agent Mode back to the provider's Manual value", async () => {
		const onChange = vi.fn();
		const user = userEvent.setup();
		render(
			<TurnSettingsBar
				models={[]}
				settings={{}}
				configOptions={[OPTIONS[0], { ...OPTIONS[2], currentValue: "plan" }]}
				onChangeConfigOption={onChange}
			/>,
		);

		await user.click(screen.getByRole("button", { name: "Model and reasoning effort for the next turn" }));
		await user.click(screen.getByRole("switch", { name: "Plan Mode" }));
		expect(onChange).toHaveBeenCalledWith("mode", { value: "manual" });
	});

	it("keeps a select-based Fast Mode beside Plan Mode instead of nesting it under More", async () => {
		const onChange = vi.fn();
		const user = userEvent.setup();
		render(
			<TurnSettingsBar
				models={[]}
				settings={{}}
				configOptions={[
					OPTIONS[0],
					OPTIONS[2],
					{
						id: "fast-mode",
						name: "Fast mode",
						type: "select",
						currentValue: "off",
						choices: [
							{ value: "on", name: "On" },
							{ value: "off", name: "Off" },
						],
					},
				]}
				onChangeConfigOption={onChange}
			/>,
		);

		await user.click(screen.getByRole("button", { name: "Model and reasoning effort for the next turn" }));
		expect(screen.getByRole("switch", { name: "Plan Mode" })).toBeInTheDocument();
		expect(screen.getByRole("switch", { name: "Fast mode" })).toBeInTheDocument();
		expect(screen.queryByText("More")).not.toBeInTheDocument();
		await user.click(screen.getByRole("switch", { name: "Fast mode" }));
		expect(onChange).toHaveBeenCalledWith("fast-mode", { value: "on" });
	});

	it("keeps renamed boolean provider options beside the execution mode", async () => {
		const user = userEvent.setup();
		render(
			<TurnSettingsBar
				models={[]}
				settings={{}}
				configOptions={[
					OPTIONS[0],
					OPTIONS[2],
					{ id: "turbo", name: "Turbo", type: "boolean", currentBoolean: false, choices: [] },
				]}
				onChangeConfigOption={vi.fn()}
			/>,
		);

		await user.click(screen.getByRole("button", { name: "Model and reasoning effort for the next turn" }));
		expect(screen.getByRole("switch", { name: "Turbo" })).toBeInTheDocument();
		expect(screen.queryByText("More")).not.toBeInTheDocument();
	});

	it("keeps unclassified provider options accessible", async () => {
		const user = userEvent.setup();
		render(
			<TurnSettingsBar
				models={[]}
				settings={{}}
				configOptions={[
					OPTIONS[0],
					{
						id: "verbosity",
						name: "Verbosity",
						type: "select",
						currentValue: "high",
						choices: [
							{ value: "low", name: "Low" },
							{ value: "high", name: "High" },
						],
					},
				]}
				onChangeConfigOption={vi.fn()}
			/>,
		);

		await user.click(screen.getByRole("button", { name: "Model and reasoning effort for the next turn" }));
		expect(screen.getByText("More")).toBeInTheDocument();
	});

	it("disables provider controls while a catalog-replacing change is in flight", () => {
		render(
			<TurnSettingsBar
				models={[]}
				settings={{}}
				configOptions={[OPTIONS[0]]}
				configPending
				onChangeConfigOption={vi.fn()}
			/>,
		);

		expect(screen.getByRole("button", { name: "Model" })).toBeDisabled();
	});

	it("hides permissions while the provider is in plan mode", () => {
		render(
			<TurnSettingsBar
				models={[]}
				settings={{}}
				configOptions={[OPTIONS[0], { ...OPTIONS[2], currentValue: "plan" }]}
				onChangeConfigOption={vi.fn()}
			/>,
		);

		expect(screen.getByRole("button", { name: "Model and reasoning effort for the next turn" })).toHaveTextContent("Opus 5");
		expect(screen.queryByRole("button", { name: "Permission mode" })).not.toBeInTheDocument();
	});

	it("keeps plan and agent modes out of the permissions menu", async () => {
		const user = userEvent.setup();
		render(
			<TurnSettingsBar
				models={[]}
				settings={{}}
				configOptions={[OPTIONS[2]]}
				onChangeConfigOption={vi.fn()}
			/>,
		);

		await user.click(screen.getByRole("button", { name: "Permission mode" }));
		expect(screen.getByRole("menuitem", { name: "Manual" })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Bypass Permissions" })).toBeInTheDocument();
		expect(screen.queryByRole("menuitem", { name: "Plan Mode" })).not.toBeInTheDocument();
		expect(screen.queryByRole("menuitem", { name: "Agent Mode" })).not.toBeInTheDocument();
	});

	it("sends the provider's opaque value id when a selection changes", async () => {
		const onChange = vi.fn();
		const user = userEvent.setup();
		render(
			<TurnSettingsBar
				models={[]}
				settings={{}}
				configOptions={[OPTIONS[0]]}
				onChangeConfigOption={onChange}
			/>,
		);

		await user.click(screen.getByRole("button", { name: "Model" }));
		screen.getByRole("menuitem", { name: "Sonnet 5 (provider alias)" }).focus();
 await user.keyboard("{Enter}");
		expect(onChange).toHaveBeenCalledWith("model", { value: "sonnet" });
	});

	it("shows Codex's three native permission choices", async () => {
		const user = userEvent.setup();
		const onChange = vi.fn();
		render(
			<TurnSettingsBar
				harness="codex"
				models={[]}
				settings={{}}
				onChange={onChange}
			/>,
		);

		expect(screen.getByRole("button", { name: "Approval policy for the next turn" })).toHaveTextContent(
			"Full access",
		);
		await user.click(screen.getByRole("button", { name: "Approval policy for the next turn" }));
		expect(screen.getByRole("menuitem", { name: "Ask for approval" })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Approve for me" })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Bypass permissions" })).toBeInTheDocument();
		expect(screen.queryByRole("menuitem", { name: "Default approvals" })).not.toBeInTheDocument();
		expect(screen.queryByRole("menuitem", { name: "Accept edits" })).not.toBeInTheDocument();
		expect(screen.queryByRole("menuitem", { name: "Auto-approve" })).not.toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Full access" })).toBeInTheDocument();

		await user.click(screen.getByRole("menuitem", { name: "Approve for me" }));
		expect(onChange).toHaveBeenCalledWith({ approvalMode: "auto" });
	});

	it("keeps Codex native model+effort in one trigger when the provider has no catalog", () => {
		render(
			<TurnSettingsBar
				harness="codex"
				models={[
					{ id: "gpt-5.6-terra", displayName: "gpt-5.6-terra", default: true, efforts: ["high"] },
				]}
				settings={{ model: "gpt-5.6-terra", reasoningEffort: "high" }}
				onChange={vi.fn()}
			/>,
		);

		expect(
			screen.getByRole("button", { name: "Model and reasoning effort for the next turn" }),
		).toHaveTextContent("gpt-5.6-terra High");
		expect(screen.getByRole("button", { name: "Approval policy for the next turn" })).toHaveTextContent(
			"Full access",
		);
	});

	it("preserves an unlisted model through catalog refresh without borrowing default effort", async () => {
		const user = userEvent.setup();
		const props = { settings: { model: "gpt-6-astra" }, onChange: vi.fn() };
		const { rerender } = render(<TurnSettingsBar {...props} models={[]} />);
		const trigger = () => screen.getByRole("button", { name: "Model and reasoning effort for the next turn" });
		expect(trigger()).toHaveTextContent("gpt-6-astra");
		rerender(<TurnSettingsBar {...props} models={[{ id: "terra", displayName: "Terra", default: true, efforts: ["high"], defaultEffort: "high" }]} />);
		expect(trigger()).toHaveTextContent("gpt-6-astra");
		expect(trigger()).not.toHaveTextContent("High");
		await user.click(trigger());
		expect(screen.queryByRole("menuitem", { name: /^Effort/ })).not.toBeInTheDocument();
	});

	it("does not claim a catalog default is the native configured model", () => {
		render(<TurnSettingsBar models={[{ id: "terra", displayName: "Terra", default: true, defaultEffort: "high", efforts: ["high"] }]} settings={{}} onChange={vi.fn()} />);
		expect(screen.getByRole("button", { name: "Model and reasoning effort for the next turn" })).toHaveTextContent("Provider default");
		expect(screen.getByRole("button", { name: "Model and reasoning effort for the next turn" })).not.toHaveTextContent("High");
	});

	it("identifies provider substitution for an unlisted request", () => {
		render(<TurnSettingsBar models={[]} settings={{ model: "gpt-6-astra", reasoningEffort: "high" }} reroute={{ fromModel: "gpt-6-astra", toModel: "terra", reason: "capacity", at: "2026-09-05T00:00:00Z" }} onChange={vi.fn()} />);
		const trigger = screen.getByRole("button", { name: "Model and reasoning effort for the next turn" });
		expect(trigger).toHaveTextContent("terra High");
		expect(trigger).toHaveAttribute("title", "The provider answered with terra instead of gpt-6-astra: capacity");
	});

	it("labels bypass permission policy plainly", () => {
		render(
			<TurnSettingsBar
				models={[]}
				settings={{ approvalMode: "bypass-permissions" }}
				onChange={vi.fn()}
			/>,
		);

		expect(screen.getByRole("button", { name: "Approval policy for the next turn" })).toHaveTextContent(
			"Bypass permissions",
		);
	});
	it("distinguishes Codex bypass permissions from its default full-access posture", () => {
		render(
			<TurnSettingsBar
				harness="codex"
				models={[]}
				settings={{ approvalMode: "bypass-permissions" }}
				onChange={vi.fn()}
			/>,
		);

		expect(screen.getByRole("button", { name: "Approval policy for the next turn" })).toHaveTextContent(
			"Bypass permissions",
		);
	});
	it("keeps a lone extra option as its own picker rather than inventing a model menu", () => {
		render(
			<TurnSettingsBar
				models={[]}
				settings={{}}
				configOptions={[OPTIONS[3]]}
				onChangeConfigOption={vi.fn()}
			/>,
		);

		expect(screen.getByRole("button", { name: "Fast mode" })).toHaveTextContent("Off");
		expect(
			screen.queryByRole("button", { name: "Model and reasoning effort for the next turn" }),
		).not.toBeInTheDocument();
	});
});
