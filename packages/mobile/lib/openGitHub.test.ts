import { beforeEach, describe, expect, it, vi } from "vitest";

const { canOpenURL, openURL, openBrowserAsync, errorHaptic } = vi.hoisted(() => ({
	canOpenURL: vi.fn(),
	openURL: vi.fn(),
	openBrowserAsync: vi.fn(),
	errorHaptic: vi.fn(),
}));
vi.mock("react-native", () => ({ Linking: { canOpenURL, openURL } }));
vi.mock("expo-web-browser", () => ({ openBrowserAsync }));
vi.mock("./haptics", () => ({ haptics: { error: errorHaptic } }));

import { openGitHub } from "./openGitHub";

const PR = "https://github.com/Untrivial-ai/agent-orchestrator/pull/5648";
const PAGE = "https://example.com/docs/getting-started";

describe("openGitHub", () => {
	beforeEach(() => {
		canOpenURL.mockReset().mockResolvedValue(false);
		openURL.mockReset().mockResolvedValue(undefined);
		openBrowserAsync.mockReset().mockResolvedValue({ type: "opened" });
		errorHaptic.mockReset();
	});

	it("opens a web page in the in-app browser, never the system one", async () => {
		await openGitHub(PAGE);
		expect(openBrowserAsync).toHaveBeenCalledWith(PAGE, { createTask: false });
		expect(openURL).not.toHaveBeenCalled();
	});

	// The dependency landed in a branch whose Podfile.lock was never regenerated,
	// so the module existed in node_modules and not in the app. Importing it there
	// throws, and that used to take the screen down with a red box.
	it("uses the system browser when the build has no in-app browser", async () => {
		vi.resetModules();
		vi.doMock("expo-web-browser", () => {
			throw new Error("Cannot find native module 'ExpoWebBrowser'");
		});
		const { openGitHub: withoutBrowser } = await import("./openGitHub");
		await withoutBrowser(PAGE);
		expect(openURL).toHaveBeenCalledWith(PAGE);
		expect(errorHaptic).not.toHaveBeenCalled();
	});

	it("prefers the GitHub app for a page it has a screen for", async () => {
		canOpenURL.mockResolvedValue(true);
		await openGitHub(PR);
		expect(openURL).toHaveBeenCalledWith("github://repo/Untrivial-ai/agent-orchestrator/pull/5648");
		expect(openBrowserAsync).not.toHaveBeenCalled();
	});

	// The iOS bug report arrives as `x-safari-https://` (bugReportOpenUrl) so the
	// GitHub app's universal-link claim cannot swallow the prefilled body. The
	// in-app browser throws for anything but http(s) (WebBrowserModule.swift
	// `isValid`), so the scheme test is what keeps Report a problem out of a
	// sheet that rejects it.
	it("hands a non-web scheme to the system without trying the in-app browser", async () => {
		const report = "x-safari-https://github.com/Untrivial-ai/agent-orchestrator/issues/new?body=diag";
		await openGitHub(report);
		expect(openURL).toHaveBeenCalledWith(report);
		expect(openBrowserAsync).not.toHaveBeenCalled();
		expect(errorHaptic).not.toHaveBeenCalled();
	});

	it("falls back to the system browser when the in-app one refuses", async () => {
		openBrowserAsync.mockRejectedValue(new Error("No matching browser activity found"));
		await openGitHub(PAGE);
		expect(openBrowserAsync).toHaveBeenCalledOnce();
		expect(openURL).toHaveBeenCalledWith(PAGE);
		expect(errorHaptic).not.toHaveBeenCalled();
	});

	it("signals the failure when nothing could open the link", async () => {
		openBrowserAsync.mockRejectedValue(new Error("No matching browser activity found"));
		openURL.mockRejectedValue(new Error("Could not open URL"));
		await expect(openGitHub(PAGE)).resolves.toBeUndefined();
		expect(errorHaptic).toHaveBeenCalledOnce();
	});

	it("signals the failure when the system refuses a non-web scheme", async () => {
		openURL.mockRejectedValue(new Error("Could not open URL"));
		await expect(openGitHub("x-safari-https://github.com/x/y/issues/new")).resolves.toBeUndefined();
		expect(errorHaptic).toHaveBeenCalledOnce();
	});
});
