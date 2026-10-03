import { render, screen } from "@testing-library/react";
import { createRef, forwardRef, type HTMLAttributes } from "react";
import { describe, expect, it, vi } from "vitest";
import { SessionInspectorRail, inspectorSizing } from "./SessionInspectorRail";

vi.mock("motion/react", () => ({
	useReducedMotion: () => false,
	motion: {
		div: forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement> & {
			animate?: unknown;
			initial?: unknown;
			transition?: { duration?: number; type?: string };
			onAnimationComplete?: unknown;
		}>(({ animate: _animate, initial: _initial, onAnimationComplete: _complete, transition, ...props }, ref) =>
			<div {...props} ref={ref} data-motion={transition?.duration === 0 ? "instant" : "animated"} />),
	},
}));

describe("SessionInspectorRail session navigation", () => {
	const splitRef = createRef<HTMLDivElement>();
	const rail = (sessionKey: string, isOpen: boolean) => <SessionInspectorRail
		sessionKey={sessionKey}
		isOpen={isOpen}
		settledClosed={!isOpen}
		onExpand={() => {}}
		sizing={inspectorSizing("summary")}
		splitRef={splitRef}
	><input aria-label="Inspector draft" defaultValue="preserved" /></SessionInspectorRail>;

	it.each([[false, true], [true, false], [true, true], [false, false]])(
		"restores %s → %s across sessions without animating or remounting content",
		(from, to) => {
			const { rerender, container } = render(rail("first", from));
			const draft = screen.getByLabelText("Inspector draft");
			rerender(rail("second", to));
			expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-motion", "instant");
			expect(container.querySelector('[data-slot="inspector-gap"]')).toHaveAttribute("data-motion", "instant");
			expect(screen.getByLabelText("Inspector draft")).toBe(draft);
			expect(draft).toHaveValue("preserved");
			// Background updates must not re-enable the route transition.
			rerender(rail("second", to));
			expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-motion", "instant");
		},
	);

	it("animates explicit toggles in the current session, then snaps on navigation", () => {
		const { rerender, container } = render(rail("first", false));
		rerender(rail("first", true));
		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-motion", "animated");
		expect(container.querySelector('[data-slot="inspector-gap"]')).toHaveAttribute("data-motion", "animated");
		rerender(rail("first", false));
		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-motion", "animated");
		rerender(rail("second", true));
		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-motion", "instant");
		rerender(rail("second", false));
		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-motion", "animated");
	});
});
