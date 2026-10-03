import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { ExcerptSelectionChip } from "./ExcerptSelectionChip";

it("shows a truncated selection and reveals the full text on activation", () => {
	const selection = "A selected phrase that is long enough to be clipped in the reference chip, but must remain fully available to inspect.";
	render(<ExcerptSelectionChip selection={selection} role="assistant" />);
	const summary = screen.getByLabelText(`Referenced selection: ${selection}`);
	expect(summary.textContent).not.toContain(selection);
	const full = screen.getByText(selection);
	expect(full.closest("details")?.open).toBe(false);
	fireEvent.click(summary);
	expect(full.closest("details")?.open).toBe(true);
});
