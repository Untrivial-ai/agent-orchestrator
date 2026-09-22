import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { MultiStepLoader } from "./multi-step-loader";

const steps = ["Orchestrating", "Coordinating", "Preparing"] as const;

describe("MultiStepLoader", () => {
	it("cycles from the visual animation without scheduling loader timers", () => {
		render(<MultiStepLoader ariaLabel="Session setup activity" className="absolute right-4 top-4" duration={1_000} steps={steps} />);
		const activity = screen.getByRole("status", { name: "Session setup activity" });
		expect(activity).toHaveClass("absolute", "right-4", "top-4");
		expect(within(activity).getByText("Orchestrating")).toHaveClass("text-sm");
		expect(within(activity).getByText("Orchestrating")).toBeInTheDocument();
		expect(within(activity).queryByText("Coordinating")).not.toBeInTheDocument();
		fireEvent(within(activity).getByTestId("multi-step-loader-step"), new window.Event("animationend", { bubbles: true }));
		expect(within(activity).getByText("Coordinating")).toBeInTheDocument();
		fireEvent(within(activity).getByTestId("multi-step-loader-step"), new window.Event("animationend", { bubbles: true }));
		fireEvent(within(activity).getByTestId("multi-step-loader-step"), new window.Event("animationend", { bubbles: true }));
		expect(within(activity).getByText("Orchestrating")).toBeInTheDocument();
	});

	it("advances only when the phrase animation ends", () => {
		render(<MultiStepLoader ariaLabel="Session setup activity" duration={1_000} steps={steps} />);
		const activity = screen.getByRole("status", { name: "Session setup activity" });
		const animatedStep = within(activity).getByTestId("multi-step-loader-step");
		const dot = animatedStep.querySelector<HTMLElement>(".multi-step-loader__dot");
		const check = animatedStep.querySelector<HTMLElement>(".multi-step-loader__check");

		expect(dot).not.toBeNull();
		expect(check).not.toBeNull();
		fireEvent(dot!, new window.Event("animationend", { bubbles: true }));
		fireEvent(check!, new window.Event("animationend", { bubbles: true }));
		expect(within(activity).getByText("Orchestrating")).toBeInTheDocument();

		fireEvent(animatedStep, new window.Event("animationend", { bubbles: true }));
		expect(within(activity).getByText("Coordinating")).toBeInTheDocument();
	});
});
