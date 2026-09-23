import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { SessionTopbarHost, SessionTopbarPortal, SessionTopbarProvider } from "./SessionTopbarPortal";

describe("SessionTopbarPortal", () => {
	it("renders session chrome in the shell-owned host", () => {
		render(
			<SessionTopbarProvider>
				<SessionTopbarHost data-testid="session-topbar-host" />
				<SessionTopbarPortal>
					<span>terminal tabs</span>
				</SessionTopbarPortal>
			</SessionTopbarProvider>,
		);

		const host = screen.getByTestId("session-topbar-host");
		expect(host.classList.contains("workspace-topbar-container")).toBe(true);
		expect(within(host).getByText("terminal tabs")).not.toBeNull();
	});

	it("renders inline when a component test has no shell provider", () => {
		render(
			<SessionTopbarPortal>
				<span>terminal tabs</span>
			</SessionTopbarPortal>,
		);

		expect(screen.getByText("terminal tabs")).toBeInTheDocument();
	});
});
