import { render, screen } from "@testing-library/react";
import { CancelledError, QueryClient } from "@tanstack/react-query";
import {
	createMemoryHistory,
	createRootRoute,
	createRoute,
	createRouter,
	Outlet,
	RouterProvider,
} from "@tanstack/react-router";
import { describe, expect, it, vi } from "vitest";
import { createAppRouter, RouteErrorFallback } from "../router";

function renderFallback(error: Error) {
	const reset = vi.fn();
	render(<RouteErrorFallback error={error} reset={reset} />);
	return reset;
}

describe("router error fallback", () => {
	it("is wired as the router's default error component", () => {
		const router = createAppRouter(new QueryClient());
		expect(router.options.defaultErrorComponent).toBe(RouteErrorFallback);
	});

	it("renders nothing for a cancelled query instead of the crash screen", () => {
		const reset = renderFallback(new CancelledError());
		expect(screen.queryByText("Something went wrong!")).toBeNull();
		expect(reset).not.toHaveBeenCalled();
	});

	it("renders nothing for an aborted request", () => {
		const abort = new Error("The operation was aborted.");
		abort.name = "AbortError";
		renderFallback(abort);
		expect(screen.queryByText("Something went wrong!")).toBeNull();
	});

	it("keeps the stock crash screen for real errors", () => {
		renderFallback(new Error("boom"));
		expect(screen.getByText("Something went wrong!")).toBeInTheDocument();
	});

	it("swallows a cancelled query end-to-end through the router boundary", () => {
		const rootRoute = createRootRoute({ component: () => <Outlet /> });
		const indexRoute = createRoute({
			getParentRoute: () => rootRoute,
			path: "/",
			component: () => {
				throw new CancelledError();
			},
		});
		const router = createRouter({
			routeTree: rootRoute.addChildren([indexRoute]),
			history: createMemoryHistory({ initialEntries: ["/"] }),
			defaultErrorComponent: RouteErrorFallback,
		});
		render(<RouterProvider router={router} />);
		expect(screen.queryByText("Something went wrong!")).toBeNull();
	});
});
