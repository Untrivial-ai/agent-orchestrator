import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { TopbarButton } from "./TopbarButton";

describe("TopbarButton", () => {
	it("makes icon controls square and makes disabled state unmistakable", () => {
		render(
			<TopbarButton aria-label="Unavailable action" disabled variant="icon">
				<span aria-hidden="true" />
			</TopbarButton>,
		);

		const button = screen.getByRole("button", { name: "Unavailable action" });
		expect(button).toHaveClass("size-control-md");
		expect(button).toHaveClass("active:scale-[0.96]", "focus-visible:ring-2");
		expect(button).toHaveClass("topbar-control--disabled-affordance");
		expect(button).not.toHaveClass("disabled:opacity-35");
		expect(button).not.toHaveClass("disabled:opacity-60");
		expect(button).toBeDisabled();
	});
});
