import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { AgentEffortSelect, effortLabel } from "./AgentEffortSelect";

describe("AgentEffortSelect", () => {
	// Effort is a per-model capability. Sonnet 4.5 and Haiku 4.5 accept none at
	// all, so the control must be absent rather than empty or disabled — an
	// effort dropdown on a model that ignores effort is a promise the agent
	// will not keep.
	it("renders nothing when the model advertises no effort levels", () => {
		const { container } = render(
			<AgentEffortSelect value="" efforts={undefined} onChange={vi.fn()} />,
		);
		expect(container).toBeEmptyDOMElement();

		const empty = render(<AgentEffortSelect value="" efforts={[]} onChange={vi.fn()} />);
		expect(empty.container).toBeEmptyDOMElement();
	});

	// The levels come from the provider per model, so the control must show
	// exactly what was passed and never a fixed list.
	it("offers only the levels the provider reported for this model", async () => {
		const user = userEvent.setup();
		render(
			<AgentEffortSelect
				aria-label="Reasoning effort"
				value=""
				efforts={["low", "medium", "high"]}
				onChange={vi.fn()}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Reasoning effort" }));

		expect(await screen.findByRole("menuitem", { name: "Low" })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Medium" })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "High" })).toBeInTheDocument();
		// Opus 4.6 omits xhigh; it must not appear just because other models have it.
		expect(screen.queryByRole("menuitem", { name: "Extra high" })).not.toBeInTheDocument();
		expect(screen.queryByRole("menuitem", { name: "Max" })).not.toBeInTheDocument();
	});

	// Choosing nothing is a real option — it leaves whatever default the agent
	// would pick — so it has to be reachable, not just the initial state.
	it("offers the agent default as an explicit choice", async () => {
		const user = userEvent.setup();
		const onChange = vi.fn();
		render(
			<AgentEffortSelect
				aria-label="Reasoning effort"
				value="high"
				efforts={["low", "medium", "high"]}
				onChange={onChange}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Reasoning effort" }));
		await user.click(await screen.findByRole("menuitem", { name: "Agent default" }));
		expect(onChange).toHaveBeenCalledWith("");
	});

	it("passes the provider's own spelling through unchanged", async () => {
		const user = userEvent.setup();
		const onChange = vi.fn();
		render(
			<AgentEffortSelect
				aria-label="Reasoning effort"
				value=""
				efforts={["low", "xhigh", "max"]}
				onChange={onChange}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Reasoning effort" }));
		await user.click(await screen.findByRole("menuitem", { name: "Extra high" }));
		// The label is cosmetic; the value is what reaches `claude --effort`.
		expect(onChange).toHaveBeenCalledWith("xhigh");
	});
});

describe("effortLabel", () => {
	it("reads naturally without altering the underlying value", () => {
		expect(effortLabel("low")).toBe("Low");
		expect(effortLabel("medium")).toBe("Medium");
		expect(effortLabel("high")).toBe("High");
		expect(effortLabel("max")).toBe("Max");
		// "Xhigh" reads as a typo.
		expect(effortLabel("xhigh")).toBe("Extra high");
	});

	it("does not choke on an unfamiliar level", () => {
		expect(effortLabel("ultra")).toBe("Ultra");
		expect(effortLabel("")).toBe("");
	});
});
