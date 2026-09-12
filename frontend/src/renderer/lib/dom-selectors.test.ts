import { afterEach, describe, expect, it } from "vitest";
import { hasRelevantBrowserOverlayMutation } from "./dom-selectors";

afterEach(() => document.body.replaceChildren());

function observeOneMutation(mutate: () => void): Promise<boolean> {
	return new Promise((resolve) => {
		const observer = new MutationObserver((records) => {
			observer.disconnect();
			resolve(hasRelevantBrowserOverlayMutation(records));
		});
		observer.observe(document.body, {
			attributes: true,
			attributeFilter: ["data-state"],
			childList: true,
			subtree: true,
		});
		mutate();
	});
}

describe("hasRelevantBrowserOverlayMutation", () => {
	it("ignores unrelated Radix data-state churn", async () => {
		const accordion = document.createElement("div");
		document.body.appendChild(accordion);

		expect(await observeOneMutation(() => accordion.setAttribute("data-state", "open"))).toBe(false);
	});

	it("does not restack the native page for passive tooltips", async () => {
		const tooltip = document.createElement("div");
		tooltip.setAttribute("role", "tooltip");
		tooltip.dataset.state = "delayed-open";

		expect(await observeOneMutation(() => document.body.appendChild(tooltip))).toBe(false);
	});

	it("tracks browser overlays added, removed, or flipped in place", async () => {
		const overlay = document.createElement("div");
		overlay.dataset.browserNativeOverlay = "true";
		overlay.dataset.state = "closed";

		expect(await observeOneMutation(() => document.body.appendChild(overlay))).toBe(true);
		expect(await observeOneMutation(() => overlay.setAttribute("data-state", "open"))).toBe(true);
		expect(await observeOneMutation(() => overlay.remove())).toBe(true);
	});
});
