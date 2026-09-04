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
		await user.click(screen.getByRole("menuitem", { name: "Sonnet 5" }));
		expect(onChange).toHaveBeenCalledWith("model", { value: "sonnet" });
	});

	it("shows Codex's distinct native and explicit permission choices", async () => {
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
			"Provider configuration",
		);
		await user.click(screen.getByRole("button", { name: "Approval policy for the next turn" }));
		expect(screen.getByRole("menuitem", { name: "Accept Edits" })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Auto" })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Bypass Permissions" })).toBeInTheDocument();
		expect(screen.queryByRole("menuitem", { name: "Default approvals" })).not.toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Manual" })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Don't Ask" })).toBeInTheDocument();
		expect(screen.queryByRole("menuitem", { name: "Auto-approve" })).not.toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Provider configuration" })).toBeInTheDocument();

		await user.click(screen.getByRole("menuitem", { name: "Auto" }));
		expect(onChange).toHaveBeenCalledWith({ approvalMode: "auto" });
	});

	it.each([['Manual', 'manual'], ["Don't Ask", 'dont-ask']])("sends explicit Codex %s without changing the model", async (label, value) => {
		const onChange = vi.fn();
		const user = userEvent.setup();
		render(<TurnSettingsBar harness="codex" models={[]} settings={{ model: "chosen" }} onChange={onChange} />);
		await user.click(screen.getByRole("button", { name: "Approval policy for the next turn" }));
		await user.click(screen.getByRole("menuitem", { name: label }));
		expect(onChange).toHaveBeenCalledWith({ model: "chosen", approvalMode: value });
	});

	it("shows unsupported ACP policies disabled and preserves native selection values", async () => {
		const onChange = vi.fn();
		const user = userEvent.setup();
		render(<TurnSettingsBar models={[]} settings={{}} configOptions={[OPTIONS[2]]} onChangeConfigOption={onChange} />);
		await user.click(screen.getByRole("button", { name: "Permission mode" }));
		expect(screen.getAllByRole("menuitem").map((item) => item.textContent)).toEqual(["Auto", "Manual", "Accept Edits", "Don't Ask", "Bypass Permissions"]);
		const unavailable = screen.getByRole("menuitem", { name: "Don't Ask" });
		expect(unavailable).toHaveAttribute("aria-disabled", "true");
		await user.click(unavailable);
		expect(onChange).not.toHaveBeenCalled();
		await user.click(screen.getByRole("menuitem", { name: "Manual" }));
		expect(onChange).toHaveBeenCalledWith("mode", { value: "manual" });
	});

	it.each(["kimchi", "omp"])("uses %s native yolo under the shared bypass action", async (harness) => {
		const onChange = vi.fn();
		const user = userEvent.setup();
		render(<TurnSettingsBar harness={harness} models={[]} settings={{}} configOptions={[{ ...OPTIONS[2], currentValue: "default", choices: [{ value: "default", name: "Configured policy" }, { value: "yolo", name: "YOLO" }] }]} onChangeConfigOption={onChange} />);
		await user.click(screen.getByRole("button", { name: "Permission mode" }));
		await user.click(screen.getByRole("menuitem", { name: "Bypass Permissions" }));
		expect(onChange).toHaveBeenCalledWith("mode", { value: "yolo" });
	});

	it("renders an omitted permission choice array as unavailable policies", async () => {
		const user = userEvent.setup();
		render(<TurnSettingsBar harness="claude-code" models={[]} settings={{}} configOptions={[{ ...OPTIONS[2], choices: undefined } as unknown as ChatConfigOption]} onChangeConfigOption={vi.fn()} />);
		await user.click(screen.getByRole("button", { name: "Permission mode" }));
		expect(screen.getAllByRole("menuitem")).toHaveLength(5);
		for (const item of screen.getAllByRole("menuitem")) expect(item).toHaveAttribute("aria-disabled", "true");
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
			"Provider configuration",
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
			"Bypass Permissions",
		);
	});
	it("distinguishes Codex bypass permissions from its native defaults", () => {
		render(
			<TurnSettingsBar
				harness="codex"
				models={[]}
				settings={{ approvalMode: "bypass-permissions" }}
				onChange={vi.fn()}
			/>,
		);

		expect(screen.getByRole("button", { name: "Approval policy for the next turn" })).toHaveTextContent(
			"Bypass Permissions",
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
