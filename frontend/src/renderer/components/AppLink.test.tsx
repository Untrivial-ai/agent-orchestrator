import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AppBrowserLinkContext, AppLink } from "./AppLink";
import { ProductExternalLink } from "./ProductExternalLink";
import { aoBridge } from "../lib/bridge";

const url = "https://github.com/aoagents/agent-orchestrator/pull/42";
afterEach(() => vi.restoreAllMocks());

describe("AppLink", () => {
	it("opens PR links in ao even when the product link stops propagation", () => {
		const open = vi.fn();
		const external = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		render(<AppBrowserLinkContext.Provider value={open}>
			<ProductExternalLink href={url} stopPropagation>PR #42</ProductExternalLink>
		</AppBrowserLinkContext.Provider>);
		fireEvent.click(screen.getByRole("link"));
		expect(open).toHaveBeenCalledExactlyOnceWith(url);
		expect(external).not.toHaveBeenCalled();
	});

	it("offers ao, external, and copy actions without opening on right-click", async () => {
		const user = userEvent.setup();
		const open = vi.fn();
		render(<AppBrowserLinkContext.Provider value={open}><AppLink href={url}>PR</AppLink></AppBrowserLinkContext.Provider>);
		fireEvent.contextMenu(screen.getByRole("link"));
		expect(open).not.toHaveBeenCalled();
		expect(screen.getAllByRole("menuitem").map(item => item.textContent)).toEqual([
			"Open in ao browser", "Open in external browser", "Copy link",
		]);
		await user.click(screen.getByRole("menuitem", { name: "Open in ao browser" }));
		expect(open).toHaveBeenCalledExactlyOnceWith(url);
	});

	it("falls back externally and disables ao when no active browser is available", () => {
		const external = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		render(<AppLink href={url}>PR</AppLink>);
		fireEvent.click(screen.getByRole("link"));
		expect(external).toHaveBeenCalledExactlyOnceWith(url);
		fireEvent.contextMenu(screen.getByRole("link"));
		expect(screen.getByRole("menuitem", { name: "Open in ao browser" })).toHaveAttribute("aria-disabled", "true");
	});

	it("preserves handlers that cancel navigation and fragment links", () => {
		const open = vi.fn();
		const handled = vi.fn((event) => event.preventDefault());
		render(<AppBrowserLinkContext.Provider value={open}>
			<AppLink href={url} onClick={handled}>Custom</AppLink>
			<AppLink href="#section">Section</AppLink>
		</AppBrowserLinkContext.Provider>);
		fireEvent.click(screen.getByRole("link", { name: "Custom" }));
		fireEvent.click(screen.getByRole("link", { name: "Section" }));
		expect(handled).toHaveBeenCalledOnce();
		expect(open).not.toHaveBeenCalled();
	});
});
