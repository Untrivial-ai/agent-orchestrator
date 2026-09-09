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
		expect(screen.queryByRole("menuitem", { name: "Default approvals" })).not.toBeInTheDocument();
		expect(screen.queryByRole("menuitem", { name: "Accept edits" })).not.toBeInTheDocument();
		expect(screen.queryByRole("menuitem", { name: "Auto-approve" })).not.toBeInTheDocument();
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
