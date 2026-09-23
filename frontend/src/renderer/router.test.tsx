import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const routerState = vi.hoisted(() => ({ hasResolvedLocation: false }));

vi.mock("@tanstack/react-router", async (importOriginal) => ({
	...(await importOriginal<typeof import("@tanstack/react-router")>()),
	useRouterState: ({ select }: { select: (state: { resolvedLocation?: object }) => unknown }) =>
		select({ resolvedLocation: routerState.hasResolvedLocation ? {} : undefined }),
}));

vi.mock("./components/DaemonStartupLoader", () => ({
	DaemonStartupLoader: () => <div>startup screen</div>,
}));

import { AppPendingFallback } from "./router";

describe("AppPendingFallback", () => {
	beforeEach(() => {
		routerState.hasResolvedLocation = false;
	});

	it("shows startup branding during the initial route load", () => {
		render(<AppPendingFallback />);

		expect(screen.getByText("startup screen")).toBeInTheDocument();
	});

	it("does not show startup branding during later route loads", () => {
		routerState.hasResolvedLocation = true;

		render(<AppPendingFallback />);

		expect(screen.queryByText("startup screen")).not.toBeInTheDocument();
	});
});
