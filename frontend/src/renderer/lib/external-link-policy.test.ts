import { fireEvent } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { aoBridge } from "./bridge";
import { handleModifierLinkClick, openLinkInSystemBrowser, requiresSystemBrowser } from "./external-link-policy";

describe("external link policy", () => {
	beforeEach(() => {
		document.addEventListener("click", handleModifierLinkClick);
	});

	afterEach(() => {
		document.removeEventListener("click", handleModifierLinkClick);
		document.body.replaceChildren();
		vi.restoreAllMocks();
	});

	it("opens Option/Alt-clicked anchors externally after their own handlers run", () => {
		const openExternal = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		const ownHandler = vi.fn();
		const anchor = document.body.appendChild(document.createElement("a"));
		anchor.href = "https://docs.example.com/guide";
		anchor.addEventListener("click", ownHandler);

		fireEvent.click(anchor, { altKey: true });

		expect(ownHandler).toHaveBeenCalledOnce();
		expect(openExternal).toHaveBeenCalledWith("https://docs.example.com/guide");
	});

	it("leaves plain and already-handled clicks alone", () => {
		const openExternal = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		const anchor = document.body.appendChild(document.createElement("a"));
		anchor.href = "https://docs.example.com/guide";
		anchor.addEventListener("click", (event) => event.preventDefault());

		fireEvent.click(anchor);
		fireEvent.click(anchor, { altKey: true });

		expect(openExternal).not.toHaveBeenCalled();
	});

	it("safely ignores malformed SVG anchors", () => {
		const anchor = document.createElementNS("http://www.w3.org/2000/svg", "a");
		anchor.setAttribute("href", "http://[");
		const event = {
			altKey: true,
			button: 0,
			defaultPrevented: false,
			preventDefault: vi.fn(),
			target: anchor,
		} as unknown as MouseEvent;

		expect(() => handleModifierLinkClick(event)).not.toThrow();
		expect(event.preventDefault).not.toHaveBeenCalled();
	});

	it("logs system-browser bridge failures", async () => {
		const error = new Error("IPC unavailable");
		vi.spyOn(aoBridge.app, "openExternal").mockRejectedValue(error);
		const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);

		await openLinkInSystemBrowser("https://docs.example.com");

		expect(warn).toHaveBeenCalledWith("Unable to open link in system browser", error);
	});

	it.each([
		"dash.cloudflare.com",
		"https://dash.cloudflare.com/login",
		"https://identity.dash.cloudflare.com/",
	])("routes Cloudflare dashboard URLs to the system browser", (url) => {
		expect(requiresSystemBrowser(url)).toBe(true);
	});

	it.each(["https://cloudflare.com/", "https://example.com/"])(
		"keeps unrelated URLs in the AO Browser",
		(url) => expect(requiresSystemBrowser(url)).toBe(false),
	);
});
