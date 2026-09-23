import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { PairingQr } from "./PairingQr";

const CAPTION = "Preparing remote access — usually 30-60 seconds";

describe("PairingQr", () => {
	it("says what it is doing while the code is being prepared", () => {
		render(<PairingQr value={null} size={204} caption={CAPTION} />);
		expect(screen.getByRole("status")).toHaveTextContent(CAPTION);
	});

	// A code withheld because the connector is not advertisable must not be
	// scannable: one scanned early carries no tunnel endpoint and would pair
	// here then fail from every other network.
	it("exposes no scannable code before one exists", () => {
		const { container } = render(<PairingQr value={null} size={204} caption={CAPTION} />);
		expect(container.querySelector("[data-qr-value]")).toBeNull();
	});

	it("renders the real code once it arrives", () => {
		const { container } = render(<PairingQr value="aomobile://pair#real" size={204} caption={CAPTION} />);
		expect(container.querySelector("[data-qr-value]")).toHaveAttribute(
			"data-qr-value",
			"aomobile://pair#real",
		);
	});

	// The caption belongs to the wait. Leaving it under a finished code would
	// claim the app is still working when it is done.
	it("drops the caption once the code is shown", () => {
		render(<PairingQr value="aomobile://pair#real" size={204} caption={CAPTION} />);
		expect(screen.queryByRole("status")).toBeNull();
	});

	// The decoys stay mounted briefly so the two overlap and one dissolves into
	// the other, rather than the placeholder vanishing before the code appears.
	// Only on the transition: mounting with a code already in hand has nothing
	// to resolve from and should not fade anything in.
	it("keeps the placeholder on screen through the resolve", () => {
		const { container, rerender } = render(<PairingQr value={null} size={204} caption={CAPTION} />);
		expect(container.querySelectorAll(".ao-qr-decoy").length).toBeGreaterThan(0);

		rerender(<PairingQr value="aomobile://pair#real" size={204} caption={CAPTION} />);

		expect(container.querySelector("[data-qr-value]")).not.toBeNull();
		expect(container.querySelector(".ao-qr-decoy--settling")).not.toBeNull();
	});

	it("shows no placeholder when a code is available from the start", () => {
		const { container } = render(<PairingQr value="aomobile://pair#real" size={204} caption={CAPTION} />);
		expect(container.querySelectorAll(".ao-qr-decoy")).toHaveLength(0);
	});
});
