import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { TurnPlan } from "./TurnPlan";
import type { ConversationPlan } from "../../types/conversation";

// The plan is the one panel that changes while the reader is looking at it, so the
// things worth pinning are: every step's state is available to assistive tech rather
// than only as a colour, and the panel is absent rather than empty when the agent
// made no plan.

const plan: ConversationPlan = {
	explanation: "Land the store first so the endpoint has something to read.",
	steps: [
		{ text: "Land the conversation store", status: "completed" },
		{ text: "Run the backend tests", status: "in_progress" },
		{ text: "Add the snapshot endpoint", status: "pending" },
	],
};

describe("TurnPlan", () => {
	it("renders the steps as a list with the plan's own explanation", () => {
		render(<TurnPlan plan={plan} />);
		expect(screen.getByRole("list")).toBeInTheDocument();
		expect(screen.getAllByRole("listitem")).toHaveLength(3);
		expect(screen.getByText(/Land the store first/)).toBeInTheDocument();
	});

	it("counts what is done, so progress is readable without reading every row", () => {
		render(<TurnPlan plan={plan} />);
		expect(screen.getByText("1/3")).toBeInTheDocument();
	});

	it("states each step's status in text, not only as a mark", () => {
		render(<TurnPlan plan={plan} />);
		expect(screen.getByLabelText("Land the conversation store — done")).toBeInTheDocument();
		expect(screen.getByLabelText("Run the backend tests — in progress")).toBeInTheDocument();
		expect(screen.getByLabelText("Add the snapshot endpoint — not started")).toBeInTheDocument();
	});

	it("says the plan can still change while the turn runs", () => {
		render(<TurnPlan plan={plan} live />);
		expect(screen.getByLabelText("still planning")).toBeInTheDocument();
	});

	// A finished plan is not still planning, even on a turn that is technically live.
	it("stops claiming to be planning once every step is done", () => {
		render(
			<TurnPlan
				plan={{ steps: [{ text: "Only step", status: "completed" }] }}
				live
			/>,
		);
		expect(screen.queryByLabelText("still planning")).not.toBeInTheDocument();
	});

	it("renders nothing for a plan with no steps", () => {
		const { container } = render(<TurnPlan plan={{ steps: [] }} />);
		expect(container).toBeEmptyDOMElement();
	});

	it("omits the explanation when the provider gave none", () => {
		render(<TurnPlan plan={{ steps: plan.steps }} />);
		expect(screen.queryByText(/Land the store first/)).not.toBeInTheDocument();
	});

	it("collapses the plan body when the header icon is clicked", async () => {
		const user = userEvent.setup();
		render(<TurnPlan plan={plan} />);

		expect(screen.getByRole("list")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Collapse plan" }));
		expect(screen.queryByRole("list")).not.toBeInTheDocument();
		expect(screen.queryByText(/Land the store first/)).not.toBeInTheDocument();
		expect(screen.getByText("1/3")).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Expand plan" }));
		expect(screen.getByRole("list")).toBeInTheDocument();
		expect(screen.getByText(/Land the store first/)).toBeInTheDocument();
	});
});
