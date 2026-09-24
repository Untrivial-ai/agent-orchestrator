import { fireEvent } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { aoBridge } from "./bridge";
import {
	handleModifierLinkClick,
	isPotentialWorkspaceFileLink,
	isWorkspaceHtmlLink,
	openLinkInSystemBrowser,
	workspaceFilePath,
} from "./external-link-policy";

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

	it("recognizes only existing safe workspace HTML links", () => {
		expect(isWorkspaceHtmlLink("./test-ui.html", ["test-ui.html"])).toBe(true);
		expect(isWorkspaceHtmlLink("/tmp/worktree/test-ui.html", ["test-ui.html"])).toBe(true);
		expect(isWorkspaceHtmlLink("README.md", ["README.md"])).toBe(false);
		expect(isWorkspaceHtmlLink("../test-ui.html", ["../test-ui.html"])).toBe(false);
		expect(isWorkspaceHtmlLink("missing.html", ["test-ui.html"])).toBe(false);
	});

	it("resolves relative, POSIX, Windows, and file URLs to workspace files", () => {
		const paths = ["reports/final report.html", "README.md"];
		expect(workspaceFilePath("./reports/final%20report.html#results", paths)).toBe("reports/final report.html");
		expect(workspaceFilePath("/tmp/worktree/reports/final report.html", paths)).toBe("reports/final report.html");
		expect(workspaceFilePath("C:\\worktree\\reports\\final report.html", paths)).toBe("reports/final report.html");
		expect(workspaceFilePath("file:///C:/worktree/reports/final%20report.html", paths)).toBe("reports/final report.html");
	});

	it("does not suffix-match relative paths and prefers the most specific absolute match", () => {
		const paths = ["report.html", "docs/report.html"];
		expect(workspaceFilePath("docs/report.html", paths)).toBe("docs/report.html");
		expect(workspaceFilePath("other/report.html", paths)).toBeUndefined();
		expect(workspaceFilePath("/tmp/worktree/docs/report.html", paths)).toBe("docs/report.html");
	});

	it("recognizes unindexed local-looking report paths without treating schemes as files", () => {
		expect(isPotentialWorkspaceFileLink("reports/new-report.html")).toBe(true);
		expect(isPotentialWorkspaceFileLink("C:\\worktree\\reports\\new-report.html")).toBe(true);
		expect(isPotentialWorkspaceFileLink("mailto:support@example.com")).toBe(false);
		expect(isPotentialWorkspaceFileLink("javascript:alert(1)")).toBe(false);
		expect(isPotentialWorkspaceFileLink("file://[")).toBe(false);
		expect(isPotentialWorkspaceFileLink("../outside/report.html")).toBe(false);
	});
});
