import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { GlobalToast } from "./GlobalToast";
import { useUiStore } from "../stores/ui-store";

describe("GlobalToast", () => {
	afterEach(() => useUiStore.getState().clearGlobalToast());

	it("stacks concurrent notifications and marks errors as alerts", () => {
		const { showGlobalToast } = useUiStore.getState();
		showGlobalToast("First", "Keep this visible");
		showGlobalToast("Second", "Something failed", "error");

		render(<GlobalToast />);

		expect(screen.getAllByRole("status")).toHaveLength(1);
		expect(screen.getAllByRole("alert")).toHaveLength(1);
		expect(screen.getByText("First")).toBeInTheDocument();
		expect(screen.getByText("Second")).toBeInTheDocument();
	});

	it("keeps toast keys unique after dismissing the newest toast", () => {
		const store = useUiStore.getState();
		store.showGlobalToast("First");
		store.showGlobalToast("Second");
		store.dismissGlobalToast(2);
		store.showGlobalToast("Third");

		expect(useUiStore.getState().globalToasts.map((toast) => toast.nonce)).toEqual([1, 3]);
	});
});
