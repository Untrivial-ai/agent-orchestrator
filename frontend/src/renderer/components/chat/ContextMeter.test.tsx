import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { ContextMeter } from "./ContextMeter";
import type { ConversationRateLimits, ConversationUsage } from "../../types/conversation";

// The behaviour under test is the threshold encoding, because that is what carries
// the meaning: a user glances at the fill and the colour, and only reads the digits
// if something looks wrong. The numbers here are the real ones a live codex
// app-server reported (18055 of a 258400 window; a pro account at 71% of a 7 day
// rate limit window).

function usage(over: Partial<ConversationUsage> = {}): ConversationUsage {
	return {
		contextUsed: 18055,
		contextWindow: 258400,
		inputTokens: 18050,
		outputTokens: 5,
		cachedTokens: 11008,
		totalTokens: 18055,
		...over,
	};
}

function limits(over: Partial<ConversationRateLimits> = {}): ConversationRateLimits {
	return {
		primaryUsedPercent: 71,
		secondaryUsedPercent: -1,
		primaryResetsInSeconds: 490444,
		planLabel: "pro",
		...over,
	};
}

/** The circular fill inside the gauge. */
function fill(): SVGCircleElement {
	const inner = screen.getByRole("progressbar").querySelector("circle:last-child");
	if (!inner) throw new Error("meter has no fill element");
	return inner as SVGCircleElement;
}

describe("ContextMeter", () => {
	it("reports exact accessible values with compact numbers on hover", async () => {
		render(<ContextMeter usage={usage({ cost: 0.4594, currency: "USD" })} />);
		expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "7");
		expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuetext", "18,055 / 258,400 tokens (7%)");
		await userEvent.hover(screen.getByRole("progressbar"));
		const tooltip = await screen.findByRole("tooltip");
		expect(tooltip).toHaveTextContent("18.1K / 258.4K tokens (7%)");
		expect(tooltip).not.toHaveTextContent("tokens spent in total");
		expect(tooltip).not.toHaveTextContent("Provider-reported cost");
	});

	it("encodes fullness in the circular stroke", () => {
		render(<ContextMeter usage={usage({ contextUsed: 129200 })} />);
		expect(fill()).toHaveAttribute("stroke-dasharray", "50 100");
	});

	it("keeps a nearly-empty conversation's fill visible", () => {
		render(<ContextMeter usage={usage({ contextUsed: 100 })} />);
		expect(fill()).toHaveAttribute("stroke-dasharray", "8 100");
	});

	describe("threshold colours", () => {
		it("uses the AO logo accent below 70%", () => {
			render(<ContextMeter usage={usage({ contextUsed: 172_000 })} />);
			expect(screen.getByRole("progressbar")).toHaveClass("text-logo-accent");
		});

		it("shifts to the needs-you token from 70%", () => {
			// Exactly at the boundary, which must warn rather than stay normal.
			render(<ContextMeter usage={usage({ contextUsed: 180_880 })} />);
			expect(screen.getByRole("progressbar")).toHaveClass("text-status-needs-you");
		});

		it("shifts to the exited token from 90%, where the next turn is at risk", () => {
			render(<ContextMeter usage={usage({ contextUsed: 232_560 })} />);
			expect(screen.getByRole("progressbar")).toHaveClass("text-status-exited");
		});
	});

	it("clamps a provider that overreports past its own window", () => {
		render(<ContextMeter usage={usage({ contextUsed: 300_000 })} />);
		// Full, not overflowing the track and not claiming 116%.
		expect(fill()).toHaveAttribute("stroke-dasharray", "100 100");
		expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "100");
	});

	it("shows known context without claiming a window the provider did not report", async () => {
		render(<ContextMeter usage={usage({ contextWindow: 0, contextUsed: 18_055 })} />);
		// No window means no honest fullness. Drawing an empty bar would claim a
		// conversation is roomy when it might be nearly full.
		expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
		const readout = screen.getByRole("img", { name: "Context 18,055 / unknown" });
		await userEvent.hover(readout);
		expect(await screen.findByRole("tooltip")).toHaveTextContent("Context 18.1K / unknown");
	});

	it("does not present cumulative spending as current context", () => {
		const { container } = render(<ContextMeter usage={usage({ contextWindow: 0, contextUsed: 0, totalTokens: 900 })} />);
		expect(container).toBeEmptyDOMElement();
	});

	it("does not claim a failed turn with zero reported used is 0% full", () => {
		render(<ContextMeter usage={usage({ contextUsed: 0, contextWindow: 262_144 })} />);
		expect(screen.getByRole("img", { name: "Context unknown / 262,144" })).toBeInTheDocument();
		expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
	});

	it("renders nothing before the provider has reported anything", () => {
		const { container } = render(<ContextMeter />);
		// Absent is distinct from zero: an empty meter for an unreported conversation
		// would be a claim AO has not earned.
		expect(container).toBeEmptyDOMElement();
	});

	describe("quota warning", () => {
		it("stays hidden while the account has comfortable headroom", () => {
			render(<ContextMeter usage={usage()} rateLimits={limits({ primaryUsedPercent: 20 })} />);
			// A readout that is always present becomes furniture, and this one has to be
			// noticed on the day it matters.
			expect(screen.queryByText(/quota/)).not.toBeInTheDocument();
		});

		it("appears once the account is close to a wall", () => {
			render(<ContextMeter usage={usage()} rateLimits={limits({ primaryUsedPercent: 76 })} />);
			expect(screen.getByText("76% quota")).toBeInTheDocument();
		});

		it("escalates its colour past 90%, where turns start failing", () => {
			render(<ContextMeter usage={usage()} rateLimits={limits({ primaryUsedPercent: 94 })} />);
			expect(screen.getByText("94% quota").className).toContain("text-status-exited");
		});

		it("warns on the tighter of the two windows", () => {
			render(
				<ContextMeter
					usage={usage()}
					rateLimits={limits({ primaryUsedPercent: 30, secondaryUsedPercent: 88 })}
				/>,
			);
			// The tighter window is the one that will actually stop the next turn.
			expect(screen.getByText("88% quota")).toBeInTheDocument();
		});

		it("ignores a window the provider did not report", () => {
			render(
				<ContextMeter
					usage={usage()}
					rateLimits={limits({ primaryUsedPercent: -1, secondaryUsedPercent: -1 })}
				/>,
			);
			// Negative is the daemon's "not reported". Treating it as data would draw a
			// meter running backwards.
			expect(screen.queryByText(/quota/)).not.toBeInTheDocument();
		});

		it("warns on quota even when no usage has been reported yet", () => {
			// The startup rate-limit read lands before the first turn, so this is the
			// state a user sees when opening a conversation on a nearly-spent account:
			// exactly when the warning is most useful.
			render(<ContextMeter rateLimits={limits({ primaryUsedPercent: 97 })} />);
			expect(screen.getByText("97% quota")).toBeInTheDocument();
			expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
		});
	});
});
