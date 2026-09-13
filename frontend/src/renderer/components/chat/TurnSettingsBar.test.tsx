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
	it.each(["ao-plan-project-1", "agents/plan-reviewer", "my_plan_agent"])(
		"does not treat custom agent %s as native Plan Mode",
		async (custom) => {
			const user = userEvent.setup();
			const onChange = vi.fn();
			const view = (currentValue: string) => (
				<TurnSettingsBar models={[]} settings={{}} onChangeConfigOption={onChange}
					configOptions={[OPTIONS[0], { ...OPTIONS[2], currentValue, choices: [
						{ value: custom, name: custom },
						{ value: "build", name: "Build" },
						{ value: "plan", name: "Plan" },
					] }]} />
			);
			const { rerender } = render(view(custom));
			await user.click(screen.getByRole("button", { name: "Model and reasoning effort for the next turn" }));
			expect(screen.getByRole("switch", { name: "Plan Mode" })).not.toBeChecked();
			await user.click(screen.getByRole("switch", { name: "Plan Mode" }));
			expect(onChange).toHaveBeenLastCalledWith("mode", { value: "plan" });
			rerender(view("plan"));
			expect(screen.getByRole("switch", { name: "Plan Mode" })).toBeChecked();
			await user.click(screen.getByRole("switch", { name: "Plan Mode" }));
			expect(onChange).toHaveBeenLastCalledWith("mode", { value: "build" });
		},
	);

	it("keeps OpenCode Plan Mode reversible through its Build mode", async () => {
		const user = userEvent.setup();
		const onChange = vi.fn();
		const mode: ChatConfigOption = {
			id: "mode",
			name: "Session Mode",
			category: "mode",
			type: "select",
			currentValue: "build",
			choices: [
				{ value: "build", name: "build" },
				{ value: "agents/custom", name: "agents/custom" },
				{ value: "plan", name: "plan" },
			],
		};
		const view = (currentValue: string) => (
			<TurnSettingsBar
				models={[]}
				settings={{}}
				configOptions={[OPTIONS[0], { ...mode, currentValue }]}
				onChangeConfigOption={onChange}
			/>
		);
		const { rerender } = render(view("build"));
		await user.click(screen.getByRole("button", { name: "Model and reasoning effort for the next turn" }));
		expect(screen.getByRole("switch", { name: "Plan Mode" })).not.toBeChecked();
		await user.click(screen.getByRole("switch", { name: "Plan Mode" }));
		expect(onChange).toHaveBeenLastCalledWith("mode", { value: "plan" });
		rerender(view("plan"));
		expect(screen.getByRole("switch", { name: "Plan Mode" })).toBeChecked();
		await user.keyboard("{Escape}");
		await user.click(screen.getByRole("button", { name: "Model and reasoning effort for the next turn" }));
		await user.click(screen.getByRole("switch", { name: "Plan Mode" }));
		expect(onChange).toHaveBeenLastCalledWith("mode", { value: "build" });
		rerender(view("build"));
		expect(screen.getByRole("switch", { name: "Plan Mode" })).not.toBeChecked();
	});

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
		await user.click(screen.getByRole("menuitem", { name: /Model/ }));
		expect(screen.getByRole("menuitemradio", { name: "Opus 5", checked: true })).toBeInTheDocument();
		expect(screen.getByRole("menuitemradio", { name: "Sonnet 5", checked: false })).toBeInTheDocument();
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
		expect(screen.getByRole("menuitemradio", { name: "Manual" })).toBeInTheDocument();
		expect(screen.getByRole("menuitemradio", { name: "Bypass Permissions" })).toBeInTheDocument();
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
		await user.click(screen.getByRole("menuitemradio", { name: "Sonnet 5" }));
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
		expect(screen.getByRole("menuitemradio", { name: "Ask for approval" })).toBeInTheDocument();
		expect(screen.getByRole("menuitemradio", { name: "Approve for me" })).toBeInTheDocument();
		expect(screen.getByRole("menuitemradio", { name: "Bypass permissions" })).toBeInTheDocument();
		expect(screen.queryByRole("menuitemradio", { name: "Default approvals" })).not.toBeInTheDocument();
		expect(screen.queryByRole("menuitemradio", { name: "Accept edits" })).not.toBeInTheDocument();
		expect(screen.queryByRole("menuitemradio", { name: "Auto-approve" })).not.toBeInTheDocument();
		expect(screen.getByRole("menuitemradio", { name: "Full access" })).toBeInTheDocument();

		await user.click(screen.getByRole("menuitemradio", { name: "Approve for me" }));
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

describe("remember project permissions", () => {
	it("keeps choosing a session policy separate from remembering the confirmed policy", async () => {
		const user = userEvent.setup();
		const onChange = vi.fn();
		const remember = vi.fn();
		const { rerender } = render(<TurnSettingsBar models={[]} harness="codex"
			settings={{ approvalMode: "auto" }} onChange={onChange} onRememberPermissions={remember} />);
		await user.click(screen.getByRole("button", { name: "Approval policy for the next turn" }));
		await user.click(screen.getByRole("menuitemradio", { name: "Full access" }));
		expect(onChange).toHaveBeenCalledWith({ approvalMode: "default" });
		expect(remember).not.toHaveBeenCalled();
		rerender(<TurnSettingsBar models={[]} harness="codex"
			settings={{ approvalMode: "default" }} onChange={onChange} onRememberPermissions={remember} />);
		await user.click(screen.getByRole("button", { name: "Approval policy for the next turn" }));
		await user.click(screen.getByRole("menuitem", { name: "Remember for this project" }));
		expect(remember).toHaveBeenCalledWith("default");
	});

	it("remembers a provider choice only through its daemon-supplied permission mapping", async () => {
		const user = userEvent.setup();
		const remember = vi.fn();
		render(<TurnSettingsBar models={[]} settings={{ approvalMode: "auto" }}
			configOptions={[{ ...OPTIONS[2], choices: [
				{ value: "bypass", name: "Bypass Permissions", permissionMode: "bypass-permissions" },
			] }]} onChangeConfigOption={vi.fn()} onRememberPermissions={remember} />);
		await user.click(screen.getByRole("button", { name: "Permission mode" }));
		await user.click(screen.getByRole("menuitem", { name: "Remember for this project" }));
		expect(remember).toHaveBeenCalledWith("bypass-permissions");
	});

	it("does not substitute a stale AO mode for an unmapped provider choice", async () => {
		const user = userEvent.setup();
		render(<TurnSettingsBar models={[]} settings={{ approvalMode: "auto" }}
			configOptions={[OPTIONS[2]]} onChangeConfigOption={vi.fn()} onRememberPermissions={vi.fn()} />);
		await user.click(screen.getByRole("button", { name: "Permission mode" }));
		expect(screen.queryByRole("menuitem", { name: "Remember for this project" })).not.toBeInTheDocument();
	});

	it("disables overlapping writes and shows save results", () => {
		const props = { models: [], settings: {}, onChange: vi.fn(), onRememberPermissions: vi.fn() };
		const { rerender } = render(<TurnSettingsBar {...props} rememberPermissionsPending />);
		expect(screen.getByRole("button", { name: "Approval policy for the next turn" })).toBeDisabled();
		expect(screen.getByRole("status")).toHaveTextContent("Saving project default");
		rerender(<TurnSettingsBar {...props} rememberPermissionsError="Could not save project default" />);
		expect(screen.getByRole("alert")).toHaveTextContent("Could not save project default");
		expect(screen.getByRole("button", { name: "Approval policy for the next turn" })).toBeEnabled();
		rerender(<TurnSettingsBar {...props} rememberedPermissionMode="default" />);
		expect(screen.getByRole("status")).toHaveTextContent("saved for new sessions in this project");
	});

	it("shows success only for the exact saved native permission mode", () => {
		const props = { models: [], onChange: vi.fn(), onRememberPermissions: vi.fn() };
		const { rerender } = render(<TurnSettingsBar {...props} settings={{ approvalMode: "auto" }} rememberedPermissionMode="auto" />);
		expect(screen.getByRole("status")).toHaveTextContent("saved for new sessions");
		rerender(<TurnSettingsBar {...props} settings={{ approvalMode: "default" }} rememberedPermissionMode="auto" />);
		expect(screen.queryByRole("status")).not.toBeInTheDocument();
	});

	it("matches saved success against the provider mode instead of stale native settings", () => {
		const props = { models: [], settings: { approvalMode: "auto" as const }, onChangeConfigOption: vi.fn(), onRememberPermissions: vi.fn() };
		const option: ChatConfigOption = { ...OPTIONS[2], currentValue: "auto", choices: [
			{ value: "auto", name: "Auto", permissionMode: "auto" },
			{ value: "manual", name: "Manual", permissionMode: "default" },
			{ value: "unknown", name: "Unknown" },
		] };
		const { rerender } = render(<TurnSettingsBar {...props} configOptions={[option]} rememberedPermissionMode="auto" />);
		expect(screen.getByRole("status")).toHaveTextContent("saved for new sessions");
		rerender(<TurnSettingsBar {...props} configOptions={[option]} rememberedPermissionMode="auto" configPending />);
		expect(screen.queryByRole("status")).not.toBeInTheDocument();
		for (const currentValue of ["manual", "unknown"]) {
			rerender(<TurnSettingsBar {...props} configOptions={[{ ...option, currentValue }]} rememberedPermissionMode="auto" />);
			expect(screen.queryByRole("status")).not.toBeInTheDocument();
		}
	});

});

describe("native model selection", () => {
	it("keeps an explicit model visible when the catalog does not contain it", () => {
		render(
			<TurnSettingsBar
				models={[
					{ id: "astra", displayName: "Astra", default: true, efforts: ["high"], defaultEffort: "high" },
				]}
				settings={{ model: "nano" }}
				onChange={vi.fn()}
				harness="codex"
			/>,
		);
		expect(
			screen.getByRole("button", { name: "Model and reasoning effort for the next turn" }),
		).toHaveTextContent(/^nano$/);
	});
});

describe("Cursor Ask and Agent chat modes", () => {
	// Values are deliberately not lowercase: AO must round-trip whatever the
	// provider advertised, never a value re-derived from the label.
	const CURSOR_MODE: ChatConfigOption = {
		id: "mode",
		name: "Chat mode",
		category: "mode",
		type: "select",
		currentValue: "ASK",
		choices: [
			{ value: "ASK", name: "Ask" },
			{ value: "AGENT", name: "Agent" },
		],
	};

	it("treats Ask and Agent as execution modes when one option advertises the pair", async () => {
		const user = userEvent.setup();
		render(
			<TurnSettingsBar
				harness="cursor"
				models={[]}
				settings={{}}
				configOptions={[CURSOR_MODE]}
				onChangeConfigOption={vi.fn()}
			/>,
		);

		const trigger = screen.getByRole("button", { name: "Model mode for the next turn" });
		await user.click(trigger);
		expect(screen.getByRole("menuitemradio", { name: "Ask" })).toBeInTheDocument();
		expect(screen.getByRole("menuitemradio", { name: "Agent" })).toBeInTheDocument();
		expect(screen.queryByRole("switch", { name: "Plan Mode" })).not.toBeInTheDocument();
	});

	it("shows the provider's current choice, starting on Ask", () => {
		render(
			<TurnSettingsBar
				harness="cursor"
				models={[]}
				settings={{}}
				configOptions={[CURSOR_MODE]}
				onChangeConfigOption={vi.fn()}
			/>,
		);

		expect(screen.getByRole("button", { name: "Model mode for the next turn" })).toHaveTextContent(
			"Ask",
		);
	});

	it("shows Agent on the trigger once the provider reports Agent", () => {
		render(
			<TurnSettingsBar
				harness="cursor"
				models={[]}
				settings={{}}
				configOptions={[{ ...CURSOR_MODE, currentValue: "AGENT" }]}
				onChangeConfigOption={vi.fn()}
			/>,
		);

		expect(screen.getByRole("button", { name: "Model mode for the next turn" })).toHaveTextContent(
			"Agent",
		);
	});

	it("sends each mode's exact advertised value rather than a normalized label", async () => {
		const onChange = vi.fn();
		const user = userEvent.setup();
		render(
			<TurnSettingsBar
				harness="cursor"
				models={[]}
				settings={{}}
				configOptions={[CURSOR_MODE]}
				onChangeConfigOption={onChange}
			/>,
		);

		await user.click(screen.getByRole("button", { name: "Model mode for the next turn" }));
		await user.click(screen.getByRole("menuitemradio", { name: "Agent" }));
		expect(onChange).toHaveBeenCalledWith("mode", { value: "AGENT" });

		await user.click(screen.getByRole("button", { name: "Model mode for the next turn" }));
		await user.click(screen.getByRole("menuitemradio", { name: "Ask" }));
		expect(onChange).toHaveBeenLastCalledWith("mode", { value: "ASK" });
	});

	it("exposes exactly one execution control for the Ask/Agent pair", () => {
		render(
			<TurnSettingsBar
				harness="cursor"
				models={[]}
				settings={{}}
				configOptions={[CURSOR_MODE]}
				onChangeConfigOption={vi.fn()}
			/>,
		);

		expect(screen.getAllByRole("button", { name: "Model mode for the next turn" })).toHaveLength(1);
		expect(screen.queryByRole("button", { name: "Chat mode" })).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Approval policy for the next turn" }),
		).not.toBeInTheDocument();
	});

	it("keeps Ask out of the approval menu, which stays AO's own policy list", async () => {
		const user = userEvent.setup();
		render(
			<TurnSettingsBar
				harness="cursor"
				models={[]}
				settings={{}}
				configOptions={[CURSOR_MODE]}
				onChange={vi.fn()}
				onChangeConfigOption={vi.fn()}
			/>,
		);

		const approvals = screen.getByRole("button", { name: "Approval policy for the next turn" });
		await user.click(approvals);
		expect(screen.queryByRole("menuitemradio", { name: "Ask" })).not.toBeInTheDocument();
		expect(screen.queryByRole("menuitemradio", { name: "Agent" })).not.toBeInTheDocument();
		expect(screen.getByRole("menuitemradio", { name: "Default approvals" })).toBeInTheDocument();
		expect(screen.getByRole("menuitemradio", { name: "Accept edits" })).toBeInTheDocument();
		expect(screen.getByRole("menuitemradio", { name: "Auto-approve" })).toBeInTheDocument();
		expect(screen.getByRole("menuitemradio", { name: "Bypass permissions" })).toBeInTheDocument();
	});

	it("does not read a lone approval-flavoured Ask as an execution mode", async () => {
		const user = userEvent.setup();
		render(
			<TurnSettingsBar
				models={[]}
				settings={{}}
				configOptions={[
					{
						id: "mode",
						name: "Permission mode",
						category: "mode",
						type: "select",
						currentValue: "ask",
						choices: [
							{ value: "ask", name: "Ask for approval" },
							{ value: "auto", name: "Approve for me" },
						],
					},
				]}
				onChangeConfigOption={vi.fn()}
			/>,
		);

		expect(
			screen.queryByRole("button", { name: "Model mode for the next turn" }),
		).not.toBeInTheDocument();
		const trigger = screen.getByRole("button", { name: "Permission mode" });
		expect(trigger).toHaveTextContent("Ask for approval");
		await user.click(trigger);
		expect(screen.getByRole("menuitemradio", { name: "Ask for approval" })).toBeInTheDocument();
		expect(screen.getByRole("menuitemradio", { name: "Approve for me" })).toBeInTheDocument();
	});

	it("keeps the Plan/Agent switch and its Manual wire value unchanged", async () => {
		const onChange = vi.fn();
		const user = userEvent.setup();
		render(
			<TurnSettingsBar
				models={[]}
				settings={{}}
				configOptions={[OPTIONS[2]]}
				onChangeConfigOption={onChange}
			/>,
		);

		expect(screen.getByRole("button", { name: "Permission mode" })).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Model mode for the next turn" }));
		const planSwitch = screen.getByRole("switch", { name: "Plan Mode" });
		expect(planSwitch).not.toBeChecked();
		await user.click(planSwitch);
		expect(onChange).toHaveBeenCalledWith("mode", { value: "plan" });
	});

	it("does not confuse a provider-owned agent option with AO's Switch agent", async () => {
		const user = userEvent.setup();
		render(
			<TurnSettingsBar
				harness="cursor"
				models={[]}
				settings={{}}
				configOptions={[CURSOR_MODE, OPTIONS[4]]}
				onChangeConfigOption={vi.fn()}
			/>,
		);

		expect(screen.queryByRole("button", { name: "Agent" })).not.toBeInTheDocument();
		expect(screen.queryByText("Code reviewer")).not.toBeInTheDocument();
		const trigger = screen.getByRole("button", { name: "Model mode for the next turn" });
		expect(trigger).toHaveTextContent("Ask");
		await user.click(trigger);
		expect(screen.queryByRole("menuitemradio", { name: "Code reviewer" })).not.toBeInTheDocument();
	});
});

// Captured from a live `cursor-agent acp` session/new response: Cursor advertises
// three postures in one mode option, alongside its own model catalog.
describe("Cursor's live Agent/Plan/Ask mode catalog", () => {
	const CURSOR_MODES: ChatConfigOption = {
		id: "mode",
		name: "Mode",
		category: "mode",
		type: "select",
		currentValue: "agent",
		choices: [
			{ value: "agent", name: "Agent", description: "Full agent capabilities with tool access" },
			{ value: "plan", name: "Plan", description: "Read-only mode for planning" },
			{ value: "ask", name: "Ask", description: "Q&A mode - no edits or command execution" },
		],
	};
	const CURSOR_MODELS: ChatConfigOption = {
		id: "model",
		name: "Model",
		category: "model",
		type: "select",
		currentValue: "grok-4.6[effort=high,fast=true]",
		choices: [
			{ value: "default[]", name: "Auto" },
			{ value: "grok-4.6[effort=high,fast=true]", name: "grok-4.6" },
		],
	};

	it("keeps the mode control visible beside the model picker rather than nested in it", async () => {
		const user = userEvent.setup();
		render(
			<TurnSettingsBar
				harness="cursor"
				models={[]}
				settings={{}}
				configOptions={[CURSOR_MODELS, CURSOR_MODES]}
				onChangeConfigOption={vi.fn()}
			/>,
		);

		const modeTrigger = screen.getByRole("button", { name: "Model mode for the next turn" });
		expect(modeTrigger).toHaveTextContent("Agent");
		expect(screen.getByRole("button", { name: "Model" })).toHaveTextContent("grok-4.6");

		await user.click(modeTrigger);
		expect(screen.getByRole("menuitemradio", { name: "Agent" })).toBeInTheDocument();
		expect(screen.getByRole("menuitemradio", { name: "Plan" })).toBeInTheDocument();
		expect(screen.getByRole("menuitemradio", { name: "Ask" })).toBeInTheDocument();
	});

	it("sends Cursor's own mode ids, including the bracketed model ids untouched", async () => {
		const onChange = vi.fn();
		const user = userEvent.setup();
		render(
			<TurnSettingsBar
				harness="cursor"
				models={[]}
				settings={{}}
				configOptions={[CURSOR_MODELS, CURSOR_MODES]}
				onChangeConfigOption={onChange}
			/>,
		);

		await user.click(screen.getByRole("button", { name: "Model mode for the next turn" }));
		await user.click(screen.getByRole("menuitemradio", { name: "Ask" }));
		expect(onChange).toHaveBeenCalledWith("mode", { value: "ask" });

		await user.click(screen.getByRole("button", { name: "Model mode for the next turn" }));
		await user.click(screen.getByRole("menuitemradio", { name: "Agent" }));
		expect(onChange).toHaveBeenLastCalledWith("mode", { value: "agent" });

		await user.click(screen.getByRole("button", { name: "Model" }));
		await user.click(screen.getByRole("menuitemradio", { name: "grok-4.6" }));
		expect(onChange).toHaveBeenLastCalledWith("model", {
			value: "grok-4.6[effort=high,fast=true]",
		});
	});

	it("shows Ask on the trigger when Cursor reports Ask as current", () => {
		render(
			<TurnSettingsBar
				harness="cursor"
				models={[]}
				settings={{}}
				configOptions={[CURSOR_MODELS, { ...CURSOR_MODES, currentValue: "ask" }]}
				onChangeConfigOption={vi.fn()}
			/>,
		);

		expect(screen.getByRole("button", { name: "Model mode for the next turn" })).toHaveTextContent(
			"Ask",
		);
	});

	it("names the option rather than asserting a posture the provider did not report", () => {
		render(
			<TurnSettingsBar
				harness="cursor"
				models={[]}
				settings={{}}
				configOptions={[{ ...CURSOR_MODES, currentValue: undefined }]}
				onChangeConfigOption={vi.fn()}
			/>,
		);

		const trigger = screen.getByRole("button", { name: "Model mode for the next turn" });
		expect(trigger).toHaveTextContent("Mode");
		expect(trigger).not.toHaveTextContent("Agent Mode");
	});

	it("does not claim a posture when the current value is a permission shown elsewhere", () => {
		render(
			<TurnSettingsBar
				models={[]}
				settings={{}}
				configOptions={[
					{
						id: "mode",
						name: "Chat mode",
						category: "mode",
						type: "select",
						currentValue: "bypass",
						choices: [
							{ value: "agent", name: "Agent" },
							{ value: "ask", name: "Ask" },
							{ value: "plan", name: "Plan" },
							{ value: "bypass", name: "Bypass Permissions" },
						],
					},
				]}
				onChangeConfigOption={vi.fn()}
			/>,
		);

		const trigger = screen.getByRole("button", { name: "Model mode for the next turn" });
		expect(trigger).toHaveTextContent("Chat mode");
		expect(trigger).not.toHaveTextContent("Agent Mode");
		expect(screen.getByRole("button", { name: "Chat mode" })).toHaveTextContent(
			"Bypass Permissions",
		);
	});

	it("keeps an unclassified option reachable when a mode picker is the only other control", async () => {
		const user = userEvent.setup();
		render(
			<TurnSettingsBar
				harness="cursor"
				models={[]}
				settings={{}}
				configOptions={[
					CURSOR_MODES,
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

		expect(screen.getByRole("button", { name: "Model mode for the next turn" })).toHaveTextContent(
			"Agent",
		);
		const extra = screen.getByRole("button", { name: "Verbosity" });
		expect(extra).toHaveTextContent("High");
		await user.click(extra);
		expect(screen.getByRole("menuitemradio", { name: "Low" })).toBeInTheDocument();
	});

	it("disables the standalone mode trigger and a lone extra while a change is in flight", () => {
		render(
			<TurnSettingsBar
				harness="cursor"
				models={[]}
				settings={{}}
				configOptions={[
					CURSOR_MODES,
					{
						id: "verbosity",
						name: "Verbosity",
						type: "select",
						currentValue: "high",
						choices: [{ value: "low", name: "Low" }],
					},
				]}
				configPending
				onChangeConfigOption={vi.fn()}
			/>,
		);

		expect(screen.getByRole("button", { name: "Model mode for the next turn" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Verbosity" })).toBeDisabled();
	});

	it("offers exactly one execution control and no provider approval picker", () => {
		render(
			<TurnSettingsBar
				harness="cursor"
				models={[]}
				settings={{}}
				configOptions={[CURSOR_MODELS, CURSOR_MODES]}
				onChangeConfigOption={vi.fn()}
			/>,
		);

		expect(screen.getAllByRole("button", { name: "Model mode for the next turn" })).toHaveLength(1);
		expect(screen.queryByRole("button", { name: "Mode" })).not.toBeInTheDocument();
		expect(screen.queryByRole("switch", { name: "Plan Mode" })).not.toBeInTheDocument();
	});
});


describe("preventive read-only permissions", () => {
 it("offers read-only only when the provider declares support", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  const { rerender } = render(<TurnSettingsBar models={[]} harness="codex" settings={{ approvalMode: "auto" }} onChange={onChange} />);
  await user.click(screen.getByRole("button", { name: "Approval policy for the next turn" }));
  expect(screen.queryByRole("menuitemradio", { name: "Read-only" })).not.toBeInTheDocument();
  await user.keyboard("{Escape}");
  rerender(<TurnSettingsBar models={[]} harness="codex" settings={{ approvalMode: "auto" }} onChange={onChange} supportsReadOnly />);
  await user.click(screen.getByRole("button", { name: "Approval policy for the next turn" }));
  await user.click(screen.getByRole("menuitemradio", { name: "Read-only" }));
  expect(onChange).toHaveBeenCalledWith({ approvalMode: "read-only" });
 });
 it("keeps a read-only session restricted even while capabilities refresh", async () => {
  const user = userEvent.setup();
  render(<TurnSettingsBar models={[]} harness="codex" permissions="read-only" settings={{ approvalMode: "read-only" }} onChange={vi.fn()} />);
  await user.click(screen.getByRole("button", { name: "Approval policy for the next turn" }));
  expect(screen.getAllByRole("menuitemradio")).toHaveLength(1);
  expect(screen.getByRole("menuitemradio", { name: "Read-only" })).toBeChecked();
 });
});
