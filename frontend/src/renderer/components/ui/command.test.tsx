import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { CommandDialog } from "./command";

describe("CommandDialog", () => {
	it("keeps a non-modal palette semantically modal and focus-contained", async () => {
		const user = userEvent.setup();
		render(
			<>
				<button type="button">Outside the palette</button>
				<CommandDialog modal={false} open>
					<button type="button">First palette action</button>
					<button type="button">Last palette action</button>
				</CommandDialog>
			</>,
		);

		const dialog = screen.getByRole("dialog", { name: "Command palette" });
		expect(dialog).toHaveAttribute("aria-modal", "true");
		expect(document.querySelector('[data-slot="command-dialog-overlay"]')).toBeInTheDocument();

		for (let index = 0; index < 6; index++) {
			await user.tab();
			expect(document.activeElement).toEqual(expect.any(HTMLElement));
			expect((document.activeElement as HTMLElement).closest('[role="dialog"]')).toBe(dialog);
		}
	});
});
