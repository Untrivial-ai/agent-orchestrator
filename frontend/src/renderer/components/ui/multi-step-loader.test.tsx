import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { MultiStepLoader } from "./multi-step-loader";

const steps = ["Creating the workspace", "Connecting to the worker", "Preparing your repository and agent", "Connecting your terminal"] as const;

describe("MultiStepLoader", () => {
	it("shows every stage with checks for completed stages and no progress bar", () => {
		const view = render(<MultiStepLoader ariaLabel="Session setup activity" activeIndex={1} duration={1_000} steps={steps} />);
		const activity = screen.getByRole("status", { name: "Session setup activity" });
		const phrase = within(activity).getByTestId("multi-step-loader-step");
		const animatedPhrase = phrase.querySelector(".multi-step-loader__step");
		expect(animatedPhrase).toHaveStyle("--multi-step-loader-duration: 1000ms");
		for (const step of steps) expect(activity).toHaveTextContent(step);
		expect(phrase).toHaveTextContent(steps[1]);
		expect(within(activity).queryByRole("progressbar")).not.toBeInTheDocument();
		expect(activity).not.toHaveTextContent("%");
		expect(within(activity).getAllByTestId("multi-step-loader-check")).toHaveLength(1);
		expect(within(activity).getByTestId("multi-step-loader-active-dot")).toHaveClass("multi-step-loader__dot");
		view.rerender(<MultiStepLoader ariaLabel="Session setup activity" activeIndex={2} duration={1_000} steps={steps} />);
		expect(within(activity).getAllByTestId("multi-step-loader-check")).toHaveLength(2);
		expect(within(activity).getAllByTestId("multi-step-loader-active-dot")).toHaveLength(1);
		view.rerender(<MultiStepLoader ariaLabel="Session setup activity" activeIndex={3} duration={1_000} steps={steps} />);
		expect(within(activity).getAllByTestId("multi-step-loader-check")).toHaveLength(3);
		view.rerender(<MultiStepLoader ariaLabel="Session setup activity" activeIndex={3} complete duration={1_000} steps={steps} />);
		expect(within(activity).getAllByTestId("multi-step-loader-check")).toHaveLength(4);
		expect(within(activity).queryByTestId("multi-step-loader-active-dot")).not.toBeInTheDocument();
	});

	it("changes phrase only when its active stage prop changes", () => {
		const view = render(<MultiStepLoader ariaLabel="Session setup activity" activeIndex={0} steps={steps} />);
		const shimmer = screen.getByTestId("multi-step-loader-step").querySelector(".multi-step-loader__shimmer");
		view.rerender(<MultiStepLoader ariaLabel="Session setup activity" activeIndex={0} steps={steps} />);
		expect(screen.getByTestId("multi-step-loader-step").querySelector(".multi-step-loader__shimmer")).toBe(shimmer);
		fireEvent(screen.getByTestId("multi-step-loader-step"), new window.Event("animationend", { bubbles: true }));
		expect(screen.getByTestId("multi-step-loader-step")).toHaveTextContent(steps[0]);
		view.rerender(<MultiStepLoader ariaLabel="Session setup activity" activeIndex={2} steps={steps} />);
		expect(screen.getByTestId("multi-step-loader-step")).toHaveTextContent(steps[2]);
	});
});
